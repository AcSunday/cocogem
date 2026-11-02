package workflow

import (
	"context"
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// mayPublish reports whether this step may submit a formal artifact. Built-ins
// reserve publication for the designated final role. A free run allows its roots
// and the final continuation of each root chain; delegated tasks and earlier
// continuations are rejected before a draft can be created. A root's artifact
// is kept as an attempt draft if the root later delegates.
func (e *Engine) mayPublish(ctx context.Context, run w.Run, step w.Step) (bool, error) {
	current := run
	if run.SelectedWorkflow == nil && step.Kind == "" && (run.Input.Workflow == "" || run.Input.Workflow == "free") {
		// The planning root may select a method during this same invocation, so
		// its publication authority follows the persisted run, not the stale
		// dispatch snapshot.
		if err := e.Store.Read(ctx, func(tx *store.Tx) error {
			var err error
			current, err = tx.Run(run.ID)
			return err
		}); err != nil {
			return false, err
		}
	}
	in := effectiveInput(current)
	if RequiredArtifact(step, in) != "" {
		return true, nil
	}
	if in.Workflow != "" && in.Workflow != "free" {
		return false, nil
	}
	if step.Kind == "" {
		return true, nil
	}
	if step.Kind != "continuation" {
		return false, nil
	}
	var steps []w.Step
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(run.ID)
		return err
	}); err != nil {
		return false, err
	}
	// Only the root chain's final continuation may publish; an inner
	// continuation belongs to a delegated task and stays a draft.
	return slices.ContainsFunc(finalSteps(in, steps), func(final w.Step) bool {
		return final.ID == step.ID
	}), nil
}
