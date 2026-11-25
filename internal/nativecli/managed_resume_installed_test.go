package nativecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

// The second host prompt contains only a delta. The loopback model can return
// the expected second answer only if the CLI supplies the first answer again.
const (
	resumeFirstReply = "fixture_history_marker_87231"
	resumeDelta      = "fixture_follow_up_48163"
	resumeLastReply  = "fixture_history_confirmed"
)

func resumeFixtureReply(prompt []byte) string {
	if !bytes.Contains(prompt, []byte(resumeDelta)) {
		return resumeFirstReply
	}
	if bytes.Contains(prompt, []byte(resumeFirstReply)) {
		return resumeLastReply
	}
	return "missing previous turn"
}

func checkInstalledResume(t *testing.T, request model.Request) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p := Provider{}
	info, err := p.InspectExecutionConnection(ctx, request.Executor, request.Model, "")
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedConnection = model.Connection{Identity: info.Identity, ResolvedModel: info.ResolvedModel}
	request.SessionStorageRoot = t.TempDir()
	request.WorkspaceRoot = t.TempDir()
	for _, turn := range []struct{ mode, input, reply string }{
		{"fresh", "first synthetic message", resumeFirstReply},
		{"resume", resumeDelta, resumeLastReply},
	} {
		request.SessionMode = turn.mode
		request.Messages = []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: turn.input}}}}
		var id string
		var reply strings.Builder
		err := p.Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
			return "", errors.New("unexpected host tool call")
		}, func(kind string, data map[string]any) error {
			switch kind {
			case "native_session":
				id, _ = data["session_id"].(string)
				if id == "" || data["mode"] != turn.mode || turn.mode == "resume" && id != request.SessionID {
					return errors.New("native session acknowledgement does not match the requested turn")
				}
			case "text":
				reply.WriteString(data["text"].(string))
			}
			return nil
		})
		if err != nil || id == "" || strings.TrimSpace(reply.String()) != turn.reply {
			t.Fatalf("%s: reply=%q session acknowledged=%t err=%v", turn.mode, reply.String(), id != "", err)
		}
		request.SessionID = id
	}
}

func TestInstalledPiManagedResume(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_PI") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_PI=1 for the installed Pi resume check")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		installedPiChunk(w, map[string]any{"role": "assistant", "content": resumeFixtureReply(body)}, nil)
		installedPiChunk(w, map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	for _, mode := range []string{"full_access", "workspace_autonomous"} {
		t.Run(mode, func(t *testing.T) {
			request := installedPiRequest(t, server.URL)
			t.Setenv("PI_HOME", t.TempDir())
			request.ExecutionMode = mode
			request.TrustedExecution = mode == "full_access"
			request.AuthorizeNative = denyResumeFixtureCall
			checkInstalledResume(t, request)
		})
	}
}

func denyResumeFixtureCall(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
	return model.NativePermissionDecision{Scope: model.NativeScopeDeny}, nil
}

func TestInstalledCodexManagedReadOnlyResume(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for the installed Codex resume check")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input json.RawMessage
			Tools []json.RawMessage
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		for _, tool := range body.Tools {
			var declaration struct{ Name, Type string }
			if err := json.Unmarshal(tool, &declaration); err != nil {
				t.Error(err)
			}
			for _, name := range []string{declaration.Name, declaration.Type} {
				switch name {
				case "exec_command", "write_stdin", "shell", "shell_command", "local_shell", "apply_patch":
					t.Errorf("read-only turn exposed %q", name)
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": resumeFixtureReply(body.Input), "annotations": []any{}}}}
		writeSSEJSON(w, map[string]any{"type": "response.output_item.done", "item": item})
		writeSSEJSON(w, map[string]any{"type": "response.completed", "response": map[string]any{
			"id": "resp_fixture", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7},
		}})
	}))
	defer server.Close()
	for _, tc := range []struct{ transport, mode string }{
		{"app-server", "full_access"}, {"app-server", "workspace_autonomous"}, {"exec", "workspace_autonomous"},
	} {
		t.Run(tc.transport+"/"+tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CODEX_HOME", dir)
			config := "model_provider = \"fixture\"\n[model_providers.fixture]\nname = \"Fixture\"\nbase_url = \"" + server.URL +
				"/v1\"\nwire_api = \"responses\"\nsupports_websockets = false\nrequires_openai_auth = false\n"
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			request := testRequest()
			request.Executor, request.Model, request.ExecutionMode = "codex", "fixture", tc.mode
			request.ReadOnly = true
			if tc.transport == "app-server" {
				request.AuthorizeNative = denyResumeFixtureCall
			}
			checkInstalledResume(t, request)
		})
	}
}
