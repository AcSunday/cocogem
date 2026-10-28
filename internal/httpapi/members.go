package httpapi

import (
	"errors"
	"net/http"
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (s *Server) initializeMembers(tx *store.Tx, workspaceID string) error {
	agents, err := tx.GlobalAgents()
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		available := []string{}
		executors := s.executorSnapshot()
		for _, executor := range executors {
			if executor.Installed && executor.Supported {
				available = append(available, executor.ID)
			}
		}
		for i, name := range []string{"布偶猫", "缅因猫", "橘猫"} {
			handles := []string{"ragdoll", "maine", "orange"}
			roles := []string{"研究资料，整理证据与不确定性。", "独立质疑结论，核对依据与遗漏。", "组织讨论，汇总观点并保留分歧。"}
			agent := w.Agent{ID: w.NewID(), DisplayName: name, Handle: handles[i], Aliases: []string{}, RoleDescription: roles[i], Model: "default", Tools: tools.Names, Revision: 1}
			if len(s.config.Models) > 0 {
				agent.Model = s.config.Models[0]
			}
			if len(available) > 0 {
				agent.Executor, agent.Model = available[i%len(available)], "default"
			} else if len(executors) > 0 {
				agent.Executor, agent.Model = "claude", "default"
			}
			if err := tx.SaveAgent(agent); err != nil {
				return err
			}
			agents = append(agents, agent)
		}
	}
	selected := []w.Agent{}
	for _, agent := range agents {
		if agent.Disabled || len(selected) == 3 {
			continue
		}
		// Existing workspaces may contain different identities with the same handle.
		if err := w.ValidateAgent(agent, selected, []string{agent.Model}, agent.Tools); err != nil {
			continue
		}
		member := w.WorkspaceMember{WorkspaceID: workspaceID, AgentID: agent.ID, Enabled: true, Tools: agent.Tools, Revision: 1}
		if err := tx.SaveMember(member); err != nil {
			return err
		}
		selected = append(selected, agent)
	}
	return nil
}

func (s *Server) globalAgents(rw http.ResponseWriter, r *http.Request) {
	var agents []w.Agent
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		var err error
		agents, err = tx.GlobalAgents()
		return err
	})
	respond(rw, r, map[string]any{"items": agents}, err)
}

func (s *Server) agents(rw http.ResponseWriter, r *http.Request) {
	var agents []w.Agent
	err := s.store.Read(r.Context(), func(tx *store.Tx) error {
		if _, err := tx.Workspace(r.PathValue("id")); err != nil {
			return err
		}
		var err error
		agents, err = tx.Agents(r.PathValue("id"))
		return err
	})
	respond(rw, r, map[string]any{"items": agents}, err)
}

func (s *Server) createGlobalAgent(rw http.ResponseWriter, r *http.Request) {
	s.createMemberProfile(rw, r, "")
}

func (s *Server) createAgent(rw http.ResponseWriter, r *http.Request) {
	s.createMemberProfile(rw, r, r.PathValue("id"))
}

func (s *Server) createMemberProfile(rw http.ResponseWriter, r *http.Request, workspaceID string) {
	var agent w.Agent
	if !decode(rw, r, &agent) {
		return
	}
	agent.ID, agent.WorkspaceID, agent.Membership, agent.Revision = w.NewID(), workspaceID, nil, 1
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		if err := s.validateExecutor(agent.Executor); err != nil {
			return err
		}
		if err := w.ValidateAgent(agent, nil, s.config.Models, tools.AllNames); err != nil {
			return err
		}
		if err := tx.SaveAgent(agent); err != nil {
			return err
		}
		var err error
		if workspaceID == "" {
			agent, err = tx.Agent(agent.ID)
		} else {
			agent, err = tx.WorkspaceAgent(workspaceID, agent.ID)
		}
		return err
	})
	respond(rw, r, agent, err)
}

// revokedTools reports whether any previously granted tool disappeared.
func revokedTools(previous, next []string) bool {
	return slices.ContainsFunc(previous, func(tool string) bool { return !slices.Contains(next, tool) })
}

func (s *Server) updateAgent(rw http.ResponseWriter, r *http.Request) {
	var agent w.Agent
	if !decode(rw, r, &agent) {
		return
	}
	revoked := false
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		old, err := tx.Agent(r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := revision(r, old.Revision); err != nil {
			return err
		}
		agent.ID, agent.WorkspaceID, agent.Membership, agent.Revision = old.ID, "", nil, old.Revision+1
		if err := s.validateExecutor(agent.Executor); err != nil {
			return err
		}
		if err := w.ValidateAgent(agent, nil, s.config.Models, tools.AllNames); err != nil {
			return err
		}
		revoked = agent.Disabled && !old.Disabled || revokedTools(old.Tools, agent.Tools)
		return tx.SaveAgent(agent)
	})
	if err == nil && revoked && s.engine != nil {
		// A profile-wide revocation affects every workspace using this member.
		err = s.engine.CancelMember(r.Context(), "", agent.ID)
	}
	respond(rw, r, agent, err)
}

func validateMemberTools(member w.WorkspaceMember) error {
	for _, tool := range member.Tools {
		if !slices.Contains(tools.AllNames, tool) {
			return errors.New("工作空间工具权限无效")
		}
	}
	return nil
}

func (s *Server) linkMember(rw http.ResponseWriter, r *http.Request) {
	s.saveMember(rw, r, false)
}

func (s *Server) updateMember(rw http.ResponseWriter, r *http.Request) {
	s.saveMember(rw, r, true)
}

func (s *Server) saveMember(rw http.ResponseWriter, r *http.Request, update bool) {
	var member w.WorkspaceMember
	if !decode(rw, r, &member) {
		return
	}
	member.WorkspaceID, member.AgentID = r.PathValue("id"), r.PathValue("agentID")
	var agent w.Agent
	revoked := false
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		old, err := tx.Member(member.WorkspaceID, member.AgentID)
		if update {
			if err != nil {
				return err
			}
			if err := revision(r, old.Revision); err != nil {
				return err
			}
			member.Revision = old.Revision + 1
			revoked = old.Enabled && !member.Enabled || revokedTools(old.Tools, member.Tools)
		} else {
			if err == nil {
				return w.ErrConflict
			}
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			member.Revision = 1
		}
		if err := validateMemberTools(member); err != nil {
			return err
		}
		if err := tx.SaveMember(member); err != nil {
			return err
		}
		agent, err = tx.WorkspaceAgent(member.WorkspaceID, member.AgentID)
		return err
	})
	if err == nil && revoked && s.engine != nil {
		err = s.engine.CancelMember(r.Context(), member.WorkspaceID, member.AgentID)
	}
	respond(rw, r, agent, err)
}

func (s *Server) unlinkMember(rw http.ResponseWriter, r *http.Request) {
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		member, err := tx.Member(r.PathValue("id"), r.PathValue("agentID"))
		if err != nil {
			return err
		}
		if err := revision(r, member.Revision); err != nil {
			return err
		}
		return tx.DeleteMember(member.WorkspaceID, member.AgentID)
	})
	if err == nil && s.engine != nil {
		err = s.engine.CancelMember(r.Context(), r.PathValue("id"), r.PathValue("agentID"))
	}
	respond(rw, r, DeleteReply{ID: r.PathValue("agentID")}, err)
}
