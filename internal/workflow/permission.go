package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// permissionWaitInterval is the bounded poll the waiter uses. It holds no
// database transaction and does not count as CLI silence.
const permissionWaitInterval = 150 * time.Millisecond

// requestAccessToolInput is the discoverable request path. It carries the exact
// arguments of the operation it asks for, so an approval can be bound to the
// real call's digest instead of hoping the model repeats itself consistently.
type requestAccessToolInput struct {
	Action    string          `json:"action"`
	ToolName  string          `json:"tool_name"`
	Scope     string          `json:"scope"`
	Arguments json.RawMessage `json:"arguments"`
	Reason    string          `json:"reason"`
}

// requestAccessDefinition is added to the visible catalog when the current mode
// permits asking for something.
func requestAccessDefinition() model.Tool {
	return model.Tool{
		Name:        "request_tool_access",
		Description: "Ask the user to authorize one concrete effect the current permission mode does not admit. Supply the exact tool name, the normalized resource scope and the exact arguments you will pass to that tool. The host validates and normalizes the scope, shows the request in this conversation, and on approval returns a one-time receipt: your next call must use the same tool, action, scope and arguments, and it will execute exactly once. On denial it returns the user's decision so you can choose an alternative.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"read", "write", "run_test", "execute", "network", "publish_artifact"}},
				"tool_name": map[string]any{"type": "string", "description": "The exact tool you intend to call"},
				"scope":     map[string]any{"type": "string", "description": "Absolute normalized path or command working directory the effect targets"},
				"arguments": map[string]any{"type": "object", "description": "The exact arguments object you will pass to tool_name"},
				"reason":    map[string]any{"type": "string", "description": "Short reason shown to the user (no secrets)"},
			},
			"required":             []string{"action", "tool_name", "scope", "arguments", "reason"},
			"additionalProperties": false,
		},
	}
}

// currentCallID returns the exact identity of the tool call being executed. A
// caller without a protocol identity still gets a unique one; sharing an empty
// identity would let a single allowance cover two different calls.
func (x *stepRuntime) currentCallID(ctx context.Context) string {
	if id := agent.ToolCallID(ctx); id != "" {
		return id
	}
	return fmt.Sprintf("host-%d", x.hostCallSeq.Add(1))
}

// requestAccess creates a durable pre-approval for the exact described
// operation, then waits. The approval is consumed by the next matching real call
// (same run/step/attempt, action, tool, scope and argument digest) exactly once.
func (x *stepRuntime) requestAccess(ctx context.Context, callID string, raw json.RawMessage) (string, error) {
	var in requestAccessToolInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", errors.New("invalid request_tool_access arguments")
	}
	if !slicesContainsAction(in.Action) || strings.TrimSpace(in.ToolName) == "" || strings.TrimSpace(in.Scope) == "" {
		return "", errors.New("request_tool_access requires a known action, the exact tool name and a concrete scope")
	}
	if in.ToolName == "request_tool_access" {
		return "", errors.New("request_tool_access cannot authorize itself")
	}
	// The host normalizes the scope. A model-supplied relative fragment never
	// becomes an authorization scope on its own.
	scope := w.NormalizeScope(x.run.ProjectRoot(), in.Scope)
	if scope == "" {
		scope = w.NormalizeScope(x.run.TaskDirectory, in.Scope)
	}
	if scope == "" {
		return "", errors.New("the requested scope could not be normalized against this task")
	}
	arguments := in.Arguments
	if len(arguments) == 0 || string(arguments) == "null" {
		return "", errors.New("request_tool_access requires the exact arguments of the intended call")
	}
	reason := strings.TrimSpace(in.Reason)
	if len([]rune(reason)) > 500 {
		reason = string([]rune(reason)[:500])
	}
	service := project.New(x.engine.Store)
	run := x.snapshotRun()
	request, err := service.RequestToolPermission(context.WithoutCancel(ctx), project.CreatePermissionRequestInput{
		ThreadID: run.ThreadID, WorkspaceID: run.WorkspaceID, RunID: run.ID,
		StepID: x.step.ID, AttemptID: x.attemptSnapshot().ID, MemberID: x.agent.ID, MemberName: x.agent.DisplayName,
		ToolCallID: callID, Origin: w.OriginHost, Adapter: x.agent.Executor,
		Action: in.Action, ToolName: in.ToolName, NormalizedResourceScope: scope,
		Arguments: arguments, Reason: reason, PreApproval: true,
	})
	if err != nil {
		return "", err
	}
	if err := x.observeEvent("tool_finished", map[string]any{
		"tool_call_id": callID, "name": "request_tool_access", "origin": w.OriginHost,
		"status": "awaiting_permission", "permission_request_id": request.ID,
	}); err != nil {
		return "", err
	}
	x.noteAwaitingPermission(callID)
	defer x.clearAwaitingPermission(callID)
	// The request tool blocks on the user too, so it pauses the same bounded
	// execution budget. The approval receipt returns immediately after the
	// decision and the real effect runs as a later call with its own window.
	resume := x.pauseToolDeadline(ctx)
	defer resume()
	decided, err := service.WaitForDecision(ctx, request.ID, permissionWaitInterval)
	if err != nil {
		return "", err
	}
	switch decided.Status {
	case w.PermissionAllowedOnce, w.PermissionAllowedConversation:
		// The allowance stays unconsumed until the real call arrives, so one
		// decision produces exactly one effect on the matching operation.
		return fmt.Sprintf(`{"decision":%q,"tool_name":%q,"action":%q,"scope":%q,"instruction":"Call %s now with exactly these arguments. The approval is consumed by that call and cannot authorize a different operation."}`, decided.Status, in.ToolName, in.Action, scope, in.ToolName), nil
	case w.PermissionDenied:
		return fmt.Sprintf(`{"decision":"denied","tool_name":%q,"instruction":"The user denied this request. Do not retry the same effect; choose an alternative or finish with an explicit limitation."}`, in.ToolName), nil
	default:
		return fmt.Sprintf(`{"decision":%q,"instruction":"The request is no longer actionable. Do not retry it automatically; choose an alternative or finish with an explicit limitation."}`, decided.Status), nil
	}
}

// decideHostAccess enforces the live permission boundary for one host tool call.
// It returns a non-nil admission only when a one-time allowance was consumed; the
// caller must settle it with the real outcome of the execution.
func (x *stepRuntime) decideHostAccess(ctx context.Context, callID, name string, input json.RawMessage) (*w.ToolInvocationAdmission, string, bool, error) {
	action := w.ActionForTool(name)
	if action == "" {
		return nil, "", false, nil
	}
	scope := x.toolScope(name, input)
	service := project.New(x.engine.Store)
	run := x.snapshotRun()
	decision, err := service.CheckToolAccess(ctx, project.AccessRequest{
		Run: run, StepID: x.step.ID, Attempt: x.attemptSnapshot().ID,
		CallID: callID, Name: name, Arguments: input, Scope: scope,
	})
	if err != nil {
		return nil, "", false, err
	}
	if decision.Allowed {
		// A matching conversation grant is a human decision for this exact tool and
		// scope, so the executor may use the approved scope. A plain mode admission
		// (inside-project) needs no approved scope.
		return nil, scope, decision.GrantID != "", nil
	}
	if decision.Settlement != nil {
		return nil, "", false, fmt.Errorf("permission_already_consumed: this allowance was already used (%s)", decision.Settlement.Status)
	}
	if decision.Request != nil {
		admission, approvedScope, err := x.awaitDecision(ctx, service, *decision.Request, callID, run, action, name, scope, input)
		return admission, approvedScope, err == nil, err
	}
	if !decision.Requestable {
		return nil, "", false, fmt.Errorf("%s is outside this step's task/role ceiling; the user cannot escalate beyond the accepted objective", name)
	}
	request, err := service.RequestToolPermission(context.WithoutCancel(ctx), project.CreatePermissionRequestInput{
		ThreadID: run.ThreadID, WorkspaceID: run.WorkspaceID, RunID: run.ID,
		StepID: x.step.ID, AttemptID: x.attemptSnapshot().ID, MemberID: x.agent.ID, MemberName: x.agent.DisplayName,
		ToolCallID: callID, Origin: w.OriginHost, Adapter: x.agent.Executor,
		Action: action, ToolName: name, NormalizedResourceScope: scope,
		Arguments: input, Reason: reasonForTool(name, scope),
		OperationSummary: operationSummary(name, input, scope),
	})
	if err != nil {
		return nil, "", false, err
	}
	if err := x.observeEvent("tool_finished", map[string]any{
		"tool_call_id": callID, "name": name, "origin": w.OriginHost,
		"status": "awaiting_permission", "permission_request_id": request.ID,
	}); err != nil {
		return nil, "", false, err
	}
	admission, approvedScope, err := x.awaitDecision(ctx, service, request, callID, run, action, name, scope, input)
	return admission, approvedScope, err == nil, err
}

// awaitDecision blocks on the user's decision, then consumes a one-time
// allowance exactly once before returning.
func (x *stepRuntime) awaitDecision(ctx context.Context, service *project.Service, request w.ToolPermissionRequest, callID string, run w.Run, action, name, scope string, arguments json.RawMessage) (*w.ToolInvocationAdmission, string, error) {
	x.noteAwaitingPermission(callID)
	defer x.clearAwaitingPermission(callID)
	// A human decision is not execution time. Pause the executing tool's bounded
	// deadline for exactly this wait; resume restarts a full execution window
	// before the approved effect runs. Cancellation still propagates through the
	// parent context while paused.
	resume := x.pauseToolDeadline(ctx)
	defer resume()
	decided, err := service.WaitForDecision(ctx, request.ID, permissionWaitInterval)
	if err != nil {
		return nil, "", err
	}
	switch decided.Status {
	case w.PermissionAllowedConversation:
		return nil, scope, nil
	case w.PermissionAllowedOnce:
		var admission w.ToolInvocationAdmission
		var created bool
		var err error
		if decided.PreApproval {
			// The approval is bound to the described operation, and this real
			// call is that operation.
			admission, created, err = service.ConsumePreApproval(ctx, decided.ID, run, x.step.ID, x.attemptSnapshot().ID, callID, action, name, scope, arguments)
		} else {
			admission, created, err = service.ConsumeAllowance(ctx, decided.ID, run, x.step.ID, x.attemptSnapshot().ID, callID)
		}
		if err != nil {
			return nil, "", err
		}
		if !created {
			return nil, "", fmt.Errorf("permission_already_consumed: this allowance was already used (%s)", admission.Status)
		}
		if err := service.MarkAdmissionStarted(context.WithoutCancel(ctx), admission.ID); err != nil {
			return nil, "", err
		}
		return &admission, scope, nil
	case w.PermissionDenied:
		return nil, "", errors.New("permission_denied: the user denied this operation; do not retry it, choose an alternative or finish with an explicit limitation")
	case w.PermissionExpired:
		return nil, "", errors.New("permission_expired: the permission mode changed while this request was pending; it must be re-evaluated")
	default:
		return nil, "", fmt.Errorf("permission_%s: the request is no longer actionable", decided.Status)
	}
}

// settleAdmission records the real outcome of a consumed allowance. A denied or
// failed call never settles as success, and a canceled/uncertain effect is
// recorded as unknown so it is never replayed automatically.
func (x *stepRuntime) settleAdmission(ctx context.Context, admission *w.ToolInvocationAdmission, output string, callErr error) {
	if admission == nil {
		return
	}
	status := w.AdmissionCompleted
	switch {
	case callErr != nil && ctx.Err() != nil:
		status = w.AdmissionUnknown
	case callErr != nil:
		status = w.AdmissionFailed
	}
	resultRef := ""
	if callErr != nil {
		resultRef = truncateRef(callErr.Error())
	} else {
		resultRef = truncateRef(output)
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(x.baseContext()), 5*time.Second)
	defer cancel()
	_ = project.New(x.engine.Store).SettleAdmission(settleCtx, admission.ID, status, resultRef)
}

func truncateRef(value string) string {
	const limit = 256
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// toolScope derives the host-owned normalized scope for the effect a tool
// targets. Unknown shapes fall back to the project root, which never widens the
// boundary.
func (x *stepRuntime) toolScope(name string, input json.RawMessage) string {
	root := x.run.ProjectRoot()
	var args map[string]any
	_ = json.Unmarshal(input, &args)
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
				return value
			}
		}
		return ""
	}
	target := pick("path", "cwd", "source_id", "url")
	switch name {
	case "run_command":
		if target == "" {
			target = "."
		}
		if !filepath.IsAbs(target) {
			base := x.run.TaskDirectory
			if base == "" {
				base = root
			}
			target = filepath.Join(base, target)
		}
	case "run_test":
		base := root
		if x.run.TaskDirectory != "" {
			base = x.run.TaskDirectory
		}
		target = base
	case "write_file", "patch_file", "read_file", "list_files", "search_content":
		if target == "" {
			target = "."
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
	case "external_publish":
		// The approved scope is the exact destination and method; the body is
		// bound by the argument digest.
		var in struct {
			Kind   string `json:"kind"`
			Method string `json:"method"`
			URL    string `json:"url"`
		}
		if json.Unmarshal(input, &in) != nil || in.URL == "" {
			return "external:invalid"
		}
		return "external:" + in.Kind + ":" + in.Method + ":" + in.URL
	case "fetch_url", "web_search":
		// Network scope is the public web, not a filesystem path. A fixed token
		// keeps grants from matching an unrelated file scope.
		return "network:public"
	default:
		if target == "" {
			return w.NormalizeScope(root, root)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
	}
	return w.NormalizeScope(root, target)
}

// reasonForTool builds the short, secret-free explanation shown with a request.
func reasonForTool(name, scope string) string {
	if scope == "" {
		return "操作 " + name + " 需要确认"
	}
	return "操作 " + name + " 需要授权：" + scope
}

func slicesContainsAction(action string) bool {
	switch action {
	case "read", "write", "run_test", "execute", "network", "publish_artifact":
		return true
	}
	return false
}

// noteAwaitingPermission records that this step is blocked on a human decision.
func (x *stepRuntime) noteAwaitingPermission(callID string) {
	x.mu.Lock()
	if x.awaiting == nil {
		x.awaiting = map[string]bool{}
	}
	x.awaiting[callID] = true
	x.mu.Unlock()
	// Capture the identity before opening the transaction: the observer may hold
	// x.mu while waiting for the store mutex, so never take x.mu inside a
	// transaction callback.
	attemptID := x.attemptSnapshot().ID
	x.engine.Store.Write(context.WithoutCancel(x.baseContext()), func(tx *store.Tx) error {
		current, err := tx.Attempt(attemptID)
		if err != nil {
			return err
		}
		current.Recovery = "awaiting_permission"
		return tx.SaveAttempt(current)
	})
}

// clearAwaitingPermission records that the human decision (or cancellation) has
// ended the suspension. It is never treated as progress by itself.
func (x *stepRuntime) clearAwaitingPermission(callID string) {
	x.mu.Lock()
	delete(x.awaiting, callID)
	remaining := len(x.awaiting)
	x.mu.Unlock()
	if remaining != 0 {
		return
	}
	attemptID := x.attemptSnapshot().ID
	x.engine.Store.Write(context.WithoutCancel(x.baseContext()), func(tx *store.Tx) error {
		current, err := tx.Attempt(attemptID)
		if err != nil {
			return err
		}
		if current.Recovery == "awaiting_permission" {
			current.Recovery = ""
			return tx.SaveAttempt(current)
		}
		return nil
	})
}

// scopeInsideProject reports whether a normalized scope is inside this run's
// project or task tree.
func (x *stepRuntime) scopeInsideProject(scope string) bool {
	if scope == "" {
		return false
	}
	for _, root := range []string{x.run.ProjectRoot(), x.run.TaskDirectory} {
		if root == "" {
			continue
		}
		normalized := w.NormalizeScope(root, root)
		if normalized != "" && w.ScopeContains(normalized, scope) {
			return true
		}
	}
	return false
}

// operationSummary renders the bounded, secret-free description shown with a
// request. It is derived from the exact arguments so the user can judge the
// effect (command, destination) without opening raw logs.
func operationSummary(name string, raw json.RawMessage, scope string) string {
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	truncate := func(value string, limit int) string {
		if len(value) <= limit {
			return value
		}
		return value[:limit] + "…"
	}
	switch name {
	case "run_command":
		if argv, ok := args["argv"].([]any); ok {
			parts := make([]string, 0, len(argv))
			for _, item := range argv {
				if text, ok := item.(string); ok {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				return truncate(strings.Join(parts, " "), 300) + " (cwd " + scope + ")"
			}
		}
	case "write_file", "patch_file":
		if path, ok := args["path"].(string); ok {
			return name + " " + truncate(path, 300) + " (scope " + scope + ")"
		}
	case "external_publish":
		if request, err := tools.DecodeExternalRequest(raw); err == nil {
			return truncate(request.Summary(), 400)
		}
	}
	return reasonForTool(name, scope)
}
