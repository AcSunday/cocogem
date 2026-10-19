package nativecli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/AcSunday/cocogem/internal/model"
)

func geminiPolicyBoundary() error {
	base := "/etc/gemini-cli"
	if runtime.GOOS == "darwin" {
		base = "/Library/Application Support/GeminiCli"
	}
	paths := []string{filepath.Join(base, "settings.json"), filepath.Join(base, "system-defaults.json"), filepath.Join(base, "policies")}
	for _, key := range []string{"GEMINI_CLI_SYSTEM_SETTINGS_PATH", "GEMINI_CLI_SYSTEM_DEFAULTS_PATH"} {
		if value := os.Getenv(key); value != "" {
			paths = append(paths, value)
		}
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("Gemini 存在系统级设置或策略，当前适配不能确认工具隔离；请使用原生 CLI")
		}
	}
	return nil
}

func prepareGemini(request model.Request, workdir string, gate *bridge) ([]string, map[string]string, error) {
	if err := geminiPolicyBoundary(); err != nil {
		return nil, nil, err
	}
	serverName := "cocogem_" + gate.token[:12]
	gate.nativePrefix = "mcp_" + serverName + "_"
	dir := filepath.Join(workdir, ".gemini")
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, nil, errors.New("create scoped Gemini configuration")
	}
	policyDir := filepath.Join(dir, "policies")
	if err := os.Mkdir(policyDir, 0700); err != nil {
		return nil, nil, errors.New("create scoped Gemini policies")
	}
	// Nonempty --policy replaces the user policy directory; admin rules are authoritative.
	policy := "[[rule]]\ntoolName = \"*\"\ndecision = \"deny\"\npriority = 998\n\n[[rule]]\ntoolName = \"*\"\nmcpName = \"" + serverName + "\"\ndecision = \"allow\"\npriority = 999\n"
	if err := os.WriteFile(filepath.Join(policyDir, "cocogem.toml"), []byte(policy), 0600); err != nil {
		return nil, nil, errors.New("write scoped Gemini policies")
	}
	settings := map[string]any{
		"mcpServers":  map[string]any{serverName: map[string]any{"type": "http", "url": gate.url(), "headers": map[string]string{"Authorization": "Bearer " + gate.token}, "trust": true}},
		"tools":       map[string]any{"core": []string{}, "discoveryCommand": "", "callCommand": ""},
		"hooksConfig": map[string]any{"enabled": false},
		"mcp":         map[string]any{"serverCommand": "", "allowed": []string{serverName}},
		"context":     map[string]any{"fileName": ".cocogem-no-memory-" + gate.token[:12], "includeDirectoryTree": false, "loadMemoryFromIncludeDirectories": false, "memoryBoundaryMarkers": []string{}},
		"general":     map[string]any{"maxAttempts": 1, "retryFetchErrors": false},
		"advanced":    map[string]any{"autoConfigureMemory": false, "ignoreLocalEnv": true},
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), encoded, 0600); err != nil {
		return nil, nil, errors.New("write scoped Gemini settings")
	}
	args := []string{"--output-format", "stream-json", "--approval-mode", "default", "--skip-trust", "--extensions", "none", "--allowed-mcp-server-names", serverName, "--policy", policyDir, "--admin-policy", policyDir, "--prompt", "Read the task on stdin; only use the supplied collaboration MCP tools and complete this step."}
	return args, map[string]string{"GEMINI_CLI_TRUST_WORKSPACE": "true", "GEMINI_SYSTEM_MD": "", "GEMINI_WRITE_SYSTEM_MD": ""}, nil
}

func parseGemini(reader io.Reader, gate *bridge) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	initialized, finished := false, false
	sessionID := ""
	bytesRead, events, textBytes := 0, 0, 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		bytesRead += len(line)
		events++
		if bytesRead > 8*1024*1024 || events > 2048 {
			return errors.New("native CLI event budget exceeded")
		}
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
			Role      string `json:"role"`
			Content   string `json:"content"`
			Status    string `json:"status"`
			ToolName  string `json:"tool_name"`
			Error     struct {
				Message string `json:"message"`
			} `json:"error"`
			Stats tokenUsage `json:"stats"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			return errors.New("malformed native CLI event")
		}
		if finished {
			return errors.New("native CLI emitted data after its terminal result")
		}
		if initialized && event.SessionID != "" && event.SessionID != sessionID {
			return errors.New("native CLI changed its session identity")
		}
		switch event.Type {
		case "init":
			if initialized || !sessionPattern.MatchString(event.SessionID) {
				return errors.New("native CLI session initialization is invalid")
			}
			initialized, sessionID = true, event.SessionID
			if err := gate.emit("native_session", map[string]any{"executor": "gemini", "session_id": sessionID, "mode": "fresh", "cwd": "isolated"}); err != nil {
				return err
			}
		case "message":
			if !initialized {
				return errors.New("native CLI replied before session initialization")
			}
			if event.Role == "assistant" && event.Content != "" {
				textBytes += len(event.Content)
				if textBytes > 256*1024 {
					return errors.New("native CLI visible output exceeds budget")
				}
				if err := gate.emit("text", map[string]any{"text": event.Content}); err != nil {
					return err
				}
			}
		case "tool_use":
			name := strings.TrimPrefix(event.ToolName, gate.nativePrefix)
			if !initialized || gate.nativePrefix == "" || !strings.HasPrefix(event.ToolName, gate.nativePrefix) || gate.schemas[name] == nil {
				return errors.New("native CLI requested an unapproved native tool")
			}
		case "tool_result":
			if !initialized {
				return errors.New("native CLI returned a tool result before initialization")
			}
		case "result":
			if !initialized || event.Status != "success" {
				return nativeFailure("result_error", event.Error.Message)
			}
			finished = true
			if err := gate.recordUsage(event.Stats); err != nil {
				return err
			}
		case "error":
			return nativeFailure("reported_error", event.Error.Message)
		default:
			return errors.New("native CLI emitted an unsupported event")
		}
	}
	if scanner.Err() != nil {
		return errors.New("native CLI output could not be read within budget")
	}
	if !finished {
		return errors.New("native CLI ended without a confirmed terminal result")
	}
	return nil
}
