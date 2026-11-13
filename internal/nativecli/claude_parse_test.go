package nativecli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// claudeVisibleText joins the text events a parse emitted, in order.
func claudeVisibleText(events []capturedEvent) string {
	var text strings.Builder
	for _, event := range events {
		if event.kind == "text" {
			text.WriteString(event.data["text"].(string))
		}
	}
	return text.String()
}

func claudeFrames(frames ...string) string {
	return strings.Join(frames, "\n") + "\n"
}

func claudeInitFrame() string {
	return `{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":[]}`
}

func claudeResultFrame() string {
	return `{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed","session_id":"` + nativeSession + `"}`
}

func claudeStreamFrame(uuid, event string) string {
	return `{"type":"stream_event","uuid":"` + uuid + `","event":` + event + `}`
}

// The installed CLI streams a block under its API content index, then delivers
// the completed block alone in its own assistant frame. A reply that opens with
// a thinking block therefore streams its text at index 1 and repeats it at
// array position 0. These frames mirror that order as captured from Claude Code
// 2.1.323 with --include-partial-messages (bodies replaced by fixtures).
func TestClaudeThinkingReplyIsNotRepeatedByItsCompleteFrame(t *testing.T) {
	stream := claudeFrames(
		claudeInitFrame(),
		claudeStreamFrame("s1", `{"type":"message_start","message":{"id":"msg_a"}}`),
		claudeStreamFrame("s2", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`),
		claudeStreamFrame("s3", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"weighing the request"}}`),
		claudeStreamFrame("s4", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"c2ln"}}`),
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_a","content":[{"type":"thinking","thinking":"","signature":"c2ln"}]}}`,
		claudeStreamFrame("s5", `{"type":"content_block_stop","index":0}`),
		claudeStreamFrame("s6", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		claudeStreamFrame("s7", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"one plus one "}}`),
		claudeStreamFrame("s8", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"is two."}}`),
		`{"type":"assistant","uuid":"a2","message":{"id":"msg_a","content":[{"type":"text","text":"one plus one is two."}]}}`,
		claudeStreamFrame("s9", `{"type":"content_block_stop","index":1}`),
		claudeStreamFrame("s10", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
		claudeStreamFrame("s11", `{"type":"message_stop"}`),
		claudeResultFrame(),
	)
	events, err := parseWithEvents(t, nil, stream, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claudeVisibleText(events), "one plus one is two."; got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

// Interleaved thinking puts several text blocks in one message. Each one is
// streamed at its own index and completed by a frame at array position 0.
func TestClaudeInterleavedThinkingKeepsEveryTextBlockOnce(t *testing.T) {
	stream := claudeFrames(
		claudeInitFrame(),
		claudeStreamFrame("s1", `{"type":"message_start","message":{"id":"msg_b"}}`),
		claudeStreamFrame("s2", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_b","content":[{"type":"thinking","thinking":""}]}}`,
		claudeStreamFrame("s3", `{"type":"content_block_stop","index":0}`),
		claudeStreamFrame("s4", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		claudeStreamFrame("s5", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Checking the files first."}}`),
		`{"type":"assistant","uuid":"a2","message":{"id":"msg_b","content":[{"type":"text","text":"Checking the files first."}]}}`,
		claudeStreamFrame("s6", `{"type":"content_block_stop","index":1}`),
		claudeStreamFrame("s7", `{"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":""}}`),
		`{"type":"assistant","uuid":"a3","message":{"id":"msg_b","content":[{"type":"thinking","thinking":""}]}}`,
		claudeStreamFrame("s8", `{"type":"content_block_stop","index":2}`),
		claudeStreamFrame("s9", `{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`),
		// The second block is shorter than the first and shares no prefix with it.
		claudeStreamFrame("s10", `{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":" Done."}}`),
		`{"type":"assistant","uuid":"a4","message":{"id":"msg_b","content":[{"type":"text","text":" Done."}]}}`,
		claudeStreamFrame("s11", `{"type":"content_block_stop","index":3}`),
		claudeStreamFrame("s12", `{"type":"message_stop"}`),
		claudeResultFrame(),
	)
	events, err := parseWithEvents(t, nil, stream, parseClaude)
	if err != nil {
		t.Fatalf("a reply with interleaved thinking failed the step: %v", err)
	}
	if got, want := claudeVisibleText(events), "Checking the files first. Done."; got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

// A later block may begin with the whole text of an earlier one. It still
// belongs to its own streamed block and must not be trimmed as a continuation.
func TestClaudeLaterBlockSharingAnEarlierPrefixIsKeptWhole(t *testing.T) {
	stream := claudeFrames(
		claudeInitFrame(),
		claudeStreamFrame("s1", `{"type":"message_start","message":{"id":"msg_c"}}`),
		claudeStreamFrame("s2", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		claudeStreamFrame("s3", `{"type":"content_block_stop","index":0}`),
		claudeStreamFrame("s4", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"OK."}}`),
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_c","content":[{"type":"text","text":"OK."}]}}`,
		claudeStreamFrame("s5", `{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"OK. Here is the plan."}}`),
		`{"type":"assistant","uuid":"a2","message":{"id":"msg_c","content":[{"type":"text","text":"OK. Here is the plan."}]}}`,
		claudeResultFrame(),
	)
	events, err := parseWithEvents(t, nil, stream, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claudeVisibleText(events), "OK.OK. Here is the plan."; got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

// A stream that stopped early is completed from the frame, wherever the block
// sits in the message.
func TestClaudeCompleteFrameFinishesAPartialStreamAfterThinking(t *testing.T) {
	stream := claudeFrames(
		claudeInitFrame(),
		claudeStreamFrame("s1", `{"type":"message_start","message":{"id":"msg_d"}}`),
		claudeStreamFrame("s2", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		claudeStreamFrame("s3", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`),
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_d","content":[{"type":"text","text":"hello world"}]}}`,
		claudeResultFrame(),
	)
	events, err := parseWithEvents(t, nil, stream, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claudeVisibleText(events), "hello world"; got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

// A complete frame that contradicts streamed text is never shown as success,
// including when the streamed block is not at the frame's array position.
func TestClaudeCompleteFrameContradictingStreamedTextFails(t *testing.T) {
	stream := claudeFrames(
		claudeInitFrame(),
		claudeStreamFrame("s1", `{"type":"message_start","message":{"id":"msg_e"}}`),
		claudeStreamFrame("s2", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		claudeStreamFrame("s3", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"the answer is 4"}}`),
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_e","content":[{"type":"text","text":"corrected: the answer is 5"}]}}`,
		claudeResultFrame(),
	)
	_, err := parseWithEvents(t, nil, stream, parseClaude)
	if FailureKindOf(err) != FailureProtocol {
		t.Fatalf("a contradicted stream was accepted: %v", err)
	}
}

// Without partial messages each block still arrives alone at array position 0.
// Two different blocks of one message are two replies, not a rewritten one.
func TestClaudeUnstreamedBlocksOfOneMessageAreAllKept(t *testing.T) {
	stream := claudeFrames(
		claudeInitFrame(),
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_f","content":[{"type":"text","text":"Reading the report."}]}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"msg_f","content":[{"type":"thinking","thinking":""}]}}`,
		`{"type":"assistant","uuid":"a3","message":{"id":"msg_f","content":[{"type":"text","text":"Done."}]}}`,
		claudeResultFrame(),
	)
	events, err := parseWithEvents(t, nil, stream, parseClaude)
	if err != nil {
		t.Fatalf("two unstreamed blocks of one message failed the step: %v", err)
	}
	if got, want := claudeVisibleText(events), "Reading the report.\nDone.\n"; got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

// The same completed block delivered again under a new envelope adds nothing.
func TestClaudeRedeliveredCompleteFrameAddsNothing(t *testing.T) {
	stream := claudeFrames(
		claudeInitFrame(),
		claudeStreamFrame("s1", `{"type":"message_start","message":{"id":"msg_g"}}`),
		claudeStreamFrame("s2", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		claudeStreamFrame("s3", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"final answer"}}`),
		`{"type":"assistant","uuid":"a1","message":{"id":"msg_g","content":[{"type":"text","text":"final answer"}]}}`,
		`{"type":"assistant","uuid":"a2","message":{"id":"msg_g","stop_reason":"end_turn","content":[{"type":"text","text":"final answer"}]}}`,
		claudeResultFrame(),
	)
	events, err := parseWithEvents(t, nil, stream, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claudeVisibleText(events), "final answer"; got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
}

// claudeAPIFailure renders the two frames Claude Code 2.1.323 ends a turn with
// when its endpoint fails: a synthetic assistant frame carrying the error
// category and HTTP status, then a result frame that reports subtype=success
// with is_error=true. Captured against a local fixture endpoint.
func claudeAPIFailure(category, status, text string) string {
	return claudeFrames(
		claudeInitFrame(),
		`{"type":"assistant","uuid":"e1","is_api_error_message":true,"error":"`+category+`","api_error_status":`+status+`,"message":{"id":"synthetic-1","model":"<synthetic>","content":[{"type":"text","text":"`+text+`"}]}}`,
		`{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","stop_reason":"stop_sequence","api_error_status":`+status+`,"num_turns":1,"result":"`+text+`","session_id":"`+nativeSession+`"}`,
	)
}

func TestClaudeEndpointFailuresAreClassifiedFromStructuredFields(t *testing.T) {
	tests := []struct {
		name     string
		category string
		status   string
		text     string
		want     FailureKind
		reason   string
	}{
		{
			name: "overloaded", category: "server_error", status: "529",
			text: "API Error: 529 fixture upstream failure. This is a server-side issue, usually temporary",
			want: FailureProviderTransient, reason: "status=529",
		},
		{
			name: "gateway origin error", category: "server_error", status: "524",
			text: "API Error: 524 fixture upstream failure",
			want: FailureProviderTransient, reason: "status=524",
		},
		{
			name: "server error with no recognisable wording", category: "server_error", status: "500",
			text: "API Error: 500 upstream said no",
			want: FailureProviderTransient, reason: "status=500",
		},
		{
			name: "rate limit", category: "rate_limit", status: "429",
			text: "API Error: Request rejected (429)",
			want: FailureQuota, reason: "status=429",
		},
		{
			name: "authentication", category: "authentication_failed", status: "401",
			text: "Failed to authenticate. API Error: 401 fixture upstream failure",
			want: FailureAuthentication, reason: "status=401",
		},
		{
			name: "bad request stays unknown", category: "unknown", status: "400",
			text: "API Error: 400 fixture upstream failure",
			want: FailureUnknown, reason: "status=400",
		},
		{
			// A proxy cut the stream while the model was still thinking. The
			// byte and timing counts in the text must not be read as HTTP codes.
			name: "truncated stream", category: "unknown", status: "null",
			text: "API Error: API returned an empty or malformed response (HTTP 200) — check for a proxy or gateway intercepting the request. Response: content-type event-stream, 1403 bytes, request-id absent. This was the non-streaming retry of streaming request, which failed with: stream_truncated, StreamTruncatedError; 6 stream events received, first after 429 ms.",
			want: FailureNetwork, reason: "category=incomplete_response",
		},
		{
			// The frames behind "result=success, category=execution": the endpoint
			// answered 200 with a stream the CLI could not use, as recorded in a
			// managed session transcript.
			name: "malformed stream reported as a server error", category: "server_error", status: "null",
			text: "API Error: The response stream was malformed and no response was produced. Try again.",
			want: FailureProviderTransient, reason: "category=connection",
		},
		{
			name: "category without a status", category: "rate_limit", status: "null",
			text: "API Error: please slow down",
			want: FailureQuota, reason: "category=quota",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseWithEvents(t, nil, claudeAPIFailure(test.category, test.status, test.text), parseClaude)
			if got := FailureKindOf(err); got != test.want {
				t.Fatalf("kind = %q, want %q (%v)", got, test.want, err)
			}
			if !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("reason %q does not name %q", err.Error(), test.reason)
			}
			if strings.Contains(err.Error(), "fixture upstream failure") || strings.Contains(err.Error(), "bytes") {
				t.Fatalf("reason echoes CLI output: %q", err.Error())
			}
		})
	}
}

// claudeToolEvidence parses stream with an adapter that reports every tool and
// returns the terminal failure as the provider would hand it to the workflow.
func claudeToolEvidence(t *testing.T, native []string, stream string) error {
	t.Helper()
	gate := newTestGate(t, testRequest().Tools, discardObserve)
	gate.setNative(native)
	gate.reportsEveryTool = true
	live := newLiveness(t.Context(), func() {}, discardObserve, 0, 0, "warn")
	err := parseClaude(strings.NewReader(stream), gate, live)
	if gate.toolFree() {
		err = toolFreeFailure(err)
	}
	return err
}

func TestClaudeFailureBeforeAnyToolIsProvenToolFree(t *testing.T) {
	failure := `{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","api_error_status":529,"result":"API Error: 529","session_id":"` + nativeSession + `"}`
	thinking := claudeStreamFrame("s1", `{"type":"message_start","message":{"id":"msg_t"}}`) + "\n" +
		claudeStreamFrame("s2", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`) + "\n" +
		claudeStreamFrame("s3", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"considering"}}`)
	tests := []struct {
		name   string
		native []string
		frames []string
		want   bool
	}{
		{name: "only thinking before the failure", frames: []string{thinking}, want: true},
		{name: "text but no tool", frames: []string{`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"partial answer"}]}}`}, want: true},
		{
			name: "a native tool ran", native: []string{"Read"},
			frames: []string{
				`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{"file_path":"/workspace/a"}}]}}`,
				`{"type":"user","message":{"id":"m2","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"ok"}]}}`,
			},
		},
		{
			name:   "a tool request was only streamed",
			frames: []string{claudeStreamFrame("s9", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_2","name":"Bash","input":{}}}`)},
		},
		{
			name:   "a host tool result arrived",
			frames: []string{`{"type":"user","message":{"id":"m3","content":[{"type":"tool_result","tool_use_id":"host_1","content":"done"}]}}`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := claudeFrames(append(append([]string{claudeInitFrame()}, test.frames...), failure)...)
			err := claudeToolEvidence(t, test.native, stream)
			if FailureKindOf(err) != FailureProviderTransient {
				t.Fatalf("unexpected failure: %v", err)
			}
			if got := ToolFree(err); got != test.want {
				t.Fatalf("ToolFree = %v, want %v", got, test.want)
			}
		})
	}
}

func TestToolFreeNeedsProofOnEveryLeaf(t *testing.T) {
	proven := &Failure{Kind: FailureNetwork, Reason: "endpoint failed", ToolFree: true}
	unproven := &Failure{Kind: FailureNetwork, Reason: "endpoint failed"}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil},
		{name: "proven failure", err: proven, want: true},
		{name: "wrapped proven failure", err: fmt.Errorf("step: %w", proven), want: true},
		{name: "unproven failure", err: unproven},
		{name: "proven next to a cancellation", err: errors.Join(context.Canceled, proven)},
		{name: "proven next to an unverified process", err: errors.Join(newFailure(FailureProcessUncertain, "group survived"), proven)},
		{name: "proven next to an untyped error", err: errors.Join(proven, errors.New("stdio closed"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ToolFree(test.err); got != test.want {
				t.Fatalf("ToolFree = %v, want %v", got, test.want)
			}
		})
	}
	// A failure inferred from stdio rather than reported by the CLI is never
	// upgraded, whatever the adapter saw.
	inferred := newFailure(FailureNetwork, "output could not be read")
	if ToolFree(toolFreeFailure(inferred)) {
		t.Fatal("an unconfirmed failure was marked tool-free")
	}
}
