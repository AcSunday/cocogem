package nativecli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

func pluginTestBridge(t *testing.T, root string, authorize func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error)) *bridge {
	t.Helper()
	gate, err := newBridge(context.Background(), nil, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	gate.cwd = root
	gate.approver = newNativeApprover(nil, nil, model.Request{
		ExecutionMode: "project_autonomous", WorkspaceRoot: root, AuthorizeNative: authorize,
	}, "opencode", nil)
	return gate
}

func pluginPost(t *testing.T, gate *bridge, path string, body any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, "http://"+gate.addr+path, strings.NewReader(string(raw)))
	request.Host = gate.addr
	request.Header.Set("Authorization", "Bearer "+gate.pluginToken)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	gate.servePlugin(recorder, request)
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	return response
}

func TestPluginBrokerBindsExactOperation(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	decisions := 0
	gate := pluginTestBridge(t, root, func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		decisions++
		scope := root
		if strings.Contains(string(request.Input), outside) {
			scope = outside
		}
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: scope}, nil
	})
	marker := filepath.Join(root, "approved.txt")
	input := map[string]any{"command": "printf APPROVED > " + shellQuote(marker), "workdir": root}
	if response := pluginPost(t, gate, "/plugin/permission", map[string]any{"id": "call-1", "tool": "shell", "input": input}); response["allow"] != true {
		t.Fatalf("permission = %v", response)
	}
	changed := map[string]any{"id": "call-1", "tool": "shell", "input": map[string]any{"command": "printf CHANGED > " + shellQuote(marker), "workdir": root}}
	if response := pluginPost(t, gate, "/plugin/execute", changed); response["ok"] == true {
		t.Fatal("a changed command reused the decision")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a changed command produced an effect")
	}
	// The decision was consumed by the rejected attempt; request a fresh one for
	// the exact operation and prove it executes exactly once.
	if response := pluginPost(t, gate, "/plugin/permission", map[string]any{"id": "call-2", "tool": "shell", "input": input}); response["allow"] != true {
		t.Fatalf("second permission = %v", response)
	}
	if response := pluginPost(t, gate, "/plugin/execute", map[string]any{"id": "call-2", "tool": "shell", "input": input}); response["ok"] != true {
		t.Fatalf("exact command = %v", response)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "APPROVED" {
		t.Fatalf("marker = %q, %v", data, err)
	}

	original := map[string]any{"path": outside, "content": "ORIGINAL"}
	if response := pluginPost(t, gate, "/plugin/permission", map[string]any{"id": "file-1", "tool": "write", "input": original}); response["allow"] != true {
		t.Fatalf("write permission = %v", response)
	}
	changedWrite := map[string]any{"id": "file-1", "tool": "write", "input": map[string]any{"path": outside, "content": "CHANGED"}}
	if response := pluginPost(t, gate, "/plugin/file", changedWrite); response["ok"] == true {
		t.Fatal("changed write bytes reused the decision")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("changed write bytes produced an effect")
	}
	if response := pluginPost(t, gate, "/plugin/permission", map[string]any{"id": "file-2", "tool": "write", "input": original}); response["allow"] != true {
		t.Fatalf("second write permission = %v", response)
	}
	if response := pluginPost(t, gate, "/plugin/file", map[string]any{"id": "file-2", "tool": "write", "input": original}); response["ok"] != true {
		t.Fatalf("exact write = %v", response)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "ORIGINAL" {
		t.Fatalf("outside = %q, %v", data, err)
	}
	if decisions != 4 {
		t.Fatalf("host decisions = %d", decisions)
	}
}

func TestPluginBrokerRejectsChangedCWD(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	var calls atomic.Int32
	gate := pluginTestBridge(t, root, func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		calls.Add(1)
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	input := map[string]any{"command": "pwd", "workdir": root}
	if response := pluginPost(t, gate, "/plugin/permission", map[string]any{"id": "cwd-1", "tool": "shell", "input": input}); response["allow"] != true {
		t.Fatalf("permission = %v", response)
	}
	changed := map[string]any{"id": "cwd-1", "tool": "shell", "input": map[string]any{"command": "pwd", "workdir": other}}
	if response := pluginPost(t, gate, "/plugin/execute", changed); response["ok"] == true {
		t.Fatal("a changed cwd reused the decision")
	}
}
