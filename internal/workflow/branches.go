package workflow

import (
	"context"
	"slices"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type scheduledBranch struct {
	index  int
	writer bool
}

func (e *Engine) stepWritesCandidate(run w.Run, step w.Step) bool {
	for _, member := range run.Agents {
		if member.ID != step.AgentID {
			continue
		}
		allowed := e.allowedTools(run, member, step)
		return slices.Contains(allowed, "write_file") || slices.Contains(allowed, "patch_file") || slices.Contains(allowed, "run_test")
	}
	return false
}

// acquireStepBranches dispatches a deterministic prefix of independent steps.
// The first uses the Run's existing slot; every sibling needs its own shared
// branch/writer reservation. Remaining steps stay pending for the next pass.
func (e *Engine) acquireStepBranches(ctx context.Context, run w.Run, ready []int, steps []w.Step) ([]scheduledBranch, error) {
	if len(ready) == 0 {
		return nil, nil
	}
	if run.TaskID == "" || !run.BudgetStarted {
		// Legacy/unmanaged runs have no shared budget ledger. They retain the
		// original all-ready scheduling behavior instead of accidentally running
		// only the first sibling and deadlocking interdependent test runners.
		branches := make([]scheduledBranch, 0, len(ready))
		for _, index := range ready {
			branches = append(branches, scheduledBranch{index: index, writer: e.stepWritesCandidate(run, steps[index])})
		}
		return branches, nil
	}
	branches := []scheduledBranch{{index: ready[0], writer: e.stepWritesCandidate(run, steps[ready[0]])}}
	if len(ready) == 1 {
		return branches, nil
	}
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		service := project.New(e.Store)
		for _, index := range ready[1:] {
			writer := e.stepWritesCandidate(run, steps[index])
			acquired, err := service.AcquireStepBranch(tx, run, writer)
			if err != nil {
				return err
			}
			if !acquired {
				break
			}
			branches = append(branches, scheduledBranch{index: index, writer: writer})
		}
		return nil
	})
	return branches, err
}

func (e *Engine) releaseStepBranches(ctx context.Context, run w.Run, branches []scheduledBranch) error {
	if len(branches) < 2 || run.TaskID == "" || !run.BudgetStarted {
		return nil
	}
	return e.Store.Write(ctx, func(tx *store.Tx) error {
		service := project.New(e.Store)
		for _, branch := range branches[1:] {
			if err := service.ReleaseStepBranch(tx, run, branch.writer); err != nil {
				return err
			}
		}
		return nil
	})
}
