package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

const selectionReasonLimit = 2000

type selectionInput struct {
	Workflow    string            `json:"workflow"`
	Targets     []string          `json:"targets"`
	Assignments map[string]string `json:"assignments"`
	Reason      string            `json:"reason"`
}

// effectiveInput is the plan input a run actually executes: the immutable user
// input plus the coordinator's persisted selection, if any.
func effectiveInput(run w.Run) w.RunInput {
	in := run.Input
	if run.SelectedWorkflow == nil {
		return in
	}
	in.Workflow = run.SelectedWorkflow.Workflow
	in.Targets = slices.Clone(run.SelectedWorkflow.Targets)
	in.Assignments = maps.Clone(run.SelectedWorkflow.Assignments)
	if in.Workflow == "engineering" {
		// Method selection is never a develop authorization: the coordinator
		// can only select the read-only analysis action.
		in.EngineeringAction = w.EngineeringAnalyze
	}
	return in
}

// selectionRoot admits only the default coordinator's planning root of a free
// run that preserved the user's explicit recipients and method. It stays true
// for an identical repeat inside the same invocation; the stored selection is
// compared separately.
func selectionRoot(run w.Run, step w.Step) bool {
	if run.Input.Workflow != "" && run.Input.Workflow != "free" {
		return false
	}
	if run.Routing.Decision != "discuss" {
		return false
	}
	return step.Kind == "" && step.ID != "" && step.AgentID == run.CoordinatorID
}

func canSelectWorkflow(run w.Run, step w.Step) bool {
	return run.SelectedWorkflow == nil && len(run.Agents) > 1 && selectionRoot(run, step)
}

func selectionTools(run w.Run, step w.Step) []model.Tool {
	if !canSelectWorkflow(run, step) {
		return nil
	}
	ids := make([]string, 0, len(run.Agents))
	for _, a := range run.Agents {
		if !a.Disabled {
			ids = append(ids, a.ID)
		}
	}
	member := map[string]any{"type": "string", "enum": ids, "description": "Exact member UUID from this run's runtime roster."}
	memberOrEmpty := func(role string) map[string]any {
		return map[string]any{"type": "string", "enum": ids, "description": member["description"].(string) + " Required for " + role + " when the chosen method uses it."}
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"workflow": map[string]any{"type": "string", "enum": []string{"discussion", "research", "engineering", "code_analysis"}},
			"targets":  map[string]any{"type": "array", "maxItems": 4, "uniqueItems": true, "items": member, "description": "Discussion participants (2–4 distinct members). Empty for other methods."},
			"assignments": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"researcher":  memberOrEmpty("research"),
				"critic":      memberOrEmpty("research"),
				"coordinator": memberOrEmpty("research, discussion or final delivery"),
				"analyst":     memberOrEmpty("engineering analysis"),
				"reviewer":    memberOrEmpty("engineering analysis"),
			}, "description": "Method role to exact member UUID. Use only the roles of the chosen workflow; never put task instructions here."},
			"reason": map[string]any{"type": "string", "maxLength": selectionReasonLimit},
		},
		"required":             []string{"workflow"},
		"additionalProperties": false,
	}
	return []model.Tool{{Name: "choose_workflow", Description: "Select this free run's collaboration method once: discussion for independent views with cross-critique, research for evidence gathering, or engineering for read-only code analysis. Use it only when the task genuinely needs that method; a simple question needs no tool. When the user explicitly requests one of these methods, independent views with cross-critique, or names choose_workflow, selecting it is required; do not answer directly or replace the request with a single handoff. Selecting engineering (or legacy code_analysis) never authorizes development or file writes; development requires the user's explicit Composer choice. Fill roles with exact runtime roster IDs chosen by actual capabilities and permissions, never by display names or CLI/model brands. If the roster cannot staff the method, finish with a short explanation instead of inventing members. After acceptance, finish this planning step without answering the task; the platform runs the selected plan next.", Schema: schema}}
}

func (e *Engine) chooseWorkflow(ctx context.Context, run w.Run, sender w.Step, raw json.RawMessage) (string, error) {
	var in selectionInput
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	roles, ok := w.WorkflowRoles[in.Workflow]
	if !ok {
		return "", errors.New("workflow method must be discussion, research, engineering or code_analysis")
	}
	if in.Workflow == "engineering" {
		// A method selection only ever means the read-only analysis action, so
		// the developer role is not part of its selectable vocabulary.
		roles = w.WorkflowActionRoles(in.Workflow, w.EngineeringAnalyze)
	}
	if utf8.RuneCountInString(in.Reason) > selectionReasonLimit || strings.TrimSpace(in.Reason) != in.Reason {
		return "", errors.New("invalid method selection reason")
	}
	if len(in.Targets) > 4 {
		return "", errors.New("select at most four discussion participants")
	}
	targets := make([]string, 0, len(in.Targets))
	for _, id := range in.Targets {
		if strings.TrimSpace(id) == "" || slices.Contains(targets, id) {
			return "", errors.New("targets must be unique member IDs")
		}
		targets = append(targets, id)
	}
	assignments := map[string]string{}
	for role, id := range in.Assignments {
		if !slices.Contains(roles, role) {
			return "", fmt.Errorf("workflow %s does not use role %s", in.Workflow, role)
		}
		if strings.TrimSpace(id) != "" {
			assignments[role] = id
		}
	}
	desired := w.WorkflowSelection{Workflow: in.Workflow, Targets: targets, Assignments: assignments, Reason: in.Reason}
	var receipt w.WorkflowSelection
	var repeated bool
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return errors.New("run no longer accepts a method selection")
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		origin := slices.IndexFunc(steps, func(s w.Step) bool { return s.ID == sender.ID })
		if origin < 0 || steps[origin].Status != "running" {
			return errors.New("the planning step is no longer running")
		}
		step := steps[origin]
		if !selectionRoot(current, step) {
			return errors.New("only the default coordinator may select a method for an unassigned free run")
		}
		for _, id := range targets {
			if !slices.ContainsFunc(current.Agents, func(a w.Agent) bool { return a.ID == id }) {
				return errors.New("selected participant is outside this run roster")
			}
		}
		if (desired.Workflow == "code_analysis" || desired.Workflow == "engineering") && current.Workspace.Root == "" {
			return errors.New("the selected engineering analysis method requires a bound source directory")
		}
		if slices.ContainsFunc(steps, func(s w.Step) bool { return s.Kind == "handoff" || s.Kind == "continuation" }) {
			return errors.New("work was already committed through delegation; finish its continuation instead of selecting a method")
		}
		if current.SelectedWorkflow != nil {
			// The accepted selection is immutable. Reuse its original defaults
			// for omitted roles so that changing global defaults cannot turn an
			// identical retry into a conflicting selection.
			for role, id := range current.SelectedWorkflow.Assignments {
				if desired.Assignments[role] == "" {
					desired.Assignments[role] = id
				}
			}
			if !sameSelection(*current.SelectedWorkflow, desired) {
				return errors.New("a different workflow method was already selected for this run")
			}
			receipt, repeated = *current.SelectedWorkflow, true
			return nil
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		if err := fillSelectionDefaults(&desired, current, settings); err != nil {
			return err
		}
		planInput := w.RunInput{Workflow: desired.Workflow, Targets: desired.Targets, Assignments: desired.Assignments}
		if desired.Workflow == "engineering" {
			planInput.EngineeringAction = w.EngineeringAnalyze
		}
		plan, err := Plan(current.ID, planInput, current.Agents)
		if err != nil {
			return fmt.Errorf("the selected method cannot run with the current roster: %w", err)
		}
		if len(steps)+len(plan) > maxSteps {
			return errors.New("collaboration step budget exceeded")
		}
		for i := range plan {
			// The plan starts only after the planning root has committed its
			// directive; independent views still share no sibling content.
			if len(plan[i].DependsOn) == 0 {
				plan[i].DependsOn = []string{step.ID}
			}
			if err := e.validateMember(tx, current, plan[i].AgentID); err != nil {
				return fmt.Errorf("selected member cannot execute this method: %w", err)
			}
			if err := tx.SaveStep(plan[i]); err != nil {
				return err
			}
		}
		receipt = w.WorkflowSelection{Workflow: desired.Workflow, Targets: slices.Clone(desired.Targets), Assignments: maps.Clone(desired.Assignments), Reason: desired.Reason, StepID: step.ID, SelectedAt: w.Now()}
		current.SelectedWorkflow = &receipt
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		eventSteps := make([]map[string]any, 0, len(plan))
		for _, s := range plan {
			eventSteps = append(eventSteps, map[string]any{"step_id": s.ID, "agent_id": s.AgentID, "role": s.Role, "depends_on": s.DependsOn})
		}
		return tx.AppendEvent(run.ID, step.ID, step.AgentID, "workflow_selected", map[string]any{"selection": receipt, "steps": eventSteps})
	})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(map[string]any{"accepted": true, "idempotent_repeat": repeated, "workflow": receipt.Workflow, "targets": receipt.Targets, "assignments": receipt.Assignments, "instruction": "Method accepted. Finish this planning step now without answering the user's task; the platform runs the selected plan next. Do not delegate from this step."})
	return string(encoded), err
}

// planningRootStep identifies the coordinator's planning step that selected
// this run's method. Its message is a directive for scheduling, not an upstream
// result: selected plan steps may not read it.
func planningRootStep(run w.Run) string {
	if run.SelectedWorkflow == nil {
		return ""
	}
	return run.SelectedWorkflow.StepID
}

// fillSelectionDefaults applies global role preferences only to members already
// in this run's roster. A configured default that is unavailable fails with a
// correctable error instead of silently assigning another member; an omitted
// role without a usable default stays empty so Plan reports the gap.
func fillSelectionDefaults(sel *w.WorkflowSelection, run w.Run, settings w.Settings) error {
	eligible := map[string]bool{}
	for _, a := range run.Agents {
		if !a.Disabled {
			eligible[a.ID] = true
		}
	}
	preferences := settings.Assignments[sel.Workflow]
	for _, role := range w.WorkflowActionRoles(sel.Workflow, w.EngineeringAnalyze) {
		if sel.Assignments[role] != "" {
			continue
		}
		id := preferences[role]
		if id == "" && role == "coordinator" {
			id = settings.DefaultCoordinatorID
		}
		if id == "" {
			continue
		}
		if !eligible[id] {
			return fmt.Errorf("the configured %s default for %s is unavailable in this run; choose an available member explicitly", role, sel.Workflow)
		}
		sel.Assignments[role] = id
	}
	return nil
}

func sameSelection(a, b w.WorkflowSelection) bool {
	return a.Workflow == b.Workflow && sameIDs(a.Targets, b.Targets) && maps.Equal(a.Assignments, b.Assignments)
}

// settledMethod classifies a successful run without rewriting the user input.
func settledMethod(run w.Run, steps []w.Step) string {
	if run.SelectedWorkflow != nil {
		return run.SelectedWorkflow.Workflow
	}
	if run.Input.Workflow != "" && run.Input.Workflow != "free" {
		return ""
	}
	if slices.ContainsFunc(steps, func(s w.Step) bool { return s.Kind == "handoff" }) {
		return "delegation"
	}
	return "direct"
}
