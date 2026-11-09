package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
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
// collaboration defaults, retention or role assignments.
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

// missingTools reports the host capabilities a planned role requires but the
// runtime standard catalog lacks. Member tool arrays are retired history and are
// never consulted, so deleting the per-member configuration cannot strand a role.
func missingTools(_ w.Agent, required []string) []string {
	catalog := w.StandardToolCatalog()
	missing := []string{}
	for _, tool := range required {
		if !slices.Contains(catalog, tool) {
			missing = append(missing, tool)
		}
	}
	return missing
}

// roleConflict reports whether two exclusive roles cannot share a member.
func roleConflict(workflow, role, other string) bool {
	return w.RolesConflict(workflow, role, other)
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
	action := ""
	roles, builtin := w.WorkflowRoles[workflow]
	if !builtin {
		return in, nil
	}
	roleTools := func(role string) []string {
		return w.RoleTools(workflow, action, role)
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
		return len(missingTools(agent, roleTools(role))) == 0
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
			if tools := roleTools(role); len(tools) > 0 {
				return in, fmt.Errorf("所选 %s 成员不可用或缺少该角色所需的 %s 权限，请重新选择", role, strings.Join(tools, "、"))
			}
			return in, fmt.Errorf("所选 %s 成员在当前工作空间不可用，请重新选择", role)
		}
		assignments[role] = id
	}
	filled, stuck := defaultRoleAssignments(workflow, roles, assignments, settings.Assignments[workflow], th.CoordinatorID, pool, can)
	if stuck != "" {
		if tools := roleTools(stuck); len(tools) > 0 {
			for other, id := range assignments {
				if id != "" && roleConflict(workflow, stuck, other) && len(missingTools(enabled[id], roleTools(stuck))) == 0 {
					return in, fmt.Errorf("已选择的 %s 占用了唯一具备 %s 权限的成员，请为 %s 选择另一位成员", other, strings.Join(tools, "、"), stuck)
				}
			}
			return in, fmt.Errorf("当前会话没有可用的 %s 成员（需要 %s 权限）", stuck, strings.Join(tools, "、"))
		}
		return in, fmt.Errorf("当前会话没有足够的可用成员承担 %s", stuck)
	}
	in.Assignments = filled
	return in, nil
}

// rolePreferences returns the roles already fixed by the user or global
// defaults, plus the roles left for the coordinator to fill by responsibility.
// An unavailable preference is treated as open; the model can choose an
// eligible member and the deterministic resolver remains the fallback.
func rolePreferences(workflow string, in w.RunInput, th w.Thread, agents []w.Agent, settings w.Settings) (map[string]string, []string) {
	roles, ok := w.WorkflowRoles[workflow]
	if !ok {
		return nil, nil
	}
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
	inPool := map[string]bool{}
	for _, id := range pool {
		inPool[id] = true
	}
	enabled := map[string]w.Agent{}
	for _, agent := range agents {
		if !agent.Disabled {
			enabled[agent.ID] = agent
		}
	}
	can := func(role, id string) bool {
		agent, ok := enabled[id]
		if !ok || !inPool[id] {
			return false
		}
		return len(missingTools(agent, w.RoleTools(workflow, "", role))) == 0
	}
	fixed := map[string]string{}
	open := []string{}
	preferences := settings.Assignments[workflow]
	for _, role := range roles {
		if id := in.Assignments[role]; id != "" {
			fixed[role] = id
			continue
		}
		if id := preferences[role]; id != "" && can(role, id) {
			fixed[role] = id
			continue
		}
		if role == "coordinator" {
			id := settings.DefaultCoordinatorID
			if id == "" {
				id = th.CoordinatorID
			}
			if id != "" && can(role, id) {
				fixed[role] = id
				continue
			}
		}
		open = append(open, role)
	}
	return fixed, open
}

// applySettingsDefaultsWithModel keeps user assignments and configured role
// defaults fixed, then asks the conversation coordinator to fill only the open
// roles from the runtime roster. Any failure falls back to the deterministic
// roster-order resolver instead of blocking the run.
func (s *Server) applySettingsDefaultsWithModel(ctx context.Context, in w.RunInput, th w.Thread, agents []w.Agent, settings w.Settings, fallback w.RunInput) (w.RunInput, string) {
	method := in.Workflow
	if method == "" {
		method = "free"
	}
	if _, ok := w.WorkflowRoles[method]; !ok {
		return fallback, ""
	}
	fixed, open := rolePreferences(method, in, th, agents, settings)
	if len(open) == 0 || s.config.Model == nil {
		return fallback, ""
	}
	selectionThread := th
	for _, id := range in.Targets {
		if !slices.Contains(selectionThread.AgentIDs, id) {
			selectionThread.AgentIDs = append(selectionThread.AgentIDs, id)
		}
	}
	assignments, reason, called := workflow.SelectMissingRoles(ctx, s.config.Model, method, open, fixed, in.Message, selectionThread, agents)
	if !called || len(assignments) == 0 {
		return fallback, "自动组队未完成，按默认顺序补齐"
	}
	merged := map[string]string{}
	for role, id := range in.Assignments {
		merged[role] = id
	}
	for role, id := range assignments {
		merged[role] = id
	}
	candidate := in
	candidate.Assignments = merged
	filled, err := applySettingsDefaults(candidate, th, agents, settings)
	if err != nil {
		return fallback, "自动组队结果不可执行，按默认顺序补齐"
	}
	return filled, reason
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
			for _, id := range pool {
				add(id)
			}
		} else {
			// The conversation coordinator is the fallback scheduler, not the
			// first worker. Prefer other eligible members for worker roles so a
			// configured coordinator is not consumed as the researcher or
			// analyst before the intended member; backtracking still uses it
			// when the remaining roster cannot staff the plan.
			for _, id := range pool {
				if id != coordinatorID {
					add(id)
				}
			}
			add(coordinatorID)
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
