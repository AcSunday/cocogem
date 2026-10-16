package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseGeminiEvent(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	events := []string{
		`{"type":"init","session_id":"abc"}`,
		`{"type":"message","role":"user","content":"不要打印"}`,
		`{"type":"message","role":"assistant","content":"你好" ,"delta":true}`,
		`{"type":"message","role":"assistant","content":"，Gemini" ,"delta":true}`,
		`{"type":"result","status":"success"}`,
	}
	for _, event := range events {
		if _, err := parseGeminiEvent([]byte(event), &output); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := output.String(), "你好，Gemini\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestParseGeminiError(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	_, err := parseGeminiEvent([]byte(`{"type":"result","status":"error","error":{"message":"quota exceeded"}}`), &output)
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("error = %v, want quota exceeded", err)
	}
	if _, err := parseGeminiEvent([]byte(`{"type":`), &output); err == nil {
		t.Fatal("malformed JSON should return an error")
	}
}

func TestParseGeminiWriteError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("I/O failure")
	_, err := parseGeminiEvent([]byte(`{"type":"message","role":"assistant","content":"hello"}`), failingWriter{wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
