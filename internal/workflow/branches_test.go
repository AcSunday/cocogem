package workflow

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestParallelStepsAcquireAndReleaseSharedBranchSlots(t *testing.T) {
	db, created, run := seedTaskRun(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		account.Policy.ParallelBranches = 2
		account.Policy.WriterBranches = 1
		account.Revision++
		if err := tx.SaveBudgetAccount(account, account.Revision-1); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		return tx.InsertRun(run, "parallel-branches", "parallel-branches-digest")
	}); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: db, Model: testutil.Model{}, Limits: DefaultLimits()}
	admission, err := engine.admitRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	steps := []w.Step{{ID: "one", RunID: run.ID}, {ID: "two", RunID: run.ID}, {ID: "three", RunID: run.ID}}
	branches, err := engine.acquireStepBranches(t.Context(), admission.run, []int{0, 1, 2}, steps)
	if err != nil || len(branches) != 2 {
		t.Fatalf("parallel admission = %+v, %v", branches, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil || account.ActiveBranches != 2 {
			t.Fatalf("active branches = %+v, %v", account, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.releaseStepBranches(t.Context(), admission.run, branches); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil || account.ActiveBranches != 1 {
			t.Fatalf("released branches = %+v, %v", account, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		service := project.New(db)
		acquired, err := service.AcquireStepBranch(tx, admission.run, true)
		if err != nil || !acquired {
			t.Fatalf("writer acquisition = %v, %v", acquired, err)
		}
		second, err := service.AcquireStepBranch(tx, admission.run, true)
		if err != nil || second {
			t.Fatalf("writer limit was bypassed = %v, %v", second, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
