package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

func codexApprovalFixture(t *testing.T, command string, escalated bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model  string            `json:"model"`
			Input  []json.RawMessage `json:"input"`
			Tools  []json.RawMessage `json:"tools"`
			Stream bool              `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		hasOutput := false
		for _, raw := range input.Input {
			if strings.Contains(string(raw), "function_call_output") {
				hasOutput = true
				t.Logf("OUTPUT %s", raw)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(value map[string]any) {
			raw, _ := json.Marshal(value)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		if turns.Add(1) == 1 && !hasOutput {
			payload := map[string]any{"cmd": command, "sandbox_permissions": "use_default"}
			if escalated {
				payload["sandbox_permissions"] = "require_escalated"
				payload["justification"] = "fixture approval"
			}
			args, _ := json.Marshal(payload)
			item := map[string]any{"id": "fc_1", "type": "function_call", "name": "exec_command", "call_id": "call_1", "arguments": string(args)}
			send(map[string]any{"type": "response.output_item.done", "item": item})
			send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}}})
			return
		}
		item := map[string]any{"id": "msg_done", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "fixture done", "annotations": []any{}}}}
		send(map[string]any{"type": "response.output_item.done", "item": item})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_done", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}}})
	}))
	return server, &turns
}

func TestInstalledCodexAppServerApproval(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a provider-free Codex app-server check")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is not installed")
	}
	for _, tc := range []struct {
		name  string
		allow bool
	}{
		{name: "allowed", allow: true},
		{name: "denied", allow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			area := t.TempDir()
			root := filepath.Join(area, "workspace")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(area, "marker.txt")
			server, _ := codexApprovalFixture(t, "printf APPROVED > "+shellQuote(marker), true)
			defer server.Close()
			t.Setenv("CODEX_HOME", t.TempDir())
			var callback atomic.Int32
			var capabilityNotice atomic.Int32
			request := model.Request{
				Executor: "codex", Model: "cocogem-fixture", ExecutionMode: "project_autonomous",
				System: "fixture", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "run one command"}}}},
				NativeTools: []string{nativeCodexCommand, nativeBash}, WorkspaceRoot: root, FirstResponseTimeout: 40 * time.Second, StallTimeout: 40 * time.Second, StallAction: "stop",
				AuthorizeNative: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					callback.Add(1)
					if !strings.Contains(string(request.Input), "APPROVED") {
						return model.NativePermissionDecision{}, fmt.Errorf("unexpected approval input %s", request.Input)
					}
					if tc.allow {
						return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: marker}, nil
					}
					return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "fixture denied"}, nil
				},
			}
			options := runOptions{skipCheck: true, adapter: agent.AdapterInfo{ID: "codex", Version: "0.166.0", Supported: true}, codexArgs: []string{
				"-c", `model_provider="cocogem_local"`,
				"-c", `model_providers.cocogem_local.name="Local"`,
				"-c", `model_providers.cocogem_local.base_url="` + server.URL + `/v1"`,
				"-c", `model_providers.cocogem_local.wire_api="responses"`,
				"-c", `model_providers.cocogem_local.requires_openai_auth=false`,
				"-c", `model_providers.cocogem_local.supports_websockets=false`,
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
			err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(kind string, data map[string]any) error {
				if kind == "native_notice" && data["kind"] == "codex_external_scope_unavailable" {
					capabilityNotice.Add(1)
				}
				return nil
			}, options)
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			// The installed approval decision has no exact outside scope. The
			// adapter declines before consuming a host approval, so neither an
			// allow nor a deny decision reaches the host and no effect occurs.
			if callback.Load() != 0 {
				t.Fatalf("explicit outside command consumed %d host approvals", callback.Load())
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("Codex command produced an outside effect without an exact scope")
			}
			if capabilityNotice.Load() == 0 {
				t.Fatal("Codex did not report the exact outside-scope capability limit")
			}
		})
	}
}

func TestInstalledCodexAppServerInProjectApproval(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a provider-free Codex app-server check")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is not installed")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "inside.txt")
	server, _ := codexApprovalFixture(t, "printf INSIDE > "+shellQuote(marker), true)
	defer server.Close()
	t.Setenv("CODEX_HOME", t.TempDir())
	var callback atomic.Int32
	request := model.Request{
		Executor: "codex", Model: "cocogem-fixture", ExecutionMode: "project_autonomous",
		System: "fixture", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "run one command"}}}},
		NativeTools: []string{nativeCodexCommand, nativeBash}, WorkspaceRoot: root,
		FirstResponseTimeout: 40 * time.Second, StallTimeout: 40 * time.Second, StallAction: "stop",
		AuthorizeNative: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			callback.Add(1)
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	}
	options := runOptions{skipCheck: true, adapter: agent.AdapterInfo{ID: "codex", Version: "0.166.0", Supported: true}, codexArgs: []string{
		"-c", `model_provider="cocogem_local"`,
		"-c", `model_providers.cocogem_local.name="Local"`,
		"-c", `model_providers.cocogem_local.base_url="` + server.URL + `/v1"`,
		"-c", `model_providers.cocogem_local.wire_api="responses"`,
		"-c", `model_providers.cocogem_local.requires_openai_auth=false`,
		"-c", `model_providers.cocogem_local.supports_websockets=false`,
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
	if err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(string, map[string]any) error { return nil }, options); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if callback.Load() == 0 {
		t.Fatal("an in-project Codex command bypassed the host callback")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "INSIDE" {
		t.Fatalf("in-project command = %q, %v", data, err)
	}
}

func TestInstalledCodexAppServerFullAccessRevalidates(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a provider-free Codex app-server check")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is not installed")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "full.txt")
	server, _ := codexApprovalFixture(t, "printf FULL > "+shellQuote(marker), false)
	defer server.Close()
	t.Setenv("CODEX_HOME", t.TempDir())
	var callback atomic.Int32
	request := model.Request{
		Executor: "codex", Model: "cocogem-fixture", ExecutionMode: "full_access", TrustedExecution: true,
		System: "fixture", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "run one command"}}}},
		NativeTools: []string{nativeBash, nativeWrite}, WorkspaceRoot: root,
		FirstResponseTimeout: 40 * time.Second, StallTimeout: 40 * time.Second, StallAction: "stop",
		AuthorizeNative: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			callback.Add(1)
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	}
	options := runOptions{skipCheck: true, adapter: agent.AdapterInfo{ID: "codex", Version: "0.166.0", Supported: true}, codexArgs: []string{
		"-c", `model_provider="cocogem_local"`,
		"-c", `model_providers.cocogem_local.name="Local"`,
		"-c", `model_providers.cocogem_local.base_url="` + server.URL + `/v1"`,
		"-c", `model_providers.cocogem_local.wire_api="responses"`,
		"-c", `model_providers.cocogem_local.requires_openai_auth=false`,
		"-c", `model_providers.cocogem_local.supports_websockets=false`,
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
	if err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(string, map[string]any) error { return nil }, options); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if callback.Load() == 0 {
		t.Fatal("full-access Codex bypassed the host callback")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "FULL" {
		t.Fatalf("full-access command = %q, %v", data, err)
	}
}

// The project_autonomous Codex profile denies reads outside the workspace even
// when the command only names a project-local script. The host cannot see the
// hidden path, so this must fail closed rather than auto-admit project cwd.
func TestInstalledCodexPermissionProfileDeniesExternalRead(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a provider-free Codex app-server check")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is not installed")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE_SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	// A project-level config must not replace the explicit thread profile.
	projectConfig := filepath.Join(root, ".codex")
	if err := os.MkdirAll(projectConfig, 0700); err != nil {
		t.Fatal(err)
	}
	projectRules := "default_permissions = \"project-broad\"\n\n[permissions.project-broad.filesystem]\n\":root\" = \"read\"\n\n[permissions.cocogem-project.filesystem]\n\":root\" = \"read\"\n" + strconv.Quote(outside) + " = \"read\"\n"
	if err := os.WriteFile(filepath.Join(projectConfig, "config.toml"), []byte(projectRules), 0600); err != nil {
		t.Fatal(err)
	}
	leak := filepath.Join(root, "leak.txt")
	counter := filepath.Join(root, "counter.txt")
	script := filepath.Join(root, "read.sh")
	body := "echo once >> " + shellQuote(counter) + "\n/bin/cat " + shellQuote(outside) + " > " + shellQuote(leak) + "\n"
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	server, _ := codexApprovalFixture(t, "/bin/sh read.sh", true)
	defer server.Close()
	t.Setenv("CODEX_HOME", t.TempDir())
	var callbacks atomic.Int32
	request := model.Request{
		Executor: "codex", Model: "cocogem-fixture", ExecutionMode: "project_autonomous",
		System: "fixture", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "run the project script"}}}},
		NativeTools: []string{nativeCodexCommand, nativeBash}, WorkspaceRoot: root,
		FirstResponseTimeout: 40 * time.Second, StallTimeout: 40 * time.Second, StallAction: "stop",
		AuthorizeNative: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			callbacks.Add(1)
			if request.Name != "Bash" || !strings.Contains(string(request.Input), "read.sh") {
				return model.NativePermissionDecision{}, fmt.Errorf("unexpected request %q %s", request.Name, request.Input)
			}
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	}
	options := runOptions{skipCheck: true, adapter: agent.AdapterInfo{ID: "codex", Version: "0.166.0", Supported: true}, codexArgs: []string{
		"-c", `model_provider="cocogem_local"`,
		"-c", `model_providers.cocogem_local.name="Local"`,
		"-c", `model_providers.cocogem_local.base_url="` + server.URL + `/v1"`,
		"-c", `model_providers.cocogem_local.wire_api="responses"`,
		"-c", `model_providers.cocogem_local.requires_openai_auth=false`,
		"-c", `model_providers.cocogem_local.supports_websockets=false`,
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
	if err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(string, map[string]any) error { return nil }, options); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("host approvals = %d, want exactly one sandboxed attempt", callbacks.Load())
	}
	if data, err := os.ReadFile(counter); err != nil || string(data) != "once\n" {
		t.Fatalf("command executions = %q, %v; want exactly one (no implicit retry)", data, err)
	}
	if data, err := os.ReadFile(leak); err == nil && len(data) != 0 {
		t.Fatalf("the permission profile leaked outside bytes into the project: %q", data)
	}
}

// A project file hardlinked to an outside inode must not become a write path
// through the named profile. The exact linked path is denied; the outside
// original and the inside alias stay unchanged.
func TestInstalledCodexHardlinkDenied(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a provider-free Codex app-server check")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is not installed")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.txt")
	if err := os.Link(outside, link); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	leak := filepath.Join(root, "leak.txt")
	script := filepath.Join(root, "link.sh")
	body := "/bin/cat " + shellQuote(link) + " > " + shellQuote(leak) + "\nprintf CHANGED > " + shellQuote(link) + "\n"
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	server, _ := codexApprovalFixture(t, "/bin/sh link.sh", false)
	defer server.Close()
	t.Setenv("CODEX_HOME", t.TempDir())
	var callbacks atomic.Int32
	var isolated atomic.Int32
	request := model.Request{
		Executor: "codex", Model: "cocogem-fixture", ExecutionMode: "project_autonomous",
		System: "fixture", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "run the linked script"}}}},
		NativeTools: []string{nativeCodexCommand, nativeBash}, WorkspaceRoot: root,
		FirstResponseTimeout: 40 * time.Second, StallTimeout: 40 * time.Second, StallAction: "stop",
		AuthorizeNative: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			callbacks.Add(1)
			if request.Name != "Bash" || !strings.Contains(string(request.Input), "link.sh") {
				return model.NativePermissionDecision{}, fmt.Errorf("unexpected request %q %s", request.Name, request.Input)
			}
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	}
	options := runOptions{skipCheck: true, adapter: agent.AdapterInfo{ID: "codex", Version: "0.166.0", Supported: true}, codexArgs: []string{
		"-c", `model_provider="cocogem_local"`,
		"-c", `model_providers.cocogem_local.name="Local"`,
		"-c", `model_providers.cocogem_local.base_url="` + server.URL + `/v1"`,
		"-c", `model_providers.cocogem_local.wire_api="responses"`,
		"-c", `model_providers.cocogem_local.requires_openai_auth=false`,
		"-c", `model_providers.cocogem_local.supports_websockets=false`,
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
	if err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(kind string, data map[string]any) error {
		if kind == "native_notice" && data["kind"] == "native_hardlink_isolated" {
			isolated.Add(1)
		}
		return nil
	}, options); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if callbacks.Load() == 0 || isolated.Load() == 0 {
		t.Fatalf("hardlink approval/notice missing: callbacks=%d isolated=%d", callbacks.Load(), isolated.Load())
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "ORIGINAL" {
		t.Fatalf("outside original changed through a Codex hardlink: %q, %v", data, err)
	}
	if data, err := os.ReadFile(link); err != nil || string(data) != "ORIGINAL" {
		t.Fatalf("inside hardlink alias changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(leak); err == nil && len(data) != 0 {
		t.Fatalf("hardlinked outside bytes leaked into the project: %q", data)
	}
}

// The app-server path must preserve the resolved model, explicit thinking
// effort and connection-only credentials while keeping those credentials out
// of native command environments.
func TestInstalledCodexAppServerForwardsModelThinkingAndBoundary(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a provider-free Codex app-server check")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is not installed")
	}
	const secret = "fixture-header-secret"
	var turns atomic.Int32
	var seenModel, seenEffort, seenHeader, seenTools atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model     string            `json:"model"`
			Input     []json.RawMessage `json:"input"`
			Tools     []json.RawMessage `json:"tools"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		seenModel.Store(input.Model)
		seenEffort.Store(input.Reasoning.Effort)
		seenTools.Store(string(codexToolsJSON(input.Tools)))
		if r.Header.Get("X-Fixture") == secret {
			seenHeader.Store(true)
		}
		hasOutput := false
		for _, raw := range input.Input {
			if strings.Contains(string(raw), "function_call_output") {
				hasOutput = true
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(value map[string]any) {
			raw, _ := json.Marshal(value)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		if turns.Add(1) == 1 && !hasOutput {
			args, _ := json.Marshal(map[string]any{"cmd": "/usr/bin/env > env.txt", "sandbox_permissions": "use_default"})
			item := map[string]any{"id": "fc_1", "type": "function_call", "name": "exec_command", "call_id": "call_1", "arguments": string(args)}
			send(map[string]any{"type": "response.output_item.done", "item": item})
			send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}}})
			return
		}
		item := map[string]any{"id": "msg_done", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "fixture done", "annotations": []any{}}}}
		send(map[string]any{"type": "response.output_item.done", "item": item})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_done", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}}})
	}))
	defer server.Close()
	root := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`model = "cocogem-fixture"
model_provider = "cocogem_local"

[model_providers.cocogem_local]
name = "Local"
base_url = "`+server.URL+`/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false

[model_providers.cocogem_local.http_headers]
"X-Fixture" = "fixture-header-secret"
`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	_, connectionArgs, connectionEnv, err := codexConnectionConfig("cocogem-fixture")
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		Executor: "codex", Model: "cocogem-fixture", ThinkingLevel: "high", ExecutionMode: "project_autonomous",
		System: "fixture", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "run env"}}}},
		NativeTools: []string{nativeCodexCommand, nativeBash, nativeCodexImage}, WorkspaceRoot: root,
		FirstResponseTimeout: 40 * time.Second, StallTimeout: 40 * time.Second, StallAction: "stop",
		AuthorizeNative: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	}
	options := runOptions{skipCheck: true, adapter: agent.AdapterInfo{ID: "codex", Version: "0.166.0", Supported: true}, codexArgs: connectionArgs, codexEnv: connectionEnv}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
	if err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(string, map[string]any) error { return nil }, options); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if got, _ := seenModel.Load().(string); got != "cocogem-fixture" {
		t.Fatalf("forwarded model = %q, want cocogem-fixture", got)
	}
	if got, _ := seenEffort.Load().(string); got != "high" {
		t.Fatalf("forwarded reasoning effort = %q, want high", got)
	}
	if ok, _ := seenHeader.Load().(bool); !ok {
		t.Fatal("connection-only credential header did not reach the app-server model client")
	}
	if tools, _ := seenTools.Load().(string); strings.Contains(tools, "view_image") {
		t.Fatal("native view_image remained enabled without per-call host admission")
	} else if strings.Contains(tools, "multi_agent_v1") || strings.Contains(tools, "spawn_agent") || strings.Contains(tools, "get_goal") || strings.Contains(tools, "create_goal") || strings.Contains(tools, "update_goal") {
		t.Fatal("native subagent/goal tools remained enabled in the app-server registry")
	} else if !strings.Contains(tools, `"name":"exec_command"`) {
		t.Fatal("the intended native shell tool is missing from the app-server registry")
	}
	env, err := os.ReadFile(filepath.Join(root, "env.txt"))
	if err != nil {
		t.Fatalf("native command environment was not captured: %v", err)
	}
	if strings.Contains(string(env), secret) {
		t.Fatal("connection-only credential leaked into the native command environment")
	}
	if !strings.Contains(string(env), "PATH=") {
		t.Fatal("native command did not receive the ordinary inherited environment")
	}
}

func codexToolsJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func TestCodexChangePaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want []string
		ok   bool
	}{
		{name: "apply patch map", raw: `{"/b.txt":{"type":"update"},"/a.txt":{"type":"add"}}`, want: []string{"/a.txt", "/b.txt"}, ok: true},
		{name: "v2 change list", raw: `[{"path":"/a.txt","kind":"add"},{"path":"/b.txt","kind":"update"}]`, want: []string{"/a.txt", "/b.txt"}, ok: true},
		{name: "empty", raw: ``, ok: false},
		{name: "no paths", raw: `[{"kind":"add"}]`, ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, ok := codexChangePaths(json.RawMessage(tc.raw))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if len(paths) != len(tc.want) {
				t.Fatalf("paths = %v, want %v", paths, tc.want)
			}
			for i := range paths {
				if paths[i] != tc.want[i] {
					t.Fatalf("paths = %v, want %v", paths, tc.want)
				}
			}
		})
	}
}

// Strict configuration must fail bounded instead of falling back to broad
// defaults when the app-server receives an unknown scoped config key.
func TestInstalledCodexAppServerStrictConfigFailsBounded(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a provider-free Codex app-server check")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI is not installed")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "unexpected.txt")
	server, _ := codexApprovalFixture(t, "printf UNEXPECTED > "+shellQuote(marker), false)
	defer server.Close()
	t.Setenv("CODEX_HOME", t.TempDir())
	var callbacks atomic.Int32
	request := model.Request{
		Executor: "codex", Model: "cocogem-fixture", ExecutionMode: "project_autonomous",
		System: "fixture", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "should not run"}}}},
		NativeTools: []string{nativeCodexCommand, nativeBash}, WorkspaceRoot: root,
		FirstResponseTimeout: 20 * time.Second, StallTimeout: 20 * time.Second, StallAction: "stop",
		AuthorizeNative: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			callbacks.Add(1)
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	}
	options := runOptions{skipCheck: true, adapter: agent.AdapterInfo{ID: "codex", Version: "0.166.0", Supported: true}, codexArgs: []string{
		"-c", `model_provider="cocogem_local"`,
		"-c", `model_providers.cocogem_local.name="Local"`,
		"-c", `model_providers.cocogem_local.base_url="` + server.URL + `/v1"`,
		"-c", `model_providers.cocogem_local.wire_api="responses"`,
		"-c", `model_providers.cocogem_local.requires_openai_auth=false`,
		"-c", `model_providers.cocogem_local.supports_websockets=false`,
		"-c", `cocogem_unknown_fixture=true`,
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
	err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(string, map[string]any) error { return nil }, options)
	if err == nil {
		t.Fatal("app-server accepted an unknown strict config key")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("malformed config failed after %s instead of a bounded startup failure", elapsed)
	}
	if callbacks.Load() != 0 {
		t.Fatal("malformed config reached a native tool approval")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("malformed config executed a native tool")
	}
}
