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

func discardObserve(string, map[string]any) error { return nil }

// lineGenerator produces NDJSON lazily so large fixtures never sit in memory.
type lineGenerator struct {
	remaining int
	line      func(int) string
	index     int
	pending   []byte
}

func (g *lineGenerator) Read(p []byte) (int, error) {
	for len(g.pending) == 0 {
		if g.index >= g.remaining {
			return 0, io.EOF
		}
		g.pending = []byte(g.line(g.index))
		g.index++
	}
	n := copy(p, g.pending)
	g.pending = g.pending[n:]
	return n, nil
}

func newTestGate(t *testing.T, tools []model.Tool, observe func(string, map[string]any) error) *bridge {
	t.Helper()
	gate, err := newBridge(context.Background(), tools, nil, observe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	return gate
}

func TestParsersAcceptLargeCumulativeStreams(t *testing.T) {
	const noise = 70000
	const messages = 8200
	chunk := strings.Repeat("x", 4096)
	if len(chunk)*messages <= 32<<20 {
		t.Fatalf("fixture must exceed the old 32MiB cumulative text budget, got %d bytes", len(chunk)*messages)
	}
	textEvents := 0
	observe := func(kind string, _ map[string]any) error {
		if kind == "text" {
			textEvents++
		}
		return nil
	}
	tests := []struct {
		name   string
		parse  func(io.Reader, *bridge, *liveness) error
		prefix string
		line   func(int) string
		tail   string
	}{
		{
			name:   "claude",
			parse:  parseClaude,
			prefix: `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n",
			line: func(i int) string {
				if i < noise {
					return `{"type":"system","subtype":"status","status":"noise"}` + "\n"
				}
				return fmt.Sprintf(`{"type":"assistant","message":{"id":"msg-%d","content":[{"type":"text","text":%q}]}}`+"\n", i, chunk)
			},
			tail: `{"type":"result","subtype":"success","is_error":false,"usage":{"input_tokens":1,"output_tokens":1}}` + "\n",
		},
		{
			name:   "codex",
			parse:  parseCodex,
			prefix: `{"type":"thread.started","thread_id":"` + fakeSession + `"}` + "\n" + `{"type":"turn.started"}` + "\n",
			line: func(i int) string {
				if i < noise {
					return fmt.Sprintf(`{"type":"item.updated","item":{"id":"plan-%d","type":"plan"}}`+"\n", i)
				}
				return fmt.Sprintf(`{"type":"item.completed","item":{"id":"msg-%d","type":"agent_message","text":%q}}`+"\n", i, chunk)
			},
			tail: `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n",
		},
		{
			name:   "gemini",
			parse:  parseGemini,
			prefix: `{"type":"init","session_id":"` + fakeSession + `"}` + "\n",
			line: func(i int) string {
				if i < noise {
					return `{"type":"thought","content":"noise"}` + "\n"
				}
				return fmt.Sprintf(`{"type":"message","id":"msg-%d","role":"assistant","content":%q}`+"\n", i, chunk)
			},
			tail: `{"type":"result","status":"success","stats":{"input_tokens":1,"output_tokens":1}}` + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate := newTestGate(t, testRequest().Tools, observe)
			live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
			stream := io.MultiReader(strings.NewReader(test.prefix), &lineGenerator{remaining: noise + messages, line: test.line}, strings.NewReader(test.tail))
			if err := test.parse(stream, gate, live); err != nil {
				t.Fatalf("cumulative stream was rejected: %v", err)
			}
			if textEvents < messages {
				t.Fatalf("text events=%d, want at least %d", textEvents, messages)
			}
			textEvents = 0
		})
	}
}

func TestParsersRejectOversizedFrame(t *testing.T) {
	huge := strings.Repeat("x", 4*1024*1024+1024)
	tests := []struct {
		name  string
		parse func(io.Reader, *bridge, *liveness) error
		head  string
		line  string
	}{
		{
			name:  "claude",
			parse: parseClaude,
			head:  `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n",
			line:  `{"type":"system","subtype":"status","payload":"` + huge + `"}` + "\n",
		},
		{
			name:  "codex",
			parse: parseCodex,
			head:  `{"type":"thread.started","thread_id":"` + fakeSession + `"}` + "\n" + `{"type":"turn.started"}` + "\n",
			line:  `{"type":"item.updated","item":{"id":"p1","type":"plan","text":"` + huge + `"}}` + "\n",
		},
		{
			name:  "gemini",
			parse: parseGemini,
			head:  `{"type":"init","session_id":"` + fakeSession + `"}` + "\n",
			line:  `{"type":"thought","content":"` + huge + `"}` + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate := newTestGate(t, testRequest().Tools, discardObserve)
			live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
			err := test.parse(strings.NewReader(test.head+test.line), gate, live)
			if err == nil || FailureKindOf(err) != FailureProtocol || !strings.Contains(err.Error(), "4 MiB") {
				t.Fatalf("oversized frame error=%v", err)
			}
		})
	}
}

func toolResultText(result any) (string, bool) {
	value, _ := result.(map[string]any)
	failed, _ := value["isError"].(bool)
	content, _ := value["content"].([]any)
	if len(content) == 0 {
		return "", failed
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	return text, failed
}

func TestBridgeObservationBoundary(t *testing.T) {
	t.Parallel()
	publish := model.Tool{Name: "publish_artifact", Description: "Publish", Schema: map[string]any{"type": "object", "properties": map[string]any{"body": map[string]any{"type": "string"}}, "required": []string{"body"}, "additionalProperties": false}}
	handoff := model.Tool{Name: "handoff_task", Description: "Handoff", Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
	receipt := strings.Repeat("p", maxObservationBytes)
	var finished []map[string]any
	gate, err := newBridge(context.Background(), append(testRequest().Tools, publish, handoff), func(ctx context.Context, name string, _ json.RawMessage) (string, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > hostToolDeadline+time.Second {
			return "", errors.New("host tool deadline missing")
		}
		switch name {
		case "publish_artifact":
			return receipt, nil
		case "handoff_task":
			return strings.Repeat("h", maxObservationBytes+1), nil
		default:
			return strings.Repeat("x", maxObservationBytes+1), nil
		}
	}, func(kind string, data map[string]any) error {
		if kind == "tool_finished" {
			finished = append(finished, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	oversized, failed := toolResultText(gate.call(json.RawMessage(`{"name":"read_file","arguments":{"path":"README.md"}}`)))
	if !failed || !strings.Contains(oversized, "tool_observation_too_large") || len(oversized) > 512 {
		t.Fatalf("oversized observation was not an explicit bounded tool error: failed=%v text=%q", failed, oversized)
	}
	if err := gate.failure(); err != nil {
		t.Fatalf("business observation failure was treated as a protocol failure: %v", err)
	}
	if len(finished) != 1 || finished[0]["status"] != "error" || finished[0]["output"] != oversized {
		t.Fatalf("oversized observation was not recorded as a tool error: %v", finished)
	}
	text, failed := toolResultText(gate.call(json.RawMessage(`{"name":"publish_artifact","arguments":{"body":"public fixture"}}`)))
	if failed || text != receipt {
		t.Fatalf("boundary-sized control receipt was altered: failed=%v length=%d", failed, len(text))
	}
	if err := gate.failure(); err != nil {
		t.Fatalf("a complete boundary-sized receipt failed the step: %v", err)
	}
	oversized, failed = toolResultText(gate.call(json.RawMessage(`{"name":"handoff_task","arguments":{}}`)))
	if !failed || !strings.Contains(oversized, "tool_receipt_too_large") || len(oversized) > 512 {
		t.Fatalf("oversized control receipt was not a typed failure: failed=%v text=%q", failed, oversized)
	}
	if FailureKindOf(gate.failure()) != FailureProtocol {
		t.Fatalf("oversized control receipt kind=%q error=%v", FailureKindOf(gate.failure()), gate.failure())
	}
	if finished[len(finished)-1]["output"] != oversized {
		t.Fatalf("oversized control receipt body was persisted: %v", finished[len(finished)-1])
	}
	text, failed = toolResultText(gate.call(json.RawMessage(`{"name":"publish_artifact","arguments":{"body":"public fixture"}}`)))
	if !failed || text != "This step is stopped." {
		t.Fatalf("the step kept executing after an oversized control receipt: failed=%v text=%q", failed, text)
	}
}

func TestBridgeRepeatedToolInputIsDiagnosticOnly(t *testing.T) {
	t.Parallel()
	tools := []model.Tool{{Name: "probe", Description: "Probe", Schema: map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}}, "required": []string{"a", "b"}, "additionalProperties": false}}}
	executions := 0
	var notices []map[string]any
	gate, err := newBridge(context.Background(), tools, func(context.Context, string, json.RawMessage) (string, error) {
		executions++
		return "ok", nil
	}, func(kind string, data map[string]any) error {
		if kind == "native_notice" {
			notices = append(notices, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	call := func(input string) bool {
		_, failed := toolResultText(gate.call(json.RawMessage(`{"name":"probe","arguments":` + input + `}`)))
		return failed
	}
	for _, input := range []string{`{"a":"1","b":"2"}`, `{ "b" : "2", "a" : "1" }`, `{"a":"1","b":"2"}`} {
		if call(input) {
			t.Fatal("repeated identical input was blocked")
		}
	}
	if len(notices) != 1 || notices[0]["kind"] != "repeated_tool_input" || len(notices[0]["detail"].(string)) > 512 {
		t.Fatalf("repeated input notice=%v", notices)
	}
	call(`{"a":"1","b":"2"}`)
	if len(notices) != 1 {
		t.Fatal("the third repetition emitted more than one diagnostic")
	}
	call(`{"a":"1","b":"3"}`)
	for range 3 {
		call(`{"a":"1","b":"3"}`)
	}
	if len(notices) != 2 {
		t.Fatalf("a new consecutive repetition did not produce one notice: %v", notices)
	}
	if executions != 8 {
		t.Fatalf("executions=%d, repeated input must not change the call count", executions)
	}
	if err := gate.failure(); err != nil {
		t.Fatal(err)
	}
}

func TestActivityTrackerDistinguishesCallIdentities(t *testing.T) {
	tracker := newActivityTracker()
	if !tracker.newTool("msg", "call-1") || !tracker.newTool("msg", "call-2") {
		t.Fatal("two legitimate tool calls with equal input were treated as one")
	}
	if tracker.newTool("msg", "call-1") {
		t.Fatal("a replayed tool call extended activity")
	}
	if !tracker.newBlockText("msg", 0, "one") || tracker.newBlockText("msg", 0, "one") || !tracker.newBlockText("msg", 0, "one two") {
		t.Fatal("text dedupe did not follow the block index and content")
	}
	if !tracker.newBlockText("msg", 1, "one two") {
		t.Fatal("an identical text block at another index was swallowed")
	}
	frame := []byte(`{"content":"same"}`)
	if !tracker.newTool("frame", "call-3") || !tracker.newTool("frame", "call-4") {
		t.Fatal("distinct call identities collided")
	}
	if tracker.duplicate("item-1", frame) || !tracker.duplicate("item-1", frame) {
		t.Fatal("frame replay was not detected per item identity")
	}
	if tracker.duplicate("item-2", frame) {
		t.Fatal("an equal frame with another item identity was treated as a replay")
	}
}

func TestActivityTrackerKeepsActiveBlockState(t *testing.T) {
	tracker := newActivityTracker()
	if !tracker.newBlockText("active", 0, "kept") {
		t.Fatal("first block was not accepted")
	}
	for index := 1; index < 2*activityItems; index++ {
		tracker.newBlockText("active", index, "noise")
	}
	if tracker.newBlockText("active", 0, "kept") {
		t.Fatal("the active message's first block state was cleared")
	}
}

// TestActivityTrackerRecentCacheBoundary records the verifiable limit: an
// identity that fell out of the bounded recent cache is accepted as new rather
// than silently dropping legitimate text. A persistent identity index is
// required to close this boundary.
func TestActivityTrackerRecentCacheBoundary(t *testing.T) {
	tracker := newActivityTracker()
	tracker.newBlockText("old", 0, "legitimate")
	for index := 0; index < activityItems+8; index++ {
		tracker.newBlockText(fmt.Sprintf("message-%d", index), 0, "text")
	}
	if !tracker.newBlockText("old", 0, "legitimate") {
		t.Fatal("an identity beyond the recent cache must be accepted rather than dropped")
	}
	if tracker.newBlockText(fmt.Sprintf("message-%d", activityItems+6), 0, "text") {
		t.Fatal("a completed identity still inside the recent cache was not deduped")
	}
}

func TestLivenessFirstResponseStartsAtDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	live := newLiveness(ctx, cancel, discardObserve, time.Minute, 0, "warn")
	live.now = func() time.Time { return fake }
	live.started = fake.Add(-time.Hour)
	live.lastProgress = fake.Add(-time.Hour)
	live.dispatched()
	if !live.check() {
		t.Fatal("first-response deadline fired before dispatch")
	}
	fake = fake.Add(59 * time.Second)
	if !live.check() {
		t.Fatal("first-response deadline fired early")
	}
	fake = fake.Add(2 * time.Second)
	if live.check() {
		t.Fatal("first-response deadline did not fire")
	}
	if FailureKindOf(live.failure()) != FailureFirstResponseTimeout || ctx.Err() == nil {
		t.Fatalf("failure=%v canceled=%v", live.failure(), ctx.Err())
	}
}

func TestLivenessInflightToolLosesExemptionAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	live := newLiveness(ctx, cancel, discardObserve, 0, 21*time.Second, "stop")
	live.now = func() time.Time { return fake }
	live.dispatched()
	live.toolStarted()
	fake = fake.Add(10 * time.Second)
	if !live.check() {
		t.Fatal("an in-flight tool was stopped before its own deadline")
	}
	fake = fake.Add(15 * time.Second)
	if live.check() {
		t.Fatal("an in-flight tool bypassed the watchdog past the host tool deadline")
	}
	if FailureKindOf(live.failure()) != FailureStallTimeout {
		t.Fatalf("failure=%v", live.failure())
	}
}

func TestLivenessDisabledChecksDoNotFire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	live := newLiveness(ctx, cancel, discardObserve, 0, 0, "stop")
	live.now = func() time.Time { return fake }
	live.dispatched()
	live.started = fake.Add(-24 * time.Hour)
	live.lastProgress = fake.Add(-24 * time.Hour)
	if !live.check() || live.failure() != nil || ctx.Err() != nil {
		t.Fatalf("explicit zero limits fired: %v", live.failure())
	}
}

func TestClaudeNonProgressAndDuplicateEventsDoNotRefresh(t *testing.T) {
	gate := newTestGate(t, testRequest().Tools, discardObserve)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	live := newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
	live.now = func() time.Time { return fake }
	live.dispatched()
	old := fake.Add(-time.Minute)
	live.mu.Lock()
	live.lastProgress = old
	live.mu.Unlock()
	stream := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n" +
		`{"type":"assistant","message":{"id":"msg-empty","content":[]}}` + "\n" +
		`{"type":"system","subtype":"status","status":"idle"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false}` + "\n"
	if err := parseClaude(strings.NewReader(stream), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	stale := live.firstSeen || !live.lastProgress.Equal(old)
	live.mu.Unlock()
	if stale {
		t.Fatal("init, status or an empty assistant message refreshed liveness")
	}
	if live.check() {
		t.Fatal("stall detection was bypassed by non-progress events")
	}
}

func TestDuplicateTextDoesNotRefreshOrRepeat(t *testing.T) {
	fake := time.Now()
	old := fake.Add(-time.Minute)
	texts := 0
	var live *liveness
	gate, err := newBridge(context.Background(), testRequest().Tools, nil, func(kind string, _ map[string]any) error {
		if kind == "text" {
			texts++
			if live != nil {
				live.mu.Lock()
				live.lastProgress = old
				live.mu.Unlock()
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live = newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
	live.now = func() time.Time { return fake }
	live.dispatched()
	live.mu.Lock()
	live.lastProgress = old
	live.mu.Unlock()
	line := `{"type":"assistant","message":{"id":"msg-same","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}` + "\n"
	stream := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n" + line + line +
		`{"type":"result","subtype":"success","is_error":false}` + "\n"
	if err := parseClaude(strings.NewReader(stream), gate, live); err != nil {
		t.Fatal(err)
	}
	if texts != 2 {
		t.Fatalf("a duplicated multi-block item was emitted %d times, want 2 unique blocks", texts)
	}
	if live.check() {
		t.Fatal("a duplicate item frame extended the stall deadline")
	}
}

func TestClaudeStreamEventRequiresValidatedNestedDelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	gate := newTestGate(t, testRequest().Tools, discardObserve)
	reset := func() *liveness {
		live := newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
		live.now = func() time.Time { return fake }
		live.dispatched()
		live.mu.Lock()
		live.lastProgress = fake.Add(-time.Minute)
		live.mu.Unlock()
		return live
	}
	envelope := `{"type":"stream_event","uuid":"u1","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}` + "\n"
	live := reset()
	if err := parseClaude(strings.NewReader(`{"type":"system","subtype":"init","session_id":"`+fakeSession+`","tools":[]}`+"\n"+envelope+`{"type":"result","subtype":"success","is_error":false}`+"\n"), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	stale := live.firstSeen
	live.mu.Unlock()
	if stale {
		t.Fatal("a top-level stream_event envelope counted as a response")
	}
	delta := `{"type":"stream_event","uuid":"u2","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}` + "\n"
	live = reset()
	if err := parseClaude(strings.NewReader(`{"type":"system","subtype":"init","session_id":"`+fakeSession+`","tools":[]}`+"\n"+delta+`{"type":"result","subtype":"success","is_error":false}`+"\n"), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	seen := live.firstSeen
	live.mu.Unlock()
	if !seen {
		t.Fatal("a validated content delta did not count as a response")
	}
}

func TestClaudeAllowedToolStartConfirmsFirstResponse(t *testing.T) {
	gate := newTestGate(t, testRequest().Tools, discardObserve)
	live := newLiveness(context.Background(), func() {}, discardObserve, time.Minute, time.Minute, "stop")
	live.dispatched()
	stream := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":["mcp__cocogem__read_file"]}` + "\n" +
		`{"type":"assistant","message":{"id":"msg-tool","content":[{"type":"tool_use","id":"tool-1","name":"mcp__cocogem__read_file","input":{}}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false}` + "\n"
	if err := parseClaude(strings.NewReader(stream), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	seen := live.firstSeen
	live.mu.Unlock()
	if !seen {
		t.Fatal("an allowed native tool start did not establish first response")
	}
}

func TestCodexItemValidationAndProgress(t *testing.T) {
	texts := 0
	gate := newTestGate(t, testRequest().Tools, func(kind string, _ map[string]any) error {
		if kind == "text" {
			texts++
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	old := fake.Add(-time.Minute)
	reset := func() *liveness {
		live := newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
		live.now = func() time.Time { return fake }
		live.dispatched()
		live.mu.Lock()
		live.lastProgress = old
		live.mu.Unlock()
		return live
	}
	head := `{"type":"thread.started","thread_id":"` + fakeSession + `"}` + "\n" + `{"type":"turn.started"}` + "\n"
	nonProgress := head + `{"type":"item.started","item":{"id":"p","type":"plan"}}` + "\n" +
		`{"type":"item.completed","item":{"id":"m","type":"agent_message","text":""}}` + "\n" +
		`{"type":"item.updated","item":{"id":"r","type":"reasoning","text":""}}` + "\n" +
		`{"type":"turn.completed"}` + "\n"
	live := reset()
	if err := parseCodex(strings.NewReader(nonProgress), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	stale := live.firstSeen || !live.lastProgress.Equal(old)
	live.mu.Unlock()
	if stale {
		t.Fatal("empty, plan or init items refreshed liveness")
	}
	reasoning := head + `{"type":"item.updated","item":{"id":"r","type":"reasoning","text":"metadata"}}` + "\n" + `{"type":"turn.completed"}` + "\n"
	live = reset()
	if err := parseCodex(strings.NewReader(reasoning), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	reasoningSeen := live.firstSeen
	live.mu.Unlock()
	if !reasoningSeen {
		t.Fatal("reasoning metadata did not count as activity")
	}
	if texts != 0 {
		t.Fatal("reasoning content was emitted or recorded")
	}
	tool := head + `{"type":"item.started","item":{"id":"t","type":"mcp_tool_call","server":"cocogem","tool":"read_file"}}` + "\n" + `{"type":"turn.completed"}` + "\n"
	live = reset()
	if err := parseCodex(strings.NewReader(tool), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	toolSeen := live.firstSeen
	live.mu.Unlock()
	if !toolSeen {
		t.Fatal("an allowed MCP tool start did not establish first response")
	}
	malformed := head + `{"type":"item.updated","item":{"type":"plan"}}` + "\n" + `{"type":"turn.completed"}` + "\n"
	if err := parseCodex(strings.NewReader(malformed), gate, reset()); err == nil || FailureKindOf(err) != FailureProtocol {
		t.Fatalf("item without an identity was accepted: %v", err)
	}
}

func TestGeminiOnlyAssistantTextCountsAsProgress(t *testing.T) {
	texts := 0
	gate := newTestGate(t, testRequest().Tools, func(kind string, _ map[string]any) error {
		if kind == "text" {
			texts++
		}
		return nil
	})
	gate.nativePrefix = "mcp_cocogem_"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	head := `{"type":"init","session_id":"` + fakeSession + `"}` + "\n"
	fake := time.Now()
	old := fake.Add(-time.Minute)
	resetLive := func() *liveness {
		live := newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
		live.now = func() time.Time { return fake }
		live.dispatched()
		live.mu.Lock()
		live.lastProgress = old
		live.mu.Unlock()
		return live
	}
	userEcho := head + `{"type":"message","role":"user","content":"echo"}` + "\n" + `{"type":"message","role":"assistant","content":""}` + "\n" + `{"type":"result","status":"success"}` + "\n"
	live := resetLive()
	if err := parseGemini(strings.NewReader(userEcho), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	echoSeen := live.firstSeen || !live.lastProgress.Equal(old)
	live.mu.Unlock()
	if echoSeen || texts != 0 {
		t.Fatal("a user echo or empty assistant frame counted as progress")
	}
	answer := head + `{"type":"message","role":"assistant","content":"answer"}` + "\n" + `{"type":"result","status":"success"}` + "\n"
	live = resetLive()
	if err := parseGemini(strings.NewReader(answer), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	answerSeen := live.firstSeen
	live.mu.Unlock()
	if !answerSeen || texts != 1 {
		t.Fatal("assistant text did not count as progress")
	}
	tool := head + `{"type":"tool_use","tool_name":"mcp_cocogem_read_file"}` + "\n" + `{"type":"result","status":"success"}` + "\n"
	live = resetLive()
	if err := parseGemini(strings.NewReader(tool), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	toolSeen := live.firstSeen
	live.mu.Unlock()
	if !toolSeen {
		t.Fatal("an allowed Gemini tool start did not establish first response")
	}
}

func TestAppendFailureKeepsCancellationFirst(t *testing.T) {
	failures := appendFailure(nil, context.Canceled)
	failures = appendFailure(failures, newFailure(FailureStallTimeout, "stall"))
	failures = appendFailure(failures, context.Canceled)
	if len(failures) != 2 || !errors.Is(failures[0], context.Canceled) {
		t.Fatalf("cancellation precedence or dedupe was lost: %v", failures)
	}
	joined := errors.Join(failures...)
	if !errors.Is(joined, context.Canceled) || FailureKindOf(joined) != FailureStallTimeout {
		t.Fatalf("joined error lost provenance: %v", joined)
	}
}

func TestClaudeIdenticalTextBlocksSurviveAndUpdatesAppendOnly(t *testing.T) {
	var texts []string
	gate, err := newBridge(context.Background(), testRequest().Tools, nil, func(kind string, data map[string]any) error {
		if kind == "text" {
			texts = append(texts, data["text"].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	live.dispatched()
	frame := `{"type":"assistant","message":{"id":"msg-same","content":[{"type":"text","text":"same"},{"type":"text","text":"same"}]}}` + "\n"
	updated := `{"type":"assistant","message":{"id":"msg-same","content":[{"type":"text","text":"same"},{"type":"text","text":"same"},{"type":"text","text":"new"}]}}` + "\n"
	stream := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n" + frame + frame + updated +
		`{"type":"result","subtype":"success","is_error":false}` + "\n"
	if err := parseClaude(strings.NewReader(stream), gate, live); err != nil {
		t.Fatal(err)
	}
	want := []string{"same\n", "same\n", "new\n"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("emitted text=%q, want two identical blocks then only the appended block %q", texts, want)
	}
}

func TestClaudeAnonymousMessagesKeepEqualText(t *testing.T) {
	var texts []string
	gate, err := newBridge(context.Background(), testRequest().Tools, nil, func(kind string, data map[string]any) error {
		if kind == "text" {
			texts = append(texts, data["text"].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	live.dispatched()
	frame := `{"type":"assistant","message":{"content":[{"type":"text","text":"same"}]}}` + "\n"
	stream := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n" + frame + frame +
		`{"type":"result","subtype":"success","is_error":false}` + "\n"
	if err := parseClaude(strings.NewReader(stream), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 {
		t.Fatalf("messages without a protocol identity dropped equal legal text: %q", texts)
	}
}

func TestClaudeStreamEventBeforeInitIsRejected(t *testing.T) {
	gate := newTestGate(t, testRequest().Tools, discardObserve)
	live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	stream := `{"type":"stream_event","uuid":"u1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}` + "\n"
	err := parseClaude(strings.NewReader(stream), gate, live)
	if FailureKindOf(err) != FailureProtocol {
		t.Fatalf("a stream event before session initialization was accepted: %v", err)
	}
}

func TestGeminiWhitespaceAndDuplicateFrameSemantics(t *testing.T) {
	texts := []string{}
	activities := []bool{}
	gate, err := newBridge(context.Background(), testRequest().Tools, nil, func(kind string, data map[string]any) error {
		if kind == "text" {
			texts = append(texts, data["text"].(string))
			activities = append(activities, data["activity"].(bool))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	gate.nativePrefix = "mcp_cocogem_"
	head := `{"type":"init","session_id":"` + fakeSession + `"}` + "\n"
	tail := `{"type":"result","status":"success"}` + "\n"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	old := fake.Add(-time.Minute)
	resetLive := func() *liveness {
		live := newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
		live.now = func() time.Time { return fake }
		live.dispatched()
		live.mu.Lock()
		live.lastProgress = old
		live.mu.Unlock()
		return live
	}
	whitespace := head + `{"type":"message","role":"assistant","content":" \n\t "}` + "\n" + tail
	live := resetLive()
	if err := parseGemini(strings.NewReader(whitespace), gate, live); err != nil {
		t.Fatal(err)
	}
	live.mu.Lock()
	seen := live.firstSeen
	stale := live.lastProgress.Equal(old)
	live.mu.Unlock()
	if seen || !stale || len(texts) != 1 || texts[0] != " \n\t " || activities[0] {
		t.Fatalf("whitespace-only content must be kept verbatim without refreshing liveness: texts=%q activities=%v", texts, activities)
	}
	texts, activities = nil, nil
	identified := `{"type":"message","id":"m1","role":"assistant","content":"answer"}` + "\n"
	live = resetLive()
	if err := parseGemini(strings.NewReader(head+identified+identified+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 1 {
		t.Fatalf("an exact identified frame replay was emitted %d times", len(texts))
	}
	anonymous := `{"type":"message","role":"assistant","content":"answer"}` + "\n"
	texts, activities = nil, nil
	live = resetLive()
	if err := parseGemini(strings.NewReader(head+anonymous+anonymous+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 {
		t.Fatalf("frames without an identity must keep delta text, emitted %d", len(texts))
	}
	live.mu.Lock()
	seen = live.firstSeen
	live.mu.Unlock()
	if !seen {
		t.Fatal("legitimate non-empty assistant output did not count as activity")
	}
}

func TestUsagePartialDoesNotClearKnownValues(t *testing.T) {
	var observed []map[string]any
	gate, err := newBridge(context.Background(), nil, nil, func(kind string, data map[string]any) error {
		if kind == "usage" {
			observed = append(observed, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	input, output := 7, 11
	if err := gate.recordUsage(tokenUsage{Input: &input, Output: &output}); err != nil {
		t.Fatal(err)
	}
	partial := 3
	if err := gate.recordUsage(tokenUsage{Input: &partial}); err != nil {
		t.Fatal(err)
	}
	if err := gate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	got := observed[len(observed)-1]
	if got["known"] != true || got["input_tokens"] != 7 || got["output_tokens"] != 11 {
		t.Fatalf("a partial usage frame cleared known complete values: %v", got)
	}

	var partialObserved []map[string]any
	partialGate, err := newBridge(context.Background(), nil, nil, func(kind string, data map[string]any) error {
		if kind == "usage" {
			partialObserved = append(partialObserved, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(partialGate.close)
	three, four := 3, 4
	_ = partialGate.recordUsage(tokenUsage{Input: &three})
	_ = partialGate.recordUsage(tokenUsage{Output: &four})
	if err := partialGate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	got = partialObserved[len(partialObserved)-1]
	if got["known"] != true || got["input_tokens"] != 3 || got["output_tokens"] != 4 {
		t.Fatalf("complementary partial usage was not combined without summing: %v", got)
	}
}

func TestUsageEmittedAfterBridgeStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var usage map[string]any
	gate, err := newBridge(ctx, nil, nil, func(kind string, data map[string]any) error {
		if kind == "usage" {
			usage = data
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	gate.stop = cancel
	input, output := 5, 6
	if err := gate.recordUsage(tokenUsage{Input: &input, Output: &output}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := gate.emitUsage(); err != nil {
		t.Fatalf("usage was not saved after the attempt was stopped: %v", err)
	}
	if usage == nil || usage["known"] != true || usage["input_tokens"] != 5 || usage["output_tokens"] != 6 {
		t.Fatalf("stopped attempt lost known usage: %v", usage)
	}
}

func TestUsageEmitDoesNotRunWithBrokenObserver(t *testing.T) {
	want := errors.New("usage store unavailable")
	calls := 0
	gate, err := newBridge(context.Background(), nil, nil, func(kind string, _ map[string]any) error {
		calls++
		if kind == "native_notice" {
			return want
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	if err := gate.notice("status", "ignored"); !errors.Is(err, want) {
		t.Fatalf("observer failure was not retained: %v", err)
	}
	input, output := 5, 6
	if err := gate.recordUsage(tokenUsage{Input: &input, Output: &output}); err != nil {
		t.Fatal(err)
	}
	if err := gate.emitUsage(); !errors.Is(err, want) || calls != 1 {
		t.Fatalf("a broken observer was called again for usage: err=%v calls=%d", err, calls)
	}
}

func TestClaudeErrorEventPreservesUsageAndStructuredCode(t *testing.T) {
	var usage map[string]any
	gate, err := newBridge(context.Background(), testRequest().Tools, nil, func(kind string, data map[string]any) error {
		if kind == "usage" {
			usage = data
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	stream := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n" +
		`{"type":"error","error":{"type":"authentication_error","message":"invalid key"},"usage":{"input_tokens":5,"output_tokens":6}}` + "\n"
	parseErr := parseClaude(strings.NewReader(stream), gate, live)
	if FailureKindOf(parseErr) != FailureAuthentication {
		t.Fatalf("structured error code was not used: %v", parseErr)
	}
	if err := gate.emitUsage(); err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage["known"] != true || usage["input_tokens"] != 5 || usage["output_tokens"] != 6 {
		t.Fatalf("usage from a top-level error event was dropped: %v", usage)
	}
}

func TestClaudeTerminalBroadCodeUsesExplicitOverflowDetail(t *testing.T) {
	gate := newTestGate(t, testRequest().Tools, discardObserve)
	head := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n"
	live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	err := parseClaude(strings.NewReader(head+`{"type":"result","subtype":"success","is_error":false,"terminal_reason":"invalid_request","result":"This model's maximum context length is 200000 tokens"}`+"\n"), gate, live)
	if FailureKindOf(err) != FailureContextOverflow {
		t.Fatalf("a broad structured code with explicit overflow detail was not refined: %v", err)
	}
	authGate := newTestGate(t, testRequest().Tools, discardObserve)
	live = newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	err = parseClaude(strings.NewReader(head+`{"type":"result","subtype":"success","is_error":false,"terminal_reason":"authentication_error","result":"maximum context length is 200000 tokens"}`+"\n"), authGate, live)
	if FailureKindOf(err) != FailureAuthentication {
		t.Fatalf("structured authentication was overridden by an overflow phrase: %v", err)
	}
}

func TestFailureKindClassification(t *testing.T) {
	tests := []struct {
		detail string
		kind   FailureKind
	}{
		{"Failed to authenticate: OAuth session expired credential-secret", FailureAuthentication},
		{"Model quota exceeded account-secret", FailureQuota},
		{"This account uses an OpenAI-compatible upstream. Use /v1/chat/completions or /v1/responses.", FailureProtocol},
		{"network connection reset", FailureNetwork},
		{"Permission denied for path-private", FailurePermission},
		{"Unknown tool private-tool", FailureToolUnavailable},
		{"model not found", FailureModelUnavailable},
	}
	for _, test := range tests {
		err := nativeFailure("error_during_execution", test.detail)
		if FailureKindOf(err) != test.kind {
			t.Errorf("detail %q kind=%q, want %q", test.detail, FailureKindOf(err), test.kind)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
			t.Errorf("failure echoed provider detail: %v", err)
		}
	}
}

func TestFailureKindsPreferStructuredFields(t *testing.T) {
	err := classifiedNativeFailure("prompt_too_long", "success", "401 invalid api key")
	if FailureKindOf(err) != FailureContextOverflow {
		t.Fatalf("structured terminal reason was not authoritative: %v", err)
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != FailureContextOverflow {
		t.Fatalf("Failure is not errors.As-able: %v", err)
	}
	if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "api key") {
		t.Fatalf("failure echoed provider detail: %v", err)
	}
	err = nativeFailure("error_during_execution", "This model's maximum context length is 200000 tokens")
	if FailureKindOf(err) != FailureContextOverflow {
		t.Fatalf("context overflow was misclassified: %v", err)
	}
	err = classifiedNativeFailure("", "invalid_request", "maximum context length is 200000 tokens")
	if FailureKindOf(err) != FailureContextOverflow {
		t.Fatalf("a broad invalid_request subtype overrode context overflow: %v", err)
	}
	err = nativeFailure("turn_failed", "something unexpected")
	if FailureKindOf(err) != FailureUnknown {
		t.Fatalf("unknown failure kind=%q", FailureKindOf(err))
	}
}

func TestFailureKindExplicitOverflowCodesAndMessages(t *testing.T) {
	for _, test := range []struct{ terminal, subtype, detail string }{
		{"context_length_exceeded", "", ""},
		{"error.context_length_exceeded", "", ""},
		{"prompt_too_long: 210000 tokens > 200000 maximum", "", ""},
		{"", "invalid_request", "prompt is too long: 208432 tokens > 200000 maximum"},
		{"", "", "context_length_exceeded: reduce the input"},
		{"", "", "This model's maximum context length is 128000 tokens. However, your messages resulted in 200001 tokens."},
	} {
		err := classifiedNativeFailure(test.terminal, test.subtype, test.detail)
		if FailureKindOf(err) != FailureContextOverflow {
			t.Errorf("terminal=%q subtype=%q detail=%q kind=%q", test.terminal, test.subtype, test.detail, FailureKindOf(err))
		}
	}
}

func TestFailureKindDoesNotInferOverflowFromBroadText(t *testing.T) {
	for _, test := range []struct {
		terminal, subtype, detail string
		want                      FailureKind
	}{
		{"network_error", "turn_failed", "network timeout while checking context window", FailureNetwork},
		{"authentication_error", "reported_error", "maximum context length is 200000 tokens", FailureAuthentication},
		{"", "reported_error", "network timeout while checking context window", FailureNetwork},
		{"", "reported_error", "connection reset while checking context window", FailureNetwork},
		{"", "reported_error", "invalid max_tokens setting", FailureUnknown},
		{"", "reported_error", "unsupported input length parameter", FailureUnknown},
		{"", "reported_error", "too many tokens", FailureUnknown},
		{"", "reported_error", "context window exceeded while probing", FailureUnknown},
	} {
		err := classifiedNativeFailure(test.terminal, test.subtype, test.detail)
		if FailureKindOf(err) != test.want {
			t.Errorf("terminal=%q subtype=%q detail=%q kind=%q, want %q", test.terminal, test.subtype, test.detail, FailureKindOf(err), test.want)
		}
	}
}

func TestCodexReasoningEmitsActivityMetadataOnly(t *testing.T) {
	var activities, texts []map[string]any
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		switch kind {
		case "native_activity":
			activities = append(activities, data)
		case "text":
			texts = append(texts, data)
		}
		return nil
	})
	fake := time.Now()
	gate.now = func() time.Time { return fake }
	live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	head := `{"type":"thread.started","thread_id":"` + fakeSession + `"}` + "\n" + `{"type":"turn.started"}` + "\n"
	tail := `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}` + "\n"
	stream := head +
		`{"type":"item.updated","item":{"id":"r1","type":"reasoning","text":"private reasoning"}}` + "\n" +
		`{"type":"item.updated","item":{"id":"r1","type":"reasoning","text":"private reasoning"}}` + "\n" +
		`{"type":"item.updated","item":{"id":"r2","type":"reasoning","text":"more private reasoning"}}` + "\n" +
		`{"type":"item.started","item":{"id":"p1","type":"plan"}}` + "\n" +
		`{"type":"status","status":"noise"}` + "\n" +
		`{"type":"item.completed","item":{"id":"a1","type":"agent_message","text":"visible answer"}}` + "\n" + tail
	if err := parseCodex(strings.NewReader(stream), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 || len(activities[0]) != 1 || activities[0]["kind"] != "reasoning" {
		t.Fatalf("reasoning activity=%v, want one metadata-only kind", activities)
	}
	if len(texts) != 1 || texts[0]["text"] != "visible answer\n" || texts[0]["activity"] != true {
		t.Fatalf("text observations=%v, want only the completed assistant message with activity=true", texts)
	}
	live.mu.Lock()
	seen := live.firstSeen
	live.mu.Unlock()
	if !seen {
		t.Fatal("validated reasoning metadata did not update liveness")
	}
	fake = fake.Add(activityInterval)
	second := head + `{"type":"item.updated","item":{"id":"r3","type":"reasoning","text":"another item"}}` + "\n" + tail
	if err := parseCodex(strings.NewReader(second), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(activities) != 2 || activities[1]["kind"] != "reasoning" {
		t.Fatalf("activity was not acknowledged after the interval: %v", activities)
	}
}

func TestCodexAgentMessageEmitsBoundedTextDeltaActivity(t *testing.T) {
	var activities, texts []map[string]any
	fake := time.Now()
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		switch kind {
		case "native_activity":
			activities = append(activities, data)
			// Advance only after a successful observation so a replayed frame
			// cannot borrow the next window.
			fake = fake.Add(activityInterval)
		case "text":
			texts = append(texts, data)
		}
		return nil
	})
	gate.now = func() time.Time { return fake }
	live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	head := `{"type":"thread.started","thread_id":"` + fakeSession + `"}` + "\n" + `{"type":"turn.started"}` + "\n"
	update := func(text string) string {
		return `{"type":"item.updated","item":{"id":"a1","type":"agent_message","text":"` + text + `"}}` + "\n"
	}
	stream := head +
		update("visible answer") +
		update("visible answer") +
		update("visible answer continues") +
		`{"type":"item.completed","item":{"id":"a1","type":"agent_message","text":"visible answer continues"}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}` + "\n"
	if err := parseCodex(strings.NewReader(stream), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(activities) != 2 {
		t.Fatalf("agent_message activity=%v, want the first update and the later new text", activities)
	}
	for _, activity := range activities {
		if len(activity) != 1 || activity["kind"] != "text_delta" {
			t.Fatalf("activity carried more than metadata: %v", activity)
		}
	}
	if len(texts) != 1 || texts[0]["text"] != "visible answer continues\n" || texts[0]["activity"] != true {
		t.Fatalf("text observations=%v, want only the completed assistant message with activity=true", texts)
	}
	live.mu.Lock()
	seen := live.firstSeen
	live.mu.Unlock()
	if !seen {
		t.Fatal("validated agent_message updates did not update liveness")
	}
}

func TestClaudeTextDeltaActivityIsThrottledWithoutBlockingProgress(t *testing.T) {
	var activities []map[string]any
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		if kind == "native_activity" {
			activities = append(activities, data)
		}
		return nil
	})
	fake := time.Now()
	gate.now = func() time.Time { return fake }
	live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
	live.now = func() time.Time { return fake }
	live.dispatched()
	head := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n"
	tail := `{"type":"result","subtype":"success","is_error":false}` + "\n"
	delta := func(uuid, text string) string {
		return `{"type":"stream_event","uuid":"` + uuid + `","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}}}` + "\n"
	}
	if err := parseClaude(strings.NewReader(head+delta("u1", "hello")+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 || len(activities[0]) != 1 || activities[0]["kind"] != "text_delta" {
		t.Fatalf("first text_delta activity=%v", activities)
	}
	fake = fake.Add(500 * time.Millisecond)
	if err := parseClaude(strings.NewReader(head+delta("u2", "world")+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 {
		t.Fatalf("rate-limited text_delta emitted again: %v", activities)
	}
	live.mu.Lock()
	progress, seen := live.lastProgress, live.firstSeen
	live.mu.Unlock()
	if !seen || !progress.Equal(fake) {
		t.Fatalf("throttling blocked live progress: seen=%v progress=%v want=%v", seen, progress, fake)
	}
	fake = fake.Add(activityInterval)
	if err := parseClaude(strings.NewReader(head+delta("u3", "again")+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(activities) != 2 || activities[1]["kind"] != "text_delta" {
		t.Fatalf("activity was not acknowledged after the interval: %v", activities)
	}
}

func TestNativeActivityObservationFailureStopsParsers(t *testing.T) {
	want := errors.New("activity write failed")
	codexStream := `{"type":"thread.started","thread_id":"` + fakeSession + `"}` + "\n" +
		`{"type":"turn.started"}` + "\n" +
		`{"type":"item.updated","item":{"id":"r1","type":"reasoning","text":"metadata"}}` + "\n" +
		`{"type":"turn.completed"}` + "\n"
	codexTextStream := `{"type":"thread.started","thread_id":"` + fakeSession + `"}` + "\n" +
		`{"type":"turn.started"}` + "\n" +
		`{"type":"item.updated","item":{"id":"a1","type":"agent_message","text":"streaming answer"}}` + "\n" +
		`{"type":"turn.completed"}` + "\n"
	claudeStream := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n" +
		`{"type":"stream_event","uuid":"u1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false}` + "\n"
	tests := []struct {
		name  string
		parse func(io.Reader, *bridge, *liveness) error
		head  string
	}{
		{"codex", parseCodex, codexStream},
		{"codex_agent_message", parseCodex, codexTextStream},
		{"claude", parseClaude, claudeStream},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate := newTestGate(t, testRequest().Tools, func(kind string, _ map[string]any) error {
				if kind == "native_activity" {
					return want
				}
				return nil
			})
			live := newLiveness(context.Background(), func() {}, discardObserve, 0, 0, "warn")
			err := test.parse(strings.NewReader(test.head), gate, live)
			if !errors.Is(err, want) || !errors.Is(gate.failure(), want) {
				t.Fatalf("observation failure was swallowed: err=%v gate=%v", err, gate.failure())
			}
		})
	}
}

func TestGeminiKeepsWhitespaceDeltasWithoutRefreshingProgress(t *testing.T) {
	var texts []string
	var flags []bool
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		if kind == "text" {
			texts = append(texts, data["text"].(string))
			flags = append(flags, data["activity"].(bool))
		}
		return nil
	})
	gate.nativePrefix = "mcp_cocogem_"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	old := fake.Add(-time.Minute)
	head := `{"type":"init","session_id":"` + fakeSession + `"}` + "\n"
	tail := `{"type":"result","status":"success"}` + "\n"
	message := func(content string) string {
		return fmt.Sprintf(`{"type":"message","role":"assistant","content":%q}`+"\n", content)
	}
	live := newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
	live.now = func() time.Time { return fake }
	live.dispatched()
	live.mu.Lock()
	live.lastProgress = old
	live.mu.Unlock()
	if err := parseGemini(strings.NewReader(head+message("hello")+message("\n")+message("world")+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts, "") != "hello\nworld" {
		t.Fatalf("whitespace delta was lost: %q", texts)
	}
	want := []bool{true, false, true}
	for index := range want {
		if flags[index] != want[index] {
			t.Fatalf("activity flags=%v, want %v", flags, want)
		}
	}
	live.mu.Lock()
	progress := live.lastProgress
	live.mu.Unlock()
	if !progress.Equal(fake) {
		t.Fatalf("visible deltas did not refresh progress: %v", progress)
	}
	texts, flags = nil, nil
	whitespace := newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
	whitespace.now = func() time.Time { return fake }
	whitespace.dispatched()
	whitespace.mu.Lock()
	whitespace.lastProgress = old
	whitespace.mu.Unlock()
	if err := parseGemini(strings.NewReader(head+message(" \n\t ")+tail), gate, whitespace); err != nil {
		t.Fatal(err)
	}
	whitespace.mu.Lock()
	seen, stale := whitespace.firstSeen, whitespace.lastProgress.Equal(old)
	whitespace.mu.Unlock()
	if seen || !stale || len(texts) != 1 || texts[0] != " \n\t " || flags[0] {
		t.Fatalf("whitespace-only content was mishandled: seen=%v stale=%v texts=%q flags=%v", seen, stale, texts, flags)
	}
	texts, flags = nil, nil
	if err := parseGemini(strings.NewReader(head+message("中文")+message("中文")+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts, "|") != "中文|中文" || len(flags) != 2 || !flags[0] || !flags[1] {
		t.Fatalf("anonymous equal text was deduped: texts=%q flags=%v", texts, flags)
	}
}

func TestClaudeKeepsWhitespaceTextWithoutRefreshingProgress(t *testing.T) {
	var texts []string
	var flags []bool
	gate := newTestGate(t, testRequest().Tools, func(kind string, data map[string]any) error {
		if kind == "text" {
			texts = append(texts, data["text"].(string))
			flags = append(flags, data["activity"].(bool))
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := time.Now()
	old := fake.Add(-time.Minute)
	live := newLiveness(ctx, cancel, discardObserve, 0, time.Minute, "stop")
	live.now = func() time.Time { return fake }
	live.dispatched()
	live.mu.Lock()
	live.lastProgress = old
	live.mu.Unlock()
	head := `{"type":"system","subtype":"init","session_id":"` + fakeSession + `","tools":[]}` + "\n"
	tail := `{"type":"result","subtype":"success","is_error":false}` + "\n"
	whitespace := `{"type":"assistant","message":{"id":"m-ws","content":[{"type":"text","text":" \n\t "}]}}` + "\n"
	if err := parseClaude(strings.NewReader(head+whitespace+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 1 || texts[0] != " \n\t " || flags[0] {
		t.Fatalf("whitespace text block was not kept verbatim: texts=%q flags=%v", texts, flags)
	}
	live.mu.Lock()
	seen, stale := live.firstSeen, live.lastProgress.Equal(old)
	live.mu.Unlock()
	if seen || !stale {
		t.Fatal("whitespace text block refreshed liveness")
	}
	visible := `{"type":"assistant","message":{"id":"m-ok","content":[{"type":"text","text":"answer"}]}}` + "\n"
	if err := parseClaude(strings.NewReader(head+visible+tail), gate, live); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 || texts[1] != "answer\n" || !flags[1] {
		t.Fatalf("visible text kept its previous behavior: texts=%q flags=%v", texts, flags)
	}
}
