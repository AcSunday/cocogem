package nativecli

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

// claudeFakeModel emits one tool request and then a final answer. It never
// contacts a provider: the installed CLI talks to this loopback fixture.
func claudeFakeModel(t *testing.T, toolName string, toolInput any) (*httptest.Server, *atomic.Int32) {
	return claudeFakeModelSecret(t, toolName, toolInput, nil)
}

func claudeFakeModelSecret(t *testing.T, toolName string, toolInput any, secret *atomic.Bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if secret != nil && (strings.Contains(string(body), "KERNEL_SECRET_FIXTURE") || strings.Contains(string(body), "OUTSIDE_SECRET")) {
			secret.Store(true)
		}
		if strings.Contains(r.URL.Path, "count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"input_tokens":1}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		turn := turns.Add(1)
		done := strings.Contains(string(body), `"tool_result"`)
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"cocogem-fixture\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n", turn)
		if !done {
			encoded, _ := json.Marshal(toolInput)
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_fixture\",\"name\":%q,\"input\":{}}}\n\n", toolName)
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", string(encoded))
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
		} else {
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fixture done\"}}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
		}
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	return server, &turns
}

func runClaudeAutonomous(t *testing.T, args claudeAutonomousFixture) error {
	t.Helper()
	server, _ := claudeFakeModelSecret(t, args.tool, args.input, args.secret)
	defer server.Close()
	previous, had := os.LookupEnv("CLAUDECODE")
	_ = os.Unsetenv("CLAUDECODE")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("CLAUDECODE", previous)
		}
	})
	request := model.Request{
		Executor: "claude", Model: "cocogem-fixture", System: "Use the supplied tools.",
		Messages:             []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Perform one fixture operation."}}}},
		NativeTools:          []string{"Read", "Write", "Edit", "Bash"},
		WorkspaceRoot:        args.root,
		ExecutionMode:        firstString(args.mode, "project_autonomous"),
		FirstResponseTimeout: 60 * time.Second,
		StallTimeout:         60 * time.Second,
		StallAction:          "stop",
		AuthorizeNative:      args.authorize,
	}
	options := runOptions{
		skipCheck: true, allowLocalEgress: args.allowLocalEgress, egressRoots: args.egressRoots,
		adapter: agent.AdapterInfo{ID: "claude", Version: "2.1.318", Supported: true, Adapter: "claude-stream-json", Isolation: "sandbox"},
		claudeSettings: map[string]any{"env": map[string]string{
			"ANTHROPIC_AUTH_TOKEN": "cocogem-fixture",
			"ANTHROPIC_BASE_URL":   server.URL,
			"ANTHROPIC_MODEL":      "cocogem-fixture",
		}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	p := Provider{command: func(id string, argv ...string) *exec.Cmd { return exec.Command(id, argv...) }}
	var events []capturedEvent
	observe := func(kind string, data map[string]any) error {
		events = append(events, capturedEvent{kind: kind, data: data})
		return nil
	}
	return p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", nil
	}, observe, options)
}

type claudeAutonomousFixture struct {
	root             string
	mode             string
	tool             string
	input            any
	secret           *atomic.Bool
	allowLocalEgress bool
	egressRoots      *x509.CertPool
	authorize        func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error)
}

func TestInstalledClaudeAutonomousBoundary(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI is not installed")
	}
	cases := []struct {
		name      string
		command   func(area, root string) string
		authorize func(area, root, marker string) func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error)
		want      func(area, root, marker string) bool
	}{
		{
			name: "inside command is autonomous",
			command: func(area, root string) string {
				return "echo inside-ok > inside.txt"
			},
			authorize: func(area, root, marker string) func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
				return func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					if request.Name != "Bash" || request.Action != "execute" {
						return model.NativePermissionDecision{}, fmt.Errorf("unexpected host request %q %q", request.Name, request.Action)
					}
					return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
				}
			},
			want: func(area, root, marker string) bool {
				data, err := os.ReadFile(filepath.Join(root, "inside.txt"))
				return err == nil && strings.Contains(string(data), "inside-ok")
			},
		},
		{
			name: "outside write stays blocked without approval",
			command: func(area, root string) string {
				return "echo leaked > " + shellQuote(filepath.Join(area, "outside.txt"))
			},
			authorize: func(area, root, marker string) func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
				return func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny}, nil
				}
			},
			want: func(area, root, marker string) bool {
				_, err := os.Stat(filepath.Join(area, "outside.txt"))
				return os.IsNotExist(err)
			},
		},
		{
			name: "approved outside write continues exactly once",
			command: func(area, root string) string {
				return "echo approved > " + shellQuote(filepath.Join(area, "approved.txt"))
			},
			authorize: func(area, root, marker string) func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
				return func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					if request.Name != "Bash" || !strings.Contains(string(request.Input), "approved.txt") {
						return model.NativePermissionDecision{}, fmt.Errorf("unexpected native request %q %s", request.Name, request.Input)
					}
					return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: filepath.Join(area, "approved.txt")}, nil
				}
			},
			want: func(area, root, marker string) bool {
				data, err := os.ReadFile(filepath.Join(area, "approved.txt"))
				return err == nil && strings.Contains(string(data), "approved")
			},
		},
		{
			name: "denied outside write never happens",
			command: func(area, root string) string {
				return "echo denied > " + shellQuote(filepath.Join(area, "denied.txt"))
			},
			authorize: func(area, root, marker string) func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
				return func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "fixture denied"}, nil
				}
			},
			want: func(area, root, marker string) bool {
				_, err := os.Stat(filepath.Join(area, "denied.txt"))
				return os.IsNotExist(err)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			area := t.TempDir()
			root := filepath.Join(area, "workspace")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("PUBLIC_FIXTURE"), 0600); err != nil {
				t.Fatal(err)
			}
			err := runClaudeAutonomous(t, claudeAutonomousFixture{
				root: root, tool: "Bash",
				input:     map[string]any{"command": tc.command(area, root)},
				authorize: tc.authorize(area, root, ""),
			})
			if err != nil {
				t.Fatalf("autonomous run failed: %v", err)
			}
			if !tc.want(area, root, "") {
				t.Fatal("the command outcome did not match the boundary contract")
			}
		})
	}
}

func TestInstalledClaudeAutonomousReadBoundary(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI is not installed")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(area, "secret.txt")
	if err := os.WriteFile(secret, []byte("OUTSIDE_SECRET_FIXTURE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	requests := 0
	authorize := func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		mu.Lock()
		requests++
		mu.Unlock()
		if request.Name != "Read" {
			return model.NativePermissionDecision{}, fmt.Errorf("unexpected request %q", request.Name)
		}
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "outside read denied"}, nil
	}
	err := runClaudeAutonomous(t, claudeAutonomousFixture{
		root: root, tool: "Read", input: map[string]any{"file_path": secret}, authorize: authorize,
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests == 0 {
		t.Fatal("an outside read was not gated by the host")
	}
}

func TestInstalledClaudeAutonomousHardlinkBoundary(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI is not installed")
	}
	for index, command := range []string{
		"printf MUTATED > pkg/linked.txt",
		`python3 -c "import os;fd=os.open(os.path.join('pkg','linked.txt'),os.O_WRONLY);os.write(fd,b'MUTATED');os.close(fd)"`,
	} {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			area := t.TempDir()
			root := filepath.Join(area, "workspace")
			if err := os.MkdirAll(filepath.Join(root, "pkg"), 0700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(area, "outside.txt")
			if err := os.WriteFile(outside, []byte("ORIGINAL_EXTERNAL"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(outside, filepath.Join(root, "pkg", "linked.txt")); err != nil {
				t.Fatal(err)
			}
			var callback atomic.Int32
			secret := &atomic.Bool{}
			err := runClaudeAutonomous(t, claudeAutonomousFixture{
				root: root, tool: "Bash", input: map[string]any{"command": command}, secret: secret,
				authorize: func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					callback.Add(1)
					return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
				},
			})
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			if data, _ := os.ReadFile(outside); string(data) != "ORIGINAL_EXTERNAL" {
				t.Fatalf("the outside inode changed through a project hardlink: %q", data)
			}
			if strings.Contains(command, "printf") && callback.Load() != 0 {
				t.Fatal("a directly named hardlink target was sent to the host instead of being refused")
			}
		})
	}
}

func TestInstalledClaudeFullAccessRevalidation(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI is not installed")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "full-access.txt")
	var callback atomic.Int32
	err := runClaudeAutonomous(t, claudeAutonomousFixture{
		root: root, mode: "full_access", tool: "Bash",
		input: map[string]any{"command": "printf FULL > " + shellQuote(marker)},
		authorize: func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			callback.Add(1)
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
		},
	})
	if err != nil {
		t.Fatalf("full-access run failed: %v", err)
	}
	if callback.Load() == 0 {
		t.Fatal("full access bypassed the host callback")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "FULL" {
		t.Fatalf("full-access command = %q, %v", data, err)
	}
}

func TestInstalledClaudeExternalWriteMockJourney(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI is not installed")
	}
	for _, tc := range []struct {
		name  string
		allow bool
	}{
		{name: "approved", allow: true},
		{name: "denied", allow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
				}
				fmt.Fprint(w, "MOCK_EXTERNAL")
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			area := t.TempDir()
			root := filepath.Join(area, "workspace")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			var externalCalls atomic.Int32
			err := runClaudeAutonomous(t, claudeAutonomousFixture{
				root: root, tool: "Bash", allowLocalEgress: true, egressRoots: roots,
				input: map[string]any{"command": "/usr/bin/curl --noproxy '' --proxy \"$COCOGEM_EGRESS_PROXY\" -sS -X POST -d payload=" + shellQuote("external-effect") + " " + shellQuote(server.URL+"/publish")},
				authorize: func(_ context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
					if request.Name == "external_http" {
						externalCalls.Add(1)
						if !strings.Contains(string(request.Input), "POST") || !strings.HasPrefix(request.Resource, "/.cocogem-external/") {
							return model.NativePermissionDecision{}, fmt.Errorf("external approval evidence = %s %s", request.Resource, request.Input)
						}
						if tc.allow {
							return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: request.Resource}, nil
						}
						return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "fixture external deny"}, nil
					}
					return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: root}, nil
				},
			})
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			if externalCalls.Load() != 1 {
				t.Fatalf("external write approval calls = %d, want 1", externalCalls.Load())
			}
			if tc.allow && posts.Load() != 1 {
				t.Fatalf("approved external POST reached upstream %d times", posts.Load())
			}
			if !tc.allow && posts.Load() != 0 {
				t.Fatalf("denied external POST reached upstream %d times", posts.Load())
			}
		})
	}
}
