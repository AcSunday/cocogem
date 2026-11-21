package workflow

import (
	"slices"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// effectiveInput is the plan input a run executes. The dynamic kernel has no
// persisted method selection, so the immutable user input is authoritative.
func effectiveInput(run w.Run) w.RunInput {
	return run.Input
}

// planningRoot reports whether this step is the coordinator's root step of a run
// with no explicit recipients: the step that may organise follow-up work.
func planningRoot(run w.Run, step w.Step) bool {
	if len(run.Input.Targets) > 0 {
		return false
	}
	planning := step.Kind == "" || step.Kind == kindRetryRoot || step.Kind == "continuation" && step.FromStepID == run.TaskPromotionStepID
	return planning && step.ID != "" && step.AgentID == run.CoordinatorID
}

// settledMethod classifies a successful run without rewriting the user input.
func settledMethod(steps []w.Step) string {
	if slices.ContainsFunc(steps, func(s w.Step) bool { return s.Kind == "handoff" || s.Kind == "relay" }) {
		return "delegation"
	}
	return "direct"
}
