package nativecli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/AcSunday/cocogem/internal/model"
)

// Gemini's private approval hook. Headless mode runs the whole hook scheduler
// (verified with the installed 0.67.0 binary: BeforeTool can deny, allow or
// rewrite tool_input; AfterTool runs after the effect). ACP mode bypasses it,
// which is why project_autonomous Gemini runs headless with this gate.
//
// The hook must always print a decision: the installed CLI proceeds when a hook
// prints nothing or exits nonzero, so the wrapper answers "deny" whenever the
// host broker cannot be reached. That is the fail-closed property the plain
// "print nothing" shape does not have.
const geminiHookName = "cocogem-native-gate"

func geminiHookScript(brokerURL, token string) string {
	return `#!/bin/sh
# Cocogem native gate. Reads the CLI hook payload on stdin and always prints a
# decision; an unreachable broker is a denial, never a silent allow.
payload=$(cat)
event=$(printf '%s' "$payload" | /usr/bin/grep -o '"hook_event_name"[[:space:]]*:[[:space:]]*"[A-Za-z]*"' | /usr/bin/sed 's/.*"\([A-Za-z]*\)"$/\1/')
case "$event" in
BeforeTool) path=authorize ;;
AfterTool) path=finalize ;;
*) path=authorize ;;
esac
reply=$(printf '%s' "$payload" | /usr/bin/curl --silent --show-error --max-time 3600 \
  -H 'Content-Type: application/json' -H "Authorization: Bearer ` + token + `" \
  --data-binary @- "` + brokerURL + `/gate/gemini/$path" 2>/dev/null)
status=$?
if [ $status -ne 0 ] || [ -z "$reply" ]; then
  printf '%s\n' '{"decision":"deny","reason":"the Cocogem approval gate is unreachable; the tool was not executed"}'
  exit 0
fi
printf '%s\n' "$reply"
`
}

// prepareGeminiNative writes the scoped Gemini configuration with the private
// gate hook and returns the headless invocation. Native tools stay available:
// the hook, not a policy deny rule, decides each call.
func prepareGeminiNative(request model.Request, workdir string, gate *bridge, broker *gateBroker) ([]string, map[string]string, error) {
	if err := geminiPolicyBoundary(); err != nil {
		return nil, nil, err
	}
	serverName := "cocogem_" + gate.token[:12]
	gate.nativePrefix = "mcp_" + serverName + "_"
	dir := filepath.Join(workdir, ".gemini")
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, nil, errors.New("create scoped Gemini configuration")
	}
	hookDir := filepath.Join(workdir, "gate")
	if err := os.MkdirAll(hookDir, 0700); err != nil {
		return nil, nil, errors.New("create scoped Gemini gate directory")
	}
	hookPath := filepath.Join(hookDir, "gemini_gate.sh")
	if err := os.WriteFile(hookPath, []byte(geminiHookScript(broker.url(), broker.token)), 0700); err != nil {
		return nil, nil, errors.New("write scoped Gemini gate hook")
	}
	settings := map[string]any{
		"mcpServers": map[string]any{serverName: map[string]any{
			"type": "http", "url": gate.url(),
			"headers": map[string]string{"Authorization": "Bearer " + gate.token}, "trust": true,
		}},
		"hooks": map[string]any{
			"BeforeTool": []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{
				"type": "command", "command": "/bin/sh " + shellQuote(hookPath), "name": geminiHookName, "timeout": 3600000,
			}}}},
			"AfterTool": []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{
				"type": "command", "command": "/bin/sh " + shellQuote(hookPath), "name": geminiHookName, "timeout": 600000,
			}}}},
		},
		"hooksConfig": map[string]any{"enabled": true},
		"mcp":         map[string]any{"serverCommand": "", "allowed": []string{serverName}},
		"context": map[string]any{"fileName": ".cocogem-no-memory-" + gate.token[:12],
			"includeDirectoryTree": false, "loadMemoryFromIncludeDirectories": false, "memoryBoundaryMarkers": []string{}},
		"general":  map[string]any{"maxAttempts": 1, "retryFetchErrors": false},
		"advanced": map[string]any{"autoConfigureMemory": false, "ignoreLocalEnv": true},
		// The model must never be able to widen its own CLI policy; the host
		// gate stays the only approval channel.
		"security": map[string]any{"disableAlwaysAllow": true},
	}
	if request.ThinkingLevel != "" && request.Model != "" && request.Model != "default" {
		settings["modelConfigs"] = map[string]any{"customOverrides": []any{map[string]any{
			"match": map[string]any{"model": request.Model},
			"modelConfig": map[string]any{"generateContentConfig": map[string]any{
				"thinkingConfig": map[string]any{"thinkingLevel": strings.ToUpper(request.ThinkingLevel)},
			}},
		}}}
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), encoded, 0600); err != nil {
		return nil, nil, errors.New("write scoped Gemini settings")
	}
	// The CLI's own approval mode is irrelevant to safety here: with "default"
	// it does not even declare shell/write tools, and any mode that does would
	// be auto-approved. The private BeforeTool hook is the authority and it is
	// fail-closed (verified: deny blocks the effect in this exact mode).
	args := []string{
		"--output-format", "stream-json", "--approval-mode", "yolo", "--skip-trust",
		"--extensions", "none", "--allowed-mcp-server-names", serverName,
		"--prompt", "Complete the assigned step with the supplied tools. Use the Cocogem tools for member handoff and final file delivery.",
	}
	if request.Model != "" && request.Model != "default" {
		args = append(args, "--model", request.Model)
	}
	// GEMINI_CLI_HOME scopes the CLI's own settings/history directory to this
	// attempt, so the private hook settings are the only ones loaded while the
	// process still starts in the real project root.
	return args, map[string]string{
		"GEMINI_CLI_HOME": workdir, "GEMINI_CONFIG_DIR": workdir,
		"GEMINI_CLI_TRUST_WORKSPACE": "true", "GEMINI_SYSTEM_MD": "", "GEMINI_WRITE_SYSTEM_MD": "",
	}, nil
}

// geminiGateReply renders one broker decision in the CLI's hook response shape.
func geminiGateReply(event string, decision gateDecision) string {
	if event == "AfterTool" {
		return `{"hookSpecificOutput":{"hookEventName":"AfterTool"}}`
	}
	if !decision.Allow {
		reason := decision.Reason
		if reason == "" {
			reason = "the operation was not approved"
		}
		encoded, err := json.Marshal(map[string]any{"decision": "deny", "reason": reason})
		if err != nil {
			return `{"decision":"deny","reason":"the approval decision could not be encoded"}`
		}
		return string(encoded)
	}
	reply := map[string]any{
		"decision": "allow",
		"hookSpecificOutput": map[string]any{
			"hookEventName": "BeforeTool",
			"tool_input":    decision.Input,
		},
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		return `{"decision":"deny","reason":"the approved input could not be encoded"}`
	}
	return string(encoded)
}
