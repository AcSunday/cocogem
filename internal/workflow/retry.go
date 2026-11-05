package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// MaxActiveRuns matches the API's accepted queue capacity.
const MaxActiveRuns = 21

// ErrQueueFull lets the HTTP boundary keep queue exhaustion distinct from
// other retry validation failures.
var ErrQueueFull = errors.New("执行队列已满")

// Retry queues a new run from the same accepted input as a failed or canceled
// run. It never revives the old attempt history: the source run stays terminal,
// and the retry is linked through Input.RetryOfRunID.
func (e *Engine) Retry(ctx context.Context, id string) (w.Run, error) {
	var source w.Run
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		source, err = tx.Run(id)
		if err != nil {
			return err
		}
		_, err = tx.Thread(source.ThreadID)
		return err
	}); err != nil {
		return source, err
	}
	if source.Status != "failed" && source.Status != "canceled" {
		return source, w.ErrConflict
	}
	if e.Model == nil && len(e.Runners) == 0 && e.Fallback == nil {
		return source, errors.New("model is unavailable; cannot retry")
	}

	var retried w.Run
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(id)
		if err != nil {
			return err
		}
		if _, err := tx.Thread(current.ThreadID); err != nil {
			return err
		}
		if current.Status != "failed" && current.Status != "canceled" {
			return w.ErrConflict
		}
		thread, err := tx.Thread(current.ThreadID)
		if err != nil {
			return err
		}
		workspace, err := tx.Workspace(current.WorkspaceID)
		if err != nil {
			return err
		}
		active, err := tx.Active()
		if err != nil {
			return err
		}
		for _, other := range active {
			if other.ThreadID != current.ThreadID {
				continue
			}
			// Repeated clicks while the retry is queued return the accepted run
			// instead of creating duplicates.
			if other.Input.RetryOfRunID == current.ID {
				retried = other
				return nil
			}
			return w.ErrConflict
		}
		if len(active) >= MaxActiveRuns {
			return ErrQueueFull
		}
		agents, err := tx.Agents(workspace.ID)
		if err != nil {
			return err
		}
		input := effectiveInput(current)
		input.ProposalID = ""
		input.RetryOfRunID = current.ID
		if strings.TrimSpace(input.Message) == "" || len([]rune(input.Message)) > 16000 {
			return errors.New("问题必须为 1–16000 个字符")
		}
		if len(input.SourceURLs) > 10 || len(input.ArtifactRefs) > 5 {
			return errors.New("资料引用过多")
		}
		for _, u := range input.SourceURLs {
			if _, err := tools.ValidateURL(u); err != nil {
				return err
			}
		}
		if (input.Workflow == "code_analysis" || input.Workflow == "engineering") && workspace.Root == "" {
			return errors.New("代码分析与工程协作需要绑定允许的资料目录")
		}
		for _, ref := range input.ArtifactRefs {
			artifact, err := tx.Artifact(ref.ID, ref.Version)
			if err != nil || artifact.ThreadID != thread.ID {
				return errors.New("产物引用不属于当前会话")
			}
		}
		runID := w.NewID()
		steps, err := Plan(runID, input, agents)
		if err != nil {
			return err
		}
		policy := e.Limits
		coordinatorID := thread.CoordinatorID
		if coordinatorID == "" {
			coordinatorID = current.CoordinatorID
		}
		issueID := current.BudgetIssueID
		if issueID == "" {
			issueID = current.ID
		}
		run := w.Run{
			ID:                    runID,
			WorkspaceID:           workspace.ID,
			Workspace:             workspace,
			ThreadID:              thread.ID,
			Input:                 input,
			Routing:               current.Routing,
			Status:                "queued",
			CreatedAt:             w.Now(),
			CoordinatorID:         coordinatorID,
			SkillVersions:         map[string]string{},
			TaskID:                current.TaskID,
			TaskRevision:          current.TaskRevision,
			PolicyVersion:         current.PolicyVersion,
			BudgetAccountID:       current.BudgetAccountID,
			GrantSnapshot:         current.GrantSnapshot,
			InputDigest:           current.InputDigest,
			ContinuedFromRun:      current.ID,
			BudgetIssueID:         issueID,
			AutomaticContinuation: current.TaskID != "",
			WriteBranch:           current.WriteBranch,
			Delivery:              &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}},
			ExecutionPolicy: &w.ExecutionPolicy{
				RunTimeout:    policy.RunTimeout,
				StepTimeout:   policy.StepTimeout,
				FirstResponse: policy.FirstResponse,
				Stall:         policy.Stall,
				StallAction:   policy.StallAction,
			},
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		for _, skill := range skills.List() {
			if workspace.SkillIDs == nil || slices.Contains(workspace.SkillIDs, skill.ID) {
				run.SkillVersions[skill.ID] = skill.Version
			}
		}
		if input.Workflow == "free" || input.Workflow == "" {
			run.SkillSnapshot, err = skills.SnapshotHash(workspace.SkillIDs)
			if err != nil {
				return err
			}
		}
		for _, step := range steps {
			index := slices.IndexFunc(agents, func(agent w.Agent) bool { return agent.ID == step.AgentID })
			if index < 0 {
				return errors.New("selected member is unavailable")
			}
			member := agents[index]
			if !slices.ContainsFunc(run.Agents, func(agent w.Agent) bool { return agent.ID == member.ID }) {
				run.Agents = append(run.Agents, member)
			}
			if !slices.Contains(thread.AgentIDs, member.ID) {
				thread.AgentIDs = append(thread.AgentIDs, member.ID)
			}
		}
		if input.Workflow == "free" || input.Workflow == "" {
			for _, member := range agents {
				if !member.Disabled && slices.Contains(thread.AgentIDs, member.ID) && !slices.ContainsFunc(run.Agents, func(agent w.Agent) bool { return agent.ID == member.ID }) {
					run.Agents = append(run.Agents, member)
				}
			}
		}
		if len(thread.AgentIDs) > 10 {
			return errors.New("会话不能超过十个成员")
		}
		thread.UpdatedAt = w.Now()
		thread.Revision++
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := prepareRetryContext(tx, &run); err != nil {
			return err
		}
		body, err := json.Marshal(input)
		if err != nil {
			return err
		}
		key := "retry-" + current.ID + "-" + w.NewID()
		if err := tx.InsertRun(run, key, contentDigest(string(body))); err != nil {
			return err
		}
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		retried = run
		return tx.AppendEvent(run.ID, "", "", "run_queued", run)
	})
	if err != nil {
		return source, err
	}
	return retried, nil
}
