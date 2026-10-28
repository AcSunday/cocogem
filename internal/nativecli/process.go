package nativecli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/proc"
)

type boundedBuffer struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int
	truncate bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data.Len()+len(p) > b.limit {
		if b.truncate {
			_, _ = b.data.Write(p[:max(0, b.limit-b.data.Len())])
			return len(p), nil
		}
		return 0, errors.New("CLI output exceeds budget")
	}
	return b.data.Write(p)
}
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func runProbe(ctx context.Context, cmd *exec.Cmd) error {
	if err := configureProcess(cmd); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	joinCancel := watchCancel(ctx, func() { terminateProcess(cmd) })
	defer joinCancel()
	return cmd.Wait()
}

// watchCancel runs stop when ctx is canceled and returns a join that cannot
// return before that callback finishes, so a late signal cannot outlive the
// call and hit a reused process id.
func watchCancel(ctx context.Context, stop func()) func() {
	done := make(chan struct{})
	after := context.AfterFunc(ctx, func() {
		defer close(done)
		stop()
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			if after() {
				close(done)
				return
			}
			<-done
		})
	}
}

// processGroupGrace bounds the polite phase of the best-effort group stop.
// The verdict always comes from checking the group afterwards, not from it.
const processGroupGrace = 250 * time.Millisecond

func streamProcess(ctx context.Context, executor string, cmd *exec.Cmd, gate *bridge, live *liveness) error {
	if err := configureProcess(cmd); err != nil {
		return err
	}
	cmd.Stderr = &boundedBuffer{limit: 8192, truncate: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return newFailure(FailureProcess, "open native CLI output")
	}
	if err := cmd.Start(); err != nil {
		return newFailure(FailureProcess, "start native CLI: executable unavailable")
	}
	if live != nil {
		live.dispatched()
	}
	joinCancel := watchCancel(ctx, func() { terminateProcess(cmd) })
	defer joinCancel()
	identity, owned, err := inspectOwnership(cmd.Process.Pid, cmd.Path)
	if err != nil {
		terminateProcess(cmd)
		_ = cmd.Wait()
		joinCancel()
		if groupErr := convergeProcessGroup(cmd, proc.Identity{}, false); groupErr != nil {
			return errors.Join(groupErr, err)
		}
		return errors.Join(newFailure(FailureProcessUncertain, "native CLI process ownership could not be recorded"), err)
	}
	if owned {
		if err := gate.emit("native_process", map[string]any{"pid": identity.PID, "pgid": identity.PGID, "start": identity.Start, "executable": identity.Executable}); err != nil {
			terminateProcess(cmd)
			_ = cmd.Wait()
			joinCancel()
			if groupErr := convergeProcessGroup(cmd, identity, true); groupErr != nil {
				return errors.Join(groupErr, err)
			}
			return err
		}
	}
	var streamErr error
	switch executor {
	case "codex":
		streamErr = parseCodex(stdout, gate, live)
	case "gemini":
		streamErr = parseGemini(stdout, gate, live)
	default:
		streamErr = parseClaude(stdout, gate, live)
	}
	if streamErr != nil {
		terminateProcess(cmd)
	}
	waitErr := cmd.Wait()
	joinCancel()
	groupErr := convergeProcessGroup(cmd, identity, owned)
	return streamFailure(ctx.Err(), groupErr, streamErr, waitErr)
}

// streamFailure joins the independent results of one stream process. A
// cancellation explains its own wait/stdio noise, so only a confirmed protocol
// failure survives next to ctxErr; an uncancelled wait error becomes a typed
// process failure instead of leaking raw stdio text into the workflow.
func streamFailure(ctxErr, groupErr, streamErr, waitErr error) error {
	if groupErr != nil {
		if ctxErr != nil {
			// The cancellation terminated the process itself; the resulting wait
			// and unconfirmed stdio errors are not independent evidence.
			if confirmedStreamFailure(streamErr) {
				return errors.Join(groupErr, ctxErr, streamErr)
			}
			return errors.Join(groupErr, ctxErr)
		}
		return errors.Join(groupErr, streamErr, waitFailure(waitErr))
	}
	if ctxErr != nil {
		// A cancellation must not mask a failure proven by explicit protocol
		// fields, but EOF/stdio errors it caused stay out of the result.
		if confirmedStreamFailure(streamErr) {
			return errors.Join(ctxErr, streamErr)
		}
		return ctxErr
	}
	if streamErr != nil {
		return streamErr
	}
	return waitFailure(waitErr)
}

func waitFailure(err error) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return newFailure(FailureProcess, "native CLI exited unsuccessfully (code %d); check its connection settings and model availability", exit.ExitCode())
	}
	return newFailure(FailureProcess, "native CLI process did not settle")
}

func confirmedStreamFailure(err error) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.confirmed
}

// convergeProcessGroup proves that no member of the group started by this
// invocation survives. A leader exiting normally proves nothing: a child can
// keep the group after closing the output pipes.
func convergeProcessGroup(cmd *exec.Cmd, identity proc.Identity, recorded bool) error {
	pid := cmd.Process.Pid
	pgid := pid
	if recorded {
		if identity.PGID <= 0 || identity.PGID != pid {
			return newFailure(FailureProcessUncertain, "native CLI process group ownership cannot be verified")
		}
		pgid = identity.PGID
	}
	members, err := proc.GroupMembers(pgid)
	if err != nil {
		return newFailure(FailureProcessUncertain, "native CLI process group state could not be verified")
	}
	if len(members) == 0 {
		return nil
	}
	if recorded {
		// A live pid with a different start time is no longer this attempt;
		// never signal whatever group now happens to use that number.
		if current, err := proc.Inspect(pid); err != nil && !errors.Is(err, proc.ErrNotRunning) {
			return newFailure(FailureProcessUncertain, "native CLI process identity could not be verified")
		} else if err == nil && current.Start != identity.Start {
			return newFailure(FailureProcessUncertain, "native CLI process identity changed before its group could be verified")
		}
	}
	terminateProcess(cmd)
	if !recorded {
		// Setpgid makes the started pid the group id even after the leader exits.
		identity = proc.Identity{PID: pid, PGID: pgid}
	}
	_ = proc.Terminate(identity, processGroupGrace)
	if alive, err := proc.GroupAlive(pgid); err != nil || alive {
		return newFailure(FailureProcessUncertain, "native CLI process group cleanup could not be verified")
	}
	return nil
}

// inspectOwnership reports whether a started process is still owned. A CLI that
// exited before the check cannot leak, so its buffered output and exit status
// must still be read instead of being replaced by an ownership error.
func inspectOwnership(pid int, executable string) (proc.Identity, bool, error) {
	identity, err := proc.Inspect(pid)
	identity.Executable = executable
	if errors.Is(err, proc.ErrNotRunning) {
		return proc.Identity{}, false, nil
	}
	if err != nil {
		return proc.Identity{}, false, err
	}
	return identity, true, nil
}

// activityTracker dedupes validated output by protocol identity without
// retaining text. The message currently being streamed is pinned, so its block
// and tool state cannot be cleared while it is active. Completed messages and
// exact frame identities live in bounded recent caches.
//
// Protocol assumption: each CLI emits updates for the active message followed,
// at most, by exact retransmissions of completed frames; updates are treated
// as append-only snapshots. An exact replay of a completed item is detected
// while it is still in the recent cache. With fixed memory a replay older than
// that cache cannot be distinguished from new output, so it is accepted rather
// than dropped; an absolute guarantee needs a persistent per-run identity index
// (run ID + item UUID + block index + content hash) consulted before emitting.
type activityTracker struct {
	activeID  string
	active    *itemActivity
	recent    map[string]*itemActivity
	order     []string
	head      int
	frames    map[string][32]byte
	frameOrd  []string
	frameHead int
}

type itemActivity struct {
	text   map[int][32]byte
	tools  map[string]bool
	frame  [32]byte
	framed bool
}

const activityItems = 4096

func newActivityTracker() *activityTracker {
	return &activityTracker{recent: map[string]*itemActivity{}, frames: map[string][32]byte{}}
}

func newItemActivity() *itemActivity {
	return &itemActivity{text: map[int][32]byte{}, tools: map[string]bool{}}
}

// item pins the currently streamed identity; switching identity completes the
// previous message into the bounded recent cache.
func (a *activityTracker) item(id string) *itemActivity {
	if a.active != nil && a.activeID == id {
		return a.active
	}
	if state, ok := a.recent[id]; ok {
		return state
	}
	if a.active != nil {
		a.recent[a.activeID] = a.active
		a.order = append(a.order, a.activeID)
		for len(a.recent) > activityItems {
			delete(a.recent, a.order[a.head])
			a.head++
		}
		if a.head > activityItems {
			a.order = append([]string(nil), a.order[a.head:]...)
			a.head = 0
		}
	}
	a.activeID, a.active = id, newItemActivity()
	return a.active
}

// newBlockText reports whether a content block at a stable index carries new
// text. Two identical blocks in one message have distinct indexes and both
// survive; a replayed update repeats the index and the hash.
func (a *activityTracker) newBlockText(item string, index int, text string) bool {
	state := a.item(item)
	hash := sha256.Sum256([]byte(text))
	if seen, ok := state.text[index]; ok && seen == hash {
		return false
	}
	state.text[index] = hash
	return true
}

// newText is the whole-item variant used by adapters whose items expose a
// single mutable text field.
func (a *activityTracker) newText(item, text string) bool {
	return a.newBlockText(item, -1, text)
}

func (a *activityTracker) newTool(item, call string) bool {
	state := a.item(item)
	if state.tools[call] {
		return false
	}
	state.tools[call] = true
	return true
}

// duplicate reports whether this exact protocol frame was already seen for the
// item identity. Different calls or items remain distinct even with equal text.
func (a *activityTracker) duplicate(item string, raw []byte) bool {
	state := a.item(item)
	hash := sha256.Sum256(raw)
	if state.framed && state.frame == hash {
		return true
	}
	state.frame, state.framed = hash, true
	return false
}

// duplicateFrame dedupes top-level stream envelopes by their event identity.
// An empty identity cannot prove a replay and is always accepted.
func (a *activityTracker) duplicateFrame(id string, raw []byte) bool {
	if id == "" {
		return false
	}
	hash := sha256.Sum256(raw)
	if seen, ok := a.frames[id]; ok && seen == hash {
		return true
	}
	if _, ok := a.frames[id]; !ok {
		a.frameOrd = append(a.frameOrd, id)
		for len(a.frames) > activityItems {
			delete(a.frames, a.frameOrd[a.frameHead])
			a.frameHead++
		}
		if a.frameHead > activityItems {
			a.frameOrd = append([]string(nil), a.frameOrd[a.frameHead:]...)
			a.frameHead = 0
		}
	}
	a.frames[id] = hash
	return false
}

func parseClaude(reader io.Reader, gate *bridge, live *liveness) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	initialized, finished := false, false
	sessionID := ""
	anonymous := 0
	activity := newActivityTracker()
	nativeCalls := map[string]*nativeCall{}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Type           string          `json:"type"`
			Subtype        string          `json:"subtype"`
			SessionID      string          `json:"session_id"`
			UUID           string          `json:"uuid"`
			IsError        bool            `json:"is_error"`
			Result         string          `json:"result"`
			Errors         []string        `json:"errors"`
			Tools          []string        `json:"tools"`
			TerminalReason string          `json:"terminal_reason"`
			Message        json.RawMessage `json:"message"`
			Stream         json.RawMessage `json:"event"`
			ToolUseResult  json.RawMessage `json:"tool_use_result"`
			Usage          tokenUsage      `json:"usage"`
			Error          struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &event); err != nil || event.Type == "" {
			return newFailure(FailureProtocol, "native CLI emitted a malformed event")
		}
		if finished {
			return newFailure(FailureProtocol, "native CLI emitted data after its terminal result")
		}
		if initialized && event.SessionID != "" && event.SessionID != sessionID {
			return newFailure(FailureProtocol, "native CLI changed its session identity")
		}
		switch event.Type {
		case "system":
			if event.Subtype == "init" {
				if initialized || !sessionPattern.MatchString(event.SessionID) {
					return newFailure(FailureProtocol, "native CLI session initialization is invalid")
				}
				initialized = true
				sessionID = event.SessionID
				for _, name := range event.Tools {
					if name == "DeferredToolPlaceholder" {
						continue
					}
					if !gate.allowedToolName(name) {
						return newFailure(FailureProtocol, "native CLI exposed a tool outside this step's permissions")
					}
				}
				if err := gate.confirmTools(event.Tools, "mcp__cocogem__"); err != nil {
					return err
				}
				if err := gate.requireTools(); err != nil {
					return err
				}
				if err := gate.emit("native_session", map[string]any{"executor": "claude", "session_id": sessionID, "mode": "fresh", "cwd": "isolated"}); err != nil {
					return err
				}
			} else {
				live.notice("system." + event.Subtype)
				if err := gate.notice("system."+event.Subtype, "ignored non-terminal session notification"); err != nil {
					return err
				}
			}
		case "assistant":
			if !initialized {
				return newFailure(FailureProtocol, "native CLI replied before session initialization")
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
			anonymousFrame := item == ""
			if anonymousFrame {
				// Without a message identity an identical frame cannot be proven
				// to be a replay, so each frame keeps its own state and legal
				// duplicate text is never dropped.
				anonymous++
				item = fmt.Sprintf("anonymous-%d", anonymous)
			} else if activity.duplicate(item, event.Message) {
				continue
			}
			for index, block := range message.Content {
				switch block.Type {
				case "tool_use":
					if !gate.allowedToolName(block.Name) {
						return newFailure(FailureProtocol, "native CLI requested an unapproved native tool")
					}
					callID := block.ID
					native := gate.nativeAllowed(block.Name)
					if native && callID == "" {
						return newFailure(FailureProtocol, "native CLI requested a native tool without a call identity")
					}
					if callID == "" {
						callID = fmt.Sprintf("%s#%d", block.Name, index)
					}
					if native {
						if _, exists := nativeCalls[callID]; exists {
							return newFailure(FailureProtocol, "native CLI reused a native tool-call identity")
						}
						input := append(json.RawMessage{}, block.Input...)
						if len(input) == 0 {
							input = json.RawMessage("{}")
						}
						nativeCalls[callID] = &nativeCall{name: block.Name, input: input}
						if activity.newTool(item, callID) {
							live.valid("tool_call")
							if err := gate.emit("tool_started", map[string]any{"tool_call_id": "cli:" + callID, "name": block.Name, "input": input, "origin": "native"}); err != nil {
								return err
							}
						}
					} else if activity.newTool(item, callID) {
						live.valid("tool_call")
					}
				case "text":
					if block.Text == "" || !activity.newBlockText(item, index, block.Text) {
						continue
					}
					if strings.TrimSpace(block.Text) == "" {
						// Whitespace is formatting, not progress; keep it exactly
						// so block separators are not lost.
						if err := gate.emit("text", map[string]any{"text": block.Text, "activity": false}); err != nil {
							return err
						}
						continue
					}
					live.valid("text")
					if err := gate.emit("text", map[string]any{"text": block.Text + "\n", "activity": true}); err != nil {
						return err
					}
				case "thinking", "redacted_thinking":
					// Internal reasoning is never shown, recorded or counted
					// without fixture evidence for this adapter.
				default:
					return newFailure(FailureProtocol, "native CLI emitted an unknown assistant content block")
				}
			}
		case "result":
			if err := gate.recordUsage(event.Usage); err != nil {
				return err
			}
			if kind := terminalFailureKind(event.TerminalReason); kind != "" {
				// A broad structured code such as invalid_request may be refined
				// by an explicit overflow detail; specific codes stay authoritative.
				if kind == FailureProtocol && explicitOverflowDetail(event.Result+" "+strings.Join(event.Errors, " ")) {
					kind = FailureContextOverflow
				}
				return confirmedFailure(kind, "native CLI ended with terminal reason %s", safeToken(event.TerminalReason))
			}
			if !initialized || event.IsError || event.Subtype != "success" {
				return classifiedNativeFailure(event.TerminalReason, event.Subtype, event.Result+" "+strings.Join(event.Errors, " "))
			}
			if err := gate.requireTools(); err != nil {
				return err
			}
			for id, call := range nativeCalls {
				if !call.resultSeen {
					return newFailure(FailureProtocol, "native CLI ended with an unresolved native tool call %s", safeToken(id))
				}
			}
			finished = true
		case "error":
			if err := gate.recordUsage(event.Usage); err != nil {
				return err
			}
			detail := event.Result
			if event.Error.Message != "" {
				detail += " " + event.Error.Message
			}
			if len(event.Errors) != 0 {
				detail += " " + strings.Join(event.Errors, " ")
			}
			return classifiedNativeFailure(event.Error.Type, "reported_error", detail)
		case "stream_event":
			if !initialized {
				return newFailure(FailureProtocol, "native CLI replied before session initialization")
			}
			kind, text, _, ok := parseClaudeStream(event.Stream)
			if !ok {
				return newFailure(FailureProtocol, "native CLI emitted an invalid stream event")
			}
			if kind == "text_delta" && strings.TrimSpace(text) != "" && !activity.duplicateFrame(event.UUID, line) {
				live.valid("text_delta")
				if err := gate.activity(activityTextDelta); err != nil {
					return err
				}
			}
		case "user":
			if !initialized {
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
			// The CLI's own file range metadata is the only completeness fact
			// the host may preserve; a single result makes attribution exact.
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
				if _, ok := nativeCalls[block.ToolUseID]; ok && block.Type == "tool_result" {
					nativeResults++
				}
			}
			for _, block := range message.Content {
				switch block.Type {
				case "tool_result":
					call, ok := nativeCalls[block.ToolUseID]
					if !ok {
						// Host MCP results are owned and recorded by the bridge.
						continue
					}
					text, unsupported := nativeResultText(block.Content)
					status := "ok"
					if block.IsError || unsupported {
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
						continue
					}
					call.resultSeen, call.resultDigest = true, digest
					data := map[string]any{"tool_call_id": "cli:" + block.ToolUseID, "name": call.name, "input": call.input, "status": status, "output": text, "truncated": truncated, "origin": "native"}
					if fileMetadata != nil && nativeResults == 1 {
						data["file"] = fileMetadata
					}
					if err := gate.emit("tool_finished", data); err != nil {
						return err
					}
				case "text":
					// Legacy text echoes carry no tool action.
				default:
					return newFailure(FailureProtocol, "native CLI emitted an unknown tool-result content block")
				}
			}
		default:
			if bytes.Contains(line, []byte(`"tool_use"`)) || bytes.Contains(line, []byte(`"tool_result"`)) || suspiciousToolAction(line) {
				return newFailure(FailureProtocol, "native CLI emitted an unknown tool action")
			}
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

// parseClaudeStream validates the nested API event; the top-level stream_event
// type alone proves nothing about visible content.
func parseClaudeStream(raw json.RawMessage) (kind, text string, index int, ok bool) {
	if len(raw) == 0 {
		return "", "", 0, false
	}
	var stream struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(raw, &stream) != nil || stream.Type == "" {
		return "", "", 0, false
	}
	if stream.Type == "content_block_delta" && stream.Delta.Type == "text_delta" {
		return "text_delta", stream.Delta.Text, stream.Index, true
	}
	return "", "", stream.Index, true
}

// Streaming CLIs can emit thousands of partial events per research step;
// per-frame bytes stay bounded, but cumulative counts are statistics only.
var sessionPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// suspiciousToolAction keeps unknown protocol extensions from hiding an action.
func suspiciousToolAction(line []byte) bool {
	lower := bytes.ToLower(line)
	if bytes.Contains(lower, []byte(`"mcp__`)) || bytes.Contains(lower, []byte(`"functioncall"`)) {
		return true
	}
	var value any
	if json.Unmarshal(line, &value) != nil {
		return false
	}
	return containsAction(value, "")
}

func containsAction(value any, key string) bool {
	switch current := value.(type) {
	case map[string]any:
		if _, hasName := current["name"].(string); hasName {
			if _, hasInput := current["input"]; hasInput {
				return true
			}
			if _, hasArguments := current["arguments"]; hasArguments {
				return true
			}
			if _, hasParameters := current["parameters"]; hasParameters {
				return true
			}
		}
		for name, child := range current {
			lower := strings.ToLower(name)
			if actionKey(lower) {
				return true
			}
			if lower == "type" {
				if text, ok := child.(string); ok && actionWord(text) {
					return true
				}
			}
			if containsAction(child, lower) {
				return true
			}
		}
	case []any:
		for _, child := range current {
			if containsAction(child, key) {
				return true
			}
		}
	case string:
		if key == "type" && actionWord(current) {
			return true
		}
	}
	return false
}

func actionKey(key string) bool {
	return strings.Contains(key, "tool") || strings.Contains(key, "command") || strings.Contains(key, "function_call") || key == "call_id" || strings.Contains(key, "permission") || strings.Contains(key, "approval") || strings.Contains(key, "sandbox") || strings.Contains(key, "patch") || key == "exec" || key == "shell"
}

func actionWord(word string) bool {
	lower := strings.ToLower(word)
	return strings.Contains(lower, "tool") || strings.Contains(lower, "command") || strings.Contains(lower, "shell") || strings.Contains(lower, "exec") || strings.Contains(lower, "permission") || strings.Contains(lower, "approval") || strings.Contains(lower, "sandbox") || strings.Contains(lower, "patch") || strings.Contains(lower, "functioncall") || strings.Contains(lower, "function_call")
}
