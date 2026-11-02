package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// ErrRecoveryUncertain blocks resuming while an owned process cannot be verified.
var ErrRecoveryUncertain = errors.New("recovery_uncertain_process: an owned native process could not be verified; confirm it has stopped before resuming")

type attemptVerdict struct {
	status    string
	reason    string
	recovery  string
	uncertain bool
}

// claim takes the durable dispatch lease for exactly one live server.
func (e *Engine) claim(ctx context.Context, runID string) (bool, error) {
	own := w.RunClaim{RunID: runID, OwnerID: e.InstanceID, OwnerPID: e.Owner.PID, OwnerStart: e.Owner.Start, ClaimedAt: w.Now()}
	var existing w.RunClaim
	found := false
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		claim, err := tx.RunClaim(runID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		existing, found = claim, true
		return nil
	})
	if err != nil {
		return false, err
	}
	if !found {
		var claimed bool
		err := e.Store.Write(ctx, func(tx *store.Tx) error {
			var err error
			claimed, err = tx.InsertRunClaim(own)
			return err
		})
		return claimed, err
	}
	if existing.OwnerID == own.OwnerID {
		var claimed bool
		err := e.Store.Write(ctx, func(tx *store.Tx) error {
			var err error
			claimed, err = tx.TakeRunClaim(existing, own)
			return err
		})
		return claimed, err
	}
	current, inspectErr := proc.Inspect(existing.OwnerPID)
	if inspectErr == nil && current.Start == existing.OwnerStart {
		return false, nil
	}
	if inspectErr != nil && !errors.Is(inspectErr, proc.ErrNotRunning) {
		return false, fmt.Errorf("verify run claim owner: %w", inspectErr)
	}
	var claimed bool
	err = e.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		claimed, err = tx.TakeRunClaim(existing, own)
		return err
	})
	return claimed, err
}

func (e *Engine) release(ctx context.Context, runID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = e.Store.Write(ctx, func(tx *store.Tx) error { return tx.ReleaseRunClaim(runID, e.InstanceID) })
}

// recoverActive reconciles every run that a dead server left non-terminal.
func (e *Engine) recoverActive(ctx context.Context) error {
	var runs []w.Run
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		runs, err = tx.Active()
		return err
	}); err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status == "queued" {
			continue
		}
		claimed, err := e.claim(ctx, run.ID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if err := e.recoverRun(ctx, run.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				e.release(ctx, run.ID)
				continue
			}
			return err
		}
	}
	return nil
}

func (e *Engine) recoverRun(ctx context.Context, id string) error {
	var run w.Run
	var attempts []w.Attempt
	var steps []w.Step
	var messages []w.Message
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		if run, err = tx.Run(id); err != nil {
			return err
		}
		if _, err = tx.Thread(run.ThreadID); err != nil {
			return err
		}
		if attempts, err = tx.Attempts(id); err != nil {
			return err
		}
		if steps, err = tx.Steps(id); err != nil {
			return err
		}
		messages, err = tx.MessagesMetadata(run.ThreadID)
		return err
	}); err != nil {
		return err
	}
	if w.Terminal(run.Status) {
		e.release(ctx, id)
		return nil
	}
	verdicts := map[string]attemptVerdict{}
	uncertain := ""
	for _, attempt := range attempts {
		if attempt.Status != "running" {
			continue
		}
		verdict := e.reconcileAttempt(attempt)
		verdicts[attempt.ID] = verdict
		if verdict.uncertain {
			uncertain = verdict.reason
		}
	}
	blockedReason := ""
	changes := []string{}
	if uncertain != "" {
		blockedReason = "recovery_uncertain_process: " + uncertain
	} else if len(attempts) == 0 && slices.ContainsFunc(steps, func(step w.Step) bool { return step.Status == "running" }) {
		blockedReason = "recovery_uncertain_process: no execution attempt ownership was recorded; manual confirmation is required"
	} else if run.Status != "canceling" {
		blockedReason, changes = e.checkAdapters(ctx, run, steps, attempts)
		if blockedReason == "" && run.SkillSnapshot != "" {
			hash, err := skills.SnapshotHash(run.Workspace.SkillIDs)
			if err != nil || hash != run.SkillSnapshot {
				blockedReason = "checkpoint_blocked: shared skill version changed before recovery; start a new run"
			}
		}
	}
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(id)
		if err != nil {
			return err
		}
		if w.Terminal(current.Status) {
			return nil
		}
		stored, err := tx.Attempts(id)
		if err != nil {
			return err
		}
		for _, attempt := range stored {
			verdict, ok := verdicts[attempt.ID]
			if !ok || attempt.Status != "running" {
				continue
			}
			if current.Status == "canceling" {
				verdict.status = "canceled"
			}
			attempt.Status = verdict.status
			attempt.Error = verdict.reason
			attempt.Recovery = verdict.recovery
			attempt.FinishedAt = w.Now()
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(id, attempt.StepID, "", "attempt_finished", attempt); err != nil {
				return err
			}
		}
		finished, err := tx.Steps(id)
		if err != nil {
			return err
		}
		if current.Status == "canceling" {
			current.Status = "canceled"
			current.FinishedAt = w.Now()
			current.Error = ""
			if err := settlePending(tx, id, "canceled before restart recovery"); err != nil {
				return err
			}
			finished, err = tx.Steps(id)
			if err != nil {
				return err
			}
			for _, step := range finished {
				if step.Status == "running" || step.Status == "interrupted" {
					step.Status = "canceled"
					step.Error = "canceled by the user before restart recovery"
					if err := tx.SaveStep(step); err != nil {
						return err
					}
					if err := tx.AppendEvent(id, step.ID, step.AgentID, "step_finished", step); err != nil {
						return err
					}
				}
			}
			for _, message := range messages {
				if message.RunID == id && message.Status == "processing" {
					if err := tx.SetMessageStatus(message.ID, "canceled"); err != nil {
						return err
					}
				}
			}
			if err := tx.SaveRun(current); err != nil {
				return err
			}
			return tx.AppendEvent(id, "", "", "run_finished", current)
		}
		if blockedReason == "" {
			workspace, err := tx.Workspace(current.WorkspaceID)
			if err != nil || workspace.Root != current.Workspace.Root || tools.ValidateSourceRoot(current.Workspace.Root, e.AllowedRoots) != nil {
				blockedReason = "checkpoint_blocked: source directory authorization changed before recovery"
			}
		}
		if blockedReason == "" {
			for _, step := range finished {
				if step.Status == "succeeded" || step.Status == "failed" || step.Status == "skipped" {
					continue
				}
				if err := e.validateMember(tx, current, step.AgentID); err != nil {
					blockedReason = "checkpoint_blocked: " + err.Error()
					break
				}
			}
		}
		if blockedReason != "" {
			for _, step := range finished {
				if step.Status == "running" || step.Status == "canceled" {
					step.Status = "interrupted"
					if step.Error == "" {
						step.Error = "service restarted; checkpoint preserved"
					}
					if err := tx.SaveStep(step); err != nil {
						return err
					}
					if err := tx.AppendEvent(id, step.ID, step.AgentID, "step_finished", step); err != nil {
						return err
					}
				}
			}
			for _, message := range messages {
				if message.RunID == id && message.Status == "processing" {
					if err := tx.SetMessageStatus(message.ID, "interrupted"); err != nil {
						return err
					}
				}
			}
			current.Status = "interrupted"
			current.Error = blockedReason
			current.FinishedAt = w.Now()
			if err := tx.SaveRun(current); err != nil {
				return err
			}
			return tx.AppendEvent(id, "", "", "run_finished", current)
		}
		failed := ""
		canceled := ""
		unfinished := false
		for _, step := range finished {
			switch step.Status {
			case "failed":
				if failed == "" {
					failed = step.Error
				}
			case "canceled":
				canceled = step.Error
			case "pending", "running", "interrupted":
				unfinished = true
			case "skipped":
				if failed == "" {
					failed = step.Error
				}
			}
		}
		if !unfinished && (failed != "" || canceled != "") {
			status, cause := "failed", failed
			if failed == "" {
				status, cause = "canceled", canceled
			}
			if err := settlePending(tx, id, "run ended during restart recovery"); err != nil {
				return err
			}
			finished, err = tx.Steps(id)
			if err != nil {
				return err
			}
			for _, step := range finished {
				if step.Status != "running" && step.Status != "interrupted" {
					continue
				}
				step.Status = "interrupted"
				step.Error = "run ended during restart recovery"
				if status == "canceled" {
					step.Status = "canceled"
				}
				if err := tx.SaveStep(step); err != nil {
					return err
				}
				if err := tx.AppendEvent(id, step.ID, step.AgentID, "step_finished", step); err != nil {
					return err
				}
			}
			for _, message := range messages {
				if message.RunID == id && message.Status == "processing" {
					target := "interrupted"
					if status == "canceled" {
						target = "canceled"
					}
					if err := tx.SetMessageStatus(message.ID, target); err != nil {
						return err
					}
				}
			}
			current.Status = status
			current.Error = cause
			current.FinishedAt = w.Now()
			if err := tx.SaveRun(current); err != nil {
				return err
			}
			return tx.AppendEvent(id, "", "", "run_finished", current)
		}
		if !unfinished {
			// A recovered legacy run recorded no delivery, so it keeps nil. A
			// run under the delivery contract must prove every final target
			// before it may settle succeeded; otherwise it fails here instead
			// of remaining active with incomplete metadata.
			if current.Delivery != nil {
				delivery, err := buildDelivery(tx, current)
				if err != nil {
					current.Status = "failed"
					current.FailureKind = failureArtifactContract
					current.Error = "final delivery is incomplete: " + err.Error()
					current.FinishedAt = w.Now()
					if err := tx.SaveRun(current); err != nil {
						return err
					}
					return tx.AppendEvent(id, "", "", "run_finished", current)
				}
				current.Delivery = delivery
				current.Method = settledMethod(current, finished)
			}
			current.Status = "succeeded"
			current.Error = ""
			current.FinishedAt = w.Now()
			if err := tx.SaveRun(current); err != nil {
				return err
			}
			if current.Delivery != nil && current.SelectedWorkflow == nil && (current.Input.Workflow == "" || current.Input.Workflow == "free") {
				if err := tx.AppendEvent(id, "", "", "method_recorded", map[string]any{"method": current.Method}); err != nil {
					return err
				}
			}
			return tx.AppendEvent(id, "", "", "run_finished", current)
		}
		for i := range finished {
			if finished[i].Status == "running" || finished[i].Status == "interrupted" || finished[i].Status == "canceled" {
				finished[i].Status = "pending"
				finished[i].Error = ""
				if err := tx.SaveStep(finished[i]); err != nil {
					return err
				}
			}
		}
		for _, message := range messages {
			if message.RunID == id && message.Status == "processing" {
				if err := tx.SetMessageStatus(message.ID, "interrupted"); err != nil {
					return err
				}
				preview, err := tx.MessagePreview(message.ID, 6000)
				if err != nil {
					return err
				}
				if err := tx.AppendEvent(id, message.StepID, message.AgentID, "message_finished", preview); err != nil {
					return err
				}
			}
		}
		current.Status = "queued"
		current.Error = ""
		current.FinishedAt = ""
		current.RecoveryCount++
		current.RecoveredAt = w.Now()
		current.RecoveryNote = "service_restart"
		if len(changes) > 0 {
			current.RecoveryNote = "service_restart;cli_changed:" + strings.Join(changes, ",")
		}
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		return tx.AppendEvent(id, "", "", "run_recovered", map[string]any{"reason": "service_restart", "changed_clis": changes, "attempts": len(verdicts)})
	})
	if err != nil {
		e.release(ctx, id)
		return err
	}
	e.release(ctx, id)
	return nil
}
