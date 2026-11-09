package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (s *Server) workspaces(rw http.ResponseWriter, r *http.Request) {
	var values []w.Workspace
	err := s.store.Read(r.Context(), func(tx *store.Tx) error { var err error; values, err = tx.Workspaces(); return err })
	respond(rw, r, map[string]any{"items": values}, err)
}

func (s *Server) validateRoot(root string) (string, error) {
	return tools.ResolveSourceRoot(root, s.config.Roots)
}

func (s *Server) createWorkspace(rw http.ResponseWriter, r *http.Request) {
	var in WorkspaceInput
	if !decode(rw, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 100 || len(in.Description) > 4000 {
		respond(rw, r, nil, errors.New("工作空间名称或说明无效"))
		return
	}
	root, err := s.validateRoot(in.Root)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	if _, err := skills.Catalogue(in.SkillIDs); err != nil {
		respond(rw, r, nil, err)
		return
	}
	ws := w.Workspace{ID: w.NewID(), Name: in.Name, Description: in.Description, Root: root, SkillIDs: in.SkillIDs, Revision: 1, CreatedAt: w.Now(), ExecutionMode: w.ExecutionControlled}
	err = s.store.Write(r.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		return s.initializeMembers(tx, ws.ID)
	})
	respond(rw, r, ws, err)
}

func (s *Server) updateWorkspace(rw http.ResponseWriter, r *http.Request) {
	var in WorkspaceInput
	if !decode(rw, r, &in) {
		return
	}
	var ws w.Workspace
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		var err error
		ws, err = tx.Workspace(r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := revision(r, ws.Revision); err != nil {
			return err
		}
		if strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 100 || len(in.Description) > 4000 {
			return errors.New("工作空间名称或说明无效")
		}
		root, err := s.validateRoot(in.Root)
		if err != nil {
			return err
		}
		if ws.Root != root {
			active, err := tx.Active()
			if err != nil {
				return err
			}
			if slices.ContainsFunc(active, func(run w.Run) bool { return run.WorkspaceID == ws.ID }) {
				return w.ErrConflict
			}
		}
		ws.Name = in.Name
		ws.Description = in.Description
		ws.Root = root
		if _, err := skills.Catalogue(in.SkillIDs); err != nil {
			return err
		}
		ws.SkillIDs = in.SkillIDs
		ws.Revision++
		return tx.SaveWorkspace(ws)
	})
	respond(rw, r, ws, err)
}

func (s *Server) deleteWorkspace(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.engine != nil {
		var list []w.Thread
		if err := s.store.Read(r.Context(), func(tx *store.Tx) error {
			var err error
			list, err = tx.ThreadsAny(id)
			return err
		}); err != nil {
			respond(rw, r, nil, err)
			return
		}
		for _, thread := range list {
			if live, liveErr := s.ownedProcessAlive(r, thread.ID); liveErr != nil {
				respond(rw, r, nil, liveErr)
				return
			} else if live {
				respond(rw, r, nil, errOwnedProcess)
				return
			}
		}
	}
	threads := map[string]bool{}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Workspace(id); err != nil {
			return err
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		if slices.ContainsFunc(active, func(run w.Run) bool { return run.WorkspaceID == id }) {
			return errRunActive
		}
		list, err := tx.ThreadsAny(id)
		if err != nil {
			return err
		}
		for _, th := range list {
			threads[th.ID] = true
		}
		return tx.DeleteWorkspace(id)
	})
	if err == nil {
		s.dropProposals(func(p proposal) bool { return threads[p.ThreadID] })
	}
	respond(rw, r, DeleteReply{ID: id}, err)
}

func (s *Server) dropProposals(match func(proposal) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, p := range s.proposals {
		if match(p) {
			delete(s.proposals, key)
		}
	}
}
