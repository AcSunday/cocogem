package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

const openCodeSession = "ses_abcdefghijklmnopqrstuvwxyz"

func openCodeSSE(kind string, fields map[string]any) string {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["sessionID"] = openCodeSession
	if _, ok := fields["assistantMessageID"]; !ok {
		fields["assistantMessageID"] = "msg"
	}
	raw, _ := json.Marshal(map[string]any{"id": kind + fmt.Sprint(fields), "type": kind, "data": fields})
	return "data: " + string(raw) + "\n\n"
}
func openCodeStart() string { return openCodeSSE("session.step.started", nil) }
func openCodeEnd(finish string) string {
	return openCodeSSE("session.step.ended", map[string]any{"finish": finish, "tokens": map[string]int{"input": 3, "output": 2}}) + openCodeSSE("session.execution.succeeded", nil)
}

func parseOpenCodeFixture(t *testing.T, stream string) ([]string, error) {
	t.Helper()
	var output []string
	gate, err := newBridge(t.Context(), nil, func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, func(kind string, data map[string]any) error {
		if kind == "text" {
			output = append(output, data["text"].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	live := newLiveness(t.Context(), func() {}, nil, 0, 0, "")
	err = parseOpenCodeEvents(newOpenCodeEvents(strings.NewReader(stream)), openCodeSession, gate, live)
	return output, err
}
func TestOpenCodeTextDeltasAndFinalSnapshot(t *testing.T) {
	stream := openCodeStart() + openCodeSSE("session.text.started", map[string]any{"ordinal": 0}) +
		openCodeSSE("session.text.delta", map[string]any{"ordinal": 0, "delta": "你"}) +
		openCodeSSE("session.text.delta", map[string]any{"ordinal": 0, "delta": "好"}) +
		openCodeSSE("session.text.ended", map[string]any{"ordinal": 0, "text": "你好！"}) + openCodeEnd("stop")
	output, err := parseOpenCodeFixture(t, stream)
	if err != nil || strings.Join(output, "") != "你好！" || len(output) != 3 {
		t.Fatalf("chunks=%q err=%v", output, err)
	}
}
func TestOpenCodeEmitsBeforeFinalTextOrEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	observed := make(chan string, 2)
	gate, err := newBridge(t.Context(), nil, func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, func(kind string, data map[string]any) error {
		if kind == "text" {
			observed <- data["text"].(string)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	live := newLiveness(t.Context(), func() {}, nil, 0, 0, "")
	done := make(chan error, 1)
	go func() { done <- parseOpenCodeEvents(newOpenCodeEvents(reader), openCodeSession, gate, live) }()
	t.Cleanup(func() { writer.Close(); <-done })
	fmt.Fprint(writer, openCodeStart()+openCodeSSE("session.text.started", map[string]any{"ordinal": 0})+openCodeSSE("session.text.delta", map[string]any{"ordinal": 0, "delta": "早"}))
	select {
	case text := <-observed:
		if text != "早" {
			t.Fatal(text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delta was buffered")
	}
	fmt.Fprint(writer, openCodeSSE("session.text.ended", map[string]any{"ordinal": 0, "text": "早"})+openCodeEnd("stop"))
}
func TestOpenCodeRejectsInvalidStreams(t *testing.T) {
	for _, test := range []struct {
		name, stream string
		kind         FailureKind
	}{
		{"missing execution completion", openCodeStart(), FailureProtocol},
		{"no step", openCodeSSE("session.text.delta", map[string]any{"ordinal": 0, "delta": "bad"}), FailureProtocol},
		{"changed final text", openCodeStart() + openCodeSSE("session.text.started", map[string]any{"ordinal": 0}) + openCodeSSE("session.text.delta", map[string]any{"ordinal": 0, "delta": "prefix"}) + openCodeSSE("session.text.ended", map[string]any{"ordinal": 0, "text": "changed"}), FailureProtocol},
		{"unfinished text", openCodeStart() + openCodeSSE("session.text.started", map[string]any{"ordinal": 0}) + openCodeEnd("stop"), FailureProtocol},
		{"truncation", openCodeStart() + openCodeEnd("length"), FailureProtocol},
		{"unfinished tool loop", openCodeStart() + openCodeEnd("tool-calls"), FailureProtocol},
		{"unapproved shell", openCodeStart() + openCodeSSE("session.tool.input.started", map[string]any{"id": "call", "name": "shell"}), FailureProtocol},
		{"unmatched result", openCodeStart() + openCodeSSE("session.tool.success", map[string]any{"id": "call"}), FailureProtocol},
		{"authentication", openCodeSSE("session.execution.failed", map[string]any{"error": map[string]string{"message": "synthetic invalid api key"}}), FailureAuthentication},
		{"negative usage", openCodeStart() + openCodeSSE("session.step.ended", map[string]any{"finish": "stop", "tokens": map[string]int{"input": -1, "output": 2}}), FailureProtocol},
		{"permission", openCodeSSE("permission.asked", nil), FailurePermission},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseOpenCodeFixture(t, test.stream)
			if FailureKindOf(err) != test.kind {
				t.Fatalf("err=%v want=%s", err, test.kind)
			}
		})
	}
}
func TestOpenCodeReasoningDoesNotEnterVisibleText(t *testing.T) {
	stream := openCodeStart() + openCodeSSE("session.reasoning.started", map[string]any{"ordinal": 0}) + openCodeSSE("session.reasoning.delta", map[string]any{"ordinal": 0, "delta": "private reasoning"}) + openCodeSSE("session.reasoning.ended", map[string]any{"ordinal": 0, "text": "private reasoning"}) + openCodeEnd("stop")
	output, err := parseOpenCodeFixture(t, stream)
	if err != nil || len(output) != 0 {
		t.Fatalf("reasoning leaked: %q %v", output, err)
	}
}

func TestOpenCodeScopesAndDeduplicatesEventIdentities(t *testing.T) {
	first := openCodeSSE("session.text.delta", map[string]any{"ordinal": 0, "delta": "哈"})
	second := strings.Replace(first, `"id":"session.text.delta`, `"id":"next-session.text.delta`, 1)
	foreign := strings.ReplaceAll(first, openCodeSession, "ses_zyxwvutsrqponmlkjihgfedcba")
	stream := openCodeStart() + openCodeSSE("session.text.started", map[string]any{"ordinal": 0}) + foreign + first + first + second + openCodeSSE("session.text.ended", map[string]any{"ordinal": 0, "text": "哈哈"}) + openCodeEnd("stop")
	output, err := parseOpenCodeFixture(t, stream)
	if err != nil || strings.Join(output, "") != "哈哈" || len(output) != 2 {
		t.Fatalf("identity replay corrupted output: %q %v", output, err)
	}
}
func TestOpenCodeObserverFailure(t *testing.T) {
	problem := errors.New("persist failed")
	gate, err := newBridge(t.Context(), []model.Tool{}, func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, func(kind string, _ map[string]any) error {
		if kind == "text" {
			return problem
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	live := newLiveness(t.Context(), func() {}, nil, 0, 0, "")
	err = parseOpenCodeEvents(newOpenCodeEvents(strings.NewReader(openCodeStart()+openCodeSSE("session.text.started", map[string]any{"ordinal": 0})+openCodeSSE("session.text.delta", map[string]any{"ordinal": 0, "delta": "hello"}))), openCodeSession, gate, live)
	if !errors.Is(err, problem) {
		t.Fatal(err)
	}
}
