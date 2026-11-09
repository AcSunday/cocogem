package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestNoProgressRequiresNewEvidenceOrVerifiedChange(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		account.Policy.NoProgressFailures = 2
		account.Revision++
		return tx.SaveBudgetAccount(account, account.Revision-1)
	}); err != nil {
		t.Fatal(err)
	}
	runs := []w.Run{
		{ID: "repair-root", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, TaskRevision: 1, BudgetIssueID: "repair-root", AutomaticContinuation: true, Status: "running", CreatedAt: w.Now()},
		{ID: "repair-repeat", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, TaskRevision: 1, BudgetIssueID: "repair-root", ContinuedFromRun: "repair-root", AutomaticContinuation: true, Status: "running", CreatedAt: w.Now()},
		{ID: "repair-evidence", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, TaskRevision: 1, BudgetIssueID: "repair-root", ContinuedFromRun: "repair-repeat", AutomaticContinuation: true, Status: "running", CreatedAt: w.Now()},
		{ID: "repair-same-evidence", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, TaskRevision: 1, BudgetIssueID: "repair-root", ContinuedFromRun: "repair-evidence", AutomaticContinuation: true, Status: "running", CreatedAt: w.Now()},
	}
	inserted := map[int]bool{}
	ensureRun := func(index int) {
		t.Helper()
		if inserted[index] {
			return
		}
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			return tx.InsertRun(runs[index], "key:"+runs[index].ID, "digest:"+runs[index].ID)
		}); err != nil {
			t.Fatal(err)
		}
		inserted[index] = true
	}
	record := func(index int) w.Run {
		t.Helper()
		ensureRun(index)
		current := runs[index]
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			if err := service.RecordRunProgress(tx, &current, "failed"); err != nil {
				return err
			}
			current.Status = "failed"
			current.FinishedAt = w.Now()
			return tx.SaveRun(current)
		}); err != nil {
			t.Fatal(err)
		}
		return current
	}
	record(0)
	ensureRun(1)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveMessage(w.Message{ID: "summary-only", ThreadID: th.ID, RunID: runs[1].ID, Role: "assistant", Content: "a differently worded summary", Status: "completed", CreatedAt: w.Now()}); err != nil {
			return err
		}
		return tx.SaveArtifact(w.Artifact{ID: "summary-artifact", Version: 1, Revision: 1, ThreadID: th.ID, RunID: runs[1].ID, Type: "report", Title: "rephrased", Body: "new prose without new evidence"})
	}); err != nil {
		t.Fatal(err)
	}
	repeated := record(1)
	if repeated.ProgressObserved || repeated.ProgressDigest != "" {
		t.Fatalf("summary or heartbeat became progress: %+v", repeated)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil || view.Task.WaitingReason != "no_progress" || view.Budget.NoProgressByBranch["repair-root"] != 2 {
		t.Fatalf("no-progress pause = %+v, %v", view, err)
	}
	policy := view.Budget.Policy
	policy.NoProgressFailures = 3
	if _, err := service.AdjustBudget(t.Context(), created.Task.ID, view.Budget.Revision, policy); err != nil {
		t.Fatal(err)
	}
	ensureRun(2)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveSource(w.Source{ID: "evidence-one", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: runs[2].ID, Kind: "web", Content: "new primary evidence", ContentDigest: "same-evidence-digest"})
	}); err != nil {
		t.Fatal(err)
	}
	progress := record(2)
	if !progress.ProgressObserved || progress.ProgressDigest == "" {
		t.Fatalf("new evidence was not progress: %+v", progress)
	}
	view, err = service.Task(t.Context(), created.Task.ID)
	if err != nil || view.Task.Status != "ready" || view.Budget.NoProgressByBranch["repair-root"] != 0 {
		t.Fatalf("new evidence did not reset branch: %+v, %v", view, err)
	}
	ensureRun(3)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveSource(w.Source{ID: "evidence-two-with-new-object-id", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: runs[3].ID, Kind: "web", Content: "new primary evidence", ContentDigest: "same-evidence-digest"})
	}); err != nil {
		t.Fatal(err)
	}
	duplicate := record(3)
	if duplicate.ProgressObserved || duplicate.ProgressDigest != progress.ProgressDigest {
		t.Fatalf("same evidence with a fresh object ID reset progress: %+v", duplicate)
	}
	view, err = service.Task(t.Context(), created.Task.ID)
	if err != nil || view.Task.Status != "ready" || view.Budget.NoProgressByBranch["repair-root"] != 1 {
		t.Fatalf("same evidence was counted as new: %+v, %v", view, err)
	}
}

func TestFixRoundExpansionResumesSameQueuedRun(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		account.Policy.FixRounds = 1
		account.Revision++
		return tx.SaveBudgetAccount(account, account.Revision-1)
	}); err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: "fix-retry", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, TaskRevision: 1, BudgetIssueID: "fix-root", Input: w.RunInput{RetryOfRunID: "fix-root"}, AutomaticContinuation: true, Status: "queued", CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.InsertRun(run, "fix-retry-key", "fix-retry-digest") }); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		limited, err := service.PrepareRepairStart(tx, &run)
		if err != nil || limited {
			t.Fatalf("first repair = %v, %v", limited, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		limited, err := service.PrepareRepairStart(tx, &run)
		if err != nil || !limited {
			t.Fatalf("repair limit = %v, %v", limited, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil || view.Task.WaitingReason != "fix_limit" || view.Budget.FixRoundsByIssue["fix-root"] != 1 {
		t.Fatalf("fix limit projection = %+v, %v", view, err)
	}
	policy := view.Budget.Policy
	policy.FixRounds = 2
	if _, err := service.AdjustBudget(t.Context(), created.Task.ID, view.Budget.Revision, policy); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		limited, err := service.PrepareRepairStart(tx, &run)
		if err != nil || limited || run.ID != "fix-retry" {
			t.Fatalf("expanded repair = %+v, %v, %v", run, limited, err)
		}
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil || account.FixRoundsByIssue["fix-root"] != 2 {
			t.Fatalf("expanded fix ledger = %+v, %v", account, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
