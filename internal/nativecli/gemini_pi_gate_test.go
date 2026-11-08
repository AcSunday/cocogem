//go:build darwin

package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

// gateTestRequest builds one project_autonomous request with a host callback.
func gateTestRequest(t *testing.T, root string, decide func(model.NativePermissionRequest) (model.NativePermissionDecision, error)) model.Request {
	t.Helper()
	request := testRequest()
	request.Executor = "gemini"
	request.ExecutionMode = "project_autonomous"
	request.WorkspaceRoot = root
	request.AuthorizeNative = func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return decide(request)
	}
	return request
}

func gateTestBroker(t *testing.T, request model.Request) (*gateBroker, *bridge, *nativeSandbox, string) {
	t.Helper()
	root := request.WorkspaceRoot
	base := t.TempDir()
	gate, err := newBridge(t.Context(), nil, func(context.Context, string, json.RawMessage) (string, error) {
		return "", errors.New("host tools are not used in this test")
	}, discardObserve)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	sandbox, err := newNativeSandbox(base)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.addRoots(root)
	live := newLiveness(t.Context(), func() {}, discardObserve, 0, 0, "warn")
	approver := newNativeApprover(gate, live, request, request.Executor, sandbox)
	staging := filepath.Join(base, "staged")
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	broker, err := newGateBroker(approver, sandbox, staging, root, request.Executor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.close)
	return broker, gate, sandbox, staging
}

// execShell runs one command through the host shell, returning combined output.
func execShell(command string) (string, error) {
	out, err := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", command).CombinedOutput()
	return string(out), err
}

// execShellWithInput runs one command and feeds the given stdin.
func execShellWithInput(command, input string) (string, error) {
	cmd := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", command)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// A call the host denies must not produce any effect, and the CLI must receive a
// denial (never an allow).
func TestGateDenyHasNoEffect(t *testing.T) {
	if err := sandboxExecutable(); err != nil {
		t.Skip("macOS seatbelt unavailable")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "denied.txt")
	request := gateTestRequest(t, root, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "user denied"}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]string{"command": "printf EFFECT > denied.txt"})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-deny", Name: "run_shell_command", Input: input,
	})
	if decision.Allow {
		t.Fatal("a denied call was reported as allowed")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the denied command produced an effect")
	}
	if reply := geminiGateReply("BeforeTool", decision); !strings.Contains(reply, `"deny"`) {
		t.Fatalf("CLI reply is not a denial: %s", reply)
	}
}

// An allow_once authorization must execute exactly once, and the command must be
// rewritten into a per-call sandbox profile.
func TestGateAllowOnceRunsExactlyOnce(t *testing.T) {
	if err := sandboxExecutable(); err != nil {
		t.Skip("macOS seatbelt unavailable")
	}
	root := t.TempDir()
	var callbacks atomic.Int32
	request := gateTestRequest(t, root, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks.Add(1)
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]string{"command": "printf ONCE > once.txt"})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-once", Name: "run_shell_command", Input: input,
	})
	if !decision.Allow {
		t.Fatalf("an approved call was refused: %s", decision.Reason)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("host callbacks=%d, want exactly 1", callbacks.Load())
	}
	var narrowed map[string]string
	if err := json.Unmarshal(decision.Input, &narrowed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(narrowed["command"], "sandbox-exec") {
		t.Fatalf("the approved command was not narrowed into the per-call sandbox: %q", narrowed["command"])
	}
	if out, err := execShell(narrowed["command"]); err != nil {
		t.Fatalf("the approved command failed: %v (%s)", err, out)
	}
	data, err := os.ReadFile(filepath.Join(root, "once.txt"))
	if err != nil || string(data) != "ONCE" {
		t.Fatalf("approved write result=%q err=%v", data, err)
	}
	outside := filepath.Join(filepath.Dir(root), "escape.txt")
	out, _ := execShell(strings.Replace(narrowed["command"], "once.txt", outside, 1))
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the per-call profile did not block an outside write: %s", out)
	}
}

// Gemini's installed read_file schema uses `file_path`, not `path`. A host that
// classified only `path` would resolve the project directory, auto-allow, and
// let the CLI open the outside file.
func TestGateInstalledReadFileFieldReachesHost(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	var seen model.NativePermissionRequest
	var callbacks atomic.Int32
	request := gateTestRequest(t, root, func(got model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks.Add(1)
		seen = got
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "synthetic"}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	// Exact installed payload shape: read_file{file_path, start_line?, end_line?}
	input, _ := json.Marshal(map[string]any{"file_path": outside})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "gemini:read-outside", Name: "read_file", Input: input,
	})
	if decision.Allow {
		t.Fatal("an outside read_file was auto-allowed")
	}
	if callbacks.Load() != 1 {
		t.Fatalf("host callbacks=%d, want the outside read to reach the host once", callbacks.Load())
	}
	if seen.Action != "read" {
		t.Fatalf("classified action=%q, want read", seen.Action)
	}
	if !strings.HasSuffix(seen.Resource, "outside.txt") {
		t.Fatalf("resource=%q, want the exact outside file", seen.Resource)
	}
	if seen.Resource == root || strings.HasSuffix(seen.Resource, "workspace") {
		t.Fatal("the outside read was classified as the project directory")
	}
}

// The same check for the other installed path fields that are not `path`.
func TestGateInstalledPathFieldsAreResolved(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := resolvePhysicalPath(filepath.Join(area, "elsewhere"))
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		input  map[string]any
		want   string
		action string
	}{
		{"write_file", map[string]any{"file_path": filepath.Join(outside, "w.txt"), "content": "X"}, filepath.Join(outside, "w.txt"), "write"},
		{"replace", map[string]any{"file_path": filepath.Join(outside, "r.txt"), "old_string": "a", "new_string": "b"}, filepath.Join(outside, "r.txt"), "write"},
		{"glob", map[string]any{"dir_path": outside, "pattern": "*"}, outside, "read"},
		{"grep_search", map[string]any{"dir_path": outside, "pattern": "x"}, outside, "read"},
		{"list_directory", map[string]any{"dir_path": outside}, outside, "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen model.NativePermissionRequest
			request := gateTestRequest(t, root, func(got model.NativePermissionRequest) (model.NativePermissionDecision, error) {
				seen = got
				return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny}, nil
			})
			broker, _, _, _ := gateTestBroker(t, request)
			raw, _ := json.Marshal(tc.input)
			decision := broker.authorize(context.Background(), gateRequest{
				CallID: "call-" + tc.name, Name: tc.name, Input: raw,
			})
			if decision.Allow {
				t.Fatalf("%s was auto-allowed for an outside target", tc.name)
			}
			if seen.Action != tc.action {
				t.Fatalf("action=%q, want %q", seen.Action, tc.action)
			}
			if !strings.HasPrefix(seen.Resource, outside) {
				t.Fatalf("resource=%q, want inside %q", seen.Resource, outside)
			}
		})
	}
}

// An outside working directory is requested as exactly one extra scope in the
// same call: approved continues, refused stops. This replaces the earlier
// blanket refusal, which the user asked to close.
func TestGateShellOutsideWorkingDirectory(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := resolvePhysicalPath(filepath.Join(area, "elsewhere"))
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	approved := gateTestRequest(t, root, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		scope := request.Resource
		if scope == "" {
			scope = root
		}
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: scope}, nil
	})
	broker, _, _, _ := gateTestBroker(t, approved)
	input, _ := json.Marshal(map[string]any{"command": "ls", "dir_path": outside})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-outside-cwd", Name: "run_shell_command", Input: input,
	})
	if !decision.Allow {
		t.Fatalf("an approved outside working directory was refused: %s", decision.Reason)
	}
	var narrowed map[string]string
	if err := json.Unmarshal(decision.Input, &narrowed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(narrowed["command"], outside) {
		t.Fatalf("the approved command did not run in the approved directory: %q", narrowed["command"])
	}
	// The same call is refused when the host denies that directory.
	denied := gateTestRequest(t, root, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		if strings.HasSuffix(request.Resource, "elsewhere") {
			return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "denied"}, nil
		}
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: request.Resource}, nil
	})
	deniedBroker, _, _, _ := gateTestBroker(t, denied)
	refused := deniedBroker.authorize(context.Background(), gateRequest{
		CallID: "call-outside-cwd-denied", Name: "run_shell_command", Input: input,
	})
	if refused.Allow {
		t.Fatal("a denied outside working directory was allowed")
	}
}

// An inside dir_path is allowed and keeps its real working directory.
func TestGateShellInsideWorkingDirectory(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	inside := filepath.Join(root, "sub")
	if err := os.MkdirAll(inside, 0700); err != nil {
		t.Fatal(err)
	}
	request := gateTestRequest(t, root, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]any{"command": "printf X > marker.txt", "dir_path": inside})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-inside-cwd", Name: "run_shell_command", Input: input,
	})
	if !decision.Allow {
		t.Fatalf("an inside dir_path was refused: %s", decision.Reason)
	}
	var narrowed map[string]string
	if err := json.Unmarshal(decision.Input, &narrowed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(narrowed["command"], inside) {
		t.Fatalf("the approved command did not run in the declared directory: %q", narrowed["command"])
	}
}

// A native tool the adapter did not declare must be refused, never passed.
func TestGateUndeclaredToolIsRefused(t *testing.T) {
	root := t.TempDir()
	request := gateTestRequest(t, root, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-unknown", Name: "some_new_tool", Input: json.RawMessage(`{"path":"x"}`),
	})
	if decision.Allow {
		t.Fatal("an undeclared native tool was allowed")
	}
}

// A file tool without a path must not default to the project directory.
func TestGateFileToolWithoutPathIsRefused(t *testing.T) {
	root := t.TempDir()
	request := gateTestRequest(t, root, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-nopath", Name: "read_file", Input: json.RawMessage(`{}`),
	})
	if decision.Allow {
		t.Fatal("a path-less file tool was allowed")
	}
}

// Repeated authorize for the same call id and input must not ask the host twice
// or re-stage; different arguments under the same id must be refused.
func TestGateRepeatedAuthorizeIsBoundAndIdempotent(t *testing.T) {
	root := t.TempDir()
	var callbacks atomic.Int32
	request := gateTestRequest(t, root, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks.Add(1)
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]string{"command": "printf A > a.txt"})
	first := broker.authorize(context.Background(), gateRequest{CallID: "call-replay", Name: "run_shell_command", Input: input})
	second := broker.authorize(context.Background(), gateRequest{CallID: "call-replay", Name: "run_shell_command", Input: input})
	if !first.Allow || !second.Allow {
		t.Fatalf("a repeated identical authorize changed the decision: %+v %+v", first, second)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("host callbacks=%d, want 1 (a replay must not consume twice)", callbacks.Load())
	}
	other, _ := json.Marshal(map[string]string{"command": "printf B > b.txt"})
	third := broker.authorize(context.Background(), gateRequest{CallID: "call-replay", Name: "run_shell_command", Input: other})
	if third.Allow {
		t.Fatal("different arguments under one call id were allowed")
	}
	if callbacks.Load() != 1 {
		t.Fatalf("a mismatched replay still asked the host: callbacks=%d", callbacks.Load())
	}
}

// Repeated finalize must not commit twice, and concurrent writes to one outside
// file cannot both be staged.
func TestGateFinalizeAndConcurrentWrites(t *testing.T) {
	if err := sandboxExecutable(); err != nil {
		t.Skip("macOS seatbelt unavailable")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(area, "shared.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	request := gateTestRequest(t, root, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: target}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	first, _ := json.Marshal(map[string]any{"file_path": target, "content": "FIRST"})
	second, _ := json.Marshal(map[string]any{"file_path": target, "content": "SECOND"})
	var wg sync.WaitGroup
	results := make([]gateDecision, 2)
	for i, input := range [][]byte{first, second} {
		wg.Add(1)
		go func(index int, payload []byte) {
			defer wg.Done()
			results[index] = broker.authorize(context.Background(), gateRequest{
				CallID: "call-concurrent-" + string(rune('a'+index)), Name: "write_file", Input: payload,
			})
		}(i, input)
	}
	wg.Wait()
	allowed := 0
	for _, decision := range results {
		if decision.Allow {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("concurrent writes to one outside file: allowed=%d, want exactly 1", allowed)
	}
	// Commit once from the winner and prove a duplicate finalize is a no-op.
	var winner string
	for i, decision := range results {
		if decision.Allow {
			winner = "call-concurrent-" + string(rune('a'+i))
			var narrowed map[string]any
			if err := json.Unmarshal(decision.Input, &narrowed); err != nil {
				t.Fatal(err)
			}
			staged, _ := narrowed["file_path"].(string)
			if err := os.WriteFile(staged, []byte("COMMITTED"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := broker.finalize(winner, false); err != nil {
		t.Fatal(err)
	}
	if err := broker.finalize(winner, false); err != nil {
		t.Fatalf("a duplicate finalize failed: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "COMMITTED" {
		t.Fatalf("commit result=%q", data)
	}
	if err := broker.finalize(winner, true); err != nil {
		t.Fatalf("a repeated failed finalize failed: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "COMMITTED" {
		t.Fatalf("a late failed finalize rewrote the file: %q", data)
	}
}

// A read approval for an outside file must never become a write: the read tool
// is served from a staged copy and no commit exists for it.
func TestGateReadApprovalNeverCommits(t *testing.T) {
	if err := sandboxExecutable(); err != nil {
		t.Skip("macOS seatbelt unavailable")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	request := gateTestRequest(t, root, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: outside}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]any{"file_path": outside})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-read-commit", Name: "read_file", Input: input,
	})
	if !decision.Allow {
		t.Fatalf("the approved read was refused: %s", decision.Reason)
	}
	var narrowed map[string]string
	if err := json.Unmarshal(decision.Input, &narrowed); err != nil {
		t.Fatal(err)
	}
	if narrowed["file_path"] == outside {
		t.Fatal("the outside read was not served through the staged copy")
	}
	if err := os.WriteFile(narrowed["file_path"], []byte("TAMPERED"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := broker.finalize("call-read-commit", false); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "ORIGINAL" {
		t.Fatalf("a read approval wrote the outside file: %q err=%v", data, err)
	}
}

// An approved outside write commits only after the tool's real result, and a
// failed tool leaves the outside file untouched.
func TestGateApprovedOutsideWriteCommitsAfterResult(t *testing.T) {
	if err := sandboxExecutable(); err != nil {
		t.Skip("macOS seatbelt unavailable")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	request := gateTestRequest(t, root, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: outside}, nil
	})
	broker, _, _, _ := gateTestBroker(t, request)
	input, _ := json.Marshal(map[string]string{"file_path": outside, "content": "NEW"})
	decision := broker.authorize(context.Background(), gateRequest{
		CallID: "call-write", Name: "write_file", Input: input,
	})
	if !decision.Allow {
		t.Fatalf("an approved outside write was refused: %s", decision.Reason)
	}
	var narrowed map[string]string
	if err := json.Unmarshal(decision.Input, &narrowed); err != nil {
		t.Fatal(err)
	}
	if narrowed["file_path"] == outside {
		t.Fatal("the outside write was not staged")
	}
	if err := os.WriteFile(narrowed["file_path"], []byte("NEW"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "ORIGINAL" {
		t.Fatalf("outside file changed before the tool result: %q", data)
	}
	if err := broker.finalize("call-write", false); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "NEW" {
		t.Fatalf("approved write was not committed: %q", data)
	}
	decision = broker.authorize(context.Background(), gateRequest{
		CallID: "call-write-2", Name: "write_file", Input: input,
	})
	_ = json.Unmarshal(decision.Input, &narrowed)
	if err := os.WriteFile(narrowed["file_path"], []byte("BROKEN"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := broker.finalize("call-write-2", true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "NEW" {
		t.Fatalf("a failed tool wrote the outside file: %q", data)
	}
}

// Without a host callback the adapter must refuse instead of inventing a
// decision, and a nil callback is not an allow.
func TestGateMissingCallbackFailsClosed(t *testing.T) {
	root := t.TempDir()
	request := testRequest()
	request.Executor = "gemini"
	request.ExecutionMode = "project_autonomous"
	request.WorkspaceRoot = root
	gate, err := newBridge(t.Context(), nil, func(context.Context, string, json.RawMessage) (string, error) {
		return "", errors.New("unused")
	}, discardObserve)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	live := newLiveness(t.Context(), func() {}, discardObserve, 0, 0, "warn")
	approver := newNativeApprover(gate, live, request, "gemini", nil)
	input, _ := json.Marshal(map[string]string{"command": "printf X > x.txt"})
	authorization, err := approver.authorize(context.Background(), "call-nil", "run_shell_command", input, "")
	if err == nil || authorization.Allow {
		t.Fatalf("a missing host callback was treated as an allow: allow=%v err=%v", authorization.Allow, err)
	}
}

// The hook script must print a denial, never nothing, when the broker is down.
func TestGeminiHookScriptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "gate.sh")
	if err := os.WriteFile(hook, []byte(geminiHookScript("http://127.0.0.1:1", "token")), 0700); err != nil {
		t.Fatal(err)
	}
	payload := `{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"printf X"}}`
	out, _ := execShellWithInput("/bin/sh "+shellQuote(hook), payload)
	if !strings.Contains(out, `"deny"`) {
		t.Fatalf("an unreachable broker did not produce a denial: %q", out)
	}
}

// The Pi extension must be generated with the exact broker endpoint and must
// answer a failed host round-trip with a block.
func TestPiExtensionSourceFailsClosed(t *testing.T) {
	source := piGateExtensionSource("http://127.0.0.1:1", "secret-token")
	for _, want := range []string{"tool_call", "block: true", "/gate/pi/tool_call", "secret-token", "the Cocogem approval gate is unavailable"} {
		if !strings.Contains(source, want) {
			t.Fatalf("generated Pi extension is missing %q", want)
		}
	}
}
