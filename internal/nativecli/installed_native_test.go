package nativecli

import (
	"context"
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

// Opt-in installed-binary check: the real Claude CLI runs against a local fake
// model, so no provider credentials are used and no real web request is made.
// Opt-in installed-binary denial check: the real Claude CLI receives a
// synthetic model that requests tools outside --tools. The CLI itself must
// answer with its fixed "No such tool available" result; the host accepts only
// that exact proof and never executes or replays the call. This is CLI-boundary
// evidence, not provider or model-quality evidence.
func TestInstalledClaudeDeniedToolBoundary(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI is not installed")
	}
	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{"Bash", map[string]any{"command": "touch"}},
		{"Task", map[string]any{"description": "synthetic subtask", "prompt": "synthetic subtask"}},
		{"mcp__cocogem__not_exposed_fixture", map[string]any{"query": "synthetic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			area := t.TempDir()
			root := filepath.Join(area, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(area, "denied-bash-marker")
			if tc.name == "Bash" {
				tc.input = map[string]any{"command": "touch " + marker}
			}
			var turns atomic.Int32
			var mu sync.Mutex
			violations := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "fixture body", http.StatusBadRequest)
					return
				}
				if strings.Contains(r.URL.Path, "count_tokens") {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"input_tokens":1}`)
					return
				}
				var request struct {
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				}
				if json.Unmarshal(body, &request) == nil {
					for _, tool := range request.Tools {
						if tool.Name == "" || tool.Name == "DeferredToolPlaceholder" || tool.Name == "Read" || strings.HasPrefix(tool.Name, "mcp__cocogem__") {
							continue
						}
						mu.Lock()
						violations = append(violations, tool.Name)
						mu.Unlock()
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				turn := turns.Add(1)
				if strings.Contains(string(body), "No such tool available") {
					fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_recovered\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"cocogem-fixture\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
					fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"RECOVERED_WITHOUT_TOOL_EXECUTION\"}}\n\n")
					fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
					fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
					fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					return
				}
				encoded, _ := json.Marshal(tc.input)
				fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"cocogem-fixture\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n", turn)
				fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_%d\",\"name\":%q,\"input\":{}}}\n\n", turn, tc.name)
				fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", string(encoded))
				fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
				fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
				fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()

			previous, had := os.LookupEnv("CLAUDECODE")
			_ = os.Unsetenv("CLAUDECODE")
			t.Cleanup(func() {
				if had {
					_ = os.Setenv("CLAUDECODE", previous)
				}
			})
			t.Setenv("ENABLE_TOOL_SEARCH", "false")
			var events []capturedEvent
			observe := func(kind string, data map[string]any) error {
				events = append(events, capturedEvent{kind: kind, data: data})
				return nil
			}
			request := model.Request{
				Executor:             "claude",
				Model:                "cocogem-fixture",
				System:               "Use only the supplied Read tool.",
				Messages:             []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Request the synthetic denied tool once, then answer."}}}},
				NativeTools:          []string{"Read"},
				WorkspaceRoot:        root,
				FirstResponseTimeout: 30 * time.Second,
				StallTimeout:         30 * time.Second,
				StallAction:          "stop",
			}
			options := runOptions{
				skipCheck: true,
				adapter:   agent.AdapterInfo{ID: "claude", Version: "2.1.312", Supported: true, Adapter: "claude-stream-json", Isolation: "enforced", Program: path},
				claudeSettings: map[string]any{
					"env": map[string]string{
						"ANTHROPIC_AUTH_TOKEN": "cocogem-fixture",
						"ANTHROPIC_BASE_URL":   server.URL,
						"ANTHROPIC_MODEL":      "cocogem-fixture",
					},
				},
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
			if err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
				return "", nil
			}, observe, options); err != nil {
				t.Fatalf("installed CLI fixed denial was not accepted: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("a denied Bash command wrote its marker")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(violations) != 0 {
				t.Fatalf("installed CLI offered tools outside the granted subset: %v", violations)
			}
			var denied map[string]any
			for _, event := range events {
				switch event.kind {
				case "tool_started":
					if event.data["name"] == safeToken(tc.name) {
						t.Fatalf("denied tool emitted tool_started: %+v", event.data)
					}
				case "tool_finished":
					if event.data["denied"] == true {
						denied = event.data
					}
					if event.data["name"] == safeToken(tc.name) && event.data["status"] != "error" {
						t.Fatalf("denied tool result was not an error: %+v", event.data)
					}
				}
			}
			if denied == nil {
				t.Fatalf("CLI denial was not recorded as a bounded event: %+v", events)
			}
			if _, present := denied["input"]; present {
				t.Fatal("denial event retained the denied tool input")
			}
		})
	}
}

func TestInstalledClaudeNativeReadBoundary(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_NATIVE_TOOLS=1 for an installed-binary boundary check")
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI is not installed")
	}
	area := t.TempDir()
	root := filepath.Join(area, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("PUBLIC_FIXTURE_NATIVE_READ"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET_FIXTURE_DO_NOT_READ"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(area, "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE_FIXTURE_DO_NOT_READ"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("SECRET_FIXTURE_GIT"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, markerPath := range []string{filepath.Join(root, "CLAUDE.md"), filepath.Join(area, "CLAUDE.md")} {
		if err := os.WriteFile(markerPath, []byte("COCOGEM_PROJECT_MEMORY_MARKER"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	var turns atomic.Int32
	var memorySeen atomic.Bool
	var mu sync.Mutex
	violations := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "fixture body", http.StatusBadRequest)
			return
		}
		if strings.Contains(r.URL.Path, "count_tokens") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"input_tokens":1}`)
			return
		}
		if strings.Contains(string(body), "COCOGEM_PROJECT_MEMORY_MARKER") {
			memorySeen.Store(true)
		}
		var request struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.Unmarshal(body, &request) == nil {
			for _, tool := range request.Tools {
				if tool.Name == "" || tool.Name == "DeferredToolPlaceholder" || tool.Name == "Read" || strings.HasPrefix(tool.Name, "mcp__cocogem__") {
					continue
				}
				mu.Lock()
				violations = append(violations, tool.Name)
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		turn := turns.Add(1)
		type call struct {
			name  string
			input any
		}
		var next *call
		bodyText := string(body)
		switch {
		case !strings.Contains(bodyText, filepath.Join(root, "README.md")):
			next = &call{"Read", map[string]any{"file_path": filepath.Join(root, "README.md")}}
		case !strings.Contains(bodyText, filepath.Join(root, ".env")):
			next = &call{"Read", map[string]any{"file_path": filepath.Join(root, ".env")}}
		case !strings.Contains(bodyText, outside):
			next = &call{"Read", map[string]any{"file_path": outside}}
		case !strings.Contains(bodyText, filepath.Join(root, "linked.txt")):
			next = &call{"Read", map[string]any{"file_path": filepath.Join(root, "linked.txt")}}
		case !strings.Contains(bodyText, filepath.Join(root, ".git", "config")):
			next = &call{"Read", map[string]any{"file_path": filepath.Join(root, ".git", "config")}}
		}
		if next == nil {
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_done\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"cocogem-fixture\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fixture complete\"}}\n\n")
			fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		encoded, _ := json.Marshal(next.input)
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"cocogem-fixture\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n", turn)
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_%d\",\"name\":%q,\"input\":{}}}\n\n", turn, next.name)
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", string(encoded))
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	previous, had := os.LookupEnv("CLAUDECODE")
	_ = os.Unsetenv("CLAUDECODE")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("CLAUDECODE", previous)
		}
	})
	var events []capturedEvent
	observe := func(kind string, data map[string]any) error {
		events = append(events, capturedEvent{kind: kind, data: data})
		return nil
	}
	request := model.Request{
		Executor:             "claude",
		Model:                "cocogem-fixture",
		System:               "Use only the supplied Read tool on the fixture paths.",
		Messages:             []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Read the fixture paths in order."}}}},
		NativeTools:          []string{"Read"},
		WorkspaceRoot:        root,
		FirstResponseTimeout: 30 * time.Second,
		StallTimeout:         30 * time.Second,
		StallAction:          "stop",
	}
	options := runOptions{
		skipCheck: true,
		adapter:   agent.AdapterInfo{ID: "claude", Version: "2.1.312", Supported: true, Adapter: "claude-stream-json", Isolation: "enforced", Program: path},
		claudeSettings: map[string]any{
			"env": map[string]string{
				"ANTHROPIC_AUTH_TOKEN": "cocogem-fixture",
				"ANTHROPIC_BASE_URL":   server.URL,
				"ANTHROPIC_MODEL":      "cocogem-fixture",
			},
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	p := Provider{command: func(id string, args ...string) *exec.Cmd { return exec.Command(id, args...) }}
	if err := p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		return "", nil
	}, observe, options); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if memorySeen.Load() {
		t.Fatal("project or parent CLAUDE.md was loaded despite the disabled memory boundary")
	}
	if len(violations) != 0 {
		t.Fatalf("installed CLI requested tools outside the read-only subset: %v", violations)
	}
	var readOutput, envOutput string
	for _, event := range events {
		if event.kind != "tool_finished" || event.data["origin"] != "native" {
			continue
		}
		output, _ := event.data["output"].(string)
		if strings.Contains(output, "SECRET_FIXTURE") || strings.Contains(output, "OUTSIDE_FIXTURE") || strings.Contains(output, "SECRET_FIXTURE_GIT") {
			t.Fatalf("denied fixture content leaked into a native result: %q", output)
		}
		status, _ := event.data["status"].(string)
		if event.data["name"] != "Read" {
			continue
		}
		if strings.Contains(output, "PUBLIC_FIXTURE_NATIVE_READ") {
			readOutput = output
			if status != "ok" {
				t.Fatalf("public fixture read was not successful: %+v", event.data)
			}
		}
		if strings.Contains(output, "denied") || strings.Contains(output, "outside") || strings.Contains(output, "Permission") {
			envOutput = output
		}
	}
	if readOutput == "" {
		t.Fatalf("actual native Read did not return the public fixture: %+v", events)
	}
	if envOutput == "" {
		t.Fatalf("denied paths did not produce visible native errors: %+v", events)
	}
}
