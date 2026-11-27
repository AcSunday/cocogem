package nativecli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

func agyConnectionSettings() (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, errors.New("home directory unavailable for Antigravity configuration")
	}
	settings := map[string]any{}
	var configFiles []string
	for _, path := range []string{
		filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"),
		filepath.Join(home, ".gemini", "antigravity", "settings.json"),
		filepath.Join(home, ".agents", "settings.json"),
	} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, fmt.Errorf("read Antigravity settings %s: %w", filepath.Base(path), readErr)
		}
		var parsed map[string]any
		if json.Unmarshal(data, &parsed) != nil || parsed == nil {
			return nil, fmt.Errorf("Antigravity settings file %s is invalid JSON", path)
		}
		for key, value := range parsed {
			settings[key] = value
		}
		sum := sha256.Sum256(data)
		configFiles = append(configFiles, path+":"+hex.EncodeToString(sum[:8]))
	}
	authSources := []string{}
	for _, key := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			authSources = append(authSources, "env:"+key)
		}
	}
	for _, path := range []string{
		filepath.Join(home, ".gemini", "antigravity", "user_settings.pb"),
		filepath.Join(home, ".gemini", "antigravity", "oauth_creds.json"),
		filepath.Join(home, ".gemini", "antigravity-cli"),
		filepath.Join(home, ".gemini", "antigravity"),
	} {
		if info, statErr := os.Stat(path); statErr == nil {
			authSources = append(authSources, fmt.Sprintf("%s:%d", filepath.Base(path), info.ModTime().Unix()))
		}
	}
	if len(configFiles) == 0 && len(authSources) == 0 {
		return nil, errors.New("Antigravity CLI authentication or configuration was not found")
	}
	settings["_cocogem_meta"] = map[string]any{
		"configs": configFiles,
		"auth":    authSources,
		"base":    strings.TrimSpace(os.Getenv("GOOGLE_GEMINI_BASE_URL")),
		"model":   strings.TrimSpace(os.Getenv("AGY_MODEL")),
	}
	return settings, nil
}

func agyPlan(selectedModel string) (connectionPlan, error) {
	settings, err := agyConnectionSettings()
	if err != nil {
		return connectionPlan{}, err
	}
	info := agyInfoFromSettings(settings, selectedModel)
	delete(settings, "_cocogem_meta")
	return connectionPlan{executor: "agy", info: info, agySettings: settings}, nil
}

func agyInfoFromSettings(settings map[string]any, selectedModel string) agent.ConnectionInfo {
	resolved := strings.TrimSpace(selectedModel)
	if resolved == "" || resolved == "default" {
		if envModel := strings.TrimSpace(os.Getenv("AGY_MODEL")); envModel != "" {
			resolved = envModel
		} else if configured, ok := settings["model"].(string); ok && strings.TrimSpace(configured) != "" {
			resolved = strings.TrimSpace(configured)
		}
	}
	if resolved == "" || resolved == "default" {
		resolved = "cli-default"
	}
	fields := map[string]any{
		"executor":       "agy",
		"model":          resolved,
		"model_provider": settings["modelProvider"],
		"effort":         settings["effort"],
		"meta":           settings["_cocogem_meta"],
	}
	return agent.ConnectionInfo{ResolvedModel: resolved, Identity: connectionDigest(fields)}
}

func agyStepSettings(base map[string]any, probeURL string) map[string]any {
	settings := map[string]any{}
	for key, value := range base {
		if key == "_cocogem_meta" {
			continue
		}
		settings[key] = value
	}
	// Disable Antigravity's built-in sandbox-exec wrapper so Cocogem's per-call
	// macOS seatbelt sandbox is never nested inside another sandbox-exec.
	settings["agentMode"] = ""
	settings["enableTerminalSandbox"] = false
	settings["toolPermission"] = "always-proceed"
	if probeURL != "" {
		settings["modelProvider"] = "gemini"
		settings["model"] = "cocogem-probe"
		settings["customModelsConfig"] = map[string]any{
			"customModels": map[string]any{
				"cocogem-probe": map[string]any{
					"modelName":         "gemini-api://cocogem-probe",
					"label":             "cocogem-probe",
					"toolFormatterType": "TOOL_FORMATTER_TYPE_NONE",
				},
			},
		}
	}
	return settings
}

func agyHookScript(brokerURL, token string) string {
	return `#!/bin/sh
# Cocogem native gate for Antigravity CLI. PreToolUse fails closed; PostToolUse
# commits or releases any staged outside-project write.
action="${1:-authorize}"
payload=$(cat)
reply=$(printf '%s' "$payload" | /usr/bin/curl --noproxy 127.0.0.1,localhost --silent --show-error --max-time 3600 \
  -H 'Content-Type: application/json' -H "Authorization: Bearer ` + token + `" \
  --data-binary @- "` + brokerURL + `/gate/agy/$action" 2>/dev/null)
status=$?
if [ "$action" = "finalize" ]; then
  printf '{}\n'
  exit 0
fi
if [ $status -ne 0 ] || [ -z "$reply" ]; then
  printf '%s\n' '{"decision":"deny","reason":"the Cocogem approval gate is unreachable; the tool was not executed"}'
  exit 0
fi
printf '%s\n' "$reply"
`
}

func agyStdinPayload(inputText string) (string, error) {
	encoded, err := json.Marshal(struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}{
		Event: "user",
		Message: struct {
			Content string `json:"content"`
		}{Content: inputText},
	})
	if err != nil {
		return "", err
	}
	return string(encoded) + "\n", nil
}

func prepareAgy(request model.Request, configDir string, gate *bridge, broker *gateBroker, baseSettings map[string]any, probeURL string) ([]string, error) {
	if broker == nil {
		return nil, errors.New("Antigravity CLI requires the native approval gate")
	}
	agentsDir := filepath.Join(configDir, ".agents")
	if err := os.MkdirAll(agentsDir, 0700); err != nil {
		return nil, errors.New("create isolated Antigravity CLI configuration directory")
	}
	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"cocogem": map[string]any{
				"serverUrl": gate.url(),
				"headers":   map[string]string{"Authorization": "Bearer " + gate.token},
			},
		},
	}
	encodedMCP, err := json.Marshal(mcpConfig)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "mcp_config.json"), encodedMCP, 0600); err != nil {
		return nil, errors.New("write isolated Antigravity MCP configuration")
	}

	hookScriptPath := filepath.Join(configDir, "gate-hook.sh")
	if err := os.WriteFile(hookScriptPath, []byte(agyHookScript(broker.url(), broker.token)), 0700); err != nil {
		return nil, errors.New("write isolated Antigravity gate hook")
	}
	hooksConfig := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "/bin/sh " + shellQuote(hookScriptPath) + " authorize",
							"timeout": 3600,
						},
					},
				},
			},
			"PostToolUse": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "/bin/sh " + shellQuote(hookScriptPath) + " finalize",
							"timeout": 3600,
						},
					},
				},
			},
		},
	}
	encodedHooks, err := json.Marshal(hooksConfig)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "hooks.json"), encodedHooks, 0600); err != nil {
		return nil, errors.New("write isolated Antigravity hooks configuration")
	}

	settings := agyStepSettings(baseSettings, probeURL)
	encodedSettings, err := json.Marshal(settings)
	if err != nil {
		return nil, errors.New("encode scoped Antigravity CLI settings")
	}
	settingsPath := filepath.Join(configDir, "settings.json")
	if err := os.WriteFile(settingsPath, encodedSettings, 0600); err != nil {
		return nil, errors.New("write isolated Antigravity CLI settings")
	}

	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--disable-slash-commands",
		"--add-dir", configDir,
		"--settings", settingsPath,
	}
	if request.SessionMode == "resume" {
		if !sessionPattern.MatchString(request.SessionID) {
			return nil, errors.New("native session resume requires a valid UUID session identity")
		}
		args = append(args, "--conversation", request.SessionID)
	}
	if request.ThinkingLevel != "" {
		args = append(args, "--effort", request.ThinkingLevel)
	}
	if probeURL != "" {
		args = append(args, "--model", "cocogem-probe")
	}
	return args, nil
}
