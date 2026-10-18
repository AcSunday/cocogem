package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunInvalidArguments(t *testing.T) {
	t.Parallel()
	var output, diagnostics bytes.Buffer
	if code := Run(nil, &output, &diagnostics); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if output.Len() != 0 || !strings.Contains(diagnostics.String(), "用法：cocogem") {
		t.Fatalf("stdout=%q stderr=%q", output.String(), diagnostics.String())
	}
}

func TestParseArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		wantCLI string
		want    string
		wantErr bool
		resume  string
	}{
		{name: "default claude", args: []string{"hello"}, wantCLI: "claude", want: "hello"},
		{name: "explicit claude", args: []string{"claude", "hello"}, wantCLI: "claude", want: "hello"},
		{name: "codex", args: []string{"codex", "hello"}, wantCLI: "codex", want: "hello"},
		{name: "gemini", args: []string{"gemini", "hello"}, wantCLI: "gemini", want: "hello"},
		{name: "prompt can start with dash", args: []string{"-hello"}, wantCLI: "claude", want: "-hello"},
		{name: "missing prompt", wantErr: true},
		{name: "empty prompt", args: []string{"  "}, wantErr: true},
		{name: "unknown cli", args: []string{"other", "hello"}, wantErr: true},
		{name: "too many args", args: []string{"claude", "hello", "extra"}, wantErr: true},
		{name: "resume latest", args: []string{"codex", "--resume", "latest", "hello"}, wantCLI: "codex", want: "hello", resume: "latest"},
		{name: "resume ID", args: []string{"gemini", "--resume=" + testSessionID, "hello"}, wantCLI: "gemini", want: "hello", resume: testSessionID},
		{name: "default resume", args: []string{"--resume", "latest", "hello"}, wantCLI: "claude", want: "hello", resume: "latest"},
		{name: "literal resume flag", args: []string{"codex", "--", "--resume=latest"}, wantCLI: "codex", want: "--resume=latest"},
		{name: "invalid ID", args: []string{"codex", "--resume", "--unsafe", "hello"}, wantErr: true},
		{name: "missing resume prompt", args: []string{"codex", "--resume", "latest"}, wantErr: true},
		{name: "missing prompt after equals flag", args: []string{"codex", "--resume=latest"}, wantErr: true},
		{name: "empty resume", args: []string{"codex", "--resume=", "hello"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opt, err := parseArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
			if opt.CLI != tt.wantCLI || opt.Prompt != tt.want {
				t.Fatalf("got (%q, %q), want (%q, %q)", opt.CLI, opt.Prompt, tt.wantCLI, tt.want)
			}
			if opt.Resume != tt.resume {
				t.Fatalf("resume = %q, want %q", opt.Resume, tt.resume)
			}
		})
	}
}
