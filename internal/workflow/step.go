package workflow

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (e *Engine) executeStep(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source) w.Step {
	return e.executeStepWith(ctx, run, step, history, all, sourceSnapshot, nil)
}

func (e *Engine) executeStepWith(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source, checkpoint *checkpointState) w.Step {
	memberIndex := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == step.AgentID })
	if memberIndex < 0 {
		step.Status, step.Error = "failed", "member is outside this run roster"
		step.FailureKind = failureUnknown
		if err := e.saveStep(ctx, step, "step_finished"); err != nil {
			e.report(err)
		}
		return step
	}
	a := run.Agents[memberIndex]
	var adapter agent.AdapterInfo
	var connection agent.ConnectionInfo
	connectionKnown := false
	connectionBlocked := false
	if a.Executor != "" && e.Adapters != nil {
		var checkErr error
		adapter, checkErr = e.Adapters.Check(ctx, a.Executor)
		if checkErr != nil && !adapter.Installed {
			adapter.Reason = checkErr.Error()
		}
		if adapter.Supported {
			var connectionErr error
			connection, connectionKnown, connectionErr = e.connectionInfo(ctx, a.Executor, a.Model, a.ThinkingLevel)
			if connectionErr != nil {
				adapter.Supported = false
				adapter.Reason = "连接配置不可用: " + connectionErr.Error()
				connectionBlocked = true
			}
		}
	}
	allowed := e.allowedTools(run, a, step)
	required := RequiredArtifact(step, effectiveInput(run))
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: step.ID, AgentID: a.ID, AgentName: a.DisplayName, Role: "assistant", Status: "processing", ContentFormat: w.ContentFormatChunksV1, CreatedAt: w.Now()}
	messageSeq := 0
	step.Status = "running"
	var handoff Handoff
	var attempt w.Attempt
	var scope *readScope
	var stepDeadline time.Time
	var previousDraft *w.Artifact
	var retiredSession string
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return errors.New("run stopped before dispatch")
		}
		if err := e.validateMember(tx, run, step.AgentID); err != nil {
			return err
		}
		existing, err := tx.StepAttempts(step.ID)
		if err != nil {
			return err
		}
		number := len(existing) + 1
		reason := "initial"
		recovery := ""
		recoveredFrom := ""
		if len(existing) > 0 {
			latest := existing[len(existing)-1]
			reason = "retry_after_interruption"
			recovery = "new_attempt_from_context"
			recoveredFrom = latest.ID
			switch {
			case latest.Status == "failed" && latest.FailureKind == string(nativecli.FailureContextOverflow):
				reason = "context_fresh"
				recovery = "context_fresh"
				retiredSession = latest.SessionID
			case latest.Status == "failed" && transientRetryableKind(latest.FailureKind):
				reason = autoRetryReason
				recovery = "new_attempt_after_transient_failure"
			}
			previousDraft = latest.DraftArtifact
		}
		// A transient retry continues the same bubble: append the new attempt's
		// text to the failed message. Interrupted checkpoints keep their own
		// partial message as recovery evidence.
		if reason == autoRetryReason {
			previous, findErr := tx.LatestStepMessage(run.ID, step.ID)
			switch {
			case findErr == nil && previous.Status != "completed" && previous.Status != "canceled" && previous.ContentFormat == w.ContentFormatChunksV1:
				seq, seqErr := tx.MessageLastChunkSeq(previous.ID)
				if seqErr != nil {
					return seqErr
				}
				message, messageSeq = previous, seq
			case findErr != nil && !errors.Is(findErr, store.ErrNotFound):
				return findErr
			}
		}
		message.Status = "processing"
		connectionChange := ""
		for i := len(existing) - 1; i >= 0; i-- {
			if existing[i].Status == "blocked" {
				continue
			}
			connectionChange = connectionChanged(existing[i], connection, connectionKnown)
			break
		}
		policy := w.ExecutionPolicy{}
		if run.ExecutionPolicy != nil {
			policy = *run.ExecutionPolicy
		}
		dispatchNow := time.Now()
		deadline, alreadyExpired := applyStepDeadline(&step, existing, policy, dispatchNow)
		attempt = w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: step.ID, Number: number, Status: "running", Reason: reason, Recovery: recovery, RecoveredFrom: recoveredFrom, SourceIDs: sourceIDs(sourceSnapshot), StartedAt: dispatchNow.UTC().Format(time.RFC3339Nano), LastProgress: dispatchNow.UTC().Format(time.RFC3339Nano), Executor: a.Executor, Model: a.Model, ResolvedModel: connection.ResolvedModel, ConnectionID: connection.Identity, Adapter: adapter.Adapter, CLIVersion: adapter.Version, CLIProgram: adapter.Program, CLIIdentity: adapter.Identity, SessionMode: "fresh"}
		// The message belongs to this exact attempt, so a retry cannot make the
		// UI attribute it to a newer attempt's model.
		message.AttemptID, message.Model, message.ResolvedModel = attempt.ID, attempt.Model, attempt.ResolvedModel
		if a.Executor != "" && e.Adapters != nil && (!adapter.Supported || connectionChange != "") {
			reason := adapter.Reason
			if reason == "" {
				reason = "adapter compatibility could not be confirmed"
			}
			attempt.Status = "blocked"
			switch {
			case connectionChange != "":
				attempt.Error = connectionChange
			case connectionBlocked:
				attempt.Error = connectionBlockedPrefix + " " + a.Executor + ": " + reason
			default:
				attempt.Error = capabilityBlockedPrefix + " " + a.Executor + ": " + reason
			}
			step.Status = "interrupted"
			if ctx.Err() != nil {
				attempt.Status = "canceled"
				attempt.Error = ctx.Err().Error()
				step.Status = "canceled"
			}
			attempt.FailureKind = "capability_blocked"
			if strings.HasPrefix(attempt.Error, connectionBlockedPrefix) || strings.HasPrefix(attempt.Error, connectionChangedPrefix) {
				attempt.FailureKind = "connection_blocked"
			}
			attempt.FinishedAt = w.Now()
			step.Error = attempt.Error
			step.FailureKind = attempt.FailureKind
			step.AttemptCount = number
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", attempt)
		}
		// A blocked preflight never reaches this point, so this is the first
		// actual dispatch of the logical step and its clock starts here.
		if step.StartedAt == "" {
			startStepClock(&step, policy, dispatchNow)
			deadline = parseMoment(step.DeadlineAt)
			alreadyExpired = expired(deadline, dispatchNow)
		}
		stepDeadline = deadline
		if len(step.DependsOn) > 0 || len(step.ContextRefs) > 0 {
			artifacts, err := tx.ArtifactsMetadata(run.ThreadID)
			if err != nil {
				return err
			}
			// The planning root is a scheduling dependency only. Its message and
			// artifacts are withheld from the handoff so a planner that answered
			// the task anyway cannot seed the independent views.
			planningRoot := planningRootStep(run)
			dependentArtifacts := []w.Artifact{}
			for _, artifact := range artifacts {
				if artifact.RunID != run.ID || artifact.StepID == planningRoot || !slices.Contains(step.DependsOn, artifact.StepID) {
					continue
				}
				preview, err := tx.ArtifactPreview(artifact.ID, artifact.Version, 6000)
				if err != nil {
					return err
				}
				dependentArtifacts = append(dependentArtifacts, preview)
			}
			messages := all
			if planningRoot != "" {
				messages = slices.DeleteFunc(slices.Clone(all), func(m w.Message) bool { return m.StepID == planningRoot })
			}
			handoff = prepareHandoff(step, run.Input.Message, messages, dependentArtifacts, sourceSnapshot)
			if err := addReferencedInputs(tx, step, run.ThreadID, &handoff); err != nil {
				return err
			}
			if err := sealHandoff(tx, &handoff); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "handoff_prepared", handoff); err != nil {
				return err
			}
		}
		limits := e.historyLimitsFor(a)
		selected := recentHistory(history, run.ID, limits.Messages, limits.Tokens)
		historyIDs := make([]string, 0, len(selected))
		for _, message := range selected {
			historyIDs = append(historyIDs, message.ID)
		}
		scope, err = buildReadScope(tx, run, step, historyIDs, sourceSnapshot)
		if err != nil {
			return err
		}
		attempt.InputPlanDigest, err = inputPlanDigest(tx, step, run, a.Model, connection, adapter, scope, allowed, sourceSnapshot)
		if err != nil {
			return err
		}
		attempt.CheckpointStart, err = checkpointDigest(tx, run.ID, run.ThreadID, step.ID, attempt.SourceIDs, previousDraft)
		if err != nil {
			return err
		}
		if alreadyExpired {
			attempt.Status = "failed"
			attempt.FailureKind = failureStepTimeout
			attempt.Error = "step_timeout: the frozen step deadline elapsed before dispatch"
			attempt.FinishedAt = w.Now()
			step.Status = "failed"
			step.FailureKind = failureStepTimeout
			step.Error = attempt.Error
			step.AttemptCount = number
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", attempt); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "step_finished", step)
		}
		step.AttemptCount = number
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "step_started", step); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
			return err
		}
		if recovery == "context_fresh" && retiredSession != "" {
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "session_retired", map[string]any{"attempt_id": recoveredFrom, "session_id": retiredSession}); err != nil {
				return err
			}
		}
		return tx.AppendEvent(run.ID, step.ID, a.ID, "message_started", message)
	})
	if err != nil {
		step.Status = "failed"
		step.FailureKind = failureUnknown
		step.Error = err.Error()
		if saveErr := e.saveStep(context.WithoutCancel(ctx), step, "step_finished"); saveErr != nil {
			e.report(saveErr)
		}
		return step
	}
	if attempt.Status == "blocked" {
		return step
	}
	if step.Status == "failed" {
		// A frozen step deadline already elapsed; no CLI was launched.
		return step
	}
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
	knownSources := slices.Clone(rt.sources)
	rt.mu.Unlock()
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
