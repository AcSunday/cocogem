package nativecli

import (
	"encoding/json"
	"net/http"
)

// Gemini hook payloads. Only the fields the gate needs are decoded; the rest of
// the CLI's payload is passed through unchanged.
type geminiHookPayload struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	SessionID     string          `json:"session_id"`
	Cwd           string          `json:"cwd"`
	ToolResponse  json.RawMessage `json:"tool_response"`
}

func (b *gateBroker) serveGeminiAuthorize(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var payload geminiHookPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil ||
		payload.HookEventName != "BeforeTool" || payload.ToolName == "" {
		http.Error(w, "invalid Gemini hook payload", http.StatusBadRequest)
		return
	}
	// The hook payload has no tool-call id, so the identity is derived from the
	// exact (session, tool, input) the CLI also exposes in its stream. The
	// stream's own tool_id is registered as an equivalent alias once observed.
	callID := geminiHookCallID(payload)
	decision := b.authorize(r.Context(), gateRequest{
		CallID: callID, Name: payload.ToolName, Input: payload.ToolInput,
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(geminiGateReply(payload.HookEventName, decision)))
}

func (b *gateBroker) serveGeminiFinalize(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var payload geminiHookPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil ||
		payload.HookEventName != "AfterTool" {
		http.Error(w, "invalid Gemini hook payload", http.StatusBadRequest)
		return
	}
	// The CLI reports the pre-rewrite input (verified with the installed
	// binary), so a staged write is resolved by its original path.
	call, classifyErr := classifyNativeCallForGate(b.adapter, payload.ToolName, payload.ToolInput, b.cwd)
	target := ""
	if classifyErr == nil {
		target = gateScopeTarget(call)
	}
	if err := b.finalizeByPath(target, geminiHookFailed(payload.ToolResponse)); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(geminiGateReply("AfterTool", gateDecision{})))
}

// geminiHookCallID keeps one stable identity per CLI tool call. The hook payload
// carries no tool-call id (verified with the installed binary: it exposes only
// session_id, cwd, transcript_path, tool_name and tool_input), so the identity
// must be derived from exactly what both sides see. The stream event's own
// `tool_id` is the primary key; this derived key is the documented alias used
// only when the stream frame has not been read yet.
func geminiHookCallID(payload geminiHookPayload) string {
	return geminiDerivedCallID(payload.ToolName, payload.ToolInput, payload.SessionID)
}

// geminiDerivedCallID is shared by the hook broker and the stream parser so a
// pending approval and its terminal event always resolve to one identity.
func geminiDerivedCallID(name string, input json.RawMessage, sessionID string) string {
	return nativeToolID("gemini", connectionDigest(map[string]any{
		"tool": name, "input": json.RawMessage(input), "session": sessionID,
	}))
}

// geminiHookFailed reads the CLI's tool_response error object. An absent error
// is the CLI's own success statement; a missing response is unproven.
func geminiHookFailed(raw json.RawMessage) bool {
	if len(raw) == 0 {
		// No response means the effect is unproven; a staged outside write is
		// never committed from unproven evidence.
		return true
	}
	var response struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return true
	}
	return len(response.Error) != 0 && string(response.Error) != "null"
}
