package nativecli

import (
	"strings"
	"testing"
)

func reviewPartialPrefix() string {
	return strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"` + nativeSession + `","tools":[]}`,
		`{"type":"stream_event","uuid":"s1","event":{"type":"message_start","message":{"id":"msg1"}}}`,
		`{"type":"stream_event","uuid":"s2","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","uuid":"s3","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}`,
	}, "\n") + "\n"
}
func TestStreamUpdatedSnapshotsDoNotDuplicate(t *testing.T) {
	stream := reviewPartialPrefix() + strings.Join([]string{
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"text","text":"hello world"}]}}`,
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"text","text":"hello world!"}]}}`,
		`{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}`,
	}, "\n") + "\n"
	events, err := parseWithEvents(t, nil, stream, parseClaude)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, event := range events {
		if event.kind == "text" {
			text.WriteString(event.data["text"].(string))
		}
	}
	if got := strings.TrimSpace(text.String()); got != "hello world!" {
		t.Fatalf("visible text duplicated: got %q, want %q", got, "hello world!")
	}
}
func TestStreamDivergenceCannotReportSuccess(t *testing.T) {
	stream := reviewPartialPrefix() + strings.Join([]string{
		`{"type":"assistant","message":{"id":"msg1","content":[{"type":"text","text":"corrected final answer"}]}}`,
		`{"type":"result","subtype":"success","session_id":"` + nativeSession + `"}`,
	}, "\n") + "\n"
	_, err := parseWithEvents(t, nil, stream, parseClaude)
	if err == nil {
		t.Fatal("divergent authoritative final answer was silently discarded while reporting success")
	}
}
