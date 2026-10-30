package nativecli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/model"
)

func prepareCodex(request model.Request, workdir string, gate *bridge, probe bool, launch *connectionPlan) ([]string, map[string]string, error) {
	resolved := request.Model
	connectionArgs := []string{}
	env := map[string]string{}
	switch {
	case probe:
		if resolved == "" || resolved == "default" {
			resolved = "cocogem-probe"
		}
	case launch != nil:
		resolved, connectionArgs, env = launch.codexResolved, launch.codexArgs, launch.codexEnv
	default:
		var err error
		resolved, connectionArgs, env, err = codexConnectionConfig(request.Model)
		if err != nil {
			return nil, nil, err
		}
	}
	entry := map[string]any{
		"slug": resolved, "display_name": resolved, "description": "Scoped collaboration agent", "supported_reasoning_levels": []any{}, "shell_type": "disabled", "visibility": "list", "supported_in_api": true, "priority": 0,
		"support_verbosity": false, "apply_patch_tool_type": nil, "truncation_policy": map[string]any{"mode": "bytes", "limit": 10000}, "experimental_supported_tools": []string{}, "include_skills_usage_instructions": false, "include_apps_usage_instructions": false, "include_plugin_usage_instructions": false,
		"tool_mode": nil, "multi_agent_version": nil, "node_repl_disabled": true, "use_responses_lite": false, "input_modalities": []string{"text"}, "model_messages": map[string]any{"instructions_template": "You are a collaboration agent. Use only the tools supplied for this step. Follow the supplied task and finish this turn."},
	}
	encoded, err := json.Marshal(map[string]any{"models": []any{entry}})
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(workdir, "models.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		return nil, nil, errors.New("write scoped Codex model catalog")
	}
	quoted := func(value string) string { raw, _ := json.Marshal(value); return string(raw) }
	nativeWeb := !probe && slices.Contains(request.NativeTools, nativeCodexWeb)
	webSearch := "disabled"
	if nativeWeb {
		webSearch = "live"
	}
	args := []string{"exec", "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "--ephemeral", "--sandbox", "read-only", "--color", "never"}
	args = append(args, connectionArgs...)
	if nativeWeb && slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "model_providers.cocogem_connection.") }) {
		// The scoped provider must declare the Responses web tool; the real
		// Codex probe recorded actual search/open result items with this flag.
		args = append(args, "-c", "model_providers.cocogem_connection.supports_standalone_web_search=true")
	}
	args = append(args, "--model", resolved)
	for _, config := range []string{"approval_policy=\"never\"", "model_catalog_json=" + quoted(path), "web_search=" + quoted(webSearch), "skills.include_instructions=false", "project_doc_max_bytes=0", "tools.update_plan.enabled=false", "tools.experimental_request_user_input.enabled=false", "mcp_servers.cocogem.url=" + quoted(gate.url()), "mcp_servers.cocogem.bearer_token_env_var=\"COCOGEM_MCP_TOKEN\"", "mcp_servers.cocogem.default_tools_approval_mode=\"auto\""} {
		args = append(args, "-c", config)
	}
	for _, feature := range []string{"shell_tool", "view_image", "multi_agent", "multi_agent_v2", "hooks", "plugins", "apps", "code_mode", "code_mode_host", "code_mode_only", "sleep_tool", "token_budget", "request_permissions_tool", "standalone_web_search", "goals"} {
		args = append(args, "-c", "features."+feature+"=false")
	}
	for _, tool := range request.Tools {
		args = append(args, "-c", "mcp_servers.cocogem.tools."+tool.Name+".approval_mode=\"approve\"")
	}
	if env == nil {
		env = map[string]string{}
	}
	env["COCOGEM_MCP_TOKEN"] = gate.token
	return args, env, nil
}

var modelIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,199}$`)

func codexWebInput(query string, raw json.RawMessage) json.RawMessage {
	var action struct {
		Type  string `json:"type"`
		Query string `json:"query"`
		URL   string `json:"url"`
	}
	_ = json.Unmarshal(raw, &action)
	if query == "" {
		query = action.Query
	}
	payload := map[string]any{}
	if query != "" {
		payload["query"] = query
	}
	if action.Type != "" {
		payload["action"] = action.Type
	}
	if action.URL != "" {
		payload["url"] = action.URL
	}
	if len(payload) == 0 {
		return json.RawMessage("{}")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

func codexWebOutput(status string, results, failure json.RawMessage) (string, string, bool) {
	failed := slices.Contains([]string{"failed", "error"}, strings.ToLower(status))
	output := string(bytes.TrimSpace(results))
	if failed {
		if detail := string(bytes.TrimSpace(failure)); detail != "" && detail != "null" {
			output = detail
		}
	}
	if output == "" || output == "null" {
		output = ""
	}
	statusValue := "ok"
	if failed {
		statusValue = "error"
	}
	if len(output) > nativeObservationLimit {
		output, _ = trimUTF8(output, nativeObservationLimit)
		return output, statusValue, true
	}
	return output, statusValue, false
}

func parseCodex(reader io.Reader, gate *bridge, live *liveness) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	initialized, started, finished := false, false, false
	sessionID := ""
	activity := newActivityTracker()
	completed := newActivityTracker()
	finishedItems := map[string]bool{}
	webPending := map[string]bool{}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				ID      string          `json:"id"`
				Type    string          `json:"type"`
				Text    string          `json:"text"`
				Server  string          `json:"server"`
				Tool    string          `json:"tool"`
				Status  string          `json:"status"`
				Query   string          `json:"query"`
				Action  json.RawMessage `json:"action"`
				Results json.RawMessage `json:"results"`
				Error   json.RawMessage `json:"error"`
			} `json:"item"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
			Message string     `json:"message"`
			Usage   tokenUsage `json:"usage"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			return newFailure(FailureProtocol, "native CLI emitted a malformed event")
		}
		if finished {
			return newFailure(FailureProtocol, "native CLI emitted data after its terminal result")
		}
		if initialized && event.ThreadID != "" && event.ThreadID != sessionID {
			return newFailure(FailureProtocol, "native CLI changed its session identity")
		}
		switch event.Type {
		case "thread.started":
			if initialized || !sessionPattern.MatchString(event.ThreadID) {
				return newFailure(FailureProtocol, "native CLI session initialization is invalid")
			}
			initialized, sessionID = true, event.ThreadID
			if err := gate.emit("native_session", map[string]any{"executor": "codex", "session_id": sessionID, "mode": "fresh", "cwd": "isolated"}); err != nil {
				return err
			}
		case "turn.started":
			if !initialized || started {
				return newFailure(FailureProtocol, "native CLI turn initialization is invalid")
			}
			started = true
		case "item.started", "item.updated", "item.completed":
			if !started {
				return newFailure(FailureProtocol, "native CLI emitted an item outside its turn")
			}
			if event.Item.Type == "" || event.Item.ID == "" {
				return newFailure(FailureProtocol, "native CLI emitted an item without a validated type and identity")
			}
			switch event.Item.Type {
			case "agent_message":
				if text := event.Item.Text; strings.TrimSpace(text) != "" && activity.newText(event.Item.ID, text) {
					live.valid("text")
					if err := gate.activity(activityTextDelta); err != nil {
						return err
					}
				}
				if event.Type == "item.completed" && strings.TrimSpace(event.Item.Text) != "" && completed.newText(event.Item.ID, event.Item.Text) {
					if err := gate.requireTools(); err != nil {
						return err
					}
					if err := gate.emit("text", map[string]any{"text": event.Item.Text + "\n", "activity": true}); err != nil {
						return err
					}
				}
			case "reasoning":
				// Reasoning text is metadata for liveness only; its content is
				// never emitted or persisted. The activity signal only tells core
				// to refresh LastProgress and clear a warning.
				if text := event.Item.Text; strings.TrimSpace(text) != "" && activity.newText(event.Item.ID, text) {
					live.valid("reasoning")
					if err := gate.activity(activityReasoning); err != nil {
						return err
					}
				}
			case "plan", "todo_list", "reasoning_summary":
			case "mcp_tool_call":
				if event.Item.Server != "cocogem" || gate.schemas[event.Item.Tool] == nil {
					return newFailure(FailureProtocol, "native CLI requested an unapproved MCP tool: %s", safeToken(event.Item.Tool))
				}
				if activity.newTool(event.Item.ID, "") {
					live.valid("tool_call")
				}
			case "web_search":
				if !gate.nativeAllowed(nativeCodexWeb) {
					return newFailure(FailureProtocol, "native CLI exposed a native web tool outside this step's permissions: %s", safeToken(nativeCodexWeb))
				}
				started := activity.newTool(event.Item.ID, "web_search")
				if event.Type != "item.completed" {
					if started {
						webPending[event.Item.ID] = true
						live.valid("tool_call")
						if err := gate.emit("tool_started", map[string]any{"tool_call_id": "cli:" + event.Item.ID, "name": nativeWebSearch, "input": codexWebInput(event.Item.Query, event.Item.Action), "origin": "native"}); err != nil {
							return err
						}
					}
					break
				}
				if finishedItems[event.Item.ID] {
					break
				}
				finishedItems[event.Item.ID] = true
				delete(webPending, event.Item.ID)
				if started {
					if err := gate.emit("tool_started", map[string]any{"tool_call_id": "cli:" + event.Item.ID, "name": nativeWebSearch, "input": codexWebInput(event.Item.Query, event.Item.Action), "origin": "native"}); err != nil {
						return err
					}
				}
				output, status, truncated := codexWebOutput(event.Item.Status, event.Item.Results, event.Item.Error)
				live.valid("tool_call")
				if err := gate.emit("tool_finished", map[string]any{"tool_call_id": "cli:" + event.Item.ID, "name": nativeWebSearch, "input": codexWebInput(event.Item.Query, event.Item.Action), "status": status, "output": output, "truncated": truncated, "origin": "native"}); err != nil {
					return err
				}
			case "command_execution", "file_change", "local_shell_call", "function_call", "patch_apply", "view_image", "sleep":
				return newFailure(FailureProtocol, "native CLI exposed a native tool action outside the scoped host tools")
			default:
				return newFailure(FailureProtocol, "native CLI emitted an unsupported item type; tool isolation cannot be confirmed")
			}
		case "turn.completed":
			if !started {
				return newFailure(FailureProtocol, "native CLI terminal result has no active turn")
			}
			if err := gate.recordUsage(event.Usage); err != nil {
				return err
			}
			if err := gate.requireTools(); err != nil {
				return err
			}
			if len(webPending) != 0 {
				return newFailure(FailureProtocol, "native CLI ended with an unresolved native web call")
			}
			finished = true
		case "turn.failed":
			if err := gate.recordUsage(event.Usage); err != nil {
				return err
			}
			return classifiedNativeFailure(event.Error.Type, "turn_failed", event.Error.Message)
		case "error":
			if err := gate.recordUsage(event.Usage); err != nil {
				return err
			}
			detail := event.Error.Message
			if detail == "" {
				detail = event.Message
			}
			return classifiedNativeFailure(event.Error.Type, "reported_error", detail)
		default:
			if event.Item.Type != "" || suspiciousToolAction(line) {
				return newFailure(FailureProtocol, "native CLI emitted an unsupported item action")
			}
			live.notice(event.Type)
			if err := gate.notice(event.Type, "ignored unknown non-terminal notification"); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return newFailure(FailureProtocol, "native CLI output frame exceeds the 4 MiB frame limit")
		}
		return newFailure(FailureProtocol, "native CLI output could not be read")
	}
	if !finished {
		return newFailure(FailureProtocol, "native CLI ended without a confirmed terminal result")
	}
	return nil
}
