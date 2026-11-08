package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// ErrPermissionExpired means the request's epoch is no longer current.
var ErrPermissionExpired = errors.New("project: permission request expired")

// ErrInvocationMismatch rejects an allowance used by another invocation.
var ErrInvocationMismatch = errors.New("project: permission identity mismatch")

// CreatePermissionRequestInput describes one concrete effect the current mode
// does not admit. The host fills the normalized scope and argument digest; the
// invocation identity is run+step+attempt+tool_call.
type CreatePermissionRequestInput struct {
	ThreadID                string
	WorkspaceID             string
	RunID                   string
	StepID                  string
	AttemptID               string
	MemberID                string
	MemberName              string
	ToolCallID              string
	Origin                  string
	Adapter                 string
	Action                  string
	ToolName                string
	NormalizedResourceScope string
	Arguments               json.RawMessage
	Reason                  string
	// OperationSummary is display-only review data computed by the host.
	OperationSummary string
	RequestID        string
	// PreApproval marks request_tool_access. The approval is bound to the exact
	// operation (action/tool/scope/arguments digest) and consumed by the next
	// matching real call, not by the request tool's own call identity.
	PreApproval bool
	// Risk carries an adapter-proved grading fact (e.g. "hardlink") that the user
	// must explicitly acknowledge before the effect runs.
	Risk string
}

// DigestArguments hashes the exact tool arguments. The digest binds an
// allowance to the one call it authorizes.
func DigestArguments(raw json.RawMessage) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// RequestToolPermission persists a pending request. Creating a request is one
// short write; it never holds a transaction while waiting for the user.
func (s *Service) RequestToolPermission(ctx context.Context, in CreatePermissionRequestInput) (w.ToolPermissionRequest, error) {
	var request w.ToolPermissionRequest
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		request, err = s.requestToolPermissionInTx(tx, in)
		return err
	})
	return request, err
}

func (s *Service) requestToolPermissionInTx(tx *store.Tx, in CreatePermissionRequestInput) (w.ToolPermissionRequest, error) {
	if in.ThreadID == "" || in.RunID == "" || in.StepID == "" || in.AttemptID == "" || in.ToolCallID == "" || in.Action == "" || in.ToolName == "" || in.MemberID == "" {
		return w.ToolPermissionRequest{}, errorf("invalid_request", "a permission request requires thread, run, step, attempt, member, call, action and tool")
	}
	// The requesting member must still be an enabled member of this workspace;
	// a stale client cannot create authority for a removed or disabled member.
	member, err := tx.WorkspaceAgent(in.WorkspaceID, in.MemberID)
	if err != nil || member.Disabled {
		return w.ToolPermissionRequest{}, errorf("authority_revoked", "the requesting member is not enabled in this workspace")
	}
	// The same invocation (run+step+attempt+tool_call) reuses its request. Native
	// ids restart at native-1 per attempt, so reusing run+call alone would bind a
	// different operation to an earlier decision.
	existing, err := tx.PermissionRequestsByInvocation(in.RunID, in.StepID, in.AttemptID, in.ToolCallID)
	if err != nil {
		return w.ToolPermissionRequest{}, err
	}
	digest := DigestArguments(in.Arguments)
	for _, candidate := range existing {
		if candidate.Status == w.PermissionPending || candidate.Status == w.PermissionAllowedOnce || candidate.Status == w.PermissionAllowedConversation {
			// Same call identity must describe the same immutable operation.
			if candidate.Action != in.Action || candidate.ToolName != in.ToolName || candidate.NormalizedResourceScope != in.NormalizedResourceScope || candidate.ArgumentsDigest != digest {
				return w.ToolPermissionRequest{}, errorf("conflict", "this invocation already has a permission request for a different operation")
			}
			return candidate, nil
		}
	}
	// A pending pre-approval for the same intended operation is reused instead of
	// asking the user twice.
	if in.PreApproval {
		pending, err := tx.PendingPreApprovals(in.RunID, in.StepID, in.AttemptID)
		if err != nil {
			return w.ToolPermissionRequest{}, err
		}
		for _, candidate := range pending {
			if candidate.Action == in.Action && candidate.ToolName == in.ToolName && candidate.NormalizedResourceScope == in.NormalizedResourceScope && candidate.ArgumentsDigest == digest {
				return candidate, nil
			}
		}
	}
	policy, err := s.CurrentPolicy(tx)
	if err != nil {
		return w.ToolPermissionRequest{}, err
	}
	now := s.now()
	request := w.ToolPermissionRequest{
		ID: w.NewID(), ThreadID: in.ThreadID, WorkspaceID: in.WorkspaceID,
		RunID: in.RunID, StepID: in.StepID, AttemptID: in.AttemptID,
		MemberID: in.MemberID, MemberName: in.MemberName, ToolCallID: in.ToolCallID,
		Origin: in.Origin, Adapter: in.Adapter, Action: in.Action, ToolName: in.ToolName,
		NormalizedResourceScope: in.NormalizedResourceScope,
		ArgumentsDigest:         digest,
		Reason:                  in.Reason, OperationSummary: in.OperationSummary, PermissionEpoch: policy.Epoch,
		PreApproval: in.PreApproval, Risk: in.Risk,
		Status: w.PermissionPending, RequestID: in.RequestID, Revision: 1, CreatedAt: now,
	}
	if err := tx.SavePermissionRequest(request); err != nil {
		return w.ToolPermissionRequest{}, err
	}
	return request, nil
}

// DecisionInput is one user decision. Scope is never taken from the body.
type DecisionInput struct {
	Decision  string `json:"decision"`
	RequestID string `json:"request_id"`
}

// DecidePermissionRequest settles a pending request exactly once. An identical
// replay returns the stored decision; a conflicting replay is rejected. The
// decision revalidates the persisted execution truth: an attempt or run that
// already ended cannot receive a late approval.
func (s *Service) DecidePermissionRequest(ctx context.Context, id string, expected int, in DecisionInput) (w.ToolPermissionRequest, error) {
	var request w.ToolPermissionRequest
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		request, err = s.decideInTx(tx, id, expected, in)
		return err
	})
	return request, err
}

func (s *Service) decideInTx(tx *store.Tx, id string, expected int, in DecisionInput) (w.ToolPermissionRequest, error) {
	request, err := tx.PermissionRequest(id)
	if err != nil {
		return request, errorf("not_found", "permission request was not found")
	}
	if !w.ValidDecision(in.Decision) {
		return request, errorf("invalid_request", "decision must be allow_once, allow_conversation or deny")
	}
	if len(in.RequestID) < 8 || len(in.RequestID) > 100 {
		return request, errorf("invalid_request", "a request_id of 8–100 characters is required for idempotent decisions")
	}
	if request.Status != w.PermissionPending {
		if request.RequestID == in.RequestID && request.Status == w.DecisionStatus(in.Decision) {
			return request, nil
		}
		return request, errorf("conflict", "permission request was already decided")
	}
	if expected > 0 && expected != request.Revision {
		return request, errorf("revision_conflict", "permission request changed; reload before deciding")
	}
	policy, err := s.CurrentPolicy(tx)
	if err != nil {
		return request, err
	}
	now := s.now()
	if request.PermissionEpoch != policy.Epoch {
		request.Status, request.ResolvedAt, request.Revision = w.PermissionExpired, now, request.Revision+1
		if err := tx.SavePermissionRequest(request); err != nil {
			return request, err
		}
		return request, errorf("revision_conflict", "permission request belongs to an earlier permission epoch and must be re-evaluated")
	}
	if err := s.validateLiveInvocation(tx, request); err != nil {
		// The invocation is gone; settle the request as canceled instead of
		// creating authority for an execution that will never run.
		request.Status, request.ResolvedAt, request.Revision = w.PermissionCanceled, now, request.Revision+1
		if err := tx.SavePermissionRequest(request); err != nil {
			return request, err
		}
		return request, err
	}
	request.Status = w.DecisionStatus(in.Decision)
	request.RequestID = in.RequestID
	request.ResolvedAt = now
	request.Revision++
	if err := tx.SavePermissionRequest(request); err != nil {
		return request, err
	}
	if in.Decision == w.DecisionAllowConversation {
		grant := w.ConversationGrant{
			ID: w.NewID(), ThreadID: request.ThreadID, Action: request.Action,
			ToolMatcher: request.ToolName, NormalizedResourceScope: request.NormalizedResourceScope,
			PermissionEpoch: policy.Epoch, SourceRequestID: request.ID, Status: "active", CreatedAt: now,
		}
		if err := tx.SaveConversationGrant(grant); err != nil {
			return request, err
		}
		// The grant is thread-wide: every other member already waiting on the
		// same operation resumes under it instead of asking a second time.
		if err := s.settleSiblingsUnderGrant(tx, grant, request.ID, now); err != nil {
			return request, err
		}
	}
	return request, nil
}

// settleSiblingsUnderGrant resolves pending requests of other members that the
// new conversation grant now covers. Scope and tool must match exactly; an
// unrelated target keeps waiting for its own decision.
func (s *Service) settleSiblingsUnderGrant(tx *store.Tx, grant w.ConversationGrant, sourceRequestID, now string) error {
	pending, err := tx.PendingPermissionRequestsMatching(grant.ThreadID, grant.Action, grant.ToolMatcher, grant.NormalizedResourceScope, grant.PermissionEpoch, sourceRequestID)
	if err != nil {
		return err
	}
	for _, sibling := range pending {
		// A waiting member only adopts the shared grant while its own execution is
		// still live; an ended attempt is canceled, never handed authority.
		if err := s.validateLiveInvocation(tx, sibling); err != nil {
			sibling.Status, sibling.ResolvedAt, sibling.Revision = w.PermissionCanceled, now, sibling.Revision+1
		} else {
			sibling.Status, sibling.ResolvedAt, sibling.Revision = w.PermissionAllowedConversation, now, sibling.Revision+1
		}
		if err := tx.SavePermissionRequest(sibling); err != nil {
			return err
		}
	}
	return nil
}

// validateLiveInvocation checks that the request still describes a live run,
// step and attempt belonging to a current task and an enabled member.
func (s *Service) validateLiveInvocation(tx *store.Tx, request w.ToolPermissionRequest) error {
	run, err := tx.Run(request.RunID)
	if err != nil {
		return errorf("authority_revoked", "the run for this permission request no longer exists")
	}
	if w.Terminal(run.Status) || run.Status == "canceling" || run.Status != "running" && run.Status != "queued" {
		return errorf("authority_revoked", "the run for this permission request is no longer active")
	}
	if run.ThreadID != request.ThreadID {
		return errorf("authority_revoked", "the permission request does not belong to this run's conversation")
	}
	if run.TaskID != "" {
		task, err := tx.Task(run.TaskID)
		if err != nil {
			return errorf("authority_revoked", "the task for this permission request no longer exists")
		}
		if task.Status == "paused" || task.Status == "canceled" || task.CurrentRevision != run.TaskRevision {
			return errorf("authority_revoked", "task control or revision no longer admits this execution")
		}
	}
	attempt, err := tx.Attempt(request.AttemptID)
	if err != nil {
		return errorf("authority_revoked", "the attempt for this permission request no longer exists")
	}
	if attempt.Status != "running" {
		return errorf("authority_revoked", "the attempt for this permission request has ended")
	}
	step, err := stepForAttempt(tx, request.RunID, request.StepID)
	if err != nil {
		return err
	}
	if step.Status != "running" {
		return errorf("authority_revoked", "the step for this permission request has ended")
	}
	member, err := tx.WorkspaceAgent(run.WorkspaceID, request.MemberID)
	if err != nil || member.Disabled {
		return errorf("authority_revoked", "the requesting member is no longer enabled in this workspace")
	}
	return nil
}

func stepForAttempt(tx *store.Tx, runID, stepID string) (w.Step, error) {
	steps, err := tx.Steps(runID)
	if err != nil {
		return w.Step{}, err
	}
	for _, step := range steps {
		if step.ID == stepID {
			return step, nil
		}
	}
	return w.Step{}, errorf("not_found", "the step for this permission request no longer exists")
}

// RevokeConversationGrant revokes one grant idempotently and closes the
// authority it was still granting: pending requests for the same operation in
// that thread are canceled, and any execution still running is reported so the
// caller can stop and verify it outside this transaction.
func (s *Service) RevokeConversationGrant(ctx context.Context, id string) (w.ConversationGrant, error) {
	var grant w.ConversationGrant
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		grant, err = tx.ConversationGrant(id)
		if err != nil {
			return errorf("not_found", "conversation grant was not found")
		}
		if grant.Status == "revoked" {
			return nil
		}
		now := s.now()
		grant.Status, grant.RevokedAt = "revoked", now
		if err := tx.SaveConversationGrant(grant); err != nil {
			return err
		}
		pending, err := tx.PendingPermissionRequestsMatching(grant.ThreadID, grant.Action, grant.ToolMatcher, grant.NormalizedResourceScope, grant.PermissionEpoch, "")
		if err != nil {
			return err
		}
		for _, request := range pending {
			request.Status, request.ResolvedAt, request.Revision = w.PermissionCanceled, now, request.Revision+1
			if err := tx.SavePermissionRequest(request); err != nil {
				return err
			}
		}
		runs, err := tx.Runs(grant.ThreadID)
		if err != nil {
			return err
		}
		affected := 0
		for _, run := range runs {
			if !w.Terminal(run.Status) && run.Status != "canceling" {
				affected++
			}
		}
		grant.AffectedActiveRuns = affected
		return nil
	})
	return grant, err
}

// ThreadPermissionRequests lists a thread's pending requests and bounded history.
func (s *Service) ThreadPermissionRequests(ctx context.Context, threadID string, limit int) ([]w.ToolPermissionRequest, error) {
	items := []w.ToolPermissionRequest{}
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		if _, err := tx.Thread(threadID); err != nil {
			return err
		}
		var err error
		items, err = tx.ThreadPermissionRequests(threadID, limit)
		return err
	})
	return items, err
}

// ThreadConversationGrants lists a thread's conversation grants.
func (s *Service) ThreadConversationGrants(ctx context.Context, threadID string) ([]w.ConversationGrant, error) {
	items := []w.ConversationGrant{}
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		if _, err := tx.Thread(threadID); err != nil {
			return err
		}
		var err error
		items, err = tx.ThreadConversationGrants(threadID)
		return err
	})
	return items, err
}

// AccessDecision is the outcome of the pre-execution admission check.
type AccessDecision struct {
	// Allowed means the tool may execute now without a user decision.
	Allowed bool
	// Request is the pending request the caller must wait on.
	Request *w.ToolPermissionRequest
	// Settlement is an existing admission for an already-consumed one-time
	// allowance. The caller must not execute again.
	Settlement *w.ToolInvocationAdmission
	// Requestable reports whether the caller may create a request for this call.
	Requestable bool
	// GrantID records the conversation grant that admitted the call.
	GrantID string
}

// AccessRequest is one concrete tool call under evaluation.
type AccessRequest struct {
	Run     w.Run
	StepID  string
	Attempt string
	CallID  string
	Name    string
	// Arguments is the exact tool input; the digest binds an allowance to it.
	Arguments json.RawMessage
	// Scope is the host-normalized target of the effect (absolute path, or a
	// command working directory). It is never taken from model text alone.
	Scope string
	// Risk is a non-empty grading fact an adapter proved about this exact target
	// (currently "hardlink"). A risky target is never auto-allowed, even when it
	// is inside the project; it needs an explicit concrete decision.
	Risk string
}

// CheckToolAccess decides whether one concrete tool call may run. It performs a
// single read; creation and consumption happen in their own short transactions
// so a waiter never holds the database.
//
// The live global mode and permission epoch are authoritative: a frozen
// full_access Run cannot keep admitting effects after the policy was tightened.
func (s *Service) CheckToolAccess(ctx context.Context, in AccessRequest) (AccessDecision, error) {
	decision := AccessDecision{}
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		policy, err := s.CurrentPolicy(tx)
		if err != nil {
			return err
		}
		action := w.ActionForTool(in.Name)
		if action == "" {
			return nil
		}
		// A run that predates the authorization snapshot has no permission
		// identity to bind to. It keeps its historical behavior; every new run
		// (including a resumed one) is re-authorized and gets a snapshot.
		if in.Run.GrantSnapshot == "" {
			decision.Allowed = true
			return nil
		}
		// The live mode and epoch decide the boundary, not the frozen snapshot.
		// An attempt admitted under an older policy boundary is never
		// re-authorized in place: a permission change must not silently widen a
		// running attempt, and the user must not be asked to approve an action the
		// old attempt was never allowed to take. A new execution is required.
		if in.Run.PermissionEpoch != policy.Epoch || in.Run.RequestedExecutionMode != policy.Mode {
			return errorf("authority_revoked", "the permission mode changed after this execution started; finish or restart it under the current policy")
		}
		// The frozen Run action list is the task/role ceiling. It is separate
		// from the live global mode: a controlled task that explicitly allows an
		// effect still needs the effect to be requestable even though
		// ModeActions(controlled) does not name it. An action the task itself
		// never admitted is a hard role ceiling and can never be escalated.
		taskAdmits := slices.Contains(in.Run.GrantActions, action)
		if !taskAdmits && action != "execute" {
			// The task/role ceiling blocks file/test scope escalation. Command
			// execution is a mode-level capability: a controlled free task that
			// never named "execute" must still be able to ask for one concrete
			// command instead of being permanently unable to request it.
			return nil
		}
		if !taskAdmits && !slices.Contains(w.RequestableActions(policy.Mode), action) {
			return nil
		}
		// A capability the accepted task never admitted is never auto-run; it may
		// only go through an explicit decision.
		needsDecision := !taskAdmits
		requestedScope := in.Scope
		if requestedScope == "" {
			requestedScope = normalizeRunRoot(in.Run)
		}
		// 1. The concrete target is inside the live mode boundary. A target the
		// adapter graded as risky is never auto-allowed: the user must accept that
		// exact operation.
		if !needsDecision && in.Risk == "" && s.insideProjectBoundary(in.Run, action, in.Name, requestedScope) {
			decision.Allowed = true
			return nil
		}
		// 2. A pending request for this exact invocation takes priority.
		requests, err := tx.PermissionRequestsByInvocation(in.Run.ID, in.StepID, in.Attempt, in.CallID)
		if err != nil {
			return err
		}
		for _, request := range requests {
			if request.Status != w.PermissionPending {
				continue
			}
			held := request
			decision.Request = &held
			return nil
		}
		// 3. An active conversation grant for this exact tool, action and scope.
		// A risky target still needs a decision that acknowledged the risk.
		if !needsDecision && in.Risk == "" {
			grant, err := tx.ActiveConversationGrant(in.Run.ThreadID, in.Name, action, requestedScope, policy.Epoch)
			if err == nil {
				decision.Allowed, decision.GrantID = true, grant.ID
				return nil
			}
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		// 3b. An unconsumed pre-approval for exactly this operation. The call
		// identity differs from the request tool's own call, so the binding is
		// the immutable operation: action + tool + scope + argument digest.
		digest := DigestArguments(in.Arguments)
		preApprovals, err := tx.AllowedOncePreApprovals(in.Run.ID, in.StepID, in.Attempt, action, in.Name, requestedScope, digest, policy.Epoch)
		if err != nil {
			return err
		}
		for _, candidate := range preApprovals {
			if _, err := tx.AdmissionByRequest(candidate.ID); err == nil {
				// Already consumed by an earlier call; it cannot admit another.
				continue
			}
			if !slices.Contains(in.Run.GrantActions, action) {
				return nil
			}
			held := candidate
			decision.Request = &held
			return nil
		}
		// 4. A settled one-time allowance for this exact invocation.
		for _, request := range requests {
			if request.Status != w.PermissionAllowedOnce || request.PermissionEpoch != policy.Epoch || request.ArgumentsDigest != digest || request.Revision < 2 {
				continue
			}
			record, err := tx.AdmissionByRequestCall(request.ID, in.CallID)
			if err == nil {
				held := record
				decision.Settlement = &held
				return nil
			}
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			decision.Allowed = true
			return nil
		}
		// 5. Nothing admits it: a request may be created for this action class.
		decision.Requestable = slices.Contains(w.RequestableActions(policy.Mode), action)
		return nil
	})
	return decision, err
}

// insideProjectBoundary reports whether an action's concrete target is inside
// the live mode's boundary. The comparison is on resolved real paths, so a
// symlink, a `..` segment or a sibling prefix cannot be mistaken for the
// project tree.
func (s *Service) insideProjectBoundary(run w.Run, action, name, scope string) bool {
	root := normalizeRunRoot(run)
	if root == "" && run.TaskDirectory == "" {
		// No bound directory: there is no project tree to escape. Reads, network
		// and collaboration stay available; every file/command effect still
		// needs an explicit scope decision (write/run_test/execute are refused by
		// the caller's tool gating when no directory is bound).
		switch action {
		case "read", "network", "collaborate", "publish_artifact":
			return true
		}
		return false
	}
	switch run.EffectiveExecutionMode {
	case w.ExecutionFullAccess:
		return true
	case w.ExecutionProjectAutonomous:
		switch action {
		case "collaborate":
			return true
		case "network":
			return scope == "network:public" || strings.HasPrefix(scope, "http://") || strings.HasPrefix(scope, "https://")
		case "publish_artifact":
			return isLocalPublish(name)
		case "read", "write", "run_test":
			return s.scopeInsideProject(run, scope)
		case "execute":
			return s.scopeInsideProject(run, scope)
		}
		return false
	default:
		// The controlled mode keeps the existing standard host template: in-project
		// reads, writes and tests are admitted subject to the role/task checks
		// above; anything outside the project, and every command, needs an
		// approval. That is the "受控规则 + 不足时申请" contract.
		switch action {
		case "collaborate":
			return true
		case "network":
			return scope == "network:public" || strings.HasPrefix(scope, "http://") || strings.HasPrefix(scope, "https://")
		case "publish_artifact":
			return isLocalPublish(name)
		case "read", "write", "run_test":
			return s.scopeInsideProject(run, scope)
		}
		// An external publish/deploy is never inside the project boundary.
		return false
	}
}

// scopeInsideProject compares normalized real paths against the run's project
// and task directories, never a string prefix.
func (s *Service) scopeInsideProject(run w.Run, scope string) bool {
	roots := []string{normalizeRunRoot(run)}
	if roots[0] == "" && run.TaskDirectory == "" {
		// No bound tree: a read of a captured source or a relative target has no
		// project boundary to cross, so it is not an outside effect.
		if scope != "" {
			return true
		}
		return false
	}
	if scope == "" {
		return false
	}
	if run.TaskDirectory != "" {
		roots = append(roots, w.NormalizeScope(run.TaskDirectory, run.TaskDirectory))
	}
	for _, root := range roots {
		if root != "" && w.ScopeContains(root, scope) {
			return true
		}
	}
	return false
}

func normalizeRunRoot(run w.Run) string {
	root := run.ProjectRoot()
	if root == "" {
		return ""
	}
	return w.NormalizeScope(root, root)
}

// isLocalPublish separates host-internal persist/deliver tools from an external
// publish or deploy effect. These tools only write to the host's own store or
// publish immutable bytes into this conversation; they never reach an external
// destination, so they are not an outside effect.
func isLocalPublish(name string) bool {
	switch name {
	case "publish_artifact", "publish_file", "save_claim", "save_memory", "save_dataset", "analyze_csv":
		return true
	}
	return false
}

// ConsumeAllowance records the durable admission for an allowed one-time
// request. The caller's invocation must match the request exactly; a mismatch is
// an error, never another authorization.
func (s *Service) ConsumeAllowance(ctx context.Context, requestID string, run w.Run, stepID, attemptID, toolCallID string) (w.ToolInvocationAdmission, bool, error) {
	admission := w.ToolInvocationAdmission{}
	created := false
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.PermissionRequest(requestID)
		if err != nil {
			return err
		}
		if current.Status != w.PermissionAllowedOnce {
			return errorf("authority_revoked", "the one-time allowance is no longer valid")
		}
		if current.RunID != run.ID || current.StepID != stepID || current.AttemptID != attemptID || current.ToolCallID != toolCallID {
			return ErrInvocationMismatch
		}
		policy, err := s.CurrentPolicy(tx)
		if err != nil {
			return err
		}
		if current.PermissionEpoch != policy.Epoch {
			return errorf("revision_conflict", "the allowance belongs to an earlier permission epoch")
		}
		candidate := w.ToolInvocationAdmission{
			ID: w.NewID(), PermissionRequestID: current.ID, RunID: run.ID,
			StepID: stepID, AttemptID: attemptID, ToolCallID: toolCallID,
			ArgumentsDigest: current.ArgumentsDigest, PermissionEpoch: policy.Epoch,
			Status: w.AdmissionReserved, ReservedAt: s.now(),
		}
		ok, err := tx.ReserveAdmission(candidate)
		if err != nil {
			return err
		}
		if !ok {
			existing, err := tx.AdmissionByRequestCall(current.ID, toolCallID)
			if err != nil {
				return err
			}
			if existing.RunID != run.ID || existing.StepID != stepID || existing.AttemptID != attemptID {
				return ErrInvocationMismatch
			}
			admission = existing
			return nil
		}
		admission, created = candidate, true
		return nil
	})
	return admission, created, err
}

// ConsumePreApproval consumes an intent-bound pre-approval for the real call.
// The operation is bound by run/step/attempt plus the exact action, tool, scope
// and argument digest the user approved; the call identity belongs to the real
// call and is recorded on the admission.
func (s *Service) ConsumePreApproval(ctx context.Context, requestID string, run w.Run, stepID, attemptID, toolCallID, action, toolName, scope string, arguments json.RawMessage) (w.ToolInvocationAdmission, bool, error) {
	admission := w.ToolInvocationAdmission{}
	created := false
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.PermissionRequest(requestID)
		if err != nil {
			return err
		}
		if current.Status != w.PermissionAllowedOnce || !current.PreApproval {
			return errorf("authority_revoked", "the one-time allowance is no longer valid")
		}
		digest := DigestArguments(arguments)
		if current.RunID != run.ID || current.StepID != stepID || current.AttemptID != attemptID ||
			current.Action != action || current.ToolName != toolName || current.NormalizedResourceScope != scope ||
			current.ArgumentsDigest != digest {
			return ErrInvocationMismatch
		}
		policy, err := s.CurrentPolicy(tx)
		if err != nil {
			return err
		}
		if current.PermissionEpoch != policy.Epoch {
			return errorf("revision_conflict", "the allowance belongs to an earlier permission epoch")
		}
		candidate := w.ToolInvocationAdmission{
			ID: w.NewID(), PermissionRequestID: current.ID, RunID: run.ID,
			StepID: stepID, AttemptID: attemptID, ToolCallID: toolCallID,
			ArgumentsDigest: current.ArgumentsDigest, PermissionEpoch: policy.Epoch,
			Status: w.AdmissionReserved, ReservedAt: s.now(),
		}
		ok, err := tx.ReserveAdmission(candidate)
		if err != nil {
			return err
		}
		if !ok {
			existing, err := tx.AdmissionByRequest(current.ID)
			if err != nil {
				return err
			}
			admission = existing
			return nil
		}
		admission, created = candidate, true
		return nil
	})
	return admission, created, err
}

// SettleAdmission records the terminal state of a reserved admission. A crash
// leaves reserved/started rows behind, which settle as unknown and never replay.
func (s *Service) SettleAdmission(ctx context.Context, id, status, resultRef string) error {
	return s.store.Write(ctx, func(tx *store.Tx) error {
		admission, err := tx.Admission(id)
		if err != nil {
			return err
		}
		if admission.Status == w.AdmissionCompleted || admission.Status == w.AdmissionFailed || admission.Status == w.AdmissionUnknown {
			return nil
		}
		admission.Status, admission.ResultRef, admission.SettledAt = status, resultRef, s.now()
		return tx.SaveAdmission(admission)
	})
}

// MarkAdmissionStarted records that the effect began, so a crash is visible as
// "started without a result" instead of a reusable allowance.
func (s *Service) MarkAdmissionStarted(ctx context.Context, id string) error {
	return s.store.Write(ctx, func(tx *store.Tx) error {
		admission, err := tx.Admission(id)
		if err != nil {
			return err
		}
		if admission.Status != w.AdmissionReserved {
			return nil
		}
		admission.Status, admission.StartedAt = w.AdmissionStarted, s.now()
		return tx.SaveAdmission(admission)
	})
}

// WaitForDecision blocks without holding a database transaction until the
// request is settled, the epoch changes, or the context ends. Each poll is a
// bounded read; the workflow treats this state as activity, not silence.
func (s *Service) WaitForDecision(ctx context.Context, id string, interval time.Duration) (w.ToolPermissionRequest, error) {
	if interval <= 0 {
		interval = 150 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		var request w.ToolPermissionRequest
		var expired, granted bool
		err := s.store.Read(ctx, func(tx *store.Tx) error {
			var err error
			request, err = tx.PermissionRequest(id)
			if err != nil {
				return err
			}
			if request.Status != w.PermissionPending {
				return nil
			}
			policy, err := s.CurrentPolicy(tx)
			if err != nil {
				return err
			}
			expired = request.PermissionEpoch != policy.Epoch
			if expired {
				return nil
			}
			// Another member's conversation grant in the same thread covers this
			// exact operation. It is only a hint here; the write below revalidates
			// everything inside one transaction before changing any state.
			if _, err := tx.ActiveConversationGrant(request.ThreadID, request.ToolName, request.Action, request.NormalizedResourceScope, policy.Epoch); err == nil {
				granted = true
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			return nil
		})
		if err != nil {
			return request, err
		}
		if request.Status != w.PermissionPending {
			return request, nil
		}
		if expired {
			return request, ErrPermissionExpired
		}
		if granted {
			// Adoption happens under a write transaction that revalidates the
			// grant, the live epoch and the execution itself, so a concurrent
			// revocation or stop cannot be resurrected by a stale read.
			var settled w.ToolPermissionRequest
			if err := s.store.Write(ctx, func(tx *store.Tx) error {
				current, err := tx.PermissionRequest(id)
				if err != nil {
					return err
				}
				if current.Status != w.PermissionPending {
					settled = current
					return nil
				}
				policy, err := s.CurrentPolicy(tx)
				if err != nil {
					return err
				}
				if current.PermissionEpoch != policy.Epoch {
					current.Status, current.ResolvedAt, current.Revision = w.PermissionExpired, s.now(), current.Revision+1
					settled = current
					return tx.SavePermissionRequest(current)
				}
				_, err = tx.ActiveConversationGrant(current.ThreadID, current.ToolName, current.Action, current.NormalizedResourceScope, policy.Epoch)
				if errors.Is(err, store.ErrNotFound) {
					// The grant was revoked between the read and this write; keep
					// waiting instead of inventing authority.
					settled = current
					return nil
				}
				if err != nil {
					return err
				}
				if err := s.validateLiveInvocation(tx, current); err != nil {
					current.Status, current.ResolvedAt, current.Revision = w.PermissionCanceled, s.now(), current.Revision+1
					settled = current
					return tx.SavePermissionRequest(current)
				}
				current.Status, current.ResolvedAt, current.Revision = w.PermissionAllowedConversation, s.now(), current.Revision+1
				settled = current
				return tx.SavePermissionRequest(current)
			}); err != nil {
				return request, err
			}
			if settled.Status != w.PermissionPending {
				return settled, nil
			}
		}
		select {
		case <-ctx.Done():
			return request, ctx.Err()
		case <-ticker.C:
		}
	}
}

// CancelRunPermissionRequests settles a run's pending requests when execution
// ends, so nothing stays actionable after its attempt is gone.
func (s *Service) CancelRunPermissionRequests(ctx context.Context, runID string) error {
	return s.store.Write(ctx, func(tx *store.Tx) error {
		items, err := tx.RunPendingPermissionRequests(runID)
		if err != nil {
			return err
		}
		for _, request := range items {
			request.Status, request.ResolvedAt, request.Revision = w.PermissionCanceled, s.now(), request.Revision+1
			if err := tx.SavePermissionRequest(request); err != nil {
				return err
			}
		}
		return nil
	})
}

// RevokeThreadGrants revokes every active grant of a thread.
func (s *Service) RevokeThreadGrants(ctx context.Context, threadID string) error {
	return s.store.Write(ctx, func(tx *store.Tx) error {
		return tx.RevokeConversationGrantsForThread(threadID, s.now())
	})
}
