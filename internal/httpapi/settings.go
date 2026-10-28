package httpapi

import (
	"fmt"
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

func (s *Server) updateSettings(rw http.ResponseWriter, r *http.Request) {
	var in SettingsInput
	if !decode(rw, r, &in) {
		return
	}
	var value w.Settings
	err := s.store.Write(r.Context(), func(tx *store.Tx) error {
		current, err := tx.Settings()
		if err != nil {
			return err
		}
		if err := revision(r, current.Revision); err != nil {
			return err
		}
		assignments := in.Assignments
		if assignments == nil {
			assignments = map[string]map[string]string{}
		}
		value = w.Settings{Revision: current.Revision + 1, DefaultCoordinatorID: in.DefaultCoordinatorID, Assignments: assignments}
		agents, err := tx.GlobalAgents()
		if err != nil {
			return err
		}
		if err := w.ValidateSettings(value, agents); err != nil {
			return err
		}
		return tx.SaveSettings(value)
	})
	respond(rw, r, value, err)
}

// routingCatalog keeps presentation-only avatars out of routing validity.
func routingCatalog(agents []w.Agent) []w.Agent {
	out := slices.Clone(agents)
	for i := range out {
		out[i].Avatar = ""
	}
	return out
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
		if len(th.AgentIDs) > 4 {
			th.AgentIDs = th.AgentIDs[:4]
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

// requiredRoleTool is the tool a role cannot work without.
func requiredRoleTool(role string) string {
	if role == "coordinator" || role == "reviewer" {
		return "publish_artifact"
	}
	return ""
}

// roleConflict reports whether two exclusive roles cannot share a member.
func roleConflict(workflow, role, other string) bool {
	switch workflow {
	case "research":
		return role == "researcher" && other == "critic" || role == "critic" && other == "researcher"
	case "code_analysis":
		return role == "analyst" && other == "reviewer" || role == "reviewer" && other == "analyst"
	}
	return false
}

// applySettingsDefaults fills omitted built-in roles from an explicit override,
// then the global preference, then the conversation roster. Explicit overrides
// are authoritative: an unusable one is an error instead of a silent reassign.
// Defaults only select enabled members already in this workspace and
// conversation, never adding a member relationship or permission.
func applySettingsDefaults(in w.RunInput, th w.Thread, agents []w.Agent, settings w.Settings) (w.RunInput, error) {
	workflow := in.Workflow
	if workflow == "" {
		workflow = "free"
	}
	roles, builtin := w.WorkflowRoles[workflow]
	if !builtin {
		return in, nil
	}
	enabled := map[string]w.Agent{}
	for _, agent := range agents {
		if !agent.Disabled {
			enabled[agent.ID] = agent
		}
	}
	can := func(role, id string) bool {
		agent, ok := enabled[id]
		if !ok {
			return false
		}
		tool := requiredRoleTool(role)
		return tool == "" || slices.Contains(agent.Tools, tool)
	}
	// Defaults stay inside the roster the user selected explicitly: the
	// conversation members plus any targets named for this run.
	pool := slices.Clone(th.AgentIDs)
	for _, id := range in.Targets {
		if !slices.Contains(pool, id) {
			pool = append(pool, id)
		}
	}
	if len(pool) == 0 {
		for _, agent := range agents {
			if !agent.Disabled {
				pool = append(pool, agent.ID)
			}
		}
	}
	assignments := make(map[string]string, len(in.Assignments))
	for role, id := range in.Assignments {
		if !slices.Contains(roles, role) {
			return in, fmt.Errorf("流程 %s 不支持角色 %s", workflow, role)
		}
		if id == "" {
			continue
		}
		if !can(role, id) {
			if tool := requiredRoleTool(role); tool != "" {
				return in, fmt.Errorf("所选 %s 成员不可用或缺少该角色所需的 %s 权限，请重新选择", role, tool)
			}
			return in, fmt.Errorf("所选 %s 成员在当前工作空间不可用，请重新选择", role)
		}
		assignments[role] = id
	}
	filled, stuck := defaultRoleAssignments(workflow, roles, assignments, settings.Assignments[workflow], th.CoordinatorID, pool, can)
	if stuck != "" {
		if tool := requiredRoleTool(stuck); tool != "" {
			for other, id := range assignments {
				if id != "" && roleConflict(workflow, stuck, other) && slices.Contains(enabled[id].Tools, tool) {
					return in, fmt.Errorf("已选择的 %s 占用了唯一具备 %s 权限的成员，请为 %s 选择另一位成员", other, tool, stuck)
				}
			}
			return in, fmt.Errorf("当前会话没有可用的 %s 成员（需要 %s 权限）", stuck, tool)
		}
		return in, fmt.Errorf("当前会话没有足够的可用成员承担 %s", stuck)
	}
	in.Assignments = filled
	return in, nil
}

// defaultRoleAssignments fills omitted roles with enabled members from the pool.
// Configured roles are placed first, then each role tries the global preference,
// the conversation coordinator, and pool order. A pick that leaves a later role
// unfillable is backtracked, so the only publisher can move from an unrestricted
// role to the reviewer instead of rejecting a feasible two-member plan. Explicit
// assignments are never changed; a failed fill returns the blocking role.
func defaultRoleAssignments(workflow string, roles []string, assigned, preferences map[string]string, coordinatorID string, pool []string, can func(role, id string) bool) (map[string]string, string) {
	out := make(map[string]string, len(roles))
	for role, id := range assigned {
		out[role] = id
	}
	inPool := map[string]bool{}
	for _, id := range pool {
		inPool[id] = true
	}
	conflicts := func(role, id string) bool {
		for other, otherID := range out {
			if otherID == id && roleConflict(workflow, role, other) {
				return true
			}
		}
		return false
	}
	order := make([]string, 0, len(roles))
	for _, role := range roles {
		if preferences[role] != "" {
			order = append(order, role)
		}
	}
	for _, role := range roles {
		if preferences[role] == "" {
			order = append(order, role)
		}
	}
	stuck := ""
	var fill func(int) bool
	fill = func(i int) bool {
		if i == len(order) {
			return true
		}
		role := order[i]
		if out[role] != "" {
			return fill(i + 1)
		}
		seen := map[string]bool{}
		candidates := make([]string, 0, len(pool)+2)
		add := func(id string) {
			if id != "" && !seen[id] {
				seen[id] = true
				candidates = append(candidates, id)
			}
		}
		add(preferences[role])
		if role == "coordinator" {
			add(coordinatorID)
		}
		for _, id := range pool {
			add(id)
		}
		for _, id := range candidates {
			if !inPool[id] || !can(role, id) || conflicts(role, id) {
				continue
			}
			out[role] = id
			if fill(i + 1) {
				return true
			}
			delete(out, role)
		}
		if stuck == "" {
			stuck = role
		}
		return false
	}
	if fill(0) {
		return out, ""
	}
	return out, stuck
}
