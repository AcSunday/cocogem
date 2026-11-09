package nativecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestClaudeManagedSessionPersistsAndResumesFromPrivateStorage(t *testing.T) {
	workspace := t.TempDir()
	storage := filepath.Join(t.TempDir(), "server-session")
	request := model.Request{
		ExecutionMode: "full_access", NativeTools: []string{"Write", "Bash"},
		SessionStorageRoot: storage, SessionMode: "fresh",
	}
	args, env, cwd := runNativeFixture(t, request, workspace)
	if capturedArg(args, "--resume") != "" || strings.Contains(args, "--no-session-persistence") {
		t.Fatalf("fresh managed invocation did not enable persistence: %s", args)
	}
	if capturedArg(args, "--permission-mode") != "bypassPermissions" || capturedArg(args, "--tools") != "Write,Bash" {
		t.Fatalf("explicit full-access tools were not launched: %s", args)
	}
	if cwd != workspace && !strings.Contains(cwd, strings.TrimPrefix(workspace, "/private")) {
		t.Fatalf("native execution cwd=%q, want authorized workspace %q", cwd, workspace)
	}
	if !strings.Contains(env, "CLAUDE_CONFIG_DIR="+filepath.Join(storage, ".cocogem", "config")+"\n") {
		t.Fatalf("managed Claude mixed its session with terminal configuration: %s", env)
	}
	if info, err := os.Stat(storage); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("managed session storage is not private: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(storage, ".cocogem", "config", "settings.json")); err != nil {
		t.Fatalf("managed CLI state was removed after the attempt: %v", err)
	}

	request.SessionMode, request.SessionID = "resume", "11111111-1111-4111-8111-111111111111"
	args, _, _ = runNativeFixture(t, request, workspace)
	if capturedArg(args, "--resume") != request.SessionID || strings.Contains(args, "--no-session-persistence") {
		t.Fatalf("resume invocation lost its exact native identity: %s", args)
	}
}

func TestClaudeResumeAcknowledgementRejectsChangedIdentity(t *testing.T) {
	const expected = "11111111-1111-4111-8111-111111111111"
	for _, test := range []struct {
		name, observed, mode string
		wantFailure          bool
	}{
		{name: "exact identity", observed: expected, mode: "resume"},
		{name: "changed identity", observed: "22222222-2222-4222-8222-222222222222", mode: "resume", wantFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			events := []capturedEvent{}
			observe := func(kind string, data map[string]any) error {
				events = append(events, capturedEvent{kind: kind, data: data})
				return nil
			}
			gate, err := newBridge(ctx, nil, func(context.Context, string, json.RawMessage) (string, error) { return "", nil }, observe)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.close()
			gate.sessionMode, gate.sessionID = "resume", expected
			live := newLiveness(ctx, cancel, observe, time.Minute, time.Minute, "stop")
			live.dispatched()
			stream := `{"type":"system","subtype":"init","session_id":"` + test.observed + `","tools":[]}` + "\n" +
				`{"type":"result","subtype":"success","session_id":"` + test.observed + `"}` + "\n"
			err = parseClaude(strings.NewReader(stream), gate, live)
			if test.wantFailure {
				if FailureKindOf(err) != FailureProtocol {
					t.Fatalf("changed resume was accepted: %v", err)
				}
				for _, event := range events {
					if event.kind == "native_session" {
						t.Fatalf("invalid resume emitted a runner acknowledgement: %+v", event.data)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.kind == "native_session" && event.data["session_id"] == expected && event.data["mode"] == test.mode {
					found = true
				}
			}
			if !found {
				t.Fatalf("valid resume was not acknowledged: %+v", events)
			}
		})
	}
}
