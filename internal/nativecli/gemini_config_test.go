package nativecli

import (
	"encoding/json"
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
	plan, err := geminiPlan("default")
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
	plan, err := geminiPlan("default")
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
