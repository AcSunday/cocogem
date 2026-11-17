package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	Kind           string `json:"kind"`
	Target         string `json:"target"`
	Filter         string `json:"filter"`
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
	Kind             string
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
	Filter           string
	Output           string
}

// RunTest executes a host-composed test command inside an OS sandbox. It accepts
// only a validated enum plus a bounded target and filter, never an argv, and
// never invokes a shell.
func RunTest(ctx context.Context, root string, in RunTestInput) (TestResult, error) {
	if err := ctx.Err(); err != nil {
		return TestResult{}, err
	}
	in, err := normalizeTestInput(in)
	if err != nil {
		return TestResult{}, err
	}
	timeout := defaultTestTimeout
	if in.TimeoutSeconds != 0 {
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
	toolchain := in.Toolchain
	if toolchain == ToolchainAuto {
		// Resolve auto against the concrete target so a monorepo package with a
		// different manifest is detected honestly.
		toolchain, err = detectToolchain(resolvedRoot, relTarget)
		if err != nil {
			return TestResult{}, err
		}
	}
	command, err := toolchainCommand(in.Kind, toolchain, in.Filter)
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
	for _, name := range []string{"tmp", "home", "gocache", "gopath", "npm-cache", "cache", "pnpm-home", "yarn-cache", "cargo-home", "rustup-home"} {
		if err := os.Mkdir(filepath.Join(temp, name), 0700); err != nil {
			return TestResult{}, errors.New("could not create the private test workspace")
		}
	}
	// The resolved toolchain command replaces the fixed shape above; the enum
	// only selects the command family and never the bytes.
	spec, err := resolveTestToolchain(ctx, toolchain, relTarget, in.Filter, temp)
	if err != nil {
		return TestResult{}, err
	}
	// The resolver chooses the real executable; the host-composed enum argv
	// supplies the subcommand, kind and validated filter. A package manager
	// owns its own entry point, so only its script arguments are appended.
	switch {
	case len(spec.Command) == 0:
		spec.Command = command
	case toolchain == ToolchainGo || toolchain == ToolchainCargo:
		spec.Command = append(spec.Command, command...)
	case toolchain == ToolchainPytest:
		// spec.Command is the interpreter plus the pytest entry point; the
		// pytest arguments (including -k filter) come from the enum.
		spec.Command = append(spec.Command, command...)
	case toolchain == ToolchainNPM || toolchain == ToolchainPNPM || toolchain == ToolchainYarn:
		spec.Command = append(spec.Command, command[2:]...)
	}
	spec.Display = displayCommand(spec)
	plan, err := prepareSandbox(ctx, sandboxSpec{Root: resolvedRoot, Temp: temp, Toolchain: spec})
	if err != nil {
		return TestResult{}, err
	}
	argv := append(append([]string{}, plan.Prefix...), spec.Command...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = absTarget
	if plan.Env != nil {
		cmd.Env = plan.Env
	} else {
		cmd.Env = spec.Env
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
	result := TestResult{
		Toolchain: toolchain, Kind: in.Kind, Target: relTarget, Filter: in.Filter,
		Command: displayCommand(spec), ExitCode: exitCode,
		Passed: waitErr == nil && exitCode == 0, TimedOut: timedOut,
		Truncated: output.truncated, DurationMS: duration.Milliseconds(),
	}
	workspaceAfter, workspaceAfterErr := WorkspaceDigest(resolvedRoot)
	result.testWorkspaceEvidence(workspaceBefore, workspaceAfter, workspaceBeforeErr, workspaceAfterErr)
	header := "$ " + displayCommand(spec) + "\n(cwd " + relTarget + ", timeout " + timeout.String() + ")\n"
	if in.Filter != "" {
		header += "filter " + in.Filter + "\n"
	}
	if kindNote := kindEvidenceNote(in.Kind); kindNote != "" {
		header += kindNote + "\n"
	}
	if timedOut {
		header += "test killed after the bounded timeout\n"
	}
	if result.WorkspaceSHA256 == "" {
		header += result.EvidenceReason + "\n"
	}
	result.Output = header + output.String()
	return result, nil
}

// kindEvidenceNote states what a non-test kind can prove. A typecheck, lint or
// build run is supporting evidence; only an actual test verifies behaviour.
func kindEvidenceNote(kind string) string {
	switch kind {
	case TestKindTypecheck, TestKindLint, TestKindBuild:
		return "this kind is supporting evidence only; it does not verify runtime behaviour"
	}
	return ""
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

// resolveNPMCommand avoids executing an arbitrary PATH shim inside the sandbox.
// The host resolves npm's own package entry point, then Node executes that exact
// script with the same credential-free environment as the rest of the test.
func resolveNPMCommand(ctx context.Context, npmPath, npmResolved, nodePath, temp string) ([]string, string, error) {
	roots := []string{npmPackageRoot(npmResolved)}
	// Agent launchers and version managers often put a shell shim before the
	// actual installation. Inspect other configured PATH entries on the host,
	// but never execute those shims inside the sandbox.
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(directory, "npm")
		if candidate == npmPath {
			continue
		}
		resolved := resolveExecutable(candidate)
		if resolved == npmResolved {
			continue
		}
		roots = append(roots, npmPackageRoot(resolved))
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(queryCtx, npmPath, "root", "-g")
	cmd.Dir = temp
	cmd.Env = append(commonTestEnv(temp), "PATH="+strings.Join([]string{filepath.Dir(nodePath), filepath.Dir(npmPath), "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin"}, ":"))
	if output, err := cmd.Output(); err == nil {
		roots = append([]string{filepath.Join(strings.TrimSpace(string(output)), "npm")}, roots...)
	}
	for _, root := range roots {
		if root == "" || root == "." || root == string(filepath.Separator) {
			continue
		}
		entry := filepath.Join(root, "bin", "npm-cli.js")
		info, err := os.Stat(entry)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		return []string{nodePath, entry, "test", "--silent"}, root, nil
	}
	return nil, "", errors.New("npm CLI package could not be resolved safely")
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

func resolveNodeBinary(preferred string) string {
	candidates := []string{preferred}
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		candidates = append(candidates, filepath.Join(directory, "node"))
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		resolved := resolveExecutable(candidate)
		if seen[resolved] || !isExecutableFile(resolved) {
			continue
		}
		seen[resolved] = true
		file, err := os.Open(resolved)
		if err != nil {
			continue
		}
		prefix := make([]byte, 2)
		n, readErr := io.ReadFull(file, prefix)
		closeErr := file.Close()
		if readErr == nil && closeErr == nil && n == 2 && string(prefix) != "#!" {
			return resolved
		}
	}
	return ""
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
	// ExtraWrites are exact approved outside roots a project-autonomous command
	// may write, plus this scope's IO outputs directory.
	ExtraWrites []string
	// ExtraReads are exact outside roots a command may read but never write,
	// such as this scope's read-only attachment inputs.
	ExtraReads []string
	// DenyWrites are exact paths that stay denied even when a parent subtree is
	// writable, such as the source repository's hooks and branch refs.
	DenyWrites []string
}

type sandboxPlan struct {
	Prefix []string
	Env    []string
}
