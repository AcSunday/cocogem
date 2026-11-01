package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

const piSession = "11111111-1111-4111-8111-111111111111"

func piEvent(value map[string]any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestParsePiStreamsTextAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		piEvent(map[string]any{"type": "session", "id": piSession, "version": 3}),
		piEvent(map[string]any{"type": "message_start", "message": map[string]any{"role": "system", "content": ""}}),
		piEvent(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}}),
		piEvent(map[string]any{"type": "message_update", "usage": map[string]any{"input": 5, "output": 2}, "assistantMessageEvent": map[string]any{"type": "text_delta", "contentIndex": 0, "delta": "hello"}}),
		piEvent(map[string]any{"type": "message_update", "usage": map[string]any{"input": 5, "output": 2}, "assistantMessageEvent": map[string]any{"type": "text_end", "contentIndex": 0, "content": "hello"}}),
		piEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "stop", "usage": map[string]any{"input": 5, "output": 2}, "content": []any{map[string]any{"type": "text", "text": "hello"}}}}),
		piEvent(map[string]any{"type": "agent_settled", "aborted": false}),
	}, "\n") + "\n"
	events, err := parseWithEvents(t, nil, stream, parsePi)
	if err != nil {
		t.Fatal(err)
	}
	var text, session bool
	for _, event := range events {
		if event.kind == "text" && event.data["text"] == "hello" {
			text = true
		}
		if event.kind == "native_session" && event.data["executor"] == "pi" && event.data["session_id"] == piSession {
			session = true
		}
	}
	if !text || !session {
		t.Fatalf("text=%v session=%v events=%+v", text, session, events)
	}
}

func TestParsePiEmitsFullTextWhenNoDeltasArrive(t *testing.T) {
	stream := strings.Join([]string{
		piEvent(map[string]any{"type": "session", "id": piSession}),
		piEvent(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}}),
		piEvent(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_end", "contentIndex": 0, "content": "whole reply"}}),
		piEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "stop"}}),
		piEvent(map[string]any{"type": "agent_settled", "aborted": false}),
	}, "\n") + "\n"
	events, err := parseWithEvents(t, nil, stream, parsePi)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.kind == "text" && event.data["text"] == "whole reply" {
			return
		}
	}
	t.Fatalf("complete text was not emitted: %+v", events)
}

func TestParsePiAcceptsOnlyDeclaredToolCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observe := func(string, map[string]any) error { return nil }
	gate, err := newBridge(ctx, []model.Tool{{Name: "probe", Description: "probe", Schema: map[string]any{"type": "object"}}}, func(context.Context, string, json.RawMessage) (string, error) {
		return "{}", nil
	}, observe)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	if err := gate.confirmTools([]string{"mcp__cocogem__probe"}, "mcp__cocogem__"); err != nil {
		t.Fatal(err)
	}
	live := newLiveness(ctx, cancel, observe, time.Minute, time.Minute, "stop")
	live.dispatched()
	stream := strings.Join([]string{
		piEvent(map[string]any{"type": "session", "id": piSession}),
		piEvent(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}}),
		piEvent(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "toolcall_end", "contentIndex": 0, "toolCall": map[string]any{"type": "toolCall", "id": "call_1", "name": "mcp__cocogem__probe", "arguments": map[string]any{}}}}),
		piEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "toolUse"}}),
		piEvent(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}}),
		piEvent(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "contentIndex": 0, "delta": "done"}}),
		piEvent(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_end", "contentIndex": 0, "content": "done"}}),
		piEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "stop"}}),
		piEvent(map[string]any{"type": "agent_settled", "aborted": false}),
	}, "\n") + "\n"
	if err := parsePi(strings.NewReader(stream), gate, live); err != nil {
		t.Fatal(err)
	}
}

func TestParsePiTerminalFailures(t *testing.T) {
	tests := []struct {
		name string
		tail string
		kind FailureKind
	}{
		{
			name: "retry exhausted",
			tail: piEvent(map[string]any{"type": "auto_retry_end", "success": false, "finalError": "Connection error."}),
			kind: FailureNetwork,
		},
		{
			name: "assistant error without retry frame",
			tail: piEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "error", "errorMessage": "invalid api key"}}) + "\n" + piEvent(map[string]any{"type": "agent_settled", "aborted": false}),
			kind: FailureAuthentication,
		},
		{
			name: "truncated reply",
			tail: piEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "length"}}) + "\n" + piEvent(map[string]any{"type": "agent_settled", "aborted": false}),
			kind: FailureProtocol,
		},
		{
			name: "unfinished tool loop",
			tail: piEvent(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": "toolUse"}}) + "\n" + piEvent(map[string]any{"type": "agent_settled", "aborted": false}),
			kind: FailureProtocol,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := piEvent(map[string]any{"type": "session", "id": piSession}) + "\n" + test.tail + "\n"
			_, err := parseWithEvents(t, nil, stream, parsePi)
			if kind := FailureKindOf(err); kind != test.kind {
				t.Fatalf("kind=%q error=%v want=%q", kind, err, test.kind)
			}
		})
	}
}

func TestParsePiRequiresATerminalResult(t *testing.T) {
	stream := piEvent(map[string]any{"type": "session", "id": piSession}) + "\n" + piEvent(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}}) + "\n"
	_, err := parseWithEvents(t, nil, stream, parsePi)
	if kind := FailureKindOf(err); kind != FailureProtocol {
		t.Fatalf("kind=%q error=%v", kind, err)
	}
}

func TestParsePiRejectsInvalidSessionIdentity(t *testing.T) {
	stream := piEvent(map[string]any{"type": "session", "id": "not-a-uuid"}) + "\n"
	_, err := parseWithEvents(t, nil, stream, parsePi)
	if kind := FailureKindOf(err); kind != FailureProtocol {
		t.Fatalf("kind=%q error=%v", kind, err)
	}
}

func TestPiUsageTotalsAcrossResponsesAndFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		tail    string
		wantErr bool
	}{
		{name: "tool continuation", tail: `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","usage":{"input":30,"output":4}}}` + "\n" + `{"type":"agent_settled","aborted":false}`},
		{name: "failed continuation", tail: `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"invalid api key","usage":{"input":30,"output":4}}}` + "\n" + `{"type":"agent_settled","aborted":false}`, wantErr: true},
		{name: "interrupted stream", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reported map[string]any
			gate := newTestGate(t, nil, func(kind string, data map[string]any) error {
				if kind == "usage" {
					reported = data
				}
				return nil
			})
			live := newLiveness(t.Context(), func() {}, discardObserve, 0, 0, "warn")
			stream := strings.Join([]string{
				`{"type":"session","id":"` + piSession + `"}`,
				`{"type":"message_start","message":{"role":"assistant"}}`,
				`{"type":"message_update","usage":{"input":10,"output":2},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"first"}}`,
				`{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse","usage":{"input":10,"output":2}}}`,
				`{"type":"message_start","message":{"role":"toolResult"}}`,
				`{"type":"message_end","message":{"role":"toolResult"}}`,
				`{"type":"message_start","message":{"role":"assistant"}}`,
				`{"type":"message_update","usage":{"input":30,"output":4},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"second"}}`,
				`{"type":"message_update","usage":{"input":30,"output":4},"assistantMessageEvent":{"type":"text_end","contentIndex":0,"content":"second"}}`,
				test.tail,
			}, "\n") + "\n"
			err := parsePi(strings.NewReader(stream), gate, live)
			if (err != nil) != test.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, test.wantErr)
			}
			if err := gate.emitUsage(); err != nil {
				t.Fatal(err)
			}
			if reported["input_tokens"] != 40 || reported["output_tokens"] != 6 || reported["known"] != true {
				t.Fatalf("attempt usage=%v, want input 40 output 6 known", reported)
			}
		})
	}
}

func TestPiUsageDoesNotInventMissingResponseUsage(t *testing.T) {
	gate := newTestGate(t, nil, discardObserve)
	u := piUsageTotals{previousKnown: true}
	input, output := 5, 2
	if err := u.record(gate, piUsage{Input: &input, Output: &output}); err != nil {
		t.Fatal(err)
	}
	u.next()
	if err := u.record(gate, piUsage{}); err != nil {
		t.Fatal(err)
	}
	if gate.usageKnown || gate.inputTokens != 5 || gate.outputTokens != 2 {
		t.Fatalf("missing response usage should keep known partial totals: known=%v input=%d output=%d", gate.usageKnown, gate.inputTokens, gate.outputTokens)
	}
	negative := -1
	if err := u.record(gate, piUsage{Input: &negative}); FailureKindOf(err) != FailureProtocol {
		t.Fatalf("invalid usage accepted: %v", err)
	}
}

func TestPiThinkingUpdatesLivenessWithoutLeakingContent(t *testing.T) {
	for _, test := range []struct {
		name, delta string
		valid       bool
	}{
		{name: "thinking", delta: "synthetic private reasoning", valid: true},
		{name: "whitespace", delta: "   "},
	} {
		t.Run(test.name, func(t *testing.T) {
			var activities []map[string]any
			gate := newTestGate(t, nil, func(kind string, data map[string]any) error {
				if kind == "text" {
					t.Fatal("reasoning leaked into visible text")
				}
				if kind == "native_activity" {
					activities = append(activities, data)
				}
				return nil
			})
			live := newLiveness(t.Context(), func() {}, discardObserve, time.Second, time.Second, "stop")
			now := time.Now()
			live.now = func() time.Time { return now }
			live.dispatched()
			now = now.Add(900 * time.Millisecond)
			stream := `{"type":"session","id":"` + piSession + `"}` + "\n" + `{"type":"message_start","message":{"role":"assistant"}}` + "\n" +
				piEvent(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": test.delta}}) + "\n"
			_ = parsePi(strings.NewReader(stream), gate, live)
			now = now.Add(200 * time.Millisecond)
			alive := live.check()
			if test.valid {
				if !alive || live.failure() != nil || len(activities) != 1 || len(activities[0]) != 1 || activities[0]["kind"] != "reasoning" {
					t.Fatalf("valid reasoning not recorded as metadata: err=%v activity=%v", live.failure(), activities)
				}
			} else if alive || FailureKindOf(live.failure()) != FailureFirstResponseTimeout || len(activities) != 0 {
				t.Fatalf("whitespace counted as progress: err=%v activities=%v", live.failure(), activities)
			}
		})
	}
}

func TestPiThinkingObservationFailureStopsParsing(t *testing.T) {
	want := errors.New("activity write failed")
	gate := newTestGate(t, nil, func(kind string, _ map[string]any) error {
		if kind == "native_activity" {
			return want
		}
		return nil
	})
	live := newLiveness(t.Context(), func() {}, discardObserve, 0, 0, "warn")
	stream := `{"type":"session","id":"` + piSession + `"}` + "\n" + `{"type":"message_start","message":{"role":"assistant"}}` + "\n" + `{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"synthetic private reasoning"}}` + "\n"
	if err := parsePi(strings.NewReader(stream), gate, live); !errors.Is(err, want) {
		t.Fatalf("observation failure lost: %v", err)
	}
}
