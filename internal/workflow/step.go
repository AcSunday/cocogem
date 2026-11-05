package workflow

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (e *Engine) executeStep(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source) w.Step {
	return e.executeStepWith(ctx, run, step, history, all, sourceSnapshot, nil)
}

func (e *Engine) executeStepWith(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source, checkpoint *checkpointState) w.Step {
	prep := e.prepareStepAttempt(ctx, run, step, history, all, sourceSnapshot)
	if prep.stopped {
		return prep.step
	}
	step = prep.step
	a := prep.agent
	allowed := prep.allowed
	required := prep.required
	message := prep.message
	messageSeq := prep.messageSeq
	attempt := prep.attempt
	handoff := prep.handoff
	scope := prep.scope
	stepDeadline := prep.stepDeadline
	previousDraft := prep.previousDraft
	retiredSession := prep.retiredSession
	adapter := prep.adapter
	connection := prep.connection

	var err error
	var stepCtx context.Context
	var stopStep context.CancelFunc
	if !stepDeadline.IsZero() {
		stepCtx, stopStep = context.WithDeadline(ctx, stepDeadline)
	} else {
		stepCtx, stopStep = context.WithCancel(ctx)
	}
	defer stopStep()
	rt := &stepRuntime{
		ctx:            ctx,
		stepCtx:        stepCtx,
		engine:         e,
		run:            run,
		step:           step,
		agent:          a,
		allowed:        allowed,
		required:       required,
		scope:          scope,
		attempt:        attempt,
		sources:        slices.Clone(sourceSnapshot),
		nativeCaptured: map[string]bool{},
		retiredSession: retiredSession,
	}
	rt.writer = newMessageWriter(e.Store, stepCtx, 100*time.Millisecond, run.ID, step.ID, a.ID, attempt.ID, message, messageSeq, func(err error) {
		rt.internalFailed.Store(true)
		e.report(err)
		stopStep()
	})
	request, requestErr := e.buildRequest(stepCtx, run, step, a, allowed, required, connection, adapter, message, handoff, scope, history, all, sourceSnapshot, previousDraft, rt.attempt)
	if requestErr != nil {
		err = requestErr
	} else if err = stepCtx.Err(); err != nil {
		// A checkpoint, user cancel or frozen deadline arrived before launch.
	} else if a.Executor != "" {
		runner := e.Runners[a.Executor]
		if runner == nil {
			runner = e.Fallback
		}
		if runner == nil {
			err = errors.New(capabilityBlockedPrefix + " " + a.Executor + ": selected CLI executor is unavailable")
		} else {
			err = runner.Run(stepCtx, request, rt.executeTool, rt.observeEvent)
		}
	} else {
		err = (agent.Loop{Model: e.Model}).Run(stepCtx, request, rt.executeTool, rt.observeEvent)
	}
	rt.mu.Lock()
	rt.closed = true
	rt.mu.Unlock()
	rt.execWG.Wait()
	if closeErr := rt.writer.Close(); closeErr != nil && err == nil {
		err = closeErr
	} else if closeErr != nil {
		e.report(closeErr)
	}
	message = rt.writer.Snapshot()
	rt.mu.Lock()
	publishedCopy := rt.published
	delegatedCopy := rt.delegated
	yieldRequest := rt.yieldRequest
	knownSources := slices.Clone(rt.sources)
	rt.mu.Unlock()
	if yieldRequest != nil && err == nil {
		yieldCtx, stopYield := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, _, yieldErr := project.New(e.Store).YieldRun(yieldCtx, run.ID, yieldRequest.Predicate, yieldRequest.ObservationCursor, yieldRequest.WakeSource, yieldRequest.EventID, "")
		stopYield()
		if yieldErr == nil {
			step.Status = "yielded"
			step.Error = "waiting for durable predicate: " + yieldRequest.Predicate
			return step
		}
		err = yieldErr
	}
	step, attempt, saveErr := e.settleStep(ctx, stepSettlement{
		run:             run,
		step:            step,
		agent:           a,
		required:        required,
		attempt:         rt.attempt,
		message:         message,
		published:       publishedCopy,
		delegated:       delegatedCopy,
		sources:         knownSources,
		stepCtx:         stepCtx,
		stepDeadline:    stepDeadline,
		checkpoint:      checkpoint,
		internalFailure: rt.internalFailed.Load(),
		cause:           err,
	})
	// Automatic retries create a fresh Attempt and Message after the failed
	// evidence is durable. Only a transient provider/connection failure with
	// no committed side effect, live context and untriggered checkpoint is
	// eligible; the persisted reason keeps the budget across restarts.
	if saveErr == nil &&
		step.Status == "failed" && attempt.Status == "failed" &&
		ctx.Err() == nil && !rt.mutated.Load() &&
		// attempt.Number is durable and includes the current attempt, so the
		// budget survives restart and cannot be reset by relabelling reasons.
		attempt.Number <= autoRetryLimit &&
		(checkpoint == nil || !checkpoint.triggered()) &&
		transientRetryable(err) {
		delay := e.retryDelay(attempt.Number - 1)
		eventErr := e.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
			return tx.AppendEvent(run.ID, step.ID, a.ID, "retry_scheduled", map[string]any{
				"attempt_id":        attempt.ID,
				"attempt_number":    attempt.Number,
				"next_attempt":      attempt.Number + 1,
				"max_attempts":      autoRetryLimit + 1,
				"failure_kind":      attempt.FailureKind,
				"reason":            autoRetryReason,
				"backoff_millis":    delay.Milliseconds(),
				"side_effects_seen": rt.mutated.Load(),
			})
		})
		if eventErr != nil {
			e.report(eventErr)
			return step
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return step
		case <-timer.C:
		}
		if ctx.Err() != nil || checkpoint != nil && checkpoint.triggered() {
			return step
		}
		return e.executeStepWith(ctx, run, step, history, all, sourceSnapshot, checkpoint)
	}
	return step
}
