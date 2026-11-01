package nativecli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AcSunday/cocogem/internal/model"
)

// piAgentDir is the user configuration directory Pi itself reads. A scoped
// copy is written per attempt so user MCP servers, extensions and context
// files never enter the invocation.
func piAgentDir() string {
	if dir := os.Getenv("PI_CODING_AGENT_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// readPiFile keeps a bounded snapshot of one user configuration file.
func readPiFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Pi %s", filepath.Base(path))
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || !json.Valid(data) {
		return nil, fmt.Errorf("Pi %s must be valid JSON within 1 MiB", filepath.Base(path))
	}
	return data, nil
}

// piSettingsForScope removes user resources from the copied settings. Provider,
// model, proxy, retry and thinking defaults stay because they define the
// existing connection; packages, extensions, skills, prompts and themes would
// load external code or content and are disabled for a scoped step.
func piSettingsForScope(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil {
		return raw
	}
	for _, key := range []string{"packages", "extensions", "skills", "prompts", "themes", "defaultProjectTrust", "sessionDir"} {
		delete(settings, key)
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return raw
	}
	return encoded
}

func piProbeModels(probeURL string) []byte {
	encoded, _ := json.Marshal(map[string]any{"providers": map[string]any{"cocogem-probe": map[string]any{
		"name": "Cocogem probe", "baseUrl": probeURL + "/v1", "api": "openai-completions", "apiKey": "cocogem-probe-token",
		"models": []any{map[string]any{"id": "cocogem-probe", "name": "Cocogem probe", "reasoning": false, "input": []string{"text"}, "contextWindow": 128000, "maxTokens": 64}},
	}}})
	return encoded
}

const piProbeSettings = `{"defaultProvider":"cocogem-probe","defaultModel":"cocogem-probe","defaultThinkingLevel":"off"}`

// preparePi writes the scoped agent directory and returns the Pi invocation.
// The bridge is the model's only tool source; Pi's own read/bash/edit/write
// tools stay disabled because they have no deny-rule equivalent that would
// honor the bounded workspace root.
func preparePi(request model.Request, workdir string, gate *bridge, probeURL string, launch *connectionPlan) ([]string, map[string]string, error) {
	var plan connectionPlan
	switch {
	case probeURL != "":
		plan = connectionPlan{piAuth: []byte("{}"), piModels: piProbeModels(probeURL), piSettings: []byte(piProbeSettings)}
	case launch != nil:
		plan = *launch
	default:
		return nil, nil, errors.New("Pi connection was not resolved before launch")
	}
	dir, err := writePiConfig(workdir, plan)
	if err != nil {
		return nil, nil, err
	}
	// Direct exposure declares exactly the host MCP tools to the model. The
	// default codemode exposure would hide them behind an extension script.
	if err := writePiMCP(dir, gate); err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		names = append(names, "mcp__cocogem__"+tool.Name)
	}
	// --no-extensions would also disable the built-in MCP extension, so user
	// extensions are excluded through the scoped agent directory instead.
	args := []string{"--mode", "json", "--print", "--no-session", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--no-builtin-tools"}
	if plan.piModel != "" {
		args = append(args, "--model", plan.piModel, "--thinking", plan.piThinking)
	}
	if len(names) == 0 {
		args = append(args, "--no-tools")
	} else {
		args = append(args, "--tools", strings.Join(names, ","))
	}
	return args, piScopedEnv(dir), nil
}

func writePiMCP(dir string, gate *bridge) error {
	server := map[string]any{"type": "http", "url": gate.url(), "headers": map[string]string{"Authorization": "Bearer " + gate.token}, "exposure": "direct"}
	encoded, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"cocogem": server}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), encoded, 0600); err != nil {
		return errors.New("write scoped Pi MCP configuration")
	}
	return nil
}

type piUsage struct {
	Input  *int `json:"input"`
	Output *int `json:"output"`
}

// Pi reports cumulative usage per assistant response, not per invocation.
type piUsageTotals struct {
	input, output               int
	currentInput, currentOutput int
	inputKnown, outputKnown     bool
	previousKnown               bool
}

func (u *piUsageTotals) next() {
	u.input += u.currentInput
	u.output += u.currentOutput
	u.previousKnown = u.previousKnown && u.inputKnown && u.outputKnown
	u.currentInput, u.currentOutput = 0, 0
	u.inputKnown, u.outputKnown = false, false
}

func (u *piUsageTotals) record(gate *bridge, usage piUsage) error {
	maxInt := int(^uint(0) >> 1)
	if usage.Input != nil {
		if *usage.Input < 0 || *usage.Input > maxInt-u.input {
			return newFailure(FailureProtocol, "native CLI returned invalid token usage")
		}
		u.currentInput, u.inputKnown = *usage.Input, true
	}
	if usage.Output != nil {
		if *usage.Output < 0 || *usage.Output > maxInt-u.output {
			return newFailure(FailureProtocol, "native CLI returned invalid token usage")
		}
		u.currentOutput, u.outputKnown = *usage.Output, true
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	gate.inputTokens, gate.outputTokens = u.input+u.currentInput, u.output+u.currentOutput
	gate.usageKnown = u.previousKnown && u.inputKnown && u.outputKnown
	return nil
}

// parsePi reads Pi's JSONL session events. A run can retry internally; only
// agent_settled, or an explicit retry failure, is terminal. Pi exits 0 after a
// fully failed retry loop, so the exit status alone proves nothing.
func parsePi(reader io.Reader, gate *bridge, live *liveness) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	initialized, finished := false, false
	sessionID := ""
	messageSeq := 0
	usage := piUsageTotals{previousKnown: true}
	sawAssistant, lastStop, lastError := false, "", ""
	activity := newActivityTracker()
	streams := newTextStreams()
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Type                  string          `json:"type"`
			ID                    string          `json:"id"`
			Message               json.RawMessage `json:"message"`
			AssistantMessageEvent json.RawMessage `json:"assistantMessageEvent"`
			Usage                 piUsage         `json:"usage"`
			Aborted               bool            `json:"aborted"`
			Success               bool            `json:"success"`
			FinalError            string          `json:"finalError"`
			ToolName              string          `json:"toolName"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			return newFailure(FailureProtocol, "native CLI emitted a malformed event")
		}
		if finished {
			return newFailure(FailureProtocol, "native CLI emitted data after its terminal result")
		}
		switch event.Type {
		case "session":
			if initialized || !sessionPattern.MatchString(event.ID) {
				return newFailure(FailureProtocol, "native CLI session initialization is invalid")
			}
			initialized, sessionID = true, event.ID
			if err := gate.emit("native_session", map[string]any{"executor": "pi", "session_id": sessionID, "mode": "fresh", "cwd": "isolated"}); err != nil {
				return err
			}
		case "message_start":
			if !initialized {
				return newFailure(FailureProtocol, "native CLI replied before session initialization")
			}
			if role := piMessageRole(event.Message); role == "assistant" {
				if messageSeq > 0 {
					usage.next()
				}
				messageSeq++
				lastStop, lastError = "", ""
				if err := usage.record(gate, piUsage{}); err != nil {
					return err
				}
			}
		case "message_update":
			if !initialized {
				return newFailure(FailureProtocol, "native CLI replied before session initialization")
			}
			if messageSeq == 0 {
				return newFailure(FailureProtocol, "native CLI streamed content before an assistant message")
			}
			if err := usage.record(gate, event.Usage); err != nil {
				return err
			}
			var update struct {
				Type         string `json:"type"`
				ContentIndex int    `json:"contentIndex"`
				Content      string `json:"content"`
				Delta        string `json:"delta"`
				ToolCall     struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"toolCall"`
			}
			if json.Unmarshal(event.AssistantMessageEvent, &update) != nil || update.Type == "" {
				return newFailure(FailureProtocol, "native CLI emitted an invalid stream update")
			}
			key := fmt.Sprintf("assistant-%d#%d", messageSeq, update.ContentIndex)
			switch update.Type {
			case "text_start":
			case "text_delta":
				text, err := streams.delta(key, update.Delta)
				if err != nil {
					return newFailure(FailureProtocol, "native CLI emitted invalid text")
				}
				if err := piText(gate, live, text); err != nil {
					return err
				}
			case "text_end":
				text, streamed, err := streams.authoritative(key, update.Content)
				if err != nil {
					return newFailure(FailureProtocol, "native CLI text conflicted with its stream: %v", err)
				}
				if streamed {
					if err := piText(gate, live, text); err != nil {
						return err
					}
				} else if activity.newBlockText(key, update.ContentIndex, update.Content) {
					if err := piText(gate, live, update.Content); err != nil {
						return err
					}
				}
			case "thinking_start", "thinking_end":
			case "thinking_delta":
				if strings.TrimSpace(update.Delta) != "" {
					live.valid("reasoning")
					if err := gate.activity(activityReasoning); err != nil {
						return err
					}
				}
			case "toolcall_start", "toolcall_delta":
			case "toolcall_end":
				if !gate.allowedToolName(update.ToolCall.Name) {
					// Pi itself rejects an undeclared tool; the bridge never runs it.
					if err := gate.notice("unapproved_tool_call", "the CLI reported a tool call outside this step's inventory"); err != nil {
						return err
					}
					break
				}
				callID := update.ToolCall.ID
				if callID == "" {
					return newFailure(FailureProtocol, "native CLI requested a tool without a call identity")
				}
				if activity.newTool(key, callID) {
					live.valid("tool_call")
				}
			case "start", "done", "error":
			default:
				if strings.Contains(strings.ToLower(update.Type), "tool") {
					return newFailure(FailureProtocol, "native CLI emitted an unknown tool action")
				}
				live.notice(update.Type)
				if err := gate.notice(update.Type, "ignored unknown stream update"); err != nil {
					return err
				}
			}
		case "message_end":
			if !initialized {
				return newFailure(FailureProtocol, "native CLI ended a message before initialization")
			}
			var message struct {
				Role         string  `json:"role"`
				StopReason   string  `json:"stopReason"`
				ErrorMessage string  `json:"errorMessage"`
				Usage        piUsage `json:"usage"`
			}
			if json.Unmarshal(event.Message, &message) != nil {
				return newFailure(FailureProtocol, "native CLI returned an invalid message")
			}
			if message.Role == "assistant" {
				if err := usage.record(gate, message.Usage); err != nil {
					return err
				}
				sawAssistant = true
				lastStop, lastError = message.StopReason, message.ErrorMessage
			}
		case "tool_execution_start":
			if event.ToolName != "" {
				live.valid("tool_call")
			}
		case "tool_execution_end":
		case "auto_retry_start":
			live.notice("auto_retry")
			if err := gate.notice("auto_retry_start", "the CLI is retrying a transient failure"); err != nil {
				return err
			}
		case "auto_retry_end":
			if !event.Success {
				return classifiedNativeFailure("", "retry_exhausted", event.FinalError)
			}
		case "agent_settled":
			if !initialized {
				return newFailure(FailureProtocol, "native CLI finished without a session identity")
			}
			if event.Aborted {
				return classifiedNativeFailure("", "aborted", "native CLI aborted the run")
			}
			if !sawAssistant {
				return newFailure(FailureProtocol, "native CLI finished without an assistant response")
			}
			if lastStop == "error" || lastStop == "aborted" {
				return classifiedNativeFailure(lastError, "message_ended", lastError)
			}
			if lastStop == "length" {
				return confirmedFailure(FailureProtocol, "native CLI reply was truncated at the model output token limit")
			}
			if lastStop != "stop" {
				return newFailure(FailureProtocol, "native CLI did not confirm a completed assistant reply")
			}
			if err := gate.requireTools(); err != nil {
				return err
			}
			finished = true
		case "agent_start", "agent_end", "turn_start", "turn_end", "entry_appended", "session_info_changed", "thinking_level_changed", "queue_update", "compaction_start", "compaction_end", "summarization_retry_scheduled", "summarization_retry_attempt_start", "summarization_retry_finished", "extension_error":
			live.notice(event.Type)
			if err := gate.notice(event.Type, "ignored non-terminal session notification"); err != nil {
				return err
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
			return newFailure(FailureProtocol, "native CLI output frame exceeds the 4 MiB frame limit")
		}
		return newFailure(FailureProtocol, "native CLI output could not be read")
	}
	if !finished {
		return newFailure(FailureProtocol, "native CLI ended without a confirmed terminal result")
	}
	return nil
}

func piMessageRole(raw json.RawMessage) string {
	var message struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &message)
	return message.Role
}

// piText emits one validated text fragment. Whitespace keeps its exact form but
// never counts as progress.
func piText(gate *bridge, live *liveness, text string) error {
	if text == "" {
		return nil
	}
	visible := strings.TrimSpace(text) != ""
	if visible {
		if err := gate.requireTools(); err != nil {
			return err
		}
		live.valid("text")
	}
	return gate.emit("text", map[string]any{"text": text, "activity": visible})
}
