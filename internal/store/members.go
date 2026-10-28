package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// migrateAgents preserves every old identity; names are never used to merge records.
func migrateAgents(db *sql.DB) error {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_profiles'").Scan(&exists); err != nil {
		return err
	}
	t := &Tx{ctx: ctx, tx: tx}
	if exists > 0 {
		agents, err := list[w.Agent](t, "SELECT body FROM agent_profiles ORDER BY rowid")
		if err != nil {
			return err
		}
		for _, agent := range agents {
			workspaceID := agent.WorkspaceID
			agent.WorkspaceID, agent.Membership = "", nil
			if err := t.exec("INSERT INTO global_agents(id,revision,body) VALUES(?,?,?)", agent.ID, agent.Revision, body(agent)); err != nil {
				return fmt.Errorf("preserve member %s: %w", agent.ID, err)
			}
			member := w.WorkspaceMember{WorkspaceID: workspaceID, AgentID: agent.ID, Enabled: true, Tools: agent.Tools, Revision: 1}
			if err := t.exec("INSERT INTO workspace_members(workspace_id,agent_id,revision,body) VALUES(?,?,?,?)", workspaceID, agent.ID, member.Revision, body(member)); err != nil {
				return err
			}
		}
		if err := t.exec("DROP TABLE agent_profiles"); err != nil {
			return err
		}
	}
	if err := t.exec("INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(2,strftime('%Y-%m-%dT%H:%M:%fZ','now'))"); err != nil {
		return err
	}
	return tx.Commit()
}

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

func effectiveAgent(agent w.Agent, member w.WorkspaceMember) w.Agent {
	agent.WorkspaceID, agent.Membership = member.WorkspaceID, &member
	agent.Disabled = agent.Disabled || !member.Enabled
	allowed := []string{}
	for _, tool := range agent.Tools {
		if slices.Contains(member.Tools, tool) {
			allowed = append(allowed, tool)
		}
	}
	agent.Tools = allowed
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
			return t.SaveMember(w.WorkspaceMember{WorkspaceID: workspaceID, AgentID: agent.ID, Enabled: true, Tools: agent.Tools, Revision: 1})
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
