package workflow

import (
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// recoveryTransaction owns one restart-recovery decision. Every terminal
// outcome is a method so the decision order stays visible in recoverRun.
type recoveryTransaction struct {
	e        *Engine
	tx       *store.Tx
	id       string
	run      *w.Run
	steps    []w.Step
	messages []w.Message
	changes  []string
	verdicts map[string]attemptVerdict
}

func (r *recoveryTransaction) applyVerdicts() error {
	stored, err := r.tx.Attempts(r.id)
	if err != nil {
		return err
	}
	for _, attempt := range stored {
		verdict, ok := r.verdicts[attempt.ID]
		if !ok || attempt.Status != "running" {
			continue
		}
		if r.run.Status == "canceling" {
			verdict.status = "canceled"
		}
		attempt.Status = verdict.status
		attempt.Error = verdict.reason
		attempt.Recovery = verdict.recovery
		attempt.FinishedAt = w.Now()
		if err := r.tx.SaveAttempt(attempt); err != nil {
			return err
		}
		if err := r.tx.AppendEvent(r.id, attempt.StepID, "", "attempt_finished", attempt); err != nil {
			return err
		}
	}
	r.steps, err = r.tx.Steps(r.id)
	return err
}

func (r *recoveryTransaction) finishCanceled() error {
	r.run.Status = "canceled"
	r.run.FinishedAt = w.Now()
	r.run.Error = ""
	if err := settlePending(r.tx, r.id, "canceled before restart recovery"); err != nil {
		return err
	}
	steps, err := r.tx.Steps(r.id)
	if err != nil {
		return err
	}
	r.steps = steps
	for _, step := range r.steps {
		if step.Status == "running" || step.Status == "interrupted" {
			step.Status = "canceled"
			step.Error = "canceled by the user before restart recovery"
			if err := r.tx.SaveStep(step); err != nil {
				return err
			}
			if err := r.tx.AppendEvent(r.id, step.ID, step.AgentID, "step_finished", step); err != nil {
				return err
			}
		}
	}
	for _, message := range r.messages {
		if message.RunID == r.id && message.Status == "processing" {
			if err := r.tx.SetMessageStatus(message.ID, "canceled"); err != nil {
				return err
			}
		}
	}
	if err := r.tx.SaveRun(*r.run); err != nil {
		return err
	}
	return r.tx.AppendEvent(r.id, "", "", "run_finished", *r.run)
}

func (r *recoveryTransaction) block(reason string) error {
	for _, step := range r.steps {
		if step.Status == "running" || step.Status == "canceled" {
			step.Status = "interrupted"
			if step.Error == "" {
				step.Error = "service restarted; checkpoint preserved"
			}
			if err := r.tx.SaveStep(step); err != nil {
				return err
			}
			if err := r.tx.AppendEvent(r.id, step.ID, step.AgentID, "step_finished", step); err != nil {
				return err
			}
		}
	}
	for _, message := range r.messages {
		if message.RunID == r.id && message.Status == "processing" {
			if err := r.tx.SetMessageStatus(message.ID, "interrupted"); err != nil {
				return err
			}
		}
	}
	r.run.Status = "interrupted"
	r.run.Error = reason
	r.run.FinishedAt = w.Now()
	if err := r.tx.SaveRun(*r.run); err != nil {
		return err
	}
	return r.tx.AppendEvent(r.id, "", "", "run_finished", *r.run)
}

// settle finishes a run whose steps already reached a real failure or
// cancellation. It reports whether unfinished steps remain for requeue.
func (r *recoveryTransaction) settle() (bool, bool, error) {
	failed := ""
	canceled := ""
	unfinished := false
	for _, step := range r.steps {
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
	if unfinished || (failed == "" && canceled == "") {
		return unfinished, false, nil
	}
	status, cause := "failed", failed
	if failed == "" {
		status, cause = "canceled", canceled
	}
	if err := settlePending(r.tx, r.id, "run ended during restart recovery"); err != nil {
		return false, false, err
	}
	steps, err := r.tx.Steps(r.id)
	if err != nil {
		return false, false, err
	}
	r.steps = steps
	for _, step := range r.steps {
		if step.Status != "running" && step.Status != "interrupted" {
			continue
		}
		step.Status = "interrupted"
		step.Error = "run ended during restart recovery"
		if status == "canceled" {
			step.Status = "canceled"
		}
		if err := r.tx.SaveStep(step); err != nil {
			return false, false, err
		}
		if err := r.tx.AppendEvent(r.id, step.ID, step.AgentID, "step_finished", step); err != nil {
			return false, false, err
		}
	}
	for _, message := range r.messages {
		if message.RunID == r.id && message.Status == "processing" {
			target := "interrupted"
			if status == "canceled" {
				target = "canceled"
			}
			if err := r.tx.SetMessageStatus(message.ID, target); err != nil {
				return false, false, err
			}
		}
	}
	r.run.Status = status
	r.run.Error = cause
	r.run.FinishedAt = w.Now()
	if err := r.tx.SaveRun(*r.run); err != nil {
		return false, false, err
	}
	return false, true, r.tx.AppendEvent(r.id, "", "", "run_finished", *r.run)
}

func (r *recoveryTransaction) succeed() error {
	// A recovered legacy run recorded no delivery, so it keeps nil. A run under
	// the delivery contract must prove every final target before it may settle
	// succeeded; otherwise it fails here instead of remaining active with
	// incomplete metadata.
	if r.run.Delivery != nil {
		delivery, err := buildDelivery(r.tx, *r.run)
		if err != nil {
			r.run.Status = "failed"
			r.run.FailureKind = failureArtifactContract
			r.run.Error = "final delivery is incomplete: " + err.Error()
			r.run.FinishedAt = w.Now()
			if err := r.tx.SaveRun(*r.run); err != nil {
				return err
			}
			return r.tx.AppendEvent(r.id, "", "", "run_finished", *r.run)
		}
		r.run.Delivery = delivery
		r.run.Method = settledMethod(*r.run, r.steps)
	}
	r.run.Status = "succeeded"
	r.run.Error = ""
	r.run.FinishedAt = w.Now()
	if err := r.tx.SaveRun(*r.run); err != nil {
		return err
	}
	if r.run.Delivery != nil && r.run.SelectedWorkflow == nil && (r.run.Input.Workflow == "" || r.run.Input.Workflow == "free") {
		if err := r.tx.AppendEvent(r.id, "", "", "method_recorded", map[string]any{"method": r.run.Method}); err != nil {
			return err
		}
	}
	return r.tx.AppendEvent(r.id, "", "", "run_finished", *r.run)
}

func (r *recoveryTransaction) requeue() error {
	for i := range r.steps {
		if r.steps[i].Status == "running" || r.steps[i].Status == "interrupted" || r.steps[i].Status == "canceled" {
			r.steps[i].Status = "pending"
			r.steps[i].Error = ""
			if err := r.tx.SaveStep(r.steps[i]); err != nil {
				return err
			}
		}
	}
	for _, message := range r.messages {
		if message.RunID == r.id && message.Status == "processing" {
			if err := r.tx.SetMessageStatus(message.ID, "interrupted"); err != nil {
				return err
			}
			preview, err := r.tx.MessagePreview(message.ID, 6000)
			if err != nil {
				return err
			}
			if err := r.tx.AppendEvent(r.id, message.StepID, message.AgentID, "message_finished", preview); err != nil {
				return err
			}
		}
	}
	r.run.Status = "queued"
	r.run.Error = ""
	r.run.FinishedAt = ""
	r.run.RecoveryCount++
	r.run.RecoveredAt = w.Now()
	r.run.RecoveryNote = "service_restart"
	if len(r.changes) > 0 {
		r.run.RecoveryNote = "service_restart;cli_changed:" + strings.Join(r.changes, ",")
	}
	if err := r.tx.SaveRun(*r.run); err != nil {
		return err
	}
	return r.tx.AppendEvent(r.id, "", "", "run_recovered", map[string]any{"reason": "service_restart", "changed_clis": r.changes, "attempts": len(r.verdicts)})
}
