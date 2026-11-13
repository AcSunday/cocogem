package nativecli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// slowLaunchCLI is a Codex-shaped fixture whose --version answer is delayed, the
// way the OS delays the first launch of a CLI binary that was just installed
// or updated while it verifies it.
func slowLaunchCLI(t *testing.T, delay time.Duration) func(string, ...string) *exec.Cmd {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n--version) sleep %.1f; printf 'codex-cli 0.164.0\\n' ;;\n*) printf '%%s\\n' '--ignore-user-config --ignore-rules --ephemeral --sandbox --json' ;;\nesac\n", delay.Seconds())
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return func(_ string, args ...string) *exec.Cmd { return exec.Command(path, args...) }
}

// Measured on macOS: the first launch of a freshly written 344 MB Claude Code
// binary took 4.3–4.8 s on an idle machine and is slower under load, so a member
// dispatched right after its CLI updated itself was blocked by a 5 s probe.
func TestCapabilityProbeAcceptsCLIWhoseFirstLaunchTakesSixSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a six-second launch")
	}
	t.Parallel()
	info, err := check(t.Context(), "codex", slowLaunchCLI(t, 6*time.Second))
	if err != nil || !info.Supported {
		t.Fatalf("a slow first launch blocked the CLI: supported=%v reason=%q err=%v", info.Supported, info.Reason, err)
	}
}

// A CLI that does not answer inside the window is stopped at the window and the
// reason says so, instead of looking like an incompatible installation.
func TestCapabilityProbeReportsTimeoutAsItsOwnReason(t *testing.T) {
	t.Parallel()
	started := time.Now()
	info, err := checkWithin(t.Context(), "codex", slowLaunchCLI(t, 30*time.Second), 300*time.Millisecond)
	if err == nil || info.Supported {
		t.Fatalf("a CLI that never answered was accepted: %+v", info)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the probe outlived its window by %s", elapsed)
	}
	if !strings.Contains(info.Reason, "超时") || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("a timeout was reported as %q / %v", info.Reason, err)
	}
	if _, _, probeErr := probeCLI(t.Context(), "codex", slowLaunchCLI(t, 30*time.Second), 300*time.Millisecond); !errors.Is(probeErr, errProbeTimeout) {
		t.Fatalf("probe error = %v, want the timeout sentinel", probeErr)
	}
}

// A CLI that answers with a failure is still a failed probe, not a timeout.
func TestCapabilityProbeKeepsFailureDistinctFromTimeout(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	info, err := check(t.Context(), "codex", func(_ string, args ...string) *exec.Cmd { return exec.Command(path, args...) })
	if err == nil || info.Supported || strings.Contains(info.Reason, "超时") {
		t.Fatalf("a failing CLI was reported as %q / %v", info.Reason, err)
	}
}
