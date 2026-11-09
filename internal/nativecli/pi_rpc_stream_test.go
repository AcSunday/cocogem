package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// blockingReader delivers the given lines and then blocks forever, like Pi's
// long-lived RPC stdout when stdin stays open. A terminal frame must end the
// read without waiting for EOF.
type blockingReader struct {
	lines []string
	index int
	done  chan struct{}
}

func newBlockingReader(lines ...string) *blockingReader {
	return &blockingReader{lines: lines, done: make(chan struct{})}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.index < len(r.lines) {
		line := r.lines[r.index]
		r.index++
		if len(line) > len(p) {
			// One frame per Read is enough for bufio.Scanner.
			copy(p, line[:len(p)])
			r.lines[r.index-1] = line[len(p):]
			r.index--
			return len(p), nil
		}
		copy(p, line)
		return len(line), nil
	}
	<-r.done
	return 0, io.EOF
}

// piRecord encodes one RPC line.
func piRecord(t *testing.T, value map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}

// settledLines is a complete, successful Pi run: session state, prompt
// acceptance, one assistant message and agent_settled.
func settledLines(t *testing.T) []string {
	return []string{
		piRecord(t, map[string]any{"id": "cocogem-state", "type": "response", "command": "get_state", "success": true,
			"data": map[string]any{"sessionId": piSession}}),
		piRecord(t, map[string]any{"id": "cocogem-prompt", "type": "response", "command": "prompt", "success": true,
			"data": map[string]any{"disposition": "started"}}),
		piRecord(t, map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}}),
		piRecord(t, map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "text_delta", "contentIndex": 0, "delta": "done"}}),
		piRecord(t, map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "text_end", "contentIndex": 0, "content": "done"}}),
		piRecord(t, map[string]any{"type": "message_end", "message": map[string]any{
			"role": "assistant", "stopReason": "stop"}}),
		piRecord(t, map[string]any{"type": "agent_settled", "aborted": false}),
	}
}

// A confirmed terminal must end the read immediately, before stdin closes.
func TestParsePiRPCTerminalEndsReadWithoutEOF(t *testing.T) {
	gate := newTestGate(t, nil, discardObserve)
	reader := newBlockingReader(settledLines(t)...)
	defer close(reader.done)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- parsePiRPC(reader, gate, nil, newTestPiStdin()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a settled run was reported as a failure: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("parse kept reading after agent_settled; the RPC stdin never closed")
	}
}

// A failure before any terminal must also return instead of blocking on EOF.
func TestParsePiRPCFailureEndsReadWithoutEOF(t *testing.T) {
	gate := newTestGate(t, nil, discardObserve)
	reader := newBlockingReader(
		piRecord(t, map[string]any{"id": "cocogem-state", "type": "response", "command": "get_state", "success": false, "error": "no model"}),
	)
	defer close(reader.done)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- parsePiRPC(reader, gate, nil, newTestPiStdin()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a rejected state query was treated as success")
		}
	case <-ctx.Done():
		t.Fatal("parse blocked after a terminal failure")
	}
}

// A rejected prompt is terminal too: Pi never starts a run for it.
func TestParsePiRPCRejectedPromptIsTerminal(t *testing.T) {
	gate := newTestGate(t, nil, discardObserve)
	reader := newBlockingReader(
		piRecord(t, map[string]any{"id": "cocogem-state", "type": "response", "command": "get_state", "success": true,
			"data": map[string]any{"sessionId": piSession}}),
		piRecord(t, map[string]any{"id": "cocogem-prompt", "type": "response", "command": "prompt", "success": false,
			"error": "invalid api key"}),
	)
	defer close(reader.done)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- parsePiRPC(reader, gate, nil, newTestPiStdin()) }()
	select {
	case err := <-done:
		if FailureKindOf(err) != FailureAuthentication {
			t.Fatalf("rejected prompt error=%v, want authentication", err)
		}
	case <-ctx.Done():
		t.Fatal("a rejected prompt blocked the parser")
	}
}

// Text alone is not a terminal: a stream that ends without agent_settled is a
// protocol failure, never a success.
func TestParsePiRPCTextWithoutSettleIsNotSuccess(t *testing.T) {
	gate := newTestGate(t, nil, discardObserve)
	lines := settledLines(t)
	// A non-blocking reader that ends right after the text frames: EOF without
	// agent_settled must never become a successful run.
	stream := strings.NewReader(strings.Join(lines[:len(lines)-1], ""))
	err := parsePiRPC(stream, gate, nil, newTestPiStdin())
	if FailureKindOf(err) != FailureProtocol {
		t.Fatalf("error=%v, want a protocol failure", err)
	}
}

// The whole tool lifecycle carries one identity, and the tool output is the
// CLI's own bounded result.
func TestParsePiRPCToolLifecycleUsesOneIdentityAndRealOutput(t *testing.T) {
	var events []capturedEvent
	gate := newTestGate(t, nil, func(kind string, data map[string]any) error {
		events = append(events, capturedEvent{kind: kind, data: data})
		return nil
	})
	gate.setNative([]string{"bash"})
	lines := []string{
		piRecord(t, map[string]any{"id": "cocogem-state", "type": "response", "command": "get_state", "success": true,
			"data": map[string]any{"sessionId": piSession}}),
		piRecord(t, map[string]any{"id": "cocogem-prompt", "type": "response", "command": "prompt", "success": true,
			"data": map[string]any{"disposition": "started"}}),
		piRecord(t, map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}}),
		piRecord(t, map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "toolcall_end", "contentIndex": 0,
			"toolCall": map[string]any{"id": "call_x", "name": "bash", "arguments": map[string]any{}}}}),
		piRecord(t, map[string]any{"type": "tool_execution_start", "toolCallId": "call_x", "toolName": "bash",
			"args": map[string]any{"command": "printf hi"}}),
		piRecord(t, map[string]any{"type": "tool_execution_update", "toolCallId": "call_x", "toolName": "bash"}),
		piRecord(t, map[string]any{"type": "tool_execution_end", "toolCallId": "call_x", "toolName": "bash",
			"isError": false, "result": map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": "hi from tool"}},
				"structuredContent": map[string]any{"output": "hi from tool", "exit_code": 3}}}),
		piRecord(t, map[string]any{"type": "message_end", "message": map[string]any{
			"role": "assistant", "stopReason": "stop"}}),
		piRecord(t, map[string]any{"type": "agent_settled", "aborted": false}),
	}
	reader := newBlockingReader(lines...)
	defer close(reader.done)
	if err := parsePiRPC(reader, gate, nil, newTestPiStdin()); err != nil {
		t.Fatalf("the tool run failed: %v", err)
	}
	var started, finished map[string]any
	for _, event := range events {
		switch event.kind {
		case "tool_started":
			started = event.data
		case "tool_finished":
			finished = event.data
		}
	}
	if started == nil || finished == nil {
		t.Fatalf("tool lifecycle incomplete: %+v", events)
	}
	if started["tool_call_id"] != "pi:call_x" || finished["tool_call_id"] != "pi:call_x" {
		t.Fatalf("lifecycle ids started=%v finished=%v, want one pi:call_x",
			started["tool_call_id"], finished["tool_call_id"])
	}
	output, _ := finished["output"].(string)
	if !strings.Contains(output, "hi from tool") || !strings.Contains(output, "exit_code=3") {
		t.Fatalf("tool output=%q, want the CLI's own text and exit code", output)
	}
	if finished["status"] != "succeeded" {
		t.Fatalf("status=%v, want succeeded", finished["status"])
	}
}

// The identity helpers must agree for the exact forms both adapters produce.
func TestNativeToolIDIsCanonical(t *testing.T) {
	if got := nativeToolID("pi", "call_1"); got != "pi:call_1" {
		t.Fatalf("pi id=%q", got)
	}
	if got := nativeToolID("pi", "pi:call_1"); got != "pi:call_1" {
		t.Fatalf("already-prefixed pi id=%q", got)
	}
	if got := nativeToolID("gemini", "abc"); got != "gemini:abc" {
		t.Fatalf("gemini id=%q", got)
	}
	if got := nativeToolID("gemini", ""); got != "" {
		t.Fatalf("empty id=%q, want empty", got)
	}
	if !nativeToolIDMatches("pi:call_1", "call_1") || !nativeToolIDMatches("call_1", "pi:call_1") {
		t.Fatal("equivalent ids were not matched")
	}
	if nativeToolIDMatches("pi:call_1", "pi:call_2") || nativeToolIDMatches("", "pi:call_1") {
		t.Fatal("distinct ids were matched")
	}
}

// piToolResultOutput never invents output and always bounds it.
func TestPiToolResultOutputIsBoundedAndHonest(t *testing.T) {
	if got := piToolResultOutput(nil); got != "" {
		t.Fatalf("empty result rendered as %q", got)
	}
	exit, text := 7, "out"
	raw, _ := json.Marshal(map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": text}},
		"structuredContent": map[string]any{"exit_code": exit},
	})
	got := piToolResultOutput(raw)
	if !strings.Contains(got, text) || !strings.Contains(got, "exit_code=7") {
		t.Fatalf("rendered=%q", got)
	}
	long := strings.Repeat("a", 10000)
	bounded, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": long}}})
	if out := piToolResultOutput(bounded); len(out) > 4096 {
		t.Fatalf("tool output not bounded: %d bytes", len(out))
	}
	if got := piToolResultOutput(json.RawMessage(`"plain"`)); got != `"plain"` {
		t.Fatalf("unexpected fallback=%q", got)
	}
}

var _ = errors.New

// newTestPiStdin builds an input sink that records nothing; the parser only
// needs somewhere to write the state query and prompt.
func newTestPiStdin() *piRPCStdin {
	return &piRPCStdin{stdin: writeCloser{discardWriter{}}}
}

// discardWriter accepts and drops RPC writes.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// writeCloser adapts a writer to the stdin interface.
type writeCloser struct{ discardWriter }

func (writeCloser) Close() error { return nil }
