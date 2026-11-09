package project

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func runningTaskFixture(t *testing.T, db *store.Store) (*Service, w.Thread, TaskView, w.Run) {
	t.Helper()
	service := New(db)
	ws := w.Workspace{ID: "wake-ws", Name: "wake", Revision: 1, CreatedAt: w.Now(), ExecutionMode: w.ExecutionControlled}
	th := w.Thread{ID: "wake-thread", WorkspaceID: ws.ID, CoordinatorID: "owner", AgentIDs: []string{"owner"}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: ws.ID, ThreadID: th.ID, OwnerMemberID: "owner", Goal: "wait and resume",
		AllowedActions: []string{"read", "collaborate"}, SourceMessageID: "wake-create", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	member := w.Agent{ID: "owner", DisplayName: "owner", Executor: "claude", Model: "default"}
	run := w.Run{
		ID: "wake-original-run", WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID,
		TaskID: created.Task.ID, TaskRevision: 1, Status: "queued", CreatedAt: w.Now(),
		Agents: []w.Agent{member}, CoordinatorID: member.ID, AutomaticContinuation: true,
		Input:        w.RunInput{Message: "wait and resume", Workflow: "free"},
		ExecutionRef: "old-execution", ExecutionRoot: "/old/candidate",
	}
	attempt := w.Attempt{ID: "wake-attempt", RunID: run.ID, StepID: "wake-step", Number: 1, Status: "running", StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &run, "wake-create"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "wake-original", "wake-original-digest"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: attempt.StepID, RunID: run.ID, AgentID: member.ID, Status: "running"}); err != nil {
			return err
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		if err := tx.SaveMessage(w.Message{ID: "wake-message", ThreadID: th.ID, RunID: run.ID, StepID: attempt.StepID, Status: "processing", Content: "partial"}); err != nil {
			return err
		}
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	return service, th, created, run
}

func TestWakeupUsesExactYieldedRunAfterRestartAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service, th, created, original := runningTaskFixture(t, db)
	wakeup, immediate, err := service.YieldRun(t.Context(), original.ID, "event", "cursor-1", "test", "event-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if immediate.ID != "" || wakeup.YieldedRunID != original.ID {
		t.Fatalf("yield registration = %+v, immediate=%+v", wakeup, immediate)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		task, err := tx.Task(created.Task.ID)
		if err != nil {
			return err
		}
		if task.Status != "waiting" || task.CurrentRunID != "" {
			t.Fatalf("waiting task = %+v", task)
		}
		steps, err := tx.Steps(original.ID)
		if err != nil || len(steps) != 1 || steps[0].Status != "yielded" {
			t.Fatalf("yielded steps = %+v, %v", steps, err)
		}
		attempt, err := tx.Attempt("wake-attempt")
		if err != nil || attempt.Status != "yielded" {
			t.Fatalf("yielded attempt = %+v, %v", attempt, err)
		}
		message, err := tx.Message("wake-message")
		if err != nil || message.Status != "yielded" {
			t.Fatalf("yielded message = %+v, %v", message, err)
		}
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil || account.ActiveBranches != 0 || account.UsedContinuations != 1 || account.ReservedRuns != 0 {
			t.Fatalf("yielded budget = %+v, %v", account, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service = New(db)
	// A later terminal run in the same thread must never become the continuation
	// source merely because it happens to be last in thread order.
	other := original
	other.ID, other.TaskID, other.Status = "wake-unrelated-later", "another-task", "succeeded"
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertRun(other, "wake-unrelated-later", "wake-unrelated-digest")
	}); err != nil {
		t.Fatal(err)
	}
	continuation, err := service.WakeTask(t.Context(), "test", "event-1", "observed-event-1")
	if err != nil {
		t.Fatal(err)
	}
	if continuation.ContinuedFromRun != original.ID || continuation.InputDigest == "" || continuation.ExecutionRef != "" || continuation.ExecutionRoot != "" {
		t.Fatalf("continuation binding = %+v", continuation)
	}
	replay, err := service.WakeTask(t.Context(), "test", "event-1", "observed-event-1")
	if err != nil || replay.ID != continuation.ID {
		t.Fatalf("wake replay = %+v, %v", replay, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		runs, err := tx.Runs(th.ID)
		if err != nil || len(runs) != 3 {
			t.Fatalf("wake duplicated runs: %+v, %v", runs, err)
		}
		stored, err := tx.Wakeup(wakeup.ID)
		if err != nil || stored.Status != "consumed" || stored.ContinuationRunID != continuation.ID {
			t.Fatalf("consumed wakeup = %+v, %v", stored, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWakeupRejectsStaleGeneration(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	member := w.Agent{ID: "owner", DisplayName: "owner"}
	run := w.Run{ID: "stale-wake-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, TaskRevision: 1, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{member}, CoordinatorID: member.ID, AutomaticContinuation: true}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &run, "stale-wake"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "stale-wake", "stale-wake-digest"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: "stale-wake-step", RunID: run.ID, AgentID: member.ID, Status: "running"}); err != nil {
			return err
		}
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	wakeup, _, err := service.YieldRun(t.Context(), run.ID, "event", "", "stale", "event-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		task, err := tx.Task(created.Task.ID)
		if err != nil {
			return err
		}
		task.WaitGeneration++
		return tx.SaveTask(task, task.CurrentRevision)
	}); err != nil {
		t.Fatal(err)
	}
	continuation, err := service.WakeTask(t.Context(), "stale", "event-1", "late-event")
	if err != nil || continuation.ID != "" {
		t.Fatalf("stale generation created a run: %+v, %v", continuation, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		stored, err := tx.Wakeup(wakeup.ID)
		if err == nil && stored.Status != "superseded" {
			t.Fatalf("stale wakeup = %+v", stored)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
