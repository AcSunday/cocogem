package project

import (
	"errors"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func grantID(workspaceID, memberID string) string { return "grant-" + workspaceID + "-" + memberID }

func modeGrant(ws w.Workspace, memberID, mode, source string, previous w.Grant, now string) w.Grant {
	grant := w.Grant{ID: grantID(ws.ID, memberID), WorkspaceID: ws.ID, SubjectMemberID: memberID, Mode: mode,
		Actions: w.ModeActions(mode), Resources: []string{"workspace:" + ws.ID}, ExternalActions: mode == w.ExecutionFullAccess,
		SourceMessageID: source, Revision: 1, Epoch: 1, Status: "active", CreatedAt: now}
	if previous.ID != "" {
		grant.Revision = previous.Revision + 1
		grant.Epoch = previous.Epoch + 1
		grant.CreatedAt = previous.CreatedAt
		grant.ExpiresAt = previous.ExpiresAt
		grant.Resources = nonNil(previous.Resources)
		if previous.Status != "active" {
			grant.Status = previous.Status
			grant.RevokedAt = previous.RevokedAt
		}
	}
	return grant
}

func (s *Service) setModeGrants(tx *store.Tx, ws w.Workspace, mode, source string) error {
	members, err := tx.Agents(ws.ID)
	if err != nil {
		return err
	}
	for _, member := range members {
		previous, err := tx.Grant(grantID(ws.ID, member.ID))
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := tx.SaveGrant(modeGrant(ws, member.ID, mode, source, previous, s.now())); err != nil {
			return err
		}
	}
	return nil
}

// EnsureRunAuthorization freezes the task/mode action upper bound and a digest
// of current grant epochs. Existing revoked grants are never revived by a Run.
func (s *Service) EnsureRunAuthorization(tx *store.Tx, run *w.Run, source string) error {
	if run.TaskID == "" {
		return nil
	}
	task, err := tx.Task(run.TaskID)
	if err != nil {
		return err
	}
	if task.TransferRequestID != "" {
		return errorf("authority_revoked", "task admission is frozen while owner transfer is pending")
	}
	revision, err := tx.TaskRevision(task.ID, run.TaskRevision)
	if err != nil {
		return err
	}
	ws, err := tx.Workspace(task.WorkspaceID)
	if err != nil {
		return err
	}
	mode := ws.ExecutionMode
	if mode == "" {
		mode = w.ExecutionControlled
	}
	run.RequestedExecutionMode = mode
	run.EffectiveExecutionMode = mode
	run.EnforcedCapabilities = []string{"host_grant_intersection", "scoped_mcp", "candidate_tree_lease"}
	run.UnenforcedConstraints = nil
	if mode == w.ExecutionProjectAutonomous {
		// No real OS isolation probe has passed yet. Keep native shell and writes
		// closed; this is an honest controlled execution fallback, not an upgrade.
		run.EffectiveExecutionMode = w.ExecutionControlled
		run.UnenforcedConstraints = w.NativeIsolationUnknowns()
	}
	if mode == w.ExecutionFullAccess {
		run.EnforcedCapabilities = []string{"host_grant_intersection", "scoped_mcp", "process_group_observation"}
		run.UnenforcedConstraints = w.NativeIsolationUnknowns()
	}
	upper := w.AuthorizedActions(w.ModeActions(run.EffectiveExecutionMode), revision.AllowedActions, revision.AllowedActions, revision.AllowedActions, revision.AllowedActions)
	run.GrantActions = upper
	type grantState struct {
		ID       string   `json:"id"`
		Epoch    int      `json:"epoch"`
		Revision int      `json:"revision"`
		Actions  []string `json:"actions"`
	}
	states := []grantState{}
	for _, member := range run.Agents {
		id := grantID(ws.ID, member.ID)
		grant, err := tx.Grant(id)
		if errors.Is(err, store.ErrNotFound) {
			grant = modeGrant(ws, member.ID, mode, source, w.Grant{}, s.now())
			grant.Actions = upper
			if err := tx.SaveGrant(grant); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if grant.Status != "active" || grant.ExpiresAt != "" && grant.ExpiresAt <= s.now() {
			return errorf("authority_revoked", "member %s has no current project grant", member.ID)
		}
		states = append(states, grantState{ID: grant.ID, Epoch: grant.Epoch, Revision: grant.Revision, Actions: grant.Actions})
	}
	slices.SortFunc(states, func(a, b grantState) int { return strings.Compare(a.ID, b.ID) })
	run.GrantSnapshot = Digest(states)
	return nil
}

// FilterAuthorizedTools rechecks the live grant at every step dispatch. The
// frozen Run list can only shrink; a mode change cannot expand an old Attempt.
func (s *Service) FilterAuthorizedTools(tx *store.Tx, run w.Run, memberID string, names []string) ([]string, error) {
	if run.TaskID == "" || run.GrantSnapshot == "" {
		return names, nil
	}
	task, err := tx.Task(run.TaskID)
	if err != nil {
		return nil, err
	}
	if task.TransferRequestID != "" || task.Status == "paused" || task.Status == "canceled" || task.CurrentRevision != run.TaskRevision {
		return nil, errorf("authority_revoked", "task control or revision no longer admits this step")
	}
	grant, err := tx.Grant(grantID(task.WorkspaceID, memberID))
	if err != nil {
		return nil, errorf("authority_revoked", "member grant is unavailable")
	}
	now := s.now()
	if grant.Status != "active" || grant.ExpiresAt != "" && grant.ExpiresAt <= now {
		return nil, errorf("authority_revoked", "member grant is revoked or expired")
	}
	allowed := []string{}
	for _, name := range names {
		action := w.ActionForTool(name)
		if action != "" && slices.Contains(run.GrantActions, action) && slices.Contains(grant.Actions, action) {
			allowed = append(allowed, name)
		}
	}
	return allowed, nil
}
