package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseClaudeEvent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "assistant text blocks",
			input: `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"你好"},{"type":"tool_use"},{"type":"text","text":"Go!"}]}}`,
			want:  "你好\nGo!\n",
		},
		{name: "non assistant", input: `{"type":"result","subtype":"success"}`},
		{name: "failed result", input: `{"type":"result","is_error":true,"result":"quota exceeded"}`, wantErr: true},
		{name: "non assistant with string message", input: `{"type":"system","message":"ignored"}`},
		{name: "malformed assistant message", input: `{"type":"assistant","message":"invalid"}`, wantErr: true},
		{name: "malformed", input: `{"type":`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			_, err := parseClaudeEvent([]byte(tt.input), &output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
			if output.String() != tt.want {
				t.Fatalf("output = %q, want %q", output.String(), tt.want)
			}
		})
	}
}

func TestClaudeWriteError(t *testing.T) {
	t.Parallel()
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`
	wantErr := errors.New("I/O failure")
	_, err := parseClaudeEvent([]byte(input), failingWriter{wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
