package nativecli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type agyUsage struct {
	InputTokens     *int `json:"input_tokens"`
	OutputTokens    *int `json:"output_tokens"`
	ThinkingTokens  *int `json:"thinking_tokens"`
	CacheReadTokens *int `json:"cache_read_tokens"`
	TotalTokens     *int `json:"total_tokens"`
}

func (u agyUsage) toTokenUsage() tokenUsage {
	return tokenUsage{
		Input:              u.InputTokens,
		Output:             u.OutputTokens,
		CacheReadAlt:       u.CacheReadTokens,
		InputIncludesCache: false,
	}
}

type agyEvent struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	Init           struct {
		ConversationID string `json:"conversation_id"`
		Model          string `json:"model"`
	} `json:"init"`
	StepUpdate *struct {
		StepIndex int       `json:"step_index"`
		State     string    `json:"state"`
		StepType  string    `json:"step_type"`
		ToolName  string    `json:"tool_name"`
		TextDelta string    `json:"text_delta"`
		Usage     *agyUsage `json:"usage"`
		ToolInfo  *struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
			Output     string          `json:"output"`
			Error      *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result *struct {
		ConversationID string    `json:"conversation_id"`
		Status         string    `json:"status"`
		Response       string    `json:"response"`
		Error          string    `json:"error"`
		Usage          *agyUsage `json:"usage"`
	} `json:"result"`
}

type agyTrackedTool struct {
	id       string
	name     string
	input    json.RawMessage
	started  bool
	finished bool
	isHost   bool
}

func isAgyHostMCPCall(name string, params json.RawMessage) bool {
	if name != "call_mcp_tool" {
		return false
	}
	var args struct {
		ServerName string `json:"ServerName"`
	}
	return json.Unmarshal(params, &args) == nil && args.ServerName == "cocogem"
}

func parseAgy(reader io.Reader, gate *bridge, live *liveness) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxCLIFrameBytes)
	activity := newActivityTracker()
	initialized := false
	finished := false
	sessionID := ""
	textStreamed := false
	toolsByStep := map[int]*agyTrackedTool{}

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event agyEvent
		if err := json.Unmarshal(line, &event); err != nil || event.Event == "" {
			return newFailure(FailureProtocol, "native CLI emitted a malformed event")
		}
		if debugNative() {
			fmt.Fprintf(os.Stderr, "native debug: agy event=%q len=%d\n", event.Event, len(line))
		}
		if finished {
			return newFailure(FailureProtocol, "native CLI emitted data after its terminal result")
		}
		switch event.Event {
		case "init":
			id := strings.TrimSpace(event.ConversationID)
			if id == "" {
				id = strings.TrimSpace(event.Init.ConversationID)
			}
			if initialized || !sessionPattern.MatchString(id) {
				return newFailure(FailureProtocol, "native CLI session initialization is invalid")
			}
			initialized = true
			sessionID = id
			live.valid("init")
			if err := gate.acknowledgeSession("agy", sessionID, "scoped"); err != nil {
				return err
			}

		case "step_update":
			if !initialized {
				return newFailure(FailureProtocol, "native CLI replied before session initialization")
			}
			if event.StepUpdate == nil {
				continue
			}
			su := event.StepUpdate
			if su.Usage != nil {
				if err := gate.recordUsage(su.Usage.toTokenUsage()); err != nil {
					return err
				}
			}
			switch su.StepType {
			case "agent_response":
				if su.TextDelta != "" {
					textStreamed = true
					if strings.TrimSpace(su.TextDelta) != "" {
						live.valid("text")
						_ = gate.activity(activityTextDelta)
					}
					if err := gate.emit("text", map[string]any{"text": su.TextDelta, "activity": strings.TrimSpace(su.TextDelta) != ""}); err != nil {
						return err
					}
				} else if su.State == "ACTIVE" {
					live.valid("step_update")
				}

			case "planner_response", "thinking":
				live.valid("thinking")
				_ = gate.activity(activityReasoning)

			case "tool":
				gate.noteTool()
				live.valid("tool")
				name := strings.TrimSpace(su.ToolName)
				var params json.RawMessage
				output := ""
				var toolErrMsg string
				if su.ToolInfo != nil {
					if name == "" {
						name = strings.TrimSpace(su.ToolInfo.Name)
					}
					params = su.ToolInfo.Parameters
					output = su.ToolInfo.Output
					if su.ToolInfo.Error != nil {
						toolErrMsg = strings.TrimSpace(su.ToolInfo.Error.Message)
					}
				}
				tracked := toolsByStep[su.StepIndex]
				if tracked == nil {
					tracked = &agyTrackedTool{
						id:   nativeToolID("agy", fmt.Sprintf("step-%d", su.StepIndex)),
						name: name,
					}
					toolsByStep[su.StepIndex] = tracked
				} else if tracked.name == "" && name != "" {
					tracked.name = name
				}
				if len(params) > 0 {
					tracked.input = append(json.RawMessage{}, params...)
				}
				if isAgyHostMCPCall(tracked.name, tracked.input) {
					tracked.isHost = true
				}
				if tracked.isHost {
					// Host cocogem MCP calls are already emitted by bridge.go.
					if activity.newTool(fmt.Sprintf("%d", su.StepIndex), tracked.id) {
						live.valid("tool_call")
					}
					continue
				}
				if tracked.name == "call_mcp_tool" && len(tracked.input) == 0 && su.State != "DONE" && su.State != "ERROR" {
					// Wait for call_mcp_tool parameters before deciding whether it
					// targets the host cocogem MCP server or an external server.
					continue
				}
				if !tracked.started && tracked.name != "" {
					tracked.started = true
					observedInput := tracked.input
					if len(observedInput) == 0 {
						observedInput = json.RawMessage("{}")
					}
					tracked.input = observedInput
					if err := gate.emit("tool_started", map[string]any{
						"tool_call_id": tracked.id,
						"name":         safeToken(tracked.name),
						"input":        tracked.input,
						"origin":       "native",
					}); err != nil {
						return err
					}
				}
				if !tracked.finished && (su.State == "DONE" || su.State == "ERROR") {
					tracked.finished = true
					status := "ok"
					if su.State == "ERROR" || toolErrMsg != "" {
						status = "error"
					}
					if output == "" && toolErrMsg != "" {
						output = toolErrMsg
					}
					truncated := false
					if len(output) > nativeObservationLimit {
						output, truncated = trimUTF8(output, nativeObservationLimit)
					}
					if err := gate.emit("tool_finished", map[string]any{
						"tool_call_id": tracked.id,
						"name":         safeToken(tracked.name),
						"input":        tracked.input,
						"status":       status,
						"output":       output,
						"truncated":    truncated,
						"origin":       "native",
					}); err != nil {
						return err
					}
				}

			default:
				if su.State == "ACTIVE" || su.State == "DONE" {
					live.valid("step_update")
				}
			}

		case "result":
			if event.Result == nil {
				return newFailure(FailureProtocol, "native CLI emitted an empty result frame")
			}
			res := event.Result
			id := strings.TrimSpace(res.ConversationID)
			if id == "" {
				id = strings.TrimSpace(event.ConversationID)
			}
			if !initialized {
				if !sessionPattern.MatchString(id) {
					return newFailure(FailureProtocol, "native CLI ended without session initialization")
				}
				initialized = true
				sessionID = id
				if err := gate.acknowledgeSession("agy", sessionID, "scoped"); err != nil {
					return err
				}
			} else if id != "" && id != sessionID {
				return newFailure(FailureProtocol, "native CLI changed its session identity")
			}
			if res.Usage != nil {
				if err := gate.recordUsage(res.Usage.toTokenUsage()); err != nil {
					return err
				}
			}
			status := strings.ToUpper(strings.TrimSpace(res.Status))
			if status != "SUCCESS" || strings.TrimSpace(res.Error) != "" {
				finished = true
				detail := strings.TrimSpace(res.Error)
				if detail == "" {
					detail = strings.TrimSpace(res.Response)
				}
				return classifiedNativeFailure("", status, detail)
			}
			if !textStreamed && res.Response != "" {
				if strings.TrimSpace(res.Response) != "" {
					live.valid("text")
				}
				if err := gate.emit("text", map[string]any{"text": res.Response, "activity": strings.TrimSpace(res.Response) != ""}); err != nil {
					return err
				}
			}
			if err := gate.requireTools(); err != nil {
				return err
			}
			finished = true

		default:
			if err := gate.notice("agy."+safeToken(event.Event), "ignored unknown Antigravity stream event"); err != nil {
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
		return newFailure(FailureProtocol, "native CLI ended without a confirmed terminal result")
	}
	return nil
}

func agyStartupFailure(streamErr error, stderr string, outputBytes int64) error {
	var failure *Failure
	if outputBytes != 0 || !errors.As(streamErr, &failure) || failure.Kind != FailureProtocol || failure.Reason != "native CLI ended without a confirmed terminal result" {
		return streamErr
	}
	if idx := strings.LastIndex(stderr, "AGY_ERROR:"); idx >= 0 {
		raw := strings.TrimSpace(stderr[idx+len("AGY_ERROR:"):])
		if lineEnd := strings.IndexByte(raw, '\n'); lineEnd >= 0 {
			raw = raw[:lineEnd]
		}
		var payload struct {
			Status    string `json:"status"`
			Message   string `json:"message"`
			ErrorCode int    `json:"error_code"`
		}
		if json.Unmarshal([]byte(raw), &payload) == nil && (payload.Status != "" || payload.ErrorCode != 0 || payload.Message != "") {
			return classifiedEndpointFailure("", payload.Status, payload.ErrorCode, payload.Status, payload.Message)
		}
	}
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "unauthenticated") || strings.Contains(lower, "login") || strings.Contains(lower, "api key") {
		return newFailure(FailureAuthentication, "Antigravity CLI could not authenticate with the configured credentials")
	}
	return streamErr
}
