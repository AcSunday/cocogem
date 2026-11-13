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
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestOpenCodeGatewayUsesSelectedProvider(t *testing.T) {
	for _, tc := range []struct {
		name, group, settings string
		duplicateSelected     bool
	}{
		{name: "provider options", group: "provider", settings: "options"},
		{name: "providers settings", group: "providers", settings: "settings"},
		{name: "providers take precedence for duplicate id", group: "providers", settings: "settings", duplicateSelected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "selected "+r.URL.Path)
			}))
			defer selected.Close()
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, "other "+r.URL.Path)
			}))
			defer other.Close()

			config := map[string]any{
				"model": "other/default",
				tc.group: map[string]any{
					"selected": map[string]any{tc.settings: map[string]any{"baseURL": selected.URL + "/v1", "apiKey": "synthetic-key"}, "name": "Selected"},
					"other":    map[string]any{tc.settings: map[string]any{"baseURL": other.URL + "/v1", "apiKey": "other-key"}},
				},
			}
			otherGroup, otherSettings := "providers", "settings"
			if tc.group == "providers" {
				otherGroup, otherSettings = "provider", "options"
			}
			otherID := "other"
			if tc.duplicateSelected {
				otherID = "selected"
			}
			config[otherGroup] = map[string]any{otherID: map[string]any{otherSettings: map[string]any{"baseURL": other.URL + "/v1"}}}
			original, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			plan := connectionPlan{openCodeConfig: config, openCodeModel: "selected/nested/model#max", openCodeVariants: []string{"max"}}
			gate := &bridge{sandbox: &nativeSandbox{}, addr: "127.0.0.1:1", token: "synthetic"}
			t.Cleanup(func() {
				if gate.modelGateway != nil {
					gate.modelGateway.close()
				}
			})
			dir := t.TempDir()
			args, _, err := (Provider{}).prepareOpenCode(t.Context(), model.Request{ExecutionMode: "project_autonomous", ThinkingLevel: "max"}, dir, gate, "", &plan)
			if err != nil {
				t.Fatalf("prepare selected provider: %v", err)
			}
			if gate.modelGateway == nil || !strings.Contains(strings.Join(args, " "), "--model selected/nested/model#max") {
				t.Fatalf("selected model or gateway missing: %v", args)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "opencode.json"))
			if err != nil {
				t.Fatal(err)
			}
			var written map[string]any
			if err := json.Unmarshal(raw, &written); err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal(original, &want); err != nil {
				t.Fatal(err)
			}
			want[tc.group].(map[string]any)["selected"].(map[string]any)[tc.settings].(map[string]any)["baseURL"] = gate.modelGateway.url()
			for _, group := range []string{"provider", "providers"} {
				if !reflect.DeepEqual(written[group], want[group]) {
					t.Fatalf("%s connection fields were changed unexpectedly: got %v, want %v", group, written[group], want[group])
				}
			}
			after, err := json.Marshal(plan.openCodeConfig)
			if err != nil || string(after) != string(original) {
				t.Fatal("preparation changed the frozen connection plan")
			}
			client := &http.Client{Timeout: 3 * time.Second}
			response, err := client.Get(gate.modelGateway.url() + "/chat/completions")
			if err != nil {
				t.Fatalf("request through gateway: %v", err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK || string(body) != "selected /v1/chat/completions" {
				t.Fatalf("gateway reached the wrong upstream: status=%d body=%q err=%v", response.StatusCode, body, err)
			}
		})
	}
}

// This opt-in check uses the installed CLI and two synthetic loopback models.
func TestInstalledOpenCodeProjectAutonomousProviderOptions(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("project-autonomous native isolation is only available on macOS")
	}
	var selectedCalls, otherCalls atomic.Int32
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		installedPiChunk(w, map[string]any{"role": "assistant", "content": "selected provider"}, nil)
		installedPiChunk(w, map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer selected.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls.Add(1)
		http.Error(w, "wrong provider", http.StatusInternalServerError)
	}))
	defer other.Close()

	dir := installedOpenCodeFixture(t, selected.URL)
	provider := func(endpoint string) map[string]any {
		return map[string]any{
			"npm": "@ai-sdk/openai-compatible", "name": "Synthetic provider",
			"options": map[string]any{"baseURL": endpoint + "/v1", "apiKey": "synthetic-key"},
			"models":  map[string]any{"fixture": map[string]any{"name": "Synthetic model", "limit": map[string]any{"context": 128000, "output": 1024}}},
		}
	}
	config := map[string]any{
		"model": "other/fixture",
		"provider": map[string]any{
			"other": provider(other.URL), "selected": provider(selected.URL),
		},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	request := model.Request{
		Executor: "opencode", Model: "selected/fixture", ExecutionMode: "project_autonomous", WorkspaceRoot: t.TempDir(),
		System: "Reply without tools.", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Synthetic provider test."}}}},
		FirstResponseTimeout: 30 * time.Second, StallTimeout: 30 * time.Second, StallAction: "stop",
	}
	var answer strings.Builder
	p := Provider{command: func(id string, argv ...string) *exec.Cmd { return exec.Command(id, argv...) }}
	err = p.run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil }, func(kind string, data map[string]any) error {
		if kind == "text" {
			answer.WriteString(data["text"].(string))
		}
		return nil
	}, runOptions{})
	if err != nil || answer.String() != "selected provider" || selectedCalls.Load() == 0 || otherCalls.Load() != 0 {
		t.Fatalf("selected provider was not used: answer=%q selected=%d other=%d err=%v", answer.String(), selectedCalls.Load(), otherCalls.Load(), err)
	}
}

func TestOpenCodeGatewayRejectsMissingSelectedEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, group, settings string
		duplicateSelected     bool
	}{
		{name: "provider options", group: "provider", settings: "options"},
		{name: "providers settings", group: "providers", settings: "settings"},
		{name: "missing endpoint in overriding providers entry", group: "providers", settings: "settings", duplicateSelected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{
				"model": "other/default",
				tc.group: map[string]any{
					"selected": map[string]any{tc.settings: map[string]any{"apiKey": "synthetic-key"}},
					"other":    map[string]any{tc.settings: map[string]any{"baseURL": "https://other.invalid/v1"}},
				},
			}
			otherGroup, otherSettings := "providers", "settings"
			if tc.group == "providers" {
				otherGroup, otherSettings = "provider", "options"
			}
			otherID := "other"
			if tc.duplicateSelected {
				otherID = "selected"
			}
			config[otherGroup] = map[string]any{otherID: map[string]any{otherSettings: map[string]any{"baseURL": "https://other.invalid/v1"}}}
			plan := connectionPlan{openCodeConfig: config, openCodeModel: "selected/model#max", openCodeVariants: []string{"max"}}
			gate := &bridge{sandbox: &nativeSandbox{}, addr: "127.0.0.1:1"}
			t.Cleanup(func() {
				if gate.modelGateway != nil {
					gate.modelGateway.close()
				}
			})
			dir := t.TempDir()
			_, _, err := (Provider{}).prepareOpenCode(t.Context(), model.Request{ExecutionMode: "project_autonomous"}, dir, gate, "", &plan)
			if err == nil || !strings.Contains(err.Error(), "OpenCode model endpoint is not available for the loopback gateway") {
				t.Fatalf("missing selected endpoint must fail closed, got %v", err)
			}
			if gate.modelGateway != nil {
				t.Fatal("a gateway was created for a different provider")
			}
			if _, err := os.Stat(filepath.Join(dir, "opencode.json")); !os.IsNotExist(err) {
				t.Fatalf("scoped execution config was written after endpoint failure: %v", err)
			}
		})
	}
}
