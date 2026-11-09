package workflow

import (
	"errors"
	"fmt"
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func retryStepPlan(tx *store.Tx, runID string, source w.Run, stepID string) ([]w.Step, error) {
	previous, err := tx.Steps(source.ID)
	if err != nil {
		return nil, err
	}
	byID := map[string]w.Step{}
	for _, step := range previous {
		byID[step.ID] = step
	}
	target, ok := byID[stepID]
	if !ok || target.Status != "failed" && target.Status != "canceled" {
		return nil, errors.New("只能重试本轮中失败或已停止的成员步骤")
	}
	results := map[string]w.Step{}
	for _, step := range previous {
		if step.Status == "succeeded" {
			results[step.ID] = step
		}
	}
	history, err := tx.Runs(source.ThreadID)
	if err != nil {
		return nil, err
	}
	for _, repair := range history {
		if repair.Input.RetryOfRunID != source.ID || repair.RetryOfStepID == "" || repair.Status != "succeeded" {
			continue
		}
		completed, err := tx.Steps(repair.ID)
		if err != nil {
			return nil, err
		}
		for _, step := range completed {
			if original, exists := byID[step.RetryOfStepID]; exists && step.Status == "succeeded" {
				original.Status = "succeeded"
				byID[original.ID], results[original.ID] = original, step
			}
		}
	}
	selected := map[string]string{stepID: w.NewID()}
	for changed := true; changed; {
		changed = false
		for _, step := range previous {
			if selected[step.ID] != "" || byID[step.ID].Status != "skipped" && byID[step.ID].Status != "pending" {
				continue
			}
			related, ready := false, true
			for _, id := range step.DependsOn {
				related = related || selected[id] != ""
				ready = ready && (selected[id] != "" || byID[id].Status == "succeeded")
			}
			if related && ready {
				selected[step.ID], changed = w.NewID(), true
			}
		}
	}
	messages, err := tx.MessagesMetadata(source.ThreadID)
	if err != nil {
		return nil, err
	}
	artifacts, err := tx.ArtifactsMetadata(source.ThreadID)
	if err != nil {
		return nil, err
	}
	out := []w.Step{}
	for _, old := range previous {
		if selected[old.ID] == "" {
			continue
		}
		step := old
		step.ID, step.RunID, step.Status = selected[old.ID], runID, "pending"
		step.RetryOfStepID = old.ID
		step.Error, step.FailureKind, step.StartedAt, step.DeadlineAt, step.AttemptCount = "", "", "", "", 0
		step.ContextRefs, step.DependsOn = slices.Clone(old.ContextRefs), nil
		step.FromStepID, step.ContinuationID = selected[old.FromStepID], selected[old.ContinuationID]
		for _, dep := range old.DependsOn {
			if mapped := selected[dep]; mapped != "" {
				step.DependsOn = append(step.DependsOn, mapped)
				continue
			}
			if byID[dep].Status != "succeeded" {
				return nil, errors.New("该成员的上游步骤尚未成功，请先重试上游失败成员")
			}
			if dep == planningRootStep(source) || dep == source.TaskPromotionStepID {
				continue
			}
			result := results[dep]
			for _, message := range messages {
				if message.RunID == result.RunID && message.StepID == result.ID && message.Status == "completed" && !slices.Contains(step.ContextRefs, message.ID) {
					step.ContextRefs = append(step.ContextRefs, message.ID)
				}
			}
			for _, artifact := range artifacts {
				ref := fmt.Sprintf("%s@%d", artifact.ID, artifact.Version)
				if artifact.RunID == result.RunID && artifact.StepID == result.ID && !slices.Contains(step.ContextRefs, ref) {
					step.ContextRefs = append(step.ContextRefs, ref)
				}
			}
		}
		out = append(out, step)
	}
	// A partial repair can finish its member's reply while another independent
	// member is still failed; it must not claim the entire workflow completed.
	if len(finalSteps(effectiveInput(source), out)) == 0 {
		for i := range out {
			if !slices.ContainsFunc(out, func(s w.Step) bool { return slices.Contains(s.DependsOn, out[i].ID) }) {
				out[i].Kind = "retry_result"
			}
		}
	}
	return out, nil
}
