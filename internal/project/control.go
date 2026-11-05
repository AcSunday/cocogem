package project

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// BeginTaskTransfer freezes new admission without changing ownership. Process
// cancellation and descendant verification must happen outside this transaction.
func (s *Service) BeginTaskTransfer(ctx context.Context, taskID, newOwnerID, requestID string, expectedOwnerRevision int) (w.Task, error) {
	var result w.Task
	if newOwnerID == "" || requestID == "" {
		return result, errorf("invalid_request", "owner transfer requires a target member and request ID")
	}
	requestDigest := Digest(struct {
		TaskID, NewOwnerID string
		Expected           int
	}{taskID, newOwnerID, expectedOwnerRevision})
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		if previous, err := tx.TaskAction(taskID, requestID); err == nil {
			if previous.RequestDigest != requestDigest || !strings.HasPrefix(previous.Action, "transfer") {
				return errorf("conflict", "transfer request ID was reused for different input")
			}
			result = previous.Task
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		if task.OwnerRevision != expectedOwnerRevision || task.OwnerMemberID == newOwnerID || task.TransferRequestID != "" {
			return errorf("revision_conflict", "owner revision changed or another transfer is pending")
		}
		member, err := tx.WorkspaceAgent(task.WorkspaceID, newOwnerID)
		if err != nil || member.Disabled {
			return errorf("recipient_unavailable", "new owner is not enabled in this workspace")
		}
		task.PendingOwnerID = newOwnerID
		task.TransferRequestID = requestID
		task.RootControlEpoch++
		task.UpdatedAt = s.now()
		if err := tx.SaveTask(task, task.CurrentRevision); err != nil {
			return err
		}
		receipt := w.TaskActionReceipt{
			TaskID: taskID, RequestID: requestID, Action: "transfer_pending", RequestDigest: requestDigest,
			Task: task, PendingOwnerID: newOwnerID, OwnerRevision: task.OwnerRevision,
			TaskRevision: task.CurrentRevision, RootControlEpoch: task.RootControlEpoch,
			ExecutionRef: task.CurrentRunID, CreatedAt: s.now(),
		}
		if err := tx.InsertTaskAction(receipt); err != nil {
			return err
		}
		result = task
		return nil
	})
	return result, err
}

// CompleteTaskTransfer applies the owner CAS only after the host verifies stop.
// An uncertain outcome keeps the old owner and the admission freeze in place.
func (s *Service) CompleteTaskTransfer(ctx context.Context, taskID, requestID string, stopUncertain bool) (w.Task, error) {
	var result w.Task
	var outcomeErr error
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		receipt, err := tx.TaskAction(taskID, requestID)
		if err != nil || !strings.HasPrefix(receipt.Action, "transfer") {
			return errorf("not_found", "transfer request was not registered")
		}
		if receipt.Action == "transfer" {
			result = receipt.Task
			return nil
		}
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		uncertain := stopUncertain || task.TransferRequestID != requestID || task.PendingOwnerID != receipt.PendingOwnerID ||
			task.OwnerRevision != receipt.OwnerRevision || task.CurrentRevision != receipt.TaskRevision ||
			task.RootControlEpoch != receipt.RootControlEpoch || task.CurrentRunID != ""
		if !uncertain {
			runs, err := tx.WorkspaceRuns(task.WorkspaceID)
			if err != nil {
				return err
			}
			for _, run := range runs {
				if run.TaskID == taskID && !w.Terminal(run.Status) {
					uncertain = true
				}
			}
			executions, err := tx.Executions(task.WorkspaceID)
			if err != nil {
				return err
			}
			for _, execution := range executions {
				if execution.TaskID == taskID && slices.Contains([]string{"active", "quarantined", "uncertain"}, execution.Status) {
					uncertain = true
				}
			}
		}
		if uncertain {
			receipt.Action = "transfer_uncertain"
			receipt.Task = task
			receipt.UpdatedAt = s.now()
			if err := tx.SaveTaskTransferReceipt(receipt); err != nil {
				return err
			}
			result = task
			outcomeErr = errorf("revocation_uncertain", "old owner execution cannot be verified stopped; transfer remains pending")
			return nil
		}
		task.OwnerMemberID = receipt.PendingOwnerID
		task.OwnerRevision++
		task.RootControlEpoch++
		task.PendingOwnerID = ""
		task.TransferRequestID = ""
		task.UpdatedAt = s.now()
		if err := tx.SaveTask(task, receipt.TaskRevision); err != nil {
			return err
		}
		receipt.Action = "transfer"
		receipt.Task = task
		receipt.UpdatedAt = s.now()
		if err := tx.SaveTaskTransferReceipt(receipt); err != nil {
			return err
		}
		result = task
		return nil
	})
	return result, errors.Join(err, outcomeErr)
}

func (s *Service) AskQuestion(ctx context.Context, taskID, blockingScope, body string, options []string, recommended string) (w.Question, error) {
	var question w.Question
	if strings.TrimSpace(body) == "" || !slices.Contains([]string{"task", "branch"}, blockingScope) {
		return question, errorf("invalid_request", "question requires a body and task or branch blocking scope")
	}
	if recommended != "" && !slices.Contains(options, recommended) {
		return question, errorf("invalid_request", "recommended answer must be one of the declared options")
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		question = w.Question{
			ID: w.NewID(), TaskID: taskID, TaskRevision: task.CurrentRevision,
			BlockingScope: blockingScope, Body: body, Options: nonNil(options), Recommended: recommended,
			Status: "open", CreatedAt: s.now(),
		}
		return tx.SaveQuestion(question)
	})
	return question, err
}

func (s *Service) AnswerQuestion(ctx context.Context, taskID, questionID, answer, answerRef string, expectedRevision int) (w.Question, error) {
	var question w.Question
	var outcomeErr error
	if answer == "" || answerRef == "" {
		return question, errorf("invalid_request", "answer and stable answer reference are required")
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		question, err = tx.Question(questionID)
		if err != nil || question.TaskID != taskID {
			return errorf("not_found", "question was not found on this task")
		}
		if question.Status == "resolved" {
			if question.AnswerRef != answerRef || question.Answer != answer {
				return errorf("conflict", "question was already resolved with another answer")
			}
			return nil
		}
		if question.Status != "open" {
			return errorf("revision_conflict", "question is no longer open")
		}
		if question.TaskRevision != expectedRevision || task.CurrentRevision != question.TaskRevision {
			question.Status = "superseded"
			if err := tx.SaveQuestion(question); err != nil {
				return err
			}
			outcomeErr = errorf("revision_conflict", "answer targets an older question revision")
			return nil
		}
		question.Status = "resolved"
		question.Answer = answer
		question.AnswerRef = answerRef
		question.ResolvedAt = s.now()
		return tx.SaveQuestion(question)
	})
	if err == nil && outcomeErr == nil && question.Status == "resolved" {
		// WakeTask is replay-safe: it retains the answer observation and either
		// creates the exact continuation or returns its existing run.
		_, err = s.WakeTask(ctx, "question", questionID, "question_answer:"+answerRef)
	}
	return question, errors.Join(err, outcomeErr)
}

func validWakePredicate(predicate string) bool {
	if predicate == "event" {
		return true
	}
	if deadline, ok := strings.CutPrefix(predicate, "at:"); ok {
		_, err := time.Parse(time.RFC3339, deadline)
		return err == nil
	}
	return false
}

func (s *Service) YieldRun(ctx context.Context, runID, predicate, cursor, source, eventID, satisfiedBy string) (w.Wakeup, w.Run, error) {
	var wakeup w.Wakeup
	var continuation w.Run
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		run, err := tx.Run(runID)
		if err != nil {
			return err
		}
		if run.Status != "running" || run.TaskID == "" || source == "" || eventID == "" || !validWakePredicate(predicate) {
			return errorf("conflict", "only a running task run can yield with a structured event or RFC3339 deadline predicate")
		}
		task, err := tx.Task(run.TaskID)
		if err != nil {
			return err
		}
		if task.CurrentRunID != run.ID || task.CurrentRevision != run.TaskRevision || task.Status == "paused" || task.Status == "canceled" {
			return errorf("authority_revoked", "run no longer owns the current task execution")
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		running := []w.Step{}
		for _, step := range steps {
			if step.Status == "running" {
				running = append(running, step)
			}
		}
		if len(running) != 1 {
			return errorf("conflict", "yield requires exactly one active invocation and no concurrent branch")
		}
		now := s.now()
		currentStep := running[0]
		currentStep.Status = "yielded"
		currentStep.Error = "waiting for durable predicate: " + predicate
		if err := tx.SaveStep(currentStep); err != nil {
			return err
		}
		attempts, err := tx.StepAttempts(currentStep.ID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attempt.Status != "running" {
				continue
			}
			attempt.Status = "yielded"
			attempt.Error = currentStep.Error
			attempt.FinishedAt = now
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
		}
		messages, err := tx.Messages(run.ThreadID)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.RunID == run.ID && message.Status == "processing" {
				message.Status = "yielded"
				if err := tx.SaveMessage(message); err != nil {
					return err
				}
			}
		}
		task.WaitGeneration++
		wakeup = w.Wakeup{
			ID: w.NewID(), TaskID: task.ID, TaskRevision: task.CurrentRevision, WaitGeneration: task.WaitGeneration,
			Predicate: predicate, ObservationCursor: cursor, WakeSource: source, EventID: eventID,
			YieldedRunID: run.ID, Status: "pending", CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.SaveWakeup(wakeup); err != nil {
			return err
		}
		run.Status = "yielded"
		run.FinishedAt = now
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		task.Status = "waiting"
		task.WaitingReason = "external"
		if source == "question" {
			task.WaitingReason = "question"
		}
		task.CurrentRunID = ""
		task.UpdatedAt = now
		if err := tx.SaveTask(task, task.CurrentRevision); err != nil {
			return err
		}
		if satisfiedBy == "" && predicate == "event" {
			if observed, observeErr := tx.WakeEvent(source, eventID); observeErr == nil {
				satisfiedBy = observed.SatisfiedBy
			} else if !errors.Is(observeErr, store.ErrNotFound) {
				return observeErr
			}
		}
		if satisfiedBy != "" {
			continuation, err = s.queueWakeInTx(tx, &wakeup, satisfiedBy)
			return err
		}
		return nil
	})
	return wakeup, continuation, err
}

func (s *Service) WakeTask(ctx context.Context, source, eventID, satisfiedBy string) (w.Run, error) {
	var continuation w.Run
	if source == "" || eventID == "" || satisfiedBy == "" {
		return continuation, errorf("invalid_request", "wake event requires source, event ID and observation")
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveWakeEvent(w.WakeEvent{WakeSource: source, EventID: eventID, SatisfiedBy: satisfiedBy, ObservedAt: s.now()}); err != nil {
			return err
		}
		observed, err := tx.WakeEvent(source, eventID)
		if err != nil {
			return err
		}
		wakeup, err := tx.WakeupBySourceEvent(source, eventID)
		if errors.Is(err, store.ErrNotFound) {
			// The event is durable; a later YieldRun consumes it in the same
			// transaction that registers the predicate.
			return nil
		}
		if err != nil {
			return err
		}
		if wakeup.Status != "pending" {
			if wakeup.ContinuationRunID != "" {
				continuation, err = tx.Run(wakeup.ContinuationRunID)
			}
			return err
		}
		continuation, err = s.queueWakeInTx(tx, &wakeup, observed.SatisfiedBy)
		return err
	})
	return continuation, err
}

// WakeDue scans durable predicates after restart; no in-memory timer is source
// of truth. Arbitrary model-written expressions are never evaluated here.
func (s *Service) WakeDue(ctx context.Context, now string) ([]w.Run, error) {
	continuations := []w.Run{}
	pending, err := func() ([]w.Wakeup, error) {
		var values []w.Wakeup
		err := s.store.Read(ctx, func(tx *store.Tx) error {
			var err error
			values, err = tx.PendingWakeups()
			return err
		})
		return values, err
	}()
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, wakeup := range pending {
		deadline, ok := strings.CutPrefix(wakeup.Predicate, "at:")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339, deadline)
		current, currentErr := time.Parse(time.RFC3339, now)
		if err != nil || currentErr != nil || at.After(current) {
			continue
		}
		var continuation w.Run
		err = s.store.Write(ctx, func(tx *store.Tx) error {
			current, err := tx.Wakeup(wakeup.ID)
			if err != nil {
				return err
			}
			if current.Status != "pending" {
				if current.ContinuationRunID != "" {
					continuation, err = tx.Run(current.ContinuationRunID)
				}
				return err
			}
			continuation, err = s.queueWakeInTx(tx, &current, "deadline:"+now)
			return err
		})
		if err != nil {
			errs = append(errs, err)
		} else if continuation.ID != "" {
			continuations = append(continuations, continuation)
		}
	}
	return continuations, errors.Join(errs...)
}

func (s *Service) queueWakeInTx(tx *store.Tx, wakeup *w.Wakeup, satisfiedBy string) (w.Run, error) {
	var continuation w.Run
	task, err := tx.Task(wakeup.TaskID)
	if err != nil {
		return continuation, err
	}
	if task.CurrentRevision != wakeup.TaskRevision || task.WaitGeneration != wakeup.WaitGeneration {
		wakeup.Status = "superseded"
		wakeup.UpdatedAt = s.now()
		return continuation, tx.SaveWakeup(*wakeup)
	}
	if task.Status == "paused" || task.Status == "canceled" {
		wakeup.Status = "canceled"
		wakeup.UpdatedAt = s.now()
		return continuation, tx.SaveWakeup(*wakeup)
	}
	effects, err := tx.Effects(task.ID)
	if err != nil {
		return continuation, err
	}
	for _, effect := range effects {
		if slices.Contains([]string{"executing", "partial", "uncertain"}, effect.Status) {
			return continuation, errorf("effect_outcome_uncertain", "pending effect prevents wake continuation")
		}
	}
	previous, err := tx.Run(wakeup.YieldedRunID)
	if err != nil {
		return continuation, err
	}
	if previous.TaskID != task.ID || previous.Status != "yielded" {
		return continuation, errorf("conflict", "wakeup is not bound to its yielded task run")
	}
	member := w.Agent{}
	for _, candidate := range previous.Agents {
		if candidate.ID == task.OwnerMemberID {
			member = candidate
		}
	}
	if member.ID == "" {
		return continuation, errorf("recipient_unavailable", "continuation has no current coordinator")
	}
	now := s.now()
	continuation = previous
	continuation.ID = w.NewID()
	continuation.Status = "queued"
	continuation.TaskRevision = task.CurrentRevision
	continuation.CreatedAt = now
	continuation.StartedAt = ""
	continuation.FinishedAt = ""
	continuation.DeadlineAt = ""
	continuation.PolicyAdoptedAt = ""
	continuation.Error = ""
	continuation.FailureKind = ""
	continuation.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
	continuation.ContinuedFromRun = previous.ID
	continuation.CollaborationMessageID = ""
	continuation.ExecutionRef = ""
	continuation.ExecutionRoot = ""
	continuation.AutomaticContinuation = true
	continuation.BudgetReserved = false
	continuation.BudgetStarted = false
	continuation.Agents = []w.Agent{member}
	continuation.CoordinatorID = member.ID
	if err := s.EnsureRunAuthorization(tx, &continuation, "wakeup:"+wakeup.ID); err != nil {
		return continuation, err
	}
	manifest, err := s.PrepareContextInTx(tx, task.ID, task.CurrentRevision, ContextHostInput(continuation))
	if err != nil {
		return continuation, err
	}
	taskView, err := s.taskInTx(tx, task.ID)
	if err != nil {
		return continuation, err
	}
	continuation.ContextRef = manifest.ID
	continuation.InputDigest = ContextInputDigest(taskView, manifest, "wakeup:"+wakeup.ID, continuation.Input)
	budgetExhausted := false
	if err := tx.ReserveTaskRun(&continuation); errors.Is(err, w.ErrBudgetExhausted) {
		// The answer remains authoritative. Keep an unreserved continuation queued
		// so expanding the existing account resumes it instead of losing the answer.
		budgetExhausted = true
	} else if err != nil {
		return continuation, err
	}
	if err := tx.InsertRun(continuation, "wakeup:"+wakeup.ID, Digest(*wakeup)); err != nil {
		return continuation, err
	}
	if err := tx.SaveStep(w.Step{
		ID: w.NewID(), RunID: continuation.ID, AgentID: member.ID, AgentName: member.DisplayName,
		Role: "responder", Instruction: "continue from the durable work record after predicate: " + wakeup.Predicate,
		Status: "pending",
	}); err != nil {
		return continuation, err
	}
	wakeup.Status = "consumed"
	wakeup.SatisfiedBy = satisfiedBy
	wakeup.ContinuationRunID = continuation.ID
	wakeup.UpdatedAt = now
	if err := tx.SaveWakeup(*wakeup); err != nil {
		return continuation, err
	}
	task.Status = "ready"
	task.WaitingReason = ""
	if budgetExhausted {
		task.Status = "waiting"
		task.WaitingReason = "budget"
	}
	task.UpdatedAt = now
	return continuation, tx.SaveTask(task, task.CurrentRevision)
}
