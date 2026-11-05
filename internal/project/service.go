// Package project implements durable project/task use cases over the existing
// SQLite transaction boundary. It does not invoke models, tools or networks.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Error carries a stable machine-readable contract code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func errorf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Service coordinates project invariants; every multi-object write uses one
// store transaction and performs no external call while that transaction is open.
type Service struct {
	store *store.Store
	now   func() string
}

// New constructs the project use-case service explicitly.
func New(db *store.Store) *Service {
	return &Service{store: db, now: w.Now}
}

// CreateTaskInput is an accepted user/root or authorized owned-child objective.
type CreateTaskInput struct {
	WorkspaceID     string                  `json:"workspace_id"`
	ParentID        string                  `json:"parent_id,omitempty"`
	ThreadID        string                  `json:"thread_id"`
	OwnerMemberID   string                  `json:"owner_member_id"`
	Goal            string                  `json:"goal"`
	Scope           []string                `json:"scope"`
	NonGoals        []string                `json:"non_goals"`
	ConstraintRefs  []string                `json:"constraint_refs"`
	Acceptance      []w.AcceptanceCriterion `json:"acceptance"`
	InputRefs       []string                `json:"input_refs"`
	AllowedActions  []string                `json:"allowed_actions"`
	SourceMessageID string                  `json:"source_message_id"`
	AcceptedBy      string                  `json:"accepted_by"`
	Kind            string                  `json:"kind,omitempty"`
	Priority        int                     `json:"priority,omitempty"`
}

// TaskView joins the current objective with its exact acceptance projection.
type TaskView struct {
	Task        w.Task               `json:"task"`
	Revision    w.TaskRevision       `json:"revision"`
	Budget      w.BudgetAccount      `json:"budget"`
	Completions []w.CompletionRecord `json:"completions"`
}

// Overview is the two-page product's project-facing projection.
type Overview struct {
	Workspace      w.Workspace       `json:"workspace"`
	Records        []w.ProjectRecord `json:"records"`
	Tasks          []TaskView        `json:"tasks"`
	NeedsAttention []w.Question      `json:"needs_attention"`
}

// CreateTask persists a root or owned child with its first revision and root ledger.
func (s *Service) CreateTask(ctx context.Context, in CreateTaskInput) (TaskView, error) {
	var view TaskView
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		view, err = s.CreateTaskInTx(tx, in)
		return err
	})
	return view, err
}

// CreateTaskInTx is also used by legacy Run submission to atomically uplift the
// new user input into a durable task without creating a second user message.
func (s *Service) CreateTaskInTx(tx *store.Tx, in CreateTaskInput) (TaskView, error) {
	var view TaskView
	if strings.TrimSpace(in.Goal) == "" || len([]rune(in.Goal)) > 16000 {
		return view, errorf("invalid_request", "goal must contain 1–16000 characters")
	}
	if in.AcceptedBy == "" {
		return view, errorf("acceptance_required", "an accepted user or authorized owner source is required")
	}
	ws, err := tx.Workspace(in.WorkspaceID)
	if err != nil {
		return view, err
	}
	th, err := tx.Thread(in.ThreadID)
	if err != nil || th.WorkspaceID != ws.ID {
		return view, errorf("not_found", "task thread was not found in this workspace")
	}
	if th.TaskID != "" {
		return view, errorf("conflict", "thread already belongs to a durable task")
	}
	now := s.now()
	taskID := w.NewID()
	rootID, accountID, scopeID := taskID, w.NewID(), w.NewID()
	rootEpoch := 1
	account := w.BudgetAccount{}
	if in.ParentID == "" {
		settings, err := tx.Settings()
		if err != nil {
			return view, err
		}
		policy, err := w.ResolveBudget(settings.CollaborationBudget, ws.BudgetOverride)
		if err != nil {
			return view, err
		}
		account = w.BudgetAccount{
			ID: accountID, RootTaskID: rootID, Policy: policy,
			Sources: budgetSources(ws.BudgetOverride), Revision: 1,
			FixRoundsByIssue: map[string]int{}, NoProgressByBranch: map[string]int{}, UpdatedAt: now,
		}
	} else {
		parent, err := tx.Task(in.ParentID)
		if err != nil || parent.WorkspaceID != ws.ID {
			return view, errorf("not_found", "parent task was not found in this workspace")
		}
		if parent.Status == "paused" || parent.Status == "canceled" {
			return view, errorf("authority_revoked", "paused or canceled task trees cannot admit new work")
		}
		rootID, accountID, scopeID = parent.RootTaskID, parent.BudgetAccountID, parent.CollaborationScopeID
		rootEpoch = parent.RootControlEpoch
		account, err = tx.BudgetAccount(accountID)
		if err != nil {
			return view, err
		}
	}
	acceptance, err := normalizeAcceptance(in.Acceptance, in.Goal)
	if err != nil {
		return view, err
	}
	revision := w.TaskRevision{
		TaskID: taskID, Revision: 1, Goal: strings.TrimSpace(in.Goal),
		Scope: nonNil(in.Scope), NonGoals: nonNil(in.NonGoals), ConstraintRefs: nonNil(in.ConstraintRefs),
		Acceptance: acceptance, InputRefs: nonNil(in.InputRefs), AllowedActions: nonNil(in.AllowedActions),
		SourceMessageID: in.SourceMessageID, AcceptedBy: in.AcceptedBy,
		AcceptanceEvidence: in.SourceMessageID, CreatedAt: now,
	}
	revision.Digest = Digest(revision)
	task := w.Task{
		ID: taskID, WorkspaceID: ws.ID, ParentID: in.ParentID, ThreadID: th.ID,
		CurrentRevision: 1, OwnerMemberID: in.OwnerMemberID, OwnerRevision: 1,
		RootTaskID: rootID, RootControlEpoch: rootEpoch, BudgetAccountID: accountID,
		CollaborationScopeID: scopeID, Status: "ready", Kind: in.Kind, Priority: in.Priority,
		EvaluationGeneration: 1, EvaluationStatus: "incomplete", CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.InsertTask(task); err != nil {
		return view, err
	}
	if err := tx.InsertTaskRevision(revision); err != nil {
		return view, err
	}
	if in.ParentID == "" {
		if err := tx.InsertBudgetAccount(account); err != nil {
			return view, err
		}
	}
	th.TaskID = taskID
	th.UpdatedAt = now
	th.Revision++
	if err := tx.SaveThread(th); err != nil {
		return view, err
	}
	return TaskView{Task: task, Revision: revision, Budget: account, Completions: []w.CompletionRecord{}}, nil
}

func runInputRefs(in w.RunInput) []string {
	refs := nonNil(in.SourceURLs)
	for _, id := range in.SourceIDs {
		refs = append(refs, "source:"+id)
	}
	return refs
}

// EnsureThreadTask preserves old chats until their first new task submission.
// Every later user submission is an accepted goal revision; pending steering is
// applied once to that new execution boundary rather than mutating an old Run.
func (s *Service) EnsureThreadTask(tx *store.Tx, th w.Thread, in w.RunInput, messageID, runID string) (TaskView, error) {
	actions := []string{"read", "publish_artifact", "collaborate", "network"}
	if in.EngineeringAction == w.EngineeringDevelop {
		actions = append(actions, "write", "run_test")
	}
	if th.TaskID == "" {
		return s.CreateTaskInTx(tx, CreateTaskInput{
			WorkspaceID: th.WorkspaceID, ThreadID: th.ID, OwnerMemberID: th.CoordinatorID,
			Goal: in.Message, Acceptance: nil, InputRefs: runInputRefs(in),
			AllowedActions: actions, SourceMessageID: messageID, AcceptedBy: "user",
		})
	}
	view, err := s.taskInTx(tx, th.TaskID)
	if err != nil {
		return TaskView{}, err
	}
	items, err := tx.Inbox(th.TaskID)
	if err != nil {
		return TaskView{}, err
	}
	goal := strings.TrimSpace(in.Message)
	pending := []w.InboxItem{}
	for _, item := range items {
		if item.Status != "pending" {
			continue
		}
		if item.ApplicationPolicy == "follow_up" {
			now := s.now()
			followThread := w.Thread{
				ID: w.NewID(), WorkspaceID: th.WorkspaceID, Title: "后续任务",
				AgentIDs: slices.Clone(th.AgentIDs), CoordinatorID: th.CoordinatorID,
				Internal: true, Revision: 1, UpdatedAt: now,
			}
			if err := tx.SaveThread(followThread); err != nil {
				return TaskView{}, err
			}
			follow, err := s.CreateTaskInTx(tx, CreateTaskInput{
				WorkspaceID: th.WorkspaceID, ThreadID: followThread.ID,
				OwnerMemberID: view.Task.OwnerMemberID, Goal: item.Body,
				AllowedActions:  slices.Clone(view.Revision.AllowedActions),
				SourceMessageID: item.SourceMessageID, AcceptedBy: "user",
			})
			if err != nil {
				return TaskView{}, err
			}
			item.Status = "applied"
			item.AppliedRevision = follow.Task.CurrentRevision
			item.RunID = ""
			item.FollowUpTaskID = follow.Task.ID
			item.UpdatedAt = now
			if err := tx.SaveInbox(item); err != nil {
				return TaskView{}, err
			}
			continue
		}
		if item.ExpectedRevision != view.Task.CurrentRevision {
			item.Status = "superseded"
			item.UpdatedAt = s.now()
			if err := tx.SaveInbox(item); err != nil {
				return TaskView{}, err
			}
			continue
		}
		goal += "\n\n补充要求：" + strings.TrimSpace(item.Body)
		pending = append(pending, item)
	}
	if len(view.Revision.AllowedActions) > 0 {
		actions = nonNil(view.Revision.AllowedActions)
		if in.EngineeringAction == w.EngineeringDevelop {
			for _, action := range []string{"write", "run_test"} {
				if !slices.Contains(actions, action) {
					actions = append(actions, action)
				}
			}
		}
	}
	view, err = s.reviseTaskInTx(tx, view.Task, ReviseTaskInput{
		Goal: goal, Scope: view.Revision.Scope, NonGoals: view.Revision.NonGoals,
		ConstraintRefs: view.Revision.ConstraintRefs, Acceptance: nil,
		InputRefs: runInputRefs(in), AllowedActions: actions,
		SourceMessageID: messageID, AcceptedBy: "user",
	})
	if err != nil {
		return TaskView{}, err
	}
	for _, item := range pending {
		item.Status = "applied"
		item.AppliedRevision = view.Task.CurrentRevision
		item.RunID = runID
		item.UpdatedAt = s.now()
		if err := tx.SaveInbox(item); err != nil {
			return TaskView{}, err
		}
	}
	return view, nil
}

// Task returns one exact current view.
func (s *Service) Task(ctx context.Context, id string) (TaskView, error) {
	var view TaskView
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		view, err = s.taskInTx(tx, id)
		return err
	})
	return view, err
}

func (s *Service) taskInTx(tx *store.Tx, id string) (TaskView, error) {
	task, err := tx.Task(id)
	if err != nil {
		return TaskView{}, err
	}
	revision, err := tx.TaskRevision(id, task.CurrentRevision)
	if err != nil {
		return TaskView{}, err
	}
	budget, err := tx.BudgetAccount(task.BudgetAccountID)
	if err != nil {
		return TaskView{}, err
	}
	completions, err := tx.Completions(id)
	if err != nil {
		return TaskView{}, err
	}
	return TaskView{Task: task, Revision: revision, Budget: budget, Completions: completions}, nil
}

// ReviseTaskInput fully replaces goal-scoped fields; omitted slices are empty,
// never an implicit deletion of required acceptance criteria.
type ReviseTaskInput struct {
	Goal            string                  `json:"goal"`
	Scope           []string                `json:"scope"`
	NonGoals        []string                `json:"non_goals"`
	ConstraintRefs  []string                `json:"constraint_refs"`
	Acceptance      []w.AcceptanceCriterion `json:"acceptance"`
	InputRefs       []string                `json:"input_refs"`
	AllowedActions  []string                `json:"allowed_actions"`
	SourceMessageID string                  `json:"source_message_id"`
	AcceptedBy      string                  `json:"accepted_by"`
}

// ReviseTask applies If-Match semantics and advances the evaluation generation.
func (s *Service) ReviseTask(ctx context.Context, id string, expected int, in ReviseTaskInput) (TaskView, error) {
	var view TaskView
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(id)
		if err != nil {
			return err
		}
		if task.CurrentRevision != expected {
			return errorf("revision_conflict", "task revision changed; reload before revising")
		}
		view, err = s.reviseTaskInTx(tx, task, in)
		return err
	})
	return view, err
}

// reviseTaskInTx is shared by explicit revisions and legacy Thread submissions.
// The caller's surrounding transaction also owns message, Run and inbox writes.
func (s *Service) reviseTaskInTx(tx *store.Tx, task w.Task, in ReviseTaskInput) (TaskView, error) {
	if in.AcceptedBy == "" || in.SourceMessageID == "" {
		return TaskView{}, errorf("acceptance_required", "goal revisions require a user source and acceptance basis")
	}
	acceptance, err := normalizeAcceptance(in.Acceptance, in.Goal)
	if err != nil {
		return TaskView{}, err
	}
	next := w.TaskRevision{
		TaskID: task.ID, Revision: task.CurrentRevision + 1, Goal: strings.TrimSpace(in.Goal),
		Scope: nonNil(in.Scope), NonGoals: nonNil(in.NonGoals), ConstraintRefs: nonNil(in.ConstraintRefs),
		Acceptance: acceptance, InputRefs: nonNil(in.InputRefs), AllowedActions: nonNil(in.AllowedActions),
		SourceMessageID: in.SourceMessageID, AcceptedBy: in.AcceptedBy,
		AcceptanceEvidence: in.SourceMessageID, CreatedAt: s.now(),
	}
	if next.Goal == "" {
		return TaskView{}, errorf("invalid_request", "goal must not be empty")
	}
	next.Digest = Digest(next)
	if err := tx.InsertTaskRevision(next); err != nil {
		return TaskView{}, err
	}
	expected := task.CurrentRevision
	task.CurrentRevision = next.Revision
	task.EvaluationGeneration++
	task.EvaluationStatus = "incomplete"
	task.Status = "ready"
	task.WaitingReason = ""
	task.UpdatedAt = s.now()
	if err := tx.SaveTask(task, expected); err != nil {
		return TaskView{}, err
	}
	if err := invalidateTrackCurrent(tx, task.ID); err != nil {
		return TaskView{}, err
	}
	return s.taskInTx(tx, task.ID)
}

// RecordCompletionInput is a candidate judgment; host fields are assigned by the service.
type RecordCompletionInput struct {
	EvaluationGeneration int                 `json:"evaluation_generation"`
	InputSnapshotDigest  string              `json:"input_snapshot_digest"`
	SubjectDigest        string              `json:"subject_digest"`
	CandidateRefs        []string            `json:"candidate_refs"`
	Results              []w.CriterionResult `json:"results"`
	JudgeMemberID        string              `json:"judge_member_id"`
	EvidenceProducer     string              `json:"evidence_producer"`
	Unresolved           []string            `json:"unresolved"`
}

// RecordCompletion records a user, runner, provider or model candidate judgment.
// Host-observed evidence cannot be asserted through this externally reachable path.
func (s *Service) RecordCompletion(ctx context.Context, taskID string, in RecordCompletionInput) (w.CompletionRecord, error) {
	return s.recordCompletion(ctx, taskID, in, false)
}

// RecordHostCompletion is reserved for a host observation made by the execution
// adapter itself, never for a producer label copied from an API or tool payload.
func (s *Service) RecordHostCompletion(ctx context.Context, taskID string, in RecordCompletionInput) (w.CompletionRecord, error) {
	if in.EvidenceProducer != "host_observed" {
		return w.CompletionRecord{}, errorf("invalid_request", "host completion requires a host observation")
	}
	return s.recordCompletion(ctx, taskID, in, true)
}

// recordCompletion validates exact versions and pending effects before projecting
// task completion. Run success alone never calls this method.
func (s *Service) recordCompletion(ctx context.Context, taskID string, in RecordCompletionInput, allowHost bool) (w.CompletionRecord, error) {
	var record w.CompletionRecord
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		view, err := s.taskInTx(tx, taskID)
		if err != nil {
			return err
		}
		if in.EvaluationGeneration != view.Task.EvaluationGeneration || in.InputSnapshotDigest != view.Revision.Digest {
			return errorf("evidence_stale", "completion does not match the current task and evaluation generation")
		}
		if !slices.Contains([]string{"host_observed", "runner_reported", "provider_reported", "model_assessed", "human_confirmed"}, in.EvidenceProducer) {
			return errorf("invalid_request", "unknown evidence producer")
		}
		if in.EvidenceProducer == "host_observed" && !allowHost {
			return errorf("invalid_request", "host-observed evidence must be assigned by the host adapter")
		}
		if in.JudgeMemberID == "" {
			return errorf("invalid_request", "completion requires a stable judge identity")
		}
		effects, err := tx.Effects(taskID)
		if err != nil {
			return err
		}
		for _, effect := range effects {
			if slices.Contains([]string{"executing", "partial", "uncertain"}, effect.Status) {
				return errorf("effect_outcome_uncertain", "pending effect %s prevents current completion", effect.ID)
			}
		}
		validCriteria := map[string]bool{}
		for _, criterion := range view.Revision.Acceptance {
			validCriteria[criterion.CriterionID] = true
		}
		seen := map[string]bool{}
		for _, result := range in.Results {
			if !validCriteria[result.CriterionID] || seen[result.CriterionID] {
				return errorf("invalid_request", "completion contains an unknown or duplicate criterion")
			}
			seen[result.CriterionID] = true
			if !slices.Contains([]string{"passed", "failed", "not_run", "inconclusive", "not_applicable"}, result.Result) {
				return errorf("invalid_request", "invalid criterion result")
			}
			if result.Result == "passed" && len(result.Evidence) == 0 {
				return errorf("invalid_request", "a passed criterion requires exact evidence references")
			}
		}
		if !allowHost {
			digest, err := externalSubjectDigest(tx, view, in)
			if err != nil {
				return err
			}
			in.SubjectDigest = digest
		}
		record = w.CompletionRecord{
			ID: w.NewID(), TaskID: taskID, TaskRevision: view.Task.CurrentRevision,
			EvaluationGeneration: in.EvaluationGeneration, InputSnapshotDigest: in.InputSnapshotDigest,
			SubjectDigest: in.SubjectDigest, CandidateRefs: nonNil(in.CandidateRefs), Results: nonNil(in.Results),
			JudgeMemberID: in.JudgeMemberID, EvidenceProducer: in.EvidenceProducer,
			Validity: "current", Unresolved: nonNil(in.Unresolved), CreatedAt: s.now(),
		}
		if record.Accepted(view.Task, view.Revision) && (record.SubjectDigest == "" || len(record.CandidateRefs) == 0) {
			return errorf("invalid_request", "accepted completion requires an exact candidate and subject digest")
		}
		if err := tx.InsertCompletion(record); err != nil {
			return err
		}
		task := view.Task
		if record.Accepted(task, view.Revision) {
			task.Status = "completed"
			task.EvaluationStatus = "current"
		} else {
			task.Status = "ready"
			task.EvaluationStatus = "incomplete"
		}
		task.UpdatedAt = s.now()
		return tx.SaveTask(task, view.Task.CurrentRevision)
	})
	return record, err
}

// TaskAction applies control epochs to the selected task's owned descendants only.
func (s *Service) TaskAction(ctx context.Context, id, action, requestID string) (w.Task, error) {
	task, _, err := s.TaskActionWithReplay(ctx, id, action, requestID)
	return task, err
}

// TaskActionWithReplay distinguishes a committed control action from its replay,
// allowing the HTTP layer to avoid canceling newer work during request retries.
func (s *Service) TaskActionWithReplay(ctx context.Context, id, action, requestID string) (w.Task, bool, error) {
	if requestID == "" {
		return w.Task{}, false, errorf("invalid_request", "a stable request id is required")
	}
	if !slices.Contains([]string{"start", "pause", "resume", "cancel"}, action) {
		return w.Task{}, false, errorf("invalid_request", "unknown task action")
	}
	requestDigest := Digest(struct {
		TaskID string `json:"task_id"`
		Action string `json:"action"`
	}{TaskID: id, Action: action})
	var result w.Task
	replayed := false
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		if previous, err := tx.TaskAction(id, requestID); err == nil {
			if previous.RequestDigest != requestDigest {
				return errorf("conflict", "the same action request id was reused for a different action")
			}
			result = previous.Task
			replayed = true
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		task, err := tx.Task(id)
		if err != nil {
			return err
		}
		tasks, err := tx.Tasks(task.WorkspaceID)
		if err != nil {
			return err
		}
		owned := map[string]bool{task.ID: true}
		for changed := true; changed; {
			changed = false
			for _, candidate := range tasks {
				if owned[candidate.ParentID] && !owned[candidate.ID] {
					owned[candidate.ID] = true
					changed = true
				}
			}
		}
		for _, candidate := range tasks {
			if !owned[candidate.ID] {
				continue
			}
			next := candidate
			switch action {
			case "pause":
				if candidate.Status != "completed" && candidate.Status != "canceled" {
					next.Status = "paused"
				}
			case "cancel":
				if candidate.Status != "completed" {
					next.Status = "canceled"
				}
			case "resume":
				if candidate.Status == "paused" {
					next.Status = "ready"
				}
			case "start":
				if candidate.ID == id && candidate.Status == "ready" {
					next.Status = "active"
				}
			}
			if action == "pause" || action == "cancel" {
				next.RootControlEpoch++
			}
			if next.Status != candidate.Status && !w.CanTransitionTask(candidate.Status, next.Status) {
				return errorf("conflict", "invalid task transition from %s to %s", candidate.Status, next.Status)
			}
			next.UpdatedAt = s.now()
			if err := tx.SaveTask(next, candidate.CurrentRevision); err != nil {
				return err
			}
			if candidate.ID == id {
				result = next
			}
		}
		return tx.InsertTaskAction(w.TaskActionReceipt{TaskID: id, RequestID: requestID, Action: action, RequestDigest: requestDigest, Task: result, CreatedAt: s.now()})
	})
	return result, replayed, err
}

// AdjustBudget changes only the stable account, never started Run usage. Queued
// reservations are released and deterministically reassigned under the new cap.
func (s *Service) AdjustBudget(ctx context.Context, taskID string, expected int, policy w.BudgetPolicy) (w.BudgetAccount, error) {
	var account w.BudgetAccount
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		account, err = tx.BudgetAccount(task.BudgetAccountID)
		if err != nil {
			return err
		}
		if account.Revision != expected {
			return errorf("revision_conflict", "budget account changed; reload before adjusting")
		}
		if err := w.ValidateBudgetPolicy(policy); err != nil {
			return err
		}
		tasks, err := tx.Tasks(task.WorkspaceID)
		if err != nil {
			return err
		}
		byID := map[string]w.Task{}
		for _, candidate := range tasks {
			byID[candidate.ID] = candidate
		}
		runs, err := tx.WorkspaceRuns(task.WorkspaceID)
		if err != nil {
			return err
		}
		type queuedReservation struct {
			run  w.Run
			task w.Task
		}
		queued := []queuedReservation{}
		for _, run := range runs {
			owner, ok := byID[run.TaskID]
			if run.Status == "queued" && run.AutomaticContinuation && !run.BudgetStarted && ok && owner.BudgetAccountID == account.ID {
				queued = append(queued, queuedReservation{run: run, task: owner})
			}
		}
		slices.SortFunc(queued, func(a, b queuedReservation) int {
			if a.task.Priority != b.task.Priority {
				return b.task.Priority - a.task.Priority
			}
			if order := strings.Compare(a.run.CreatedAt, b.run.CreatedAt); order != 0 {
				return order
			}
			return strings.Compare(a.run.ID, b.run.ID)
		})
		released := 0
		for i := range queued {
			if !queued[i].run.BudgetReserved {
				continue
			}
			queued[i].run.BudgetReserved = false
			queued[i].run.ReservationReleaseReason = "reservation_released_due_to_policy"
			if err := tx.SaveRun(queued[i].run); err != nil {
				return err
			}
			if err := tx.AppendEvent(queued[i].run.ID, "", "", "reservation_released_due_to_policy", map[string]any{"budget_account_id": account.ID, "policy_revision": expected + 1}); err != nil {
				return err
			}
			released++
		}
		account.ReservedRuns = max(0, account.ReservedRuns-released)
		account.Policy = policy
		account.Sources = w.BudgetSources{AutoContinuations: "task", ParallelBranches: "task", WriterBranches: "task", FixRounds: "task", NoProgressFailures: "task"}
		account.Revision++
		account.UpdatedAt = s.now()
		if err := tx.SaveBudgetAccount(account, expected); err != nil {
			return err
		}
		for _, candidate := range tasks {
			if candidate.BudgetAccountID != account.ID || candidate.Status != "waiting" {
				continue
			}
			issueID := ""
			for _, run := range runs {
				if run.TaskID == candidate.ID && run.BudgetIssueID != "" {
					issueID = run.BudgetIssueID
				}
			}
			allowed := issueID != "" && candidate.WaitingReason == "fix_limit" && account.FixRoundsByIssue[issueID] < policy.FixRounds
			if candidate.WaitingReason == "no_progress" {
				allowed = issueID != "" && account.NoProgressByBranch[issueID] < policy.NoProgressFailures
			}
			if !allowed {
				continue
			}
			candidate.Status = "ready"
			candidate.WaitingReason = ""
			candidate.UpdatedAt = s.now()
			if err := tx.SaveTask(candidate, candidate.CurrentRevision); err != nil {
				return err
			}
		}
		for _, item := range queued {
			currentTask, err := tx.Task(item.task.ID)
			if err != nil {
				return err
			}
			if currentTask.Status == "paused" || currentTask.Status == "canceled" {
				continue
			}
			if account.RemainingContinuations() > 0 {
				if err := tx.ReserveTaskRun(&item.run); err != nil {
					return err
				}
				if err := tx.SaveRun(item.run); err != nil {
					return err
				}
				account, err = tx.BudgetAccount(account.ID)
				if err != nil {
					return err
				}
				if currentTask.Status == "waiting" && currentTask.WaitingReason == "budget" {
					currentTask.Status = "ready"
					currentTask.WaitingReason = ""
					currentTask.UpdatedAt = s.now()
					if err := tx.SaveTask(currentTask, currentTask.CurrentRevision); err != nil {
						return err
					}
				}
			} else if currentTask.Status != "active" && currentTask.Status != "completed" {
				currentTask.Status = "waiting"
				currentTask.WaitingReason = "budget"
				currentTask.UpdatedAt = s.now()
				if err := tx.SaveTask(currentTask, currentTask.CurrentRevision); err != nil {
					return err
				}
			}
		}
		if account.RemainingContinuations() > 0 {
			for _, candidate := range tasks {
				if candidate.BudgetAccountID != account.ID || candidate.Status != "waiting" || candidate.WaitingReason != "budget" {
					continue
				}
				candidate.Status = "ready"
				candidate.WaitingReason = ""
				candidate.UpdatedAt = s.now()
				if err := tx.SaveTask(candidate, candidate.CurrentRevision); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return account, err
}

// ReserveRun reserves one automatic continuation at dispatch. The first
// user-initiated root Run is free; idempotent replay must reuse its original Run.
func (s *Service) ReserveRun(ctx context.Context, taskID string, automatic bool) (w.BudgetAccount, error) {
	var account w.BudgetAccount
	exhausted := false
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		if task.Status == "paused" || task.Status == "canceled" {
			return errorf("authority_revoked", "task control state prevents dispatch")
		}
		account, err = tx.BudgetAccount(task.BudgetAccountID)
		if err != nil {
			return err
		}
		if automatic && account.RemainingContinuations() <= 0 {
			exhausted = true
			if task.Status != "waiting" || task.WaitingReason != "budget" {
				task.Status = "waiting"
				task.WaitingReason = "budget"
				task.UpdatedAt = s.now()
				return tx.SaveTask(task, task.CurrentRevision)
			}
			return nil
		}
		if automatic {
			account.ReservedRuns++
			account.Revision++
			account.UpdatedAt = s.now()
			return tx.SaveBudgetAccount(account, account.Revision-1)
		}
		return nil
	})
	if err == nil && exhausted {
		err = errorf("budget_exhausted", "automatic continuation budget is exhausted")
	}
	return account, err
}

// ProjectOverview returns active records and task projections without loading all message bodies.
func (s *Service) ProjectOverview(ctx context.Context, workspaceID string) (Overview, error) {
	var out Overview
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		ws, err := tx.Workspace(workspaceID)
		if err != nil {
			return err
		}
		records, err := tx.ProjectRecords(workspaceID)
		if err != nil {
			return err
		}
		tasks, err := tx.Tasks(workspaceID)
		if err != nil {
			return err
		}
		out = Overview{Workspace: ws, Records: currentRecords(records), Tasks: []TaskView{}, NeedsAttention: []w.Question{}}
		for _, task := range tasks {
			view, err := s.taskInTx(tx, task.ID)
			if err != nil {
				return err
			}
			out.Tasks = append(out.Tasks, view)
			questions, err := tx.Questions(task.ID)
			if err != nil {
				return err
			}
			for _, question := range questions {
				if question.Status == "open" {
					out.NeedsAttention = append(out.NeedsAttention, question)
				}
			}
		}
		return nil
	})
	return out, err
}

// AddDependency rejects self-links and cycles before committing an exact edge.
func (s *Service) AddDependency(ctx context.Context, in w.TaskDependency) (w.TaskDependency, error) {
	if in.Mode != "pinned" && in.Mode != "track_current" {
		return in, errorf("invalid_request", "dependency mode must be pinned or track_current")
	}
	if in.ProducerTaskID == in.ConsumerTaskID {
		return in, errorf("dependency_cycle", "a task cannot depend on itself")
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		consumer, err := tx.Task(in.ConsumerTaskID)
		if err != nil {
			return err
		}
		producer, err := tx.Task(in.ProducerTaskID)
		if err != nil || producer.WorkspaceID != consumer.WorkspaceID {
			return errorf("not_found", "producer task is outside the consumer workspace")
		}
		all, err := tx.Dependencies(consumer.WorkspaceID)
		if err != nil {
			return err
		}
		graph := map[string][]string{}
		for _, edge := range all {
			graph[edge.ConsumerTaskID] = append(graph[edge.ConsumerTaskID], edge.ProducerTaskID)
		}
		graph[consumer.ID] = append(graph[consumer.ID], producer.ID)
		seen, visiting := map[string]bool{}, map[string]bool{}
		var visit func(string) bool
		visit = func(id string) bool {
			if visiting[id] {
				return true
			}
			if seen[id] {
				return false
			}
			visiting[id] = true
			for _, parent := range graph[id] {
				if visit(parent) {
					return true
				}
			}
			visiting[id] = false
			seen[id] = true
			return false
		}
		if visit(consumer.ID) {
			return errorf("dependency_cycle", "dependency would create a cycle")
		}
		in.ID = w.NewID()
		in.Valid = true
		in.CreatedAt = s.now()
		return tx.SaveDependency(in)
	})
	return in, err
}

// SetExecutionMode persists an explicit project choice and never resets budget.
func (s *Service) SetExecutionMode(ctx context.Context, workspaceID, mode, sourceMessageID string, expectedRevision int) (w.Workspace, error) {
	if !w.ValidExecutionMode(mode) || sourceMessageID == "" {
		return w.Workspace{}, errorf("invalid_request", "a valid execution mode and user source are required")
	}
	var ws w.Workspace
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		ws, err = tx.Workspace(workspaceID)
		if err != nil {
			return err
		}
		if expectedRevision != ws.Revision {
			return errorf("revision_conflict", "workspace changed before the execution mode was saved")
		}
		ws.ExecutionMode = mode
		ws.Revision++
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return s.setModeGrants(tx, ws, mode, sourceMessageID)
	})
	return ws, err
}

// SetBudgetOverride saves only future root defaults and validates the merged value.
func (s *Service) SetBudgetOverride(ctx context.Context, workspaceID string, override *w.BudgetPolicyOverride, expectedRevision int) (w.Workspace, error) {
	var ws w.Workspace
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		ws, err = tx.Workspace(workspaceID)
		if err != nil {
			return err
		}
		if expectedRevision != ws.Revision {
			return errorf("revision_conflict", "workspace changed before the budget policy was saved")
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		if _, err := w.ResolveBudget(settings.CollaborationBudget, override); err != nil {
			return err
		}
		ws.BudgetOverride = override
		ws.Revision++
		return tx.SaveWorkspace(ws)
	})
	return ws, err
}

// SaveProjectRecord accepts or supersedes a versioned project fact.
func (s *Service) SaveProjectRecord(ctx context.Context, in w.ProjectRecord) (w.ProjectRecord, error) {
	if in.WorkspaceID == "" || in.ID == "" || strings.TrimSpace(in.Content) == "" || in.AcceptedBy == "" {
		return in, errorf("invalid_request", "project record requires identity, content and acceptance source")
	}
	if in.Visibility == "" {
		in.Visibility = "project"
	}
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		records, err := tx.ProjectRecords(in.WorkspaceID)
		if err != nil {
			return err
		}
		revision := 1
		for _, record := range records {
			if record.ID == in.ID {
				revision = max(revision, record.Revision+1)
			}
		}
		in.Revision = revision
		in.Status = "active"
		in.SourceRefs = nonNil(in.SourceRefs)
		in.CreatedAt = s.now()
		if err := tx.SaveProjectRecord(in); err != nil {
			return err
		}
		if in.Supersedes != "" {
			for _, record := range currentRecords(records) {
				if record.ID == in.Supersedes {
					record.Status = "superseded"
					record.Revision++
					record.CreatedAt = s.now()
					return tx.SaveProjectRecord(record)
				}
			}
		}
		return nil
	})
	return in, err
}

// Digest is the canonical SHA-256 of a JSON snapshot.
func Digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func normalizeAcceptance(values []w.AcceptanceCriterion, goal string) ([]w.AcceptanceCriterion, error) {
	if len(values) == 0 {
		values = []w.AcceptanceCriterion{{
			CriterionID: "outcome", Statement: strings.TrimSpace(goal), Required: true,
			Kind: "outcome", EvidenceRequirements: []string{"exact_candidate", "current_inputs"},
			EvaluationPolicy: "all_required",
		}}
	}
	seen := map[string]bool{}
	for i := range values {
		values[i].CriterionID = strings.TrimSpace(values[i].CriterionID)
		values[i].Statement = strings.TrimSpace(values[i].Statement)
		if values[i].CriterionID == "" || values[i].Statement == "" || seen[values[i].CriterionID] {
			return nil, errorf("invalid_request", "acceptance criteria require unique ids and statements")
		}
		seen[values[i].CriterionID] = true
		values[i].EvidenceRequirements = nonNil(values[i].EvidenceRequirements)
	}
	return values, nil
}

func budgetSources(override *w.BudgetPolicyOverride) w.BudgetSources {
	source := func(present bool) string {
		if present {
			return "project"
		}
		return "global"
	}
	return w.BudgetSources{
		AutoContinuations:  source(override != nil && override.AutoContinuations != nil),
		ParallelBranches:   source(override != nil && override.ParallelBranches != nil),
		WriterBranches:     source(override != nil && override.WriterBranches != nil),
		FixRounds:          source(override != nil && override.FixRounds != nil),
		NoProgressFailures: source(override != nil && override.NoProgressFailures != nil),
	}
}

func currentRecords(records []w.ProjectRecord) []w.ProjectRecord {
	latest := map[string]w.ProjectRecord{}
	for _, record := range records {
		if record.Revision > latest[record.ID].Revision {
			latest[record.ID] = record
		}
	}
	out := []w.ProjectRecord{}
	for _, record := range latest {
		if record.Status == "active" {
			out = append(out, record)
		}
	}
	slices.SortFunc(out, func(a, b w.ProjectRecord) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func invalidateTrackCurrent(tx *store.Tx, producerTaskID string) error {
	task, err := tx.Task(producerTaskID)
	if err != nil {
		return err
	}
	dependencies, err := tx.Dependencies(task.WorkspaceID)
	if err != nil {
		return err
	}
	for _, dependency := range dependencies {
		if dependency.ProducerTaskID != producerTaskID || dependency.Mode != "track_current" || !dependency.Valid {
			continue
		}
		dependency.Valid = false
		if err := tx.SaveDependencyValidity(dependency); err != nil {
			return err
		}
		consumer, err := tx.Task(dependency.ConsumerTaskID)
		if err != nil {
			return err
		}
		consumer.EvaluationGeneration++
		consumer.EvaluationStatus = "needs_revalidation"
		if consumer.Status == "completed" {
			consumer.Status = "ready"
		}
		consumer.UpdatedAt = w.Now()
		if err := tx.SaveTask(consumer, consumer.CurrentRevision); err != nil {
			return err
		}
	}
	return nil
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return slices.Clone(values)
}

// ParseRevision supports HTTP If-Match without accepting ambiguous values.
func ParseRevision(value string) (int, error) {
	var revision int
	if _, err := fmt.Sscanf(value, "%d", &revision); err != nil || revision < 1 {
		return 0, errorf("revision_conflict", "If-Match must contain a positive task revision")
	}
	return revision, nil
}

// IsExpired centralizes optional protocol expiry checks.
func IsExpired(value string, now time.Time) bool {
	if value == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !now.Before(expires)
}

// IsCode exposes stable errors without string matching.
func IsCode(err error, code string) bool {
	var projectError *Error
	return errors.As(err, &projectError) && projectError.Code == code
}
