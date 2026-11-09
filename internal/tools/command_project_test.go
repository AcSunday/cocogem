package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestProjectAutonomousCommandBoundary is the real OS-boundary regression for a
// project-scoped command: it writes inside the project, and a write outside it is
// denied by the sandbox, not by string inspection.
func TestProjectAutonomousCommandBoundary(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("macOS sandbox-exec is unavailable")
	}
	root := t.TempDir()
	work := t.TempDir()
	outside := t.TempDir()
	// `set -e` makes the denied outside write fail the whole command instead of
	// being ignored by the shell, so the exit status is real evidence.
	script := `set -e; printf inside > "./inside.txt"; printf outside > "` + filepath.Join(outside, "outside.txt") + `"; echo done`
	raw, err := json.Marshal(map[string]any{"argv": []string{"/bin/sh", "-c", script}, "cwd": ".", "timeout_seconds": 30})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Root: root, WorkDir: work, ExecutionMode: w.ExecutionProjectAutonomous}
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteCommand(context.Background(), raw, scope)
	if err == nil {
		t.Fatalf("command with an outside write was accepted: %s", result)
	}
	if data, readErr := os.ReadFile(filepath.Join(work, "inside.txt")); readErr != nil || string(data) != "inside" {
		t.Fatalf("in-project write missing: %q %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "outside.txt")); statErr == nil {
		t.Fatal("sandbox did not block the outside write")
	}
	if !strings.Contains(err.Error(), "exit code") {
		t.Fatalf("failure was not reported as a command failure: %v", err)
	}
}

// TestProjectAutonomousApprovedOutsideWrite proves an explicitly approved outside
// scope is the only additional writable root.
func TestProjectAutonomousApprovedOutsideWrite(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("macOS sandbox-exec is unavailable")
	}
	root := t.TempDir()
	work := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "approved.txt")
	raw, err := json.Marshal(map[string]any{
		"argv": []string{"/bin/sh", "-c", `printf approved > "` + target + `"`}, "cwd": ".", "timeout_seconds": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Root: root, WorkDir: work, ExecutionMode: w.ExecutionProjectAutonomous, ApprovedRoots: []string{outside}}
	if _, err := ExecuteCommand(context.Background(), raw, scope); err != nil {
		t.Fatalf("approved outside write failed: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "approved" {
		t.Fatalf("approved write missing: %q %v", data, err)
	}
	// Without the approval the same command must be blocked.
	scope.ApprovedRoots = nil
	if _, err := ExecuteCommand(context.Background(), raw, scope); err == nil {
		t.Fatal("unapproved outside write succeeded")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("the approved file disappeared")
	}
}

// TestControlledCommandRequiresApprovalOrFullAccess proves controlled mode never
// silently executes a command.
func TestControlledCommandRequiresApprovalOrFullAccess(t *testing.T) {
	root := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"argv": []string{"/bin/echo", "hi"}})
	scope := Scope{Root: root, WorkDir: root, ExecutionMode: w.ExecutionControlled}
	if _, err := ExecuteCommand(context.Background(), raw, scope); err == nil {
		t.Fatal("controlled mode executed a command without approval")
	}
}
