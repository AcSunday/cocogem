//go:build darwin

package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// geminiFixtureServer answers a Gemini-shaped stream, asking for one shell tool
// call on the first turn and a final text on the second.
func geminiFixtureServer(t *testing.T, command string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "countTokens") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"totalTokens":1}`)
			return
		}
		var body struct {
			Tools []struct {
				Declarations []struct {
					Name string `json:"name"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(raw, &body)
		// The CLI's internal router calls the same endpoint without the task
		// tool menu; only a request that declares the shell tool may drive the
		// scripted tool call.
		hasShell := false
		for _, tool := range body.Tools {
			for _, declaration := range tool.Declarations {
				if declaration.Name == "run_shell_command" {
					hasShell = true
				}
			}
		}
		if calls != nil {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": PING\n\n")
		if debugNative() {
			var names []string
			for _, tool := range body.Tools {
				for _, declaration := range tool.Declarations {
					names = append(names, declaration.Name)
				}
			}
			fmt.Fprintf(os.Stderr, "native debug: fixture path=%s turn=%d tools=%v\n", r.URL.Path, turns.Load(), names)
		}
		if turns.Add(1) == 1 && hasShell {
			payload, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{
				"content": map[string]any{"role": "model", "parts": []any{map[string]any{
					"functionCall": map[string]any{"name": "run_shell_command", "args": map[string]any{"command": command}},
				}}},
				"finishReason": "STOP", "index": 0,
			}}, "usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 4, "totalTokenCount": 7}})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			return
		}
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"FIXTURE_DONE\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4,\"totalTokenCount\":7}}\n\n")
	}))
	t.Cleanup(server.Close)
	return server
}

// installedGeminiRequest prepares a project_autonomous Gemini request pointing at
// a loopback fake model through the provider's injected launcher.
func installedGeminiRequest(t *testing.T, root, endpoint string, decide func(model.NativePermissionRequest) (model.NativePermissionDecision, error)) (model.Request, Provider) {
	t.Helper()
	request := testRequest()
	request.Executor = "gemini"
	request.Model = "gemini-2.5-pro"
	request.ExecutionMode = "project_autonomous"
	request.WorkspaceRoot = root
	request.Tools = nil
	request.RequiredTools = nil
	request.FirstResponseTimeout = 90 * time.Second
	request.StallTimeout = 90 * time.Second
	request.AuthorizeNative = func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return decide(request)
	}
	for key, value := range map[string]string{
		"GEMINI_API_KEY": "cocogem-installed-dummy", "GOOGLE_API_KEY": "cocogem-installed-dummy",
		"GOOGLE_GEMINI_BASE_URL": endpoint, "GEMINI_CLI_AUTH_OVERRIDE": "gemini-api-key",
		"GOOGLE_GENAI_USE_VERTEXAI": "false", "GOOGLE_GENAI_USE_GCA": "false",
	} {
		t.Setenv(key, value)
	}
	provider := Provider{command: func(id string, args ...string) *exec.Cmd {
		cmd := exec.Command(id, args...)
		cmd.Env = os.Environ()
		return cmd
	}}
	return request, provider
}

func requireInstalledGemini(t *testing.T) {
	t.Helper()
	if os.Getenv("COCOGEM_CHECK_INSTALLED_GEMINI") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_GEMINI=1 for the installed Gemini gate check")
	}
	if _, err := exec.LookPath("gemini"); err != nil {
		t.Skip("gemini CLI is not installed")
	}
	if err := sandboxExecutable(); err != nil {
		t.Skip("macOS seatbelt unavailable")
	}
}

func TestInstalledGeminiGateUsesUserConnectionAndTask(t *testing.T) {
	requireInstalledGemini(t)
	for _, key := range geminiConnectionEnv {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GEMINI_CLI_AUTH_OVERRIDE", "")
	if err := os.Unsetenv("GEMINI_CLI_AUTH_OVERRIDE"); err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		select {
		case requests <- string(raw):
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"FIXTURE_DONE\"}]},\"finishReason\":\"STOP\",\"index\":0}]}\n\n")
	}))
	defer server.Close()
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	dir := filepath.Join(home, ".gemini")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=synthetic-key\nGOOGLE_GEMINI_BASE_URL="+server.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		Executor: "gemini", Model: "gemini-3.8-flash", ExecutionMode: "project_autonomous", WorkspaceRoot: t.TempDir(),
		System: "SYNTHETIC_SYSTEM_INSTRUCTION", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "SYNTHETIC_USER_TASK"}}}},
		FirstResponseTimeout: 30 * time.Second, StallTimeout: 30 * time.Second, StallAction: "stop",
		AuthorizeNative: func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			return model.NativePermissionDecision{Scope: model.NativeScopeDeny}, nil
		},
	}
	p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
	info, err := p.InspectExecutionConnection(t.Context(), "gemini", request.Model, "")
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedConnection = model.Connection{Identity: info.Identity, ResolvedModel: info.ResolvedModel}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=changed-key\nGOOGLE_GEMINI_BASE_URL=http://127.0.0.1:1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_API_KEY", "late-key")
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	if err := p.RunGeminiNative(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(string, map[string]any) error { return nil }); err != nil {
		t.Fatalf("gated Gemini did not complete with its existing connection: %v", err)
	}
	select {
	case body := <-requests:
		if !strings.Contains(body, "SYNTHETIC_SYSTEM_INSTRUCTION") || !strings.Contains(body, "SYNTHETIC_USER_TASK") {
			t.Fatal("the model request did not include the actual system instruction and user task")
		}
	default:
		t.Fatal("the configured endpoint was not called")
	}
}

// Deny: the real CLI asks the host, the host denies, and no effect appears.
func TestInstalledGeminiGateDeny(t *testing.T) {
	requireInstalledGemini(t)
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "denied-marker.txt")
	server := geminiFixtureServer(t, "printf EFFECT > "+marker, nil)
	var callbacks atomic.Int32
	request, provider := installedGeminiRequest(t, root, server.URL, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks.Add(1)
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "synthetic denial"}, nil
	})
	lifecycle := &piLifecycle{status: map[string]string{}, output: map[string]string{}}
	var texts []string
	err := provider.RunGeminiNative(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", fmt.Errorf("host tools must not be used")
	}, func(kind string, data map[string]any) error {
		lifecycle.observe(kind, data)
		if kind == "text" {
			if text, ok := data["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		if debugNative() {
			fmt.Fprintf(os.Stderr, "native debug: event kind=%s data=%v\n", kind, data)
		}
		return nil
	})
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the denied native call produced an effect (run err=%v)", err)
	}
	if callbacks.Load() == 0 {
		t.Fatalf("the host gate was never consulted (run err=%v texts=%v)", err, texts)
	}
	// The denial must settle the exact approval the host answered, and the
	// started/finished events must carry that same identity.
	if len(lifecycle.awaiting) != 1 || len(lifecycle.denied) != 1 || lifecycle.awaiting[0] != lifecycle.denied[0] {
		t.Fatalf("deny lifecycle: awaiting=%v resolved-denied=%v", lifecycle.awaiting, lifecycle.denied)
	}
	for _, id := range append(append([]string{}, lifecycle.started...), lifecycle.finished...) {
		if id != lifecycle.awaiting[0] {
			t.Fatalf("event id %q does not match the approval %q", id, lifecycle.awaiting[0])
		}
	}
	if len(lifecycle.finished) != 1 || lifecycle.status[lifecycle.finished[0]] != "failed" {
		t.Fatalf("a denied call must finish as failed exactly once: %v %v", lifecycle.finished, lifecycle.status)
	}
}

// Allow once: the real CLI must execute the command exactly once, and the
// rewrite must keep it inside the per-call sandbox.
func TestInstalledGeminiGateAllowOnce(t *testing.T) {
	requireInstalledGemini(t)
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "allowed-marker.txt")
	outside := filepath.Join(area, "outside-marker.txt")
	server := geminiFixtureServer(t, "printf ALLOWED > "+marker, nil)
	var callbacks atomic.Int32
	request, provider := installedGeminiRequest(t, root, server.URL, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		callbacks.Add(1)
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	lifecycle := &piLifecycle{status: map[string]string{}, output: map[string]string{}}
	err := provider.RunGeminiNative(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", fmt.Errorf("host tools must not be used")
	}, func(kind string, data map[string]any) error {
		lifecycle.observe(kind, data)
		return nil
	})
	if err != nil {
		t.Fatalf("the approved inside-project step failed: %v", err)
	}
	data, statErr := os.ReadFile(marker)
	if statErr != nil || string(data) != "ALLOWED" {
		t.Fatalf("inside-project allowed effect=%q err=%v", data, statErr)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("host callbacks=%d, want exactly 1 for one native call", callbacks.Load())
	}
	lifecycle.assertOneLifecycle(t, "succeeded")
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatal("the sandboxed command wrote outside the project")
	}
}

func TestInstalledGeminiGateRestoresNodeOptionsForNativeCommand(t *testing.T) {
	requireInstalledGemini(t)
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "node-options.txt")
	server := geminiFixtureServer(t, "node -p 'process.env.NODE_OPTIONS || \"unset\"' > "+marker, nil)
	request, provider := installedGeminiRequest(t, root, server.URL, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
	})
	command := provider.command
	provider.command = func(id string, args ...string) *exec.Cmd {
		cmd := command(id, args...)
		cmd.Env = overrideEnv(cmd.Env, map[string]string{"NODE_OPTIONS": "--no-warnings"})
		return cmd
	}
	err := provider.RunGeminiNative(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", fmt.Errorf("host tools must not be used")
	}, discardObserve)
	data, readErr := os.ReadFile(marker)
	if err != nil || readErr != nil || strings.TrimSpace(string(data)) != "--no-warnings" {
		t.Fatalf("native Node command: run=%v read=%v options=%q", err, readErr, data)
	}
}

// An approval for the exact outside resource must let the effect continue in the
// same call, with no second request and no replay.
func TestInstalledGeminiGateOutsideContinuesInSameCall(t *testing.T) {
	requireInstalledGemini(t)
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	approved := filepath.Join(area, "approved.txt")
	server := geminiFixtureServer(t, "printf APPROVED > "+approved, nil)
	var calls atomic.Int32
	var requested []string
	request, provider := installedGeminiRequest(t, root, server.URL, func(request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		calls.Add(1)
		requested = append(requested, request.Resource)
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: request.Resource}, nil
	})
	err := provider.RunGeminiNative(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", fmt.Errorf("host tools must not be used")
	}, func(string, map[string]any) error { return nil })
	if calls.Load() != 1 {
		t.Fatalf("host callbacks=%d, want exactly 1 for the single outside effect", calls.Load())
	}
	if len(requested) != 1 || !strings.HasSuffix(requested[0], "approved.txt") {
		t.Fatalf("the host was asked for %v, want the exact outside file", requested)
	}
	if data, statErr := os.ReadFile(approved); statErr != nil || string(data) != "APPROVED" {
		t.Fatalf("the approved outside effect did not continue in the same call: %q err=%v run_err=%v", data, statErr, err)
	}
}

// An approval for one outside path must not authorize a command that also
// touches another outside path.
func TestInstalledGeminiGateUnapprovedOutsideTargetIsRefused(t *testing.T) {
	requireInstalledGemini(t)
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	approved := filepath.Join(area, "approved.txt")
	other := filepath.Join(area, "other.txt")
	// Pre-existing marker: a refused command must leave its exact bytes intact.
	// (An assertion that this file is absent would be self-contradictory.)
	if err := os.WriteFile(other, []byte("OTHER"), 0600); err != nil {
		t.Fatal(err)
	}
	server := geminiFixtureServer(t, "printf ESCAPE > "+approved+"; printf ESCAPE > "+other, nil)
	request, provider := installedGeminiRequest(t, root, server.URL, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		// Deliberately approve a path the command does not touch.
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: filepath.Join(area, "unrelated.txt")}, nil
	})
	_ = provider.RunGeminiNative(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", fmt.Errorf("host tools must not be used")
	}, func(string, map[string]any) error { return nil })
	if data, err := os.ReadFile(other); err != nil || string(data) != "OTHER" {
		t.Fatalf("a mismatched approval executed the unapproved outside target: %q err=%v", data, err)
	}
	if _, err := os.Stat(approved); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a mismatched approval still executed the other outside target")
	}
}

// A host callback that errors must not become an allow.
func TestInstalledGeminiGateCallbackErrorDenies(t *testing.T) {
	requireInstalledGemini(t)
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "error-marker.txt")
	server := geminiFixtureServer(t, "printf EFFECT > "+marker, nil)
	request, provider := installedGeminiRequest(t, root, server.URL, func(model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		return model.NativePermissionDecision{}, fmt.Errorf("host ledger unavailable")
	})
	_ = provider.RunGeminiNative(t.Context(), request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", fmt.Errorf("host tools must not be used")
	}, func(string, map[string]any) error { return nil })
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a failing host callback allowed the effect")
	}
}
