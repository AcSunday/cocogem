package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TransferTask owns the non-transactional stop phase. SQLite never remains open
// while cancellation, process inspection or descendant verification is attempted.
func (e *Engine) TransferTask(ctx context.Context, taskID, newOwnerID, requestID string, expectedOwnerRevision int) (w.Task, error) {
	service := project.New(e.Store)
	task, err := service.BeginTaskTransfer(ctx, taskID, newOwnerID, requestID, expectedOwnerRevision)
	if err != nil || task.TransferRequestID != requestID {
		// A completed replay has no pending marker and must not cancel the new
		// owner's later work.
		return task, err
	}
	stopCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	cancelErr := e.cancelTaskRuns(stopCtx, taskID)
	uncertain, verifyErr := e.taskStopUncertain(stopCtx, taskID, task.OwnerMemberID)
	completed, completeErr := service.CompleteTaskTransfer(stopCtx, taskID, requestID, uncertain || cancelErr != nil || verifyErr != nil)
	if completeErr != nil {
		return completed, errors.Join(cancelErr, verifyErr, completeErr)
	}
	return completed, errors.Join(cancelErr, verifyErr)
}

func (e *Engine) cancelTaskRuns(ctx context.Context, taskID string) error {
	ids := []string{}
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		runs, err := tx.Active()
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.TaskID == taskID {
				ids = append(ids, run.ID)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	var cancelErr error
	for _, id := range ids {
		if _, err := e.Cancel(ctx, id); err != nil {
			cancelErr = errors.Join(cancelErr, err)
		}
	}
	for {
		uncertain, err := e.taskRunsActive(ctx, taskID)
		if err != nil || !uncertain {
			return errors.Join(cancelErr, err)
		}
		select {
		case <-ctx.Done():
			return errors.Join(cancelErr, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (e *Engine) taskRunsActive(ctx context.Context, taskID string) (bool, error) {
	active := false
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		runs, err := tx.Active()
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.TaskID == taskID {
				active = true
			}
		}
		return nil
	})
	return active, err
}

func (e *Engine) taskStopUncertain(ctx context.Context, taskID, oldOwnerID string) (bool, error) {
	uncertain := false
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		runs, err := tx.WorkspaceRuns(task.WorkspaceID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.TaskID != taskID {
				continue
			}
			if !w.Terminal(run.Status) {
				uncertain = true
			}
			steps, err := tx.Steps(run.ID)
			if err != nil {
				return err
			}
			for _, step := range steps {
				if step.AgentID != oldOwnerID {
					continue
				}
				if step.Status == "running" {
					uncertain = true
				}
				attempts, err := tx.StepAttempts(step.ID)
				if err != nil {
					return err
				}
				for _, attempt := range attempts {
					if attempt.Process != nil {
						// A process-group handle cannot prove that every escaped
						// descendant exited. Keep ownership frozen in that case.
						uncertain = true
					}
				}
			}
		}
		return nil
	})
	return uncertain, err
}
