package httpapi

import (
	"context"
	"errors"
	"log/slog"
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
	if _, err := skills.CatalogueFor(in.Root, in.SkillIDs); err != nil {
		respond(rw, r, nil, err)
		return
	}
	ws := w.Workspace{ID: w.NewID(), Name: in.Name, Description: in.Description, Root: root, SkillIDs: in.SkillIDs, Revision: 1, CreatedAt: w.Now(), ExecutionMode: w.ExecutionWorkspaceAutonomous}
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
	var sealedThreads []string
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
			// An unapplied candidate belongs to the previous repository. Refusing
			// the rebind keeps the user from writing that work into a new project.
			allCandidates, err := tx.Executions(ws.ID)
			if err != nil {
				return err
			}
			for _, candidate := range allCandidates {
				if candidate.Kind == "verify_dir" {
					continue
				}
				if candidate.Status == "active" || candidate.Status == "completed" || candidate.Status == "uncertain" {
					return &RequestError{409, "candidate_pending", "该工作空间仍有未应用或未丢弃的候选变更，请先在对应会话中选择应用或丢弃后再更换项目目录"}
				}
			}
			list, err := tx.ThreadsAny(ws.ID)
			if err != nil {
				return err
			}
			for _, thread := range list {
				sealedThreads = append(sealedThreads, thread.ID)
			}
		}
		ws.Name = in.Name
		ws.Description = in.Description
		ws.Root = root
		if _, err := skills.CatalogueFor(in.Root, in.SkillIDs); err != nil {
			return err
		}
		ws.SkillIDs = in.SkillIDs
		ws.Revision++
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		// Every native session bound to the previous root is history: the next
		// turn must start a fresh one in the new project instead of continuing
		// with cross-repository memory.
		for _, threadID := range sealedThreads {
			if err := tx.SealThreadSessions(threadID); err != nil {
				return err
			}
		}
		return nil
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
	var (
		candidates []w.WorkspaceExecution
		ioScopes   []string
		roots      []string
	)
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		ws, err := tx.Workspace(id)
		if err != nil {
			return err
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		if slices.ContainsFunc(active, func(run w.Run) bool { return run.WorkspaceID == id }) {
			return errRunActive
		}
		candidates, err = tx.Executions(id)
		if err != nil {
			return err
		}
		seenRoot := map[string]bool{}
		addRoot := func(root string) {
			if root != "" && !seenRoot[root] {
				seenRoot[root] = true
				roots = append(roots, root)
			}
		}
		addRoot(ws.Root)
		for _, item := range candidates {
			addRoot(item.CanonicalRoot)
		}
		seenScope := map[string]bool{}
		addScope := func(scope string) {
			if scope != "" && !seenScope[scope] {
				seenScope[scope] = true
				ioScopes = append(ioScopes, scope)
			}
		}
		list, err := tx.ThreadsAny(id)
		if err != nil {
			return err
		}
		for _, th := range list {
			addScope(th.ID)
			addScope(th.TaskID)
		}
		tasks, err := tx.Tasks(id)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			addScope(task.ID)
		}
		return tx.DeleteWorkspace(id)
	})
	if err == nil && s.engine != nil {
		if releaseErr := s.engine.ReleaseDeletedTaskExecutions(context.WithoutCancel(r.Context()), candidates, ioScopes, roots); releaseErr != nil {
			slog.Error("release workspace executions failed", "workspace", id, "error", releaseErr)
		}
	}
	respond(rw, r, DeleteReply{ID: id}, err)
}
