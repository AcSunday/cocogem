package nativecli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// readConnectionConfig never returns file contents in diagnostics.
func readConnectionConfig(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("CLI connection configuration is not a regular file")
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot read local CLI connection configuration")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("CLI connection configuration is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(b) > 1024*1024 {
		return nil, errors.New("CLI connection configuration exceeds its budget")
	}
	return b, nil
}

func claudeConnectionSettings() (map[string]any, error) {
	settings := map[string]any{"disableAllHooks": true, "enabledPlugins": map[string]any{}}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserHomeDir()
		if err != nil {
			return nil, errors.New("cannot locate local Claude connection configuration")
		}
		dir = filepath.Join(base, ".claude")
	}
	allowed := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"}
	env := map[string]string{}
	for _, key := range allowed {
		if value, exists := os.LookupEnv(key); exists {
			env[key] = value
		}
	}
	b, err := readConnectionConfig(filepath.Join(dir, "settings.json"))
	if err != nil {
		return nil, err
	}
	if b == nil {
		settings["env"] = env
		return settings, nil
	}
	var input struct {
		Env          map[string]string `json:"env"`
		Model        string            `json:"model"`
		Effort       string            `json:"effortLevel"`
		APIKeyHelper json.RawMessage   `json:"apiKeyHelper"`
	}
	if json.Unmarshal(b, &input) != nil {
		return nil, errors.New("local Claude settings JSON is invalid")
	}
	for _, key := range allowed {
		if _, exists := env[key]; !exists {
			if value, exists := input.Env[key]; exists {
				env[key] = value
			}
		}
	}
	var helper string
	if len(input.APIKeyHelper) > 0 && json.Unmarshal(input.APIKeyHelper, &helper) != nil {
		return nil, errors.New("local Claude apiKeyHelper configuration is invalid")
	}
	if helper != "" && env["ANTHROPIC_API_KEY"] == "" && env["ANTHROPIC_AUTH_TOKEN"] == "" {
		return nil, errors.New("Claude uses apiKeyHelper; configure a static CLI authentication environment value for the restricted adapter")
	}
	settings["env"] = env
	if input.Model != "" {
		settings["model"] = input.Model
	}
	if input.Effort != "" {
		settings["effortLevel"] = input.Effort
	}
	return settings, nil
}
