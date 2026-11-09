package workflow

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func seedTaskRun(t *testing.T) (*store.Store, project.TaskView, w.Run) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ws := w.Workspace{ID: "budget-ws", Name: "预算", Revision: 1}
	th := w.Thread{ID: "budget-thread", WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	created, err := project.New(db).CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: ws.ID, ThreadID: th.ID, Goal: "持续推进任务", SourceMessageID: "message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "budget-run", WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID,
		TaskID: created.Task.ID, TaskRevision: 1, Status: "queued", CreatedAt: w.Now(),
		Input: w.RunInput{Message: "继续", Workflow: "free"}, AutomaticContinuation: true,
		Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}},
	}
	return db, created, run
}

func TestTaskBudgetSettlesStartFinishAndQueuedCancellation(t *testing.T) {
	db, created, run := seedTaskRun(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		return tx.InsertRun(run, "budget-key", "budget-hash")
	}); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: db, Model: testutil.Model{}, Limits: DefaultLimits()}
	admission, err := engine.admitRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if !admission.run.BudgetStarted || admission.run.TaskID != created.Task.ID {
		t.Fatalf("admission lost task budget identity: %+v", admission.run)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Status = "succeeded"
		current.FinishedAt = w.Now()
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		if account.UsedContinuations != 1 || account.ReservedRuns != 0 || account.ActiveBranches != 0 {
			t.Fatalf("settled account = %+v", account)
		}
		task, err := tx.Task(created.Task.ID)
		if err != nil {
			return err
		}
		if task.Status != "ready" || task.EvaluationStatus != "incomplete" || task.CurrentRunID != "" {
			t.Fatalf("Run success completed or retained task execution: %+v", task)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	queued := run
	queued.ID = "queued-cancel"
	queued.Status = "queued"
	queued.BudgetStarted = false
	queued.BudgetReserved = false
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.ReserveTaskRun(&queued); err != nil {
			return err
		}
		return tx.InsertRun(queued, "queued-cancel-key", "queued-cancel-hash")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Cancel(t.Context(), queued.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		if account.UsedContinuations != 1 || account.ReservedRuns != 0 {
			t.Fatalf("queued cancellation refunded started work or retained reservation: %+v", account)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskBudgetAdmissionRetriesReservation(t *testing.T) {
	db, created, run := seedTaskRun(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		run.BudgetAccountID = created.Budget.ID
		return tx.InsertRun(run, "unreserved-key", "unreserved-hash")
	}); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: db, Model: testutil.Model{}, Limits: DefaultLimits()}
	admission, err := engine.admitRun(t.Context(), run)
	if err != nil || !admission.run.BudgetReserved || !admission.run.BudgetStarted {
		t.Fatalf("automatic admission did not reserve and start: %+v, %v", admission.run, err)
	}
}

func TestTaskBudgetExhaustionKeepsQueuedRunForAdjustment(t *testing.T) {
	db, created, run := seedTaskRun(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		account.Policy.AutoContinuations = 1
		account.UsedContinuations = 1
		account.Revision++
		if err := tx.SaveBudgetAccount(account, account.Revision-1); err != nil {
			return err
		}
		run.BudgetAccountID = created.Budget.ID
		return tx.InsertRun(run, "waiting-budget-key", "waiting-budget-hash")
	}); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: db, Model: testutil.Model{}, Limits: DefaultLimits()}
	if _, err := engine.admitRun(t.Context(), run); !errors.Is(err, w.ErrBudgetExhausted) {
		t.Fatalf("exhausted admission = %v", err)
	}
	view, err := project.New(db).Task(t.Context(), created.Task.ID)
	if err != nil || view.Task.Status != "waiting" || view.Task.WaitingReason != "budget" {
		t.Fatalf("waiting task = %+v, %v", view.Task, err)
	}
	policy := view.Budget.Policy
	policy.AutoContinuations = 2
	if _, err := project.New(db).AdjustBudget(t.Context(), created.Task.ID, view.Budget.Revision, policy); err != nil {
		t.Fatal(err)
	}
	admission, err := engine.admitRun(t.Context(), run)
	if err != nil || admission.run.ID != run.ID || !admission.run.BudgetStarted {
		t.Fatalf("adjusted queued run was not admitted: %+v, %v", admission.run, err)
	}
}
