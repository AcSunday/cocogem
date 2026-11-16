package httpapi

import (
	"errors"
	"net/http"

	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
)

// CandidateList is the unapplied candidate directories of one conversation.
type CandidateList struct {
	Items []workflow.CandidateView `json:"items"`
}

// threadCandidates lists unapplied candidate directories and their verification.
func (s *Server) threadCandidates(rw http.ResponseWriter, r *http.Request) {
	if err := s.visibleThreadRead(r, r.PathValue("id")); err != nil {
		respond(rw, r, nil, err)
		return
	}
	items, err := s.engine.Candidates(r.Context(), r.PathValue("id"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	respond(rw, r, CandidateList{Items: items}, nil)
}

// applyCandidate writes one candidate back to the bound project, then reclaims
// its worktree so the next write derives a fresh one.
func (s *Server) applyCandidate(rw http.ResponseWriter, r *http.Request) {
	if err := s.visibleThreadRead(r, r.PathValue("id")); err != nil {
		respond(rw, r, nil, err)
		return
	}
	result, err := s.engine.ApplyCandidate(r.Context(), r.PathValue("id"), r.PathValue("executionID"))
	if err != nil {
		if errors.Is(err, execution.ErrApplyConflict) {
			writeError(rw, r, 409, "candidate_conflict", "项目目录中的相关文件已被外部修改，请先核对再应用："+err.Error())
			return
		}
		respond(rw, r, nil, err)
		return
	}
	respond(rw, r, result, nil)
}

// discardCandidate destroys one candidate without touching the bound project.
func (s *Server) discardCandidate(rw http.ResponseWriter, r *http.Request) {
	if err := s.visibleThreadRead(r, r.PathValue("id")); err != nil {
		respond(rw, r, nil, err)
		return
	}
	result, err := s.engine.DiscardCandidate(r.Context(), r.PathValue("id"), r.PathValue("executionID"))
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	respond(rw, r, result, nil)
}

// visibleThreadRead rejects every child API of a soft-deleted conversation.
func (s *Server) visibleThreadRead(r *http.Request, id string) error {
	return s.store.Read(r.Context(), func(tx *store.Tx) error {
		_, err := tx.Thread(id)
		return err
	})
}
