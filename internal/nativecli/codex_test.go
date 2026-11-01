package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexResourceHelpersContinueOnlyAfterConfirmedEmptyOrRefusedResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		server    string
		tool      string
		arguments string
		status    string
		result    string
		failure   string
	}{
		{"empty discovery", "codex", "list_mcp_resources", `{}`, "completed", `{"content":[{"type":"text","text":"{\"resources\":[]}"}],"structured_content":null}`, `null`},
		{"empty templates", "codex", "list_mcp_resource_templates", `{}`, "completed", `{"content":[{"type":"text","text":"{\"resourceTemplates\":[]}"}],"structured_content":null}`, `null`},
		{"refused list", "cocogem", "list_mcp_resources", `{"server":"cocogem"}`, "failed", `null`, `{"message":"resources/list failed: resources/list failed for ` + "`cocogem`" + `: Mcp error: -32601: method not found"}`},
		{"refused read", "cocogem", "read_mcp_resource", `{"server":"cocogem","uri":"file:///private/fixture"}`, "failed", `null`, `{"message":"resources/read failed: resources/read failed for ` + "`cocogem`" + ` (file:///private/fixture): Mcp error: -32601: method not found"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := fmt.Sprintf(`"id":"resource-1","type":"mcp_tool_call","server":%q,"tool":%q,"arguments":%s`, tc.server, tc.tool, tc.arguments)
			stream := `{"type":"thread.started","thread_id":"` + nativeSession + `"}` + "\n" + `{"type":"turn.started"}` + "\n" +
				`{"type":"item.started","item":{` + item + `,"status":"in_progress","result":null,"error":null}}` + "\n" +
				`{"type":"item.completed","item":{` + item + `,"status":"` + tc.status + `","result":` + tc.result + `,"error":` + tc.failure + `}}` + "\n" +
				`{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"continue with host tools"}}` + "\n" + `{"type":"turn.completed"}` + "\n"
			events, err := parseWithEvents(t, nil, stream, parseCodex)
			if err != nil {
				t.Fatal(err)
			}
			var notices, replies int
			for _, event := range events {
				if event.kind == "tool_started" || event.kind == "tool_finished" {
					t.Fatal("resource discovery must not be recorded as an executed host tool")
				}
				if event.kind == "native_notice" {
					notices++
					if strings.Contains(fmt.Sprint(event.data), "file://") {
						t.Fatal("resource arguments leaked into a notice")
					}
				}
				if event.kind == "text" {
					replies++
				}
			}
			if notices != 1 || replies != 1 {
				t.Fatalf("notices=%d replies=%d", notices, replies)
			}
		})
	}
}

func TestCodexResourceHelpersFailClosed(t *testing.T) {
	t.Parallel()
	item := func(id, server, tool, args, status, result, failure string) string {
		return fmt.Sprintf(`{"type":"item.completed","item":{"id":%q,"type":"mcp_tool_call","server":%q,"tool":%q,"arguments":%s,"status":%q,"result":%s,"error":%s}}`, id, server, tool, args, status, result, failure)
	}
	empty := `{"content":[{"type":"text","text":"{\"resources\":[]}"}],"structured_content":null}`
	for _, tc := range []struct {
		name string
		body string
	}{
		{"unknown server", item("a", "external", "list_mcp_resources", `{"server":"external"}`, "completed", empty, "null")},
		{"mismatched target", item("a", "codex", "list_mcp_resources", `{"server":"external"}`, "completed", empty, "null")},
		{"unknown tool", item("a", "cocogem", "unknown_tool", `{}`, "completed", empty, "null")},
		{"resource read succeeded", item("a", "cocogem", "read_mcp_resource", `{"server":"cocogem","uri":"fixture://secret"}`, "completed", empty, "null")},
		{"nonempty discovery", item("a", "codex", "list_mcp_resources", `{}`, "completed", `{"content":[{"type":"text","text":"{\"resources\":[{\"uri\":\"fixture://secret\"}]}"}]}`, "null")},
		{"discovery cursor", item("a", "codex", "list_mcp_resources", `{}`, "completed", `{"content":[{"type":"text","text":"{\"resources\":[],\"nextCursor\":\"secret\"}"}]}`, "null")},
		{"unrecognized refusal", item("a", "cocogem", "read_mcp_resource", `{"server":"cocogem","uri":"fixture://secret"}`, "failed", "null", `{"message":"timeout"}`)},
		{"unexpected arguments", item("a", "codex", "list_mcp_resources", `{"path":"private"}`, "completed", empty, "null")},
		{"structured result", item("a", "codex", "list_mcp_resources", `{}`, "completed", `{"content":[{"type":"text","text":"{\"resources\":[]}"}],"structured_content":{"secret":"private"}}`, "null")},
		{"unknown result field", item("a", "codex", "list_mcp_resources", `{}`, "completed", `{"content":[{"type":"text","text":"{\"resources\":[]}"}],"unknown":"private"}`, "null")},
		{"unfinished helper", `{"type":"item.started","item":{"id":"a","type":"mcp_tool_call","server":"codex","tool":"list_mcp_resources","arguments":{}}}`},
		{"changed identity", `{"type":"item.started","item":{"id":"a","type":"mcp_tool_call","server":"codex","tool":"list_mcp_resources","arguments":{}}}` + "\n" + item("a", "codex", "list_mcp_resource_templates", `{}`, "completed", empty, "null")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := `{"type":"thread.started","thread_id":"` + nativeSession + `"}` + "\n" + `{"type":"turn.started"}` + "\n" + tc.body + "\n" + `{"type":"turn.completed"}` + "\n"
			if _, err := parseWithEvents(t, nil, stream, parseCodex); err == nil || FailureKindOf(err) != FailureProtocol {
				t.Fatalf("unexpected resource action was accepted: %v", err)
			}
		})
	}
}

// The installed CLI talks only to this local fake model and the scoped bridge.
// It must continue from resource discovery to an actual granted host tool.
func TestInstalledCodexMCPResourceHelpers(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a local provider-free binary check")
	}
	for _, tc := range []struct{ name, tool, arguments string }{
		{"list_all", "list_mcp_resources", `{}`},
		{"list_templates_all", "list_mcp_resource_templates", `{}`},
		{"list_scoped", "list_mcp_resources", `{"server":"cocogem"}`},
		{"templates_scoped", "list_mcp_resource_templates", `{"server":"cocogem"}`},
		{"read_scoped", "read_mcp_resource", `{"server":"cocogem","uri":"fixture://unavailable"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var turns, feedback atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Input []struct {
						Type string `json:"type"`
					} `json:"input"`
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil {
					http.Error(w, "invalid JSON", http.StatusBadRequest)
					return
				}
				for _, item := range input.Input {
					if item.Type == "function_call_output" {
						feedback.Add(1)
					}
				}
				item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "public fixture answer", "annotations": []any{}}}}
				switch turns.Add(1) {
				case 1:
					item = map[string]any{"id": "fc_resource", "type": "function_call", "name": tc.tool, "call_id": "call_resource", "arguments": tc.arguments}
				case 2:
					item = map[string]any{"id": "fc_host", "type": "function_call", "namespace": "mcp__cocogem", "name": "read_file", "call_id": "call_host", "arguments": `{"path":"README.md"}`}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range []map[string]any{{"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}}}} {
					raw, _ := json.Marshal(event)
					fmt.Fprintf(w, "data: %s\n\n", raw)
				}
			}))
			defer server.Close()
			request := testRequest()
			request.Executor = "codex"
			request.RequiredTools = []string{"read_file"}
			options := runOptions{skipCheck: true, codexProbe: true}
			for _, config := range []string{`model_provider="fixture"`, `model_providers.fixture.name="Local fixture"`, `model_providers.fixture.base_url="` + server.URL + `/v1"`, `model_providers.fixture.wire_api="responses"`, `model_providers.fixture.requires_openai_auth=false`, `model_providers.fixture.supports_websockets=false`} {
				options.codexArgs = append(options.codexArgs, "-c", config)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			var calls, notices, replies int
			err := (Provider{}).run(ctx, request, func(_ context.Context, name string, _ json.RawMessage) (string, error) {
				if name != "read_file" {
					t.Errorf("unexpected host tool: %s", name)
				}
				calls++
				return `{"source":"public fixture"}`, nil
			}, func(kind string, data map[string]any) error {
				if kind == "native_notice" && data["kind"] == "codex.resource_unavailable" {
					notices++
				}
				if kind == "text" {
					replies++
				}
				return nil
			}, options)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || notices != 1 || replies != 1 || turns.Load() != 3 || feedback.Load() < 2 {
				t.Fatalf("calls=%d notices=%d replies=%d turns=%d feedback=%d", calls, notices, replies, turns.Load(), feedback.Load())
			}
		})
	}
}
