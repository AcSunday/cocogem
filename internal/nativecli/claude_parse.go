package nativecli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// claudeEvent is one decoded stream-json frame. The parser validates the frame
// before any handler can turn it into persisted evidence.
type claudeEvent struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	SessionID         string          `json:"session_id"`
	UUID              string          `json:"uuid"`
	IsError           bool            `json:"is_error"`
	Result            string          `json:"result"`
	Errors            []string        `json:"errors"`
	Tools             []string        `json:"tools"`
	TerminalReason    string          `json:"terminal_reason"`
	Message           json.RawMessage `json:"message"`
	Stream            json.RawMessage `json:"event"`
	ToolUseResult     json.RawMessage `json:"tool_use_result"`
	Usage             tokenUsage      `json:"usage"`
	Error             json.RawMessage `json:"error"`
	IsAPIErrorMessage bool            `json:"is_api_error_message"`
}

// claudeParser holds the state one Claude stream invocation accumulates.
type claudeParser struct {
	gate *bridge
	live *liveness

	initialized bool
	finished    bool
	sessionID   string
	// apiErrorCode is the last provider error code reported by the CLI. It is
	// only used to classify a terminal failure, never as progress.
	apiErrorCode string
	anonymous    int

	activity    *activityTracker
	nativeCalls map[string]*nativeCall
	deniedCalls map[string]*deniedCall
	deniedCount int

	// Partial-message deltas are emitted once; the later complete assistant
	// frame is compared against a bounded length/digest window and only its
	// unseen suffix is sent. The visible body is never retained here.
	streamMessageID string
	streams         *textStreams
}

func parseClaude(reader io.Reader, gate *bridge, live *liveness) error {
	p := &claudeParser{
		gate:        gate,
		live:        live,
		activity:    newActivityTracker(),
		nativeCalls: map[string]*nativeCall{},
		deniedCalls: map[string]*deniedCall{},
		streams:     newTextStreams(),
	}
	return p.parse(reader)
}

func (p *claudeParser) parse(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event claudeEvent
		if err := json.Unmarshal(line, &event); err != nil || event.Type == "" {
			return newFailure(FailureProtocol, "native CLI emitted a malformed event")
		}
		errorCode, errorMessage, errorValid := decodeClaudeError(event.Error)
		if !errorValid {
			return newFailure(FailureProtocol, "native CLI emitted a malformed error field")
		}
		if p.finished {
			return newFailure(FailureProtocol, "native CLI emitted data after its terminal result")
		}
		if p.initialized && event.SessionID != "" && event.SessionID != p.sessionID {
			return newFailure(FailureProtocol, "native CLI changed its session identity")
		}
		switch event.Type {
		case "system":
			if err := p.handleSystem(event, errorCode); err != nil {
				return err
			}
		case "assistant":
			if err := p.handleAssistant(event, errorCode); err != nil {
				return err
			}
		case "result":
			if err := p.handleResult(event, errorCode, errorMessage); err != nil {
				return err
			}
		case "error":
			return p.handleError(event, errorCode, errorMessage)
		case "stream_event":
			if err := p.handleStreamEvent(event, line); err != nil {
				return err
			}
		case "user":
			if err := p.handleUser(event); err != nil {
				return err
			}
		default:
			if bytes.Contains(line, []byte(`"tool_use"`)) || bytes.Contains(line, []byte(`"tool_result"`)) || suspiciousToolAction(line) {
				return newFailure(FailureProtocol, "native CLI emitted an unknown tool action")
			}
			if err := p.gate.notice(event.Type, "ignored unknown non-terminal notification"); err != nil {
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
	if !p.finished {
		return newFailure(FailureProtocol, "native CLI ended without a confirmed terminal result")
	}
	return nil
}

func (p *claudeParser) handleSystem(event claudeEvent, errorCode string) error {
	if event.Subtype != "init" {
		if errorCode != "" {
			p.apiErrorCode = errorCode
		}
		p.live.notice("system." + event.Subtype)
		return p.gate.notice("system."+event.Subtype, "ignored non-terminal session notification")
	}
	if p.initialized || !sessionPattern.MatchString(event.SessionID) {
		return newFailure(FailureProtocol, "native CLI session initialization is invalid")
	}
	p.initialized = true
	p.sessionID = event.SessionID
	for _, name := range event.Tools {
		if name == "DeferredToolPlaceholder" {
			continue
		}
		if !p.gate.allowedToolName(name) {
			return newFailure(FailureProtocol, "native CLI exposed a tool outside this step's permissions: %s", safeToken(name))
		}
	}
	if err := p.gate.confirmTools(event.Tools, "mcp__cocogem__"); err != nil {
		return err
	}
	if err := p.gate.requireTools(); err != nil {
		return err
	}
	return p.gate.emit("native_session", map[string]any{"executor": "claude", "session_id": p.sessionID, "mode": "fresh", "cwd": "isolated"})
}

func (p *claudeParser) handleAssistant(event claudeEvent, errorCode string) error {
	if !p.initialized {
		return newFailure(FailureProtocol, "native CLI replied before session initialization")
	}
	if event.IsAPIErrorMessage {
		// The installed CLI emits a synthetic assistant frame carrying the
		// provider error. It is not model output, must not be shown, and the
		// CLI may still retry, so it never refreshes liveness.
		if errorCode != "" {
			p.apiErrorCode = errorCode
		}
		return p.gate.notice("assistant.api_error", "native CLI reported a provider error before a terminal result")
	}
	var message struct {
		ID      string `json:"id"`
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(event.Message, &message) != nil {
		return newFailure(FailureProtocol, "native CLI returned an invalid assistant message")
	}
	item := message.ID
	if item == "" {
		item = event.UUID
	}
	if item == "" {
		// Without a message identity an identical frame cannot be proven to be
		// a replay, so each frame keeps its own state and legal duplicate text
		// is never dropped.
		p.anonymous++
		item = fmt.Sprintf("anonymous-%d", p.anonymous)
	} else if p.activity.duplicate(item, event.Message) {
		return nil
	}
	for index, block := range message.Content {
		switch block.Type {
		case "tool_use":
			if err := p.handleToolUse(item, index, block.ID, block.Name, block.Input); err != nil {
				return err
			}
		case "text":
			if err := p.handleAssistantText(item, index, block.Text); err != nil {
				return err
			}
		case "thinking", "redacted_thinking":
			// Internal reasoning is never shown, recorded or counted without
			// fixture evidence for this adapter.
		default:
			return newFailure(FailureProtocol, "native CLI emitted an unknown assistant content block")
		}
	}
	return nil
}

func (p *claudeParser) handleToolUse(item string, index int, callID, name string, input json.RawMessage) error {
	if !p.gate.allowedToolName(name) {
		// The CLI is the enforced boundary. A tool missing from its inventory
		// is rejected by the CLI with a fixed result; hold a bounded pending
		// denial instead of trusting the model or replaying the call. Input is
		// never retained or emitted.
		safeName := safeToken(name)
		if safeName == "unconfirmed" || callID == "" || len(callID) > 200 {
			return newFailure(FailureProtocol, "native CLI requested an unapproved native tool without a safe identity (%s)", safeName)
		}
		if _, exists := p.nativeCalls[callID]; exists {
			return newFailure(FailureProtocol, "native CLI reused a native tool-call identity")
		}
		if _, exists := p.deniedCalls[callID]; exists {
			return newFailure(FailureProtocol, "native CLI reused a denied tool-call identity")
		}
		if p.deniedCount >= maxClaudeDeniedCalls {
			return newFailure(FailureProtocol, "native CLI requested more than %d denied tools", maxClaudeDeniedCalls)
		}
		p.deniedCount++
		p.deniedCalls[callID] = &deniedCall{name: name}
		return nil
	}
	native := p.gate.nativeAllowed(name)
	if native && callID == "" {
		return newFailure(FailureProtocol, "native CLI requested a native tool without a call identity")
	}
	if callID == "" {
		callID = fmt.Sprintf("%s#%d", name, index)
	}
	if _, exists := p.deniedCalls[callID]; exists {
		return newFailure(FailureProtocol, "native CLI reused a denied tool-call identity")
	}
	if native {
		if _, exists := p.nativeCalls[callID]; exists {
			return newFailure(FailureProtocol, "native CLI reused a native tool-call identity")
		}
		observed := append(json.RawMessage{}, input...)
		if len(observed) == 0 {
			observed = json.RawMessage("{}")
		}
		p.nativeCalls[callID] = &nativeCall{name: name, input: observed}
		if p.activity.newTool(item, callID) {
			p.live.valid("tool_call")
			if err := p.gate.emit("tool_started", map[string]any{"tool_call_id": "cli:" + callID, "name": name, "input": observed, "origin": "native"}); err != nil {
				return err
			}
		}
	} else if p.activity.newTool(item, callID) {
		p.live.valid("tool_call")
	}
	return nil
}

func (p *claudeParser) handleAssistantText(item string, index int, text string) error {
	if text == "" {
		return nil
	}
	key := claudeStreamKey(item, index)
	suffix, streamed, err := p.streams.authoritative(key, text)
	if err != nil {
		return newFailure(FailureProtocol, "native CLI complete assistant frame conflicted with streamed text: %v", err)
	}
	if streamed {
		if suffix == "" {
			return nil
		}
		if strings.TrimSpace(suffix) != "" {
			p.live.valid("text")
		}
		return p.gate.emit("text", map[string]any{"text": suffix, "activity": strings.TrimSpace(suffix) != ""})
	}
	if !p.activity.newBlockText(item, index, text) {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		// Whitespace is formatting, not progress; keep it exactly so block
		// separators are not lost.
		return p.gate.emit("text", map[string]any{"text": text, "activity": false})
	}
	p.live.valid("text")
	return p.gate.emit("text", map[string]any{"text": text + "\n", "activity": true})
}

func (p *claudeParser) handleResult(event claudeEvent, errorCode, errorMessage string) error {
	if err := p.gate.recordUsage(event.Usage); err != nil {
		return err
	}
	terminalReason := event.TerminalReason
	if terminalReason == "" && p.apiErrorCode != "" && (event.IsError || event.Subtype != "success") {
		terminalReason = p.apiErrorCode
	}
	if kind := terminalFailureKind(terminalReason); kind != "" {
		// A broad structured code such as invalid_request may be refined by an
		// explicit overflow detail; specific codes stay authoritative.
		if kind == FailureProtocol && explicitOverflowDetail(event.Result+" "+strings.Join(event.Errors, " ")) {
			kind = FailureContextOverflow
		}
		return confirmedFailure(kind, "native CLI ended with terminal reason %s", safeToken(terminalReason))
	}
	if !p.initialized || event.IsError || event.Subtype != "success" {
		detail := event.Result + " " + strings.Join(event.Errors, " ")
		if errorMessage != "" {
			detail += " " + errorMessage
		}
		return classifiedNativeFailure(terminalReason, event.Subtype, detail)
	}
	if err := p.gate.requireTools(); err != nil {
		return err
	}
	for id, call := range p.nativeCalls {
		if !call.resultSeen {
			return newFailure(FailureProtocol, "native CLI ended with an unresolved native tool call %s", safeToken(id))
		}
	}
	for id, call := range p.deniedCalls {
		if !call.resolved {
			return newFailure(FailureProtocol, "native CLI ended without the fixed denial result for tool %s (call %s)", safeToken(call.name), safeToken(id))
		}
	}
	p.finished = true
	return nil
}

func (p *claudeParser) handleError(event claudeEvent, errorCode, errorMessage string) error {
	if err := p.gate.recordUsage(event.Usage); err != nil {
		return err
	}
	detail := event.Result
	if errorMessage != "" {
		detail += " " + errorMessage
	}
	if len(event.Errors) != 0 {
		detail += " " + strings.Join(event.Errors, " ")
	}
	return classifiedNativeFailure(errorCode, "reported_error", detail)
}

func (p *claudeParser) handleStreamEvent(event claudeEvent, line []byte) error {
	if !p.initialized {
		return newFailure(FailureProtocol, "native CLI replied before session initialization")
	}
	streamEvent, ok := parseClaudeStream(event.Stream)
	if !ok {
		return newFailure(FailureProtocol, "native CLI emitted an invalid stream event")
	}
	switch streamEvent.kind {
	case "message_start":
		if streamEvent.messageID != "" {
			p.streamMessageID = streamEvent.messageID
		}
		return nil
	case "message_stop":
		p.streamMessageID = ""
		return nil
	}
	key := claudeStreamKey(p.streamMessageID, streamEvent.index)
	switch streamEvent.kind {
	case "block_start":
		p.streams.setBlockType(key, streamEvent.blockType)
		if streamEvent.blockType == "text" && streamEvent.text != "" {
			return emitClaudeDelta(streamEvent.text, key, p.streams, p.live, p.gate)
		}
	case "text_delta":
		if p.streams.blockType(key) == "thinking" {
			return nil
		}
		if p.activity.duplicateFrame(event.UUID, line) {
			return nil
		}
		return emitClaudeDelta(streamEvent.text, key, p.streams, p.live, p.gate)
	case "thinking_delta":
		if p.activity.duplicateFrame(event.UUID, line) || strings.TrimSpace(streamEvent.text) == "" {
			return nil
		}
		p.live.valid("reasoning")
		return p.gate.activity(activityReasoning)
	}
	return nil
}

func (p *claudeParser) handleUser(event claudeEvent) error {
	if !p.initialized {
		return newFailure(FailureProtocol, "native CLI returned a tool result before session initialization")
	}
	var message struct {
		Content []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
			Text      string          `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(event.Message, &message) != nil {
		return newFailure(FailureProtocol, "native CLI returned an invalid tool-result message")
	}
	// The CLI's own file range metadata is the only completeness fact the host
	// may preserve; a single result makes attribution exact.
	var fileMetadata json.RawMessage
	if len(event.ToolUseResult) != 0 {
		var result struct {
			File json.RawMessage `json:"file"`
		}
		if json.Unmarshal(event.ToolUseResult, &result) == nil && len(result.File) != 0 && result.File[0] == '{' {
			fileMetadata = append(json.RawMessage{}, result.File...)
		}
	}
	nativeResults := 0
	for _, block := range message.Content {
		if _, ok := p.nativeCalls[block.ToolUseID]; ok && block.Type == "tool_result" {
			nativeResults++
		}
	}
	for _, block := range message.Content {
		switch block.Type {
		case "tool_result":
			if err := p.handleToolResult(block.ToolUseID, block.Content, block.IsError, fileMetadata, nativeResults); err != nil {
				return err
			}
		case "text":
			// Legacy text echoes carry no tool action.
		default:
			return newFailure(FailureProtocol, "native CLI emitted an unknown tool-result content block")
		}
	}
	return nil
}

func (p *claudeParser) handleToolResult(
	callID string,
	raw json.RawMessage,
	isError bool,
	fileMetadata json.RawMessage,
	nativeResults int,
) error {
	if call, denied := p.deniedCalls[callID]; denied {
		if call.resolved {
			return newFailure(FailureProtocol, "native CLI replayed a denied tool result")
		}
		if !isError {
			return newFailure(FailureProtocol, "native CLI reported success for a tool it never offered (%s)", safeToken(call.name))
		}
		text, unsupported := nativeResultText(raw)
		if unsupported || !claudeToolDenial(text, call.name) {
			return newFailure(FailureProtocol, "native CLI denied tool %s with an unrecognized result; the host cannot prove it did not execute", safeToken(call.name))
		}
		call.resolved = true
		return p.gate.emit("tool_finished", map[string]any{
			"tool_call_id": "cli:" + callID,
			"name":         safeToken(call.name),
			"status":       "error",
			"output":       claudeDenialEventText + safeToken(call.name),
			"denied":       true,
			"origin":       "native",
		})
	}
	call, ok := p.nativeCalls[callID]
	if !ok {
		// Host MCP results are owned and recorded by the bridge.
		return nil
	}
	text, unsupported := nativeResultText(raw)
	status := "ok"
	if isError || unsupported {
		status = "error"
	}
	if unsupported {
		text = "native tool returned unsupported non-text content"
	}
	truncated := false
	if len(text) > nativeObservationLimit {
		text, truncated = trimUTF8(text, nativeObservationLimit)
	}
	digest := sha256.Sum256([]byte(status + "\x00" + text))
	if call.resultSeen {
		if call.resultDigest != digest {
			return newFailure(FailureProtocol, "native CLI changed a completed native tool result")
		}
		return nil
	}
	call.resultSeen, call.resultDigest = true, digest
	data := map[string]any{"tool_call_id": "cli:" + callID, "name": call.name, "input": call.input, "status": status, "output": text, "truncated": truncated, "origin": "native"}
	if fileMetadata != nil && nativeResults == 1 {
		data["file"] = fileMetadata
	}
	return p.gate.emit("tool_finished", data)
}

func claudeStreamKey(messageID string, index int) string {
	return fmt.Sprintf("%s#%d", messageID, index)
}
