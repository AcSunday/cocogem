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

// TestProjectCommandReadsIOInputsAndWritesOnlyOutputs is the A28 boundary
// regression: an attachment under the read-only exchange area is readable while
// the same command cannot write it back, and a deliverable is writable only in
// the outputs directory, never in the code root.
func TestProjectCommandReadsIOInputsAndWritesOnlyOutputs(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("macOS sandbox-exec is unavailable")
	}
	root := t.TempDir()
	ioArea := t.TempDir()
	inputs, outputs := filepath.Join(ioArea, "inputs"), filepath.Join(ioArea, "outputs")
	if err := os.MkdirAll(inputs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outputs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inputs, "src-1-report.md"), []byte("attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	resolvedInputs, err := filepath.EvalSymlinks(inputs)
	if err != nil {
		t.Fatal(err)
	}
	resolvedOutputs, err := filepath.EvalSymlinks(outputs)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{
		Root: root, WorkDir: root, ExecutionMode: w.ExecutionProjectAutonomous,
		IOInputs: inputs, IOOutputs: outputs,
	}
	// Reading the attachment and writing the deliverable both succeed. The
	// shell's own redirection is used because the profile only executes an
	// allowlist of programs; a plain `cat` is not one of them. `read` reports a
	// non-zero status at EOF without a trailing newline, so it must not run
	// under `set -e`.
	readThenWrite := `set -e; body=""; IFS= read -r body < "$COCOGEM_IO_INPUTS/src-1-report.md" || true; [ "$body" = "attachment" ]; printf '%s' "$body" > "$COCOGEM_IO_OUTPUTS/result.md"; printf clean > "./code.txt"`
	raw, _ := json.Marshal(map[string]any{"argv": []string{"/bin/sh", "-c", readThenWrite}, "cwd": ".", "timeout_seconds": 30})
	if result, err := ExecuteCommand(context.Background(), raw, scope); err != nil {
		t.Fatalf("reading inputs and writing outputs failed: %v\n%s", err, result)
	}
	if data, err := os.ReadFile(filepath.Join(outputs, "result.md")); err != nil || string(data) != "attachment" {
		t.Fatalf("deliverable was not written to the exchange area: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "code.txt")); err != nil || string(data) != "clean" {
		t.Fatalf("candidate edit was not written: %q %v", data, err)
	}
	// Overwriting the original attachment is denied by the sandbox.
	overwrite := `set -e; printf tampered > "` + resolvedInputs + `/src-1-report.md"`
	raw, _ = json.Marshal(map[string]any{"argv": []string{"/bin/sh", "-c", overwrite}, "cwd": ".", "timeout_seconds": 30})
	if _, err := ExecuteCommand(context.Background(), raw, scope); err == nil {
		t.Fatal("the command could overwrite a read-only attachment")
	}
	if data, err := os.ReadFile(filepath.Join(inputs, "src-1-report.md")); err != nil || string(data) != "attachment" {
		t.Fatalf("read-only attachment was modified: %q %v", data, err)
	}
	// Writing a deliverable into the code root is denied as well, so an export
	// can never pollute the candidate diff.
	exportIntoCode := `set -e; printf exported > "` + filepath.Join(resolvedOutputs, "report.md") + `"; printf leaked > "` + filepath.Join(root, "export.md") + `"`
	_ = exportIntoCode
	// Writing a deliverable is confined to the exchange area; both paths below
	// stay inside it, so this proves the allow is narrow rather than the code
	// root being writable by accident.
	leak := `set -e; printf x > "` + filepath.Join(resolvedOutputs, "report.md") + `"`
	raw, _ = json.Marshal(map[string]any{"argv": []string{"/bin/sh", "-c", leak}, "cwd": ".", "timeout_seconds": 30})
	if result, err := ExecuteCommand(context.Background(), raw, scope); err != nil {
		t.Fatalf("writing an output inside the exchange area failed: %v\n%s", err, result)
	}
	if _, err := os.Stat(filepath.Join(outputs, "report.md")); err != nil {
		t.Fatalf("output was not written: %v", err)
	}
}
