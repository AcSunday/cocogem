package nativecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestNativeApproverCallsHostForInsideProjectCalls(t *testing.T) {
	root := t.TempDir()
	var calls atomic.Int32
	approver := newNativeApprover(nil, nil, model.Request{
		ExecutionMode: "project_autonomous", WorkspaceRoot: root,
		AuthorizeNative: func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			calls.Add(1)
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	}, "claude", nil)
	input := json.RawMessage(`{"command":"echo inside > inside.txt"}`)
	result, err := approver.authorize(context.Background(), "call-1", "Bash", input, "")
	if err != nil || !result.Allow {
		t.Fatalf("inside call = %+v, %v", result, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("host callback was bypassed for an inside-project call: %d", calls.Load())
	}
}

func TestNativeApproverHonorsHostDenialInsideProject(t *testing.T) {
	root := t.TempDir()
	approver := newNativeApprover(nil, nil, model.Request{
		ExecutionMode: "project_autonomous", WorkspaceRoot: root,
		AuthorizeNative: func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "role tightened"}, nil
		},
	}, "claude", nil)
	result, err := approver.authorize(context.Background(), "call-1", "Bash", json.RawMessage(`{"command":"echo inside > inside.txt"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Allow {
		t.Fatal("a live host denial was bypassed for an inside-project call")
	}
}

func TestNativeApproverRequiresHostCallbackForProjectAutonomous(t *testing.T) {
	approver := newNativeApprover(nil, nil, model.Request{ExecutionMode: "project_autonomous", WorkspaceRoot: t.TempDir()}, "claude", nil)
	_, err := approver.authorize(context.Background(), "call-1", "Read", json.RawMessage(`{"file_path":"`+t.TempDir()+`/x"}`), "")
	if err == nil {
		t.Fatal("a project-autonomous native effect ran without a host callback")
	}
}

func TestNativeApproverFullAccessRevalidatesWhenBound(t *testing.T) {
	var calls atomic.Int32
	approver := newNativeApprover(nil, nil, model.Request{
		ExecutionMode: "full_access", WorkspaceRoot: t.TempDir(),
		AuthorizeNative: func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			calls.Add(1)
			return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny}, nil
		},
	}, "claude", nil)
	result, err := approver.authorize(context.Background(), "call-1", "Bash", json.RawMessage(`{"command":"echo x"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Allow || calls.Load() != 1 {
		t.Fatalf("full_access with a bound host callback = allow:%v calls:%d", result.Allow, calls.Load())
	}
	// With no callback bound, full access keeps its existing local execution.
	legacy := newNativeApprover(nil, nil, model.Request{ExecutionMode: "full_access", WorkspaceRoot: t.TempDir()}, "claude", nil)
	result, err = legacy.authorize(context.Background(), "call-1", "Bash", json.RawMessage(`{"command":"echo x"}`), "")
	if err != nil || !result.Allow {
		t.Fatalf("full_access without a callback = %+v, %v", result, err)
	}
}

func TestNativeApproverHardlinkTargetIsRefused(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "root")
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "node_modules", "linked.txt")
	if err := os.Link(outside, link); err != nil {
		t.Fatal(err)
	}
	scan := scanProjectHardlinks(root)
	if scan.unknown || len(scan.dangerous) != 1 {
		t.Fatalf("hardlink scan = %+v", scan)
	}
	var calls atomic.Int32
	approver := newNativeApprover(nil, nil, model.Request{
		ExecutionMode: "project_autonomous", WorkspaceRoot: root,
		AuthorizeNative: func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			calls.Add(1)
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	}, "claude", nil)
	approver.setDangerousPaths(scan.dangerous)
	input := json.RawMessage(`{"command":"printf MUTATED > node_modules/linked.txt"}`)
	result, err := approver.authorize(context.Background(), "call-1", "Bash", input, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Allow || calls.Load() != 0 {
		t.Fatalf("dangerous hardlink was admitted: allow=%v calls=%d", result.Allow, calls.Load())
	}
}

func TestNativeApproverExternalEffectUsesExactURL(t *testing.T) {
	var seen model.NativePermissionRequest
	approver := newNativeApprover(nil, nil, model.Request{
		ExecutionMode: "project_autonomous", WorkspaceRoot: t.TempDir(),
		AuthorizeNative: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			seen = request
			return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny}, nil
		},
	}, "claude", nil)
	allowed, _ := approver.authorizeExternal(context.Background(), "POST", "https://example.test/publish?x=1", "digest")
	if allowed {
		t.Fatal("a denied external effect was allowed")
	}
	if seen.Name != "external_http" || seen.Action != "execute" {
		t.Fatalf("external request classification = %q %q", seen.Name, seen.Action)
	}
	if !strings.Contains(string(seen.Input), "https://example.test/publish?x=1") || !strings.Contains(string(seen.Input), "POST") || !strings.Contains(string(seen.Input), "digest") {
		t.Fatalf("external request input lost the exact effect: %s", seen.Input)
	}
	if !strings.HasPrefix(seen.Resource, "/.cocogem-external/example.test/") {
		t.Fatalf("external request scope = %q", seen.Resource)
	}
	if strings.Contains(seen.Resource, "example.test/publish") == false {
		t.Fatalf("external request scope lost the destination path: %q", seen.Resource)
	}
}
