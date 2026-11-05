package project

import (
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// PrepareRepairStart binds an automatic run to a stable issue chain and charges
// only explicit repair retries against the fix-round policy. The caller commits
// a true limit result so the original queued run remains available after a
// versioned budget expansion.
func (s *Service) PrepareRepairStart(tx *store.Tx, run *w.Run) (bool, error) {
	if run.TaskID == "" || !run.AutomaticContinuation {
		return false, nil
	}
	if run.BudgetIssueID == "" {
		run.BudgetIssueID = run.ID
	}
	task, err := tx.Task(run.TaskID)
	if err != nil {
		return false, err
	}
	limited := task.Status == "waiting" && (task.WaitingReason == "fix_limit" || task.WaitingReason == "no_progress")
	account, err := tx.BudgetAccount(task.BudgetAccountID)
	if err != nil {
		return false, err
	}
	if run.Input.RetryOfRunID != "" && !limited {
		if account.FixRoundsByIssue == nil {
			account.FixRoundsByIssue = map[string]int{}
		}
		if account.FixRoundsByIssue[run.BudgetIssueID] >= account.Policy.FixRounds {
			limited = true
			task.WaitingReason = "fix_limit"
		} else {
			account.FixRoundsByIssue[run.BudgetIssueID]++
			account.Revision++
			account.UpdatedAt = s.now()
			if err := tx.SaveBudgetAccount(account, account.Revision-1); err != nil {
				return false, err
			}
		}
	}
	if !limited {
		return false, nil
	}
	if task.Status != "paused" && task.Status != "canceled" {
		task.Status = "waiting"
		if task.WaitingReason == "" {
			task.WaitingReason = "no_progress"
		}
		task.UpdatedAt = s.now()
		if err := tx.SaveTask(task, task.CurrentRevision); err != nil {
			return false, err
		}
	}
	return true, tx.SaveRun(*run)
}

// RecordRunProgress records only substantive, host-observable progress. Message
// wording, heartbeat events and summary-only artifact edits never enter this
// digest. A retry carries forward the last proven digest so repeating the same
// evidence remains a no-progress failure rather than looking new by object ID.
func (s *Service) RecordRunProgress(tx *store.Tx, run *w.Run, status string) error {
	if run.TaskID == "" {
		return nil
	}
	if run.BudgetIssueID == "" {
		run.BudgetIssueID = run.ID
	}
	previous := w.Run{}
	previousID := run.ContinuedFromRun
	if previousID == "" {
		previousID = run.Input.RetryOfRunID
	}
	if previousID != "" {
		var err error
		previous, err = tx.Run(previousID)
		if err != nil {
			return err
		}
	}
	tokens, err := substantiveProgress(tx, *run)
	if err != nil {
		return err
	}
	run.ProgressDigest = previous.ProgressDigest
	if len(tokens) > 0 {
		slices.Sort(tokens)
		digest := Digest(tokens)
		if digest != previous.ProgressDigest {
			run.ProgressDigest = digest
			run.ProgressObserved = true
		}
	}
	task, err := tx.Task(run.TaskID)
	if err != nil {
		return err
	}
	account, err := tx.BudgetAccount(task.BudgetAccountID)
	if err != nil {
		return err
	}
	if account.NoProgressByBranch == nil {
		account.NoProgressByBranch = map[string]int{}
	}
	changed := false
	switch status {
	case "succeeded":
		if account.NoProgressByBranch[run.BudgetIssueID] != 0 {
			account.NoProgressByBranch[run.BudgetIssueID] = 0
			changed = true
		}
	case "failed":
		if run.AutomaticContinuation {
			if run.ProgressObserved {
				account.NoProgressByBranch[run.BudgetIssueID] = 0
			} else {
				account.NoProgressByBranch[run.BudgetIssueID]++
			}
			changed = true
		}
	}
	if changed {
		account.Revision++
		account.UpdatedAt = s.now()
		if err := tx.SaveBudgetAccount(account, account.Revision-1); err != nil {
			return err
		}
	}
	if status == "failed" && run.AutomaticContinuation && !run.ProgressObserved &&
		account.NoProgressByBranch[run.BudgetIssueID] >= account.Policy.NoProgressFailures &&
		task.Status != "paused" && task.Status != "canceled" {
		task.Status = "waiting"
		task.WaitingReason = "no_progress"
		task.UpdatedAt = s.now()
		if err := tx.SaveTask(task, task.CurrentRevision); err != nil {
			return err
		}
	}
	return nil
}

func substantiveProgress(tx *store.Tx, run w.Run) ([]string, error) {
	tokens := []string{}
	sources, err := tx.Sources(run.ThreadID)
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		if source.RunID != run.ID {
			continue
		}
		digest := source.ContentDigest
		if digest == "" {
			digest = source.SHA256
		}
		if digest == "" {
			digest = Digest(source.Content)
		}
		tokens = append(tokens, "evidence:"+digest)
	}
	if run.ExecutionRef != "" {
		effect, err := tx.EffectByKey(run.TaskID, "execution:"+run.ExecutionRef)
		if err == nil && effect.AfterDigest != "" && effect.AfterDigest != effect.BeforeDigest &&
			(effect.Status == "applied" || effect.Status == "reconciled_applied") {
			tokens = append(tokens, "change:"+effect.AfterDigest)
		} else if err != nil && err != store.ErrNotFound {
			return nil, err
		}
	}
	completions, err := tx.Completions(run.TaskID)
	if err != nil {
		return nil, err
	}
	for _, completion := range completions {
		tokens = append(tokens, "completion:"+Digest(struct {
			Subject string              `json:"subject"`
			Results []w.CriterionResult `json:"results"`
		}{completion.SubjectDigest, completion.Results}))
	}
	return tokens, nil
}
