//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommand(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	binary := filepath.Join(t.TempDir(), "cocogem")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	for _, provider := range []string{"claude", "codex", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			const session = "12345678-1234-1234-1234-123456789abc"
			stub := `#!/bin/sh
printf '%s\n' "$@" > args.txt
if [ "$COCOGEM_TEST_FAIL" = 1 ]; then exit 7; fi
case "$0" in
  *claude)
    printf '%s\n' '{"type":"system","subtype":"init","session_id":"` + session + `"}' '{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}'
    ;;
  *codex)
    cat > prompt.txt
    printf '%s\n' '{"type":"thread.started","thread_id":"` + session + `"}' '{"type":"item.completed","item":{"type":"agent_message","text":"hello"}}'
    ;;
  *gemini)
    printf '%s\n' '{"type":"init","session_id":"` + session + `"}' '{"type":"message","role":"assistant","content":"hello"}' '{"type":"result","status":"success"}'
    ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, provider), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			run := func(wantCode int, fail bool, args ...string) (string, string) {
				t.Helper()
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "COCOGEM_TEST_FAIL=0")
				if fail {
					cmd.Env = append(cmd.Env, "COCOGEM_TEST_FAIL=1")
				}
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				code := 0
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatal(err)
					}
					code = exit.ExitCode()
				}
				if code != wantCode {
					t.Fatalf("exit=%d, want=%d; stderr=%s", code, wantCode, &stderr)
				}
				return stdout.String(), stderr.String()
			}
			_, usage := run(2, false)
			if !strings.Contains(usage, "用法：cocogem") {
				t.Fatal(usage)
			}
			const prompt = "a prompt with spaces and $(literal)"
			out, diagnostics := run(0, false, provider, prompt)
			if out != "hello\n" || !strings.Contains(diagnostics, session) {
				t.Fatalf("stdout=%q stderr=%q", out, diagnostics)
			}
			sessionPath := filepath.Join(dir, ".cocogem", provider+".json")
			before, err := os.ReadFile(sessionPath)
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				CLI       string `json:"cli"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(before, &record); err != nil || record.CLI != provider || record.SessionID != session {
				t.Fatalf("session=%s error=%v", before, err)
			}
			if provider == "codex" {
				body, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
				if err != nil || string(body) != prompt {
					t.Fatalf("stdin=%q error=%v", body, err)
				}
			}
			run(0, false, provider, "--resume", "latest", "continue")
			args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
			if err != nil || !strings.Contains(string(args), session) {
				t.Fatalf("resume args=%q error=%v", args, err)
			}
			run(1, true, provider, "will fail")
			after, err := os.ReadFile(sessionPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed call changed session: %s error=%v", after, err)
			}
		})
	}
}
