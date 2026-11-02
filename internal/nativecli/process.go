package nativecli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	case "pi":
		streamErr = parsePi(stdout, gate, live)
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

// A denied tool call is recoverable only when the CLI itself proves the tool
// was unavailable with its fixed, tool-specific error. The bound keeps a model
// from looping on denials; the four concrete refusal cases stay fatal.
const (
	maxClaudeDeniedCalls  = 3
	claudeDenialPrefix    = "<tool_use_error>Error: No such tool available: "
	claudeDenialSuffix    = "</tool_use_error>"
	claudeDenialDisabled  = " is disabled for this session, in subagents as well as here."
	claudeDenialEventText = "Native tool call denied by the CLI: No such tool available: "
)

type deniedCall struct {
	name     string
	resolved bool
}

// claudeToolDenial accepts only the CLI's exact "No such tool available"
// message for this call's own tool name. Any other is_error text, including a
// successful or permission-shaped result, is not proof that nothing ran.
func claudeToolDenial(text, name string) bool {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, claudeDenialPrefix+name) || !strings.HasSuffix(text, claudeDenialSuffix) {
		return false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(text, claudeDenialPrefix+name), claudeDenialSuffix)
	return rest == "" || rest == ". "+name+claudeDenialDisabled
}

// emitClaudeDelta records the visible prefix and emits exactly the new bytes.
// The activity event is throttled inside the bridge; visible text is not.
func emitClaudeDelta(text, key string, streams *textStreams, live *liveness, gate *bridge) error {
	text, err := streams.delta(key, text)
	if err != nil {
		return newFailure(FailureProtocol, "native CLI emitted invalid streamed text: %v", err)
	}
	if text == "" {
		return nil
	}
	if strings.TrimSpace(text) != "" {
		live.valid("text_delta")
		if err := gate.activity(activityTextDelta); err != nil {
			return err
		}
	}
	return gate.emit("text", map[string]any{"text": text, "activity": strings.TrimSpace(text) != ""})
}

type claudeStreamEvent struct {
	kind      string
	text      string
	blockType string
	messageID string
	index     int
}

// decodeClaudeError accepts both shapes emitted by installed CLI versions: a
// bundled object ({type,message}) and a bare provider code string
// ("server_error"). Anything else is malformed rather than silently ignored.
func decodeClaudeError(raw json.RawMessage) (code, message string, ok bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", "", true
	}
	if trimmed[0] == '"' {
		if json.Unmarshal(trimmed, &code) != nil {
			return "", "", false
		}
		return code, "", true
	}
	var object struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(trimmed, &object) != nil {
		return "", "", false
	}
	return object.Type, object.Message, true
}

// parseClaudeStream validates the nested API event; the top-level stream_event
// type alone proves nothing about visible content. Thinking deltas are
// classified separately and never reach the visible text stream.
func parseClaudeStream(raw json.RawMessage) (claudeStreamEvent, bool) {
	if len(raw) == 0 {
		return claudeStreamEvent{}, false
	}
	var stream struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		ContentBlock struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content_block"`
	}
	if json.Unmarshal(raw, &stream) != nil || stream.Type == "" {
		return claudeStreamEvent{}, false
	}
	event := claudeStreamEvent{messageID: stream.Message.ID, index: stream.Index}
	switch stream.Type {
	case "message_start":
		event.kind = "message_start"
	case "message_stop":
		event.kind = "message_stop"
	case "content_block_start":
		event.kind, event.blockType, event.text = "block_start", stream.ContentBlock.Type, stream.ContentBlock.Text
	case "content_block_delta":
		switch stream.Delta.Type {
		case "text_delta":
			event.kind, event.text = "text_delta", stream.Delta.Text
		case "thinking_delta":
			event.kind, event.text = "thinking_delta", stream.Delta.Thinking
		}
	}
	return event, true
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
