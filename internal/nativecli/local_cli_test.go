package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This opt-in check exercises the installed binary against a local fake model, never a real provider.
func TestInstalledCodexToolRegistry(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a local provider-free binary check")
	}
	requests := make(chan []string, 4)
	feedback := make(chan string, 8)
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []struct {
				Type   string          `json:"type"`
				Output json.RawMessage `json:"output"`
			} `json:"input"`
			Tools []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		for _, item := range input.Input {
			if item.Type == "function_call_output" {
				feedback <- string(item.Output)
			}
		}
		var names []string
		for _, tool := range input.Tools {
			if tool.Type == "namespace" {
				for _, nested := range tool.Tools {
					names = append(names, tool.Name+"__"+nested.Name)
				}
			} else {
				names = append(names, tool.Name)
			}
		}
		requests <- names
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"id": "msg_public", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "public fixture answer", "annotations": []any{}}}}
		if turns.Add(1) == 1 {
			item = map[string]any{"id": "fc_public", "type": "function_call", "name": "read_file", "namespace": "mcp__cocogem", "call_id": "call_public", "arguments": `{"path":"README.md"}`}
		}
		for _, event := range []map[string]any{{"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": "resp_public", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}}}} {
			raw, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}))
	defer server.Close()
	p := Provider{command: func(id string, args ...string) *exec.Cmd {
		if len(args) > 0 && args[0] == "exec" && args[len(args)-1] != "--help" {
			for _, value := range []string{"model_provider=\"cocogem_local\"", "model_providers.cocogem_local.name=\"Local configuration check\"", "model_providers.cocogem_local.base_url=\"" + server.URL + "/v1\"", "model_providers.cocogem_local.wire_api=\"responses\"", "model_providers.cocogem_local.requires_openai_auth=false", "model_providers.cocogem_local.supports_websockets=false"} {
				args = append(args, "-c", value)
			}
		}
		return exec.Command(id, args...)
	}}
	request := testRequest()
	request.Executor = "codex"
	request.Model = "cocogem-local-fixture"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var calls atomic.Int32
	err := p.Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		calls.Add(1)
		return `{"source":"public fixture"}`, nil
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		var details []string
		for len(feedback) > 0 {
			details = append(details, <-feedback)
		}
		t.Fatalf("native loop host calls=%d feedback=%v", calls.Load(), details)
	}
	select {
	case names := <-requests:
		found := false
		for _, name := range names {
			if name == "mcp__cocogem__read_file" {
				found = true
				continue
			}
			if name != "list_mcp_resources" && name != "list_mcp_resource_templates" && name != "read_mcp_resource" {
				t.Errorf("unexpected native tool %q in registry %s", name, strings.Join(names, ","))
			}
		}
		if !found {
			t.Fatalf("host MCP tool missing from registry %v", names)
		}
	default:
		t.Fatal("native binary never contacted the local fixture provider")
	}
}

func TestInstalledGeminiToolRegistry(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_GEMINI") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_GEMINI=1 for a local provider-free binary check")
	}
	requests := make(chan []string, 8)
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "countTokens") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"totalTokens":1}`)
			return
		}
		var input struct {
			Tools []struct {
				Declarations []struct {
					Name string `json:"name"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		var names []string
		for _, tool := range input.Tools {
			for _, fn := range tool.Declarations {
				names = append(names, fn.Name)
			}
		}
		requests <- names
		w.Header().Set("Content-Type", "text/event-stream")
		if turns.Add(1) == 1 && len(names) > 0 {
			raw, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": names[0], "args": map[string]any{"path": "README.md"}}}}}, "finishReason": "STOP", "index": 0}}, "usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 4, "totalTokenCount": 7}})
			fmt.Fprintf(w, "data: %s\n\n", raw)
			return
		}
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"public fixture answer\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4,\"totalTokenCount\":7}}\n\n")
	}))
	defer server.Close()
	p := Provider{command: func(id string, args ...string) *exec.Cmd {
		cmd := exec.Command(id, args...)
		cmd.Env = overrideEnv(os.Environ(), map[string]string{"GEMINI_API_KEY": "cocogem-local-dummy", "GOOGLE_API_KEY": "cocogem-local-dummy", "GOOGLE_GEMINI_BASE_URL": server.URL, "GEMINI_CLI_AUTH_OVERRIDE": "gemini-api-key", "GOOGLE_GENAI_USE_VERTEXAI": "false", "GOOGLE_GENAI_USE_GCA": "false"})
		return cmd
	}}
	request := testRequest()
	request.Executor = "gemini"
	request.Model = "gemini-local-fixture"
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var calls atomic.Int32
	err := p.Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		calls.Add(1)
		return `{"source":"public fixture"}`, nil
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("native loop host calls=%d", calls.Load())
	}
	found := false
	for {
		select {
		case names := <-requests:
			for _, name := range names {
				if strings.HasPrefix(name, "mcp_cocogem_") && strings.HasSuffix(name, "_read_file") {
					found = true
				} else {
					t.Errorf("unexpected native Gemini tool %q", name)
				}
			}
		default:
			if !found {
				t.Fatal("host MCP tool missing from local Gemini provider request")
			}
			return
		}
	}
}
