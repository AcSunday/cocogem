package nativecli

import (
	"strings"
	"testing"
)

func TestGeminiResumeRequiresExactSessionAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		name, observed string
		wantFailure    bool
	}{
		{"same session", fakeSession, false},
		{"different session", "99999999-9999-4999-8999-999999999999", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ack map[string]any
			gate := newTestGate(t, nil, func(kind string, data map[string]any) error {
				if kind == "native_session" {
					ack = data
				}
				return nil
			})
			gate.sessionMode, gate.sessionID = "resume", fakeSession
			frames := `{"type":"init","session_id":"` + tc.observed + "\"}\n" + `{"type":"result","status":"success"}`
			err := parseGemini(strings.NewReader(frames), gate, nil)
			if tc.wantFailure {
				if FailureKindOf(err) != FailureProtocol || ack != nil {
					t.Fatalf("different session: ack=%v err=%v", ack, err)
				}
			} else if err != nil || ack["mode"] != "resume" || ack["session_id"] != fakeSession {
				t.Fatalf("resumed session: ack=%v err=%v", ack, err)
			}
		})
	}
}

func TestGeminiTopLevelDiagnosticsRespectSeverityAndPreserveUsage(t *testing.T) {
	for _, tc := range []struct {
		name, frames string
		want         FailureKind
	}{
		{"warning followed by success", `{"type":"error","severity":"warning","message":"Temporary tool warning"}` + "\n" + `{"type":"result","status":"success","stats":{"input_tokens":12,"output_tokens":4}}`, ""},
		{"error result without diagnostics", `{"type":"result","status":"error","stats":{"input_tokens":12,"output_tokens":4}}`, FailureProtocol},
		{"empty provider response", `{"type":"error","severity":"error","message":"The model returned an empty response with no text or thoughts. This may be a transient API issue; please try again."}` + "\n" + `{"type":"result","status":"error","stats":{"input_tokens":12,"output_tokens":4}}`, FailureProtocol},
		{"top-level network error", `{"type":"error","severity":"error","message":"connection reset https://private.invalid?token=secret"}` + "\n" + `{"type":"result","status":"error","stats":{"input_tokens":12,"output_tokens":4}}`, FailureNetwork},
		{"error cannot be turned into success", `{"type":"error","severity":"error","message":"The model returned an empty response with no text or thoughts."}` + "\n" + `{"type":"result","status":"success","stats":{"input_tokens":12,"output_tokens":4}}`, FailureProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := false
			gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
				if kind == "usage" && data["known"] == true && data["input_tokens"] == 12 && data["output_tokens"] == 4 {
					usage = true
				}
				return nil
			})
			head := `{"type":"init","session_id":"` + fakeSession + `"}` + "\n"
			err := parseGemini(strings.NewReader(head+tc.frames+"\n"), gate, nil)
			if usageErr := gate.emitUsage(); usageErr != nil {
				t.Fatal(usageErr)
			}
			if FailureKindOf(err) != tc.want || tc.want != "" && err == nil || !usage {
				t.Fatalf("error=%v usage=%v want=%s", err, usage, tc.want)
			}
			if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private.invalid")) {
				t.Fatal("CLI diagnostic leaked provider details")
			}
		})
	}
}

func TestParseGeminiEmitsThinkingFromThoughtFramesAndFlag(t *testing.T) {
	var thinking, texts strings.Builder
	gate := newTestGate(t, nil, func(kind string, data map[string]any) error {
		switch kind {
		case "thinking":
			thinking.WriteString(data["text"].(string))
		case "text":
			texts.WriteString(data["text"].(string))
		}
		return nil
	})
	live := newLiveness(t.Context(), func() {}, discardObserve, 0, 0, "warn")
	stream := strings.Join([]string{
		`{"type":"init","session_id":"` + fakeSession + `"}`,
		`{"type":"thought","id":"t1","content":"check repo first; "}`,
		`{"type":"message","id":"t2","role":"assistant","thought":true,"content":"verify fix"}`,
		`{"type":"message","id":"m1","role":"assistant","content":"Done."}`,
		`{"type":"result","status":"success"}`,
	}, "\n") + "\n"
	if err := parseGemini(strings.NewReader(stream), gate, live); err != nil {
		t.Fatalf("parseGemini: %v", err)
	}
	if thinking.String() != "check repo first; verify fix" {
		t.Fatalf("thinking = %q, want %q", thinking.String(), "check repo first; verify fix")
	}
	if texts.String() != "Done." {
		t.Fatalf("visible text = %q, want %q", texts.String(), "Done.")
	}
}
