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

// Only inference thinking defaults are carried from the user's CLI settings;
// tools, hooks, scripts, MCP servers and other capabilities never enter the
// scoped settings.
type geminiSettings struct {
	Model struct {
		Name string `json:"name"`
	} `json:"model"`
	Security struct {
		Auth struct {
			SelectedType string `json:"selectedType"`
		} `json:"auth"`
	} `json:"security"`
	ModelConfigs struct {
		CustomOverrides []geminiOverride `json:"customOverrides"`
	} `json:"modelConfigs"`
}

type geminiOverride struct {
	Match       map[string]any `json:"match"`
	ModelConfig struct {
		GenerateContentConfig struct {
			ThinkingConfig map[string]any `json:"thinkingConfig"`
		} `json:"generateContentConfig"`
	} `json:"modelConfig"`
}

func readGeminiSettings() geminiSettings {
	file := geminiSettingsFile()
	if file == "" {
		return geminiSettings{}
	}
	data, err := os.ReadFile(file)
	if err != nil || len(data) > 1024*1024 {
		return geminiSettings{}
	}
	var settings geminiSettings
	_ = json.Unmarshal(data, &settings)
	return settings
}

// geminiThinkingOverrides keeps concrete-model thinkingConfig overrides from
// the user settings. Other modelConfigs fields are not forwarded.
func geminiThinkingOverrides(settings geminiSettings) []any {
	overrides := []any{}
	for _, candidate := range settings.ModelConfigs.CustomOverrides {
		model, _ := candidate.Match["model"].(string)
		thinking := map[string]any{}
		for key, value := range candidate.ModelConfig.GenerateContentConfig.ThinkingConfig {
			switch key {
			case "thinkingLevel":
				if _, ok := value.(string); ok {
					thinking[key] = value
				}
			case "thinkingBudget":
				if _, ok := value.(float64); ok {
					thinking[key] = value
				}
			case "includeThoughts":
				if _, ok := value.(bool); ok {
					thinking[key] = value
				}
			}
		}
		if model == "" || len(thinking) == 0 {
			continue
		}
		overrides = append(overrides, map[string]any{
			"match":       map[string]any{"model": model},
			"modelConfig": map[string]any{"generateContentConfig": map[string]any{"thinkingConfig": thinking}},
		})
	}
	return overrides
}

func prepareGemini(request model.Request, workdir string, gate *bridge, launch *connectionPlan) ([]string, map[string]string, error) {
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
	if gate.trustedNative {
		policy = "[[rule]]\ntoolName = \"*\"\ndecision = \"allow\"\npriority = 999\nallowRedirection = true\n"
	}
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
	if request.ThinkingLevel != "" {
		if request.Model == "" || request.Model == "default" {
			return nil, nil, errors.New("Gemini thinking_level requires an explicit model")
		}
		level := strings.ToUpper(request.ThinkingLevel)
		settings["modelConfigs"] = map[string]any{
			"customOverrides": []any{map[string]any{
				"match": map[string]any{"model": request.Model},
				"modelConfig": map[string]any{"generateContentConfig": map[string]any{
					"thinkingConfig": map[string]any{"thinkingLevel": level},
				}},
			}},
		}
	} else {
		var thinking []any
		if launch != nil {
			thinking = launch.geminiThinking
		} else {
			thinking = geminiThinkingOverrides(readGeminiSettings())
		}
		if len(thinking) > 0 {
			settings["modelConfigs"] = map[string]any{"customOverrides": thinking}
		}
	}
	if gate.trustedNative {
		delete(settings, "tools")
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), encoded, 0600); err != nil {
		return nil, nil, errors.New("write scoped Gemini settings")
	}
	args := []string{"--output-format", "stream-json", "--approval-mode", "default", "--skip-trust", "--extensions", "none", "--allowed-mcp-server-names", serverName, "--policy", policyDir, "--admin-policy", policyDir, "--prompt", "Read the task on stdin; only use the supplied collaboration MCP tools and complete this step."}
	if gate.trustedNative {
		for i, value := range args {
			if value == "default" {
				args[i] = "yolo"
			}
		}
		args[len(args)-1] = "Read the authorized task on stdin. Use native tools and Cocogem delivery/collaboration tools to complete it."
	}
	return args, map[string]string{"GEMINI_CLI_TRUST_WORKSPACE": "true", "GEMINI_SYSTEM_MD": "", "GEMINI_WRITE_SYSTEM_MD": ""}, nil
}

func parseGemini(reader io.Reader, gate *bridge, live *liveness) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxCLIFrameBytes)
	initialized, finished := false, false
	var reported error
	sessionID := ""
	// toolIDs maps the CLI's own tool_id to the one canonical identity approved
	// by the host for that exact call. hostIDs records the ids of host MCP calls,
	// whose results keep the original no-op behaviour.
	toolIDs := map[string]string{}
	toolNames := map[string]string{}
	hostIDs := map[string]bool{}
	activity := newActivityTracker()
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Type       string          `json:"type"`
			ID         string          `json:"id"`
			Parameters json.RawMessage `json:"parameters"`
			Output     json.RawMessage `json:"output"`
			SessionID  string          `json:"session_id"`
			Role       string          `json:"role"`
			Content    string          `json:"content"`
			ToolID     string          `json:"tool_id"`
			Status     string          `json:"status"`
			Severity   string          `json:"severity"`
			Message    string          `json:"message"`
			ToolName   string          `json:"tool_name"`
			Error      struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
			Stats tokenUsage `json:"stats"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			return newFailure(FailureProtocol, "native CLI emitted a malformed event")
		}
		if finished {
			return newFailure(FailureProtocol, "native CLI emitted data after its terminal result")
		}
		if initialized && event.SessionID != "" && event.SessionID != sessionID {
			return newFailure(FailureProtocol, "native CLI changed its session identity")
		}
		switch event.Type {
		case "init":
			if initialized || !sessionPattern.MatchString(event.SessionID) {
				return newFailure(FailureProtocol, "native CLI session initialization is invalid")
			}
			initialized, sessionID = true, event.SessionID
			if err := gate.emit("native_session", map[string]any{"executor": "gemini", "session_id": sessionID, "mode": "fresh", "cwd": "isolated"}); err != nil {
				return err
			}
		case "message":
			if !initialized {
				return newFailure(FailureProtocol, "native CLI replied before session initialization")
			}
			// User echoes and empty frames carry no assistant output. Non-empty
			// assistant content is kept even when whitespace-only, because it may
			// be a meaningful delta; only non-whitespace text proves progress.
			// With an identity only an exact frame replay is deduped; without one
			// a retransmission cannot be distinguished, so the text is kept.
			if event.Role != "assistant" || event.Content == "" {
				break
			}
			if event.ID != "" && activity.duplicate(event.ID, line) {
				break
			}
			visible := strings.TrimSpace(event.Content) != ""
			if visible {
				if err := gate.requireTools(); err != nil {
					return err
				}
				live.valid("text")
			}
			if err := gate.emit("text", map[string]any{"text": event.Content, "activity": visible}); err != nil {
				return err
			}
		case "tool_use":
			gate.noteTool()
			if !initialized {
				return newFailure(FailureProtocol, "native CLI requested a tool before session initialization")
			}
			item := event.ID
			if item == "" {
				item = event.ToolName
			}
			if activity.newTool(item, "") {
				live.valid("tool_call")
			}
			// Host MCP tools are validated against this step's declared
			// inventory and produce no native lifecycle here; the bridge emits
			// their tool_started/tool_finished from the host call itself.
			if strings.HasPrefix(event.ToolName, gate.nativePrefix) {
				name := strings.TrimPrefix(event.ToolName, gate.nativePrefix)
				if gate.schemas[name] == nil {
					return newFailure(FailureProtocol, "native CLI requested an unapproved host tool: %s", safeToken(event.ToolName))
				}
				// tool_result carries no tool_name, so the classification made
				// here is remembered by id for the result that follows.
				if event.ToolID != "" {
					hostIDs[event.ToolID] = true
				}
				if event.ID != "" {
					hostIDs[event.ID] = true
				}
				break
			}
			// Outside the gated adapter a CLI-native tool outside the MCP server
			// is still a protocol violation (the controlled policy denies it).
			if gateAliasesFor(gate) == nil && !gate.trustedNative {
				return newFailure(FailureProtocol, "native CLI requested an unapproved native tool: %s", safeToken(event.ToolName))
			}
			// The hook payload carries no tool-call id, so both the approval and
			// these events use one derived identity built from what the CLI
			// exposes on both channels: session, tool name and the ORIGINAL
			// input. The rewritten input (sandbox prefix, staged path) is never
			// part of the identity, so the hook and the stream agree.
			id := geminiDerivedCallID(event.ToolName, event.Parameters, sessionID)
			toolNames[id] = safeToken(event.ToolName)
			if toolIDs != nil {
				// Bind the CLI's own tool_id to this canonical identity so the
				// matching tool_result (which carries no parameters) resolves to
				// the same approved call.
				if event.ToolID != "" {
					toolIDs[event.ToolID] = id
				}
				if event.ID != "" {
					toolIDs[event.ID] = id
				}
				toolIDs[id] = id
			}
			if err := gate.emit("tool_started", map[string]any{
				"tool_call_id": id, "name": safeToken(event.ToolName),
				"input": event.Parameters, "origin": "native", "status": "started",
			}); err != nil {
				return err
			}
		case "tool_result":
			gate.noteTool()
			if !initialized {
				return newFailure(FailureProtocol, "native CLI returned a tool result before initialization")
			}
			status := "succeeded"
			if event.Status != "" && event.Status != "success" {
				status = "failed"
			}
			resultID := firstNonEmpty(event.ToolID, event.ID)
			// A host MCP result keeps the original no-op behaviour: the bridge
			// already emitted its own tool events for that call.
			if hostIDs[resultID] || strings.HasPrefix(event.ToolName, gate.nativePrefix) {
				break
			}
			// Outside the gated adapter a native result is not part of the
			// declared tool inventory and needs no native lifecycle.
			if gateAliasesFor(gate) == nil && !gate.trustedNative {
				break
			}
			// A native result carries no parameters, so it resolves through the
			// identity bound at tool_use. An unbound result is a protocol error:
			// guessing an id would split the approval from the effect.
			id := toolIDs[resultID]
			if id == "" {
				return newFailure(FailureProtocol, "native CLI returned a tool result without its tool-call identity")
			}
			if err := gate.emit("tool_finished", map[string]any{
				"tool_call_id": id, "name": toolNames[id],
				"status": status, "origin": "native", "output": geminiToolOutput(event.Output, event.Content),
			}); err != nil {
				return err
			}
		case "result":
			if err := gate.recordUsage(event.Stats); err != nil {
				return err
			}
			if reported != nil {
				return reported
			}
			if !initialized || event.Status != "success" {
				if event.Error.Type == "" && event.Error.Message == "" {
					return confirmedFailure(FailureProtocol, "Gemini reported an unsuccessful result without diagnostic details; this step did not complete")
				}
				if failure := geminiSSEFailure(event.Error.Message); failure != nil {
					return failure
				}
				return classifiedNativeFailure(event.Error.Type, "result_error", event.Error.Message)
			}
			if err := gate.requireTools(); err != nil {
				return err
			}
			finished = true
		case "error":
			if err := gate.recordUsage(event.Stats); err != nil {
				return err
			}
			if event.Severity == "warning" {
				if err := gate.notice("gemini_warning", "Gemini reported a non-terminal warning; awaiting its final result"); err != nil {
					return err
				}
				continue
			}
			detail := event.Error.Message
			if detail == "" {
				detail = event.Message
			}
			if failure := geminiSSEFailure(detail); failure != nil {
				reported = failure
			} else if strings.HasPrefix(detail, "The model returned an empty response") || strings.HasPrefix(detail, "The model returned reasoning thoughts but no final response text") || strings.HasPrefix(detail, "Model response was truncated because it exceeded the token limit") || strings.HasPrefix(detail, "Invalid stream:") {
				reported = confirmedFailure(FailureProtocol, "Gemini returned an empty, truncated or malformed response; this step did not complete")
			} else {
				reported = classifiedNativeFailure(event.Error.Type, "reported_error", detail)
			}
		default:
			if strings.Contains(strings.ToLower(event.Type), "tool") || suspiciousToolAction(line) {
				return newFailure(FailureProtocol, "native CLI emitted an unknown tool action")
			}
			live.notice(event.Type)
			if err := gate.notice(event.Type, "ignored unknown non-terminal notification"); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return newFailure(FailureProtocol, "native CLI output frame exceeds the 16 MiB frame limit")
		}
		return newFailure(FailureProtocol, "native CLI output could not be read")
	}
	if !finished {
		if reported != nil {
			return reported
		}
		return newFailure(FailureProtocol, "native CLI ended without a confirmed terminal result")
	}
	return nil
}

// The compatibility shim emits only these fixed markers, never provider text.
func geminiSSEFailure(detail string) error {
	switch {
	case strings.Contains(detail, "COCOGEM_GEMINI_SSE_ERROR_TRANSIENT"):
		return confirmedFailure(FailureProviderTransient, "Gemini provider returned a temporary server error; this step did not complete")
	case strings.Contains(detail, "COCOGEM_GEMINI_SSE_ERROR_QUOTA"):
		return confirmedFailure(FailureQuota, "Gemini provider reported a quota or rate limit; this step did not complete")
	case strings.Contains(detail, "COCOGEM_GEMINI_SSE_ERROR_AUTHENTICATION"):
		return confirmedFailure(FailureAuthentication, "Gemini provider rejected the existing authentication; this step did not complete")
	case strings.Contains(detail, "COCOGEM_GEMINI_SSE_ERROR_PERMISSION"):
		return confirmedFailure(FailurePermission, "Gemini provider denied this request; this step did not complete")
	case strings.Contains(detail, "COCOGEM_GEMINI_SSE_ERROR_UNKNOWN"):
		return confirmedFailure(FailureUnknown, "Gemini provider returned an unclassified stream error; this step did not complete")
	case strings.Contains(strings.ToLower(detail), "incomplete json segment at the end"):
		return confirmedFailure(FailureProtocol, "Gemini model stream ended with an incomplete event; this step did not complete")
	default:
		return nil
	}
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// geminiToolOutput keeps the CLI's own tool output text for the event; it never
// invents success when the frame carries none.
func geminiToolOutput(raw json.RawMessage, content string) string {
	if len(raw) == 0 {
		return content
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}
