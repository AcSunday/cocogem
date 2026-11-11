//go:build darwin

package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

// piGateServer is a loopback OpenAI-compatible fixture. The first assistant
// message asks for one shell tool call with command; the second answers with
// text. No provider credential and no external network are involved.
func piGateServer(t *testing.T, command string) *httptest.Server {
	t.Helper()
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "chat/completions") {
			http.Error(w, "fixture", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if turns.Add(1) == 1 {
			arguments, _ := json.Marshal(map[string]string{"command": command})
			installedPiChunk(w, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": "call_gate", "type": "function",
				"function": map[string]any{"name": "bash", "arguments": string(arguments)},
			}}}, "tool_calls")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		installedPiChunk(w, map[string]any{"role": "assistant", "content": "GATE_DONE"}, nil)
		installedPiChunk(w, map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server
}

// piGateRequest prepares a project_autonomous Pi request. The caller's launcher
// is reused so the fixture endpoint reaches the child exactly as in production
// probes, while the agent directory stays inside a temp dir. The connection is
// resolved first (as the workflow does) so the gated launch uses the frozen
// snapshot rather than re-reading user config mid-attempt.
func piGateRequest(t *testing.T, root, endpoint string, decide func(model.NativePermissionRequest) (model.NativePermissionDecision, error)) (model.Request, Provider) {
	t.Helper()
	if os.Getenv("COCOGEM_CHECK_INSTALLED_PI") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_PI=1 for provider-free Pi checks")
	}
	if _, err := exec.LookPath("pi"); err != nil {
		t.Skip("pi CLI is not installed")
	}
	if err := sandboxExecutable(); err != nil {
		t.Skip("macOS seatbelt unavailable")
	}
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("PI_TELEMETRY", "0")
	models, err := json.Marshal(map[string]any{"providers": map[string]any{"fixture": map[string]any{
		"name": "Fixture", "baseUrl": endpoint + "/v1", "api": "openai-completions", "apiKey": "synthetic-dummy-key",
		"models": []any{map[string]any{"id": "fixture-model", "name": "Fixture", "reasoning": false,
			"input": []string{"text"}, "contextWindow": 128000, "maxTokens": 8192}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	writePiFixture(t, dir, "models.json", string(models))
	writePiFixture(t, dir, "settings.json", `{"defaultProvider":"fixture","defaultModel":"fixture-model","defaultThinkingLevel":"off","retry":{"enabled":false}}`)
	request := testRequest()
	request.Executor = "pi"
	request.Model = "default"
	request.Tools = nil
	request.RequiredTools = nil
	request.ExecutionMode = "project_autonomous"
	request.WorkspaceRoot = root
	request.FirstResponseTimeout = 90 * time.Second
	request.StallTimeout = 90 * time.Second
	request.AuthorizeNative = func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return decide(request)
	}
	provider := Provider{command: func(id string, args ...string) *exec.Cmd {
		cmd := exec.Command(id, args...)
		cmd.Env = overrideEnv(os.Environ(), map[string]string{
			"PI_CODING_AGENT_DIR": dir, "PI_OFFLINE": "1", "PI_TELEMETRY": "0",
		})
		return cmd
	}}
	info, err := provider.InspectExecutionConnection(t.Context(), "pi", "default", "")
	if err != nil {
		t.Fatalf("resolve the Pi fixture connection: %v", err)
	}
	request.ExpectedConnection = model.Connection{Identity: info.Identity, ResolvedModel: info.ResolvedModel}
	return request, provider
}

// piLifecycle is the observable native tool lifecycle of one attempt.
type piLifecycle struct {
	started  []string
	finished []string
	status   map[string]string
	output   map[string]string
	awaiting []string
	resolved []string
	denied   []string
}

func (l *piLifecycle) observe(kind string, data map[string]any) {
	id, _ := data["tool_call_id"].(string)
	switch kind {
	case "tool_started":
		l.started = append(l.started, id)
	case "tool_finished":
		l.finished = append(l.finished, id)
		status, _ := data["status"].(string)
		l.status[id] = status
		output, _ := data["output"].(string)
		l.output[id] = output
	case "native_permission":
		if data["status"] == "awaiting_permission" {
			l.awaiting = append(l.awaiting, id)
		}
	case "native_permission_resolved":
		l.resolved = append(l.resolved, id)
		if allowed, _ := data["allowed"].(bool); !allowed {
			l.denied = append(l.denied, id)
		}
	}
}

func runPiGate(t *testing.T, request model.Request, provider Provider) (*piLifecycle, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	lifecycle := &piLifecycle{status: map[string]string{}, output: map[string]string{}}
	// Provider.Run is the production entry point; main native dispatches the
	// gated path there, so the installed check exercises real wiring.
	err := provider.Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", errors.New("host MCP tools must not be used by this fixture")
	}, func(kind string, data map[string]any) error {
		lifecycle.observe(kind, data)
		if debugNative() {
			fmt.Fprintf(os.Stderr, "native debug: pi event kind=%s data=%v\n", kind, data)
		}
		return nil
	})
	return lifecycle, err
}

func TestInstalledPiGateExposesAndExecutesHostTool(t *testing.T) {
	var turns, calls, starts, finishes atomic.Int32
	var foreignCalls atomic.Int32
	var exposed atomic.Bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignCalls.Add(1)
		http.Error(w, "unexpected project server", http.StatusForbidden)
	}))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("invalid synthetic model request")
		}
		for _, tool := range body.Tools {
			if tool.Function.Name == "mcp__cocogem__probe" {
				exposed.Store(true)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if turns.Add(1) == 1 && exposed.Load() {
			installedPiChunk(w, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0, "id": "call_host", "type": "function",
				"function": map[string]any{"name": "mcp__cocogem__probe", "arguments": `{}`},
			}}}, "tool_calls")
		} else {
			installedPiChunk(w, map[string]any{"role": "assistant", "content": "HOST_DONE"}, nil)
			installedPiChunk(w, map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	root := t.TempDir()
	projectDir := filepath.Join(root, ".pi")
	if err := os.MkdirAll(filepath.Join(projectDir, "extensions"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "unexpected-extension")
	extension := "import fs from 'node:fs'; fs.writeFileSync(" + jsonString(marker) + ", 'loaded'); export default function() {}\n"
	if err := os.WriteFile(filepath.Join(projectDir, "extensions", "fixture.mjs"), []byte(extension), 0600); err != nil {
		t.Fatal(err)
	}
	projectMCP, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"foreign": map[string]any{"type": "http", "url": foreign.URL, "exposure": "direct"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "mcp.json"), projectMCP, 0600); err != nil {
		t.Fatal(err)
	}
	request, provider := piGateRequest(t, root, server.URL, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Scope: model.NativeScopeDeny}, nil
	})
	request.Tools = []model.Tool{{Name: "probe", Description: "Synthetic host tool", Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}
	request.RequiredTools = []string{"probe"}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	err = provider.Run(ctx, request, func(_ context.Context, name string, input json.RawMessage) (string, error) {
		if name != "probe" || string(input) != "{}" {
			return "", errors.New("unexpected synthetic host call")
		}
		calls.Add(1)
		return `{"ok":true}`, nil
	}, func(kind string, data map[string]any) error {
		if data["name"] == "probe" && kind == "tool_started" {
			starts.Add(1)
		}
		if data["name"] == "probe" && kind == "tool_finished" {
			finishes.Add(1)
		}
		if data["name"] == "mcp__cocogem__probe" && (kind == "tool_started" || kind == "tool_finished") {
			return errors.New("Pi duplicated the host tool lifecycle")
		}
		return nil
	})
	if err != nil || !exposed.Load() || calls.Load() != 1 || starts.Load() != 1 || finishes.Load() != 1 {
		t.Fatalf("gated Pi host tool did not complete once: exposed=%v calls=%d starts=%d finishes=%d error=%v", exposed.Load(), calls.Load(), starts.Load(), finishes.Load(), err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) || foreignCalls.Load() != 0 {
		t.Fatalf("project extension or MCP server escaped isolation: marker=%v foreign calls=%d", err, foreignCalls.Load())
	}
}

// assertOneLifecycle proves the approval, the started event and the terminal
// event all carry one identity, and that the terminal state matches the
// decision. It never accepts a missing pair as success.
func (l *piLifecycle) assertOneLifecycle(t *testing.T, wantStatus string) {
	t.Helper()
	if len(l.awaiting) != 1 || len(l.started) != 1 {
		t.Fatalf("lifecycle: awaiting=%v started=%v, want exactly one each", l.awaiting, l.started)
	}
	if l.awaiting[0] != l.started[0] {
		t.Fatalf("approval id %q and start id %q differ", l.awaiting[0], l.started[0])
	}
	if len(l.resolved) != 1 || l.resolved[0] != l.awaiting[0] {
		t.Fatalf("resolved=%v does not settle approval %q", l.resolved, l.awaiting[0])
	}
	if len(l.finished) != 1 || l.finished[0] != l.started[0] {
		t.Fatalf("finished=%v does not pair with start %q", l.finished, l.started[0])
	}
	if got := l.status[l.finished[0]]; got != wantStatus {
		t.Fatalf("terminal status=%q, want %q", got, wantStatus)
	}
}

// Inside-project autonomy: the real Pi executes its own bash tool inside the
// project after the host gate allows it, once.
func TestInstalledPiGateInsideProjectAutonomous(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "inside-marker.txt")
	server := piGateServer(t, "printf INSIDE > "+marker)
	var callbacks atomic.Int32
	request, provider := piGateRequest(t, root, server.URL, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks.Add(1)
		if request.Action != "execute" {
			t.Errorf("classified action=%q for a bash call", request.Action)
		}
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	lifecycle, err := runPiGate(t, request, provider)
	if err != nil {
		t.Fatalf("the inside-project step failed: %v", err)
	}
	data, statErr := os.ReadFile(marker)
	if statErr != nil || string(data) != "INSIDE" {
		t.Fatalf("inside-project native effect=%q err=%v", data, statErr)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("host callbacks=%d, want exactly 1 for one native call", callbacks.Load())
	}
	lifecycle.assertOneLifecycle(t, "succeeded")
}

// A host denial must leave no effect: the real Pi blocks the tool before it runs.
func TestInstalledPiGateDenyHasNoEffect(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "denied-marker.txt")
	server := piGateServer(t, "printf DENIED > "+marker)
	var callbacks atomic.Int32
	request, provider := piGateRequest(t, root, server.URL, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks.Add(1)
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "synthetic denial"}, nil
	})
	lifecycle, _ := runPiGate(t, request, provider)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a denied Pi tool call produced an effect")
	}
	if callbacks.Load() != 1 {
		t.Fatalf("host callbacks=%d, want exactly 1", callbacks.Load())
	}
	if len(lifecycle.awaiting) != 1 || len(lifecycle.denied) != 1 || lifecycle.awaiting[0] != lifecycle.denied[0] {
		t.Fatalf("deny lifecycle: awaiting=%v denied=%v", lifecycle.awaiting, lifecycle.denied)
	}
}

// A one-time approval for the exact outside path continues in the same call:
// the command is rewritten into a per-call profile, so Pi's own tool runs it.
func TestInstalledPiGateApprovedOutsideContinuesInSameCall(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	approved := filepath.Join(area, "approved.txt")
	server := piGateServer(t, "printf APPROVED > "+approved)
	var callbacks atomic.Int32
	var requested []string
	request, provider := piGateRequest(t, root, server.URL, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks.Add(1)
		requested = append(requested, request.Resource)
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: request.Resource}, nil
	})
	lifecycle, err := runPiGate(t, request, provider)
	if err != nil {
		t.Fatalf("the approved outside step failed: %v", err)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("host callbacks=%d, want exactly 1", callbacks.Load())
	}
	lifecycle.assertOneLifecycle(t, "succeeded")
	if len(requested) != 1 || !strings.HasSuffix(requested[0], "approved.txt") {
		t.Fatalf("the host was asked for %v, want the exact outside file", requested)
	}
	if data, err := os.ReadFile(approved); err != nil || string(data) != "APPROVED" {
		t.Fatalf("the approved outside effect did not continue in the same call: %q err=%v", data, err)
	}
}

// A mismatched approval must not authorize a different outside path.
func TestInstalledPiGateUnapprovedOutsideTargetIsRefused(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	approved := filepath.Join(area, "approved.txt")
	other := filepath.Join(area, "other.txt")
	if err := os.WriteFile(other, []byte("OTHER"), 0600); err != nil {
		t.Fatal(err)
	}
	server := piGateServer(t, "printf ESCAPE > "+approved+"; printf ESCAPE > "+other)
	request, provider := piGateRequest(t, root, server.URL, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: filepath.Join(area, "unrelated.txt")}, nil
	})
	lifecycle, _ := runPiGate(t, request, provider)
	if len(lifecycle.awaiting) != 1 || len(lifecycle.denied) != 1 {
		t.Fatalf("the refused outside target did not reach the host: awaiting=%v denied=%v", lifecycle.awaiting, lifecycle.denied)
	}
	if _, err := os.Stat(approved); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a mismatched approval still executed the outside target")
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "OTHER" {
		t.Fatalf("a mismatched approval modified an unapproved outside file: %q err=%v", data, err)
	}
}

// A host callback error is a denial, never an allow, and must not produce effect.
func TestInstalledPiGateCallbackErrorDenies(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "error-marker.txt")
	server := piGateServer(t, "printf EFFECT > "+marker)
	request, provider := piGateRequest(t, root, server.URL, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{}, errors.New("host ledger unavailable")
	})
	lifecycle, _ := runPiGate(t, request, provider)
	if len(lifecycle.awaiting) != 1 || len(lifecycle.denied) != 1 {
		t.Fatalf("a host error did not reach the gate: awaiting=%v denied=%v", lifecycle.awaiting, lifecycle.denied)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failing host callback allowed the effect")
	}
}
