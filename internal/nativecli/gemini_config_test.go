package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

const geminiUserSettings = `{
  "model": {"name": "fixture-model"},
  "modelConfigs": {"customOverrides": [
    {"match": {"model": "fixture-model"}, "modelConfig": {"generateContentConfig": {"thinkingConfig": {"thinkingLevel": "HIGH", "thinkingBudget": 4096, "includeThoughts": false, "temperature": 0.9}}}},
    {"match": {}, "modelConfig": {"generateContentConfig": {"thinkingConfig": {"thinkingLevel": "LOW"}}}}
  ]},
  "tools": {"core": ["run_shell_command"]},
  "hooks": {"script": "never-run-this-hook"},
  "mcpServers": {"untrusted": {"command": "never-run-this-server"}}
}`

func writeGeminiUserSettings(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("GEMINI_CONFIG_DIR", t.TempDir())
	dir := filepath.Join(home, ".gemini")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(geminiUserSettings), 0600); err != nil {
		t.Fatal(err)
	}
}

func scopedGeminiSettings(t *testing.T, workdir string) (map[string]any, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workdir, ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	return settings, string(raw)
}

func scopedGeminiOverrides(t *testing.T, settings map[string]any) []any {
	t.Helper()
	configs, _ := settings["modelConfigs"].(map[string]any)
	overrides, _ := configs["customOverrides"].([]any)
	if len(overrides) != 1 {
		t.Fatalf("scoped Gemini overrides=%v, want exactly one", overrides)
	}
	return overrides
}

func TestGeminiConfiguredThinkingDefaultsSurviveScopedSettings(t *testing.T) {
	writeGeminiUserSettings(t)
	plan, err := geminiPlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if plan.info.ResolvedModel != "fixture-model" || len(plan.geminiThinking) != 1 {
		t.Fatalf("configured thinking defaults were not captured: model=%q thinking=%v", plan.info.ResolvedModel, plan.geminiThinking)
	}
	gate := &bridge{addr: "127.0.0.1:1234", token: "fixturetoken123456"}
	for name, launch := range map[string]*connectionPlan{"planned": &plan, "unplanned": nil} {
		t.Run(name, func(t *testing.T) {
			workdir := t.TempDir()
			request := model.Request{Executor: "gemini", Model: "fixture-model"}
			if _, _, err := prepareGemini(request, workdir, gate, launch); err != nil {
				t.Fatal(err)
			}
			settings, raw := scopedGeminiSettings(t, workdir)
			override := scopedGeminiOverrides(t, settings)[0].(map[string]any)
			if match, _ := override["match"].(map[string]any); match["model"] != "fixture-model" {
				t.Fatalf("thinking override lost its model match: %v", override)
			}
			thinking := override["modelConfig"].(map[string]any)["generateContentConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
			if thinking["thinkingLevel"] != "HIGH" || thinking["thinkingBudget"] != float64(4096) || thinking["includeThoughts"] != false {
				t.Fatalf("configured thinking defaults were not preserved: %v", thinking)
			}
			if _, ok := thinking["temperature"]; ok {
				t.Fatal("non-thinking modelConfig field was forwarded")
			}
			for _, forbidden := range []string{"never-run-this-hook", "never-run-this-server", "run_shell_command"} {
				if strings.Contains(raw, forbidden) {
					t.Fatalf("scoped Gemini settings inherited %q: %s", forbidden, raw)
				}
			}
		})
	}
}

func TestGeminiMemberThinkingReplacesConfiguredDefaults(t *testing.T) {
	writeGeminiUserSettings(t)
	plan, err := geminiPlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	request := model.Request{Executor: "gemini", Model: "fixture-model", ThinkingLevel: "low"}
	if _, _, err := prepareGemini(request, workdir, &bridge{addr: "127.0.0.1:1234", token: "fixturetoken123456"}, &plan); err != nil {
		t.Fatal(err)
	}
	settings, raw := scopedGeminiSettings(t, workdir)
	override := scopedGeminiOverrides(t, settings)[0].(map[string]any)
	thinking := override["modelConfig"].(map[string]any)["generateContentConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if thinking["thinkingLevel"] != "LOW" || strings.Contains(raw, "HIGH") {
		t.Fatalf("explicit member thinking did not replace configured defaults: %s", raw)
	}
}

func TestGeminiConnectionSnapshotsUserEnvFile(t *testing.T) {
	for _, key := range geminiConnectionEnv {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_MODEL", "env-model")
	dir := filepath.Join(home, ".gemini")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeEnv := func(endpoint string) {
		t.Helper()
		raw := "export GEMINI_API_KEY='synthetic-$TOKEN'\nGOOGLE_API_KEY=ignored\nGOOGLE_GEMINI_BASE_URL=\"" + endpoint + "\"\nGEMINI_MODEL=file-model\nNODE_OPTIONS=unsafe-value\n"
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEnv("https://first.invalid")
	plan, err := geminiPlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if plan.info.ResolvedModel != "env-model" || plan.geminiGatedEnv["GEMINI_API_KEY"] != "synthetic-$TOKEN" || plan.geminiGatedEnv["GOOGLE_GEMINI_BASE_URL"] != "https://first.invalid" || plan.geminiGatedEnv["GOOGLE_API_KEY"] != "" {
		t.Fatal("Gemini connection did not preserve the user env file or process environment precedence")
	}
	if _, ok := plan.geminiGatedEnv["NODE_OPTIONS"]; ok {
		t.Fatal("non-connection variable crossed the configuration boundary")
	}
	if _, ok := plan.geminiEnv["GEMINI_API_KEY"]; ok {
		t.Fatal("user file values changed the non-gated environment")
	}
	writeEnv("https://second.invalid")
	next, err := geminiPlan(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if next.info.Identity == plan.info.Identity || plan.geminiGatedEnv["GOOGLE_GEMINI_BASE_URL"] != "https://first.invalid" {
		t.Fatal("connection file changes were not reflected in identity while preserving the original snapshot")
	}
}

func TestGeminiConnectionParsingHonorsCancellation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", ".env"), []byte("GEMINI_API_KEY=synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := geminiPlan(ctx, "default"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled connection parsing returned %v", err)
	}
}
