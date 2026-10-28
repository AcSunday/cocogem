package nativecli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestCodexConnectionIdentityTracksModelProviderAndEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	write := func(model, provider, endpoint string) {
		t.Helper()
		config := "model = \"" + model + "\"\nmodel_provider = \"" + provider + "\"\n[model_providers." + provider + "]\nname = \"Synthetic\"\nbase_url = \"" + endpoint + "\"\nwire_api = \"responses\"\nrequires_openai_auth = false\n"
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("synthetic-a", "provider_a", "https://a.invalid/v1")
	first, err := codexConnectionInfo("default")
	if err != nil {
		t.Fatal(err)
	}
	write("synthetic-b", "provider_b", "https://b.invalid/v1")
	second, err := codexConnectionInfo("default")
	if err != nil {
		t.Fatal(err)
	}
	if first.ResolvedModel != "synthetic-a" || second.ResolvedModel != "synthetic-b" || first.Identity == second.Identity {
		t.Fatalf("connection change was not detected: %+v %+v", first, second)
	}
}

func TestClaudeConnectionIdentityTracksEnvironmentWithoutSettingsFile(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "synthetic-only")
	t.Setenv("ANTHROPIC_MODEL", "synthetic-a")
	t.Setenv("ANTHROPIC_BASE_URL", "https://a.invalid/v1")
	first, err := (Provider{}).InspectConnection(t.Context(), "claude", "default")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_MODEL", "synthetic-b")
	t.Setenv("ANTHROPIC_BASE_URL", "https://b.invalid/v1")
	second, err := (Provider{}).InspectConnection(t.Context(), "claude", "default")
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity == second.Identity || first.ResolvedModel != "synthetic-a" || second.ResolvedModel != "synthetic-b" {
		t.Fatalf("environment-only connection change was missed: %+v %+v", first, second)
	}
}

func TestGeminiConnectionIdentityTracksCLIHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("GEMINI_CONFIG_DIR", t.TempDir())
	dir := filepath.Join(home, ".gemini")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"model":{"name":"synthetic-a"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := (Provider{}).InspectConnection(t.Context(), "gemini", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"model":{"name":"synthetic-b"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := (Provider{}).InspectConnection(t.Context(), "gemini", "default")
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity == second.Identity || first.ResolvedModel != "synthetic-a" || second.ResolvedModel != "synthetic-b" {
		t.Fatalf("Gemini CLI home model change was missed: %+v %+v", first, second)
	}
}

func TestCodexRunUsesResolvedConnectionPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	write := func(model string) {
		t.Helper()
		config := "model = \"" + model + "\"\nmodel_provider = \"synthetic\"\n[model_providers.synthetic]\nname = \"Synthetic\"\nbase_url = \"https://synthetic.invalid/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = false\n"
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("synthetic-a")
	info, err := (Provider{}).InspectConnection(t.Context(), "codex", "default")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an edit between preflight and launch.
	write("synthetic-b")
	request := testRequest()
	request.Executor = "codex"
	request.Model = "default"
	request.ExpectedConnection = model.Connection{ResolvedModel: info.ResolvedModel, Identity: info.Identity}
	p := fakeProvider("call")
	command := p.command
	actual := ""
	p.command = func(id string, args ...string) *exec.Cmd {
		for i, arg := range args {
			if arg == "--model" && i+1 < len(args) {
				actual = args[i+1]
			}
		}
		return command(id, args...)
	}
	err = p.Run(context.Background(), request, func(context.Context, string, json.RawMessage) (string, error) { return "ok", nil }, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if actual != "synthetic-a" {
		t.Fatalf("launched with model %q after recording %q", actual, info.ResolvedModel)
	}
}
