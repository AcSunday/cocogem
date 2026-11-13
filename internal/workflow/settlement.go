package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (e *Engine) finish(parent context.Context, run w.Run, status string, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if w.Terminal(current.Status) {
			return nil
		}
		if current.Status == "canceling" {
			// User cancellation is committed before any later failure can settle.
			status, cause = "canceled", nil
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		hasFailed := false
		for _, step := range steps {
			if step.Status == "failed" {
				hasFailed = true
			}
		}
		if status == "interrupted" && hasFailed {
			status = "failed"
			cause = nil
		}
		if status == "succeeded" {
			cause = nil
		}
		if cause == nil && (status == "failed" || status == "interrupted") {
			_, aggregated, _ := aggregateSteps(steps, false)
			cause = errorFrom(aggregated)
		}
		if status != "interrupted" {
			if err := settlePending(tx, run.ID, "run ended before step started"); err != nil {
				return err
			}
		}
		for _, step := range steps {
			if step.Status != "running" {
				continue
			}
			switch status {
			case "interrupted":
				step.Status = "interrupted"
			case "canceled":
				step.Status = "canceled"
			default:
				step.Status = "failed"
			}
			step.Error = "step ended without a committed completion"
			if status == "succeeded" {
				step.Status = "failed"
				step.Error = "step ended without a committed completion"
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, step.AgentID, "step_finished", step); err != nil {
				return err
			}
		}
		attempts, err := tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attempt.Status != "running" {
				continue
			}
			switch status {
			case "interrupted":
				attempt.Status = "interrupted"
			case "canceled":
				attempt.Status = "canceled"
			default:
				attempt.Status = "failed"
			}
			attempt.Error = "attempt ended without a committed completion"
			attempt.FinishedAt = w.Now()
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, attempt.StepID, "", "attempt_finished", attempt); err != nil {
				return err
			}
		}
		from := map[string]bool{"processing": true}
		if status == "interrupted" {
			from["canceled"] = true
		}
		if err := transitionMessages(tx, run.ID, run.ThreadID, from, status, "message_finished"); err != nil {
			return err
		}
		kind := current.FailureKind
		if run.FailureKind != "" {
			kind = run.FailureKind
		}
		switch status {
		case "canceled", "succeeded":
			kind = ""
		case "failed":
			if cause != nil && strings.HasPrefix(cause.Error(), failureRunTimeout+":") {
				kind = failureRunTimeout
			} else if cause != nil && strings.HasPrefix(cause.Error(), failureStepTimeout+":") {
				kind = failureStepTimeout
			}
			if kind == "" || kind == failureCheckpointInterrupt {
				_, _, aggregatedKind := aggregateSteps(steps, false)
				kind = aggregatedKind
			}
			if kind == "" {
				kind = failureUnknown
			}
		case "interrupted":
			if kind == "" {
				_, _, aggregatedKind := aggregateSteps(steps, false)
				kind = aggregatedKind
			}
		}
		current.Status = status
		current.FailureKind = kind
		current.FinishedAt = w.Now()
		if cause != nil && (status == "failed" || status == "interrupted") {
			current.Error = cause.Error()
		} else {
			current.Error = ""
		}
		if status == "succeeded" {
			// Delivery and the terminal run state commit together; a run can
			// never be observed succeeded without its authoritative references.
			// Incomplete final results fail the run in this same transaction
			// instead of returning an error that leaves it running forever.
			delivery, deliveryErr := buildDelivery(tx, current)
			if deliveryErr != nil {
				status = "failed"
				current.Status = "failed"
				current.FailureKind = failureArtifactContract
				current.Error = "final delivery is incomplete: " + deliveryErr.Error()
				if current.Delivery == nil {
					current.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
				}
			} else {
				current.Delivery = delivery
				current.Method = settledMethod(current, steps)
			}
		}
		if current.CollaborationMessageID != "" {
			receipts, err := tx.Receipts(current.CollaborationMessageID)
			if err != nil {
				return err
			}
			for _, receipt := range receipts {
				if receipt.Status == "handled" || receipt.Status == "rejected" {
					continue
				}
				receipt.Status = "rejected"
				receipt.ResponseRef = current.ID
				receipt.Detail = "linked_response"
				if status == "succeeded" {
					if current.Delivery != nil && len(current.Delivery.MessageIDs) > 0 {
						receipt.ResponseRef = current.Delivery.MessageIDs[0]
					} else if current.Delivery != nil && len(current.Delivery.ArtifactRefs) > 0 {
						ref := current.Delivery.ArtifactRefs[0]
						receipt.ResponseRef = fmt.Sprintf("artifact:%s@%d", ref.ID, ref.Version)
					}
					if err := project.New(e.Store).ValidateConsultationResponse(tx, current, receipt.ResponseRef); err != nil {
						status = "failed"
						current.Status = "failed"
						current.FailureKind = failureArtifactContract
						current.Error = err.Error()
						current.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
						receipt.Detail = "disclosure_denied"
					} else {
						receipt.Status = "handled"
					}
				}
				receipt.Attempt++
				receipt.UpdatedAt = w.Now()
				if err := tx.SaveReceipt(receipt); err != nil {
					return err
				}
			}
		}
		if err := project.New(e.Store).RecordRunProgress(tx, &current, status); err != nil {
			return err
		}
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		if status == "succeeded" && current.SelectedWorkflow == nil && (current.Input.Workflow == "" || current.Input.Workflow == "free") {
			if err := tx.AppendEvent(run.ID, "", "", "method_recorded", map[string]any{"method": current.Method}); err != nil {
				return err
			}
		}
		return tx.AppendEvent(run.ID, "", "", "run_finished", current)
	})
	if err != nil {
		e.report(err)
	}
	if err == nil {
		slog.Info("run settled", "run_id", run.ID, "status", status)
		if run.TaskID != "" {
			if _, wakeErr := project.New(e.Store).WakeTask(ctx, "delegation", run.TaskID, "run:"+run.ID+":"+status); wakeErr != nil {
				e.report(wakeErr)
			}
		}
	}
}

// transitionMessages changes only lifecycle fields, so a failure cleanup can
// never replace a chunked preview with an empty full-body save.
func transitionMessages(tx *store.Tx, runID, threadID string, from map[string]bool, to, kind string) error {
	messages, err := tx.MessagesMetadata(threadID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.RunID != runID || !from[message.Status] {
			continue
		}
		if err := tx.SetMessageStatus(message.ID, to); err != nil {
			return err
		}
		preview, err := tx.MessagePreview(message.ID, 6000)
		if err != nil {
			return err
		}
		if err := tx.AppendEvent(runID, message.StepID, message.AgentID, kind, preview); err != nil {
			return err
		}
	}
	return nil
}

// buildDelivery reconstructs the terminal delivery from the persisted plan and
// message lifecycle, never from timestamps or model self-descriptions. It is
// called only inside the transaction that commits a successful run. Every
// designated final target must be complete and contribute at least one actual
// committed reference; an incomplete delivery is an error, never a nil success.
func buildDelivery(tx *store.Tx, run w.Run) (*w.Delivery, error) {
	steps, err := tx.Steps(run.ID)
	if err != nil {
		return nil, err
	}
	finals := finalSteps(effectiveInput(run), steps)
	if len(finals) == 0 {
		return nil, errors.New("no final delivery target")
	}
	messages, err := tx.MessagesMetadata(run.ThreadID)
	if err != nil {
		return nil, err
	}
	artifacts, err := tx.ArtifactsMetadata(run.ThreadID)
	if err != nil {
		return nil, err
	}
	delivery := &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
	for _, step := range finals {
		if step.Status != "succeeded" {
			return nil, fmt.Errorf("final target %s (%s) is %s", step.AgentName, step.Role, step.Status)
		}
		committed := 0
		for _, message := range messages {
			// A retry leaves earlier failed attempts in history; only a completed
			// message from the designated final step is a delivery reference.
			if message.RunID != run.ID || message.StepID != step.ID || message.Status != "completed" {
				continue
			}
			visible, err := messageHasVisibleText(tx, message)
			if err != nil {
				return nil, err
			}
			// A runner that finishes without emitting text leaves an empty
			// completed message; it is not a final result. A final artifact can
			// still carry the delivery on its own.
			if !visible {
				continue
			}
			delivery.MessageIDs = append(delivery.MessageIDs, message.ID)
			committed++
		}
		for _, artifact := range artifacts {
			if artifact.RunID == run.ID && artifact.StepID == step.ID {
				delivery.ArtifactRefs = append(delivery.ArtifactRefs, w.ArtifactRef{ID: artifact.ID, Version: artifact.Version})
				committed++
			}
		}
		if committed == 0 {
			return nil, fmt.Errorf("final target %s (%s) has no committed message or artifact", step.AgentName, step.Role)
		}
	}
	return delivery, nil
}

// messageHasVisibleText reports whether a completed message carries at least
// one non-whitespace byte. The metadata projection never includes the body, so
// a bounded preview decides the common case and paging handles a body whose
// visible text starts after the preview.
func messageHasVisibleText(tx *store.Tx, message w.Message) (bool, error) {
	preview, err := tx.MessagePreview(message.ID, 6000)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(preview.Content) != "" {
		return true, nil
	}
	if preview.ContentBytes <= len(preview.Content) {
		return false, nil
	}
	// shortcut: only the first 2 MiB of a body is inspected, so an all-blank
	// prefix longer than that is treated as empty; raise the budget if model
	// output of that shape becomes real.
	const scanBudget = 2 << 20
	scanned, cursor := len(preview.Content), ""
	for scanned < scanBudget {
		page, err := tx.MessageContent(message.ID, cursor, store.MaxContentChunks)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(page.Content) != "" {
			return true, nil
		}
		if !page.HasMore || page.Content == "" {
			return false, nil
		}
		scanned += len(page.Content)
		cursor = page.NextCursor
	}
	return false, nil
}

func (e *Engine) saveStep(ctx context.Context, s w.Step, kind string) error {
	return e.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveStep(s); err != nil {
			return err
		}
		return tx.AppendEvent(s.RunID, s.ID, s.AgentID, kind, s)
	})
}

func settlePending(tx *store.Tx, runID, reason string) error {
	steps, err := tx.Steps(runID)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.Status == "pending" {
			step.Status = "skipped"
			step.Error = reason
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(runID, step.ID, step.AgentID, "step_finished", step); err != nil {
				return err
			}
		}
	}
	return nil
}
