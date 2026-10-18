package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseCodexEvent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "completed agent messages",
			input: `{"type":"thread.started"}` + "\n" + `{"type":"item.started","item":{"type":"agent_message","text":"partial"}}` + "\n" + `{"type":"item.completed","item":{"type":"agent_message","text":"你好\nGo!"}}`,
			want:  "你好\nGo!\n",
		},
		{name: "other item", input: `{"type":"item.completed","item":{"type":"reasoning","text":"hidden"}}`},
		{name: "intermediate error", input: `{"type":"error","message":"Reconnecting..."}`},
		{name: "failed turn", input: `{"type":"turn.failed","error":{"message":"quota exceeded"}}`, wantErr: true},
		{name: "malformed", input: `{"type":`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			var err error
			for _, line := range strings.Split(tt.input, "\n") {
				if _, err = parseCodexEvent([]byte(line), &output); err != nil {
					break
				}
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
			if output.String() != tt.want {
				t.Fatalf("output = %q, want %q", output.String(), tt.want)
			}
		})
	}
}

func TestCodexWriteError(t *testing.T) {
	t.Parallel()
	input := `{"type":"item.completed","item":{"type":"agent_message","text":"hello"}}`
	wantErr := errors.New("I/O failure")
	_, err := parseCodexEvent([]byte(input), failingWriter{wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
