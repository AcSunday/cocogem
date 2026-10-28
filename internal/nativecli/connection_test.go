package nativecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cocogem-native-test-config-")
	if err != nil {
		os.Exit(1)
	}
	os.Setenv("CLAUDE_CONFIG_DIR", dir)
	os.Setenv("CODEX_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestClaudePreservesOnlyConnectionSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	// Keep the assertion independent of the developer's live CLI environment.
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		if value, ok := os.LookupEnv(key); ok {
			_ = os.Unsetenv(key)
			t.Cleanup(func() { _ = os.Setenv(key, value) })
		}
	}
	settings := map[string]any{
		"model": "configured-model", "effortLevel": "high",
		"env":   map[string]string{"ANTHROPIC_AUTH_TOKEN": "test-only-secret", "ANTHROPIC_BASE_URL": "https://example.invalid", "ANTHROPIC_MODEL": "custom-model", "UNSAFE_CUSTOM_SETTING": "not-inherited"},
		"hooks": map[string]any{"PreToolUse": "unsafe command"}, "enabledPlugins": map[string]bool{"unsafe-plugin": true}, "mcpServers": map[string]any{"external": "not-inherited"},
	}
	raw, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := claudeConnectionSettings()
	if err != nil {
		t.Fatal(err)
	}
	env := got["env"].(map[string]string)
	if env["ANTHROPIC_AUTH_TOKEN"] != "test-only-secret" || env["ANTHROPIC_BASE_URL"] != "https://example.invalid" || env["UNSAFE_CUSTOM_SETTING"] != "" {
		t.Fatal("connection allowlist was not preserved")
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "unsafe command") || strings.Contains(string(encoded), "unsafe-plugin") || strings.Contains(string(encoded), "mcpServers") {
		t.Fatal("tool customizations escaped the connection allowlist")
	}
	if got["disableAllHooks"] != true || got["model"] != "configured-model" {
		t.Fatal("safe settings missing")
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "environment-token")
	got, err = claudeConnectionSettings()
	if err != nil || got["env"].(map[string]string)["ANTHROPIC_AUTH_TOKEN"] != "environment-token" {
		t.Fatal("environment precedence changed")
	}
}

func TestClaudeConfigurationErrorsNeverExposeContents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"env":{"ANTHROPIC_AUTH_TOKEN":"private-test-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := claudeConnectionSettings()
	if err == nil || strings.Contains(err.Error(), "private-test-value") {
		t.Fatal("invalid settings leaked content or succeeded")
	}
}

func TestEmptyClaudeCredentialHelperDoesNotBlockNativeAuthentication(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	for _, value := range []string{"null", `""`} {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"apiKeyHelper":`+value+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := claudeConnectionSettings(); err != nil {
			t.Fatal(err)
		}
	}
}
