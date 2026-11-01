package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParsePiEvent(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	parser := &piEvents{}
	events := []string{
		`{"type":"session","id":"` + testSessionID + `"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"你好"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"，Pi"}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`,
		`{"type":"agent_settled","aborted":false}`,
	}
	for _, event := range events {
		if _, err := parser.parse([]byte(event), &output); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := output.String(), "你好，Pi\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestParsePiErrors(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	parser := &piEvents{}
	id, err := parser.parse([]byte(`{"type":"session","id":"`+testSessionID+`"}`), &output)
	if err != nil || id != testSessionID {
		t.Fatalf("session id = %q, error = %v", id, err)
	}
	if _, err := parser.parse([]byte(`{"type":"auto_retry_end","success":false,"finalError":"quota exceeded"}`), &output); err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("error = %v, want quota exceeded", err)
	}
	if _, err := parser.parse([]byte(`{"type":"agent_settled","aborted":true}`), &output); err == nil {
		t.Fatal("aborted run should return an error")
	}
	if _, err := parser.parse([]byte(`{"type":`), &output); err == nil {
		t.Fatal("malformed JSON should return an error")
	}
}

func TestParsePiWriteError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("I/O failure")
	_, err := new(piEvents).parse([]byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello"}}`), failingWriter{wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestPiTerminalStatus(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		stop string
		want string
	}{
		{name: "authentication", stop: "error", want: "synthetic invalid api key"},
		{name: "aborted message", stop: "aborted", want: "synthetic invalid api key"},
		{name: "truncated", stop: "length", want: "truncated"},
		{name: "unfinished tool loop", stop: "toolUse", want: "completed assistant reply"},
		{name: "missing result", want: "completed assistant reply"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parser := &piEvents{}
			message := `{"type":"message_end","message":{"role":"assistant","stopReason":"` + test.stop + `","errorMessage":"synthetic invalid api key"}}`
			if _, err := parser.parse([]byte(message), &bytes.Buffer{}); err != nil {
				t.Fatalf("an intermediate failed message must allow Pi recovery: %v", err)
			}
			_, err := parser.parse([]byte(`{"type":"agent_settled","aborted":false}`), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("terminal error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPiRetryCanRecoverBeforeSettling(t *testing.T) {
	t.Parallel()
	parser := &piEvents{}
	var output bytes.Buffer
	for _, event := range []string{
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"transient failure"}}`,
		`{"type":"auto_retry_start"}`,
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"recovered"}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`,
		`{"type":"auto_retry_end","success":true}`,
		`{"type":"agent_settled","aborted":false}`,
	} {
		if _, err := parser.parse([]byte(event), &output); err != nil {
			t.Fatal(err)
		}
	}
	if !parser.finished || output.String() != "recovered\n" {
		t.Fatalf("finished=%v output=%q", parser.finished, output.String())
	}
}
