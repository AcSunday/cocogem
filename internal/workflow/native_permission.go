package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// nativeAuthorizer binds one step's native approval callback. It is the single
// host path a CLI adapter may use before a native effect.
//
// The callback never trusts the adapter's raw resource: it normalizes the target
// to a real path (or the fixed network token) and asks the live permission
// service with the exact run/step/attempt/call identity. A decision is bound to
// that identity; the returned ResourceScope is the only scope native may use.
func (x *stepRuntime) nativeAuthorizer() func(context.Context, model.NativePermissionRequest) (model.NativePermissionDecision, error) {
	return func(ctx context.Context, request model.NativePermissionRequest) (model.NativePermissionDecision, error) {
		deny := model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny}
		if ctx.Err() != nil {
			return deny, ctx.Err()
		}
		name := strings.TrimSpace(request.Name)
		callID := strings.TrimSpace(request.CallID)
		if name == "" || callID == "" {
			return deny, errors.New("native request lacks a tool name or call identity")
		}
		action := request.Action
		if action == "" {
			action = w.ActionForNativeTool(name)
		}
		// The step role is an independent hard ceiling. A reviewer may verify with
		// tests but never gains a file write or shell through an app-server request
		// or a shared conversation grant, whatever the CLI exposes.
		if !x.toolAllowedForRole(nativeAccessName(action, name)) {
			return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "this step's role does not permit that operation"}, nil
		}
		scope := x.nativeScope(action, request.Resource, request.Input)
		arguments := request.Input
		if len(arguments) == 0 {
			arguments = []byte("null")
		}
		run := x.snapshotRun()
		service := project.New(x.engine.Store)
		admission := project.AccessRequest{
			Run: run, StepID: x.step.ID, Attempt: x.attemptSnapshot().ID,
			CallID: callID, Name: nativeAccessName(action, name), Arguments: arguments, Scope: scope,
			Risk: request.Risk,
		}
		decision, err := service.CheckToolAccess(ctx, admission)
		if err != nil {
			// A revoked boundary is a definite denial, not a transport failure:
			// the CLI gets "deny" so it can continue without an effect and without
			// waiting for a decision that must never be requested.
			if project.IsCode(err, "authority_revoked") {
				return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "the permission mode changed after this execution started"}, nil
			}
			return deny, err
		}
		if decision.Allowed {
			// An automatic admission never acknowledges a risk; a risky target only
			// ever runs after a human decision.
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: scope}, nil
		}
		if decision.Settlement != nil {
			// A consumed one-time allowance must not execute a second time. The
			// CLI has not produced an effect yet, so denying is safe and honest.
			return deny, nil
		}
		if decision.Request != nil {
			return x.awaitNativeDecision(ctx, service, *decision.Request, scope)
		}
		if !decision.Requestable {
			return deny, nil
		}
		reason := strings.TrimSpace(request.Reason)
		if len([]rune(reason)) > 500 {
			reason = string([]rune(reason)[:500])
		}
		if reason == "" {
			reason = reasonForTool(name, scope)
		}
		created, err := service.RequestToolPermission(context.WithoutCancel(ctx), project.CreatePermissionRequestInput{
			ThreadID: run.ThreadID, WorkspaceID: run.WorkspaceID, RunID: run.ID,
			StepID: x.step.ID, AttemptID: x.attemptSnapshot().ID, MemberID: x.agent.ID, MemberName: x.agent.DisplayName,
			ToolCallID: callID, Origin: w.OriginNative, Adapter: request.Adapter,
			Action: action, ToolName: nativeAccessName(action, name), NormalizedResourceScope: scope,
			Arguments: arguments, Reason: reason, Risk: nativeRiskReason(request),
		})
		if err != nil {
			return deny, err
		}
		if err := x.observeEvent("tool_finished", map[string]any{
			"tool_call_id": callID, "name": name, "origin": w.OriginNative,
			"status": "awaiting_permission", "permission_request_id": created.ID,
		}); err != nil {
			return deny, err
		}
		return x.awaitNativeDecision(ctx, service, created, scope)
	}
}

// awaitNativeDecision suspends only this call until the user decides, then
// consumes a one-time allowance exactly once before authorizing the effect.
func (x *stepRuntime) awaitNativeDecision(ctx context.Context, service *project.Service, request w.ToolPermissionRequest, scope string) (model.NativePermissionDecision, error) {
	deny := model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny}
	x.noteAwaitingPermission(request.ToolCallID)
	defer x.clearAwaitingPermission(request.ToolCallID)
	// The callback may run inside a host tool call that owns a pausable
	// execution deadline. Pause it only for the human wait; the adapter executes
	// the approved effect after this returns with the ordinary finite window.
	resume := x.pauseToolDeadline(ctx)
	defer resume()
	decided, err := service.WaitForDecision(ctx, request.ID, permissionWaitInterval)
	if err != nil {
		if errors.Is(err, project.ErrPermissionExpired) {
			return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "permission mode changed; the request must be re-evaluated"}, nil
		}
		return deny, err
	}
	switch decided.Status {
	case w.PermissionAllowedConversation:
		// The adapter is still told to allow exactly this call once; the durable
		// conversation grant is the host's own ledger for later matching calls.
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeConversation, ResourceScope: scope,
			AcknowledgedRisk: decided.Risk != ""}, nil
	case w.PermissionAllowedOnce:
		run := x.snapshotRun()
		reserved, created, err := service.ConsumeAllowance(ctx, decided.ID, run, x.step.ID, x.attemptSnapshot().ID, request.ToolCallID)
		if err != nil {
			return deny, err
		}
		if !created {
			return deny, nil
		}
		if err := service.MarkAdmissionStarted(context.WithoutCancel(ctx), reserved.ID); err != nil {
			return deny, err
		}
		// The CLI runs the effect after this callback returns, so the admission is
		// settled from that call's terminal event, keyed by its own call id. There
		// is no single shared slot: concurrent native calls each keep their own.
		x.mu.Lock()
		if x.nativeAdmissions == nil {
			x.nativeAdmissions = map[string]string{}
		}
		x.nativeAdmissions[request.ToolCallID] = reserved.ID
		x.mu.Unlock()
		return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce, ResourceScope: scope,
			AcknowledgedRisk: decided.Risk != ""}, nil
	case w.PermissionDenied:
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "the user denied this operation"}, nil
	case w.PermissionExpired:
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "permission mode changed; the request must be re-evaluated"}, nil
	default:
		return model.NativePermissionDecision{Allow: false, Scope: model.NativeScopeDeny, Reason: "the request is no longer actionable"}, nil
	}
}

// nativeScope normalizes the adapter's best-effort target. A network effect gets
// the fixed public token; a filesystem/command target resolves against the task
// directory or the bound project, never the process working directory.
func (x *stepRuntime) nativeScope(action, resource string, input []byte) string {
	run := x.snapshotRun()
	if action == "network" {
		return "network:public"
	}
	target := strings.TrimSpace(resource)
	if target == "" {
		target = nativeInputPath(input)
	}
	if target == "" {
		target = "."
	}
	if !filepath.IsAbs(target) {
		base := run.TaskDirectory
		if base == "" {
			base = run.ProjectRoot()
		}
		if base == "" {
			return ""
		}
		target = filepath.Join(base, target)
	}
	root := run.ProjectRoot()
	if root == "" {
		root = run.TaskDirectory
	}
	if root == "" {
		return ""
	}
	return w.NormalizeScope(root, target)
}

// nativeInputPath extracts the most probable target path from a native input
// object. It is only a hint: the resolved scope is what the host approves.
func nativeInputPath(input []byte) string {
	if len(input) == 0 {
		return ""
	}
	var value map[string]any
	if err := json.Unmarshal(input, &value); err != nil {
		return ""
	}
	for _, key := range []string{"file_path", "path", "filePath", "notebook_path", "cmd", "command", "argv", "url"} {
		raw, ok := value[key]
		if !ok {
			continue
		}
		switch typed := raw.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				if key == "cmd" || key == "command" {
					// A shell command is not a path. Its working directory is the
					// only scope the host can normalize honestly.
					return ""
				}
				return typed
			}
		case []any:
			if len(typed) > 0 {
				if first, ok := typed[0].(string); ok {
					return first
				}
			}
		}
	}
	return ""
}

// nativeAccessName maps a native tool to the host action vocabulary used by
// CheckToolAccess, so mode/task/grant checks stay identical to host tools.
func nativeAccessName(action, _ string) string {
	switch action {
	case "read":
		return "read_file"
	case "write":
		return "write_file"
	case "run_test":
		return "run_test"
	case "execute":
		return "run_command"
	case "network":
		return "fetch_url"
	case "publish_artifact":
		return "publish_artifact"
	default:
		return "read_file"
	}
}

// snapshotRun re-reads the live run so the callback never authorizes against a
// stale in-memory copy (status, epoch and task revision can change meanwhile).
func (x *stepRuntime) snapshotRun() w.Run {
	run := x.run
	if x.engine == nil || x.engine.Store == nil {
		return run
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(x.baseContext()), 3*time.Second)
	defer cancel()
	if err := x.engine.Store.Read(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(x.run.ID)
		if err == nil {
			run = current
		}
		return err
	}); err != nil {
		// Keep the frozen snapshot; CheckToolAccess still compares the live epoch.
		return x.run
	}
	return run
}

// bindNativeAuthorizer attaches the callback to the request. Adapters receive nil
// when this step must not admit any native effect.
func (x *stepRuntime) bindNativeAuthorizer(request *model.Request) {
	if request == nil {
		return
	}
	request.AuthorizeNative = x.nativeAuthorizer()
}

// nativeRiskReason records the adapter's grading fact so a risky target is never
// auto-allowed and the user decision is bound to that exact risk.
func nativeRiskReason(request model.NativePermissionRequest) string {
	if request.Risk == "" {
		return ""
	}
	return request.Risk
}

// settleNativeAdmission settles the one-time allowance consumed by one native
// call from that call's own terminal event. An unknown/interrupted call stays
// unknown so it is never replayed as success.
func (x *stepRuntime) settleNativeAdmission(callID, status string) {
	if callID == "" {
		return
	}
	x.mu.Lock()
	id := ""
	if x.nativeAdmissions != nil {
		id = x.nativeAdmissions[callID]
		delete(x.nativeAdmissions, callID)
	}
	x.mu.Unlock()
	if id == "" {
		return
	}
	settled := w.AdmissionCompleted
	switch status {
	case "denied", "canceled":
		settled = w.AdmissionFailed
	case "failed", "error":
		settled = w.AdmissionFailed
	case "unknown":
		settled = w.AdmissionUnknown
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(x.baseContext()), 5*time.Second)
	defer cancel()
	_ = project.New(x.engine.Store).SettleAdmission(ctx, id, settled, status)
}
