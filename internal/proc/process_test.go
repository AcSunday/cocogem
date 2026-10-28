//go:build darwin || linux

package proc

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestProcHelper(t *testing.T) {
	mode := os.Getenv("COCOGEM_PROC_TEST_MODE")
	if mode == "" {
		return
	}
	if mode == "child" {
		signal.Ignore(syscall.SIGTERM)
		_ = os.WriteFile(os.Getenv("COCOGEM_PROC_TEST_PID"), []byte(strconv.Itoa(os.Getpid())), 0600)
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcHelper$")
	cmd.Env = append(os.Environ(), "COCOGEM_PROC_TEST_MODE=child")
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	time.Sleep(20 * time.Second)
	os.Exit(0)
}

func TestTerminateStopsEntireOwnedProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcHelper$")
	cmd.Env = append(os.Environ(), "COCOGEM_PROC_TEST_MODE=leader", "COCOGEM_PROC_TEST_PID="+pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	var childPID int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if raw, err := os.ReadFile(pidFile); err == nil {
			childPID, _ = strconv.Atoi(string(raw))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("synthetic child did not start")
	}
	leader, err := Inspect(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	child, err := Inspect(childPID)
	if err != nil {
		t.Fatal(err)
	}
	if child.PGID != leader.PGID {
		t.Fatal("fixture child is not in the owned group")
	}
	if err := Terminate(leader, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(childPID); err == nil {
		t.Fatalf("Terminate returned success while owned child %d remained alive", childPID)
	}
}
