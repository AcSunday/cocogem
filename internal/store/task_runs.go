package store

import (
	"errors"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// ReserveTaskRun binds a queued Run to its current task and reserves one shared
// automatic continuation. User-initiated Runs do not consume that allowance.
func (t *Tx) ReserveTaskRun(run *w.Run) error {
	if run.TaskID == "" {
		return nil
	}
	task, err := t.Task(run.TaskID)
	if err != nil {
		return err
	}
	if task.Status == "paused" || task.Status == "canceled" || task.Status == "completed" {
		return w.ErrConflict
	}
	if run.AutomaticContinuation && task.Status == "waiting" && (task.WaitingReason == "fix_limit" || task.WaitingReason == "no_progress") {
		return w.ErrRepairLimit
	}
	if run.TaskRevision != 0 && run.TaskRevision != task.CurrentRevision {
		return w.ErrConflict
	}
	account, err := t.BudgetAccount(task.BudgetAccountID)
	if err != nil {
		return err
	}
	run.TaskRevision = task.CurrentRevision
	run.BudgetAccountID = account.ID
	run.WriteBranch = run.Input.EngineeringAction == w.EngineeringDevelop
	if !run.AutomaticContinuation {
		return nil
	}
	if account.RemainingContinuations() <= 0 {
		return w.ErrBudgetExhausted
	}
	account.ReservedRuns++
	account.Revision++
	account.UpdatedAt = w.Now()
	if err := t.SaveBudgetAccount(account, account.Revision-1); err != nil {
		return err
	}
	run.BudgetReserved = true
	return nil
}

// TaskRunStarted binds the only active Run to its exact task revision and turns
// its durable reservation into usage. It never marks the objective complete.
func (t *Tx) TaskRunStarted(run *w.Run) error {
	if run.TaskID == "" {
		return nil
	}
	task, err := t.Task(run.TaskID)
	if err != nil {
		return err
	}
	if task.CurrentRevision != run.TaskRevision || task.Status == "paused" || task.Status == "canceled" {
		return w.ErrConflict
	}
	if task.CurrentRunID != "" && task.CurrentRunID != run.ID {
		return w.ErrConflict
	}
	if run.BudgetStarted || run.AutomaticContinuation && !run.BudgetReserved {
		return w.ErrConflict
	}
	account, err := t.BudgetAccount(task.BudgetAccountID)
	if err != nil {
		return err
	}
	if run.BudgetAccountID != account.ID {
		return w.ErrConflict
	}
	if account.ActiveBranches >= account.Policy.ParallelBranches || run.WriteBranch && account.ActiveWriters >= account.Policy.WriterBranches {
		return w.ErrExecutionBusy
	}
	if run.AutomaticContinuation {
		if account.ReservedRuns < 1 {
			return w.ErrConflict
		}
		account.ReservedRuns--
		account.UsedContinuations++
	}
	account.ActiveBranches++
	if run.WriteBranch {
		account.ActiveWriters++
	}
	account.Revision++
	account.UpdatedAt = w.Now()
	if err := t.SaveBudgetAccount(account, account.Revision-1); err != nil {
		return err
	}
	run.BudgetStarted = true
	task.Status = "active"
	task.CurrentRunID = run.ID
	task.WaitingReason = ""
	task.UpdatedAt = w.Now()
	return t.SaveTask(task, task.CurrentRevision)
}

// TaskRunFinished releases only the matching execution projection. A queued
// cancellation releases an unstarted reservation; started usage is never refunded.
func (t *Tx) TaskRunFinished(run w.Run) error {
	if run.TaskID == "" {
		return nil
	}
	task, err := t.Task(run.TaskID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.BudgetReserved || run.BudgetStarted {
		account, err := t.BudgetAccount(task.BudgetAccountID)
		if err != nil {
			return err
		}
		if run.BudgetStarted {
			account.ActiveBranches = max(0, account.ActiveBranches-1)
			if run.WriteBranch {
				account.ActiveWriters = max(0, account.ActiveWriters-1)
			}
		} else if run.AutomaticContinuation && run.BudgetReserved {
			account.ReservedRuns = max(0, account.ReservedRuns-1)
		}
		account.Revision++
		account.UpdatedAt = w.Now()
		if err := t.SaveBudgetAccount(account, account.Revision-1); err != nil {
			return err
		}
	}
	if task.CurrentRunID != run.ID {
		return nil
	}
	task.CurrentRunID = ""
	if task.Status == "active" {
		task.Status = "ready"
	}
	task.UpdatedAt = w.Now()
	return t.SaveTask(task, task.CurrentRevision)
}
