package nativecli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/AcSunday/cocogem/internal/agent"
)

// connectionPlan is the exact non-secret identity and private launch inputs
// resolved before an attempt. It is cached so a later config edit cannot change
// what the already-authorized attempt launches.
type connectionPlan struct {
	executor       string
	info           agent.ConnectionInfo
	claudeSettings map[string]any
	codexResolved  string
	codexArgs      []string
	codexEnv       map[string]string
	geminiModel    string
	geminiEnv      map[string]string
}

var connectionPlans = struct {
	sync.Mutex
	entries map[string]connectionPlan
}{entries: map[string]connectionPlan{}}

func rememberPlan(plan connectionPlan) {
	if plan.info.Identity == "" {
		return
	}
	key := plan.executor + "\x00" + plan.info.Identity
	connectionPlans.Lock()
	defer connectionPlans.Unlock()
	connectionPlans.entries[key] = plan
}

func cachedPlan(executor, identity string) (connectionPlan, bool) {
	connectionPlans.Lock()
	defer connectionPlans.Unlock()
	plan, ok := connectionPlans.entries[executor+"\x00"+identity]
	return plan, ok
}

// InspectConnection resolves the actual model and a non-secret upstream identity.
func (p Provider) InspectConnection(_ context.Context, executor, model string) (agent.ConnectionInfo, error) {
	plan, err := resolveConnection(executor, model)
	if err != nil {
		return agent.ConnectionInfo{}, err
	}
	rememberPlan(plan)
	return plan.info, nil
}

func resolveConnection(executor, model string) (connectionPlan, error) {
	switch executor {
	case "claude":
		return claudePlan(model)
	case "codex":
		return codexPlan(model)
	case "gemini":
		return geminiPlan(model)
	default:
		return connectionPlan{}, errors.New("不支持的 CLI")
	}
}

func claudePlan(model string) (connectionPlan, error) {
	settings, err := claudeConnectionSettings()
	if err != nil {
		return connectionPlan{}, err
	}
	info := claudeInfoFromSettings(settings, model)
	return connectionPlan{executor: "claude", info: info, claudeSettings: settings}, nil
}

func claudeConnectionInfo(model string) (agent.ConnectionInfo, error) {
	plan, err := claudePlan(model)
	if err != nil {
		return agent.ConnectionInfo{}, err
	}
	return plan.info, nil
}

func claudeInfoFromSettings(settings map[string]any, model string) agent.ConnectionInfo {
	env, _ := settings["env"].(map[string]string)
	resolved := model
	if resolved == "" || resolved == "default" {
		if env["ANTHROPIC_MODEL"] != "" {
			resolved = env["ANTHROPIC_MODEL"]
		} else if configured, ok := settings["model"].(string); ok && configured != "" {
			resolved = configured
		}
	}
	if resolved == "" || resolved == "default" {
		resolved = "cli-default"
	}
	fields := map[string]any{
		"executor":     "claude",
		"model":        resolved,
		"base_url":     env["ANTHROPIC_BASE_URL"],
		"api_key":      env["ANTHROPIC_API_KEY"] != "",
		"auth_token":   env["ANTHROPIC_AUTH_TOKEN"] != "",
		"settings":     settings["model"],
		"effort":       settings["effortLevel"],
		"opus_model":   env["ANTHROPIC_DEFAULT_OPUS_MODEL"],
		"sonnet_model": env["ANTHROPIC_DEFAULT_SONNET_MODEL"],
		"haiku_model":  env["ANTHROPIC_DEFAULT_HAIKU_MODEL"],
	}
	return agent.ConnectionInfo{ResolvedModel: resolved, Identity: connectionDigest(fields)}
}

func codexPlan(model string) (connectionPlan, error) {
	resolved, args, env, err := codexConnectionConfig(model)
	if err != nil {
		return connectionPlan{}, err
	}
	return connectionPlan{executor: "codex", info: codexInfoFromLaunch(resolved, args, env), codexResolved: resolved, codexArgs: args, codexEnv: env}, nil
}

func codexConnectionInfo(model string) (agent.ConnectionInfo, error) {
	plan, err := codexPlan(model)
	if err != nil {
		return agent.ConnectionInfo{}, err
	}
	return plan.info, nil
}

func codexInfoFromLaunch(resolved string, args []string, env map[string]string) agent.ConnectionInfo {
	envKeys := make([]string, 0, len(env))
	for key := range env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	// These are the exact non-secret connection overrides passed to the CLI.
	launchArgs := append([]string{}, args...)
	sort.Strings(launchArgs)
	fields := map[string]any{
		"executor": "codex",
		"model":    resolved,
		"args":     launchArgs,
		"env_keys": envKeys,
	}
	return agent.ConnectionInfo{ResolvedModel: resolved, Identity: connectionDigest(fields)}
}

func geminiPlan(model string) (connectionPlan, error) {
	settingsFile := geminiSettingsFile()
	settingsHash := ""
	var settingsData []byte
	if settingsFile != "" {
		if data, err := os.ReadFile(settingsFile); err == nil && len(data) <= 1024*1024 {
			settingsData = data
			settingsHash = connectionDigest(string(data))
		}
	}
	resolved := model
	if resolved == "" || resolved == "default" {
		for _, key := range []string{"GEMINI_MODEL", "GOOGLE_GENAI_MODEL", "GEMINI_CLI_MODEL"} {
			if value := os.Getenv(key); value != "" {
				resolved = value
				break
			}
		}
	}
	if (resolved == "" || resolved == "default") && len(settingsData) > 0 {
		var config struct {
			Model struct {
				Name string `json:"name"`
			} `json:"model"`
		}
		if json.Unmarshal(settingsData, &config) == nil && config.Model.Name != "" {
			resolved = config.Model.Name
		}
	}
	if resolved == "" || resolved == "default" {
		resolved = "cli-default"
	}
	envKeys := []string{
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL", "GEMINI_CLI_AUTH_OVERRIDE",
		"GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_GENAI_USE_GCA", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
		"GEMINI_MODEL", "GOOGLE_GENAI_MODEL", "GEMINI_CLI_MODEL", "GEMINI_CLI_HOME", "GEMINI_CONFIG_DIR",
	}
	env := map[string]string{}
	fields := map[string]any{"executor": "gemini", "model": resolved, "settings_hash": settingsHash}
	for _, key := range envKeys {
		value := os.Getenv(key)
		env[key] = value
		if key != "GEMINI_API_KEY" && key != "GOOGLE_API_KEY" {
			fields[key] = value
		} else {
			fields[key] = value != ""
		}
	}
	info := agent.ConnectionInfo{ResolvedModel: resolved, Identity: connectionDigest(fields)}
	return connectionPlan{executor: "gemini", info: info, geminiModel: resolved, geminiEnv: env}, nil
}

func geminiConnectionInfo(model string) (agent.ConnectionInfo, error) {
	plan, err := geminiPlan(model)
	if err != nil {
		return agent.ConnectionInfo{}, err
	}
	return plan.info, nil
}

func geminiSettingsFile() string {
	if home := os.Getenv("GEMINI_CLI_HOME"); home != "" {
		return filepath.Join(home, ".gemini", "settings.json")
	}
	if dir := os.Getenv("GEMINI_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".gemini", "settings.json")
	}
	return ""
}

func connectionDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

var _ agent.ConnectionInspector = Provider{}
