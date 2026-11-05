package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

// TestInstalledClaudeManagedResumeAndNativeEffects uses the installed CLI with
// a loopback fake model. It verifies real session persistence and real native
// tool effects without provider credentials, external network or model claims.
func TestInstalledClaudeManagedResumeAndNativeEffects(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary managed-session check")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI is not installed")
	}
	area := t.TempDir()
	workspace := filepath.Join(area, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	writePath := filepath.Join(workspace, "native-write.txt")
	shellPath := filepath.Join(workspace, "native-shell.txt")
	var turns atomic.Int32
	var mu sync.Mutex
	bodies := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, "fixture body", http.StatusBadRequest)
			return
		}
		if strings.Contains(r.URL.Path, "count_tokens") {
			rw.Header().Set("Content-Type", "application/json")
			fmt.Fprint(rw, `{"input_tokens":1}`)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		turn := turns.Add(1)
		var payload struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(rw, "fixture request", http.StatusBadRequest)
			return
		}
		awaitingToolResult := false
		if len(payload.Messages) > 0 {
			var blocks []struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(payload.Messages[len(payload.Messages)-1].Content, &blocks); err == nil {
				awaitingToolResult = len(blocks) > 0 && blocks[len(blocks)-1].Type == "tool_result"
			}
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		writeEvent := func(event string, value any) {
			encoded, _ := json.Marshal(value)
			fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, encoded)
		}
		writeEvent("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("msg_%d", turn), "type": "message", "role": "assistant", "model": "cocogem-fixture", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}})
		if !awaitingToolResult {
			name, input := "Write", map[string]any{"file_path": writePath, "content": "native-write-proof"}
			if strings.Contains(string(body), "session_delta/v1") {
				name, input = "Bash", map[string]any{"command": "printf native-shell-proof > " + shellPath, "description": "write the managed resume fixture"}
			}
			encoded, _ := json.Marshal(input)
			writeEvent("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("tool_%d", turn), "name": name, "input": map[string]any{}}})
			writeEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}})
			writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
			writeEvent("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}})
		} else {
			writeEvent("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			writeEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "fixture complete"}})
			writeEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
			writeEvent("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}})
		}
		writeEvent("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer server.Close()

	wrapper := filepath.Join(area, "claude-wrapper")
	script := "#!/bin/bash\nprintf '%s\\n' \"$@\" > \"$COCOGEM_CAPTURE_ARGS\"\nset -o pipefail\ntee \"$COCOGEM_CAPTURE_STDIN\" | \"$COCOGEM_REAL_CLAUDE\" \"$@\" 2> \"$COCOGEM_CAPTURE_STDERR\" | tee \"$COCOGEM_CAPTURE_STDOUT\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCOGEM_REAL_CLAUDE", path)
	t.Setenv("CLAUDECODE", "")
	storage := filepath.Join(area, "managed-session")
	request := model.Request{
		Executor: "claude", Model: "cocogem-fixture", ExecutionMode: "full_access",
		System:      "Use the explicitly enabled native tools for the fixture operation.",
		Messages:    []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "initial full ContextPlan"}}}},
		NativeTools: []string{"Write", "Bash"}, WorkspaceRoot: workspace,
		SessionStorageRoot: storage, SessionMode: "fresh",
		FirstResponseTimeout: 30 * time.Second, StallTimeout: 30 * time.Second, StallAction: "stop",
	}
	options := runOptions{
		skipCheck: true,
		adapter:   agent.AdapterInfo{ID: "claude", Version: "2.1.318", Supported: true, Adapter: "claude-stream-json", Program: path},
		claudeSettings: map[string]any{"env": map[string]string{
			"ANTHROPIC_AUTH_TOKEN": "cocogem-fixture", "ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_MODEL": "cocogem-fixture",
		}},
	}
	var invocation atomic.Int32
	provider := Provider{command: func(_ string, args ...string) *exec.Cmd {
		index := invocation.Add(1)
		cmd := exec.Command(wrapper, args...)
		cmd.Env = append(os.Environ(),
			"COCOGEM_CAPTURE_STDIN="+filepath.Join(area, fmt.Sprintf("stdin-%d.txt", index)),
			"COCOGEM_CAPTURE_ARGS="+filepath.Join(area, fmt.Sprintf("args-%d.txt", index)),
			"COCOGEM_CAPTURE_STDOUT="+filepath.Join(area, fmt.Sprintf("stdout-%d.txt", index)),
			"COCOGEM_CAPTURE_STDERR="+filepath.Join(area, fmt.Sprintf("stderr-%d.txt", index)),
		)
		return cmd
	}}
	execute := func(context.Context, string, json.RawMessage) (string, error) { return `{"ok":true}`, nil }
	var nativeID string
	observe := func(kind string, data map[string]any) error {
		if kind == "native_session" {
			nativeID, _ = data["session_id"].(string)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err := provider.run(ctx, request, execute, observe, options); err != nil {
		stdout, _ := os.ReadFile(filepath.Join(area, "stdout-1.txt"))
		stderr, _ := os.ReadFile(filepath.Join(area, "stderr-1.txt"))
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if nativeID == "" {
		t.Fatal("installed Claude did not acknowledge a native session")
	}
	if content, err := os.ReadFile(writePath); err != nil || string(content) != "native-write-proof" {
		stdout, _ := os.ReadFile(filepath.Join(area, "stdout-1.txt"))
		stderr, _ := os.ReadFile(filepath.Join(area, "stderr-1.txt"))
		mu.Lock()
		captured := append([]string{}, bodies...)
		mu.Unlock()
		t.Fatalf("real native Write did not produce the fixture: %q, %v\nstdout=%s\nstderr=%s\nbodies=%v", content, err, stdout, stderr, captured)
	}

	request.SessionMode, request.SessionID = "resume", nativeID
	request.Messages = []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "session_delta/v1: apply the new accepted constraint"}}}}
	if err := provider.run(ctx, request, execute, observe, options); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(shellPath); err != nil || string(content) != "native-shell-proof" {
		stdout, _ := os.ReadFile(filepath.Join(area, "stdout-2.txt"))
		stderr, _ := os.ReadFile(filepath.Join(area, "stderr-2.txt"))
		stdin, _ := os.ReadFile(filepath.Join(area, "stdin-2.txt"))
		args, _ := os.ReadFile(filepath.Join(area, "args-2.txt"))
		mu.Lock()
		captured := append([]string{}, bodies...)
		mu.Unlock()
		t.Fatalf("real resumed Bash did not produce the fixture: %q, %v\nstdout=%s\nstderr=%s\nstdin=%s\nargs=%s\nbodies=%v", content, err, stdout, stderr, stdin, args, captured)
	}
	args, err := os.ReadFile(filepath.Join(area, "args-2.txt"))
	if err != nil || !strings.Contains(string(args), "--resume\n"+nativeID+"\n") {
		t.Fatalf("second invocation did not resume the exact native session: %q, %v", args, err)
	}
	stdin, err := os.ReadFile(filepath.Join(area, "stdin-2.txt"))
	if err != nil || !strings.Contains(string(stdin), "session_delta/v1") || strings.Contains(string(stdin), "initial full ContextPlan") {
		t.Fatalf("host resume input was not a delta: %q, %v", stdin, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 3 || !strings.Contains(bodies[2], "native-write-proof") {
		t.Fatalf("resumed provider request did not include the prior native conversation: %+v", bodies)
	}
}
