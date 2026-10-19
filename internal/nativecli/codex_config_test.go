package nativecli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexKeepsSelectedConnectionWithoutUserCapabilities(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	config := `model = "native-default-model"
model_provider = "local_proxy"
[model_providers.local_proxy]
name = "Selected connection"
base_url = "https://fixture.invalid/v1"
wire_api = "responses"
requires_openai_auth = false
experimental_bearer_token = "fixture-bearer-secret"
[model_providers.local_proxy.http_headers]
X-Account = "fixture-header-secret"
[features]
hooks = true
shell_tool = true
[mcp_servers.untrusted]
command = "never-run-this"
`
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, args, env, err := codexConnectionConfig("default")
	if err != nil || resolved != "native-default-model" || len(args) == 0 {
		t.Fatalf("profile resolution failed: model=%q err=%v", resolved, err)
	}
	raw := strings.Join(args, " ")
	for _, forbidden := range []string{"fixture-bearer-secret", "fixture-header-secret", "never-run-this", "[features]", "[mcp_servers"} {
		if strings.Contains(raw, forbidden) {
			t.Fatal("connection profile leaked credentials or inherited capabilities")
		}
	}
	if env["COCOGEM_CODEX_CONNECTION_TOKEN"] != "fixture-bearer-secret" || len(env) != 2 {
		t.Fatal("connection credentials were not confined to child environment")
	}
	if !strings.Contains(raw, "cocogem_connection") || !strings.Contains(raw, "https://fixture.invalid/v1") {
		t.Fatal("selected connection was not preserved")
	}
	files, _ := os.ReadDir(home)
	if len(files) != 1 {
		t.Fatal("connection adaptation wrote unexpected native home files")
	}
	original, err := os.ReadFile(path)
	if err != nil || string(original) != config {
		t.Fatal("original native configuration was changed")
	}
}

func TestInstalledCodexConfiguredConnection(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_INSTALLED_CODEX") != "1" {
		t.Skip("set COCOGEM_CHECK_INSTALLED_CODEX=1 for a local provider-free profile check")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	var seen atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-fixture-secret" || r.Header.Get("X-Fixture-Header") != "local-header-secret" {
			t.Errorf("configured connection credentials missing: bearer=%v header=%v", r.Header.Get("Authorization") == "Bearer local-fixture-secret", r.Header.Get("X-Fixture-Header") == "local-header-secret")
		}
		var input struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "native-default-model" {
			t.Error("configured default model was not selected")
		}
		seen.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "public fixture answer", "annotations": []any{}}}}
		for _, event := range []map[string]any{{"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}}}} {
			raw, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
	}))
	defer server.Close()
	config := `model="native-default-model"` + "\n" + `model_provider="configured_proxy"` + "\n" + `[model_providers.configured_proxy]` + "\n" + `name="Local profile fixture"` + "\n" + `base_url="` + server.URL + `/v1"` + "\n" + `wire_api="responses"` + "\n" + `requires_openai_auth=false` + "\n" + `experimental_bearer_token="local-fixture-secret"` + "\n" + `supports_websockets=false` + "\n" + `[model_providers.configured_proxy.http_headers]` + "\n" + `X-Fixture-Header="local-header-secret"`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	request := testRequest()
	request.Executor = "codex"
	request.Tools = nil
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := (Provider{}).Run(ctx, request, func(context.Context, string, json.RawMessage) (string, error) {
		t.Fatal("unexpected tool call")
		return "", nil
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !seen.Load() {
		t.Fatal("native CLI did not load its scoped connection profile")
	}
}

func TestPrepareCodexUsesResolvedDefaultInCatalogAndArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(`model = "native-default-model"`), 0600); err != nil {
		t.Fatal(err)
	}
	request := testRequest()
	request.Executor = "codex"
	args, _, err := prepareCodex(request, t.TempDir(), &bridge{addr: "127.0.0.1:1234", token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for index, arg := range args {
		if arg == "--model" && index+1 < len(args) && args[index+1] == "native-default-model" {
			found = true
		}
	}
	if !found {
		t.Fatal("resolved native model missing from arguments")
	}
	for index, arg := range args {
		if arg == "-c" && index+1 < len(args) && strings.HasPrefix(args[index+1], "model_catalog_json=") {
			var path string
			_ = json.Unmarshal([]byte(strings.TrimPrefix(args[index+1], "model_catalog_json=")), &path)
			raw, _ := os.ReadFile(path)
			if !strings.Contains(string(raw), `"slug":"native-default-model"`) {
				t.Fatal("catalog and selected model diverged")
			}
		}
	}
}

func TestCodexConnectionRejectsUnsafeOrUnusableConfiguration(t *testing.T) {
	for _, value := range []string{
		`model = "default"`,
		`model="fixture-model"` + "\n" + `model_provider="missing"`,
		`model="fixture-model"` + "\n" + `model_provider="proxy"` + "\n" + `[model_providers.proxy.auth]` + "\n" + `command="never-run-this"`,
		`model="fixture-model"` + "\n" + `model_provider="proxy"` + "\n" + `[model_providers.proxy]` + "\n" + `base_url="https://credential:secret@fixture.invalid"`,
		`model = "fixture-secret`,
	} {
		t.Run("reject", func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, _, err := codexConnectionConfig("default")
			if err == nil || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "never-run-this") {
				t.Fatalf("unsafe configuration error=%v", err)
			}
		})
	}
}
