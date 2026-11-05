package project

import (
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// AcquireStepBranch accounts for an additional concurrent execution group in an
// already running Run. The Run's first group was charged by TaskRunStarted.
func (s *Service) AcquireStepBranch(tx *store.Tx, run w.Run, writer bool) (bool, error) {
	if run.TaskID == "" || !run.BudgetStarted {
		return false, nil
	}
	task, err := tx.Task(run.TaskID)
	if err != nil {
		return false, err
	}
	if task.CurrentRunID != run.ID || task.Status != "active" {
		return false, w.ErrConflict
	}
	account, err := tx.BudgetAccount(task.BudgetAccountID)
	if err != nil {
		return false, err
	}
	if account.ActiveBranches >= account.Policy.ParallelBranches || writer && account.ActiveWriters >= account.Policy.WriterBranches {
		return false, nil
	}
	account.ActiveBranches++
	if writer {
		account.ActiveWriters++
	}
	account.Revision++
	account.UpdatedAt = s.now()
	return true, tx.SaveBudgetAccount(account, account.Revision-1)
}

// ReleaseStepBranch settles exactly one previously acquired additional group.
// It is independent of the Run's base slot, which TaskRunFinished releases.
func (s *Service) ReleaseStepBranch(tx *store.Tx, run w.Run, writer bool) error {
	if run.TaskID == "" || !run.BudgetStarted {
		return nil
	}
	account, err := tx.BudgetAccount(run.BudgetAccountID)
	if err != nil {
		return err
	}
	account.ActiveBranches = max(0, account.ActiveBranches-1)
	if writer {
		account.ActiveWriters = max(0, account.ActiveWriters-1)
	}
	account.Revision++
	account.UpdatedAt = s.now()
	return tx.SaveBudgetAccount(account, account.Revision-1)
}
