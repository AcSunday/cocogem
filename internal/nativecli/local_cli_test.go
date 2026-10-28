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
	t.Setenv("CODEX_HOME", t.TempDir())
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
	request.Tools = append(request.Tools, contextProbeTools()...)
	request.RequiredTools = []string{"read_file", "read_context", "read_source"}
	request.FirstResponseTimeout = 30 * time.Second
	request.StallTimeout = 30 * time.Second
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
	if len(requests) < 2 {
		t.Fatal("native binary did not complete a tool call and model continuation")
	}
	seen := map[string]bool{}
	for len(requests) > 0 {
		names := <-requests
		for _, name := range names {
			if name == "mcp__cocogem__read_file" || name == "mcp__cocogem__read_context" || name == "mcp__cocogem__read_source" {
				seen[name] = true
				continue
			}
			if name != "list_mcp_resources" && name != "list_mcp_resource_templates" && name != "read_mcp_resource" {
				t.Errorf("unexpected native tool %q in registry %s", name, strings.Join(names, ","))
			}
		}
	}
	for _, name := range []string{"mcp__cocogem__read_file", "mcp__cocogem__read_context", "mcp__cocogem__read_source"} {
		if !seen[name] {
			t.Fatalf("host tool %s missing from the Codex tool menu", name)
		}
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
		chosen := ""
		for _, name := range names {
			if strings.HasSuffix(name, "_read_file") {
				chosen = name
			}
		}
		if turns.Add(1) == 1 && chosen != "" {
			raw, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": chosen, "args": map[string]any{"path": "README.md"}}}}}, "finishReason": "STOP", "index": 0}}, "usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 4, "totalTokenCount": 7}})
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
	request.Tools = append(request.Tools, contextProbeTools()...)
	request.RequiredTools = []string{"read_file", "read_context", "read_source"}
	request.FirstResponseTimeout = 30 * time.Second
	request.StallTimeout = 30 * time.Second
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
	seen := map[string]bool{}
	for {
		select {
		case names := <-requests:
			for _, name := range names {
				if !strings.HasPrefix(name, "mcp_cocogem_") {
					t.Errorf("unexpected native Gemini tool %q", name)
					continue
				}
				for _, suffix := range []string{"_read_file", "_read_context", "_read_source"} {
					if strings.HasSuffix(name, suffix) {
						seen[suffix] = true
					}
				}
			}
		default:
			for _, suffix := range []string{"_read_file", "_read_context", "_read_source"} {
				if !seen[suffix] {
					t.Fatalf("host MCP tool %s missing from the Gemini tool menu", suffix)
				}
			}
			return
		}
	}
}

// TestInstalledClaudeToolRegistry uses the installed Claude binary with a
// local Anthropic-protocol fixture; no provider credential or model is used.
func TestInstalledClaudeToolRegistry(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CLAUDE") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CLAUDE=1 for a local provider-free binary check")
	}
	requests := make(chan []string, 8)
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"input_tokens":1}`)
			return
		}
		var input struct {
			Stream bool `json:"stream"`
			Tools  []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		names := []string{}
		for _, tool := range input.Tools {
			names = append(names, tool.Name)
		}
		requests <- names
		w.Header().Set("Content-Type", "text/event-stream")
		if turns.Add(1) == 1 && len(names) > 0 {
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_public\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"local\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_public\",\"name\":\"mcp__cocogem__read_file\",\"input\":{}}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":4}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_public_2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"local\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"public fixture answer\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":4}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "cocogem-local-dummy")
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	t.Setenv("ANTHROPIC_MODEL", "claude-local-fixture")
	request := testRequest()
	request.Executor = "claude"
	request.Model = "default"
	request.Tools = append(request.Tools, contextProbeTools()...)
	request.RequiredTools = []string{"read_file", "read_context", "read_source"}
	request.FirstResponseTimeout = 30 * time.Second
	request.StallTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var calls atomic.Int32
	err := Provider{}.Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		calls.Add(1)
		return `{"source":"public fixture"}`, nil
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("native loop host calls=%d", calls.Load())
	}
	seen := map[string]bool{}
	for len(requests) > 0 {
		names := <-requests
		for _, name := range names {
			if name == "DeferredToolPlaceholder" {
				continue
			}
			if !strings.HasPrefix(name, "mcp__cocogem__") {
				t.Errorf("unexpected native Claude tool %q in %v", name, names)
				continue
			}
			seen[name] = true
		}
	}
	for _, name := range []string{"mcp__cocogem__read_file", "mcp__cocogem__read_context", "mcp__cocogem__read_source"} {
		if !seen[name] {
			t.Fatalf("host tool %s missing from the Claude tool menu", name)
		}
	}
}

func TestInstalledIsolationProbes(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_ISOLATION") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_ISOLATION=1 for installed isolation probes")
	}
	for _, id := range []string{"claude", "codex", "gemini"} {
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			info, err := Check(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Installed {
				t.Skip("CLI is not installed")
			}
			if !info.Supported || info.Isolation != "enforced" {
				t.Fatalf("isolation probe did not pass: %+v", info)
			}
		})
	}
}
