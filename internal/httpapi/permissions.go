package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// PermissionRequestList keeps pending requests first, then bounded history.
type PermissionRequestList struct {
	Items []w.ToolPermissionRequest `json:"items"`
}

// ConversationGrantList returns a thread's conversation-scoped grants.
type ConversationGrantList struct {
	Items []w.ConversationGrant `json:"items"`
}

// PermissionDecisionInput is the fixed three-way decision. The request scope is
// never taken from this body.
type PermissionDecisionInput struct {
	Decision  string `json:"decision"`
	RequestID string `json:"request_id"`
}

func (s *Server) threadPermissionRequests(rw http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeError(rw, r, 400, "invalid_request", "limit must be an integer from 1 to 200")
			return
		}
		limit = value
	}
	items, err := s.projects.ThreadPermissionRequests(r.Context(), r.PathValue("id"), limit)
	respond(rw, r, PermissionRequestList{Items: items}, err)
}

func (s *Server) decidePermissionRequest(rw http.ResponseWriter, r *http.Request) {
	var in PermissionDecisionInput
	if !decode(rw, r, &in) {
		return
	}
	expected, err := project.ParseRevision(r.Header.Get("If-Match"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	request, err := s.projects.DecidePermissionRequest(r.Context(), r.PathValue("id"), expected, project.DecisionInput{Decision: in.Decision, RequestID: in.RequestID})
	respond(rw, r, request, err)
}

func (s *Server) threadPermissionGrants(rw http.ResponseWriter, r *http.Request) {
	items, err := s.projects.ThreadConversationGrants(r.Context(), r.PathValue("id"))
	respond(rw, r, ConversationGrantList{Items: items}, err)
}

func (s *Server) revokePermissionGrant(rw http.ResponseWriter, r *http.Request) {
	grant, err := s.projects.RevokeConversationGrant(r.Context(), r.PathValue("id"))
	if err == nil && grant.AffectedActiveRuns > 0 && s.engine != nil {
		// Revocation is committed; the running process is stopped next and any
		// uncertainty is surfaced rather than reported as a clean stop.
		stopErr := s.engine.CancelThread(context.WithoutCancel(r.Context()), grant.ThreadID)
		stopUncertain, verifyErr := s.engine.ThreadStopUncertain(context.WithoutCancel(r.Context()), grant.ThreadID)
		if stopErr != nil || verifyErr != nil || stopUncertain {
			writeError(rw, r, 409, "revocation_uncertain", "授权已撤销；该会话仍有执行未能确认安全停止，请核实")
			return
		}
	}
	respond(rw, r, grant, err)
}

func (s *Server) projectTaskDeliveries(rw http.ResponseWriter, r *http.Request) {
	value, err := s.projects.Deliveries(r.Context(), r.PathValue("id"))
	respond(rw, r, value, err)
}

// deliveryTaskWorkspace is used by tests and callers that need the task's
// workspace without loading a full thread.
func (s *Server) deliveryTaskWorkspace(r *http.Request) (w.Task, error) {
	var task w.Task
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		task, err = tx.Task(r.PathValue("id"))
		return err
	})
	return task, err
}
