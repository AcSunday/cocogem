package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (s *Server) settings(rw http.ResponseWriter, r *http.Request) {
	var value w.Settings
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		value, err = tx.Settings()
		return err
	})
	respond(rw, r, value, err)
}

// updateSettings applies a field-merge PATCH through the project service so the
// settings CAS, the permission epoch change, request expiry and conversation
// grant revocation commit in one transaction. Saving collaboration never
// overwrites the permission, and saving the permission never clears the
// collaboration defaults or retention period.
func (s *Server) updateSettings(rw http.ResponseWriter, r *http.Request) {
	var in SettingsInput
	if !decode(rw, r, &in) {
		return
	}
	var current w.Settings
	if err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		respond(rw, r, nil, err)
		return
	}
	if err := revision(r, current.Revision); err != nil {
		respond(rw, r, nil, err)
		return
	}
	change, err := s.projects.UpdateSettings(r.Context(), current.Revision, in.patch())
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	if in.RetentionDays != nil {
		if err := s.projects.RescheduleTrash(r.Context(), change.Settings.RetentionDays); err != nil {
			respond(rw, r, nil, err)
			return
		}
	}
	if change.PermissionMoved && s.engine != nil {
		// A real permission boundary change stops in-flight executions that
		// cannot be narrowed inside their own process. Re-saving the current
		// mode never cancels newer work. The settings change is already
		// committed, so a failed or uncertain stop is reported instead of being
		// hidden behind a normal 200.
		var workspaces []w.Workspace
		if err := s.store.Read(r.Context(), func(tx *store.Tx) error {
			var err error
			workspaces, err = tx.Workspaces()
			return err
		}); err != nil {
			writeError(rw, r, 409, "revocation_uncertain", "权限已保存，但无法读取受影响项目以停止在途执行；请核实运行中的任务")
			return
		}
		var stopErr error
		for _, workspace := range workspaces {
			stopErr = errors.Join(stopErr, s.engine.CancelWorkspace(context.WithoutCancel(r.Context()), workspace.ID))
		}
		if stopErr != nil {
			writeError(rw, r, 409, "revocation_uncertain", "权限已保存；部分在途执行未能确认安全停止，请核实")
			return
		}
	}
	respond(rw, r, change.Settings, nil)
}

// applyThreadDefaults fills an omitted coordinator from the global default and
// then from the selected workspace members. Explicit choices win unchanged, and
// defaults never append a member to a roster the user selected explicitly.
func applyThreadDefaults(th *w.Thread, agents []w.Agent, settings w.Settings) {
	enabled := map[string]bool{}
	for _, agent := range agents {
		if !agent.Disabled {
			enabled[agent.ID] = true
		}
	}
	if len(th.AgentIDs) == 0 {
		if enabled[settings.DefaultCoordinatorID] {
			th.AgentIDs = append(th.AgentIDs, settings.DefaultCoordinatorID)
		}
		for _, agent := range agents {
			if !agent.Disabled && !slices.Contains(th.AgentIDs, agent.ID) {
				th.AgentIDs = append(th.AgentIDs, agent.ID)
			}
		}
		if len(th.AgentIDs) > 10 {
			th.AgentIDs = th.AgentIDs[:10]
		}
	}
	if th.CoordinatorID != "" {
		return
	}
	if slices.Contains(th.AgentIDs, settings.DefaultCoordinatorID) && enabled[settings.DefaultCoordinatorID] {
		th.CoordinatorID = settings.DefaultCoordinatorID
		return
	}
	if len(th.AgentIDs) > 0 {
		// Preserve the pre-settings fallback: the third selected member, or the
		// first enabled member when fewer are available.
		preferred := th.AgentIDs[min(2, len(th.AgentIDs)-1)]
		if enabled[preferred] {
			th.CoordinatorID = preferred
			return
		}
		for _, id := range th.AgentIDs {
			if enabled[id] {
				th.CoordinatorID = id
				break
			}
		}
	}
}
