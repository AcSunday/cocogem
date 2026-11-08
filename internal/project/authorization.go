package project

import (
	"errors"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func grantID(workspaceID, memberID string) string { return "grant-" + workspaceID + "-" + memberID }

// ExecutionPolicy is the live, global permission source used for admission.
type ExecutionPolicy struct {
	Mode  string
	Epoch int
}

// CurrentPolicy reads the single global permission policy. A workspace body's
// legacy mode is never consulted.
func (s *Service) CurrentPolicy(tx *store.Tx) (ExecutionPolicy, error) {
	settings, err := tx.Settings()
	if err != nil {
		return ExecutionPolicy{}, err
	}
	policy := ExecutionPolicy{Mode: settings.ExecutionMode, Epoch: settings.PermissionEpoch}
	if !w.ValidExecutionMode(policy.Mode) {
		policy.Mode = w.DefaultExecutionMode
	}
	if policy.Epoch < 1 {
		policy.Epoch = 1
	}
	return policy, nil
}

type grantState struct {
	ID       string   `json:"id"`
	Epoch    int      `json:"epoch"`
	Revision int      `json:"revision"`
	Actions  []string `json:"actions"`
}

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

// EnsureRunAuthorization freezes the task/mode action upper bound, the mode and
// the permission epoch the Run started with. Every dispatch and admission
// re-checks the live epoch, so a later change can only shrink this boundary.
func (s *Service) EnsureRunAuthorization(tx *store.Tx, run *w.Run, source string) error {
	var revision w.TaskRevision
	if run.TaskID != "" {
		task, err := tx.Task(run.TaskID)
		if err != nil {
			return err
		}
		if task.TransferRequestID != "" {
			return errorf("authority_revoked", "task admission is frozen while owner transfer is pending")
		}
		revision, err = tx.TaskRevision(task.ID, run.TaskRevision)
		if err != nil {
			return err
		}
	} else {
		if !run.TaskIntake {
			return nil
		}
		revision.AllowedActions = []string{"read", "network", "collaborate", "publish_artifact"}
	}
	ws, err := tx.Workspace(run.WorkspaceID)
	if err != nil {
		return err
	}
	policy, err := s.CurrentPolicy(tx)
	if err != nil {
		return err
	}
	run.RequestedExecutionMode = policy.Mode
	run.EffectiveExecutionMode = policy.Mode
	run.PermissionEpoch = policy.Epoch
	run.EnforcedCapabilities = []string{"host_grant_intersection", "scoped_mcp", "candidate_tree_lease"}
	run.UnenforcedConstraints = nil
	switch policy.Mode {
	case w.ExecutionProjectAutonomous:
		// The project boundary is enforced by the host tools and by each
		// adapter's own sandbox/allowlist. Remaining unknowns are recorded
		// honestly instead of downgrading the mode silently.
		run.EnforcedCapabilities = []string{"host_grant_intersection", "scoped_mcp", "candidate_tree_lease", "project_root_normalized", "native_allowlist"}
		run.UnenforcedConstraints = w.NativeIsolationUnknowns()
	case w.ExecutionFullAccess:
		run.EnforcedCapabilities = []string{"host_grant_intersection", "scoped_mcp", "process_group_observation"}
		run.UnenforcedConstraints = w.NativeIsolationUnknowns()
	}
	if run.TaskIntake && run.TaskID == "" {
		// Intake has no durable task yet; its tool catalog stays read-only.
		run.EffectiveExecutionMode = w.ExecutionControlled
	}
	// The frozen ceiling is the task/role boundary, not the global mode. A mode
	// may admit less than the task allows; that difference becomes an execution-
	// time request instead of silently deleting the capability. Actions the mode
	// does not name but can request therefore stay in the ceiling.
	// Union, not concatenation: AuthorizedActions intersects distinct sets, so a
	// repeated entry would distort the count and drop every shared action.
	ceiling := []string{}
	for _, action := range append(w.ModeActions(run.EffectiveExecutionMode), w.RequestableActions(run.EffectiveExecutionMode)...) {
		if !slices.Contains(ceiling, action) {
			ceiling = append(ceiling, action)
		}
	}
	upper := w.AuthorizedActions(ceiling, revision.AllowedActions, revision.AllowedActions, revision.AllowedActions, revision.AllowedActions)
	run.GrantActions = upper
	states := []grantState{}
	for _, member := range run.Agents {
		id := grantID(ws.ID, member.ID)
		grant, err := tx.Grant(id)
		if errors.Is(err, store.ErrNotFound) {
			grant = modeGrant(ws, member.ID, policy.Mode, source, w.Grant{}, s.now())
			if err := tx.SaveGrant(grant); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		// Legacy workspace/member grants are inert history after the migration:
		// the workspace permission UI is gone, so a revoked or expired row must
		// not veto a new Run of the global policy. Live authority comes from the
		// global mode, the task/role ceiling and the conversation grants.
		states = append(states, grantState{ID: grant.ID, Epoch: grant.Epoch, Revision: grant.Revision, Actions: w.ModeActions(policy.Mode)})
	}
	slices.SortFunc(states, func(a, b grantState) int { return strings.Compare(a.ID, b.ID) })
	run.GrantSnapshot = Digest(states)
	return nil
}

// FilterAuthorizedTools rechecks the live membership and task control at every
// step dispatch. The frozen Run action list can only shrink; a mode change
// cannot expand an old Attempt.
func (s *Service) FilterAuthorizedTools(tx *store.Tx, run w.Run, memberID string, names []string) ([]string, error) {
	if run.GrantSnapshot == "" {
		return names, nil
	}
	if run.TaskID != "" {
		task, err := tx.Task(run.TaskID)
		if err != nil {
			return nil, err
		}
		if task.TransferRequestID != "" || task.Status == "paused" || task.Status == "canceled" || task.CurrentRevision != run.TaskRevision {
			return nil, errorf("authority_revoked", "task control or revision no longer admits this step")
		}
	}
	grant, err := tx.Grant(grantID(run.WorkspaceID, memberID))
	if err != nil {
		return nil, errorf("authority_revoked", "member grant is unavailable")
	}
	// The legacy grant row is audit history. Enablement and membership are the
	// live checks; the legacy revoked/expired status no longer vetoes a step.
	_ = grant
	if member, err := tx.WorkspaceAgent(run.WorkspaceID, memberID); err != nil || member.Disabled {
		return nil, errorf("authority_revoked", "member is no longer enabled in this workspace")
	}
	allowed := []string{}
	for _, name := range names {
		action := w.ActionForTool(name)
		if action != "" && slices.Contains(run.GrantActions, action) {
			allowed = append(allowed, name)
		}
	}
	return allowed, nil
}
