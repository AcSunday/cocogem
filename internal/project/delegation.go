package project

import (
	"context"
	"errors"
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// DelegateTaskInput is host-bound: the model chooses only the recipient, goal,
// references and request ID. Parent identity, exposure and budget come from the
// current authorized attempt.
type DelegateTaskInput struct {
	ParentTaskID    string   `json:"parent_task_id"`
	ParentAttemptID string   `json:"parent_attempt_id"`
	OwnerMemberID   string   `json:"owner_member_id"`
	Goal            string   `json:"goal"`
	RequestID       string   `json:"request_id"`
	ContextRefs     []string `json:"context_refs,omitempty"`
}

type DelegationView struct {
	Task TaskView `json:"task"`
	Run  w.Run    `json:"run"`
}

// DelegateTask creates an attempt-bound child. Browser callers use
// DelegateTaskFromUser instead, so a request cannot invent producer exposure.
func (s *Service) DelegateTask(ctx context.Context, in DelegateTaskInput) (DelegationView, error) {
	if in.ParentAttemptID == "" {
		return DelegationView{}, errorf("invalid_request", "member delegation requires a parent attempt")
	}
	return s.delegateTask(ctx, in, false)
}

// DelegateTaskFromUser is a host-submitted delegation with no producer Attempt.
// Its accepted actions come from the current user-accepted TaskRevision, never
// from an Attempt or GrantActions supplied by the browser.
func (s *Service) DelegateTaskFromUser(ctx context.Context, in DelegateTaskInput) (DelegationView, error) {
	if in.ParentAttemptID != "" {
		return DelegationView{}, errorf("invalid_request", "user delegation cannot bind a producer attempt")
	}
	return s.delegateTask(ctx, in, true)
}

// delegateTask creates the child, its first budgeted run and return dependency
// atomically. A deterministic internal thread makes retries durable across
// process restarts without allowing the caller to choose object identities.
func (s *Service) delegateTask(ctx context.Context, in DelegateTaskInput, userSubmitted bool) (DelegationView, error) {
	var out DelegationView
	if in.ParentTaskID == "" || in.OwnerMemberID == "" || in.RequestID == "" || in.Goal == "" {
		return out, errorf("invalid_request", "delegation requires parent task, recipient, goal and request ID")
	}
	threadID := "delegation-" + Digest(struct{ Parent, Request string }{in.ParentTaskID, in.RequestID})
	requestDigest := Digest(in)
	if userSubmitted {
		requestDigest = Digest(struct {
			Input DelegateTaskInput
			Actor string
		}{in, "user"})
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		if existing, err := tx.Thread(threadID); err == nil {
			run, digest, err := tx.Idempotent(existing.ID, "delegation:"+in.RequestID)
			if err != nil {
				return err
			}
			if digest != requestDigest {
				return errorf("conflict", "delegation request ID was reused for different content")
			}
			out.Task, err = s.taskInTx(tx, existing.TaskID)
			out.Run = run
			return err
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		parent, err := tx.Task(in.ParentTaskID)
		if err != nil {
			return err
		}
		if parent.TransferRequestID != "" || parent.Status == "paused" || parent.Status == "canceled" || parent.Status == "completed" {
			return errorf("authority_revoked", "settled or revoked parent cannot delegate new work")
		}
		if _, _, err := derivedMessageDisclosure(tx, parent, SendTaskMessageInput{
			SenderTaskID: parent.ID, SenderAttemptID: in.ParentAttemptID, ContextRefs: in.ContextRefs,
		}, in.ContextRefs); err != nil {
			return err
		}
		member, err := tx.WorkspaceAgent(parent.WorkspaceID, in.OwnerMemberID)
		if err != nil || member.Disabled {
			return errorf("recipient_unavailable", "delegation recipient is not enabled in this workspace")
		}
		revision, err := tx.TaskRevision(parent.ID, parent.CurrentRevision)
		if err != nil {
			return err
		}
		actions := revision.AllowedActions
		acceptedBy := "user"
		if !userSubmitted {
			attempt, err := tx.Attempt(in.ParentAttemptID)
			if err != nil {
				return err
			}
			parentRun, err := tx.Run(attempt.RunID)
			if err != nil {
				return err
			}
			actions = intersection(revision.AllowedActions, parentRun.GrantActions)
			acceptedBy = "task:" + parent.ID
		}
		if len(actions) == 0 {
			return errorf("authority_revoked", "delegation has no remaining authorized actions")
		}
		now := s.now()
		thread := w.Thread{
			ID: threadID, WorkspaceID: parent.WorkspaceID, Title: "委派任务", Internal: true,
			AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Revision: 1, UpdatedAt: now,
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		child, err := s.CreateTaskInTx(tx, CreateTaskInput{
			WorkspaceID: parent.WorkspaceID, ParentID: parent.ID, ThreadID: thread.ID,
			OwnerMemberID: member.ID, Goal: in.Goal, InputRefs: in.ContextRefs,
			AllowedActions: actions, SourceMessageID: "delegation:" + in.RequestID,
			AcceptedBy: acceptedBy, Kind: "delegation",
		})
		if err != nil {
			return err
		}
		workspace, err := tx.Workspace(parent.WorkspaceID)
		if err != nil {
			return err
		}
		run := w.Run{
			ID: w.NewID(), WorkspaceID: parent.WorkspaceID, Workspace: workspace,
			ThreadID: thread.ID, TaskID: child.Task.ID, TaskRevision: 1,
			Status: "queued", CreatedAt: now, Agents: []w.Agent{member}, CoordinatorID: member.ID,
			Input:                 w.RunInput{Message: in.Goal, Workflow: "free", ResearchScope: "given_sources"},
			Delivery:              &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}},
			AutomaticContinuation: true,
		}
		if err := s.EnsureRunAuthorization(tx, &run, "delegation:"+in.RequestID); err != nil {
			return err
		}
		manifest, err := s.PrepareContextInTx(tx, child.Task.ID, 1, ContextHostInput(run))
		if err != nil {
			return err
		}
		run.ContextRef = manifest.ID
		run.InputDigest = ContextInputDigest(child, manifest, in.RequestID, run.Input)
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "delegation:"+in.RequestID, requestDigest); err != nil {
			return err
		}
		if err := tx.SaveDependency(w.TaskDependency{
			ID: w.NewID(), ProducerTaskID: child.Task.ID, ConsumerTaskID: parent.ID,
			Mode: "latest", Valid: true, ConsumedTaskRevision: parent.CurrentRevision,
			ConsumedDigest: revision.Digest, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{
			ID: w.NewID(), RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName,
			Role: "member", Instruction: child.Revision.Goal, Status: "pending",
		}); err != nil {
			return err
		}
		account, err := tx.BudgetAccount(child.Task.BudgetAccountID)
		if err != nil {
			return err
		}
		child.Budget = account
		out = DelegationView{Task: child, Run: run}
		return nil
	})
	return out, err
}

func intersection(a, b []string) []string {
	out := []string{}
	for _, value := range a {
		if slices.Contains(b, value) && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return out
}
