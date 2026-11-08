package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// nativeJourney reuses the host journey fixture and answers the native callback
// the way the adapters do.
func newNativeJourney(t *testing.T) *hostApprovalJourney {
	t.Helper()
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	return j
}

// TestNativeAuthorizerBindsApprovalToTheRealCall proves the native callback uses
// the model contract: an unapproved outside write creates a durable request, a
// denial returns Scope=deny with no effect, and an allow returns Scope=once with
// the normalized ResourceScope. The adapter never receives a path in Scope.
func TestNativeAuthorizerBindsApprovalToTheRealCall(t *testing.T) {
	j := newNativeJourney(t)
	authorize := j.runtime.nativeAuthorizer()
	input, err := json.Marshal(map[string]any{"file_path": j.outside, "content": "native payload"})
	if err != nil {
		t.Fatal(err)
	}
	j.queueDecision(w.DecisionDeny)
	denied, err := authorize(context.Background(), model.NativePermissionRequest{
		CallID: "native-1", Name: "Write", Input: input, Resource: j.outside,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allow || denied.Scope != model.NativeScopeDeny {
		t.Fatalf("denied native decision = %+v", denied)
	}
	if denied.ResourceScope != "" {
		t.Fatalf("denied decision leaked a scope: %+v", denied)
	}
	if _, err := os.Stat(j.outside); err == nil {
		t.Fatal("denied native effect created the file")
	}

	j.queueDecision(w.DecisionAllowOnce)
	allowed, err := authorize(context.Background(), model.NativePermissionRequest{
		CallID: "native-2", Name: "Write", Input: input, Resource: j.outside,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed.Allow || allowed.Scope != model.NativeScopeOnce {
		t.Fatalf("allowed native decision = %+v", allowed)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Dir(j.outside))
	if err != nil {
		t.Fatal(err)
	}
	wantScope := filepath.Join(resolved, filepath.Base(j.outside))
	if allowed.ResourceScope != wantScope {
		t.Fatalf("ResourceScope = %q, want %q", allowed.ResourceScope, wantScope)
	}
	// The adapter has not executed yet; the approval is only a decision. A second
	// request for the same operation must ask again.
	j.queueDecision(w.DecisionDeny)
	second, err := authorize(context.Background(), model.NativePermissionRequest{
		CallID: "native-3", Name: "Write", Input: input, Resource: j.outside,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Allow {
		t.Fatal("the second native call reused the first click")
	}
}

// TestNativeAuthorizerDeniesInsideProjectTargetsForControlledMode proves the
// controlled mode still allows an in-project write without a request, because the
// standard host template applies.
func TestNativeAuthorizerAllowsInProjectWriteWithoutRequest(t *testing.T) {
	j := newNativeJourney(t)
	target := filepath.Join(j.root, "inside.txt")
	input, err := json.Marshal(map[string]any{"file_path": target})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := j.runtime.nativeAuthorizer()(context.Background(), model.NativePermissionRequest{
		CallID: "native-in", Name: "Write", Input: input, Resource: target,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || decision.Scope != model.NativeScopeOnce {
		t.Fatalf("in-project write was not admitted: %+v", decision)
	}
	if decision.ResourceScope == "" {
		t.Fatal("admitted decision has no approved scope")
	}
}

// TestNativeAuthorizerRejectsStalePolicyEpoch proves a Run admitted under an older
// permission epoch cannot be authorized after the live policy changed.
func TestNativeAuthorizerRejectsStalePolicyEpoch(t *testing.T) {
	j := newNativeJourney(t)
	// Tighten the live policy and advance the epoch behind the Run.
	var current w.Settings
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	controlled := w.ExecutionControlled
	if _, err := j.service.UpdateSettings(t.Context(), current.Revision, project.SettingsPatch{ExecutionMode: &controlled}); err != nil {
		t.Fatal(err)
	}
	// Bump again so the Run's frozen epoch is two behind.
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	projectAutonomous := w.ExecutionProjectAutonomous
	if _, err := j.service.UpdateSettings(t.Context(), current.Revision, project.SettingsPatch{ExecutionMode: &projectAutonomous}); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"file_path": j.outside})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := j.runtime.nativeAuthorizer()(context.Background(), model.NativePermissionRequest{
		CallID: "native-stale", Name: "Write", Input: input, Resource: j.outside,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allow {
		t.Fatalf("a stale epoch was authorized: %+v", decision)
	}
	if decision.Scope != model.NativeScopeDeny {
		t.Fatalf("a stale epoch must be a hard denial: %+v", decision)
	}
	// A stale attempt must not create a fresh request; the user cannot widen an
	// attempt that started under the old policy.
	var pending []w.ToolPermissionRequest
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		pending, err = tx.PendingPreApprovalsOrRequests(j.run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("stale epoch created a request: %+v", pending)
	}
}

// TestSettleAdmissionUsesTheRealOutcome proves the per-call settlement never
// reports success for a failed or canceled effect.
func TestSettleAdmissionUsesTheRealOutcome(t *testing.T) {
	j := newNativeJourney(t)
	// Reserve an admission through a request/decision/consume cycle.
	run := j.runtime.snapshotRun()
	request, err := j.service.RequestToolPermission(t.Context(), project.CreatePermissionRequestInput{
		ThreadID: run.ThreadID, WorkspaceID: run.WorkspaceID, RunID: run.ID,
		StepID: j.step.ID, AttemptID: j.attempt.ID, MemberID: "member", MemberName: "成员",
		ToolCallID: "settle-call", Origin: w.OriginHost, Action: "write", ToolName: "write_file",
		NormalizedResourceScope: j.outside, Arguments: json.RawMessage(`{"path":"x"}`), Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	decided, err := j.service.DecidePermissionRequest(t.Context(), request.ID, request.Revision, project.DecisionInput{
		Decision: w.DecisionAllowOnce, RequestID: "settle-decision",
	})
	if err != nil {
		t.Fatal(err)
	}
	admission, created, err := j.service.ConsumeAllowance(t.Context(), decided.ID, run, j.step.ID, j.attempt.ID, "settle-call")
	if err != nil || !created {
		t.Fatalf("consume = %+v %v %v", admission, created, err)
	}
	// failed outcome
	j.runtime.settleAdmission(context.Background(), &admission, "", errFailed)
	stored := j.readAdmission(t, admission.ID)
	if stored.Status == w.AdmissionCompleted {
		t.Fatalf("failed effect settled as completed: %+v", stored)
	}
}

// errFailed is a stable sentinel for settlement tests.
var errFailed = &settleError{}

type settleError struct{}

func (*settleError) Error() string { return "failed" }

// readAdmission reads one admission through a short read transaction.
func (j *hostApprovalJourney) readAdmission(t *testing.T, id string) w.ToolInvocationAdmission {
	t.Helper()
	admission, err := j.db.ReadAdmission(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return admission
}

// TestNativeRiskNeverAutoAllowed is the risk acceptance: a target the adapter
// graded as a hardlink is not auto-allowed even inside the project, and only a
// real user decision sets AcknowledgedRisk.
func TestNativeRiskNeverAutoAllowed(t *testing.T) {
	j := newNativeJourney(t)
	target := filepath.Join(j.root, "linked.txt")
	input, err := json.Marshal(map[string]any{"file_path": target})
	if err != nil {
		t.Fatal(err)
	}
	authorize := j.runtime.nativeAuthorizer()
	// Denied: no effect, no risk acknowledgement.
	j.queueDecision(w.DecisionDeny)
	denied, err := authorize(context.Background(), model.NativePermissionRequest{
		CallID: "native-risk-1", Name: "Write", Input: input, Resource: target,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write", Risk: "hardlink",
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allow || denied.AcknowledgedRisk {
		t.Fatalf("risky call was admitted or acknowledged without a decision: %+v", denied)
	}
	// Allowed once: the user accepted this exact risk.
	j.queueDecision(w.DecisionAllowOnce)
	allowed, err := authorize(context.Background(), model.NativePermissionRequest{
		CallID: "native-risk-2", Name: "Write", Input: input, Resource: target,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write", Risk: "hardlink",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed.Allow || !allowed.AcknowledgedRisk || allowed.Scope != model.NativeScopeOnce {
		t.Fatalf("risky call was not admitted after a decision: %+v", allowed)
	}
	// A non-risky in-project target still auto-allows and never acknowledges.
	plain := filepath.Join(j.root, "plain.txt")
	plainInput, err := json.Marshal(map[string]any{"file_path": plain})
	if err != nil {
		t.Fatal(err)
	}
	auto, err := authorize(context.Background(), model.NativePermissionRequest{
		CallID: "native-plain", Name: "Write", Input: plainInput, Resource: plain,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !auto.Allow || auto.AcknowledgedRisk {
		t.Fatalf("plain in-project call = %+v", auto)
	}
}

// TestFreeFileTaskAutonomousNativeWriteRuns is the autonomous file-task
// acceptance: a free (non-engineering) task in project_autonomous must authorize
// a native Write inside the project without a human prompt and actually produce
// the bytes. TaskActions must therefore include the write capability for this
// mode; otherwise the native callback maps Write->write_file->write and the task
// ceiling would refuse it.
func TestFreeFileTaskAutonomousNativeWriteRuns(t *testing.T) {
	db, service, th, member, root := newFreeTaskFixture(t)
	// Select project_autonomous through the real service path.
	var current w.Settings
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mode := w.ExecutionProjectAutonomous
	if _, err := service.UpdateSettings(t.Context(), current.Revision, project.SettingsPatch{ExecutionMode: &mode}); err != nil {
		t.Fatal(err)
	}
	// A plain free file task created without explicit allowed actions.
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: th.ID, OwnerMemberID: member.ID,
		Goal: "写一个文件到项目里", SourceMessageID: "free-file", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(created.Revision.AllowedActions, "write") {
		t.Fatalf("autonomous free task lacks the write capability: %+v", created.Revision.AllowedActions)
	}
	run := w.Run{
		ID: "free-file-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: created.Task.CurrentRevision, BudgetAccountID: created.Budget.ID,
		Input:  w.RunInput{Message: "写一个文件到项目里", Workflow: "free", Targets: []string{member.ID}},
		Agents: []w.Agent{member}, CoordinatorID: member.ID, Status: "running", CreatedAt: w.Now(),
	}
	step := w.Step{ID: "free-file-step", RunID: run.ID, AgentID: member.ID, Role: "responder", Status: "running"}
	attempt := w.Attempt{ID: "free-file-attempt", RunID: run.ID, StepID: step.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		live, err := tx.Workspace(th.WorkspaceID)
		if err != nil {
			return err
		}
		run.Workspace = live
		if err := service.EnsureRunAuthorization(tx, &run, "free-file"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "free-file-key", "free-file-digest"); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "generated.txt")
	payload := []byte("native free file task bytes")
	input, err := json.Marshal(map[string]any{"file_path": target, "content": string(payload)})
	if err != nil {
		t.Fatal(err)
	}
	service2 := project.New(db)
	runtime := &stepRuntime{ctx: t.Context(), engine: &Engine{Store: db, AllowedRoots: []string{root}}, run: run, step: step, agent: member, attempt: attempt, allowed: w.StandardToolCatalog(), nativeCaptured: map[string]bool{}}
	decision, err := runtime.nativeAuthorizer()(context.Background(), model.NativePermissionRequest{
		CallID: "native-write-1", Name: "Write", Input: input, Resource: target,
		Origin: w.OriginNative, Adapter: "claude", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || decision.Scope != model.NativeScopeOnce {
		t.Fatalf("autonomous native write was not admitted: %+v", decision)
	}
	// No human request was needed.
	var pending []w.ToolPermissionRequest
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		pending, err = tx.PendingPreApprovalsOrRequests(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("autonomous in-project write created a request: %+v", pending)
	}
	// Simulate the adapter performing the approved effect with exactly those bytes.
	if err := os.WriteFile(decision.ResourceScope, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != string(payload) {
		t.Fatalf("approved native write bytes = %q %v", data, err)
	}
	_ = service2
}

// TestControlledFreeTaskCanRequestCommand proves a controlled free task that never
// named "execute" can still ask for one concrete command (mode-level requestable
// capability), while an in-project file write stays governed by the task ceiling.
func TestControlledFreeTaskCanRequestCommand(t *testing.T) {
	db, service, th, member, root := newFreeTaskFixture(t)
	// The controlled mode is the one where a command always needs a decision.
	mode := w.ExecutionControlled
	var current w.Settings
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSettings(t.Context(), current.Revision, project.SettingsPatch{ExecutionMode: &mode}); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: th.ID, OwnerMemberID: member.ID,
		Goal: "运行一个命令", SourceMessageID: "free-command", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "free-command-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: created.Task.CurrentRevision, BudgetAccountID: created.Budget.ID,
		Input:  w.RunInput{Message: "运行一个命令", Workflow: "free", Targets: []string{member.ID}},
		Agents: []w.Agent{member}, CoordinatorID: member.ID, Status: "running", CreatedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		live, err := tx.Workspace(th.WorkspaceID)
		if err != nil {
			return err
		}
		run.Workspace = live
		if err := service.EnsureRunAuthorization(tx, &run, "free-command"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		return tx.InsertRun(run, "free-command-key", "free-command-digest")
	}); err != nil {
		t.Fatal(err)
	}
	decision, err := service.CheckToolAccess(t.Context(), project.AccessRequest{
		Run: run, StepID: "s", Attempt: "a", CallID: "cmd-call",
		Name: "run_command", Arguments: json.RawMessage(`{"argv":["/bin/echo","hi"]}`), Scope: w.NormalizeScope(root, root),
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed {
		t.Fatal("controlled mode auto-ran a command")
	}
	if !decision.Requestable {
		t.Fatalf("controlled free task could not request a command: %+v", decision)
	}
}

// newFreeTaskFixture seeds one controlled workspace with a bound root, one member
// and one conversation; the caller creates the task of its scenario.
func newFreeTaskFixture(t *testing.T) (*store.Store, *project.Service, w.Thread, w.Agent, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	ws := w.Workspace{ID: "free-ws", Name: "自由任务", Root: root, Revision: 1, CreatedAt: w.Now()}
	member := w.Agent{ID: "owner", WorkspaceID: ws.ID, DisplayName: "Owner", Handle: "owner", Model: "default", Executor: "claude", Revision: 1}
	th := w.Thread{ID: "free-th", WorkspaceID: ws.ID, Title: "自由任务", CoordinatorID: member.ID, AgentIDs: []string{member.ID}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveMember(w.WorkspaceMember{WorkspaceID: ws.ID, AgentID: member.ID, Enabled: true, Revision: 1}); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	return db, project.New(db), th, member, root
}

// TestReviewerStepCannotGainNativeWrite proves the step role is an independent
// hard ceiling: in the same run and with the same conversation grant, an engineer
// may write while a reviewer's Write/Bash callback is denied with no effect and
// no approval prompt.
func TestReviewerStepCannotGainNativeWrite(t *testing.T) {
	j := newNativeJourney(t)
	target := filepath.Join(j.root, "review-guard.txt")
	input, err := json.Marshal(map[string]any{"file_path": target, "content": "guarded"})
	if err != nil {
		t.Fatal(err)
	}
	// A reviewer step in the same run: allowed tools exclude write_file.
	reviewer := j.runtime
	reviewer.step = w.Step{ID: "review-step", RunID: j.run.ID, AgentID: j.runtime.agent.ID, Role: "reviewer", Status: "running"}
	reviewer.attempt = w.Attempt{ID: "review-attempt", RunID: j.run.ID, StepID: "review-step", Number: 1, Status: "running", StartedAt: w.Now()}
	reviewer.allowed = []string{"read_file", "run_test", "publish_artifact"}
	j.queueDecision(w.DecisionAllowOnce)
	decision, err := reviewer.nativeAuthorizer()(context.Background(), model.NativePermissionRequest{
		CallID: "review-native-1", Name: "Write", Input: input, Resource: target,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allow {
		t.Fatalf("reviewer step gained a native write: %+v", decision)
	}
	// No approval prompt was created for a denied role ceiling.
	var pending []w.ToolPermissionRequest
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		pending, err = tx.PendingPreApprovalsOrRequests(j.run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("reviewer denial created a request: %+v", pending)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("reviewer effect was written")
	}
	// The engineer profile in the same run is unaffected.
	engineer := j.runtime
	engineer.allowed = w.StandardToolCatalog()
	allowedDecision, err := engineer.nativeAuthorizer()(context.Background(), model.NativePermissionRequest{
		CallID: "engineer-native-1", Name: "Write", Input: input, Resource: target,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !allowedDecision.Allow {
		t.Fatalf("engineer native write was denied: %+v", allowedDecision)
	}
}
