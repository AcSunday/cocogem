package workflow

import (
	"context"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// prepareExecution performs filesystem work after the SQLite capacity admission.
// Legacy/unit engines without a Manager retain their direct-root behavior and
// are not evidence that production isolation was verified.
func (e *Engine) prepareExecution(ctx context.Context, run w.Run) (w.Run, error) {
	if e.Executions == nil || !run.WriteBranch || run.TaskID == "" {
		return run, nil
	}
	candidate, err := e.Executions.Prepare(ctx, run)
	if err != nil {
		return run, err
	}
	run.ExecutionRef = candidate.ID
	run.ExecutionRoot = candidate.Path
	if err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return w.ErrConflict
		}
		current.ExecutionRef = candidate.ID
		current.ExecutionRoot = candidate.Path
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		if finishErr := e.Executions.Finish(context.WithoutCancel(ctx), candidate.ID); finishErr != nil {
			e.report(finishErr)
		}
		return run, err
	}
	return run, nil
}
