package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TaskList and the other list contracts remain stable as project views grow.
type TaskList struct {
	Items []project.TaskView `json:"items"`
}

type RevisionList struct {
	Items []w.TaskRevision `json:"items"`
}

type DependencyList struct {
	Items []w.TaskDependency `json:"items"`
}

type RecordList struct {
	Items []w.ProjectRecord `json:"items"`
}

type InboxList struct {
	Items []w.InboxItem `json:"items"`
}

type TaskActionInput struct {
	Action    string `json:"action"`
	RequestID string `json:"request_id"`
}

type TaskBudgetInput struct {
	Policy w.BudgetPolicy `json:"policy"`
}

type ExecutionPolicyInput struct {
	Mode            string `json:"mode"`
	SourceMessageID string `json:"source_message_id"`
}

type BudgetPolicyInput struct {
	Override *w.BudgetPolicyOverride `json:"override"`
}

type GrantList struct {
	Items []w.Grant `json:"items"`
}

type RevokeGrantInput struct {
	SourceMessageID string `json:"source_message_id"`
}

func (s *Server) projectOverview(rw http.ResponseWriter, r *http.Request) {
	value, err := s.projects.ProjectOverview(r.Context(), r.PathValue("id"))
	respond(rw, r, value, err)
}

func (s *Server) projectTasks(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Tasks(r.Context(), r.PathValue("id"))
	respond(rw, r, TaskList{Items: items}, err)
}

func (s *Server) createProjectTask(rw http.ResponseWriter, r *http.Request) {
	var in project.CreateTaskInput
	if !decode(rw, r, &in) {
		return
	}
	in.WorkspaceID = r.PathValue("id")
	value, err := s.projects.CreateTask(r.Context(), in)
	respond(rw, r, value, err)
}

func (s *Server) projectTask(rw http.ResponseWriter, r *http.Request) {
	value, err := s.projects.Task(r.Context(), r.PathValue("id"))
	respond(rw, r, value, err)
}

func (s *Server) reviseProjectTask(rw http.ResponseWriter, r *http.Request) {
	expected, err := project.ParseRevision(r.Header.Get("If-Match"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var in project.ReviseTaskInput
	if !decode(rw, r, &in) {
		return
	}
	value, err := s.projects.ReviseTask(r.Context(), r.PathValue("id"), expected, in)
	respond(rw, r, value, err)
}

func (s *Server) projectTaskRevisions(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Revisions(r.Context(), r.PathValue("id"))
	respond(rw, r, RevisionList{Items: items}, err)
}

func (s *Server) addTaskDependency(rw http.ResponseWriter, r *http.Request) {
	var in w.TaskDependency
	if !decode(rw, r, &in) {
		return
	}
	in.ConsumerTaskID = r.PathValue("id")
	value, err := s.projects.AddDependency(r.Context(), in)
	respond(rw, r, value, err)
}

func (s *Server) taskDependencies(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Dependencies(r.Context(), r.PathValue("id"))
	respond(rw, r, DependencyList{Items: items}, err)
}

func (s *Server) completeProjectTask(rw http.ResponseWriter, r *http.Request) {
	var in project.RecordCompletionInput
	if !decode(rw, r, &in) {
		return
	}
	value, err := s.projects.RecordCompletion(r.Context(), r.PathValue("id"), in)
	respond(rw, r, value, err)
}

func (s *Server) projectTaskAction(rw http.ResponseWriter, r *http.Request) {
	var in TaskActionInput
	if !decode(rw, r, &in) {
		return
	}
	value, replayed, err := s.projects.TaskActionWithReplay(r.Context(), r.PathValue("id"), in.Action, in.RequestID)
	if err == nil && !replayed && (in.Action == "pause" || in.Action == "cancel") && s.engine != nil {
		// Replay of an already committed action must not stop newer work. A new
		// pause/cancel always advances the root's control epoch.
		if in.Action == "pause" {
			err = s.engine.PauseTask(context.WithoutCancel(r.Context()), r.PathValue("id"))
		} else {
			err = s.engine.CancelTask(context.WithoutCancel(r.Context()), r.PathValue("id"))
		}
	}
	respond(rw, r, value, err)
}

func (s *Server) projectTaskBudget(rw http.ResponseWriter, r *http.Request) {
	view, err := s.projects.Task(r.Context(), r.PathValue("id"))
	respond(rw, r, view.Budget, err)
}

func (s *Server) adjustProjectTaskBudget(rw http.ResponseWriter, r *http.Request) {
	expected, err := project.ParseRevision(r.Header.Get("If-Match"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var in TaskBudgetInput
	if !decode(rw, r, &in) {
		return
	}
	value, err := s.projects.AdjustBudget(r.Context(), r.PathValue("id"), expected, in.Policy)
	respond(rw, r, value, err)
}

func (s *Server) projectRecords(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Records(r.Context(), r.PathValue("id"))
	respond(rw, r, RecordList{Items: items}, err)
}

func (s *Server) saveProjectRecord(rw http.ResponseWriter, r *http.Request) {
	var in w.ProjectRecord
	if !decode(rw, r, &in) {
		return
	}
	in.WorkspaceID = r.PathValue("id")
	value, err := s.projects.SaveProjectRecord(r.Context(), in)
	respond(rw, r, value, err)
}

func (s *Server) setProjectBudgetPolicy(rw http.ResponseWriter, r *http.Request) {
	expected, err := project.ParseRevision(r.Header.Get("If-Match"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var in BudgetPolicyInput
	if !decode(rw, r, &in) {
		return
	}
	value, err := s.projects.SetBudgetOverride(r.Context(), r.PathValue("id"), in.Override, expected)
	respond(rw, r, value, err)
}

func (s *Server) setProjectExecutionPolicy(rw http.ResponseWriter, r *http.Request) {
	expected, err := project.ParseRevision(r.Header.Get("If-Match"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var in ExecutionPolicyInput
	if !decode(rw, r, &in) {
		return
	}
	var previous w.Workspace
	err = s.store.Read(r.Context(), func(tx *store.Tx) error {
		var readErr error
		previous, readErr = tx.Workspace(r.PathValue("id"))
		return readErr
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	value, err := s.projects.SetExecutionMode(r.Context(), r.PathValue("id"), in.Mode, in.SourceMessageID, expected)
	if err == nil && previous.ExecutionMode != in.Mode && s.engine != nil {
		// Only an actual mode change stops old work. Re-saving the current mode
		// must not cancel a newer run or reset its native session.
		err = s.engine.CancelWorkspace(context.WithoutCancel(r.Context()), r.PathValue("id"))
	}
	respond(rw, r, value, err)
}

func (s *Server) projectGrants(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Grants(r.Context(), r.PathValue("id"))
	respond(rw, r, GrantList{Items: items}, err)
}

func (s *Server) revokeProjectGrant(rw http.ResponseWriter, r *http.Request) {
	var in RevokeGrantInput
	if !decode(rw, r, &in) {
		return
	}
	workspaceID, memberID := r.PathValue("id"), r.PathValue("memberID")
	if _, err := s.projects.BeginRevocation(r.Context(), workspaceID, memberID, in.SourceMessageID); err != nil {
		respond(rw, r, nil, err)
		return
	}
	// Revocation is committed before process cancellation. Browser cancellation
	// must not leave an authorized native invocation running.
	stopErr := s.engine.CancelMember(context.WithoutCancel(r.Context()), workspaceID, memberID)
	stopUncertain, checkErr := s.engine.RevocationStopUncertain(context.WithoutCancel(r.Context()), workspaceID, memberID)
	stopErr = errors.Join(stopErr, checkErr)
	grant, err := s.projects.FinishRevocation(context.WithoutCancel(r.Context()), workspaceID, memberID, stopErr != nil || stopUncertain)
	if err == nil && stopErr != nil && grant.Status != "revocation_uncertain" {
		err = stopErr
	}
	respond(rw, r, grant, err)
}

func (s *Server) submitTaskInbox(rw http.ResponseWriter, r *http.Request) {
	var in project.SubmitInboxInput
	if !decode(rw, r, &in) {
		return
	}
	value, err := s.projects.SubmitInbox(r.Context(), r.PathValue("id"), in)
	respond(rw, r, value, err)
}

func (s *Server) taskInbox(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Inbox(r.Context(), r.PathValue("id"))
	respond(rw, r, InboxList{Items: items}, err)
}

type QuestionList struct {
	Items []w.Question `json:"items"`
}

type QuestionAnswerInput struct {
	Answer    string `json:"answer"`
	AnswerRef string `json:"answer_ref"`
}

type TaskMessageInput struct {
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
}

type CollaborationMessageList struct {
	Items []w.CollaborationMessage `json:"items"`
}

type UserDelegationInput struct {
	OwnerMemberID string   `json:"owner_member_id"`
	Goal          string   `json:"goal"`
	RequestID     string   `json:"request_id"`
	ContextRefs   []string `json:"context_refs,omitempty"`
}

type HandoffInput struct {
	NewOwnerID            string `json:"new_owner_id"`
	RequestID             string `json:"request_id"`
	ExpectedOwnerRevision int    `json:"expected_owner_revision"`
}

type WakeupList struct {
	Items []w.Wakeup `json:"items"`
}

type WakeEventInput struct {
	WakeSource  string `json:"wake_source"`
	EventID     string `json:"event_id"`
	Observation string `json:"observation"`
	EvidenceRef string `json:"evidence_ref"`
}

type ClaimList struct {
	Items []w.Claim `json:"items"`
}

type DatasetList struct {
	Items []w.DatasetManifest `json:"items"`
}

type MemoryList struct {
	Items []w.MemoryRecord `json:"items"`
}

func (s *Server) taskQuestions(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Questions(r.Context(), r.PathValue("id"))
	respond(rw, r, QuestionList{Items: items}, err)
}

func (s *Server) answerTaskQuestion(rw http.ResponseWriter, r *http.Request) {
	expected, err := project.ParseRevision(r.Header.Get("If-Match"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var in QuestionAnswerInput
	if !decode(rw, r, &in) {
		return
	}
	value, err := s.projects.AnswerQuestion(r.Context(), r.PathValue("id"), r.PathValue("questionID"), in.Answer, in.AnswerRef, expected)
	respond(rw, r, value, err)
}

func (s *Server) taskMessages(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.CollaborationMessages(r.Context(), r.PathValue("id"))
	respond(rw, r, CollaborationMessageList{Items: items}, err)
}

func (s *Server) sendTaskMessage(rw http.ResponseWriter, r *http.Request) {
	var in TaskMessageInput
	if !decode(rw, r, &in) {
		return
	}
	// The path and authenticated user are the only browser identity inputs.
	// There is deliberately no sender_attempt_id in this request schema.
	value, err := s.projects.SendTaskMessage(r.Context(), project.SendTaskMessageInput{
		SenderTaskID: r.PathValue("id"), TargetTaskID: in.TargetTaskID,
		Kind: in.Kind, Body: in.Body, RequestID: in.RequestID, ReplyTo: in.ReplyTo,
		CorrelationID: in.CorrelationID, ContextRefs: in.ContextRefs, Summary: in.Summary,
		RelatedTaskRevision: in.RelatedTaskRevision, ExpiresAt: in.ExpiresAt, RequiresResponse: in.RequiresResponse,
	})
	respond(rw, r, value, err)
}

func (s *Server) collaborationMessage(rw http.ResponseWriter, r *http.Request) {
	value, err := s.projects.CollaborationMessage(r.Context(), r.PathValue("id"))
	respond(rw, r, value, err)
}

func (s *Server) delegateTask(rw http.ResponseWriter, r *http.Request) {
	var in UserDelegationInput
	if !decode(rw, r, &in) {
		return
	}
	value, err := s.projects.DelegateTaskFromUser(r.Context(), project.DelegateTaskInput{
		ParentTaskID: r.PathValue("id"), OwnerMemberID: in.OwnerMemberID,
		Goal: in.Goal, RequestID: in.RequestID, ContextRefs: in.ContextRefs,
	})
	respond(rw, r, value, err)
}

func (s *Server) handoffTask(rw http.ResponseWriter, r *http.Request) {
	var in HandoffInput
	if !decode(rw, r, &in) {
		return
	}
	if s.engine == nil {
		respond(rw, r, nil, &RequestError{Status: http.StatusServiceUnavailable, Code: "capability_unavailable", Message: "task transfer requires the execution engine"})
		return
	}
	value, err := s.engine.TransferTask(r.Context(), r.PathValue("id"), in.NewOwnerID, in.RequestID, in.ExpectedOwnerRevision)
	respond(rw, r, value, err)
}

func (s *Server) taskWakeups(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Wakeups(r.Context(), r.PathValue("id"))
	respond(rw, r, WakeupList{Items: items}, err)
}

func (s *Server) observeWakeEvent(rw http.ResponseWriter, r *http.Request) {
	var in WakeEventInput
	if !decode(rw, r, &in) {
		return
	}
	value, err := s.projects.ObserveUserWakeEvent(r.Context(), in.WakeSource, in.EventID, in.Observation, in.EvidenceRef)
	respond(rw, r, value, err)
}

func (s *Server) taskClaims(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Claims(r.Context(), r.PathValue("id"))
	respond(rw, r, ClaimList{Items: items}, err)
}

func (s *Server) saveTaskClaim(rw http.ResponseWriter, r *http.Request) {
	var in w.Claim
	if !decode(rw, r, &in) {
		return
	}
	in.WorkspaceID = ""
	in.TaskID = r.PathValue("id")
	in.ProducerAttemptID = ""
	// A browser can attest as the authenticated user, never as a member Attempt.
	task, err := s.projects.Task(r.Context(), in.TaskID)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	in.WorkspaceID = task.Task.WorkspaceID
	for i := range in.Evidence {
		in.Evidence[i].Reviewer = "user"
	}
	value, err := s.projects.SaveClaim(r.Context(), in)
	respond(rw, r, value, err)
}

func (s *Server) taskDatasets(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Datasets(r.Context(), r.PathValue("id"))
	respond(rw, r, DatasetList{Items: items}, err)
}

func (s *Server) saveTaskDataset(rw http.ResponseWriter, r *http.Request) {
	var in w.DatasetManifest
	if !decode(rw, r, &in) {
		return
	}
	in.TaskID = r.PathValue("id")
	in.ProducerAttemptID = ""
	task, err := s.projects.Task(r.Context(), in.TaskID)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	in.WorkspaceID = task.Task.WorkspaceID
	value, err := s.projects.SaveDataset(r.Context(), in)
	respond(rw, r, value, err)
}

func (s *Server) projectMemories(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.Memories(r.Context(), r.PathValue("id"))
	respond(rw, r, MemoryList{Items: items}, err)
}

func (s *Server) saveProjectMemory(rw http.ResponseWriter, r *http.Request) {
	var in w.MemoryRecord
	if !decode(rw, r, &in) {
		return
	}
	in.WorkspaceID = r.PathValue("id")
	in.ProducerAttemptID = ""
	in.AcceptedBy = "user"
	value, err := s.projects.SaveMemory(r.Context(), in)
	respond(rw, r, value, err)
}

func (s *Server) execution(rw http.ResponseWriter, r *http.Request) {
	value, err := s.projects.Execution(r.Context(), r.PathValue("id"))
	respond(rw, r, value, err)
}

func (s *Server) effect(rw http.ResponseWriter, r *http.Request) {
	value, err := s.projects.Effect(r.Context(), r.PathValue("id"))
	respond(rw, r, value, err)
}
