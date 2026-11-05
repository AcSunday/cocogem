package project

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type MessageView struct {
	Message  w.CollaborationMessage `json:"message"`
	Receipts []w.DeliveryReceipt    `json:"receipts"`
}

type SendTaskMessageInput struct {
	SenderTaskID        string            `json:"sender_task_id"`
	SenderAttemptID     string            `json:"sender_attempt_id,omitempty"`
	TargetTaskID        string            `json:"target_task_id"`
	Kind                string            `json:"kind"`
	Body                string            `json:"body"`
	RequestID           string            `json:"request_id"`
	ReplyTo             string            `json:"reply_to,omitempty"`
	CorrelationID       string            `json:"correlation_id,omitempty"`
	ContextRefs         []string          `json:"context_refs"`
	Summary             *w.HandoffSummary `json:"summary,omitempty"`
	RelatedTaskRevision int               `json:"related_task_revision,omitempty"`
	ExpiresAt           string            `json:"expires_at,omitempty"`
	RequiresResponse    bool              `json:"requires_response,omitempty"`
	requestDigest       string
}

func messageDisclosure(tx *store.Tx, sender, target w.Task, in SendTaskMessageInput) (string, string, error) {
	refs := slices.Clone(in.ContextRefs)
	if in.Summary != nil {
		if err := w.ValidateHandoffSummary(*in.Summary); err != nil {
			return "", "", err
		}
		refs = append(refs, in.Summary.SupportingRefs...)
		refs = append(refs, in.Summary.ContradictingRefs...)
		refs = append(refs, in.Summary.SourceVersions...)
	}
	for _, ref := range refs {
		if strings.Contains(ref, "@") {
			return "", "", errorf("invalid_request", "source versions must name exact captured source IDs")
		}
	}
	visibility, exposure, err := derivedMessageDisclosure(tx, sender, in, refs)
	if err != nil {
		return "", "", err
	}
	if visibility == "private" && sender.ID != target.ID {
		return "", "", errorf("disclosure_denied", "private derived content cannot cross tasks")
	}
	if visibility == "task_chain" && sender.CollaborationScopeID != target.CollaborationScopeID {
		return "", "", errorf("disclosure_denied", "task-chain content cannot cross collaboration scopes")
	}
	if visibility == "role" && sender.OwnerMemberID != target.OwnerMemberID {
		return "", "", errorf("disclosure_denied", "role-scoped content cannot cross members")
	}
	return visibility, exposure, nil
}

func (s *Service) sendMessageInTx(tx *store.Tx, in SendTaskMessageInput) (MessageView, error) {
	var view MessageView
	if in.SenderTaskID == "" || in.TargetTaskID == "" || in.RequestID == "" || strings.TrimSpace(in.Body) == "" {
		return view, errorf("invalid_request", "A2A requires sender, target, request ID and body")
	}
	if in.Kind == "" {
		in.Kind = "question"
	}
	if !slices.Contains([]string{"question", "evidence", "change_notice", "result", "consultation"}, in.Kind) {
		return view, errorf("invalid_request", "unknown A2A message kind")
	}
	sender, err := tx.Task(in.SenderTaskID)
	if err != nil {
		return view, err
	}
	target, err := tx.Task(in.TargetTaskID)
	if err != nil {
		return view, err
	}
	if sender.WorkspaceID != target.WorkspaceID {
		return view, errorf("disclosure_denied", "cross-workspace A2A requires an explicit import grant")
	}
	if sender.Status == "paused" || sender.Status == "canceled" || target.Status == "paused" || target.Status == "canceled" {
		return view, errorf("authority_revoked", "paused or canceled tasks do not admit A2A work")
	}
	requestDigest := in.requestDigest
	if requestDigest == "" {
		requestDigest = Digest(in)
	}
	if previous, err := tx.CollaborationMessageByRequest(sender.ID, in.RequestID); err == nil {
		if previous.RequestDigest != requestDigest {
			return view, errorf("conflict", "the same A2A request ID was reused for different content")
		}
		receipts, err := tx.Receipts(previous.ID)
		return MessageView{Message: previous, Receipts: receipts}, err
	} else if !errors.Is(err, store.ErrNotFound) {
		return view, err
	}
	pending, err := tx.PendingReceiptCount(target.ID)
	if err != nil {
		return view, err
	}
	if pending >= 64 {
		return view, errorf("queue_full", "target task has too many unhandled messages")
	}
	visibility, exposure, err := messageDisclosure(tx, sender, target, in)
	if err != nil {
		return view, err
	}
	correlation := in.CorrelationID
	if correlation == "" {
		correlation = in.RequestID
	}
	now := s.now()
	message := w.CollaborationMessage{
		ID: w.NewID(), SchemaVersion: 1, Kind: in.Kind, Body: in.Body, BodyDigest: Digest(in.Body),
		SenderTaskID: sender.ID, SenderTaskRevision: sender.CurrentRevision, SenderAttemptID: in.SenderAttemptID,
		SenderMemberID: sender.OwnerMemberID, TargetTaskID: target.ID,
		ResolvedTargetMemberID: target.OwnerMemberID, TargetOwnerRevision: target.OwnerRevision,
		RelatedTaskRevision: in.RelatedTaskRevision, ReplyTo: in.ReplyTo, CorrelationID: correlation,
		RequestID: in.RequestID, RequestDigest: requestDigest, ContextRefs: nonNil(in.ContextRefs), DisclosureScope: visibility,
		ExposureScopeDigest: exposure, BudgetAccountID: sender.BudgetAccountID,
		RootControlEpoch: sender.RootControlEpoch, CreatedAt: now, ExpiresAt: in.ExpiresAt,
	}
	if err := tx.SaveCollaborationMessage(message); err != nil {
		return view, err
	}
	receipt := w.DeliveryReceipt{MessageID: message.ID, TargetTaskID: target.ID, Status: "queued", Attempt: 1, UpdatedAt: now}
	if err := tx.SaveReceipt(receipt); err != nil {
		return view, err
	}
	return MessageView{Message: message, Receipts: []w.DeliveryReceipt{receipt}}, nil
}

func (s *Service) SendTaskMessage(ctx context.Context, in SendTaskMessageInput) (MessageView, error) {
	if in.Kind == "" {
		in.Kind = "question"
	}
	needsResponse := in.Kind == "question" || in.RequiresResponse
	if needsResponse {
		consult := false
		if err := s.store.Read(ctx, func(tx *store.Tx) error {
			sender, err := tx.Task(in.SenderTaskID)
			if err != nil {
				return err
			}
			target, err := tx.Task(in.TargetTaskID)
			if err != nil {
				return err
			}
			if _, _, err := messageDisclosure(tx, sender, target, in); err != nil {
				return err
			}
			// A live task chain can consume a question at its next safe boundary.
			// Independent roots and already settled targets need a separate,
			// requester-funded consultation rather than reopening the original.
			consult = sender.BudgetAccountID != target.BudgetAccountID || target.Status == "completed" || target.Status == "paused" || target.Status == "canceled"
			return nil
		}); err != nil {
			return MessageView{}, err
		}
		if consult {
			_, view, err := s.Consult(ctx, in)
			return view, err
		}
	}
	var view MessageView
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		view, err = s.sendMessageInTx(tx, in)
		return err
	})
	return view, err
}

func (s *Service) Consult(ctx context.Context, in SendTaskMessageInput) (TaskView, MessageView, error) {
	var taskView TaskView
	var messageView MessageView
	var outcomeErr error
	if in.Kind == "" {
		in.Kind = "question"
	}
	requestDigest := Digest(in)
	in.requestDigest = requestDigest
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		sender, err := tx.Task(in.SenderTaskID)
		if err != nil {
			return err
		}
		if previous, previousErr := tx.CollaborationMessageByRequest(sender.ID, in.RequestID); previousErr == nil {
			if previous.RequestDigest != requestDigest {
				return errorf("conflict", "the same A2A request ID was reused for different content")
			}
			taskView, err = s.taskInTx(tx, previous.TargetTaskID)
			if err != nil {
				return err
			}
			messageView.Message = previous
			messageView.Receipts, err = tx.Receipts(previous.ID)
			return err
		} else if !errors.Is(previousErr, store.ErrNotFound) {
			return previousErr
		}
		sender, err = tx.Task(in.SenderTaskID)
		if err != nil {
			return err
		}
		original, err := tx.Task(in.TargetTaskID)
		if err != nil {
			return err
		}
		if sender.WorkspaceID != original.WorkspaceID {
			return errorf("disclosure_denied", "cross-workspace consultation is unavailable")
		}
		if _, _, err := messageDisclosure(tx, sender, original, in); err != nil {
			return err
		}
		now := s.now()
		thread := w.Thread{
			ID: w.NewID(), WorkspaceID: sender.WorkspaceID, Title: "任务答疑",
			AgentIDs: []string{original.OwnerMemberID}, CoordinatorID: original.OwnerMemberID,
			Internal: true, Revision: 1, UpdatedAt: now,
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		taskView, err = s.CreateTaskInTx(tx, CreateTaskInput{
			WorkspaceID: sender.WorkspaceID, ParentID: sender.ID, ThreadID: thread.ID,
			OwnerMemberID: original.OwnerMemberID, Goal: "答疑：" + in.Body,
			InputRefs:      []string{fmt.Sprintf("work:%s@%d", original.ID, original.CurrentRevision)},
			AllowedActions: []string{"read", "collaborate", "publish_artifact"}, SourceMessageID: in.RequestID,
			AcceptedBy: "task:" + sender.ID, Kind: "consultation",
		})
		if err != nil {
			return err
		}
		member, err := tx.WorkspaceAgent(sender.WorkspaceID, original.OwnerMemberID)
		if err != nil || member.Disabled {
			return errorf("recipient_unavailable", "original task owner is not enabled for consultation")
		}
		run := w.Run{
			ID: w.NewID(), WorkspaceID: sender.WorkspaceID, Workspace: w.Workspace{ID: sender.WorkspaceID},
			ThreadID: thread.ID, TaskID: taskView.Task.ID, TaskRevision: 1,
			Status: "queued", CreatedAt: now, Agents: []w.Agent{member}, CoordinatorID: member.ID,
			Input:                 w.RunInput{Message: in.Body, Workflow: "free", ResearchScope: "given_sources"},
			Delivery:              &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}},
			AutomaticContinuation: true,
		}
		workspace, err := tx.Workspace(sender.WorkspaceID)
		if err != nil {
			return err
		}
		run.Workspace = workspace
		if err := s.EnsureRunAuthorization(tx, &run, in.RequestID); err != nil {
			return err
		}
		manifest, err := s.PrepareContextInTx(tx, run.TaskID, run.TaskRevision, ContextHostInput(run))
		if err != nil {
			return err
		}
		run.ContextRef = manifest.ID
		run.InputDigest = ContextInputDigest(taskView, manifest, in.RequestID, run.Input)
		if err := tx.ReserveTaskRun(&run); errors.Is(err, w.ErrBudgetExhausted) {
			taskView.Task.Status = "waiting"
			taskView.Task.WaitingReason = "budget"
			taskView.Task.UpdatedAt = s.now()
			if err := tx.SaveTask(taskView.Task, taskView.Task.CurrentRevision); err != nil {
				return err
			}
			outcomeErr = errorf("budget_exhausted", "consultation is durably queued until its shared budget is expanded")
		} else if err != nil {
			return err
		}
		in.Kind = "consultation"
		in.TargetTaskID = taskView.Task.ID
		in.RelatedTaskRevision = original.CurrentRevision
		messageView, err = s.sendMessageInTx(tx, in)
		if err != nil {
			return err
		}
		run.CollaborationMessageID = messageView.Message.ID
		if err := tx.InsertRun(run, "consultation:"+in.SenderTaskID+":"+in.RequestID, Digest(in)); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{
			ID: w.NewID(), RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName,
			Role: "responder", Instruction: "answer the exact correlated question from durable work and cite evidence or gaps",
			Status: "pending",
		}); err != nil {
			return err
		}
		taskView.Budget, err = tx.BudgetAccount(taskView.Task.BudgetAccountID)
		return err
	})
	return taskView, messageView, errors.Join(err, outcomeErr)
}

func (s *Service) RecordReceipt(ctx context.Context, messageID, status, proof, responseRef string) (w.DeliveryReceipt, error) {
	var receipt w.DeliveryReceipt
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		receipts, err := tx.Receipts(messageID)
		if err != nil {
			return err
		}
		if len(receipts) != 1 {
			return errorf("not_found", "delivery receipt was not found")
		}
		receipt = receipts[0]
		if receipt.Status == "handled" || receipt.Status == "rejected" || receipt.Status == "canceled" {
			return nil
		}
		if status == "handled" && responseRef == "" {
			return errorf("invalid_request", "handled requires an exact correlated response")
		}
		if status == "presented" && proof != "runner_ack" && proof != "linked_response" {
			receipt.Status = "presentation_unknown"
			receipt.Detail = "runner did not provide verifiable presentation evidence"
			receipt.UpdatedAt = s.now()
			return tx.SaveReceipt(receipt)
		}
		if !slices.Contains([]string{"dispatching", "presented", "handled", "rejected", "expired", "canceled", "presentation_unknown"}, status) {
			return errorf("invalid_request", "unknown delivery receipt status")
		}
		receipt.Status = status
		receipt.ResponseRef = responseRef
		receipt.Detail = proof
		receipt.Attempt++
		receipt.UpdatedAt = s.now()
		return tx.SaveReceipt(receipt)
	})
	return receipt, err
}

func (s *Service) CollaborationMessages(ctx context.Context, taskID string) ([]w.CollaborationMessage, error) {
	var messages []w.CollaborationMessage
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		messages, err = tx.CollaborationMessages(taskID)
		return err
	})
	return messages, err
}

func (s *Service) DeliveryReceipts(ctx context.Context, messageID string) ([]w.DeliveryReceipt, error) {
	var receipts []w.DeliveryReceipt
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		receipts, err = tx.Receipts(messageID)
		return err
	})
	return receipts, err
}
