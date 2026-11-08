//go:build darwin

package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

// Every root the sandbox writes must be scanned, not only the workspace root.
func TestGateHardlinkScanCoversAllSandboxRoots(t *testing.T) {
	area := t.TempDir()
	workspace := filepath.Join(area, "workspace")
	task := filepath.Join(area, "task")
	for _, dir := range []string{workspace, task} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	// The linked file lives in the task root, not the workspace root.
	linked := filepath.Join(task, "linked.txt")
	if err := os.Link(outside, linked); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	sandbox, err := newNativeSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(workspace, task)
	scan := gateHardlinkScan(sandbox, workspace, task)
	if scan.unknown {
		t.Fatal("a readable root was reported as unverified")
	}
	found := false
	for _, path := range scan.dangerous {
		if path == linked {
			found = true
		}
	}
	if !found {
		t.Fatalf("the task-root hardlink was not scanned: dangerous=%v", scan.dangerous)
	}
	// Scanning only the workspace root would miss it: this is the regression.
	if onlyWorkspace := scanProjectHardlinks(workspace); len(onlyWorkspace.dangerous) != 0 {
		t.Fatalf("fixture is wrong: the workspace root reported %v", onlyWorkspace.dangerous)
	}
}

// An unverified root must fail closed instead of being treated as safe.
func TestGateHardlinkScanUnverifiedRootFailsClosed(t *testing.T) {
	area := t.TempDir()
	workspace := filepath.Join(area, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(area, "does-not-exist-and-cannot-be-scanned")
	sandbox, err := newNativeSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(workspace, missing)
	scan := gateHardlinkScan(sandbox, workspace, missing)
	if !scan.unknown {
		t.Fatalf("an unscannable root was treated as verified: %+v", scan)
	}
	request := gateTestRequest(t, workspace, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: workspace}, nil
	})
	gate, err := newBridge(t.Context(), nil, func(context.Context, string, json.RawMessage) (string, error) {
		return "", errors.New("unused")
	}, discardObserve)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	live := newLiveness(t.Context(), func() {}, discardObserve, 0, 0, "warn")
	approver := newNativeApprover(gate, live, request, "gemini", sandbox)
	if err := applyGateHardlinkPolicy(gate, approver, sandbox, scan); err != nil {
		t.Fatalf("recording the unverified scan failed: %v", err)
	}
	if !sandbox.denyUnknown {
		t.Fatal("the sandbox was not told the scan is unverified")
	}
	profile, err := sandbox.profile("unverified", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `(deny file-write* (subpath "/"))`) {
		t.Fatal("an unverified scan did not produce a deny-all write rule")
	}
}

// A linked file inside an attempt root must never let a native effect modify
// the outside original: the adapter refuses that exact call before any effect.
func TestGateHardlinkLinkedFileCannotModifyOutsideOriginal(t *testing.T) {
	area := t.TempDir()
	workspace := filepath.Join(area, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside-original.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(workspace, "linked.txt")
	if err := os.Link(outside, linked); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	request := gateTestRequest(t, workspace, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: workspace}, nil
	})
	broker, _, sandbox, _ := gateTestBroker(t, request)
	scan := gateHardlinkScan(sandbox, workspace)
	if err := applyGateHardlinkPolicy(broker.approver.gate, broker.approver, sandbox, scan); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"command": "printf ESCAPE > " + shellQuote(linked)})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-hardlink", Name: "run_shell_command", Input: input,
	})
	if decision.Allow {
		t.Fatal("a command writing through an outside-linked inode was allowed")
	}
	if decision.Reason == "" {
		t.Fatal("the refusal gave no reason")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "ORIGINAL" {
		t.Fatalf("a hardlink inside the project modified the outside original: %q err=%v", data, err)
	}
}

// A linked file must also be refused by the adapter's own classifier before any
// profile is built, so the effect is never attempted.
func TestGateHardlinkLinkIsRefusedByApprover(t *testing.T) {
	area := t.TempDir()
	workspace := filepath.Join(area, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(workspace, "inside-link.txt")
	if err := os.Link(outside, linked); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	var callbacks int
	request := gateTestRequest(t, workspace, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks++
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: workspace}, nil
	})
	broker, _, sandbox, _ := gateTestBroker(t, request)
	scan := gateHardlinkScan(sandbox, workspace)
	if err := applyGateHardlinkPolicy(broker.approver.gate, broker.approver, sandbox, scan); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"command": "printf X > " + shellQuote(linked)})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-hardlink-refused", Name: "run_shell_command", Input: input,
	})
	if decision.Allow {
		t.Fatal("a command targeting a file linked outside the project was allowed")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "ORIGINAL" {
		t.Fatalf("the outside original changed: %q err=%v", data, err)
	}
}

// An approved outside working directory must run in the same call, with the
// sandbox containing exactly that directory.
func TestGateApprovedOutsideWorkingDirectoryRunsInSameCall(t *testing.T) {
	if err := sandboxExecutable(); err != nil {
		t.Skip("macOS seatbelt unavailable")
	}
	area := t.TempDir()
	workspace := filepath.Join(area, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outsideDir := resolvePhysicalPath(filepath.Join(area, "outside-dir"))
	if err := os.MkdirAll(outsideDir, 0700); err != nil {
		t.Fatal(err)
	}
	var requested []string
	request := gateTestRequest(t, workspace, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		requested = append(requested, request.Resource)
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: request.Resource}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]any{"command": "printf HERE > marker.txt", "dir_path": outsideDir})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-outside-cwd", Name: "run_shell_command", Input: input,
	})
	if !decision.Allow {
		t.Fatalf("an approved outside working directory was refused: %s", decision.Reason)
	}
	// Exactly one extra scope was requested, for the directory itself.
	if len(requested) != 2 || !strings.HasSuffix(requested[1], "outside-dir") {
		t.Fatalf("requested scopes=%v, want the project root then the exact directory", requested)
	}
	var narrowed map[string]string
	if err := json.Unmarshal(decision.Input, &narrowed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(narrowed["command"], "sandbox-exec") {
		t.Fatalf("the approved command was not sandboxed: %q", narrowed["command"])
	}
	if !strings.Contains(narrowed["command"], outsideDir) {
		t.Fatalf("the sandbox did not include the approved directory: %q", narrowed["command"])
	}
	out, err := execShell(narrowed["command"])
	if err != nil {
		t.Fatalf("the approved command failed: %v (%s)", err, out)
	}
	data, err := os.ReadFile(filepath.Join(outsideDir, "marker.txt"))
	if err != nil || string(data) != "HERE" {
		t.Fatalf("the approved working directory effect=%q err=%v", data, err)
	}
}

// A denied outside working directory must not run, and must not widen the
// sandbox to a parent.
func TestGateDeniedOutsideWorkingDirectoryIsRefused(t *testing.T) {
	area := t.TempDir()
	workspace := filepath.Join(area, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outsideDir := resolvePhysicalPath(filepath.Join(area, "outside-dir"))
	if err := os.MkdirAll(outsideDir, 0700); err != nil {
		t.Fatal(err)
	}
	request := gateTestRequest(t, workspace, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		if strings.HasSuffix(request.Resource, "outside-dir") {
			return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "denied"}, nil
		}
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: request.Resource}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]any{"command": "printf HERE > marker.txt", "dir_path": outsideDir})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-outside-cwd-denied", Name: "run_shell_command", Input: input,
	})
	if decision.Allow {
		t.Fatal("a denied outside working directory was allowed")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "marker.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the refused command produced an effect")
	}
}

// A command with an approved working directory but another, unapproved outside
// target is still refused: the extra scope must not widen the whole command.
func TestGateOutsideWorkingDirectoryDoesNotWidenOtherTargets(t *testing.T) {
	area := t.TempDir()
	workspace := filepath.Join(area, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	outsideDir := resolvePhysicalPath(filepath.Join(area, "outside-dir"))
	if err := os.MkdirAll(outsideDir, 0700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(area, "other.txt")
	if err := os.WriteFile(other, []byte("OTHER"), 0600); err != nil {
		t.Fatal(err)
	}
	// The host approves ONLY the working directory; the other outside target is
	// refused. The extra scope must not cover it.
	request := gateTestRequest(t, workspace, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		if strings.HasSuffix(request.Resource, "outside-dir") {
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: request.Resource}, nil
		}
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "not approved"}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]any{
		"command": "printf X > " + shellQuote(other), "dir_path": outsideDir,
	})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-widen", Name: "run_shell_command", Input: input,
	})
	if decision.Allow {
		t.Fatal("an extra approved directory authorized an unrelated outside target")
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "OTHER" {
		t.Fatalf("the unapproved outside target changed: %q err=%v", data, err)
	}
}
