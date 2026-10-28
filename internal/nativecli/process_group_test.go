//go:build darwin || linux

package nativecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/proc"
)

const (
	processHelperEnv   = "COCOGEM_PROCESS_HELPER"
	processModeEnv     = "COCOGEM_PROCESS_MODE"
	processChildEnv    = "COCOGEM_PROCESS_CHILD_PID"
	processInitFrame   = `{"type":"system","subtype":"init","session_id":"12345678-1234-4234-8234-123456789abc","tools":[]}`
	processResultFrame = `{"type":"result","subtype":"success","is_error":false}`
	processAuthFrame   = `{"type":"error","error":{"type":"authentication_error","message":"invalid key"}}`
)

// TestNativeProcessHelper is a synthetic native CLI. Modes either leave a
// child behind with detached output or wait to be canceled.
func TestNativeProcessHelper(t *testing.T) {
	if os.Getenv(processHelperEnv) != "1" {
		return
	}
	pidFile := os.Getenv(processChildEnv)
	spawn := func() {
		child := exec.Command("sleep", "30")
		// Nil files detach stdio; the child keeps only the inherited group.
		if err := child.Start(); err != nil {
			os.Exit(41)
		}
		if pidFile != "" {
			// Rename keeps the parent from reading a partial pid.
			tmp := pidFile + ".tmp"
			if os.WriteFile(tmp, []byte(strconv.Itoa(child.Process.Pid)), 0600) == nil {
				_ = os.Rename(tmp, pidFile)
			}
		}
	}
	frames := func(text string) {
		if _, err := os.Stdout.WriteString(text); err != nil {
			os.Exit(42)
		}
	}
	switch os.Getenv(processModeEnv) {
	case "success_child":
		spawn()
		frames(processInitFrame + "\n" + processResultFrame + "\n")
		os.Exit(0)
	case "cancel_child":
		spawn()
		frames(processInitFrame + "\n")
		time.Sleep(30 * time.Second)
	case "auth_cancel":
		// One write keeps both frames available even if cancellation kills
		// this process immediately after the test sees the session event.
		frames(processInitFrame + "\n" + processAuthFrame + "\n")
		time.Sleep(30 * time.Second)
	case "leader_child_exit":
		spawn()
		os.Exit(0)
	}
	os.Exit(0)
}

func processHelper(mode, pidFile string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeProcessHelper$")
	cmd.Env = append(os.Environ(), processHelperEnv+"=1", processModeEnv+"="+mode, processChildEnv+"="+pidFile, "GORACE=atexit_sleep_ms=0")
	return cmd
}

// cleanupProcess kills only the group and child this test created.
func cleanupProcess(t *testing.T, cmd *exec.Cmd, pidFile string) {
	t.Helper()
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil && cmd.Process != nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			// Only signal a child still proven to belong to this test's group.
			if current, err := proc.Inspect(pid); err == nil && current.PGID == cmd.Process.Pid {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
}

func readChildPID(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("synthetic child pid was not recorded in %s", path)
	return 0
}

func assertGroupGone(t *testing.T, cmd *exec.Cmd, childPID int) {
	t.Helper()
	if _, err := proc.Inspect(childPID); !errors.Is(err, proc.ErrNotRunning) {
		t.Fatalf("detached child %d survived streamProcess: %v", childPID, err)
	}
	members, err := proc.GroupMembers(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("inspect owned group: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("owned process group %d still has members %v after streamProcess returned", cmd.Process.Pid, members)
	}
}

func TestStreamProcessReapsDetachedChildAfterSuccessfulTerminal(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate, err := newBridge(ctx, nil, nil, discardObserve)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	live := newLiveness(ctx, cancel, discardObserve, 0, 0, "warn")
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := processHelper("success_child", pidFile)
	cleanupProcess(t, cmd, pidFile)
	if err := streamProcess(ctx, "claude", cmd, gate, live); err != nil {
		t.Fatalf("successful terminal with a detached child failed: %v", err)
	}
	assertGroupGone(t, cmd, readChildPID(t, pidFile))
}

func TestStreamProcessCancellationConvergesGroupBeforeReturn(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observe := func(kind string, _ map[string]any) error {
		if kind == "native_session" {
			cancel()
		}
		return nil
	}
	gate, err := newBridge(ctx, nil, nil, observe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	live := newLiveness(ctx, cancel, discardObserve, 0, 0, "warn")
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := processHelper("cancel_child", pidFile)
	cleanupProcess(t, cmd, pidFile)
	started := time.Now()
	err = streamProcess(ctx, "claude", cmd, gate, live)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not reported: %v", err)
	}
	if kind := FailureKindOf(err); kind != "" {
		t.Fatalf("EOF caused by cancellation was promoted to %s: %v", kind, err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("cancellation did not converge promptly: %s", time.Since(started))
	}
	assertGroupGone(t, cmd, readChildPID(t, pidFile))
}

func TestStreamProcessKeepsConfirmedFailureNextToCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observe := func(kind string, _ map[string]any) error {
		if kind == "native_session" {
			cancel()
		}
		return nil
	}
	gate, err := newBridge(ctx, nil, nil, observe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	live := newLiveness(ctx, cancel, discardObserve, 0, 0, "warn")
	pidFile := filepath.Join(t.TempDir(), "unused.pid")
	cmd := processHelper("auth_cancel", "")
	cleanupProcess(t, cmd, pidFile)
	err = streamProcess(ctx, "claude", cmd, gate, live)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was dropped from the joined failure: %v", err)
	}
	if kind := FailureKindOf(err); kind != FailureAuthentication {
		t.Fatalf("structured authentication failure was masked by cancellation: %v", err)
	}
	var failure *Failure
	if !errors.As(err, &failure) || !failure.confirmed {
		t.Fatalf("failure provenance was lost: %v", err)
	}
}

func TestConvergeProcessGroupWithoutRecordedIdentity(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := processHelper("leader_child_exit", pidFile)
	if err := configureProcess(cmd); err != nil {
		t.Fatal(err)
	}
	cleanupProcess(t, cmd, pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	childPID := readChildPID(t, pidFile)
	if _, err := proc.Inspect(cmd.Process.Pid); !errors.Is(err, proc.ErrNotRunning) {
		t.Fatal("fixture leader should have exited before ownership recording")
	}
	if alive, err := proc.GroupAlive(cmd.Process.Pid); err != nil || !alive {
		t.Fatalf("fixture child should keep the started group alive: alive=%v err=%v", alive, err)
	}
	if err := convergeProcessGroup(cmd, proc.Identity{}, false); err != nil {
		t.Fatalf("unrecorded group with a live child was not converged: %v", err)
	}
	assertGroupGone(t, cmd, childPID)
}

func TestConvergeProcessGroupRejectsUnverifiableOwnership(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := processHelper("leader_child_exit", pidFile)
	if err := configureProcess(cmd); err != nil {
		t.Fatal(err)
	}
	cleanupProcess(t, cmd, pidFile)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	// A recorded group id that is not the started leader cannot be proven ours.
	err := convergeProcessGroup(cmd, proc.Identity{PID: cmd.Process.Pid, PGID: cmd.Process.Pid + 1}, true)
	if kind := FailureKindOf(err); kind != FailureProcessUncertain {
		t.Fatalf("unverifiable ownership returned %v, want %s", err, FailureProcessUncertain)
	}
}

func TestWatchCancelWaitsForRunningCallback(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	join := watchCancel(ctx, func() {
		close(started)
		<-release
	})
	cancel()
	<-started
	returned := make(chan struct{})
	go func() {
		join()
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("join returned while the cancellation callback still ran")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("join did not return after the callback finished")
	}
	join()
}

func TestWatchCancelStopsUnusedCallback(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	called := make(chan struct{}, 1)
	join := watchCancel(ctx, func() { called <- struct{}{} })
	join()
	join()
	cancel()
	select {
	case <-called:
		t.Fatal("cancellation callback ran after the join stopped it")
	case <-time.After(50 * time.Millisecond):
	}
}
