package workflow

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
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
	// verifyDir is the independent reviewer sandbox derived before dispatch. It
	// is destroyed after settlement and the author candidate must be unchanged.
	verifyDir *w.WorkspaceExecution
	workRoot  string
	stopped   bool
}

// isIndependentReview reports whether this step must run in its own sandbox and
// a fresh native session, never inside the author's candidate.
func isIndependentReview(step w.Step) bool {
	return step.Role == "reviewer" || step.Role == "critic"
}

// prepareReviewSandbox materializes the reviewer's isolated copy of the author's
// exact candidate tree (or direct-write project tree) before any child process starts.
// When a non-git root exceeds isolated copy limits, it reports sandboxSkipped=true
// so the caller atomically degrades the step to read-only before computing allowed
// tools, InputPlanDigest, and step_started.
func (e *Engine) prepareReviewSandbox(ctx context.Context, run w.Run, step w.Step) (*w.WorkspaceExecution, bool, error) {
	if e.Executions == nil || !isIndependentReview(step) {
		return nil, false, nil
	}
	if run.ExecutionRef != "" {
		verification, err := e.Executions.VerifyTree(ctx, run.ExecutionRef, step.ID)
		if err != nil {
			return nil, false, err
		}
		return &verification, false, nil
	}
	if run.Workspace.Root != "" {
		verification, err := e.Executions.VerifyDirectRoot(ctx, run.Workspace.Root, run.WorkspaceID, run.ThreadID, run.TaskID, step.FromAgentID, step.ID)
		if err != nil {
			if errors.Is(err, execution.ErrSourceTooLarge) || errors.Is(err, tools.ErrDigestTooLarge) {
				slog.Warn("review sandbox skipped; source exceeds isolated copy limits",
					"run_id", run.ID, "step_id", step.ID, "workspace_root", run.Workspace.Root, "error", err)
				return nil, true, nil
			}
			return nil, false, err
		}
		return &verification, false, nil
	}
	return nil, false, nil
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
	// Materialize the reviewer's independent sandbox before computing allowed
	// tools, InputPlanDigest, and step_started so any oversized non-git read-only
	// degradation is reflected consistently across prompt, policy, and events.
	var verifyDir *w.WorkspaceExecution
	var sandboxSkipped bool
	var sandboxErr error
	if a.Executor == "" || e.Adapters == nil || adapter.Supported {
		verifyDir, sandboxSkipped, sandboxErr = e.prepareReviewSandbox(ctx, run, step)
		if sandboxSkipped {
			step.ReadOnly = true
		}
	}
	cleanupVerify := func() {
		if verifyDir == nil || e.Executions == nil {
			return
		}
		cleanCtx, stopClean := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stopClean()
		if cleanErr := e.Executions.RemoveDirectories(cleanCtx, []w.WorkspaceExecution{*verifyDir}); cleanErr != nil {
			e.report(cleanErr)
		}
		verifyDir = nil
	}
	if sandboxErr != nil {
		now := w.Now()
		step.Status = "failed"
		step.FailureKind = failureUnknown
		step.Error = sandboxErr.Error()
		if saveErr := e.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
			existing, err := tx.StepAttempts(step.ID)
			if err != nil {
				return err
			}
			number := len(existing) + 1
			failedAttempt := w.Attempt{
				ID: w.NewID(), RunID: run.ID, StepID: step.ID, Number: number,
				Status: "failed", FailureKind: failureUnknown, Error: sandboxErr.Error(),
				Reason: "initial", StartedAt: now, LastProgress: now, FinishedAt: now,
				Executor: a.Executor, Model: a.Model, ResolvedModel: connection.ResolvedModel,
				ConnectionID: connection.Identity, Adapter: adapter.Adapter,
				CLIVersion: adapter.Version, CLIProgram: adapter.Program, CLIIdentity: adapter.Identity,
				SessionMode: "fresh",
			}
			step.AttemptCount = number
			if err := tx.SaveAttempt(failedAttempt); err != nil {
				return err
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", failedAttempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", failedAttempt); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "step_finished", step)
		}); saveErr != nil {
			e.report(saveErr)
		}
		return stepPreparation{step: step, stopped: true}
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
		allowed, err = filterAuthorizedTools(tx, run, step.AgentID, allowed)
		if err != nil {
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
				// The overflowing native session is retired for good: sealing the
				// Cocogem row keeps the next attempt from ever resuming it, and the
				// continuation then gets the checkpoint capsule instead.
				if err := sealExecutionSession(tx, latest.ExecutionSessionRef); err != nil {
					return err
				}
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
		session, err := prepareExecutionSession(tx, sessionInput{
			run: run, step: step, member: a, adapter: adapter, connection: connection,
			attempt: attempt, sources: sourceSnapshot,
		})
		if err != nil {
			return err
		}
		if session.ID != "" {
			attempt.ExecutionSessionRef = session.ID
			attempt.SessionFingerprint = session.Fingerprint
			if session.NativeSessionID != "" {
				attempt.SessionMode = "resume"
				attempt.SessionID = session.NativeSessionID
			}
			if err := tx.SaveSession(session); err != nil {
				return err
			}
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
		if sandboxSkipped {
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "native_notice", map[string]any{
				"kind":    "verify_sandbox_skipped",
				"message": "bound workspace is not a git repository and exceeds isolated copy limits; running review read-only in the workspace root",
			}); err != nil {
				return err
			}
		}
		return tx.AppendEvent(run.ID, step.ID, a.ID, "message_started", message)
	})
	if err != nil {
		cleanupVerify()
		step.Status = "failed"
		step.FailureKind = failureUnknown
		step.Error = err.Error()
		if saveErr := e.saveStep(context.WithoutCancel(ctx), step, "step_finished"); saveErr != nil {
			e.report(saveErr)
		}
		return stepPreparation{step: step, stopped: true}
	}
	if attempt.Status == "blocked" {
		cleanupVerify()
		return stepPreparation{step: step, stopped: true}
	}
	if step.Status == "failed" {
		cleanupVerify()
		// A frozen step deadline already elapsed; no CLI was launched.
		return stepPreparation{step: step, stopped: true}
	}

	// The step's working root defaults to the run's project or candidate root.
	workRoot := run.ProjectRoot()
	if verifyDir != nil {
		workRoot = verifyDir.Path
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
		verifyDir:      verifyDir,
		workRoot:       workRoot,
	}
}
