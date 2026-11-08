package nativecli

import (
	"encoding/json"
	"net/http"
)

// piToolCallPayload is the private Pi extension's gate call. Pi supplies a stable
// toolCallId; extra fields are ignored.
type piToolCallPayload struct {
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	Reason string          `json:"reason"`
}

func (b *gateBroker) servePiToolCall(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var payload piToolCallPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil ||
		payload.CallID == "" || payload.Name == "" {
		http.Error(w, "invalid Pi gate payload", http.StatusBadRequest)
		return
	}
	decision := b.authorize(r.Context(), gateRequest{
		CallID: nativeToolID("pi", payload.CallID), Name: payload.Name, Input: payload.Input, Reason: payload.Reason,
	})
	writeGateJSON(w, decision)
}
