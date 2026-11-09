package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (s *Server) threads(rw http.ResponseWriter, r *http.Request) {
	var threads []w.Thread
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Workspace(r.PathValue("id")); err != nil {
			return err
		}
		var err error
		threads, err = tx.Threads(r.PathValue("id"))
		return err
	})
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	page, err := paginate(r, threads)
	respond(rw, r, page, err)
}

func validateThreadTitle(title string) error {
	if strings.TrimSpace(title) == "" || len([]rune(title)) > 150 {
		return errors.New("会话标题无效")
	}
	return nil
}

func validateThread(th w.Thread, agents []w.Agent) error {
	if err := validateThreadTitle(th.Title); err != nil {
		return err
	}
	if len(th.AgentIDs) < 1 || len(th.AgentIDs) > 10 {
		return errors.New("会话成员数量无效")
	}
	if !slices.Contains(th.AgentIDs, th.CoordinatorID) {
		return errors.New("协调者必须是会话成员")
	}
	seen := map[string]bool{}
	for _, id := range th.AgentIDs {
		if seen[id] || !slices.ContainsFunc(agents, func(a w.Agent) bool { return a.ID == id && !a.Disabled }) {
			return errors.New("成员重复或不可用")
		}
		seen[id] = true
	}
	if !slices.Contains([]string{"free", "discussion", "research", "code_analysis", "engineering"}, th.Workflow) {
		return errors.New("不支持的默认流程")
	}
	return nil
}

func (s *Server) createThread(rw http.ResponseWriter, r *http.Request) {
	var th w.Thread
	if !decode(rw, r, &th) {
		return
	}
	th.ID = w.NewID()
	th.WorkspaceID = r.PathValue("id")
	th.Revision = 1
	th.UpdatedAt = w.Now()
	th.DeletedAt, th.PurgeAt = "", ""
	th.LastRunID, th.LastRunStatus, th.ActiveRunID, th.ActiveRunStatus = "", "", "", ""
	if th.Workflow == "" {
		th.Workflow = "free"
	}
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		agents, err := tx.Agents(th.WorkspaceID)
		if err != nil {
			return err
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		applyThreadDefaults(&th, agents, settings)
		if err := validateThread(th, agents); err != nil {
			return err
		}
		return tx.SaveThread(th)
	})
	respond(rw, r, th, err)
}

func (s *Server) updateThread(rw http.ResponseWriter, r *http.Request) {
	var in ThreadUpdate
	if !decode(rw, r, &in) {
		return
	}
	var th w.Thread
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		var err error
		th, err = tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := revision(r, th.Revision); err != nil {
			return err
		}
		th.Title = in.Title
		if in.AgentIDs == nil && in.CoordinatorID == nil && in.Workflow == nil {
			// A title-only edit must still work after a member is disabled.
			if err := validateThreadTitle(th.Title); err != nil {
				return err
			}
		} else {
			if in.AgentIDs != nil {
				th.AgentIDs = *in.AgentIDs
			}
			if in.CoordinatorID != nil {
				th.CoordinatorID = *in.CoordinatorID
			}
			if in.Workflow != nil {
				th.Workflow = *in.Workflow
			}
			agents, err := tx.Agents(th.WorkspaceID)
			if err != nil {
				return err
			}
			if err := validateThread(th, agents); err != nil {
				return err
			}
		}
		th.Revision++
		th.UpdatedAt = w.Now()
		return tx.SaveThread(th)
	})
	respond(rw, r, th, err)
}

// backfillMessageModels exposes the requested model for legacy messages that
// predate per-message attempt snapshots. The run roster is frozen, so this
// never guesses a retry attempt and never claims the model was resolved.
func backfillMessageModels(messages []w.Message, runs []w.Run) {
	type rosterEntry struct {
		count int
		model string
	}
	rosters := make(map[string]map[string]rosterEntry, len(runs))
	for _, run := range runs {
		roster := make(map[string]rosterEntry, len(run.Agents))
		for _, agent := range run.Agents {
			entry := roster[agent.ID]
			entry.count++
			entry.model = agent.Model
			roster[agent.ID] = entry
		}
		rosters[run.ID] = roster
	}
	for i := range messages {
		message := &messages[i]
		if message.Role == "user" || message.Model != "" || message.ResolvedModel != "" {
			continue
		}
		if entry := rosters[message.RunID][message.AgentID]; entry.count == 1 {
			message.Model = entry.model
		}
	}
}

func (s *Server) thread(rw http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r, store.DefaultMessagePage, store.MaxMessagePage)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var view ThreadView
	err = s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		view.Thread, err = tx.Thread(r.PathValue("id"))
		if err != nil {
			return err
		}
		page, err := tx.PagedMessages(view.Thread.ID, r.URL.Query().Get("cursor"), limit)
		if err != nil {
			return err
		}
		view.Messages = Page[w.Message]{Items: page.Items, NextCursor: page.NextCursor}
		view.Runs, err = tx.Runs(view.Thread.ID)
		if err != nil {
			return err
		}
		backfillMessageModels(view.Messages.Items, view.Runs)
		view.Sources, err = tx.SourceMetadata(view.Thread.ID)
		return err
	})
	if errors.Is(err, store.ErrInvalidCursor) {
		writeError(rw, r, 400, "invalid_cursor", "分页 cursor 无效")
		return
	}
	respond(rw, r, view, err)
}
