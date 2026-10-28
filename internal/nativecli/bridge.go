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
	ctx           context.Context
	server        *http.Server
	addr          string
	token         string
	tools         []model.Tool
	schemas       map[string]*jsonschema.Schema
	execute       agent.Execute
	observe       agent.Observe
	mu            sync.Mutex
	calls         int
	err           error
	observeFailed bool
	stop          context.CancelFunc
	closed        bool
	nativePrefix  string
	usageKnown    bool
	inputTokens   int
	outputTokens  int
	inputKnown    bool
	outputKnown   bool
	required      []string
	listed        map[string]bool
	listSeen      bool
	native        map[string]bool
	live          *liveness
	notices       int
	lastInputKey  string
	inputRepeats  int
	repeatNotice  bool
	activityAt    time.Time
	now           func() time.Time
}

// activityInterval bounds metadata-only activity signals; the first one in an
// attempt is always sent.
const activityInterval = time.Second

// These are the complete native_activity kinds. The signal never carries
// reasoning or text content.
const (
	activityReasoning = "reasoning"
	activityTextDelta = "text_delta"
)

func newBridge(ctx context.Context, tools []model.Tool, execute agent.Execute, observe agent.Observe) (*bridge, error) {
	b := &bridge{ctx: ctx, tools: tools, execute: execute, observe: observe, schemas: map[string]*jsonschema.Schema{}, listed: map[string]bool{}, native: map[string]bool{}}
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

func (b *bridge) setRequired(tools []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.required = append([]string{}, tools...)
}

// setNative records the exact CLI-owned tools this step may expose. The
// bridge executes none of them; the CLI does, under its own permissions.
func (b *bridge) setNative(names []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.native = map[string]bool{}
	for _, name := range names {
		b.native[name] = true
	}
}

// allowedToolName covers both host MCP tools and the ordered native subset.
func (b *bridge) allowedToolName(name string) bool {
	const prefix = "mcp__cocogem__"
	if strings.HasPrefix(name, prefix) {
		return b.schemas[strings.TrimPrefix(name, prefix)] != nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.native[name]
}

func (b *bridge) nativeAllowed(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.native[name]
}

// confirmTools accepts an adapter's own session inventory as handshake evidence.
func (b *bridge) confirmTools(names []string, prefix string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listSeen = true
	b.listed = map[string]bool{}
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		tool := strings.TrimPrefix(name, prefix)
		if b.schemas[tool] == nil {
			return errors.New("native CLI exposed a tool outside this step's permissions")
		}
		b.listed[tool] = true
	}
	return nil
}

// requireTools proves the CLI received the exact host tool inventory.
func (b *bridge) requireTools() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.required) == 0 {
		return nil
	}
	if !b.listSeen {
		return errors.New("required_tools_unavailable: native CLI did not complete the MCP tool inventory handshake")
	}
	missing := []string{}
	for _, name := range b.required {
		if !b.listed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("required_tools_unavailable: native CLI did not expose %s", strings.Join(missing, ", "))
	}
	return nil
}

// notice records a non-critical protocol extension without trusting its payload.
func (b *bridge) notice(kind, detail string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.notices >= 50 {
		return nil
	}
	b.notices++
	return b.emitLocked("native_notice", map[string]any{"kind": kind, "detail": detail})
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
		b.err, b.observeFailed = err, true
		if b.stop != nil {
			b.stop()
		}
		return err
	}
	return nil
}

// activity acknowledges validated progress that has no visible payload. The
// window starts only after a successful write, and a failed observation stops
// the attempt instead of being swallowed.
func (b *bridge) activity(kind string) error {
	if kind != activityReasoning && kind != activityTextDelta {
		return errors.New("unsupported native activity kind")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	if !b.activityAt.IsZero() && now.Sub(b.activityAt) < activityInterval {
		return nil
	}
	if err := b.emitLocked("native_activity", map[string]any{"kind": kind}); err != nil {
		return err
	}
	b.activityAt = now
	return nil
}

func (b *bridge) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
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
	if usage.Input == nil && usage.Output == nil {
		return nil
	}
	if (usage.Input != nil && *usage.Input < 0) || (usage.Output != nil && *usage.Output < 0) {
		return newFailure(FailureProtocol, "native CLI returned invalid token usage")
	}
	// Usage is assigned, never summed: the same cumulative values can be
	// reported by more than one frame. A partial report must not overwrite a
	// complete known value on the failure path; partial known fields are kept
	// until the other side makes the pair complete.
	if usage.Input != nil && (!b.usageKnown || usage.Output != nil) {
		b.inputTokens, b.inputKnown = *usage.Input, true
	}
	if usage.Output != nil && (!b.usageKnown || usage.Input != nil) {
		b.outputTokens, b.outputKnown = *usage.Output, true
	}
	b.usageKnown = b.inputKnown && b.outputKnown
	return nil
}

// emitUsage records known or unknown usage on both success and failure paths.
// It still runs after this attempt's context was stopped (stall, protocol
// failure or cancellation) because usage belongs to the attempt; only a broken
// observer suppresses the write.
func (b *bridge) emitUsage() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.observeFailed {
		return b.err
	}
	if err := b.observe("usage", map[string]any{"known": b.usageKnown, "input_tokens": b.inputTokens, "output_tokens": b.outputTokens}); err != nil {
		// Keep an earlier protocol failure as the attempt's primary kind; the
		// observation error is still returned and joined by the caller.
		if b.err == nil {
			b.err = err
		}
		b.observeFailed = true
		if b.stop != nil {
			b.stop()
		}
		return err
	}
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
		if request.Method != "notifications/initialized" && request.Method != "notifications/cancelled" {
			_ = b.notice("mcp."+request.Method, "ignored optional MCP notification")
		}
		w.WriteHeader(http.StatusAccepted)
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
		b.mu.Lock()
		b.listSeen = true
		b.listed = map[string]bool{}
		list := make([]any, 0, len(b.tools))
		for _, tool := range b.tools {
			b.listed[tool.Name] = true
			list = append(list, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.Schema})
		}
		b.mu.Unlock()
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

// maxObservationBytes is the protocol boundary for one tool observation.
const maxObservationBytes = 65536

// controlTools carry receipts or task state that the model must receive
// intact. An oversized value is an implementation or protocol error, never a
// body that may be paged or truncated.
var controlTools = map[string]bool{"publish_artifact": true, "handoff_task": true, "read_skill": true, "get_task": true}

func (b *bridge) call(raw json.RawMessage) any {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.err != nil || b.ctx.Err() != nil {
		return toolResult("This step is stopped.", true)
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
	// The call number only gives each execution a stable identity; it is not
	// a task budget and never refuses or stops legitimate work.
	b.calls++
	id := fmt.Sprintf("native-%d", b.calls)
	if err := b.recordRepeatLocked(params.Name, params.Arguments); err != nil {
		return toolResult("Could not persist native notice.", true)
	}
	if err := b.emitLocked("tool_started", map[string]any{"tool_call_id": id, "name": params.Name, "input": params.Arguments}); err != nil {
		return toolResult("Could not persist tool start.", true)
	}
	started := time.Now()
	if b.live != nil {
		b.live.toolStarted()
	}
	callCtx, cancel := context.WithTimeout(b.ctx, hostToolDeadline)
	output, callErr := b.execute(callCtx, params.Name, params.Arguments)
	cancel()
	if b.live != nil {
		b.live.toolFinished()
	}
	status := "ok"
	if callErr != nil {
		status, output = "error", "Tool error: "+callErr.Error()
	}
	if len(output) > maxObservationBytes {
		if controlTools[params.Name] {
			// The receipt already exists externally; accepting a damaged or
			// paged copy would let the step look successful with incomplete
			// control data. Stop the attempt with a typed protocol failure.
			message := fmt.Sprintf("tool_receipt_too_large: tool %s returned %d bytes, exceeding the %d-byte host observation boundary; control receipts must be complete, so the step is stopped", params.Name, len(output), maxObservationBytes)
			if err := b.emitLocked("tool_finished", map[string]any{"tool_call_id": id, "name": params.Name, "status": "error", "output": message, "duration_ms": time.Since(started).Milliseconds()}); err != nil {
				return toolResult("Could not persist tool result.", true)
			}
			if b.err == nil {
				b.err = newFailure(FailureProtocol, "%s", message)
				if b.stop != nil {
					b.stop()
				}
			}
			return toolResult(message, true)
		}
		// Oversized business output becomes an explicit tool error so the model
		// can page it through a bounded read; it is never truncated or mistaken
		// for a protocol failure.
		status = "error"
		output = fmt.Sprintf("tool_observation_too_large: tool %s returned more than the %d-byte host observation limit; retrieve the captured content in bounded pages with read_source or read_context", params.Name, maxObservationBytes)
		if err := b.emitLocked("tool_finished", map[string]any{"tool_call_id": id, "name": params.Name, "status": status, "output": output, "duration_ms": time.Since(started).Milliseconds()}); err != nil {
			return toolResult("Could not persist tool result.", true)
		}
		return toolResult(output, true)
	}
	if err := b.emitLocked("tool_finished", map[string]any{"tool_call_id": id, "name": params.Name, "status": status, "output": output, "duration_ms": time.Since(started).Milliseconds()}); err != nil {
		return toolResult("Could not persist tool result.", true)
	}
	return toolResult(output, callErr != nil)
}

// recordRepeatLocked emits one bounded diagnostic when the same accepted tool
// input repeats consecutively. It never blocks the call.
func (b *bridge) recordRepeatLocked(name string, arguments json.RawMessage) error {
	key := name + "\x00" + canonicalToolInput(arguments)
	if key == b.lastInputKey {
		b.inputRepeats++
	} else {
		b.lastInputKey, b.inputRepeats, b.repeatNotice = key, 1, false
	}
	if b.inputRepeats != 3 || b.repeatNotice {
		return nil
	}
	b.repeatNotice = true
	if b.notices >= 50 {
		return nil
	}
	b.notices++
	return b.emitLocked("native_notice", map[string]any{"kind": "repeated_tool_input", "detail": fmt.Sprintf("%s was called 3 times consecutively with identical arguments; diagnostic only, the call was not blocked", name)})
}

// canonicalToolInput normalizes object keys and whitespace for comparison.
func canonicalToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}
