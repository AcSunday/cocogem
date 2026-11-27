package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
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
	// trusted marks an invocation whose CLI may run arbitrary native tools.
	trusted := false
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
		workRoot:       prep.workRoot,
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
	rt.thinking = newThinkingWriter(e.Store, stepCtx, 150*time.Millisecond, run.ID, step.ID, a.ID, attempt.ID)
	request, requestErr := e.buildRequest(stepCtx, run, step, a, allowed, required, connection, adapter, message, handoff, scope, history, all, sourceSnapshot, previousDraft, rt.attempt)
	if requestErr == nil && prep.workRoot != "" {
		// A reviewer step runs inside its own sandbox copy, never the author's
		// candidate or the bound project.
		request.WorkspaceRoot = prep.workRoot
	}
	if requestErr == nil {
		// Every CLI-owned effect must be admitted by the live host policy before
		// it happens. The callback is bound here, per attempt, so it always sees
		// this run's frozen identity and the live epoch.
		rt.bindNativeAuthorizer(&request)
	}
	if requestErr != nil {
		err = requestErr
	} else if run.TaskPromotionStepID == step.ID && run.TaskID != "" {
		// Promotion already committed before interruption. Complete only the
		// planning receipt; its persisted continuation owns the actual work.
		rt.delegated = true
		request.TrustedExecution = false
		err = rt.observeEvent("text", map[string]any{"text": "任务已建立，正在继续执行。"})
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
			trusted = request.TrustedExecution
			err = runner.Run(stepCtx, request, rt.executeTool, rt.observeEvent)
			// Continuity Handshake transparent self-healing: if a resumed native
			// session fails before mutating (e.g. context overflow or corrupted
			// thinking signature after Stop), seal the broken session and retry
			// cleanly with a fresh session + Continuity Capsule in this same step.
			if err != nil && request.SessionMode == "resume" && !rt.mutated.Load() && stepCtx.Err() == nil && shouldAutoSealResumeError(err) {
				if sealErr := e.sealBrokenSession(stepCtx, rt.attempt.ExecutionSessionRef); sealErr == nil {
					rt.mu.Lock()
					rt.attempt.SessionMode = "fresh"
					rt.attempt.SessionID = ""
					rt.attempt.Recovery = "context_fresh"
					rt.retiredSession = rt.attempt.ExecutionSessionRef
					freshAttempt := rt.attempt
					rt.mu.Unlock()
					if freshReq, freshErr := e.buildRequest(stepCtx, run, step, a, allowed, required, connection, adapter, message, handoff, scope, history, all, sourceSnapshot, previousDraft, freshAttempt); freshErr == nil {
						if prep.workRoot != "" {
							freshReq.WorkspaceRoot = prep.workRoot
						}
						rt.bindNativeAuthorizer(&freshReq)
						request = freshReq
						trusted = request.TrustedExecution
						err = runner.Run(stepCtx, request, rt.executeTool, rt.observeEvent)
					}
				}
			}
		}
	} else {
		err = (agent.Loop{Model: e.Model}).Run(stepCtx, request, rt.executeTool, rt.observeEvent)
	}
	// A successful file-producing step publishes its declared outputs. Full
	// access trusted execution and project-autonomous host/native file work both
	// count; a read-only step simply has no outputs directory.
	if err == nil && (request.TrustedExecution || rt.collectsAutonomousOutputs()) {
		if fileErr := rt.collectOutputFiles(stepCtx); fileErr != nil {
			err = fileErr
		}
	}
	rt.mu.Lock()
	rt.closed = true
	rt.mu.Unlock()
	rt.execWG.Wait()
	if rt.thinking != nil {
		_ = rt.thinking.Close()
	}
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
	destroyVerify := func() error {
		if prep.verifyDir == nil || e.Executions == nil {
			return nil
		}
		destroyCtx, stopDestroy := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stopDestroy()
		return e.Executions.DestroyVerifyTree(destroyCtx, *prep.verifyDir, run.ExecutionRef)
	}
	if yieldRequest != nil && err == nil {
		if destroyErr := destroyVerify(); destroyErr != nil {
			e.report(destroyErr)
			err = destroyErr
		} else {
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
	}
	// The reviewer sandbox is disposable: destroy it after the step settles and
	// assert the reviewed candidate/project never changed. Tampering MUST fail the step.
	if destroyErr := destroyVerify(); destroyErr != nil {
		e.report(destroyErr)
		if err == nil {
			err = destroyErr
		}
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
	// Arbitrary native tools may have effects the runner cannot report before a
	// disconnect, so a trusted invocation is never replayed to guess whether
	// they happened. It is replayed only when its adapter proves the CLI
	// reported the failure itself before requesting any tool.
	replaySafe := !rt.mutated.Load() && (!trusted || nativecli.ToolFree(err))
	// Automatic retries create a fresh Attempt and Message after the failed
	// evidence is durable. Only a transient provider/connection failure with
	// no committed side effect, live context and untriggered checkpoint is
	// eligible; the persisted reason keeps the budget across restarts.
	if saveErr == nil &&
		step.Status == "failed" && attempt.Status == "failed" &&
		ctx.Err() == nil && replaySafe &&
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

// shouldAutoSealResumeError reports whether a failure on a resumed native
// session indicates session overflow, corruption (e.g. truncated thinking
// signature after Stop) or missing/invalid native session identity, so the
// host can seal the session and transparently restart with a Continuity Capsule.
func shouldAutoSealResumeError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	var nf *nativecli.Failure
	if errors.As(err, &nf) {
		switch nf.Kind {
		case nativecli.FailureContextOverflow, nativecli.FailureProtocol, nativecli.FailureHostInputLimit:
			return true
		}
	}
	for _, marker := range []string{
		"context_length_exceeded", "context window", "prompt is too long", "maximum context length",
		"thinking", "signature", "session not found", "no conversation found", "invalid session",
		"could not resume", "resume failed", "corrupt",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (e *Engine) sealBrokenSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return e.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
		session, err := tx.Session(sessionID)
		if err != nil {
			return err
		}
		session.Status = "sealed"
		session.Acknowledgement = "sealed"
		session.NativeSessionID = ""
		session.UpdatedAt = w.Now()
		return tx.SaveSession(session)
	})
}
