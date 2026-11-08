package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// GlobalAgents lists reusable profiles without workspace grants.
func (t *Tx) GlobalAgents() ([]w.Agent, error) {
	return list[w.Agent](t, "SELECT body FROM global_agents ORDER BY rowid")
}

// Agent reads a global profile; use WorkspaceAgent before an actual dispatch.
func (t *Tx) Agent(id string) (w.Agent, error) {
	return get[w.Agent](t, "SELECT body FROM global_agents WHERE id=?", id)
}

// Agents lists the workspace's members with the intersection of current grants.
func (t *Tx) Agents(workspaceID string) ([]w.Agent, error) {
	members, err := list[w.WorkspaceMember](t, "SELECT body FROM workspace_members WHERE workspace_id=? ORDER BY rowid", workspaceID)
	if err != nil {
		return nil, err
	}
	agents := make([]w.Agent, 0, len(members))
	for _, member := range members {
		agent, err := t.Agent(member.AgentID)
		if err != nil {
			return nil, err
		}
		agents = append(agents, effectiveAgent(agent, member))
	}
	return agents, nil
}

// WorkspaceAgent reads current membership, disability and tool permission together.
func (t *Tx) WorkspaceAgent(workspaceID, agentID string) (w.Agent, error) {
	member, err := t.Member(workspaceID, agentID)
	if err != nil {
		return w.Agent{}, err
	}
	agent, err := t.Agent(agentID)
	if err != nil {
		return w.Agent{}, err
	}
	return effectiveAgent(agent, member), nil
}

// effectiveAgent applies current membership and enablement. Legacy tool arrays
// are audit data: they no longer intersect with the profile, because the global
// execution mode, task/role constraints and conversation grants now decide what
// may run. Membership and disability checks still apply.
func effectiveAgent(agent w.Agent, member w.WorkspaceMember) w.Agent {
	agent.WorkspaceID, agent.Membership = member.WorkspaceID, &member
	agent.Disabled = agent.Disabled || !member.Enabled
	return agent
}

// SaveAgent saves a profile and, for old callers, creates its initial workspace link.
func (t *Tx) SaveAgent(agent w.Agent) error {
	workspaceID := agent.WorkspaceID
	agent.WorkspaceID, agent.Membership = "", nil
	members, err := t.Memberships(agent.ID)
	if err != nil {
		return err
	}
	for _, member := range members {
		if err := t.validateMemberNames(member.WorkspaceID, agent); err != nil {
			return err
		}
	}
	if err := t.exec("INSERT INTO global_agents(id,revision,body) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body", agent.ID, agent.Revision, body(agent)); err != nil {
		return err
	}
	if workspaceID != "" {
		if _, err := t.Member(workspaceID, agent.ID); errors.Is(err, ErrNotFound) {
			// New links carry no tool grant; the runtime catalog is authoritative.
			return t.SaveMember(w.WorkspaceMember{WorkspaceID: workspaceID, AgentID: agent.ID, Enabled: true, Revision: 1})
		} else if err != nil {
			return err
		}
	}
	return nil
}

// Member reads a workspace's grant for one global member.
func (t *Tx) Member(workspaceID, agentID string) (w.WorkspaceMember, error) {
	return get[w.WorkspaceMember](t, "SELECT body FROM workspace_members WHERE workspace_id=? AND agent_id=?", workspaceID, agentID)
}

// Memberships lists the workspaces using a profile, for conflict checks on edits.
func (t *Tx) Memberships(agentID string) ([]w.WorkspaceMember, error) {
	return list[w.WorkspaceMember](t, "SELECT body FROM workspace_members WHERE agent_id=? ORDER BY rowid", agentID)
}

func (t *Tx) validateMemberNames(workspaceID string, agent w.Agent) error {
	peers, err := t.Agents(workspaceID)
	if err != nil {
		return err
	}
	names := append([]string{agent.Handle}, agent.Aliases...)
	for _, peer := range peers {
		if peer.ID == agent.ID {
			continue
		}
		for _, name := range names {
			for _, other := range append([]string{peer.Handle}, peer.Aliases...) {
				if strings.EqualFold(name, other) {
					return fmt.Errorf("handle or alias is already assigned in workspace: %s", name)
				}
			}
		}
	}
	return nil
}

// SaveMember stores a workspace grant; effective permissions cannot exceed its profile.
func (t *Tx) SaveMember(member w.WorkspaceMember) error {
	if _, err := t.Workspace(member.WorkspaceID); err != nil {
		return err
	}
	agent, err := t.Agent(member.AgentID)
	if err != nil {
		return err
	}
	if err := t.validateMemberNames(member.WorkspaceID, agent); err != nil {
		return err
	}
	return t.exec("INSERT INTO workspace_members(workspace_id,agent_id,revision,body) VALUES(?,?,?,?) ON CONFLICT(workspace_id,agent_id) DO UPDATE SET revision=excluded.revision,body=excluded.body", member.WorkspaceID, member.AgentID, member.Revision, body(member))
}

// DeleteMember revokes workspace access without deleting global or historical data.
func (t *Tx) DeleteMember(workspaceID, agentID string) error {
	if _, err := t.Member(workspaceID, agentID); err != nil {
		return err
	}
	return t.exec("DELETE FROM workspace_members WHERE workspace_id=? AND agent_id=?", workspaceID, agentID)
}

// DeleteAgentProfile removes a reusable member and every workspace link. Run,
// message and task snapshots already carry their own member copies, so history
// stays readable while current rosters and defaults stop referencing the ID.
func (t *Tx) DeleteAgentProfile(agentID string) error {
	if _, err := t.Agent(agentID); err != nil {
		return err
	}
	if err := t.exec("DELETE FROM workspace_members WHERE agent_id=?", agentID); err != nil {
		return err
	}
	threads, err := list[w.Thread](t, "SELECT body FROM threads")
	if err != nil {
		return err
	}
	for _, thread := range threads {
		next := slices.DeleteFunc(slices.Clone(thread.AgentIDs), func(id string) bool { return id == agentID })
		coordinator := thread.CoordinatorID
		if coordinator == agentID {
			coordinator = ""
			if len(next) > 0 {
				coordinator = next[0]
			}
		}
		if len(next) == len(thread.AgentIDs) && coordinator == thread.CoordinatorID {
			continue
		}
		thread.AgentIDs, thread.CoordinatorID = next, coordinator
		thread.Revision++
		if err := t.SaveThread(thread); err != nil {
			return err
		}
	}
	settings, err := t.Settings()
	if err != nil {
		return err
	}
	changed := false
	if settings.DefaultCoordinatorID == agentID {
		settings.DefaultCoordinatorID, changed = "", true
	}
	for workflow, roles := range settings.Assignments {
		for role, id := range roles {
			if id == agentID {
				delete(roles, role)
				changed = true
			}
		}
		if len(roles) == 0 {
			delete(settings.Assignments, workflow)
		}
	}
	if changed {
		settings.Revision++
		if err := t.SaveSettings(settings); err != nil {
			return err
		}
	}
	return t.exec("DELETE FROM global_agents WHERE id=?", agentID)
}
