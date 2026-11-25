package nativecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestPiAndCodexRejectChangedResumeIdentity(t *testing.T) {
	for _, adapter := range []string{"pi-print", "pi-rpc", "codex-exec"} {
		t.Run(adapter, func(t *testing.T) {
			ack := false
			gate := newTestGate(t, nil, func(kind string, _ map[string]any) error {
				ack = ack || kind == "native_session"
				return nil
			})
			gate.sessionMode, gate.sessionID = "resume", fakeSession
			input := newTestPiStdin()
			var err error
			switch adapter {
			case "pi-print":
				err = parsePi(strings.NewReader(`{"type":"session","id":"`+piSession+`"}`), gate, nil)
			case "pi-rpc":
				err = parsePiRPC(strings.NewReader(strings.Join(settledLines(t), "")), gate, nil, input)
			case "codex-exec":
				err = parseCodex(strings.NewReader(`{"type":"thread.started","thread_id":"`+piSession+`"}`), gate, nil)
			}
			if FailureKindOf(err) != FailureProtocol || ack || input.sent {
				t.Fatalf("changed identity: err=%v acknowledged=%v prompted=%v", err, ack, input.sent)
			}
		})
	}
}

func TestCodexPlanOnlyDisablesNativeMutations(t *testing.T) {
	for _, transport := range []string{"app-server", "exec"} {
		t.Run(transport, func(t *testing.T) {
			dir := t.TempDir()
			request := model.Request{Executor: "codex", Model: "fixture", ExecutionMode: "full_access", ReadOnly: true,
				NativeTools: []string{nativeCodexCommand, nativeCodexFile}, SessionStorageRoot: t.TempDir()}
			gate := newTestGate(t, nil, discardObserve)
			gate.trustedNative = true
			var args []string
			var err error
			switch transport {
			case "app-server":
				args, _, err = prepareCodexAppServer(request, dir, gate, nil, "", nil, nil, nil)
			case "exec":
				args, _, err = prepareCodex(request, dir, gate, true, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "models.json"))
			if err != nil {
				t.Fatal(err)
			}
			var catalog struct {
				Models []struct {
					Shell string  `json:"shell_type"`
					Patch *string `json:"apply_patch_tool_type"`
				}
			}
			if err := json.Unmarshal(raw, &catalog); err != nil || len(catalog.Models) != 1 {
				t.Fatalf("invalid model catalog: %s, %v", raw, err)
			}
			if catalog.Models[0].Shell != "disabled" || catalog.Models[0].Patch != nil || !slices.Contains(args, "features.shell_tool=false") {
				t.Fatalf("read-only shell=%q patch=%v shell disabled=%v", catalog.Models[0].Shell, catalog.Models[0].Patch != nil, slices.Contains(args, "features.shell_tool=false"))
			}
		})
	}
}

func TestPiManagedSessionRequiresPersistentStorageAndExactID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_HOME", root)
	request := model.Request{SessionStorageRoot: t.TempDir(), SessionMode: "resume", SessionID: "some-session.jsonl"}
	if _, err := piSessionArgs(request); err == nil {
		t.Fatal("a path was accepted in place of the recorded session ID")
	}
	request.SessionID = piSession
	if err := os.WriteFile(filepath.Join(root, "sessions"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := piSessionArgs(request); err == nil {
		t.Fatal("an inaccessible native session directory allowed a managed launch")
	}
	request.EphemeralSession = true
	args, err := piSessionArgs(request)
	if err != nil || !slices.Equal(args, []string{"--no-session"}) {
		t.Fatalf("disposable launch: args=%v err=%v", args, err)
	}
}

func TestCodexExecResumePreservesParentFlags(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	request := model.Request{Executor: "codex", Model: "fixture", ReadOnly: true,
		SessionStorageRoot: t.TempDir(), SessionMode: "resume", SessionID: fakeSession}
	plan := &connectionPlan{codexResolved: "fixture", codexEnv: map[string]string{"COCOGEM_CODEX_CONNECTION_HEADER_0": "synthetic"}}
	args, _, err := prepareCodex(request, t.TempDir(), newTestGate(t, nil, discardObserve), false, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.codexEnv) != 1 {
		t.Fatal("attempt-specific environment leaked into the shared connection plan")
	}
	index := slices.Index(args, "resume")
	if index < 0 || !slices.Equal(args[index:], []string{"resume", fakeSession}) || slices.Contains(args, "--ephemeral") {
		t.Fatalf("invalid managed resume arguments: %v", args)
	}
	flags := strings.Join(args[:index], "\n")
	if capturedArg(flags, "--sandbox") != "read-only" || capturedArg(flags, "--color") != "never" {
		t.Fatal("resume lost exec's sandbox or color policy")
	}
	request.SessionID = "--last"
	if _, _, err := prepareCodex(request, t.TempDir(), newTestGate(t, nil, discardObserve), true, nil); err == nil {
		t.Fatal("resume accepted a selector in place of the recorded thread ID")
	}
}

func TestPiManagedPrintResumesWithAnExistingConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("PI_HOME", t.TempDir())
	writePiFixture(t, dir, "settings.json", piProbeSettings)
	request := model.Request{Executor: "pi", Model: "default", ExecutionMode: "full_access", SessionStorageRoot: t.TempDir(), SessionMode: "fresh"}
	// Older releases left their per-step settings in the stable session cwd.
	if err := os.MkdirAll(filepath.Join(request.SessionStorageRoot, ".cocogem", "pi"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"fresh", "resume"} {
		request.SessionMode = mode
		var observedID, observedMode string
		err := piConnectionProvider().Run(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) {
			return "{}", nil
		}, func(kind string, data map[string]any) error {
			if kind == "native_session" {
				observedMode, _ = data["mode"].(string)
				observedID, _ = data["session_id"].(string)
			}
			return nil
		})
		if err != nil || observedMode != mode || observedID != piSession {
			t.Fatalf("%s: ack=%q id=%q err=%v", mode, observedMode, observedID, err)
		}
		request.SessionID = observedID
	}
}
