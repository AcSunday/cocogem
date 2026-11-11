package nativecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// streamPiRPC drives Pi's bidirectional RPC mode: one persona prompt on stdin,
// session/assistant/tool events on stdout, terminal only at agent_settled.
//
// Pi's own `--mode json --print` path closes stdin, so it cannot host the private
// extension's approval round-trip; RPC mode keeps the process alive and the
// extension gate blocks each tool call until the host answers.
func streamPiRPC(ctx context.Context, cmd *exec.Cmd, gate *bridge, live *liveness, prompt string) error {
	if err := configureProcess(cmd); err != nil {
		return err
	}
	cmd.Stderr = &boundedBuffer{limit: 8192, truncate: true}
	// The input pipe must exist before the process starts; a StdinPipe created
	// afterwards always fails.
	cmd.Stdin = nil
	input, err := newPiRPCStdin(cmd, prompt)
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		input.close()
		return newFailure(FailureProcess, "open native CLI output")
	}
	if err := cmd.Start(); err != nil {
		input.close()
		return newFailure(FailureProcess, "start native CLI: executable unavailable")
	}
	if live != nil {
		live.dispatched()
	}
	identity, owned, err := inspectOwnership(cmd.Process.Pid, cmd.Path)
	if err != nil {
		terminateProcess(cmd)
		_ = cmd.Wait()
		return newFailure(FailureProcessUncertain, "native CLI process ownership could not be recorded")
	}
	if owned {
		if err := gate.emit("native_process", map[string]any{"pid": identity.PID, "pgid": identity.PGID, "start": identity.Start, "executable": identity.Executable}); err != nil {
			terminateProcess(cmd)
			_ = cmd.Wait()
			_ = convergeProcessGroup(cmd, identity, true)
			return err
		}
	}
	joinCancel := watchCancel(ctx, func() { terminateProcess(cmd) })
	defer input.close()
	if err := input.begin(); err != nil {
		terminateProcess(cmd)
		input.close()
		_ = cmd.Wait()
		_ = convergeProcessGroup(cmd, identity, owned)
		return err
	}
	streamErr := parsePiRPC(stdout, gate, live, input)
	// Pi keeps its RPC stdin open for further commands and will not exit on
	// EOF-less stdin. A confirmed terminal (agent_settled) or any failure ends
	// this private session, so stdin is closed explicitly before Wait; a
	// cancellation is joined the same way.
	input.close()
	if streamErr != nil {
		terminateProcess(cmd)
	}
	waitErr := cmd.Wait()
	joinCancel()
	groupErr := convergeProcessGroup(cmd, identity, owned)
	return streamFailure(ctx.Err(), groupErr, streamErr, waitErr)
}

// piRPCStdin owns Pi's stdin. The session identity is requested first and the
// task prompt is written only after the CLI answers it, so the recorded native
// session cannot be empty and the prompt cannot race the state query.
type piRPCStdin struct {
	mu      sync.Mutex
	stdin   io.WriteCloser
	prompt  string
	sent    bool
	failure error
}

func newPiRPCStdin(cmd *exec.Cmd, prompt string) (*piRPCStdin, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, newFailure(FailureProcess, "open native CLI input")
	}
	return &piRPCStdin{stdin: stdin, prompt: prompt}, nil
}

func (p *piRPCStdin) write(value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return newFailure(FailureHostInputLimit, "the task input could not be encoded for Pi")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	if _, err := p.stdin.Write(append(encoded, '\n')); err != nil {
		p.failure = newFailure(FailureProcess, "Pi input stream is unavailable")
		return p.failure
	}
	return nil
}

// begin asks for the session identity. The prompt follows on that response.
func (p *piRPCStdin) begin() error {
	return p.write(map[string]any{"id": "cocogem-state", "type": "get_state"})
}

// sendPrompt writes the task prompt exactly once.
func (p *piRPCStdin) sendPrompt() error {
	p.mu.Lock()
	sent := p.sent
	p.sent = true
	p.mu.Unlock()
	if sent {
		return nil
	}
	return p.write(map[string]any{"id": "cocogem-prompt", "type": "prompt", "message": p.prompt})
}

func (p *piRPCStdin) close() {
	_ = p.stdin.Close()
}

// parsePiRPC reads Pi's RPC records. Command responses are out-of-band; the run
// is terminal only at agent_settled, and a failed prompt command is a failure.
func parsePiRPC(reader io.Reader, gate *bridge, live *liveness, input *piRPCStdin) error {
	if input == nil {
		return newFailure(FailureProcess, "Pi input stream is unavailable")
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxCLIFrameBytes)
	finished := false
	sessionID := ""
	promptAccepted := false
	messageSeq := 0
	usage := piUsageTotals{previousKnown: true}
	sawAssistant, lastStop, lastError := false, "", ""
	activity := newActivityTracker()
	streams := newTextStreams()
	var reported error
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Type                  string          `json:"type"`
			ID                    string          `json:"id"`
			Command               string          `json:"command"`
			Success               bool            `json:"success"`
			Error                 string          `json:"error"`
			Data                  json.RawMessage `json:"data"`
			Message               json.RawMessage `json:"message"`
			AssistantMessageEvent json.RawMessage `json:"assistantMessageEvent"`
			Usage                 piUsage         `json:"usage"`
			Aborted               bool            `json:"aborted"`
			FinalError            string          `json:"finalError"`
			ToolName              string          `json:"toolName"`
			ToolCallID            string          `json:"toolCallId"`
			IsError               bool            `json:"isError"`
			Result                json.RawMessage `json:"result"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type == "" {
			return newFailure(FailureProtocol, "native CLI emitted a malformed event")
		}
		if finished {
			return newFailure(FailureProtocol, "native CLI emitted data after its terminal result")
		}
		switch event.Type {
		case "response":
			if event.ID == "cocogem-state" {
				if !event.Success {
					// The state query is terminal for this attempt: Pi will not
					// run a prompt without a session identity, so reading more
					// would block on a stdin that stays open.
					return classifiedNativeFailure("", "state_unavailable", event.Error)
				}
				var state struct {
					SessionID string `json:"sessionId"`
				}
				if json.Unmarshal(event.Data, &state) != nil || !sessionPattern.MatchString(state.SessionID) {
					return newFailure(FailureProtocol, "Pi did not report a valid session identity")
				}
				sessionID = state.SessionID
				if err := gate.emit("native_session", map[string]any{
					"executor": "pi", "session_id": sessionID, "mode": "fresh", "cwd": "per-call",
				}); err != nil {
					return err
				}
				if err := input.sendPrompt(); err != nil {
					return err
				}
				continue
			}
			if event.ID != "cocogem-prompt" && event.Command != "prompt" {
				continue
			}
			if !event.Success {
				reported = classifiedNativeFailure("", "prompt_rejected", event.Error)
				// A rejected prompt never starts a run; report it once the stream
				// ends rather than waiting for agent_settled that will not come.
				return reported
			}
			var data struct {
				Disposition string `json:"disposition"`
			}
			_ = json.Unmarshal(event.Data, &data)
			if data.Disposition == "handled" {
				return newFailure(FailureProtocol, "Pi handled the prompt without starting a task run")
			}
			promptAccepted = true
		case "extension_error":
			// A broken gate extension must fail the step, not silently allow.
			return newFailure(FailurePermission, "the Cocogem approval gate failed inside Pi; the step did not continue")
		case "message_start":
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
			key := "assistant-" + itoa(messageSeq) + "#" + itoa(update.ContentIndex)
			switch update.Type {
			case "text_delta":
				text, err := streams.delta(key, update.Delta)
				if err != nil {
					return newFailure(FailureProtocol, "native CLI emitted invalid text")
				}
				if err := piRPCText(gate, live, text); err != nil {
					return err
				}
			case "text_end":
				text, streamed, err := streams.authoritative(key, update.Content)
				if err != nil {
					return newFailure(FailureProtocol, "native CLI text conflicted with its stream: %v", err)
				}
				if streamed {
					if err := piRPCText(gate, live, text); err != nil {
						return err
					}
				} else if activity.newBlockText(key, update.ContentIndex, update.Content) {
					if err := piRPCText(gate, live, update.Content); err != nil {
						return err
					}
				}
			case "thinking_delta":
				if strings.TrimSpace(update.Delta) != "" {
					if err := piReasoning(gate, live); err != nil {
						return err
					}
				}
			case "toolcall_end":
				if !gate.allowedToolName(update.ToolCall.Name) {
					return newFailure(FailureProtocol, "Pi requested a tool outside this step's inventory")
				}
				if update.ToolCall.ID == "" {
					return newFailure(FailureProtocol, "Pi requested a tool without a call identity")
				}
				if activity.newTool(key, update.ToolCall.ID) && live != nil {
					live.valid("tool_call")
				}
			case "text_start", "thinking_start", "thinking_end", "toolcall_start", "toolcall_delta", "start", "done", "error":
			default:
				if strings.Contains(strings.ToLower(update.Type), "tool") {
					return newFailure(FailureProtocol, "native CLI emitted an unknown tool action")
				}
				if err := gate.notice(update.Type, "ignored unknown stream update"); err != nil {
					return err
				}
			}
		case "message_end":
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
			if event.ToolName == "" || event.ToolCallID == "" {
				return newFailure(FailureProtocol, "Pi started a tool without an identity")
			}
			if live != nil {
				live.valid("tool_call")
			}
			if strings.HasPrefix(event.ToolName, "mcp__cocogem__") {
				if !gate.allowedToolName(event.ToolName) {
					return newFailure(FailureProtocol, "Pi started an undeclared host tool")
				}
				break
			}
			input := json.RawMessage(`{}`)
			var params struct {
				Args json.RawMessage `json:"args"`
			}
			_ = json.Unmarshal(line, &params)
			if len(params.Args) != 0 {
				input = params.Args
			}
			if err := gate.emit("tool_started", map[string]any{
				"tool_call_id": nativeToolID("pi", event.ToolCallID), "name": event.ToolName, "input": input,
				"origin": "native", "status": "started",
			}); err != nil {
				return err
			}
		case "tool_execution_end":
			if event.ToolName == "" || event.ToolCallID == "" {
				return newFailure(FailureProtocol, "Pi finished a tool without an identity")
			}
			status := "succeeded"
			if event.IsError {
				status = "failed"
			}
			if live != nil {
				live.valid("tool_result")
			}
			if strings.HasPrefix(event.ToolName, "mcp__cocogem__") {
				if !gate.allowedToolName(event.ToolName) {
					return newFailure(FailureProtocol, "Pi finished an undeclared host tool")
				}
				if event.IsError {
					if err := gate.notice("pi_host_tool_failed", "Pi reported a failed scoped host tool call"); err != nil {
						return err
					}
				}
				break
			}
			// The user must see what the tool actually reported. The CLI's own
			// result is bounded and never replaced with a synthesized success.
			if err := gate.emit("tool_finished", map[string]any{
				"tool_call_id": nativeToolID("pi", event.ToolCallID), "name": event.ToolName, "status": status,
				"origin": "native", "output": piToolResultOutput(event.Result),
			}); err != nil {
				return err
			}
		case "tool_execution_update":
			if event.ToolName == "" || event.ToolCallID == "" {
				return newFailure(FailureProtocol, "Pi updated a tool without an identity")
			}
			if live != nil {
				live.valid("tool_result")
			}
		case "auto_retry_start":
			if live != nil {
				live.notice("auto_retry")
			}
			if err := gate.notice("auto_retry_start", "the CLI is retrying a transient failure"); err != nil {
				return err
			}
		case "auto_retry_end":
			var retry struct {
				Success bool `json:"success"`
			}
			_ = json.Unmarshal(line, &retry)
			if !retry.Success {
				reported = classifiedNativeFailure("", "retry_exhausted", event.FinalError)
			}
		case "agent_settled":
			if !promptAccepted {
				return newFailure(FailureProtocol, "Pi settled without accepting the task prompt")
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
			if reported != nil {
				return reported
			}
			if err := gate.requireTools(); err != nil {
				return err
			}
			finished = true
			// agent_settled is terminal. Pi keeps its stdin open for extension
			// UI answers, so the stream would otherwise block forever; closing
			// stdin after this return is Pi's orderly shutdown.
			return nil
		default:
			if strings.Contains(strings.ToLower(event.Type), "tool") {
				return newFailure(FailureProtocol, "native CLI emitted an unknown tool action: %s", safeToken(event.Type))
			}
			if live != nil {
				live.notice(event.Type)
			}
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

// piReasoning records non-visible progress without leaking content.
func piReasoning(gate *bridge, live *liveness) error {
	if live != nil {
		live.valid("reasoning")
	}
	return gate.activity(activityReasoning)
}

// itoa keeps the message keys stable without importing strconv twice.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	negative := value < 0
	if negative {
		value = -value
	}
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}

// piToolResultOutput renders Pi's own tool result for the event: bounded text
// content plus the structured exit code when present. It never invents output.
func piToolResultOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent struct {
			Output   string `json:"output"`
			ExitCode *int   `json:"exit_code"`
		} `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return truncateUTF8(string(raw), 4096)
	}
	parts := make([]string, 0, len(result.Content)+1)
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if result.StructuredContent.ExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit_code=%d", *result.StructuredContent.ExitCode))
	}
	if len(parts) == 0 && result.StructuredContent.Output != "" {
		parts = append(parts, result.StructuredContent.Output)
	}
	return truncateUTF8(strings.Join(parts, "\n"), 4096)
}

// truncateUTF8 bounds one observation without splitting a rune.
func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && value[limit]&0xC0 == 0x80 {
		limit--
	}
	return value[:limit]
}

// piRPCText emits one validated fragment without requiring a liveness monitor.
// The shared piText helper assumes a live tracker; the RPC path is also used by
// bounded tests and probe harnesses with no tracker, so nil is tolerated here
// instead of panicking (progress accounting is simply skipped).
func piRPCText(gate *bridge, live *liveness, text string) error {
	if text == "" {
		return nil
	}
	visible := strings.TrimSpace(text) != ""
	if visible {
		if err := gate.requireTools(); err != nil {
			return err
		}
		if live != nil {
			live.valid("text")
		}
	}
	return gate.emit("text", map[string]any{"text": text, "activity": visible})
}
