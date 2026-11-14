package nativecli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	// APIErrorStatus is the HTTP status of the provider request that ended the
	// turn; null when the failure was not an HTTP error response.
	APIErrorStatus *int `json:"api_error_status"`
	// Bidirectional control frames. Request/Response are only interpreted when
	// Type names a control frame; the shape is validated before use.
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
}

// claudeParser holds the state one Claude stream invocation accumulates.
type claudeParser struct {
	gate    *bridge
	live    *liveness
	control *claudeControl

	initialized bool
	finished    bool
	sessionID   string
	// apiErrorCode and apiErrorStatus are the last provider error category and
	// HTTP status reported by the CLI. They only classify a terminal failure
	// and are never progress.
	apiErrorCode   string
	apiErrorStatus int
	anonymous      int

	activity    *activityTracker
	nativeCalls map[string]*claudeNativeCall
	deniedCalls map[string]*deniedCall
	deniedCount int

	// Partial-message deltas are emitted once; the later complete assistant
	// frame is compared against a bounded length/digest window and only its
	// unseen suffix is sent. The visible body is never retained here.
	streamMessageID string
	streams         *textStreams
	blocks          claudeBlocks
}

// claudeBlockWindow bounds how many unconfirmed streamed blocks and frame
// positions are remembered. A reply normally has one of each at a time.
const claudeBlockWindow = 64

// claudeBlocks pairs each text block the CLI streamed with the complete frame
// that later confirms it. Stream events carry a block's API content index, but
// the CLI delivers every completed block alone in its own assistant frame, so
// the frame's array position cannot name the block it completes: a reply that
// opens with a thinking block streams its text at index 1 and then repeats it
// at position 0. Blocks are therefore matched by message identity and content,
// in stream order. Only keys are held here, never text.
type claudeBlocks struct {
	// open lists streamed blocks no frame has confirmed yet, oldest first.
	open []claudeBlockRef
	// confirmed maps a frame position to the block it last confirmed.
	confirmed []claudeBlockRef
}

// claudeBlockRef names a recorded block by the message or frame position that
// owns it.
type claudeBlockRef struct {
	owner string
	key   string
}

// streamed notes that key received streamed text for message. A block a frame
// already confirmed is not reopened by a late delta.
func (b *claudeBlocks) streamed(message, key string) {
	for i := len(b.open) - 1; i >= 0; i-- {
		if b.open[i].key == key {
			return
		}
	}
	for _, ref := range b.confirmed {
		if ref.key == key {
			return
		}
	}
	b.open = appendBounded(b.open, claudeBlockRef{owner: message, key: key})
}

// resolve names the recorded block a complete frame's text belongs to. The
// caller reconciles the text against that block, which also reports a frame
// that contradicts what was already shown.
func (b *claudeBlocks) resolve(streams *textStreams, message string, index int, text string) string {
	position := claudeStreamKey(message, index)
	oldest := -1
	for i, ref := range b.open {
		if ref.owner != message {
			continue
		}
		if oldest < 0 {
			oldest = i
		}
		if equal, _ := streams.covers(ref.key, text); equal {
			return b.confirm(i, position)
		}
	}
	// A stream that stopped early is finished from its frame.
	for i, ref := range b.open {
		if ref.owner != message {
			continue
		}
		if _, extends := streams.covers(ref.key, text); extends {
			return b.confirm(i, position)
		}
	}
	// The block this position confirmed before, delivered again or grown.
	if key := b.at(position); key != "" {
		if _, extends := streams.covers(key, text); extends {
			return key
		}
	}
	// Streamed text that this frame contradicts.
	if oldest >= 0 {
		return b.open[oldest].key
	}
	// Text that was never streamed is its own block at this position; different
	// text there is the message's next block, not a rewrite of the last one.
	key := "frame:" + position
	if _, extends := streams.covers(key, text); !extends {
		streams.restart(key)
	}
	b.bind(position, key)
	return key
}

func (b *claudeBlocks) confirm(open int, position string) string {
	key := b.open[open].key
	b.open = append(b.open[:open:open], b.open[open+1:]...)
	b.bind(position, key)
	return key
}

func (b *claudeBlocks) at(position string) string {
	for i := len(b.confirmed) - 1; i >= 0; i-- {
		if b.confirmed[i].owner == position {
			return b.confirmed[i].key
		}
	}
	return ""
}

func (b *claudeBlocks) bind(position, key string) {
	for i := range b.confirmed {
		if b.confirmed[i].owner == position {
			b.confirmed = append(b.confirmed[:i:i], b.confirmed[i+1:]...)
			break
		}
	}
	b.confirmed = appendBounded(b.confirmed, claudeBlockRef{owner: position, key: key})
}

// appendBounded keeps the newest claudeBlockWindow entries.
func appendBounded(refs []claudeBlockRef, ref claudeBlockRef) []claudeBlockRef {
	refs = append(refs, ref)
	if len(refs) > claudeBlockWindow {
		refs = append([]claudeBlockRef(nil), refs[len(refs)-claudeBlockWindow:]...)
	}
	return refs
}

func parseClaude(reader io.Reader, gate *bridge, live *liveness) error {
	return parseClaudeWithControl(reader, gate, live, nil)
}

func parseClaudeWithControl(reader io.Reader, gate *bridge, live *liveness, control *claudeControl) error {
	p := &claudeParser{
		gate:        gate,
		live:        live,
		control:     control,
		activity:    newActivityTracker(),
		nativeCalls: map[string]*claudeNativeCall{},
		deniedCalls: map[string]*deniedCall{},
		streams:     newTextStreams(),
	}
	return p.parse(reader)
}

func (p *claudeParser) parse(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxCLIFrameBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event claudeEvent
		if err := json.Unmarshal(line, &event); err != nil || event.Type == "" {
			return newFailure(FailureProtocol, "native CLI emitted a malformed event")
		}
		if debugNative() {
			fmt.Fprintf(os.Stderr, "native debug: frame type=%q subtype=%q len=%d\n", event.Type, event.Subtype, len(line))
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
		case "control_request", "control_response", "control_cancel_request":
			if p.control == nil {
				return newFailure(FailureProtocol, "native CLI emitted an unexpected control frame")
			}
			if err := p.control.handle(p.gate.ctx, event, line); err != nil {
				return err
			}
			continue
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
			return newFailure(FailureProtocol, "native CLI output frame exceeds the 16 MiB frame limit")
		}
		return newFailure(FailureProtocol, "native CLI output could not be read")
	}
	if p.control != nil {
		if err := p.control.failure(); err != nil {
			return err
		}
	}
	if !p.finished {
		return newFailure(FailureProtocol, "native CLI ended without a confirmed terminal result")
	}
	return nil
}

func (p *claudeParser) controlDenied(callID string) bool {
	return p.control != nil && p.control.isDenied(callID)
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
	if p.gate.sessionMode == "resume" && p.sessionID != p.gate.sessionID {
		return newFailure(FailureProtocol, "native CLI resumed a different session identity")
	}
	mode := "fresh"
	if p.gate.sessionMode == "resume" {
		mode = "resume"
	}
	return p.gate.emit("native_session", map[string]any{"executor": "claude", "session_id": p.sessionID, "mode": mode, "cwd": "scoped"})
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
		if event.APIErrorStatus != nil {
			p.apiErrorStatus = *event.APIErrorStatus
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
	p.gate.noteTool()
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
		p.nativeCalls[callID] = &claudeNativeCall{name: name, input: observed}
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
	key := p.blocks.resolve(p.streams, item, index, text)
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
	if p.control != nil {
		// A successful result still leaves a bidirectional CLI waiting for stdin
		// EOF. Close the control stream now; trailing frames are still validated
		// below because the parser keeps reading until the process closes stdout.
		p.control.close()
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
		// The CLI ends such a turn with terminal_reason=api_error for every
		// provider failure, so the cause is in the HTTP status and the error
		// category it reported next to the result.
		status := p.apiErrorStatus
		if event.APIErrorStatus != nil {
			status = *event.APIErrorStatus
		}
		return classifiedEndpointFailure(terminalReason, event.Subtype, status, p.apiErrorCode, detail)
	}
	if err := p.gate.requireTools(); err != nil {
		return err
	}
	for id, call := range p.nativeCalls {
		if !call.resultSeen && !p.controlDenied(id) {
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
		switch streamEvent.blockType {
		case "", "text", "thinking", "redacted_thinking":
		default:
			// Any other block is a tool request in the making.
			p.gate.noteTool()
		}
		p.streams.setBlockType(key, streamEvent.blockType)
		if streamEvent.blockType == "text" && streamEvent.text != "" {
			p.blocks.streamed(p.streamMessageID, key)
			return emitClaudeDelta(streamEvent.text, key, p.streams, p.live, p.gate)
		}
	case "text_delta":
		if p.streams.blockType(key) == "thinking" {
			return nil
		}
		if p.activity.duplicateFrame(event.UUID, line) {
			return nil
		}
		if streamEvent.text != "" {
			p.blocks.streamed(p.streamMessageID, key)
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
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: user frame=%s\n", truncateDebug(event.Message))
	}
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
			p.gate.noteTool()
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
	if p.controlDenied(callID) {
		// The denial event was already emitted before the CLI produced its
		// synthetic error result; do not report the same call twice.
		call.resultSeen = true
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
