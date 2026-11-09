package project

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// These are behavioral review regressions, intended to be copied next to
// permissions.go after the integration APIs settle. They reuse setup from
// service_test.go, and operate only on t.TempDir SQLite/filesystem fixtures.
// No native CLI or real provider is invoked.

type reviewPermissionFixture struct {
	db      *store.Store
	service *Service
	run     w.Run
	step    w.Step
	attempt w.Attempt
	root    string
	outside string
}

func newReviewPermissionFixture(t *testing.T, mode string) reviewPermissionFixture {
	t.Helper()
	db, service, thread := setup(t)
	root, outside := t.TempDir(), filepath.Join(t.TempDir(), "external.txt")
	member := w.Agent{
		ID: "owner", WorkspaceID: thread.WorkspaceID, DisplayName: "Owner",
		Handle: "owner", Revision: 1,
		Tools: []string{"list_files", "read_file", "write_file", "patch_file", "run_test", "run_command", "publish_artifact", "publish_file", "web_search"},
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		workspace, err := tx.Workspace(thread.WorkspaceID)
		if err != nil {
			return err
		}
		workspace.Root = root
		if err := tx.SaveWorkspace(workspace); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		settings.ExecutionMode = mode
		settings.Revision++
		return tx.SaveSettings(settings)
	}); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: thread.WorkspaceID, ThreadID: thread.ID,
		OwnerMemberID: member.ID, Goal: "Execute the explicitly authorized review fixture",
		AllowedActions:  []string{"read", "write", "run_test", "execute", "network", "collaborate", "publish_artifact", "native_read", "native_write", "native_execute"},
		SourceMessageID: "review-user-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "review-run", WorkspaceID: thread.WorkspaceID, ThreadID: thread.ID,
		TaskID: created.Task.ID, TaskRevision: created.Task.CurrentRevision,
		BudgetAccountID: created.Budget.ID,
		Input:           w.RunInput{Workflow: "engineering", Message: "Execute the fixture inside its explicit permission boundary", Assignments: map[string]string{"engineer": member.ID}},
		Agents:          []w.Agent{member}, CoordinatorID: member.ID,
		Status: "running", CreatedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		workspace, err := tx.Workspace(thread.WorkspaceID)
		if err != nil {
			return err
		}
		run.Workspace = workspace
		if err := service.EnsureRunAuthorization(tx, &run, "review-user-message"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		return tx.InsertRun(run, "review-run-key", "review-run-digest")
	}); err != nil {
		t.Fatal(err)
	}
	f := reviewPermissionFixture{db: db, service: service, run: run, root: root, outside: outside}
	f.step, f.attempt = reviewSaveStepAttempt(t, db, run, "review-step", "review-attempt", 1)
	return f
}

func reviewSaveStepAttempt(t *testing.T, db *store.Store, run w.Run, stepID, attemptID string, number int) (w.Step, w.Attempt) {
	t.Helper()
	step := w.Step{ID: stepID, RunID: run.ID, AgentID: "owner", AgentName: "Owner", Role: "engineer", Status: "running"}
	attempt := w.Attempt{ID: attemptID, RunID: run.ID, StepID: step.ID, Number: number, Status: "running", StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	return step, attempt
}

func (f reviewPermissionFixture) requestInput(t *testing.T) CreatePermissionRequestInput {
	t.Helper()
	arguments, err := json.Marshal(map[string]any{"argv": []string{"fixture-command", f.outside}})
	if err != nil {
		t.Fatal(err)
	}
	return CreatePermissionRequestInput{
		ThreadID: f.run.ThreadID, WorkspaceID: f.run.WorkspaceID, RunID: f.run.ID,
		StepID: f.step.ID, AttemptID: f.attempt.ID, MemberID: "owner", MemberName: "Owner",
		ToolCallID: "native-1", Origin: w.OriginNative, Adapter: "review-fixture",
		Action: "execute", ToolName: "run_command", NormalizedResourceScope: f.outside,
		Arguments: arguments, Reason: "Explicit fixture request", RequestID: "review-origin-request-1",
	}
}

func reviewRequest(t *testing.T, f reviewPermissionFixture, input CreatePermissionRequestInput) w.ToolPermissionRequest {
	t.Helper()
	request, err := f.service.RequestToolPermission(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func reviewApprove(t *testing.T, f reviewPermissionFixture, request w.ToolPermissionRequest, decision string) w.ToolPermissionRequest {
	t.Helper()
	approved, err := f.service.DecidePermissionRequest(t.Context(), request.ID, request.Revision, DecisionInput{
		Decision: decision, RequestID: "review-decision-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

// (a) CLI-owned ids restart at native-1 per invocation, including concurrent
// member steps in the same Run. Only the identical operation may deduplicate.
func TestReviewPermissionRequestIdentityIncludesStepAttemptAndArguments(t *testing.T) {
	t.Run("exact duplicate returns the original request", func(t *testing.T) {
		f := newReviewPermissionFixture(t, w.ExecutionControlled)
		input := f.requestInput(t)
		first := reviewRequest(t, f, input)
		replayed := reviewRequest(t, f, input)
		if replayed.ID != first.ID {
			t.Fatalf("identical operation created another request: %s != %s", replayed.ID, first.ID)
		}
	})
	for _, kind := range []string{"other step", "other attempt"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewPermissionFixture(t, w.ExecutionControlled)
			input := f.requestInput(t)
			first := reviewRequest(t, f, input)
			if kind == "other step" {
				step, attempt := reviewSaveStepAttempt(t, f.db, f.run, "review-step-2", "review-attempt-2", 1)
				input.StepID, input.AttemptID = step.ID, attempt.ID
			} else {
				if err := f.db.Write(t.Context(), func(tx *store.Tx) error {
					ended := f.attempt
					ended.Status, ended.FinishedAt = "canceled", w.Now()
					return tx.SaveAttempt(ended)
				}); err != nil {
					t.Fatal(err)
				}
				_, attempt := reviewSaveStepAttempt(t, f.db, f.run, f.step.ID, "review-attempt-2", 2)
				input.AttemptID = attempt.ID
			}
			input.RequestID = "review-origin-request-2"
			second := reviewRequest(t, f, input)
			if second.ID == first.ID || second.StepID != input.StepID || second.AttemptID != input.AttemptID {
				t.Fatalf("native-1 from a different invocation reused %+v instead of %+v", second, input)
			}
		})
	}
	t.Run("changed arguments under the same call identity conflict", func(t *testing.T) {
		f := newReviewPermissionFixture(t, w.ExecutionControlled)
		input := f.requestInput(t)
		reviewRequest(t, f, input)
		input.Arguments = json.RawMessage(`{"argv":["fixture-command","different-target"]}`)
		if _, err := f.service.RequestToolPermission(t.Context(), input); err == nil {
			t.Fatal("a reused call identity with changed effect arguments was accepted")
		}
	})
}

// (b) At-most-once is a property of the one authorized request, not of any
// caller-chosen suffix appended to that request.
func TestReviewPermissionAllowanceCannotBeConsumedByAnotherInvocation(t *testing.T) {
	for _, kind := range []string{"other call", "other step and attempt", "other run"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewPermissionFixture(t, w.ExecutionControlled)
			request := reviewApprove(t, f, reviewRequest(t, f, f.requestInput(t)), w.DecisionAllowOnce)
			first, created, err := f.service.ConsumeAllowance(t.Context(), request.ID, f.run, f.step.ID, f.attempt.ID, request.ToolCallID)
			if err != nil || !created {
				t.Fatalf("first exact operation was not admitted: created=%v err=%v", created, err)
			}
			repeat, created, err := f.service.ConsumeAllowance(t.Context(), request.ID, f.run, f.step.ID, f.attempt.ID, request.ToolCallID)
			if err != nil || created || repeat.ID != first.ID {
				t.Fatalf("exact duplicate must return its consumed admission: %+v created=%v err=%v", repeat, created, err)
			}
			run, stepID, attemptID, callID := f.run, f.step.ID, f.attempt.ID, request.ToolCallID
			switch kind {
			case "other call":
				callID = "native-2"
			case "other step and attempt":
				step, attempt := reviewSaveStepAttempt(t, f.db, f.run, "review-step-2", "review-attempt-2", 1)
				stepID, attemptID = step.ID, attempt.ID
			case "other run":
				run.ID, run.ThreadID, run.TaskID = "review-other-run", "review-other-thread", ""
				if err := f.db.Write(t.Context(), func(tx *store.Tx) error {
					if err := tx.SaveThread(w.Thread{ID: run.ThreadID, WorkspaceID: run.WorkspaceID, Revision: 1, AgentIDs: []string{"owner"}, UpdatedAt: w.Now()}); err != nil {
						return err
					}
					return tx.InsertRun(run, "review-other-key", "review-other-digest")
				}); err != nil {
					t.Fatal(err)
				}
				step, attempt := reviewSaveStepAttempt(t, f.db, run, "review-other-step", "review-other-attempt", 1)
				stepID, attemptID = step.ID, attempt.ID
			}
			admission, created, err := f.service.ConsumeAllowance(t.Context(), request.ID, run, stepID, attemptID, callID)
			if err == nil {
				t.Fatalf("mismatched invocation reused the approval: admission=%+v created=%v", admission, created)
			}
		})
	}
}

// (c) A thread grant is shared across eligible members, not across all tools
// that happen to map to the same broad action category.
func TestReviewPermissionConversationGrantMatchesTheApprovedTool(t *testing.T) {
	f := newReviewPermissionFixture(t, w.ExecutionProjectAutonomous)
	input := f.requestInput(t)
	input.ToolName, input.Action = "write_file", "write"
	input.Arguments = json.RawMessage(`{"path":"external.txt","content":"fixture"}`)
	reviewApprove(t, f, reviewRequest(t, f, input), w.DecisionAllowConversation)
	for _, item := range []struct {
		name    string
		allowed bool
	}{
		{name: "write_file", allowed: true},
		{name: "patch_file", allowed: false},
	} {
		t.Run(item.name, func(t *testing.T) {
			decision, err := f.service.CheckToolAccess(t.Context(), AccessRequest{Run: f.run, StepID: f.step.ID, Attempt: f.attempt.ID, CallID: "another-call", Name: item.name, Arguments: input.Arguments, Scope: f.outside})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed != item.allowed {
				t.Fatalf("grant for write_file applied incorrectly to %s: %+v", item.name, decision)
			}
		})
	}
}

// (d) Being a read/write capability is not proof that its concrete target is
// inside the workspace. Outside operations must be requestable, not auto-run.
func TestReviewPermissionProjectOutsideReadsAndWritesRequireApproval(t *testing.T) {
	for _, mode := range []string{w.ExecutionControlled, w.ExecutionProjectAutonomous} {
		for _, name := range []string{"read_file", "write_file", "run_test"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				f := newReviewPermissionFixture(t, mode)
				arguments, err := json.Marshal(map[string]any{"path": f.outside})
				if err != nil {
					t.Fatal(err)
				}
				decision, err := f.service.CheckToolAccess(t.Context(), AccessRequest{Run: f.run, StepID: f.step.ID, Attempt: f.attempt.ID, CallID: "outside-call", Name: name, Arguments: arguments, Scope: f.outside})
				if err != nil {
					t.Fatal(err)
				}
				if decision.Allowed || !decision.Requestable {
					t.Fatalf("outside %s should wait for approval in %s: %+v", name, mode, decision)
				}
			})
		}
	}
}

// (e) A frozen full_access Run cannot keep admitting effects after the live
// global policy has been tightened. No real effect is needed to test admission.
func TestReviewPermissionLiveEpochBlocksOldFullAccessRun(t *testing.T) {
	f := newReviewPermissionFixture(t, w.ExecutionFullAccess)
	var settings w.Settings
	if err := f.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		settings, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mode := w.ExecutionControlled
	change, err := f.service.UpdateSettings(t.Context(), settings.Revision, SettingsPatch{ExecutionMode: &mode})
	if err != nil {
		t.Fatal(err)
	}
	if change.Settings.PermissionEpoch <= f.run.PermissionEpoch {
		t.Fatal("fixture failed to advance the live permission epoch")
	}
	decision, err := f.service.CheckToolAccess(t.Context(), AccessRequest{Run: f.run, StepID: f.step.ID, Attempt: f.attempt.ID, CallID: "old-epoch-call", Name: "run_command", Arguments: json.RawMessage(`{"argv":["fixture-command"]}`), Scope: f.outside})
	if err == nil && decision.Allowed {
		t.Fatalf("old full_access snapshot bypassed the tightened live mode: %+v", decision)
	}
}

// (f) A lost cancellation notification is not authority to approve a finished
// invocation later. Decision admission must check persisted execution truth.
func TestReviewPermissionFinishedExecutionCannotReceiveLateApproval(t *testing.T) {
	for _, kind := range []string{"attempt ended", "run canceled"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewPermissionFixture(t, w.ExecutionControlled)
			request := reviewRequest(t, f, f.requestInput(t))
			if err := f.db.Write(t.Context(), func(tx *store.Tx) error {
				if kind == "run canceled" {
					ended := f.run
					ended.Status, ended.FinishedAt = "canceled", w.Now()
					return tx.SaveRun(ended)
				}
				ended := f.attempt
				ended.Status, ended.FinishedAt = "canceled", w.Now()
				return tx.SaveAttempt(ended)
			}); err != nil {
				t.Fatal(err)
			}
			_, err := f.service.DecidePermissionRequest(t.Context(), request.ID, request.Revision, DecisionInput{
				Decision: w.DecisionAllowConversation, RequestID: "review-late-decision-1",
			})
			if err == nil {
				t.Fatal("a late approval was accepted after its execution ended")
			}
			grants, err := f.service.ThreadConversationGrants(t.Context(), f.run.ThreadID)
			if err != nil {
				t.Fatal(err)
			}
			for _, grant := range grants {
				if grant.SourceRequestID == request.ID && grant.Status == "active" {
					t.Fatalf("late decision created a usable conversation grant: %+v", grant)
				}
			}
		})
	}
}

// TestReviewPermissionSharedGrantReleasesConcurrentMember is the A/B acceptance:
// two members of the same thread both wait on the identical tool and scope. One
// allow_conversation decision must release both without a second prompt, while a
// different scope in another thread stays pending.
func TestReviewPermissionSharedGrantReleasesConcurrentMember(t *testing.T) {
	f := newReviewPermissionFixture(t, w.ExecutionProjectAutonomous)
	// Member A's request.
	first := f.requestInput(t)
	first.ToolName, first.Action = "write_file", "write"
	first.ToolCallID = "native-1"
	first.Arguments = json.RawMessage(`{"path":"shared.txt","content":"x"}`)
	requestA := reviewRequest(t, f, first)

	// Member B: a real second enabled workspace member with its own running step
	// and attempt in the same run, asking for the identical tool and scope.
	secondMember := w.Agent{ID: "second", DisplayName: "Second", Handle: "second", Model: "default", Revision: 1}
	if err := f.db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveAgent(secondMember); err != nil {
			return err
		}
		return tx.SaveMember(w.WorkspaceMember{WorkspaceID: f.run.WorkspaceID, AgentID: secondMember.ID, Enabled: true, Revision: 1})
	}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.MemberID, second.MemberName = secondMember.ID, secondMember.DisplayName
	second.RequestID = "review-origin-request-b"
	stepB := w.Step{ID: "review-step-b", RunID: f.run.ID, AgentID: secondMember.ID, AgentName: secondMember.DisplayName, Role: "engineer", Status: "running"}
	attemptB := w.Attempt{ID: "review-attempt-b", RunID: f.run.ID, StepID: stepB.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	if err := f.db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveStep(stepB); err != nil {
			return err
		}
		return tx.SaveAttempt(attemptB)
	}); err != nil {
		t.Fatal(err)
	}
	second.StepID, second.AttemptID = stepB.ID, attemptB.ID
	requestB := reviewRequest(t, f, second)
	if requestA.ID == requestB.ID {
		t.Fatal("two members shared one request row")
	}

	// A is allowed for the conversation.
	if _, err := f.service.DecidePermissionRequest(t.Context(), requestA.ID, requestA.Revision, DecisionInput{
		Decision: w.DecisionAllowConversation, RequestID: "review-shared-decision",
	}); err != nil {
		t.Fatal(err)
	}
	// B resumes under the shared grant without a second human decision.
	released, err := f.service.WaitForDecision(t.Context(), requestB.ID, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("B did not resume under the shared grant: %v", err)
	}
	if released.ID != requestB.ID {
		t.Fatalf("wait returned another request: %+v", released)
	}
	if released.Status != w.PermissionAllowedConversation {
		t.Fatalf("B status = %s", released.Status)
	}
	// A different scope in the same thread is still untouched and needs its own
	// decision.
	other := second
	other.ToolCallID = "native-2"
	other.NormalizedResourceScope = f.outside + "-different"
	other.StepID, other.AttemptID = stepB.ID, attemptB.ID
	other.RequestID = "review-origin-request-c"
	requestC := reviewRequest(t, f, other)
	var pending []w.ToolPermissionRequest
	if err := f.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		pending, err = tx.PendingPreApprovalsOrRequests(f.run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range pending {
		if item.ID == requestC.ID && item.Status == w.PermissionPending {
			found = true
		}
	}
	if !found {
		t.Fatalf("an unrelated scope was released by the shared grant: %+v", pending)
	}
}

// TestReviewPermissionSharedGrantRejectsEndedOrRevokedExecution proves adoption of
// another member's grant is not a shortcut past live execution truth: an ended
// attempt settles the request as canceled, and a revoked grant never releases a
// waiting request.
func TestReviewPermissionSharedGrantRejectsEndedOrRevokedExecution(t *testing.T) {
	t.Run("attempt ended before release", func(t *testing.T) {
		f := newReviewPermissionFixture(t, w.ExecutionProjectAutonomous)
		first := f.requestInput(t)
		first.ToolName, first.Action = "write_file", "write"
		first.ToolCallID = "native-1"
		first.Arguments = json.RawMessage(`{"path":"shared.txt","content":"x"}`)
		requestA := reviewRequest(t, f, first)
		second := first
		second.MemberID, second.MemberName = "owner", "Owner"
		second.RequestID = "review-origin-request-b2"
		stepB, attemptB := reviewSaveStepAttempt(t, f.db, f.run, "review-step-b2", "review-attempt-b2", 1)
		second.StepID, second.AttemptID = stepB.ID, attemptB.ID
		requestB := reviewRequest(t, f, second)
		// The waiting attempt ends before the shared decision is granted.
		if err := f.db.Write(t.Context(), func(tx *store.Tx) error {
			ended := attemptB
			ended.Status, ended.FinishedAt = "canceled", w.Now()
			return tx.SaveAttempt(ended)
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.DecidePermissionRequest(t.Context(), requestA.ID, requestA.Revision, DecisionInput{
			Decision: w.DecisionAllowConversation, RequestID: "review-shared-decision-b2",
		}); err != nil {
			t.Fatal(err)
		}
		settled, err := f.service.WaitForDecision(t.Context(), requestB.ID, 20*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if settled.Status == w.PermissionAllowedConversation {
			t.Fatalf("an ended attempt adopted the grant: %+v", settled)
		}
		if settled.Status != w.PermissionCanceled {
			t.Fatalf("ended attempt settlement = %s", settled.Status)
		}
	})
	t.Run("revoked grant never releases a later waiting member", func(t *testing.T) {
		f := newReviewPermissionFixture(t, w.ExecutionProjectAutonomous)
		first := f.requestInput(t)
		first.ToolName, first.Action = "write_file", "write"
		first.ToolCallID = "native-1"
		first.Arguments = json.RawMessage(`{"path":"shared.txt","content":"x"}`)
		requestA := reviewRequest(t, f, first)
		if _, err := f.service.DecidePermissionRequest(t.Context(), requestA.ID, requestA.Revision, DecisionInput{
			Decision: w.DecisionAllowConversation, RequestID: "review-shared-decision-b3",
		}); err != nil {
			t.Fatal(err)
		}
		grants, err := f.service.ThreadConversationGrants(t.Context(), f.run.ThreadID)
		if err != nil || len(grants) != 1 {
			t.Fatalf("grants = %+v %v", grants, err)
		}
		if _, err := f.service.RevokeConversationGrant(t.Context(), grants[0].ID); err != nil {
			t.Fatal(err)
		}
		// A member that asks only after the revocation sees no reusable grant.
		second := first
		second.RequestID = "review-origin-request-b3"
		stepB, attemptB := reviewSaveStepAttempt(t, f.db, f.run, "review-step-b3", "review-attempt-b3", 1)
		second.StepID, second.AttemptID = stepB.ID, attemptB.ID
		second.ToolCallID = "native-9"
		requestB := reviewRequest(t, f, second)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		settled, err := f.service.WaitForDecision(ctx, requestB.ID, 20*time.Millisecond)
		if err == nil && settled.Status == w.PermissionAllowedConversation {
			t.Fatalf("a revoked grant released the request: %+v", settled)
		}
	})
}
