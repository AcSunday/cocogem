package project

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func setup(t *testing.T) (*store.Store, *Service, w.Thread) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ws := w.Workspace{ID: "ws", Name: "项目", Revision: 1, CreatedAt: w.Now(), ExecutionMode: w.ExecutionWorkspaceAutonomous}
	th := w.Thread{ID: "th", WorkspaceID: ws.ID, Title: "任务", CoordinatorID: "owner", AgentIDs: []string{"owner"}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	return db, New(db), th
}

func create(t *testing.T, service *Service, th w.Thread) TaskView {
	t.Helper()
	view, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: th.ID, OwnerMemberID: "owner",
		Goal: "实现并验证幂等创建", AllowedActions: []string{"read", "write", "run_test"},
		SourceMessageID: "message-1", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestTaskRevisionKeepsHistoryAndRejectsStaleCompletion(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	if created.Budget.Policy != w.DefaultBudgetPolicy() {
		t.Fatalf("budget defaults = %+v", created.Budget.Policy)
	}
	revised, err := service.ReviseTask(t.Context(), created.Task.ID, 1, ReviseTaskInput{
		Goal: "实现幂等创建且不得新增 Redis", Scope: []string{"orders"},
		Acceptance: created.Revision.Acceptance, AllowedActions: created.Revision.AllowedActions,
		SourceMessageID: "message-20", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Task.CurrentRevision != 2 || revised.Task.EvaluationGeneration != 2 {
		t.Fatalf("revision projection = %+v", revised.Task)
	}
	old, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Revision.Digest == created.Revision.Digest {
		t.Fatal("goal digest did not change")
	}
	_, err = service.RecordCompletion(t.Context(), created.Task.ID, RecordCompletionInput{
		EvaluationGeneration: 1, InputSnapshotDigest: created.Revision.Digest,
		Results:       []w.CriterionResult{{CriterionID: "outcome", Result: "passed"}},
		JudgeMemberID: "reviewer", EvidenceProducer: "host_observed",
	})
	if !IsCode(err, "evidence_stale") {
		t.Fatalf("stale completion error = %v", err)
	}
}

func TestTaskBoardSpansWorkspaces(t *testing.T) {
	db, service, th := setup(t)
	create(t, service, th)
	secondWS := w.Workspace{ID: "ws-two", Name: "第二个项目", Revision: 1, CreatedAt: w.Now(), ExecutionMode: w.ExecutionWorkspaceAutonomous}
	secondThread := w.Thread{ID: "th-two", WorkspaceID: secondWS.ID, Title: "另一任务", CoordinatorID: "owner", AgentIDs: []string{"owner"}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(secondWS); err != nil {
			return err
		}
		return tx.SaveThread(secondThread)
	}); err != nil {
		t.Fatal(err)
	}
	create(t, service, secondThread)
	board, err := service.TaskBoard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Tasks) != 2 || board.Workspaces["ws"] != "项目" || board.Workspaces["ws-two"] != "第二个项目" {
		t.Fatalf("global task board = %+v", board)
	}
}

func TestCompletionIsSeparateFromRunDelivery(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	if created.Task.Status == "completed" {
		t.Fatal("task was completed by creation")
	}
	record, err := service.RecordHostCompletion(t.Context(), created.Task.ID, RecordCompletionInput{
		EvaluationGeneration: 1, InputSnapshotDigest: created.Revision.Digest,
		SubjectDigest: "tree-v1", CandidateRefs: []string{"candidate@1"},
		Results:       []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{"test@1"}}},
		JudgeMemberID: "reviewer", EvidenceProducer: "host_observed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Validity != "current" {
		t.Fatalf("record validity = %q", record.Validity)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.Status != "completed" || view.Task.EvaluationStatus != "current" {
		t.Fatalf("completion projection = %+v", view.Task)
	}
}

func TestBudgetSharedAcrossRevisionAndModeChanges(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	account, err := service.ReserveRun(t.Context(), created.Task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if account.ReservedRuns != 1 || account.RemainingContinuations() != 19 {
		t.Fatalf("reservation = %+v", account)
	}
	if _, err := setGlobalMode(t, service, w.ExecutionFullAccess); err != nil {
		t.Fatal(err)
	}
	lower := account.Policy
	lower.AutoContinuations = 1
	adjusted, err := service.AdjustBudget(t.Context(), created.Task.ID, account.Revision, lower)
	if err != nil {
		t.Fatal(err)
	}
	if adjusted.ReservedRuns != 1 || adjusted.RemainingContinuations() != 0 {
		t.Fatalf("budget adjustment erased reservation: %+v", adjusted)
	}
	_, err = service.ReserveRun(t.Context(), created.Task.ID, true)
	if !IsCode(err, "budget_exhausted") {
		t.Fatalf("exhaustion error = %v", err)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.Status != "waiting" || view.Task.WaitingReason != "budget" {
		t.Fatalf("waiting projection = %+v", view.Task)
	}
}

func TestTaskActionsOnlyControlOwnedTree(t *testing.T) {
	db, service, th := setup(t)
	root := create(t, service, th)
	otherThread := w.Thread{ID: "other", WorkspaceID: th.WorkspaceID, Title: "独立", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(otherThread) }); err != nil {
		t.Fatal(err)
	}
	independent := create(t, service, otherThread)
	if _, err := service.TaskAction(t.Context(), root.Task.ID, "cancel", "cancel-1"); err != nil {
		t.Fatal(err)
	}
	other, err := service.Task(t.Context(), independent.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.Task.Status != "ready" {
		t.Fatalf("independent task was canceled: %+v", other.Task)
	}
	_, err = service.ReserveRun(t.Context(), root.Task.ID, true)
	if !IsCode(err, "authority_revoked") {
		t.Fatalf("canceled root admitted work: %v", err)
	}
}

func TestTaskActionReceiptAndChildControlBoundary(t *testing.T) {
	db, service, th := setup(t)
	root := create(t, service, th)
	childThread := w.Thread{ID: "child-thread", WorkspaceID: th.WorkspaceID, Internal: true, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(childThread) }); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ParentID: root.Task.ID, ThreadID: childThread.ID,
		OwnerMemberID: "coder", Goal: "实现子任务", SourceMessageID: "delegation-1", AcceptedBy: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.TaskAction(t.Context(), child.Task.ID, "pause", "pause-child")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.TaskAction(t.Context(), child.Task.ID, "pause", "pause-child")
	if err != nil || replayed.RootControlEpoch != paused.RootControlEpoch {
		t.Fatalf("action replay advanced epoch: first=%+v replay=%+v err=%v", paused, replayed, err)
	}
	if _, err := service.TaskAction(t.Context(), child.Task.ID, "cancel", "pause-child"); !IsCode(err, "conflict") {
		t.Fatalf("reused action key error = %v", err)
	}
	parent, err := service.Task(t.Context(), root.Task.ID)
	if err != nil || parent.Task.Status != "ready" {
		t.Fatalf("child pause changed its parent: %+v err=%v", parent.Task, err)
	}
	if canceled, err := service.TaskAction(t.Context(), root.Task.ID, "cancel", "cancel-root"); err != nil || canceled.Status != "canceled" {
		t.Fatalf("root cancellation = %+v, %v", canceled, err)
	}
	descendant, err := service.Task(t.Context(), child.Task.ID)
	if err != nil || descendant.Task.Status != "canceled" {
		t.Fatalf("owned descendant was not canceled: %+v, %v", descendant.Task, err)
	}
}

func TestTaskViewExposesLatestRunFailureForTheTaskBoard(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	run := w.Run{
		ID: "run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID,
		Status: "failed", FailureKind: "network", Error: "the CLI could not reach its endpoint",
		CreatedAt: w.Now(), StartedAt: w.Now(), FinishedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertRun(run, "task-run", "hash")
	}); err != nil {
		t.Fatal(err)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.LastRunID != run.ID || view.LastRunStatus != "failed" ||
		view.LastRunFailureKind != "network" || view.LastRunError != run.Error ||
		view.LastRunStartedAt != run.StartedAt || view.LastRunFinishedAt != run.FinishedAt {
		t.Fatalf("latest run projection = %+v", view)
	}
}

// TestCompletedTaskReactivatedByANewAdvancingMessage is the A23 regression: a
// follow-up on an accepted task advances a new revision instead of returning a
// conflict, and the previous acceptance record stays historical.
func TestCompletedTaskReactivatedByANewAdvancingMessage(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	// Accept the first objective.
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		task := created.Task
		revision := created.Revision
		record := w.CompletionRecord{
			ID: "seed", TaskID: task.ID, TaskRevision: task.CurrentRevision,
			EvaluationGeneration: task.EvaluationGeneration,
			InputSnapshotDigest:  revision.Digest, SubjectDigest: "seed-subject",
			CandidateRefs: []string{"message:seed"}, Results: []w.CriterionResult{},
			JudgeMemberID: "owner", EvidenceProducer: "user", Validity: "current", CreatedAt: w.Now(),
		}
		if err := tx.InsertCompletion(record); err != nil {
			return err
		}
		task.Status = "completed"
		task.EvaluationStatus = "current"
		task.UpdatedAt = w.Now()
		return tx.SaveTask(task, task.CurrentRevision)
	}); err != nil {
		t.Fatal(err)
	}
	// The conversation now belongs to the accepted task.
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		th, err = tx.Thread(th.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var view TaskView
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		var err error
		view, err = service.EnsureThreadTask(tx, th, w.RunInput{Message: "继续推进下一个目标"}, "followup-message", "followup-run")
		return err
	}); err != nil {
		t.Fatalf("follow-up on a completed task was rejected: %v", err)
	}
	if view.Task.Status != "ready" || view.Task.CurrentRevision != created.Task.CurrentRevision+1 {
		t.Fatalf("reactivation = %+v", view.Task)
	}
	if view.Task.EvaluationGeneration <= created.Task.EvaluationGeneration {
		t.Fatal("reactivation did not advance the evaluation generation")
	}
	// The earlier acceptance record stays readable as history.
	if len(view.Completions) == 0 || view.Completions[0].TaskRevision != created.Task.CurrentRevision {
		t.Fatalf("historical acceptance was lost: %+v", view.Completions)
	}
}

// TestTaskBoardUsesABoundedNumberOfQueries is the A22 regression: the global
// board issues a constant number of queries regardless of task count.
func TestTaskBoardUsesABoundedNumberOfQueries(t *testing.T) {
	db, service, th := setup(t)
	// Ten independent root tasks so a per-task query would scale linearly.
	for range 10 {
		parent := w.NewID()
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			task := w.Task{ID: parent, WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RootTaskID: parent, BudgetAccountID: w.NewID(), CurrentRevision: 1, Status: "ready", CreatedAt: w.Now(), UpdatedAt: w.Now()}
			if err := tx.InsertTask(task); err != nil {
				return err
			}
			if err := tx.InsertBudgetAccount(w.BudgetAccount{ID: task.BudgetAccountID, RootTaskID: task.ID, Revision: 1, Policy: w.DefaultBudgetPolicy(), UpdatedAt: w.Now()}); err != nil {
				return err
			}
			return tx.InsertTaskRevision(w.TaskRevision{TaskID: task.ID, Revision: 1, Goal: "goal", Digest: w.NewID(), CreatedAt: w.Now()})
		}); err != nil {
			t.Fatal(err)
		}
	}
	board, err := service.TaskBoard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Tasks) < 10 {
		t.Fatalf("board returned %d tasks", len(board.Tasks))
	}
}

// TestTaskBoardProjectsAttentionAndCandidatesWithoutPerTaskQueries covers the
// board's new batch projection: pending authorizations, open questions, active
// and attention-bearing children, plus one unapplied candidate summary.
func TestTaskBoardProjectsAttentionAndCandidatesWithoutPerTaskQueries(t *testing.T) {
	db, service, th := setup(t)
	root := create(t, service, th)
	childThread := w.Thread{ID: "child-thread", WorkspaceID: th.WorkspaceID, Title: "child", CoordinatorID: "owner", AgentIDs: []string{"owner"}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(childThread) }); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ParentID: root.Task.ID, ThreadID: childThread.ID,
		OwnerMemberID: "owner", Goal: "child goal", AllowedActions: []string{"read"},
		SourceMessageID: "child-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AskQuestion(t.Context(), child.Task.ID, "task", "选择交付格式", []string{"摘要"}, "摘要"); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		run := w.Run{ID: "pending-run", WorkspaceID: th.WorkspaceID, ThreadID: childThread.ID, TaskID: child.Task.ID, TaskRevision: child.Task.CurrentRevision, Status: "running", CreatedAt: w.Now()}
		if err := tx.InsertRun(run, "pending-key", "pending-hash"); err != nil {
			return err
		}
		return tx.SavePermissionRequest(w.ToolPermissionRequest{
			ID: "pending-request", ThreadID: childThread.ID, WorkspaceID: th.WorkspaceID, RunID: run.ID,
			StepID: "step", AttemptID: "attempt", MemberID: "owner", ToolCallID: "call",
			Origin: w.OriginHost, Action: "write", ToolName: "write_file",
			NormalizedResourceScope: "/tmp/outside", ArgumentsDigest: "digest",
			PermissionEpoch: 1, Status: w.PermissionPending, Revision: 1, CreatedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveExecution(w.WorkspaceExecution{
			ID: "candidate", WorkspaceID: th.WorkspaceID, ThreadID: childThread.ID, TaskID: child.Task.ID,
			Kind: "git_worktree", Path: "/tmp/candidate", CanonicalRoot: "/tmp/project",
			Status: "completed", LeaseOwner: "owner", LeaseEpoch: 1, CreatedAt: w.Now(), UpdatedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	board, err := service.TaskBoard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range board.Tasks {
		switch view.Task.ID {
		case root.Task.ID:
			if view.ActiveChildren != 1 || view.AttentionChildren != 1 {
				t.Fatalf("root child projection = %+v", view)
			}
		case child.Task.ID:
			if view.OpenQuestions != 1 || view.PendingPermissions != 1 {
				t.Fatalf("child attention projection = %+v", view)
			}
			if view.CandidateVerification == nil || view.CandidateVerification.ExecutionID != "candidate" {
				t.Fatalf("candidate projection = %+v", view.CandidateVerification)
			}
		}
	}
}
