package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

// Opt-in installed OpenCode check: a synthetic model asks for a native Bash
// command, the real permission.asked event is answered through the local server
// API, and the host callback decides before the marker exists.
func TestInstalledOpenCodePermissionRoundTrip(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_OPENCODE") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_OPENCODE=1 for a provider-free OpenCode check")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode CLI is not installed")
	}
	for _, tc := range []struct {
		name       string
		mode       string
		allow      bool
		expectFile bool
		target     func(area, root string) string
	}{
		{name: "inside allowed", allow: true, expectFile: true, target: func(area, root string) string { return filepath.Join(root, "inside.txt") }},
		{name: "outside denied", allow: false, expectFile: false, target: func(area, root string) string { return filepath.Join(area, "outside.txt") }},
		// The private execute.before plugin hands the command to the host, which
		// runs it under a per-call profile containing exactly the approved path.
		{name: "outside approved continues", allow: true, expectFile: true, target: func(area, root string) string { return filepath.Join(area, "approved-outside.txt") }},
		{name: "full access revalidates", mode: "full_access", allow: true, expectFile: true, target: func(area, root string) string { return filepath.Join(root, "full.txt") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			area := t.TempDir()
			root := filepath.Join(area, "workspace")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			marker := tc.target(area, root)
			var turns atomic.Int32
			var callback atomic.Int32
			var sawPermission atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"object":"list","data":[{"id":"cocogem-probe","object":"model"}]}`)
					return
				}
				var input struct {
					Model    string `json:"model"`
					Messages []struct {
						Role string `json:"role"`
					} `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&input)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(value map[string]any) {
					raw, _ := json.Marshal(value)
					fmt.Fprintf(w, "data: %s\n\n", raw)
				}
				hasToolResult := false
				for _, message := range input.Messages {
					if message.Role == "tool" {
						hasToolResult = true
					}
				}
				if turns.Add(1) == 1 && !hasToolResult {
					send(map[string]any{"id": "chatcmpl-oc", "object": "chat.completion.chunk", "created": 1, "model": input.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_oc", "type": "function", "function": map[string]any{"name": "bash", "arguments": ""}}}}, "finish_reason": nil}}})
					send(map[string]any{"id": "chatcmpl-oc", "object": "chat.completion.chunk", "created": 1, "model": input.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": `{"command":"printf PERMISSION > ` + marker + `"}`}}}}, "finish_reason": nil}}})
					send(map[string]any{"id": "chatcmpl-oc", "object": "chat.completion.chunk", "created": 1, "model": input.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}})
				} else {
					send(map[string]any{"id": "chatcmpl-oc", "object": "chat.completion.chunk", "created": 1, "model": input.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "fixture done"}, "finish_reason": nil}}})
					send(map[string]any{"id": "chatcmpl-oc", "object": "chat.completion.chunk", "created": 1, "model": input.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}})
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			request := model.Request{
				Executor: "opencode", Model: "cocogem-probe/cocogem-probe", System: "Run the supplied tool.",
				Messages:             []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Run one fixture command."}}}},
				ExecutionMode:        firstString(tc.mode, "project_autonomous"),
				WorkspaceRoot:        root,
				FirstResponseTimeout: 40 * time.Second,
				StallTimeout:         40 * time.Second,
				StallAction:          "stop",
				AuthorizeNative: func(_ context.Context, req model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					callback.Add(1)
					if !strings.Contains(string(req.Input), "PERMISSION") {
						return model.NativePermissionDecision{}, fmt.Errorf("unexpected native request %q %s", req.Name, req.Input)
					}
					if tc.allow {
						scope := root
						if strings.Contains(marker, "approved-outside") {
							scope = marker
						}
						return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: scope}, nil
					}
					return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "fixture denied"}, nil
				},
			}
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			p := Provider{command: func(id string, argv ...string) *exec.Cmd { return exec.Command(id, argv...) }}
			err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(kind string, data map[string]any) error {
				if kind == "native_permission" || kind == "native_permission_resolved" {
					sawPermission.Store(true)
				}
				return nil
			}, runOptions{skipCheck: true, openCodeProbeURL: server.URL})
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			if callback.Load() == 0 || !sawPermission.Load() {
				t.Fatalf("permission.asked did not reach the host callback: callback=%d event=%v", callback.Load(), sawPermission.Load())
			}
			_, statErr := os.Stat(marker)
			if tc.expectFile && statErr != nil {
				t.Fatal("allowed native command did not execute")
			}
			if !tc.expectFile && !os.IsNotExist(statErr) {
				t.Fatal("a refused or unexpandable native command produced an effect")
			}
		})
	}
}
