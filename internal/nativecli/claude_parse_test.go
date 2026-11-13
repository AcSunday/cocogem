package nativecli

import (
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
