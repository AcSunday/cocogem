package main

import (
	"io"
	"strings"
	"testing"
)

func TestInvokeValidation(t *testing.T) {
	t.Parallel()
	for _, request := range []Request{
		{Prompt: " "},
		{Prompt: "hello", SessionID: "latest"},
		{Prompt: "hello", SessionID: "--unsafe-option"},
	} {
		if _, err := Invoke("claude", request, io.Discard); err == nil {
			t.Fatalf("invalid request succeeded: %+v", request)
		}
	}
	if _, err := Invoke("claude", Request{Prompt: "hello"}, nil); err == nil {
		t.Fatal("nil writer must fail")
	}
	if _, err := Invoke("other", Request{Prompt: "hello"}, io.Discard); err == nil || !strings.Contains(err.Error(), "unsupported CLI") {
		t.Fatalf("error = %v, want unsupported CLI", err)
	}
}

func TestSessionEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		parse eventParser
		input string
	}{
		{name: "claude", parse: parseClaudeEvent, input: `{"type":"system","subtype":"init","session_id":"` + testSessionID + `"}`},
		{name: "codex", parse: parseCodexEvent, input: `{"type":"thread.started","thread_id":"` + testSessionID + `"}`},
		{name: "gemini", parse: parseGeminiEvent, input: `{"type":"init","session_id":"` + testSessionID + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, err := scanEvents(tt.name, strings.NewReader(tt.input+"\n{}"), io.Discard, tt.parse)
			if err != nil || id != testSessionID {
				t.Fatalf("session ID = %q, error = %v", id, err)
			}
		})
	}
}
