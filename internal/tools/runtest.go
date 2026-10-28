package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/proc"
)

// ErrSandboxUnavailable means the host cannot enforce the OS test boundary.
// The caller must report this as blocked and must never fall back to an
// unsandboxed shell or test process.
var ErrSandboxUnavailable = errors.New("sandbox_unavailable")

const (
	defaultTestTimeout = 120 * time.Second
	maxTestOutputBytes = 32 << 10
)

type RunTestInput struct {
	Toolchain      string `json:"toolchain"`
	Target         string `json:"target"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// TestResult is one actual host test execution. Passed is true only for exit
// code 0; timeouts and killed process groups are failures. WorkspaceSHA256 is
// the delivered-tree digest only when the bounded digest completed immediately
// before and after the test and both matched; an empty value cannot verify
// delivery. WorkspaceChanged reports that the test rewrote delivered files
// (project test code can write allowed paths even though reviewers receive no
// host write tool); such a run never qualifies as verification.
type TestResult struct {
	Toolchain        string
	Target           string
	Command          string
	ExitCode         int
	Passed           bool
	TimedOut         bool
	Truncated        bool
	DurationMS       int64
	WorkspaceSHA256  string
	WorkspaceChanged bool
	EvidenceReason   string
	Output           string
}

// RunTest executes a fixed host test command inside an OS sandbox. It accepts
// no argv beyond the bounded structure and never invokes a shell.
func RunTest(ctx context.Context, root string, in RunTestInput) (TestResult, error) {
	if err := ctx.Err(); err != nil {
		return TestResult{}, err
	}
	toolchain := strings.TrimSpace(strings.ToLower(in.Toolchain))
	if toolchain != "go" && toolchain != "npm" {
		return TestResult{}, errors.New("unsupported toolchain: only go and npm test commands are supported")
	}
	timeout := defaultTestTimeout
	if in.TimeoutSeconds != 0 {
		if in.TimeoutSeconds < 1 || in.TimeoutSeconds > 600 {
			return TestResult{}, errors.New("timeout_seconds must be between 1 and 600")
		}
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	if root == "" {
		return TestResult{}, errors.New("no source directory is bound")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return TestResult{}, errors.New("source directory is unavailable")
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return TestResult{}, errors.New("source directory is unavailable")
	}
	relTarget, absTarget, err := resolveTestTarget(resolvedRoot, in.Target)
	if err != nil {
		return TestResult{}, err
	}
	temp, err := os.MkdirTemp("", "cocogem-test-*")
	if err != nil {
		return TestResult{}, errors.New("could not create a private test directory")
	}
	defer os.RemoveAll(temp)
	if resolved, err := filepath.EvalSymlinks(temp); err == nil {
		temp = resolved
	}
	for _, name := range []string{"tmp", "home", "gocache", "gopath", "npm-cache"} {
		if err := os.Mkdir(filepath.Join(temp, name), 0700); err != nil {
			return TestResult{}, errors.New("could not create the private test workspace")
		}
	}
	spec, err := resolveToolchain(ctx, toolchain, temp)
	if err != nil {
		return TestResult{}, err
	}
	plan, err := prepareSandbox(ctx, sandboxSpec{Root: resolvedRoot, Temp: temp, Toolchain: spec})
	if err != nil {
		return TestResult{}, err
	}
	argv := append(append([]string{}, plan.Prefix...), spec.Command...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = absTarget
	if plan.Env != nil {
		cmd.Env = plan.Env
	}
	output := &boundedBuffer{limit: maxTestOutputBytes}
	cmd.Stdout, cmd.Stderr = output, output
	if err := configureTestProcess(cmd); err != nil {
		return TestResult{}, err
	}
	// The digest before the process starts is the baseline for proving that the
	// passing test ran against the tree that is delivered now.
	workspaceBefore, workspaceBeforeErr := WorkspaceDigest(resolvedRoot)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return TestResult{}, fmt.Errorf("test process could not start: %w", err)
	}
	runCtx, stop := context.WithTimeout(ctx, timeout)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-runCtx.Done():
		terminateTestGroup(cmd)
		waitErr = <-done
	}
	runErr := runCtx.Err()
	stop()
	duration := time.Since(started)
	if err := cleanupTestGroup(cmd); err != nil {
		return TestResult{}, err
	}
	exitCode := 0
	if waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			exitCode = exit.ExitCode()
		} else {
			exitCode = -1
		}
	}
	timedOut := ctx.Err() == nil && runErr == context.DeadlineExceeded && waitErr != nil
	canceled := ctx.Err() != nil
	if canceled {
		return TestResult{}, ctx.Err()
	}
	result := TestResult{Toolchain: toolchain, Target: relTarget, Command: spec.Display, ExitCode: exitCode, Passed: waitErr == nil && exitCode == 0, TimedOut: timedOut, Truncated: output.truncated, DurationMS: duration.Milliseconds()}
	workspaceAfter, workspaceAfterErr := WorkspaceDigest(resolvedRoot)
	result.testWorkspaceEvidence(workspaceBefore, workspaceAfter, workspaceBeforeErr, workspaceAfterErr)
	header := "$ " + spec.Display + "\n(cwd " + relTarget + ", timeout " + timeout.String() + ")\n"
	if timedOut {
		header += "test killed after the bounded timeout\n"
	}
	if result.WorkspaceSHA256 == "" {
		header += result.EvidenceReason + "\n"
	}
	result.Output = header + output.String()
	return result, nil
}

// testWorkspaceEvidence exposes the workspace digest only when the tree was
// identical immediately before and after the test. A test that writes delivered
// project files (source, tests or fixtures) cannot verify the current tree.
func (r *TestResult) testWorkspaceEvidence(before, after string, beforeErr, afterErr error) {
	switch {
	case beforeErr != nil || afterErr != nil:
		r.EvidenceReason = "workspace digest was unavailable before or after the test; this run cannot verify the delivered project files"
	case before != after:
		r.WorkspaceChanged = true
		r.EvidenceReason = "the test changed project files; this run cannot verify the delivered project files"
	default:
		r.WorkspaceSHA256 = after
	}
}

type toolchainSpec struct {
	Command    []string
	Display    string
	GoRoot     string
	GoModCache string
	NodePath   string
	NPMRoot    string
	Env        []string
}

// resolveToolchain derives exact executable paths and a credential-free
// environment. Host queries are trusted; the child never inherits os.Environ.
func resolveToolchain(ctx context.Context, toolchain, temp string) (toolchainSpec, error) {
	env := commonTestEnv(temp)
	switch toolchain {
	case "go":
		goPath, err := exec.LookPath("go")
		if err != nil {
			return toolchainSpec{}, errors.New("go toolchain is unavailable")
		}
		goroot, modcache := queryGoEnv(ctx, goPath, temp)
		goBin := resolveExecutable(goPath)
		if goroot != "" {
			if candidate := filepath.Join(goroot, "bin", "go"); isExecutableFile(candidate) {
				goBin = candidate
			}
		}
		if goroot == "" {
			// A local go binary without a resolved toolchain still needs its
			// own tree available to the sandbox.
			if filepath.Base(filepath.Dir(goBin)) == "bin" {
				goroot = filepath.Dir(filepath.Dir(goBin))
			}
		}
		if goroot != "" {
			if resolved, err := filepath.EvalSymlinks(goroot); err == nil {
				goroot = resolved
			}
		}
		if info, err := os.Stat(modcache); modcache == "" || err != nil || !info.IsDir() {
			modcache = filepath.Join(temp, "gomodcache")
			_ = os.Mkdir(modcache, 0700)
		} else if resolved, err := filepath.EvalSymlinks(modcache); err == nil {
			modcache = resolved
		}
		if goroot != "" {
			env = append(env, "GOROOT="+goroot)
		}
		env = append(env,
			"GOMODCACHE="+modcache,
			"GOPATH="+filepath.Join(temp, "gopath"),
			"GOCACHE="+filepath.Join(temp, "gocache"),
			"GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off",
			"GOFLAGS=-buildvcs=false", "GOWORK=off", "CGO_ENABLED=0", "GOTELEMETRY=off",
		)
		env = append(env, "PATH="+strings.Join([]string{filepath.Dir(goBin), "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"))
		return toolchainSpec{Command: []string{goBin, "test", "-count=1", "./..."}, Display: "go test -count=1 ./...", GoRoot: goroot, GoModCache: modcache, Env: env}, nil
	case "npm":
		npmPath, err := exec.LookPath("npm")
		if err != nil {
			return toolchainSpec{}, errors.New("npm is unavailable; only go test and npm test are supported")
		}
		nodePath, err := exec.LookPath("node")
		if err != nil {
			return toolchainSpec{}, errors.New("node is unavailable; only go test and npm test are supported")
		}
		nodePath = resolveExecutable(nodePath)
		npmResolved := resolveExecutable(npmPath)
		env = append(env,
			"PATH="+strings.Join([]string{filepath.Dir(nodePath), filepath.Dir(npmPath), "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
			"npm_config_cache="+filepath.Join(temp, "npm-cache"),
			"npm_config_offline=true", "npm_config_audit=false", "npm_config_fund=false",
			"npm_config_update_notifier=false", "npm_config_progress=false",
		)
		return toolchainSpec{Command: []string{npmPath, "test", "--silent"}, Display: "npm test --silent", NodePath: nodePath, NPMRoot: npmPackageRoot(npmResolved), Env: env}, nil
	}
	return toolchainSpec{}, errors.New("unsupported toolchain")
}

func commonTestEnv(temp string) []string {
	return []string{
		"HOME=" + filepath.Join(temp, "home"),
		"TMPDIR=" + filepath.Join(temp, "tmp"),
		"TMP=" + filepath.Join(temp, "tmp"),
		"TEMP=" + filepath.Join(temp, "tmp"),
		"LANG=C", "LC_ALL=C", "TZ=UTC", "CI=1",
	}
}

// queryGoEnv resolves the local toolchain without inheriting the server
// environment, project env files, workspace hooks or network settings. The
// query runs in the private temp directory with a bounded timeout, so a broken
// host configuration cannot hang test evidence or fetch another toolchain.
func queryGoEnv(ctx context.Context, goPath, temp string) (string, string) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(queryCtx, goPath, "env", "GOROOT", "GOMODCACHE")
	// A hung toolchain process must not keep the evidence path open through an
	// inherited pipe after the context deadline kills it.
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = temp
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + filepath.Join(temp, "home"),
		"TMPDIR=" + filepath.Join(temp, "tmp"),
		"GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off",
		"GOWORK=off", "GOFLAGS=", "GO111MODULE=",
		"LANG=C", "LC_ALL=C",
	}
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "", ""
	}
	goroot := strings.TrimSpace(lines[0])
	modcache := strings.TrimSpace(lines[1])
	if goroot == "" {
		return "", ""
	}
	return filepath.Clean(goroot), filepath.Clean(modcache)
}

func resolveExecutable(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func npmPackageRoot(resolved string) string {
	marker := string(filepath.Separator) + filepath.Join("node_modules", "npm")
	if index := strings.Index(resolved, marker); index >= 0 {
		return resolved[:index+len(marker)]
	}
	dir := filepath.Dir(resolved)
	for range 5 {
		if info, err := os.Stat(filepath.Join(dir, "package.json")); err == nil && info.Mode().IsRegular() {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return filepath.Dir(resolved)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0
}

// resolveTestTarget confines the target to an existing real directory below root.
func resolveTestTarget(root, target string) (string, string, error) {
	if target == "" {
		target = "."
	}
	if len(target) > 512 || !filepath.IsLocal(target) || DeniedPath(target) {
		return "", "", errors.New("test target is outside the allowed project scope")
	}
	local := filepath.Clean(target)
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", "", errors.New("source directory is unavailable")
	}
	defer r.Close()
	current := ""
	parts := []string{}
	if local != "." {
		parts = strings.Split(local, string(filepath.Separator))
	}
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := r.Lstat(current)
		if err != nil {
			return "", "", errors.New("test target directory is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("symbolic links are not supported")
		}
		if !info.IsDir() {
			return "", "", errors.New("test target is not a directory")
		}
	}
	return filepath.ToSlash(local), filepath.Join(root, local), nil
}

func cleanupTestGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	members, err := proc.GroupMembers(pgid)
	if err != nil {
		return fmt.Errorf("could not verify the test process group: %w", err)
	}
	if len(members) == 0 {
		return nil
	}
	terminateTestGroup(cmd)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		members, err = proc.GroupMembers(pgid)
		if err != nil {
			return fmt.Errorf("could not verify the test process group: %w", err)
		}
		if len(members) == 0 {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("test process group %d still has live members %v; result is uncertain", pgid, members)
}

// boundedBuffer keeps the first bytes of combined output for evidence.
type boundedBuffer struct {
	mu        sync.Mutex
	buf       strings.Builder
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sandboxSpec and sandboxPlan are filled by the platform-specific sandbox
// implementation. A nil error means the OS boundary is actually available.
type sandboxSpec struct {
	Root      string
	Temp      string
	Toolchain toolchainSpec
}

type sandboxPlan struct {
	Prefix []string
	Env    []string
}
