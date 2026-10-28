package nativecli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexCapabilityCompatibilityAndReasons(t *testing.T) {
	t.Parallel()
	help := "--ignore-user-config --ignore-rules --ephemeral --sandbox --json"
	for _, test := range []struct {
		name, version, help, reason string
	}{
		{name: "reviewed version", version: "codex-cli 0.163.0", help: help},
		{name: "newer capability-compatible version", version: "codex-cli 0.164.0", help: help},
		{name: "newer unknown version with required capabilities", version: "codex-cli 0.200.0", help: help},
		{name: "prerelease with required capabilities", version: "codex-cli 0.163.0-alpha.1", help: help},
		{name: "known incompatible baseline", version: "codex-cli 0.99.0", help: help, reason: "低于已验证的协议基线"},
		{name: "failed version probe", help: help, reason: "版本探测失败"},
		{name: "missing isolation", version: "codex-cli 0.164.0", help: "--json --sandbox", reason: "缺少受限执行参数 --ignore-user-config"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := capabilityError("codex", test.version, test.help)
			if test.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("capability error=%v, want %q", err, test.reason)
			}
		})
	}
}

func TestNativeFailureReportsProtocolMismatchWithoutProviderDetails(t *testing.T) {
	err := nativeFailure("result_error", `{"error":{"code":400,"message":"This account uses an OpenAI-compatible upstream. Use /v1/chat/completions or /v1/responses. https://private.example/v1?token=private-value"}}`)
	if !strings.Contains(err.Error(), "category=protocol_mismatch") || !strings.Contains(err.Error(), "endpoint and provider mapping") {
		t.Fatal("native API protocol rejection was not identified", err)
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "https://") || strings.Contains(err.Error(), "login") {
		t.Fatal("provider error leaked details or suggested credential changes", err)
	}
}

func TestCheckDetectsReplacedProgramWithSameVersion(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.310 (Claude Code)';;\n--help) echo '--restricted --tools --strict-mcp-config --setting-sources --permission-prompts --no-session-persistence --settings --disable-slash-commands';;\nesac\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	first, err := Check(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script+"# replaced\n"), 0700); err != nil {
		t.Fatal(err)
	}
	second, err := Check(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != second.Version || first.Identity == second.Identity {
		t.Fatalf("same version replacement was not detected: %q %q", first.Identity, second.Identity)
	}
}

type expiredReader struct {
	reader  io.Reader
	live    *liveness
	expired bool
}

func (r *expiredReader) Read(p []byte) (int, error) {
	if !r.expired {
		r.expired = true
		r.live.mu.Lock()
		r.live.lastProgress = time.Now().Add(-time.Hour)
		r.live.mu.Unlock()
	}
	return r.reader.Read(p)
}

func TestInspectOwnershipOfExitedProcessIsNotFatal(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, owned, err := inspectOwnership(pid, "/bin/sh"); err != nil || owned {
		t.Fatalf("an already exited CLI must not fail ownership with a lost result: owned=%v err=%v", owned, err)
	}
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	if _, owned, err := inspectOwnership(live.Process.Pid, live.Path); err != nil || !owned {
		t.Fatalf("a running CLI was not recorded: owned=%v err=%v", owned, err)
	}
}

func TestClaudeStatusNoticeDoesNotResetStall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observe := func(string, map[string]any) error { return nil }
	gate, err := newBridge(ctx, nil, nil, observe)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.close()
	live := newLiveness(ctx, cancel, observe, time.Hour, time.Second, "stop")
	live.dispatched()
	live.firstResponse()
	init := "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"11111111-1111-4111-8111-111111111111\",\"tools\":[]}\n"
	notice := "{\"type\":\"system\",\"subtype\":\"status\",\"status\":\"idle\"}\n"
	reader := io.MultiReader(strings.NewReader(init), &expiredReader{reader: strings.NewReader(notice), live: live})
	_ = parseClaude(reader, gate, live)
	if live.check() {
		t.Fatal("a non-progress system status notification reset an expired stall deadline")
	}
}

func TestUnknownToolActionsAreRejected(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		name   string
		parse  func(io.Reader, *bridge, *liveness) error
		stream string
	}{
		{"claude", parseClaude, "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"" + session + "\",\"tools\":[]}\n{\"type\":\"tool_started\",\"name\":\"Bash\",\"input\":{\"command\":\"synthetic-only\"}}\n{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"" + session + "\"}\n"},
		{"codex", parseCodex, "{\"type\":\"thread.started\",\"thread_id\":\"" + session + "\"}\n{\"type\":\"turn.started\"}\n{\"type\":\"tool.started\",\"name\":\"shell\",\"arguments\":{\"command\":\"synthetic-only\"}}\n{\"type\":\"turn.completed\"}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			observe := func(string, map[string]any) error { return nil }
			gate, err := newBridge(ctx, nil, nil, observe)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.close()
			live := newLiveness(ctx, cancel, observe, time.Minute, time.Minute, "stop")
			live.dispatched()
			if err := test.parse(strings.NewReader(test.stream), gate, live); err == nil {
				t.Fatal("unknown tool action was ignored and a successful terminal result was accepted")
			}
		})
	}
}
