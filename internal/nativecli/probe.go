package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

type probeResult struct {
	passed bool
	reason string
}

type probeEntry struct {
	done    chan struct{}
	result  probeResult
	expires time.Time
}

var probeCache = struct {
	sync.Mutex
	entries map[string]*probeEntry
}{entries: map[string]*probeEntry{}}

// Check proves installed CLI protocol and tool-boundary behavior with a local
// fake model. Help-only inspection never claims isolation by itself.
func Check(ctx context.Context, id string) (agent.AdapterInfo, error) {
	info, err := check(ctx, id, nil)
	if err != nil {
		return info, err
	}
	if !info.Supported || info.Identity == "" {
		return info, nil
	}
	result, err := probeIsolation(ctx, info)
	if err != nil {
		return info, err
	}
	applyProbeResult(&info, result)
	return info, nil
}

func applyProbeCache(info agent.AdapterInfo) agent.AdapterInfo {
	probeCache.Lock()
	entry := probeCache.entries[probeKey(info)]
	if entry != nil {
		select {
		case <-entry.done:
			if entry.result.passed || time.Now().Before(entry.expires) {
				applyProbeResult(&info, entry.result)
			}
		default:
		}
	}
	probeCache.Unlock()
	return info
}

func applyProbeResult(info *agent.AdapterInfo, result probeResult) {
	if result.passed {
		info.Supported = true
		info.Isolation = "enforced"
		info.Reason = ""
		return
	}
	info.Supported = false
	info.Isolation = "unavailable"
	info.Reason = "工具隔离探测未通过: " + result.reason
}

func probeKey(info agent.AdapterInfo) string {
	if info.ID == "" || info.Identity == "" {
		return ""
	}
	return info.ID + "\x00" + info.Identity
}

func probeIsolation(ctx context.Context, info agent.AdapterInfo) (probeResult, error) {
	key := probeKey(info)
	if key == "" {
		return probeResult{reason: "program identity is unavailable"}, nil
	}
	probeCache.Lock()
	if entry := probeCache.entries[key]; entry != nil {
		select {
		case <-entry.done:
			if entry.result.passed || time.Now().Before(entry.expires) {
				result := entry.result
				probeCache.Unlock()
				return result, nil
			}
			delete(probeCache.entries, key)
		default:
			probeCache.Unlock()
			select {
			case <-entry.done:
				return entry.result, nil
			case <-ctx.Done():
				return probeResult{}, ctx.Err()
			}
		}
	}
	entry := &probeEntry{done: make(chan struct{})}
	probeCache.entries[key] = entry
	probeCache.Unlock()

	result := runIsolationProbe(ctx, info)
	if ctx.Err() != nil {
		probeCache.Lock()
		delete(probeCache.entries, key)
		entry.result = probeResult{reason: "probe interrupted"}
		probeCache.Unlock()
		close(entry.done)
		return probeResult{}, ctx.Err()
	}
	entry.result = result
	if !result.passed {
		entry.expires = time.Now().Add(30 * time.Second)
	}
	close(entry.done)
	return result, nil
}

// contextProbeTools mirror the workflow-owned retrieval menu so the isolation
// probe proves each CLI exposes bounded context reads, not only one tool.
func contextProbeTools() []model.Tool {
	return []model.Tool{
		{Name: "read_context", Description: "Read one bounded page of a frozen message or artifact version", Schema: map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}, "version": map[string]any{"type": "integer"}, "cursor": map[string]any{"type": "string"}, "max_bytes": map[string]any{"type": "integer"}}, "required": []string{"kind", "id"}, "additionalProperties": false}},
		{Name: "read_source", Description: "Read one bounded page of an exact captured source", Schema: map[string]any{"type": "object", "properties": map[string]any{"source_id": map[string]any{"type": "string"}, "cursor": map[string]any{"type": "string"}, "max_bytes": map[string]any{"type": "integer"}}, "required": []string{"source_id"}, "additionalProperties": false}},
	}
}

func contextProbeRequired() []string {
	return []string{"probe", "read_context", "read_source"}
}

func runIsolationProbe(ctx context.Context, info agent.AdapterInfo) probeResult {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	server, err := newProbeServer(info.ID)
	if err != nil {
		return probeResult{reason: err.Error()}
	}
	defer server.close()

	var sessionSeen, textSeen atomic.Bool
	var toolCalls atomic.Int32
	observe := func(kind string, _ map[string]any) error {
		switch kind {
		case "native_session":
			sessionSeen.Store(true)
		case "text":
			textSeen.Store(true)
		}
		return nil
	}
	execute := func(context.Context, string, json.RawMessage) (string, error) {
		toolCalls.Add(1)
		return `{"probe":"ok"}`, nil
	}
	tools := append([]model.Tool{{Name: "probe", Description: "Synthetic host isolation probe", Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}, contextProbeTools()...)
	request := mcpOnly(model.Request{
		Executor:             info.ID,
		Model:                "cocogem-probe",
		System:               "Call the probe tool once, then reply with one short sentence.",
		Messages:             []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Synthetic isolation probe. Use only the supplied probe tool."}}}},
		Tools:                tools,
		RequiredTools:        contextProbeRequired(),
		MaxTokens:            32,
		FirstResponseTimeout: 30 * time.Second,
		StallTimeout:         30 * time.Second,
		StallAction:          "stop",
	})
	options := runOptions{skipCheck: true, adapter: info, model: "cocogem-probe"}
	switch info.ID {
	case "claude":
		options.claudeSettings = map[string]any{
			"disableAllHooks": true,
			"enabledPlugins":  map[string]any{},
			"env": map[string]string{
				"ANTHROPIC_AUTH_TOKEN": "cocogem-probe-token",
				"ANTHROPIC_BASE_URL":   server.url,
				"ANTHROPIC_MODEL":      "cocogem-probe",
			},
		}
		options.claudeEnv = map[string]string{
			"ANTHROPIC_AUTH_TOKEN": "cocogem-probe-token",
			"ANTHROPIC_BASE_URL":   server.url,
			"ANTHROPIC_MODEL":      "cocogem-probe",
		}
	case "codex":
		options.codexProbe = true
		options.codexArgs = []string{
			"-c", `model_provider="cocogem_probe"`,
			"-c", `model_providers.cocogem_probe.name="Cocogem probe"`,
			"-c", `model_providers.cocogem_probe.base_url="` + server.url + `/v1"`,
			"-c", `model_providers.cocogem_probe.wire_api="responses"`,
			"-c", `model_providers.cocogem_probe.requires_openai_auth=false`,
			"-c", `model_providers.cocogem_probe.supports_websockets=false`,
		}
	case "gemini":
		options.geminiEnv = map[string]string{
			"GEMINI_API_KEY":            "cocogem-probe-token",
			"GOOGLE_API_KEY":            "cocogem-probe-token",
			"GOOGLE_GEMINI_BASE_URL":    server.url,
			"GEMINI_CLI_AUTH_OVERRIDE":  "gemini-api-key",
			"GOOGLE_GENAI_USE_VERTEXAI": "false",
			"GOOGLE_GENAI_USE_GCA":      "false",
		}
	}
	err = (Provider{}).run(ctx, request, execute, observe, options)
	if violation := server.failure(); violation != "" {
		return probeResult{reason: "native CLI declared an unexpected model tool: " + violation}
	}
	if err != nil {
		return probeResult{reason: err.Error()}
	}
	if !sessionSeen.Load() {
		return probeResult{reason: "native session initialization was not observed"}
	}
	if toolCalls.Load() == 0 {
		return probeResult{reason: "required host tool was not exercised"}
	}
	if !textSeen.Load() {
		return probeResult{reason: "native visible output was not observed"}
	}
	return probeResult{passed: true}
}

type probeServer struct {
	url       string
	server    *http.Server
	ln        net.Listener
	id        string
	turn      atomic.Int32
	mu        sync.Mutex
	violation string
}

func (p *probeServer) fail(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.violation == "" {
		p.violation = reason
	}
}

func (p *probeServer) failure() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.violation
}

func newProbeServer(id string) (*probeServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open local probe listener: %w", err)
	}
	probe := &probeServer{url: "http://" + ln.Addr().String(), ln: ln, id: id}
	mux := http.NewServeMux()
	mux.HandleFunc("/", probe.handle)
	probe.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = probe.server.Serve(ln) }()
	return probe, nil
}

func (p *probeServer) close() {
	_ = p.server.Close()
	_ = p.ln.Close()
}

func (p *probeServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "probe", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err != nil {
		http.Error(w, "probe body", http.StatusBadRequest)
		return
	}

	if strings.Contains(r.URL.Path, "count_tokens") || strings.Contains(r.URL.Path, "countTokens") {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"input_tokens":1,"totalTokens":1}`)
		return
	}
	p.validateTools(body)
	turn := p.turn.Add(1)
	switch p.id {
	case "claude":
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			writeClaudeTool(w, "mcp__cocogem__probe")
		} else {
			writeClaudeText(w)
		}
	case "codex":
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			writeCodexTool(w)
		} else {
			writeCodexText(w)
		}
	case "gemini":
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			writeGeminiTool(w, body)
		} else {
			writeGeminiText(w)
		}
	default:
		http.Error(w, "probe", http.StatusBadRequest)
	}
}

func probeToolName(name string) bool {
	return name == "probe" || name == "read_context" || name == "read_source"
}

func (p *probeServer) validateTools(body []byte) {
	switch p.id {
	case "claude":
		var payload struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return
		}
		for _, tool := range payload.Tools {
			if tool.Name == "" || tool.Name == "DeferredToolPlaceholder" {
				continue
			}
			if !strings.HasPrefix(tool.Name, "mcp__cocogem__") || !probeToolName(strings.TrimPrefix(tool.Name, "mcp__cocogem__")) {
				p.fail(tool.Name)
			}
		}
	case "codex":
		var payload struct {
			Tools []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Tools []struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"tools"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return
		}
		for _, tool := range payload.Tools {
			if tool.Type == "namespace" && tool.Name == "mcp__cocogem" {
				for _, nested := range tool.Tools {
					if !probeToolName(nested.Name) {
						p.fail("mcp__cocogem__" + nested.Name)
					}
				}
				continue
			}
			if probeToolName(tool.Name) || tool.Name == "list_mcp_resources" || tool.Name == "list_mcp_resource_templates" || tool.Name == "read_mcp_resource" {
				continue
			}
			p.fail(tool.Name)
		}
	case "gemini":
		var payload struct {
			Tools []struct {
				FunctionDeclarations []struct {
					Name string `json:"name"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return
		}
		for _, tool := range payload.Tools {
			for _, declaration := range tool.FunctionDeclarations {
				name := declaration.Name
				if !strings.HasSuffix(name, "_probe") && !strings.HasSuffix(name, "_read_context") && !strings.HasSuffix(name, "_read_source") {
					p.fail(name)
				}
			}
		}
	}
}

func writeClaudeTool(w http.ResponseWriter, name string) {
	fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_probe\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"cocogem-probe\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
	fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_probe\",\"name\":%q,\"input\":{}}}\n\n", name)
	fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n")
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
	fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func writeClaudeText(w http.ResponseWriter) {
	fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_probe_done\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"cocogem-probe\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
	fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"isolation probe complete\"}}\n\n")
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
	fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func writeCodexTool(w http.ResponseWriter) {
	item := map[string]any{"id": "fc_probe", "type": "function_call", "name": "probe", "namespace": "mcp__cocogem", "call_id": "call_probe", "arguments": "{}"}
	writeSSEJSON(w, map[string]any{"type": "response.output_item.done", "item": item})
	writeSSEJSON(w, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_probe", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
}

func writeCodexText(w http.ResponseWriter) {
	item := map[string]any{"id": "msg_probe_done", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "isolation probe complete", "annotations": []any{}}}}
	writeSSEJSON(w, map[string]any{"type": "response.output_item.done", "item": item})
	writeSSEJSON(w, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_probe_done", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
}

func writeGeminiTool(w http.ResponseWriter, body []byte) {
	var input struct {
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(body, &input)
	name := ""
	for _, tool := range input.Tools {
		for _, declaration := range tool.FunctionDeclarations {
			if strings.HasSuffix(declaration.Name, "_probe") || strings.Contains(declaration.Name, "probe") {
				name = declaration.Name
			}
		}
	}
	if name == "" {
		writeGeminiText(w)
		return
	}
	writeSSEJSON(w, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": name, "args": map[string]any{}}}}}, "finishReason": "STOP", "index": 0}}, "usageMetadata": map[string]any{"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}})
}

func writeGeminiText(w http.ResponseWriter) {
	writeSSEJSON(w, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "isolation probe complete"}}}, "finishReason": "STOP", "index": 0}}, "usageMetadata": map[string]any{"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}})
}

func writeSSEJSON(w http.ResponseWriter, value any) {
	raw, _ := json.Marshal(value)
	fmt.Fprintf(w, "data: %s\n\n", raw)
}
