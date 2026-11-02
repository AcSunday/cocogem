package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const testOpenCodeSession = "ses_abcdefghijklmnopqrstuvwxyz"

func TestOpenCodeEvents(t *testing.T) {
	t.Parallel()
	parser := &openCodeEvents{}
	var output bytes.Buffer
	for _, event := range []string{
		`{"type":"step_start","sessionID":"` + testOpenCodeSession + `"}`,
		`{"type":"text","sessionID":"` + testOpenCodeSession + `","part":{"text":"你好"}}`,
		`{"type":"step_finish","sessionID":"` + testOpenCodeSession + `","part":{"reason":"tool-calls"}}`,
		`{"type":"step_finish","sessionID":"` + testOpenCodeSession + `","part":{"reason":"stop"}}`,
	} {
		id, err := parser.parse([]byte(event), &output)
		if err != nil || id != testOpenCodeSession {
			t.Fatalf("id=%q err=%v", id, err)
		}
	}
	if !parser.finished || output.String() != "你好\n" {
		t.Fatalf("finished=%v output=%q", parser.finished, output.String())
	}
	if _, err := parser.parse([]byte(`{"type":"text","sessionID":"`+testOpenCodeSession+`","part":{"text":"late"}}`), &output); err == nil {
		t.Fatal("accepted data after completion")
	}
}

func TestOpenCodeErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, event, want string }{
		{"provider error", `{"type":"error","error":{"message":"synthetic invalid api key"}}`, "synthetic invalid api key"},
		{"legacy provider error", `{"type":"error","error":{"data":{"message":"quota exceeded"}}}`, "quota exceeded"},
		{"truncated", `{"type":"step_finish","sessionID":"` + testOpenCodeSession + `","part":{"reason":"length"}}`, "complete"},
		{"invalid session", `{"type":"text","sessionID":"invalid","part":{"text":"hello"}}`, "invalid session"},
		{"malformed", `{"type":`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := new(openCodeEvents).parse([]byte(test.event), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v want %q", err, test.want)
			}
		})
	}
	problem := errors.New("write failed")
	_, err := new(openCodeEvents).parse([]byte(`{"type":"text","sessionID":"`+testOpenCodeSession+`","part":{"text":"hello"}}`), failingWriter{problem})
	if !errors.Is(err, problem) {
		t.Fatal(err)
	}
}

func TestOpenCodeSessionRecovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := saveSession(dir, Result{CLI: "opencode", SessionID: testOpenCodeSession}); err != nil {
		t.Fatal(err)
	}
	id, err := loadSession(dir, "opencode")
	if err != nil || id != testOpenCodeSession {
		t.Fatalf("session=%q err=%v", id, err)
	}
	if err := validateCLISessionID("codex", testOpenCodeSession); err == nil {
		t.Fatal("OpenCode ID accepted for another CLI")
	}
	if err := validateCLISessionID("opencode", testSessionID); err == nil {
		t.Fatal("UUID accepted for OpenCode")
	}
}
