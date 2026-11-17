package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Test toolchains accepted by the host.// Test toolchains accepted by the host. "auto" picks the first toolchain whose
// project manifest exists. Each one is resolved to a real executable path; the
// host never passes the request through a shell.
const (
	ToolchainAuto   = "auto"
	ToolchainGo     = "go"
	ToolchainNPM    = "npm"
	ToolchainPNPM   = "pnpm"
	ToolchainYarn   = "yarn"
	ToolchainPytest = "pytest"
	ToolchainCargo  = "cargo"
)

// Test kinds. A "test" run is the only kind that can verify a candidate; a
// typecheck, lint or build run is supporting evidence, never proof of behaviour.
const (
	TestKindTest      = "test"
	TestKindTypecheck = "typecheck"
	TestKindLint      = "lint"
	TestKindBuild     = "build"
)

// testFilterPattern keeps the filter to a single test selector, so it cannot be
// read as another flag or a shell fragment.
var testFilterPattern = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,240}$`)

// normalizeTestInput validates the enum and derives the exact argv. It never
// accepts an arbitrary command.
func normalizeTestInput(in RunTestInput) (RunTestInput, error) {
	toolchain := strings.TrimSpace(strings.ToLower(in.Toolchain))
	if toolchain == "" {
		toolchain = ToolchainGo
	}
	switch toolchain {
	case ToolchainAuto, ToolchainGo, ToolchainNPM, ToolchainPNPM, ToolchainYarn, ToolchainPytest, ToolchainCargo:
	default:
		return in, errors.New("unsupported toolchain: use auto, go, npm, pnpm, yarn, pytest or cargo")
	}
	kind := strings.TrimSpace(strings.ToLower(in.Kind))
	if kind == "" {
		kind = TestKindTest
	}
	switch kind {
	case TestKindTest, TestKindTypecheck, TestKindLint, TestKindBuild:
	default:
		return in, errors.New("unsupported test kind: use test, typecheck, lint or build")
	}
	filter := strings.TrimSpace(in.Filter)
	if filter != "" && !testFilterPattern.MatchString(filter) {
		return in, errors.New("test filter must match ^[A-Za-z0-9_./:-]+$ and be at most 240 characters")
	}
	if in.TimeoutSeconds != 0 && (in.TimeoutSeconds < 1 || in.TimeoutSeconds > 600) {
		return in, errors.New("timeout_seconds must be between 1 and 600")
	}
	in.Toolchain, in.Kind, in.Filter = toolchain, kind, filter
	return in, nil
}

// detectToolchain resolves "auto" from one project manifest in the target.
func detectToolchain(root, target string) (string, error) {
	dir := target
	if dir == "" {
		dir = "."
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, dir, name))
		return err == nil
	}
	switch {
	case exists("go.mod"):
		return ToolchainGo, nil
	case exists("pnpm-lock.yaml"):
		return ToolchainPNPM, nil
	case exists("yarn.lock"):
		return ToolchainYarn, nil
	case exists("package.json"):
		return ToolchainNPM, nil
	case exists("pyproject.toml"), exists("pytest.ini"), exists("setup.cfg"), exists("tox.ini"):
		return ToolchainPytest, nil
	case exists("Cargo.toml"):
		return ToolchainCargo, nil
	}
	return "", errors.New("no project manifest was found for toolchain=auto; pass an explicit toolchain")
}

// toolchainCommand composes the exact argv for one toolchain and kind. It is the
// only place a project toolchain command is derived, and every branch is a
// fixed shape with a validated target and filter.
func toolchainCommand(kind, toolchain, filter string) ([]string, error) {
	if toolchain == ToolchainGo {
		switch kind {
		case TestKindTest:
			args := []string{"test", "-count=1"}
			if filter != "" {
				// The filter is either a package path or a run expression.
				if strings.Contains(filter, "/") && !strings.ContainsAny(filter, "^$*[]()|+?") {
					args = append(args, "./"+strings.TrimPrefix(filter, "./"))
				} else {
					args = append(args, "-run", filter, "./...")
				}
			} else {
				args = append(args, "./...")
			}
			return args, nil
		case TestKindBuild:
			return []string{"build", "./..."}, nil
		case TestKindLint:
			return []string{"vet", "./..."}, nil
		case TestKindTypecheck:
			return []string{"build", "./..."}, nil
		}
	}
	if toolchain == ToolchainNPM || toolchain == ToolchainPNPM || toolchain == ToolchainYarn {
		command := "test"
		switch kind {
		case TestKindTypecheck:
			command = "typecheck"
		case TestKindLint:
			command = "lint"
		case TestKindBuild:
			command = "build"
		}
		if filter != "" && kind == TestKindTest {
			// A package manager test script plus a bounded filter is passed as
			// one positional argument; the host still never interpolates it into
			// a shell.
			return []string{"run", command, "--", filter}, nil
		}
		return []string{"run", command}, nil
	}
	if toolchain == ToolchainPytest {
		switch kind {
		case TestKindTest:
			args := []string{"-q"}
			if filter != "" {
				args = append(args, "-k", filter)
			}
			return args, nil
		case TestKindTypecheck:
			return []string{"-q", "--collect-only"}, nil
		case TestKindBuild:
			return []string{"-q", "--collect-only"}, nil
		case TestKindLint:
			return nil, errors.New("lint is not a pytest capability; use the project's own linter through run_command with an approval")
		}
	}
	if toolchain == ToolchainCargo {
		switch kind {
		case TestKindTest:
			args := []string{"test"}
			if filter != "" {
				args = append(args, filter)
			}
			return args, nil
		case TestKindTypecheck, TestKindBuild:
			return []string{"check"}, nil
		case TestKindLint:
			return []string{"clippy", "--all-targets"}, nil
		}
	}
	return nil, errors.New("unsupported toolchain")
}

// prepareToolchain resolves the executable, root and environment for one
// toolchain. The child never inherits the server environment.
type toolchainSpec struct {
	Command    []string
	Display    string
	GoRoot     string
	GoModCache string
	// ExecRoots is the directories the sandbox must execute from, such as the
	// toolchain's own bin directory.
	ExecRoots  []string
	ReadRoots  []string
	NodePath   string
	NPMRoot    string
	PytestRoot string
	CargoRoot  string
	Env        []string
}

// resolveTestToolchain maps one validated input onto a real toolchain.
func resolveTestToolchain(ctx context.Context, toolchain, target, filter string, temp string) (toolchainSpec, error) {
	if toolchain == ToolchainGo {
		return resolveGoToolchain(ctx, temp)
	}
	if toolchain == ToolchainNPM || toolchain == ToolchainPNPM || toolchain == ToolchainYarn {
		return resolveNodeToolchain(ctx, toolchain, target, filter, temp)
	}
	if toolchain == ToolchainPytest {
		return resolvePytestToolchain(ctx, target, filter, temp)
	}
	if toolchain == ToolchainCargo {
		return resolveCargoToolchain(ctx, target, filter, temp)
	}
	return toolchainSpec{}, errors.New("unsupported toolchain")
}

func resolveGoToolchain(ctx context.Context, temp string) (toolchainSpec, error) {
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
	env := commonTestEnv(temp)
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
	return toolchainSpec{
		// The argv's fixed prefix is just the executable; RunTest appends the
		// exact subcommand and filter from the validated enum.
		Command: []string{goBin}, Display: "go test -count=1",
		GoRoot: goroot, GoModCache: modcache, Env: env,
	}, nil
}

func resolveNodeToolchain(ctx context.Context, toolchain, target, filter, temp string) (toolchainSpec, error) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return toolchainSpec{}, errors.New("node is unavailable")
	}
	nodePath = resolveNodeBinary(nodePath)
	if nodePath == "" {
		return toolchainSpec{}, errors.New("a native node executable could not be resolved safely")
	}
	switch toolchain {
	case ToolchainPNPM:
		managerPath, err := exec.LookPath("pnpm")
		if err != nil {
			return toolchainSpec{}, errors.New("pnpm is not installed")
		}
		root := resolvePackageManagerRoot(managerPath, "pnpm")
		return toolchainSpec{
			Command: []string{managerPath, "run", "test"},
			Display: "pnpm run test",
			Env:     nodeTestEnv(temp, nodePath, managerPath, "pnpm"),
			ExecRoots: []string{
				filepath.Dir(nodePath), filepath.Dir(managerPath), root,
			},
		}, nil
	case ToolchainYarn:
		managerPath, err := exec.LookPath("yarn")
		if err != nil {
			return toolchainSpec{}, errors.New("yarn is not installed")
		}
		root := resolvePackageManagerRoot(managerPath, "yarn")
		return toolchainSpec{
			Command: []string{managerPath, "test"},
			Display: "yarn test",
			Env:     nodeTestEnv(temp, nodePath, managerPath, "yarn"),
			ExecRoots: []string{
				filepath.Dir(nodePath), filepath.Dir(managerPath), root,
			},
		}, nil
	}
	npmPath, err := exec.LookPath("npm")
	if err != nil {
		return toolchainSpec{}, errors.New("npm is unavailable")
	}
	npmResolved := resolveExecutable(npmPath)
	command, npmRoot, err := resolveNPMCommand(ctx, npmPath, npmResolved, nodePath, temp)
	if err != nil {
		return toolchainSpec{}, err
	}
	return toolchainSpec{Command: command, Display: "npm test --silent", NodePath: nodePath, NPMRoot: npmRoot, Env: nodeTestEnv(temp, nodePath, npmPath, "npm"),
		ExecRoots: []string{filepath.Dir(nodePath), filepath.Dir(npmPath), npmRoot}}, nil
}

func nodeTestEnv(temp, nodePath, managerPath, manager string) []string {
	env := commonTestEnv(temp)
	env = append(env,
		"PATH="+strings.Join([]string{filepath.Dir(nodePath), filepath.Dir(managerPath), "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
		"npm_config_cache="+filepath.Join(temp, "npm-cache"),
		"npm_config_offline=true", "npm_config_audit=false", "npm_config_fund=false",
		"npm_config_update_notifier=false", "npm_config_progress=false",
	)
	if manager == "pnpm" {
		env = append(env, "PNPM_HOME="+filepath.Join(temp, "pnpm-home"), "XDG_CACHE_HOME="+filepath.Join(temp, "cache"))
	}
	if manager == "yarn" {
		env = append(env, "YARN_CACHE_FOLDER="+filepath.Join(temp, "yarn-cache"), "YARN_ENABLE_TELEMETRY=false")
	}
	return env
}

// resolvePackageManagerRoot finds the package-manager installation root so the
// sandbox can execute its JS entry points without allowing the user's home.
func resolvePackageManagerRoot(path, name string) string {
	resolved := resolveExecutable(path)
	marker := string(filepath.Separator) + filepath.Join("node_modules", name)
	if index := strings.Index(resolved, marker); index >= 0 {
		return resolved[:index+len(marker)]
	}
	return filepath.Dir(filepath.Dir(resolved))
}

func resolvePytestToolchain(ctx context.Context, target, filter, temp string) (toolchainSpec, error) {
	pytestPath, err := exec.LookPath("pytest")
	if err != nil {
		return toolchainSpec{}, errors.New("pytest is not installed")
	}
	// pytest is a Python console script; run it with its own interpreter so the
	// sandbox executes real bytes rather than a PATH shim.
	interpreter := pythonForScript(pytestPath)
	if interpreter == "" {
		return toolchainSpec{}, errors.New("pytest could not be bound to a Python interpreter")
	}
	env := commonTestEnv(temp)
	env = append(env,
		"PATH="+strings.Join([]string{filepath.Dir(interpreter), "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
		"PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1", "PYTHONHASHSEED=0", "PYTEST_DISABLE_PLUGIN_AUTOLOAD=1",
	)
	args := []string{"-q"}
	if filter != "" {
		args = append(args, "-k", filter)
	}
	roots := []string{filepath.Dir(interpreter), filepath.Dir(pytestPath)}
	if root := pythonPackageRoot(pytestPath); root != "" {
		roots = append(roots, root)
	}
	return toolchainSpec{
		Command: append([]string{interpreter, pytestPath}, args...),
		Display: "pytest " + strings.Join(args, " "),
		Env:     env, ExecRoots: roots,
	}, nil
}

func resolveCargoToolchain(ctx context.Context, target, filter, temp string) (toolchainSpec, error) {
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		return toolchainSpec{}, errors.New("cargo is not installed")
	}
	cargoRoot := filepath.Dir(filepath.Dir(resolveExecutable(cargoPath)))
	env := commonTestEnv(temp)
	env = append(env,
		"PATH="+strings.Join([]string{filepath.Dir(cargoPath), "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
		"CARGO_HOME="+filepath.Join(temp, "cargo-home"),
		"RUSTUP_HOME="+filepath.Join(temp, "rustup-home"),
		"CARGO_NET_OFFLINE=true", "CARGO_TERM_COLOR=never", "CARGO_INCREMENTAL=0",
	)
	args := []string{"test"}
	if filter != "" {
		args = append(args, filter)
	}
	return toolchainSpec{
		Command: append([]string{resolveExecutable(cargoPath)}, args...),
		Display: "cargo " + strings.Join(args, " "),
		Env:     env, ExecRoots: []string{filepath.Dir(cargoPath), cargoRoot},
	}, nil
}

// pythonForScript resolves the interpreter referenced by a console-script
// shebang, or the interpreter shipped beside the script.
func pythonForScript(path string) string {
	resolved := resolveExecutable(path)
	data, err := os.ReadFile(resolved)
	if err == nil && len(data) > 2 {
		line := strings.SplitN(string(data), "\n", 2)[0]
		if strings.HasPrefix(line, "#!") {
			fields := strings.Fields(strings.TrimPrefix(line, "#!"))
			if len(fields) >= 2 && fields[0] == "/usr/bin/env" {
				if candidate, err := exec.LookPath(fields[1]); err == nil {
					return resolveExecutable(candidate)
				}
			} else if len(fields) >= 1 && filepath.IsAbs(fields[0]) {
				return resolveExecutable(fields[0])
			}
		}
	}
	if candidate, err := exec.LookPath("python3"); err == nil {
		return resolveExecutable(candidate)
	}
	return ""
}

// pythonPackageRoot keeps the pytest package importable inside the sandbox.
func pythonPackageRoot(script string) string {
	dir := filepath.Dir(resolveExecutable(script))
	for range 3 {
		if info, err := os.Stat(filepath.Join(dir, "site-packages")); err == nil && info.IsDir() {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

// displayCommand is the bounded, secret-free command line recorded as evidence.
// It is derived from the exact argv rather than from user text.
func displayCommand(spec toolchainSpec) string {
	// The argv is the evidence, so the display always matches the exact bytes
	// that ran, including a filter that was appended after resolution.
	if len(spec.Command) == 0 {
		return spec.Display
	}
	parts := append([]string{}, spec.Command...)
	// A console-script interpreter path is unreadable evidence; show the script.
	if strings.Contains(filepath.Base(parts[0]), "python") && len(parts) > 1 {
		parts = parts[1:]
		parts[0] = filepath.Base(parts[0])
	} else {
		parts[0] = filepath.Base(parts[0])
	}
	return strings.Join(parts, " ")
}
