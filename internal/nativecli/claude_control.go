package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// claudeNativeGate is the private PreToolUse hook callback id registered in the
// stream-json initialize request. onFailure=block makes every tool call consult
// this host gate; an unsupported CLI is rejected before the user turn starts.
const claudeNativeGate = "cocogem-native-gate"

// claudeControl owns the write side of a bidirectional Claude stream. Reads stay
// in the parser goroutine; hook decisions run concurrently so a pending user
// decision never blocks cancellation, heartbeats or terminal frames.
type claudeControl struct {
	gate     *bridge
	approver *nativeApprover
	live     *liveness
	sandbox  *nativeSandbox
	cwd      string
	prompt   string

	stdin io.WriteCloser
	write sync.Mutex

	initID    string
	initDone  chan error
	userSent  bool
	closeOnce sync.Once

	mu      sync.Mutex
	pending map[string]context.CancelFunc
	denied  map[string]string
	scopes  map[string][]string
	closed  bool
	err     error
}

func newClaudeControl(stdin io.WriteCloser, gate *bridge, live *liveness, approver *nativeApprover, sandbox *nativeSandbox, cwd, prompt string) *claudeControl {
	return &claudeControl{
		gate: gate, approver: approver, live: live, sandbox: sandbox, cwd: cwd, prompt: prompt,
		stdin: stdin, initID: "cocogem-init", initDone: make(chan error, 1),
		pending: map[string]context.CancelFunc{}, denied: map[string]string{}, scopes: map[string][]string{},
	}
}

// begin registers the PreToolUse gate. The user turn is written only after the
// CLI confirms that the hook is active and fail-closed (handleResponse).
func (c *claudeControl) begin() error {
	hooks := map[string]any{
		"PreToolUse": []any{map[string]any{
			"matcher":         "Bash|Read|Write|Edit|MultiEdit|NotebookEdit|Glob|Grep|WebFetch|WebSearch|LS",
			"hookCallbackIds": []string{claudeNativeGate},
			"onFailure":       "block",
			"timeout":         86400,
		}},
	}
	return c.writeJSON(map[string]any{
		"type": "control_request", "request_id": c.initID,
		"request": map[string]any{"subtype": "initialize", "hooks": hooks},
	})
}

// sendUser writes the first (and only) user turn after the hook ACK.
func (c *claudeControl) sendUser() error {
	c.mu.Lock()
	if c.userSent {
		c.mu.Unlock()
		return nil
	}
	c.userSent = true
	c.mu.Unlock()
	return c.writeJSON(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": c.prompt}})
}

func (c *claudeControl) writeJSON(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return newFailure(FailureProtocol, "cannot encode a native control response")
	}
	c.write.Lock()
	defer c.write.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return newFailure(FailureProtocol, "native CLI control stream is closed")
	}
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		return newFailure(FailureProcess, "native CLI control stream is unavailable")
	}
	return nil
}

func (c *claudeControl) close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		for _, cancel := range c.pending {
			cancel()
		}
		c.pending = map[string]context.CancelFunc{}
		c.mu.Unlock()
		_ = c.stdin.Close()
	})
}

func (c *claudeControl) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

func (c *claudeControl) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *claudeControl) isDenied(callID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.denied[callID]
	return ok
}

// handle processes one control frame. Hook callbacks run in their own
// goroutine; everything else stays synchronous with the parser.
func (c *claudeControl) handle(ctx context.Context, event claudeEvent, raw []byte) error {
	switch event.Type {
	case "control_response":
		return c.handleResponse(event)
	case "control_cancel_request":
		return c.cancel(event.RequestID)
	case "control_request":
		return c.handleRequest(ctx, event, raw)
	}
	return nil
}

func (c *claudeControl) handleResponse(event claudeEvent) error {
	var response struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Error     string          `json:"error"`
		Data      json.RawMessage `json:"response"`
	}
	if json.Unmarshal(event.Response, &response) != nil {
		return newFailure(FailureProtocol, "native CLI returned an invalid initialize response")
	}
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: control_response id=%q subtype=%q\n", response.RequestID, response.Subtype)
	}
	if response.RequestID != c.initID {
		// Responses to hook/can_use_tool requests are written by us, not by the
		// CLI; an unexpected response is a protocol anomaly, not terminal.
		return nil
	}
	if response.Subtype != "success" {
		err := newFailure(FailurePermission, "native CLI rejected the private permission hook: %s", safeToken(response.Error))
		c.fail(err)
		c.initDone <- err
		return nil
	}
	var data struct {
		HooksApplied           *bool `json:"hooks_applied"`
		HookOnFailureSupported *bool `json:"hook_on_failure_supported"`
	}
	_ = json.Unmarshal(response.Data, &data)
	if data.HookOnFailureSupported == nil || !*data.HookOnFailureSupported {
		err := newFailure(FailurePermission, "native CLI does not honor fail-closed permission hooks; the step was not started")
		c.fail(err)
		c.initDone <- err
		return nil
	}
	if data.HooksApplied == nil || !*data.HooksApplied {
		err := newFailure(FailurePermission, "native CLI did not apply the private permission hook; the step was not started")
		c.fail(err)
		c.initDone <- err
		return nil
	}
	if err := c.sendUser(); err != nil {
		c.fail(err)
		c.initDone <- err
		return nil
	}
	c.initDone <- nil
	return nil
}

func (c *claudeControl) cancel(requestID string) error {
	c.mu.Lock()
	cancel, ok := c.pending[requestID]
	delete(c.pending, requestID)
	c.mu.Unlock()
	if ok {
		cancel()
	}
	return nil
}

func debugNative() bool { return os.Getenv("COCOGEM_DEBUG_NATIVE") == "1" }

func truncateDebug(raw json.RawMessage) string {
	if len(raw) > 600 {
		return string(raw[:600])
	}
	return string(raw)
}

func (c *claudeControl) handleRequest(ctx context.Context, event claudeEvent, raw []byte) error {
	var request struct {
		Subtype    string          `json:"subtype"`
		CallbackID string          `json:"callback_id"`
		Input      json.RawMessage `json:"input"`
		ToolName   string          `json:"tool_name"`
		ToolUseID  string          `json:"tool_use_id"`
	}
	if json.Unmarshal(event.Request, &request) != nil {
		return newFailure(FailureProtocol, "native CLI returned an invalid control request")
	}
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: control_request subtype=%q\n", request.Subtype)
	}
	switch request.Subtype {
	case "hook_callback":
		if request.CallbackID != claudeNativeGate {
			return c.respondError(event.RequestID, "unknown hook callback")
		}
		requestID := event.RequestID
		ctx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		if _, exists := c.pending[requestID]; exists {
			c.mu.Unlock()
			cancel()
			return newFailure(FailureProtocol, "native CLI reused a hook request identity")
		}
		c.pending[requestID] = cancel
		c.mu.Unlock()
		go func() {
			defer func() {
				c.mu.Lock()
				delete(c.pending, requestID)
				c.mu.Unlock()
				cancel()
			}()
			c.handleHook(ctx, requestID, request.Input)
		}()
		return nil
	case "can_use_tool":
		requestID := event.RequestID
		ctx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		c.pending[requestID] = cancel
		c.mu.Unlock()
		go func() {
			defer func() {
				c.mu.Lock()
				delete(c.pending, requestID)
				c.mu.Unlock()
				cancel()
			}()
			c.handleCanUseTool(ctx, requestID, request.ToolName, request.ToolUseID, request.Input)
		}()
		return nil
	default:
		return c.respondError(event.RequestID, "unsupported control request")
	}
}

func (c *claudeControl) respondError(requestID, message string) error {
	return c.writeJSON(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "error", "request_id": requestID, "error": message},
	})
}

type claudeHookInput struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id"`
	CWD           string          `json:"cwd"`
}

func (c *claudeControl) handleHook(ctx context.Context, requestID string, raw json.RawMessage) {
	if debugNative() {
		fmt.Fprintf(os.Stderr, "native debug: hook raw=%s\n", truncateDebug(raw))
	}
	var hook claudeHookInput
	if json.Unmarshal(raw, &hook) != nil || hook.HookEventName != "PreToolUse" || hook.ToolName == "" || hook.ToolUseID == "" {
		_ = c.respondError(requestID, "invalid hook input")
		return
	}
	authorization, err := c.approver.authorize(ctx, "cli:"+hook.ToolUseID, hook.ToolName, hook.ToolInput, "")
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		c.fail(err)
		_ = c.denyHook(requestID, hook.ToolName, hook.ToolUseID, err.Error())
		return
	}
	if !authorization.Allow {
		reason := authorization.Reason
		if reason == "" {
			reason = "用户拒绝或授权已取消"
		}
		_ = c.denyHook(requestID, hook.ToolName, hook.ToolUseID, reason)
		return
	}
	updated, err := c.executionInput(hook.ToolName, hook.ToolInput, hook.CWD, authorization)
	if err != nil {
		_ = c.denyHook(requestID, hook.ToolName, hook.ToolUseID, err.Error())
		return
	}
	output := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":      "PreToolUse",
			"permissionDecision": "allow",
			"updatedInput":       updated,
		},
	}
	if err := c.writeJSON(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": output},
	}); err != nil {
		c.fail(err)
	}
}

func (c *claudeControl) denyHook(requestID, name, callID, reason string) error {
	c.mu.Lock()
	c.denied[callID] = reason
	c.mu.Unlock()
	if c.gate != nil {
		_ = c.gate.emit("tool_finished", map[string]any{
			"tool_call_id": "cli:" + callID, "name": safeToken(name), "origin": "native",
			"status": "denied", "output": "permission_denied: " + reason,
		})
	}
	return c.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			},
		}},
	})
}

// executionInput narrows the approved call. A Bash command is rewritten to run
// inside a per-call seatbelt profile containing the project plus only the
// resource scopes the host approved. Other tools keep their input; their host
// gate already resolved the real path.
func (c *claudeControl) executionInput(name string, input json.RawMessage, cwd string, authorization nativeAuthorization) (json.RawMessage, error) {
	var values map[string]any
	if err := json.Unmarshal(input, &values); err != nil {
		return nil, errors.New("the native tool input is not an object")
	}
	if !strings.EqualFold(name, "Bash") {
		return input, nil
	}
	if c.sandbox == nil || !c.sandbox.canWrap() {
		if c.approver != nil && c.approver.mode == "full_access" {
			// Full access keeps the existing unrestricted local execution.
			return input, nil
		}
		return nil, errors.New("the native command boundary is unavailable on this platform")
	}
	command, _ := values["command"].(string)
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("the native command is empty")
	}
	if cwd == "" {
		cwd = c.cwd
	}
	profile, err := c.sandbox.profile("cmd-"+safeToken(name)+"-"+safeToken(fmt.Sprint(time.Now().UnixNano())), authorization.ReadScopes, authorization.WriteScopes)
	if err != nil {
		return nil, err
	}
	values["command"] = c.sandbox.wrap(profile, cwd, command)
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, errors.New("cannot encode the sandboxed command")
	}
	return encoded, nil
}

func (c *claudeControl) handleCanUseTool(ctx context.Context, requestID, name, callID string, input json.RawMessage) {
	authorization, err := c.approver.authorize(ctx, "cli:"+callID, name, input, "")
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		c.fail(err)
		_ = c.denyCanUseTool(requestID, name, callID, err.Error())
		return
	}
	if !authorization.Allow {
		reason := authorization.Reason
		if reason == "" {
			reason = "用户拒绝或授权已取消"
		}
		_ = c.denyCanUseTool(requestID, name, callID, reason)
		return
	}
	updated, err := c.executionInput(name, input, c.cwd, authorization)
	if err != nil {
		_ = c.denyCanUseTool(requestID, name, callID, err.Error())
		return
	}
	if err := c.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": map[string]any{
			"behavior": "allow", "updatedInput": json.RawMessage(updated),
		}},
	}); err != nil {
		c.fail(err)
	}
}

func (c *claudeControl) denyCanUseTool(requestID, name, callID, reason string) error {
	c.mu.Lock()
	c.denied[callID] = reason
	c.mu.Unlock()
	if c.gate != nil {
		_ = c.gate.emit("tool_finished", map[string]any{
			"tool_call_id": "cli:" + callID, "name": safeToken(name), "origin": "native",
			"status": "denied", "output": "permission_denied: " + reason,
		})
	}
	return c.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": map[string]any{
			"behavior": "deny", "message": reason, "interrupt": false,
		}},
	})
}
