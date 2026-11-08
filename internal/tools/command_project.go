package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// executeProjectCommand runs one argv under the OS sandbox with writes confined to
// the project's working directory (and any explicitly approved scope), reads
// limited to the runtime trees plus those roots, and secret paths denied. The
// argv itself is never passed through a shell.
func executeProjectCommand(ctx context.Context, raw json.RawMessage, scope Scope) (string, error) {
	var in struct {
		Argv    []string `json:"argv"`
		CWD     string   `json:"cwd"`
		Timeout int      `json:"timeout_seconds"`
	}
	if err := Decode(raw, &in); err != nil {
		return "", err
	}
	if len(in.Argv) == 0 || len(in.Argv) > 128 || in.Argv[0] == "" {
		return "", errors.New("argv must contain an executable and at most 128 arguments")
	}
	for _, arg := range in.Argv {
		if strings.ContainsRune(arg, 0) {
			return "", errors.New("command arguments cannot contain NUL")
		}
	}
	if in.Timeout == 0 {
		in.Timeout = 120
	}
	if in.Timeout < 1 || in.Timeout > 600 {
		return "", errors.New("command timeout must be between 1 and 600 seconds")
	}
	workDir := scope.WorkDir
	if workDir == "" {
		workDir = scope.Root
	}
	if workDir == "" {
		return "", errors.New("no task working directory is bound")
	}
	r, local, err := openPath(workDir, in.CWD)
	if err != nil {
		return "", err
	}
	r.Close()
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return "", errors.New("task working directory is unavailable")
	}
	// Seatbelt matches real paths; a /var -> /private/var temp dir must be
	// resolved or the cwd itself is denied.
	if resolved, err := filepath.EvalSymlinks(absWork); err == nil {
		absWork = resolved
	}
	temp, err := os.MkdirTemp("", "cocogem-command-*")
	if err != nil {
		return "", errors.New("could not create a private command directory")
	}
	defer os.RemoveAll(temp)
	if resolved, err := filepath.EvalSymlinks(temp); err == nil {
		temp = resolved
	}
	spec := sandboxSpec{Root: absWork, Temp: temp, Toolchain: toolchainSpec{Command: in.Argv, Display: strings.Join(in.Argv, " "), Env: commandEnv(temp)}}
	for _, approved := range scope.ApprovedRoots {
		// An approved outside write target widens exactly that root, resolved to
		// its real path like every other sandbox path.
		if resolved, err := filepath.EvalSymlinks(approved); err == nil {
			spec.ExtraWrites = append(spec.ExtraWrites, resolved)
		} else {
			spec.ExtraWrites = append(spec.ExtraWrites, approved)
		}
	}
	plan, err := prepareCommandSandbox(ctx, spec)
	if err != nil {
		return "", err
	}
	argv := append(append([]string{}, plan.Prefix...), in.Argv...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = filepath.Join(absWork, local)
	cmd.Env = spec.Toolchain.Env
	output := &boundedBuffer{limit: maxTestOutputBytes}
	cmd.Stdout, cmd.Stderr = output, output
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(in.Timeout)*time.Second)
	defer cancel()
	if err := cmd.Start(); err != nil {
		return "", errors.New("command could not start")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-runCtx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		waitErr = <-done
	}
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	record := struct {
		Argv     []string `json:"argv"`
		CWD      string   `json:"cwd"`
		ExitCode int      `json:"exit_code"`
		Output   string   `json:"output"`
		Duration int64    `json:"duration_ms"`
	}{Argv: in.Argv, CWD: filepath.Join(absWork, local), ExitCode: exitCode, Output: output.String(), Duration: time.Since(started).Milliseconds()}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", errors.New("could not encode the command receipt")
	}
	if waitErr != nil {
		return string(encoded), errors.New("command failed with exit code " + itoa(exitCode))
	}
	return string(encoded), nil
}

// commandEnv is a credential-free environment for a sandboxed project command.
func commandEnv(temp string) []string {
	return []string{
		"PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + temp, "TMPDIR=" + temp,
		"GOCACHE=" + filepath.Join(temp, "gocache"), "GOPATH=" + filepath.Join(temp, "gopath"),
		"npm_config_cache=" + filepath.Join(temp, "npm-cache"),
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
