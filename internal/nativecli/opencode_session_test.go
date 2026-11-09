package nativecli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenCodeManagedSessionAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name         string
		resume       bool
		responseID   string
		getStatus    int
		promptStatus int
		wantAck      bool
	}{
		{name: "fresh", responseID: openCodeSession, wantAck: true},
		{name: "exact resume", resume: true, responseID: openCodeSession, wantAck: true},
		{name: "changed resume identity", resume: true, responseID: "ses_zyxwvutsrqponmlkjihgfedcba"},
		{name: "lost resume", resume: true, responseID: openCodeSession, getStatus: http.StatusNotFound},
		{name: "prompt rejected", resume: true, responseID: openCodeSession, promptStatus: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			var promptCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				rw.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/event":
					rw.Header().Set("Content-Type", "text/event-stream")
					_, _ = rw.Write([]byte("data: {\"type\":\"server.connected\",\"data\":{}}\n\n" + openCodeStart() + openCodeEnd("stop")))
				case r.Method == http.MethodPost && r.URL.Path == "/api/session":
					_ = json.NewEncoder(rw).Encode(map[string]any{"data": map[string]any{"id": test.responseID}})
				case r.Method == http.MethodGet && r.URL.Path == "/api/session/"+openCodeSession:
					if test.getStatus != 0 {
						rw.WriteHeader(test.getStatus)
						_, _ = rw.Write([]byte(`{"error":"missing"}`))
						return
					}
					_ = json.NewEncoder(rw).Encode(map[string]any{"data": map[string]any{"id": test.responseID}})
				case r.Method == http.MethodPost && r.URL.Path == "/api/session/"+openCodeSession+"/prompt":
					promptCalls.Add(1)
					if test.promptStatus != 0 {
						rw.WriteHeader(test.promptStatus)
						_, _ = rw.Write([]byte(`{"error":"rejected"}`))
						return
					}
					_, _ = rw.Write([]byte(`{}`))
				default:
					rw.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			events := []capturedEvent{}
			observe := func(kind string, data map[string]any) error {
				events = append(events, capturedEvent{kind: kind, data: data})
				return nil
			}
			gate, err := newBridge(ctx, nil, func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, observe)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.close()
			if test.resume {
				gate.sessionMode, gate.sessionID = "resume", openCodeSession
			}
			live := newLiveness(ctx, cancel, observe, 0, 0, "")
			err = runOpenCodeTurn(ctx, server.Client(), server.URL, t.TempDir(), "fixture", "provider/model", "current delta", gate, live)
			if test.wantAck {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid OpenCode resume was accepted")
			}
			acknowledged := false
			for _, event := range events {
				if event.kind != "native_session" {
					continue
				}
				acknowledged = true
				wantMode := "fresh"
				if test.resume {
					wantMode = "resume"
				}
				if event.data["session_id"] != openCodeSession || event.data["mode"] != wantMode {
					t.Fatalf("wrong OpenCode acknowledgement: %+v", event.data)
				}
			}
			if acknowledged != test.wantAck {
				t.Fatalf("acknowledged=%v want %v; events=%+v", acknowledged, test.wantAck, events)
			}
			if (test.getStatus != 0 || test.responseID != openCodeSession) && promptCalls.Load() != 0 {
				t.Fatal("a prompt was submitted before the exact resume identity was verified")
			}
		})
	}
}

func TestManagedSessionRequiresSupportedExecutorAndIdentity(t *testing.T) {
	provider := Provider{command: fakeProvider("success").command}
	request := testRequest()
	request.Executor = "codex"
	request.SessionStorageRoot = t.TempDir()
	err := provider.run(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, func(string, map[string]any) error { return nil }, runOptions{skipCheck: true})
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported executor obtained managed session storage: %v", err)
	}
}
