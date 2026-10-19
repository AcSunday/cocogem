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

	"github.com/AcSunday/cocogem/internal/model"
)

func prepareCodex(request model.Request, workdir string, gate *bridge) ([]string, map[string]string, error) {
	resolved, connectionArgs, env, err := codexConnectionConfig(request.Model)
	if err != nil {
		return nil, nil, err
	}
	entry := map[string]any{
		"slug": resolved, "display_name": resolved, "description": "Scoped collaboration agent", "supported_reasoning_levels": []any{}, "shell_type": "disabled", "visibility": "list", "supported_in_api": true, "priority": 0,
		"support_verbosity": false, "apply_patch_tool_type": nil, "truncation_policy": map[string]any{"mode": "bytes", "limit": 10000}, "experimental_supported_tools": []string{}, "include_skills_usage_instructions": false, "include_apps_usage_instructions": false, "include_plugin_usage_instructions": false,
		"tool_mode": nil, "multi_agent_version": nil, "node_repl_disabled": true, "use_responses_lite": false, "input_modalities": []string{"text"}, "model_messages": map[string]any{"instructions_template": "You are a collaboration agent. Use only the scoped cocogem MCP tools. Follow the supplied task and finish this turn."},
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
	args := []string{"exec", "--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "--ephemeral", "--sandbox", "read-only", "--color", "never"}
	args = append(args, connectionArgs...)
	args = append(args, "--model", resolved)
	for _, config := range []string{"approval_policy=\"never\"", "model_catalog_json=" + quoted(path), "web_search=\"disabled\"", "skills.include_instructions=false", "project_doc_max_bytes=0", "tools.update_plan.enabled=false", "tools.experimental_request_user_input.enabled=false", "mcp_servers.cocogem.url=" + quoted(gate.url()), "mcp_servers.cocogem.bearer_token_env_var=\"COCOGEM_MCP_TOKEN\"", "mcp_servers.cocogem.default_tools_approval_mode=\"auto\""} {
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

func parseCodex(reader io.Reader, gate *bridge) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	initialized, started, finished := false, false, false
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
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Server string `json:"server"`
				Tool   string `json:"tool"`
			} `json:"item"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string     `json:"message"`
			Usage   tokenUsage `json:"usage"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			return errors.New("malformed native CLI event")
		}
		if finished {
			return errors.New("native CLI emitted data after its terminal result")
		}
		switch event.Type {
		case "thread.started":
			if initialized || !sessionPattern.MatchString(event.ThreadID) {
				return errors.New("native CLI session initialization is invalid")
			}
			initialized = true
			if err := gate.emit("native_session", map[string]any{"executor": "codex", "session_id": event.ThreadID, "mode": "fresh", "cwd": "isolated"}); err != nil {
				return err
			}
		case "turn.started":
			if !initialized || started {
				return errors.New("native CLI turn initialization is invalid")
			}
			started = true
		case "item.started", "item.updated", "item.completed":
			if !started {
				return errors.New("native CLI emitted an item outside its turn")
			}
			switch event.Item.Type {
			case "agent_message", "reasoning":
			case "mcp_tool_call":
				if event.Item.Server != "cocogem" || gate.schemas[event.Item.Tool] == nil {
					return errors.New("native CLI requested an unapproved MCP tool")
				}
			default:
				return errors.New("native CLI exposed a capability outside scoped host tools")
			}
			if event.Type == "item.completed" && event.Item.Type == "agent_message" {
				textBytes += len(event.Item.Text)
				if textBytes > 256*1024 {
					return errors.New("native CLI visible output exceeds budget")
				}
				if err := gate.emit("text", map[string]any{"text": event.Item.Text + "\n"}); err != nil {
					return err
				}
			}
		case "turn.completed":
			if !started {
				return errors.New("native CLI terminal result has no active turn")
			}
			finished = true
			if err := gate.recordUsage(event.Usage); err != nil {
				return err
			}
		case "turn.failed":
			return nativeFailure("turn_failed", event.Error.Message)
		case "error":
			return nativeFailure("reported_error", event.Message)
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
