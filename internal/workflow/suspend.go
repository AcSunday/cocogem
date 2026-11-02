package workflow

import (
	"context"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// suspend persists a recoverable checkpoint without pretending the run ended.
func (e *Engine) suspend(parent context.Context, run w.Run) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	var canceled bool
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if w.Terminal(current.Status) {
			return nil
		}
		if current.Status == "canceling" {
			canceled = true
			return nil
		}
		if current.Status == "queued" {
			return nil
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.Status != "running" && step.Status != "canceled" {
				continue
			}
			step.Status = "interrupted"
			step.Error = "service suspended before step completion; will resume"
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, step.AgentID, "step_finished", step); err != nil {
				return err
			}
		}
		attempts, err := tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attempt.Status != "running" && attempt.Status != "canceled" {
				continue
			}
			attempt.Status = "interrupted"
			attempt.Error = "service suspended before the attempt settled"
			attempt.Recovery = "new_attempt_from_context"
			attempt.FinishedAt = w.Now()
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, attempt.StepID, "", "attempt_finished", attempt); err != nil {
				return err
			}
		}
		from := map[string]bool{"processing": true, "canceled": true}
		if err := transitionMessages(tx, run.ID, run.ThreadID, from, "interrupted", "message_finished"); err != nil {
			return err
		}
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		return tx.AppendEvent(run.ID, "", "", "run_suspended", map[string]any{"reason": "service_stop"})
	})
	if canceled {
		e.finish(context.Background(), run, "canceled", nil)
		return
	}
	if err != nil {
		e.report(err)
	}
}
