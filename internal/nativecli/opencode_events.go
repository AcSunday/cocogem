package nativecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func openCodeRequest(ctx context.Context, client *http.Client, method, endpoint, password string, input any, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return newFailure(FailureProtocol, "cannot encode OpenCode request")
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return newFailure(FailureProtocol, "invalid OpenCode request")
	}
	request.SetBasicAuth("opencode", password)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return newFailure(FailureNetwork, "OpenCode private server request failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return newFailure(FailureProtocol, "OpenCode response exceeded its budget")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nativeFailure(fmt.Sprint(response.StatusCode), string(raw))
	}
	if output != nil && json.Unmarshal(raw, output) != nil {
		return newFailure(FailureProtocol, "OpenCode returned an invalid response")
	}
	return nil
}

func runOpenCodeTurn(ctx context.Context, client *http.Client, endpoint, dir, password, selected, prompt string, gate *bridge, live *liveness) error {
	base, variant, _ := strings.Cut(selected, "#")
	provider, id, ok := strings.Cut(base, "/")
	if !ok || provider == "" || id == "" {
		return newFailure(FailureModelUnavailable, "OpenCode model was not resolved")
	}
	model := map[string]any{"providerID": provider, "id": id}
	if variant != "" {
		model["variant"] = variant
	}
	var session struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	mode := "fresh"
	if gate.sessionMode == "resume" {
		if !openCodeSessionPattern.MatchString(gate.sessionID) {
			return newFailure(FailureProtocol, "managed OpenCode resume identity is invalid")
		}
		// Unlike the CLI --session flag, this read never creates a missing
		// session. A lost cache must not silently become a fresh conversation.
		if err := openCodeRequest(ctx, client, http.MethodGet, endpoint+"/api/session/"+url.PathEscape(gate.sessionID)+"?location[directory]="+url.QueryEscape(dir), password, nil, &session); err != nil {
			return err
		}
		if session.Data.ID != gate.sessionID {
			return newFailure(FailureProtocol, "OpenCode resumed a different session identity")
		}
		mode = "resume"
	} else if err := openCodeRequest(ctx, client, http.MethodPost, endpoint+"/api/session", password, map[string]any{"agent": "cocogem", "title": "Cocogem step", "location": map[string]string{"directory": dir}, "model": model}, &session); err != nil {
		return err
	}
	if !openCodeSessionPattern.MatchString(session.Data.ID) {
		return newFailure(FailureProtocol, "OpenCode returned an invalid session ID")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	handshake := time.AfterFunc(15*time.Second, cancel)
	defer handshake.Stop()
	request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, endpoint+"/api/event", nil)
	if err != nil {
		return newFailure(FailureProtocol, "cannot subscribe to OpenCode events")
	}
	request.SetBasicAuth("opencode", password)
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return newFailure(FailureNetwork, "cannot subscribe to OpenCode events")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return newFailure(FailureProtocol, "OpenCode event subscription was rejected")
	}
	reader := newOpenCodeEvents(response.Body)
	connected, err := reader.next()
	if err != nil || connected.Type != "server.connected" {
		return newFailure(FailureProtocol, "OpenCode did not confirm its event subscription")
	}
	if !handshake.Stop() || streamCtx.Err() != nil {
		return newFailure(FailureProtocol, "OpenCode event subscription timed out")
	}
	live.dispatched()
	if err := openCodeRequest(ctx, client, http.MethodPost, endpoint+"/api/session/"+url.PathEscape(session.Data.ID)+"/prompt", password, map[string]any{"text": prompt}, nil); err != nil {
		return err
	}
	// A resume becomes reusable only after OpenCode accepts its next turn.
	// Reading a stale session alone is not an acknowledgement of this attempt.
	if err := gate.emit("native_session", map[string]any{"executor": "opencode", "session_id": session.Data.ID, "mode": mode, "cwd": "isolated"}); err != nil {
		return err
	}
	return parseOpenCodeEvents(ctx, reader, session.Data.ID, gate, live, client, endpoint, password)
}

type openCodeEventData struct {
	SessionID string                       `json:"sessionID"`
	MessageID string                       `json:"assistantMessageID"`
	Ordinal   *int                         `json:"ordinal"`
	Delta     string                       `json:"delta"`
	Text      string                       `json:"text"`
	ID        string                       `json:"id"`
	Name      string                       `json:"name"`
	Action    string                       `json:"action"`
	Resources []string                     `json:"resources"`
	Message   string                       `json:"message"`
	Finish    string                       `json:"finish"`
	Error     json.RawMessage              `json:"error"`
	Tokens    struct{ Input, Output *int } `json:"tokens"`
}

type openCodeEvent struct {
	ID   string            `json:"id"`
	Type string            `json:"type"`
	Data openCodeEventData `json:"data"`
}

type openCodeEvents struct{ scanner *bufio.Scanner }

func newOpenCodeEvents(reader io.Reader) *openCodeEvents {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxCLIFrameBytes)
	return &openCodeEvents{scanner: scanner}
}

// Bound an entire SSE frame, including multiline data and comment heartbeats.
func (r *openCodeEvents) next() (openCodeEvent, error) {
	var data []byte
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		if len(line) == 0 {
			if len(data) == 0 {
				continue
			}
			var event openCodeEvent
			var envelope struct {
				ID   string          `json:"id"`
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(data, &envelope) != nil || envelope.Type == "" {
				return event, newFailure(FailureProtocol, "OpenCode emitted a malformed event")
			}
			event.ID, event.Type = envelope.ID, envelope.Type
			switch {
			case strings.HasPrefix(event.Type, "session.text."), strings.HasPrefix(event.Type, "session.reasoning."), strings.HasPrefix(event.Type, "session.tool."), strings.HasPrefix(event.Type, "session.step."), strings.HasPrefix(event.Type, "session.execution."), event.Type == "permission.asked":
				if json.Unmarshal(envelope.Data, &event.Data) != nil {
					return event, newFailure(FailureProtocol, "OpenCode emitted malformed execution data")
				}
			}
			return event, nil
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			part := bytes.TrimPrefix(line, []byte("data:"))
			part = bytes.TrimPrefix(part, []byte(" "))
			if len(data)+len(part)+1 > maxCLIFrameBytes {
				return openCodeEvent{}, newFailure(FailureProtocol, "OpenCode event exceeded its budget")
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, part...)
		}
	}
	return openCodeEvent{}, newFailure(FailureProtocol, "OpenCode event stream ended before execution completed")
}

func parseOpenCodeEvents(ctx context.Context, reader *openCodeEvents, session string, gate *bridge, live *liveness, client *http.Client, endpoint, password string) error {
	streams := newTextStreams()
	activity := newActivityTracker()
	blocks := map[string]bool{}
	type toolCall struct{ called bool }
	calls := map[string]toolCall{}
	message, lastFinish := "", ""
	stepOpen := false
	input, output := 0, 0
	known := true
	for {
		event, err := reader.next()
		if err != nil {
			return err
		}
		data := event.Data
		if event.Type == "form.created" {
			return confirmedFailure(FailurePermission, "OpenCode requested interaction outside this step's permissions")
		}
		if data.SessionID != session {
			continue
		}
		if event.Type == "permission.asked" {
			if err := handleOpenCodePermission(ctx, gate, live, client, endpoint, password, session, data); err != nil {
				return err
			}
			continue
		}
		encoded, _ := json.Marshal(event)
		if activity.duplicateFrame(event.ID, encoded) {
			continue
		}
		switch event.Type {
		case "session.step.started":
			if stepOpen || data.MessageID == "" || len(calls) > 0 {
				return newFailure(FailureProtocol, "OpenCode started an invalid step")
			}
			message, stepOpen, lastFinish = data.MessageID, true, ""
			streams, blocks = newTextStreams(), map[string]bool{}
		case "session.text.started", "session.text.delta", "session.text.ended", "session.reasoning.started", "session.reasoning.delta", "session.reasoning.ended":
			if !stepOpen || data.MessageID != message || data.Ordinal == nil || *data.Ordinal < 0 {
				return newFailure(FailureProtocol, "OpenCode streamed content outside its active step")
			}
			kind := "text"
			if strings.HasPrefix(event.Type, "session.reasoning.") {
				kind = "reasoning"
			}
			key := fmt.Sprintf("%s#%d", kind, *data.Ordinal)
			ended, exists := blocks[key]
			if strings.HasSuffix(event.Type, ".started") {
				if exists || len(blocks) >= maxTextStreamStates {
					return newFailure(FailureProtocol, "OpenCode started a duplicate or excessive content block")
				}
				blocks[key] = false
				continue
			}
			if !exists || ended {
				return newFailure(FailureProtocol, "OpenCode updated an unknown or completed content block")
			}
			var text string
			if strings.HasSuffix(event.Type, ".delta") {
				text, err = streams.delta(key, data.Delta)
			} else {
				text, _, err = streams.authoritative(key, data.Text)
				blocks[key] = true
			}
			if err != nil {
				return newFailure(FailureProtocol, "OpenCode final text conflicted with its streamed prefix")
			}
			if text != "" {
				live.valid(kind)
				if kind == "text" {
					err = gate.emit("text", map[string]any{"text": text, "activity": strings.TrimSpace(text) != ""})
				} else {
					err = gate.activity(activityReasoning)
				}
				if err != nil {
					return err
				}
			}
		case "session.tool.input.started":
			name := strings.TrimPrefix(data.Name, "cocogem_")
			if !stepOpen || data.MessageID != message || data.ID == "" || (!gate.trustedNative && !gate.nativeBoundary && (!strings.HasPrefix(data.Name, "cocogem_") || !gate.allowedToolName("mcp__cocogem__"+name))) {
				return newFailure(FailureProtocol, "OpenCode requested an unauthorized tool")
			}
			if _, ok := calls[data.ID]; ok || len(calls) >= maxTextStreamStates {
				return newFailure(FailureProtocol, "OpenCode repeated or exceeded its active tool calls")
			}
			calls[data.ID] = toolCall{}
			if (gate.trustedNative || gate.nativeBoundary) && !strings.HasPrefix(data.Name, "cocogem_") {
				if err := gate.emit("native_action", map[string]any{"name": data.Name, "event": event.Type, "observation": "runner_reported; native_effect_audit=partial"}); err != nil {
					return err
				}
			}
			live.valid("tool_call")
		case "session.tool.called", "session.tool.input.delta", "session.tool.input.ended", "session.tool.progress", "session.tool.success", "session.tool.failed":
			call, ok := calls[data.ID]
			if !ok || !stepOpen || data.MessageID != message {
				return newFailure(FailureProtocol, "OpenCode returned an unmatched tool event")
			}
			switch event.Type {
			case "session.tool.called":
				if call.called {
					return newFailure(FailureProtocol, "OpenCode repeated a tool submission")
				}
				call.called = true
				calls[data.ID] = call
				live.valid("tool_call")
			case "session.tool.success", "session.tool.failed":
				if !call.called {
					return newFailure(FailureProtocol, "OpenCode returned a tool result before its call")
				}
				delete(calls, data.ID)
				live.valid("tool_result")
			}
		case "session.step.ended":
			if !stepOpen || data.MessageID != message || len(calls) > 0 {
				return newFailure(FailureProtocol, "OpenCode ended a step with unsettled tool calls")
			}
			for _, ended := range blocks {
				if !ended {
					return newFailure(FailureProtocol, "OpenCode ended a step with incomplete text")
				}
			}
			if data.Tokens.Input == nil || data.Tokens.Output == nil {
				known = false
			} else {
				maxInt := int(^uint(0) >> 1)
				if *data.Tokens.Input < 0 || *data.Tokens.Output < 0 || *data.Tokens.Input > maxInt-input || *data.Tokens.Output > maxInt-output {
					return newFailure(FailureProtocol, "OpenCode returned invalid token usage")
				}
				input += *data.Tokens.Input
				output += *data.Tokens.Output
			}
			gate.mu.Lock()
			gate.inputTokens, gate.outputTokens, gate.usageKnown = input, output, known
			gate.mu.Unlock()
			if data.Finish != "stop" && data.Finish != "tool-calls" {
				return newFailure(FailureProtocol, "OpenCode did not confirm a complete reply")
			}
			lastFinish, stepOpen = data.Finish, false
		case "session.execution.succeeded":
			if stepOpen || lastFinish != "stop" {
				return newFailure(FailureProtocol, "OpenCode completed with an unfinished step")
			}
			return gate.requireTools()
		case "session.step.failed", "session.execution.failed":
			return nativeFailure("opencode", string(data.Error))
		case "session.execution.interrupted":
			return confirmedFailure(FailureProtocol, "OpenCode execution was interrupted")
		case "form.created":
			return confirmedFailure(FailurePermission, "OpenCode requested interaction outside this step's permissions")
		default:
			if strings.HasPrefix(event.Type, "session.tool.") {
				return newFailure(FailureProtocol, "OpenCode emitted an unknown tool event")
			}
			live.notice(event.Type)
		}
	}
}

// handleOpenCodePermission answers one real permission.asked event through the
// local server API. The host decision is always sent as `once` or `reject`;
// `always` is never used because Cocogem's own conversation grant is the only
// durable authorization.
func handleOpenCodePermission(ctx context.Context, gate *bridge, live *liveness, client *http.Client, endpoint, password, session string, data openCodeEventData) error {
	if data.ID == "" || !strings.HasPrefix(data.ID, "per") {
		return newFailure(FailureProtocol, "OpenCode emitted an invalid permission request identity")
	}
	if gate.approver == nil {
		return confirmedFailure(FailurePermission, "OpenCode requested an interaction without a host approval callback")
	}
	name, input := openCodePermissionCall(data)
	authorization, err := gate.approver.authorize(ctx, "cli:"+data.ID, name, input, data.Message)
	if err != nil {
		return err
	}
	reply := map[string]any{"decision": "reject", "message": authorization.Reason}
	if authorization.Allow {
		reply = map[string]any{"decision": "once"}
	}
	if err := openCodeRequest(ctx, client, http.MethodPost, endpoint+"/api/session/"+url.PathEscape(session)+"/permission/"+url.PathEscape(data.ID)+"/reply", password, reply, nil); err != nil {
		return err
	}
	live.valid("permission")
	return nil
}

func openCodePermissionCall(data openCodeEventData) (string, json.RawMessage) {
	action := strings.ToLower(data.Action)
	resource := ""
	if len(data.Resources) > 0 {
		resource = data.Resources[0]
	}
	switch action {
	case "bash", "shell", "execute", "run":
		encoded, _ := json.Marshal(map[string]string{"command": resource})
		return "Bash", encoded
	case "read":
		encoded, _ := json.Marshal(map[string]string{"path": resource})
		return "Read", encoded
	case "write", "edit", "patch":
		encoded, _ := json.Marshal(map[string]string{"path": resource})
		return "Write", encoded
	case "webfetch", "fetch", "websearch", "web_search":
		encoded, _ := json.Marshal(map[string]string{"url": resource})
		return "WebFetch", encoded
	default:
		encoded, _ := json.Marshal(map[string]any{"resources": data.Resources})
		name := data.Action
		if name == "" {
			name = "Native"
		}
		return name, encoded
	}
}
