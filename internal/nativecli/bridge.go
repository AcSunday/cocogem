package nativecli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type bridge struct {
	ctx          context.Context
	server       *http.Server
	addr         string
	token        string
	tools        []model.Tool
	schemas      map[string]*jsonschema.Schema
	execute      agent.Execute
	observe      agent.Observe
	mu           sync.Mutex
	calls        int
	err          error
	stop         context.CancelFunc
	closed       bool
	nativePrefix string
	usageKnown   bool
	inputTokens  int
	outputTokens int
}

func newBridge(ctx context.Context, tools []model.Tool, execute agent.Execute, observe agent.Observe) (*bridge, error) {
	b := &bridge{ctx: ctx, tools: tools, execute: execute, observe: observe, schemas: map[string]*jsonschema.Schema{}}
	for _, tool := range tools {
		if tool.Name == "" || b.schemas[tool.Name] != nil {
			return nil, errors.New("duplicate or empty native tool name")
		}
		compiler := jsonschema.NewCompiler()
		resource := "https://cocogem.local/tool.json"
		encoded, err := json.Marshal(tool.Schema)
		if err != nil {
			return nil, errors.New("invalid host tool schema")
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
		if err != nil {
			return nil, errors.New("invalid host tool schema")
		}
		if err := compiler.AddResource(resource, value); err != nil {
			return nil, errors.New("invalid host tool schema")
		}
		schema, err := compiler.Compile(resource)
		if err != nil {
			return nil, errors.New("invalid host tool schema")
		}
		b.schemas[tool.Name] = schema
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	b.token = hex.EncodeToString(secret[:])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("open scoped MCP listener")
	}
	b.addr = listener.Addr().String()
	b.server = &http.Server{Handler: b, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}

func (b *bridge) url() string { return "http://" + b.addr + "/mcp" }

func (b *bridge) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	_ = b.server.Close()
}

func (b *bridge) failure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

func (b *bridge) emit(kind string, data map[string]any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.emitLocked(kind, data)
}

func (b *bridge) emitLocked(kind string, data map[string]any) error {
	if b.err != nil {
		return b.err
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if err := b.observe(kind, data); err != nil {
		b.err = err
		if b.stop != nil {
			b.stop()
		}
		return err
	}
	return nil
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Meta    json.RawMessage `json:"_meta"`
}

type tokenUsage struct {
	Input  *int `json:"input_tokens"`
	Output *int `json:"output_tokens"`
}

func (b *bridge) recordUsage(usage tokenUsage) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if usage.Input == nil || usage.Output == nil {
		return nil
	}
	if *usage.Input < 0 || *usage.Output < 0 {
		return errors.New("native CLI returned invalid token usage")
	}
	b.usageKnown, b.inputTokens, b.outputTokens = true, *usage.Input, *usage.Output
	return nil
}

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != b.addr || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+b.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path != "/mcp" || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "expected JSON", http.StatusUnsupportedMediaType)
		return
	}
	var request rpcRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 70*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF || request.JSONRPC != "2.0" {
		http.Error(w, "invalid MCP request", http.StatusBadRequest)
		return
	}
	if len(request.ID) == 0 {
		if request.Method == "notifications/initialized" || request.Method == "notifications/cancelled" {
			w.WriteHeader(http.StatusAccepted)
		} else {
			http.Error(w, "invalid notification", http.StatusBadRequest)
		}
		return
	}
	var result any
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		version := "2025-03-26"
		if params.ProtocolVersion == "2024-11-05" || params.ProtocolVersion == "2025-06-18" || params.ProtocolVersion == "2025-11-25" {
			version = params.ProtocolVersion
		}
		result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "cocogem", "version": "0.1.0"}}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		list := make([]any, 0, len(b.tools))
		for _, tool := range b.tools {
			list = append(list, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.Schema})
		}
		result = map[string]any{"tools": list}
	case "tools/call":
		result = b.call(request.Params)
	default:
		writeRPC(w, request.ID, nil, map[string]any{"code": -32601, "message": "method not found"})
		return
	}
	writeRPC(w, request.ID, result, nil)
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result, rpcError any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcError != nil {
		response["error"] = rpcError
	} else {
		response["result"] = result
	}
	_ = json.NewEncoder(w).Encode(response)
}

func toolResult(output string, failed bool) any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": output}}, "isError": failed}
}

func (b *bridge) call(raw json.RawMessage) any {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.err != nil || b.ctx.Err() != nil {
		return toolResult("This step is stopped.", true)
	}
	b.calls++
	if b.calls > 30 {
		b.err = errors.New("native CLI tool-call budget exceeded")
		if b.stop != nil {
			b.stop()
		}
		return toolResult("Tool-call budget exceeded.", true)
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&params); err != nil || d.Decode(new(any)) != io.EOF || len(params.Arguments) > 65536 {
		return toolResult("Invalid or oversized tool arguments.", true)
	}
	schema := b.schemas[params.Name]
	if schema == nil {
		return toolResult("Tool is not allowed for this member.", true)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(params.Arguments))
	if err != nil || schema.Validate(value) != nil {
		return toolResult("Tool arguments do not match the host schema.", true)
	}
	id := fmt.Sprintf("native-%d", b.calls)
	if err := b.emitLocked("tool_started", map[string]any{"tool_call_id": id, "name": params.Name, "input": params.Arguments}); err != nil {
		return toolResult("Could not persist tool start.", true)
	}
	started := time.Now()
	output, callErr := b.execute(b.ctx, params.Name, params.Arguments)
	status := "ok"
	if callErr != nil {
		status, output = "error", "Tool error: "+callErr.Error()
	}
	if len(output) > 65536 {
		b.err = errors.New("native CLI tool observation exceeds 64 KiB")
		if b.stop != nil {
			b.stop()
		}
		return toolResult("Tool observation exceeds 64 KiB.", true)
	}
	if err := b.emitLocked("tool_finished", map[string]any{"tool_call_id": id, "name": params.Name, "status": status, "output": output, "duration_ms": time.Since(started).Milliseconds()}); err != nil {
		return toolResult("Could not persist tool result.", true)
	}
	return toolResult(output, callErr != nil)
}
