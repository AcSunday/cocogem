package nativecli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	for _, key := range []string{"GEMINI_MODEL", "GOOGLE_GENAI_MODEL", "GEMINI_CLI_MODEL"} {
		t.Setenv(key, "")
		_ = os.Unsetenv(key)
	}
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

func TestGeminiSnapshotKeepsUnsetEnvironmentUnset(t *testing.T) {
	// The Gemini CLI loads its own .env only for keys that are not present in
	// the process environment, so a present empty value and an unset key have
	// different meanings. Editing the parent environment after the probe must
	// not change what the frozen snapshot launches with.
	for _, key := range geminiConnectionEnv {
		if value, ok := os.LookupEnv(key); ok {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, value) })
		}
	}
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	t.Setenv("GEMINI_MODEL", "synthetic-frozen-model")
	t.Setenv("GEMINI_CLI_AUTH_OVERRIDE", "")
	plan, err := geminiPlan("default")
	if err != nil {
		t.Fatal(err)
	}
	if plan.geminiEnv["GEMINI_MODEL"] != "synthetic-frozen-model" || plan.geminiEnv["GEMINI_CLI_AUTH_OVERRIDE"] != "" {
		t.Fatalf("present values were not frozen: %v", plan.geminiEnv)
	}
	if _, ok := plan.geminiEnv["GEMINI_API_KEY"]; ok {
		t.Fatal("an unset key was materialized as an empty override")
	}
	_ = os.Unsetenv("GEMINI_CLI_AUTH_OVERRIDE")
	unsetPlan, err := geminiPlan("default")
	if err != nil {
		t.Fatal(err)
	}
	if unsetPlan.info.Identity == plan.info.Identity {
		t.Fatal("explicit empty and unset environment values shared an identity")
	}
	t.Setenv("GEMINI_CLI_AUTH_OVERRIDE", "")
	rememberPlan(plan)

	// Simulate edits between the connection probe and the launch.
	t.Setenv("GEMINI_API_KEY", "synthetic-late-key")
	t.Setenv("GEMINI_MODEL", "synthetic-late-model")
	t.Setenv("GEMINI_CLI_AUTH_OVERRIDE", "vertex-ai")

	dump := filepath.Join(t.TempDir(), "env")
	p := Provider{command: func(id string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestNativeCLIHelperProcess", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "COCOGEM_NATIVE_HELPER=1", "COCOGEM_NATIVE_MODE=ok", "COCOGEM_NATIVE_EXECUTOR=gemini", "COCOGEM_NATIVE_ENV_DUMP="+dump)
		return cmd
	}}
	request := testRequest()
	request.Executor = "gemini"
	request.Model = "default"
	request.ExpectedConnection = model.Connection{Identity: plan.info.Identity, ResolvedModel: plan.info.ResolvedModel}
	if err := p.Run(context.Background(), request, func(context.Context, string, json.RawMessage) (string, error) {
		return "ok", nil
	}, func(string, map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	child := map[string]string{}
	for _, entry := range strings.Split(string(raw), "\n") {
		key, value, _ := strings.Cut(entry, "=")
		child[key] = value
	}
	if _, ok := child["GEMINI_API_KEY"]; ok {
		t.Fatal("a variable exported after the probe leaked into the frozen launch")
	}
	if _, ok := child["GOOGLE_CLOUD_PROJECT"]; ok {
		t.Fatal("an unset allowlisted variable was exported as empty")
	}
	if child["GEMINI_MODEL"] != "synthetic-frozen-model" {
		t.Fatalf("frozen value was replaced by the late edit: %q", child["GEMINI_MODEL"])
	}
	if value, ok := child["GEMINI_CLI_AUTH_OVERRIDE"]; !ok || value != "" {
		t.Fatalf("explicit empty value did not stay explicit empty: %q present=%t", value, ok)
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
