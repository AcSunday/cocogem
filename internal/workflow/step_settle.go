package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// stepSettlement carries the state produced by one attempt into the single
// place that classifies, validates and persists the final step outcome.
type stepSettlement struct {
	run             w.Run
	step            w.Step
	agent           w.Agent
	required        string
	attempt         w.Attempt
	message         w.Message
	published       *w.Artifact
	delegated       bool
	sources         []w.Source
	stepCtx         context.Context
	stepDeadline    time.Time
	checkpoint      *checkpointState
	internalFailure bool
	cause           error
}

// stepCompletion is the immutable input of the final persistence transaction.
type stepCompletion struct {
	run       w.Run
	step      w.Step
	agentID   string
	required  string
	attempt   w.Attempt
	message   w.Message
	published *w.Artifact
	sources   []w.Source
}

func (e *Engine) settleStep(ctx context.Context, s stepSettlement) (w.Step, w.Attempt, error) {
	step, attempt, message := s.step, s.attempt, s.message
	published := s.published
	if s.cause == nil && s.required != "" && published == nil && !s.delegated {
		s.cause = &nativecli.Failure{Kind: nativecli.FailureKind(failureArtifactContract), Reason: "required artifact was not submitted"}
	}
	runExpired := ctx.Err() == context.DeadlineExceeded
	stepDeadlineExpired := !s.stepDeadline.IsZero() && !time.Now().Before(s.stepDeadline) && errors.Is(s.stepCtx.Err(), context.DeadlineExceeded)
	if s.cause == nil && s.stepCtx.Err() != nil {
		s.cause = s.stepCtx.Err()
	}
	outcome := stepOutcome{step: "succeeded", attempt: "succeeded"}
	if s.cause != nil {
		checkpointed := s.checkpoint != nil && s.checkpoint.by(step.ID)
		outcome = classifyStepError(
			s.cause,
			s.stepCtx.Err(),
			checkpointed,
			s.internalFailure,
			runExpired,
			stepDeadlineExpired,
		)
	}
	step.Status = outcome.step
	step.Error = outcome.reason
	step.FailureKind = outcome.kind
	if step.Status == "succeeded" {
		step.Error = ""
		step.FailureKind = ""
	}
	attempt.Status = outcome.attempt
	attempt.Error = outcome.reason
	attempt.FailureKind = outcome.kind
	if outcome.recovery != "" {
		attempt.Recovery = outcome.recovery
	}
	attempt.FinishedAt = w.Now()
	if step.Status != "succeeded" && published != nil {
		draft := *published
		attempt.DraftArtifact = &draft
	} else if step.Status == "succeeded" {
		attempt.DraftArtifact = nil
	}
	if outcome.kind == failureCheckpointInterrupt && s.checkpoint != nil {
		if description := s.checkpoint.describe(); description != "" {
			step.Error = "checkpoint_interrupted: " + description
			attempt.Error = step.Error
		}
	}
	if step.Status == "interrupted" && s.checkpoint != nil {
		s.checkpoint.request(step.ID, step.Error)
	}
	switch step.Status {
	case "succeeded":
		message.Status = "completed"
	case "interrupted":
		message.Status = "interrupted"
	case "canceled":
		message.Status = "canceled"
	default:
		message.Status = "failed"
	}
	if step.Status == "succeeded" && published != nil {
		if validationErr := ValidateArtifact(*published, s.required, s.sources); validationErr != nil {
			step.Status, message.Status, attempt.Status = "failed", "failed", "failed"
			step.Error, attempt.Error = validationErr.Error(), validationErr.Error()
			step.FailureKind, attempt.FailureKind = "artifact_contract", "artifact_contract"
			attempt.DraftArtifact = published
		}
	}
	completion := stepCompletion{
		run:       s.run,
		step:      step,
		agentID:   s.agent.ID,
		required:  s.required,
		attempt:   attempt,
		message:   message,
		published: published,
		sources:   s.sources,
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	attempt, saveErr := e.persistStepCompletion(cleanup, completion)
	if saveErr != nil {
		step.Status = "failed"
		step.FailureKind = failureUnknown
		step.Error = saveErr.Error()
		if updateErr := e.Store.Write(cleanup, func(tx *store.Tx) error {
			stored, readErr := tx.Attempt(attempt.ID)
			if readErr != nil {
				return readErr
			}
			stored.Status = "failed"
			stored.Error = saveErr.Error()
			stored.FailureKind = failureUnknown
			stored.FinishedAt = w.Now()
			if err := tx.SaveAttempt(stored); err != nil {
				return err
			}
			return tx.SaveStep(step)
		}); updateErr != nil {
			e.report(updateErr)
		}
		e.report(saveErr)
	}
	return step, attempt, saveErr
}

// persistStepCompletion writes the artifact, attempt, message and step together
// with the terminal events. The caller commits the outcome only once.
func (e *Engine) persistStepCompletion(ctx context.Context, c stepCompletion) (w.Attempt, error) {
	attempt := c.attempt
	message := c.message
	step := c.step
	published := c.published
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(c.run.ID)
		if err != nil {
			return err
		}
		// Re-read the run: this step may itself have selected a workflow.
		current, err := tx.Run(c.run.ID)
		if err != nil {
			return err
		}
		formal := map[string]bool{}
		for _, final := range finalSteps(effectiveInput(current), steps) {
			formal[final.ID] = true
		}
		if current.TaskPromotionStepID == step.ID {
			step.Role = "task_intake"
			message.Content = "任务已建立，正在继续执行。"
			message.ContentFormat = ""
			message.ContentBytes = len(message.Content)
			message.ContentTruncated = false
		}
		if published != nil && step.Status == "succeeded" && formal[step.ID] {
			if len(current.Input.ArtifactRefs) > 0 {
				ref := current.Input.ArtifactRefs[0]
				previous, err := tx.Artifact(ref.ID, ref.Version)
				if err != nil {
					return err
				}
				if previous.Type == published.Type {
					versions, err := tx.Artifacts(c.run.ThreadID)
					if err != nil {
						return err
					}
					published.ID = previous.ID
					for _, v := range versions {
						if v.ID == previous.ID {
							published.Version = max(published.Version, v.Version+1)
						}
					}
				}
			}
			if err := tx.SaveArtifact(*published); err != nil {
				return err
			}
			if err := tx.AppendEvent(c.run.ID, step.ID, c.agentID, "artifact_published", published); err != nil {
				return err
			}
		}
		if published != nil && step.Status == "succeeded" && !formal[step.ID] {
			// A root that delegated, or a superseded continuation, keeps its
			// submission as attempt evidence; it must never become formal.
			draft := *published
			attempt.DraftArtifact = &draft
		}
		if stored, readErr := tx.Attempt(attempt.ID); readErr == nil {
			stored.Status = attempt.Status
			stored.Error = attempt.Error
			stored.FailureKind = attempt.FailureKind
			stored.Recovery = attempt.Recovery
			stored.RecoveredFrom = attempt.RecoveredFrom
			stored.SessionID = attempt.SessionID
			stored.SessionMode = attempt.SessionMode
			stored.DraftArtifact = attempt.DraftArtifact
			stored.UsageKnown = attempt.UsageKnown
			stored.UsageCompleteness = attempt.UsageCompleteness
			stored.InputTokens = attempt.InputTokens
			stored.OutputTokens = attempt.OutputTokens
			stored.FinishedAt = attempt.FinishedAt
			attempt = stored
		}
		if digest, err := checkpointDigest(tx, c.run.ID, c.run.ThreadID, step.ID, attempt.SourceIDs, attempt.DraftArtifact); err == nil {
			attempt.CheckpointDigest = digest
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.AppendEvent(c.run.ID, step.ID, c.agentID, "message_finished", message); err != nil {
			return err
		}
		if err := tx.AppendEvent(c.run.ID, step.ID, c.agentID, "attempt_finished", attempt); err != nil {
			return err
		}
		return tx.AppendEvent(c.run.ID, step.ID, c.agentID, "step_finished", step)
	})
	return attempt, err
}
