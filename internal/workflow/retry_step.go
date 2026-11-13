package workflow

import (
	"errors"
	"fmt"
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// retriesPlanningStep reports whether the failed step is the coordinator's
// planning step of a free run that had not yet chosen a method. Its retry may
// still choose one, exactly as the step it replaces could.
func retriesPlanningStep(tx *store.Tx, source w.Run, stepID string) (bool, error) {
	if source.SelectedWorkflow != nil {
		return false, nil
	}
	steps, err := tx.Steps(source.ID)
	if err != nil {
		return false, err
	}
	for _, step := range steps {
		if step.ID == stepID {
			return selectionRoot(source, step), nil
		}
	}
	return false, nil
}

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
	if in := effectiveInput(source); in.Workflow == "" || in.Workflow == "free" {
		// A free run has no fixed plan to fall back on: whatever the failed step
		// still had to organise or deliver happens through the retry itself.
		onChain := map[string]bool{}
		for _, root := range previous {
			if !freeRoot(root) {
				continue
			}
			for _, member := range deliveryChain(previous, root) {
				onChain[member.ID] = true
			}
		}
		// A replanned member of a delivery chain whose predecessor is kept as-is
		// heads the retry's chain, with the abilities its place in the chain had.
		for i := range out {
			if !freeRoot(out[i]) && out[i].FromStepID == "" && onChain[out[i].RetryOfStepID] {
				out[i].Kind = kindRetryRoot
			}
		}
		if follow, ok := repairedResultContinuation(runID, target, selected[stepID], byID, results, onChain, messages, artifacts); ok && selected[follow.RetryOfStepID] == "" {
			for i := range out {
				if out[i].ID == selected[stepID] {
					out[i].ContinuationID = follow.ID
				}
			}
			out = append(out, follow)
		}
	}
	// A partial repair can finish its member's reply while another independent
	// member is still failed; it must not claim the entire workflow completed.
	if len(finalSteps(effectiveInput(source), out)) == 0 {
		for i := range out {
			if !slices.ContainsFunc(out, func(s w.Step) bool { return slices.Contains(s.DependsOn, out[i].ID) }) {
				out[i].Kind = kindRetryResult
			}
		}
	}
	return out, nil
}

// repairedResultPrompt is the instruction of the continuation that receives a
// repaired delegated result.
const repairedResultPrompt = "此前失败的成员任务已补跑并返回结果。结合你此前已交付的内容继续完成用户原始目标：如实更新受该结果影响的结论与交付物；只有确实需要时才从当前名册选择后续成员，否则直接交付。不要重复已完成的委派，也不要为确认收到而创建新任务。"

// repairedResultContinuation returns the delegator's follow-up for a repaired
// delegated task. A delegated member reports to the member that delegated it;
// when that member's continuation already settled on the failure, nothing else
// would read the repaired result, so it goes back in a fresh continuation. A
// continuation that never ran is replanned by the caller instead.
func repairedResultContinuation(runID string, target w.Step, repairedID string, byID, results map[string]w.Step, onChain map[string]bool, messages []w.Message, artifacts []w.Artifact) (w.Step, bool) {
	if target.Kind != "handoff" || target.ContinuationID == "" {
		return w.Step{}, false
	}
	settled, ok := byID[target.ContinuationID]
	if !ok || settled.Status != "succeeded" {
		return w.Step{}, false
	}
	follow := w.Step{
		ID: w.NewID(), RunID: runID, AgentID: settled.AgentID, AgentName: settled.AgentName,
		Role: settled.Role, Instruction: repairedResultPrompt, DependsOn: []string{repairedID},
		Status: "pending", Kind: kindRetryResult, RetryOfStepID: settled.ID, Depth: settled.Depth,
	}
	if onChain[settled.ID] {
		follow.Kind = kindRetryRoot
	}
	// The delegator reads its own newest reply, so a second repaired member
	// builds on the first repair instead of the reply that repair replaced.
	latest := results[settled.ID]
	for _, message := range messages {
		if message.RunID == latest.RunID && message.StepID == latest.ID && message.Status == "completed" {
			follow.ContextRefs = append(follow.ContextRefs, message.ID)
		}
	}
	for _, artifact := range artifacts {
		if artifact.RunID == latest.RunID && artifact.StepID == latest.ID {
			follow.ContextRefs = append(follow.ContextRefs, fmt.Sprintf("%s@%d", artifact.ID, artifact.Version))
		}
	}
	return follow, true
}
