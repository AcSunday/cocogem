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

// stepPreparation is the committed input of one attempt. A stopped preparation
// has already persisted its terminal or blocked state and must not launch a CLI.
type stepPreparation struct {
	step           w.Step
	agent          w.Agent
	allowed        []string
	required       string
	message        w.Message
	messageSeq     int
	attempt        w.Attempt
	handoff        Handoff
	scope          *readScope
	stepDeadline   time.Time
	previousDraft  *w.Artifact
	retiredSession string
	adapter        agent.AdapterInfo
	connection     agent.ConnectionInfo
	stopped        bool
}

func (e *Engine) prepareStepAttempt(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source) stepPreparation {
	memberIndex := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == step.AgentID })
	if memberIndex < 0 {
		step.Status, step.Error = "failed", "member is outside this run roster"
		step.FailureKind = failureUnknown
		if err := e.saveStep(ctx, step, "step_finished"); err != nil {
			e.report(err)
		}
		return stepPreparation{step: step, stopped: true}
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
		handoff, err = prepareStepHandoff(tx, run, step, a.ID, all, sourceSnapshot)
		if err != nil {
			return err
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
		return stepPreparation{step: step, stopped: true}
	}
	if attempt.Status == "blocked" {
		return stepPreparation{step: step, stopped: true}
	}
	if step.Status == "failed" {
		// A frozen step deadline already elapsed; no CLI was launched.
		return stepPreparation{step: step, stopped: true}
	}

	return stepPreparation{
		step:           step,
		agent:          a,
		allowed:        allowed,
		required:       required,
		message:        message,
		messageSeq:     messageSeq,
		attempt:        attempt,
		handoff:        handoff,
		scope:          scope,
		stepDeadline:   stepDeadline,
		previousDraft:  previousDraft,
		retiredSession: retiredSession,
		adapter:        adapter,
		connection:     connection,
	}
}
