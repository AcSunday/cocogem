package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type taskPromotionInput struct {
	Title         string `json:"title"`
	Goal          string `json:"goal"`
	EvidenceQuote string `json:"evidence_quote"`
}

func taskIntakeTools(run w.Run) []model.Tool {
	if !run.TaskIntake || run.TaskID != "" {
		return nil
	}
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
	}
	return []model.Tool{{Name: "promote_to_task", Description: "Record a durable, user-requested objective in this conversation before executing deliverable work. Use for implementation/testing, producing a requested file or report, multiple dependent deliverables, or ongoing work. Simple explanations, quick comparisons, single lookups, hypothetical or negated requests stay as chat. Naming a member, choosing a workflow or using a tool alone is not a task. Quote an exact unquoted instruction from the current user message; resolve short acceptances using the visible conversation and preserve its constraints. The host binds the message, conversation, permissions and task identity. After acceptance, finish this invocation immediately; a fresh execution step continues the same conversation.", Schema: map[string]any{"type": "object", "properties": map[string]any{"title": text(150), "goal": text(16000), "evidence_quote": text(16000)}, "required": []string{"title", "goal", "evidence_quote"}, "additionalProperties": false}}}
}

func (e *Engine) promoteTask(ctx context.Context, run w.Run, sender w.Step, raw json.RawMessage) (string, error) {
	var in taskPromotionInput
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Title) == "" || utf8.RuneCountInString(in.Title) > 150 || strings.TrimSpace(in.Goal) == "" || utf8.RuneCountInString(in.Goal) > 16000 || strings.TrimSpace(in.EvidenceQuote) == "" || !strings.Contains(fenced.ReplaceAllString(run.Input.Message, ""), in.EvidenceQuote) {
		return "", errors.New("promotion requires a bounded goal and an exact unquoted user instruction")
	}
	var taskID string
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" || !current.TaskIntake {
			return w.ErrConflict
		}
		if current.TaskID != "" {
			if current.TaskPromotionDigest != project.Digest(in) || current.TaskPromotionStepID != sender.ID {
				return w.ErrConflict
			}
			taskID = current.TaskID
			return nil
		}
		if err := e.validateMember(tx, current, sender.AgentID); err != nil {
			return err
		}
		steps, err := tx.Steps(current.ID)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(steps, func(s w.Step) bool { return s.ID == sender.ID && s.Status == "running" })
		if index < 0 || len(steps) >= maxSteps {
			return w.ErrConflict
		}
		origin := steps[index]
		th, err := tx.Thread(current.ThreadID)
		if err != nil {
			return err
		}
		if th.TaskID != "" {
			return w.ErrConflict
		}
		messages, err := tx.MessagesMetadata(th.ID)
		if err != nil {
			return err
		}
		messageID := ""
		for _, m := range messages {
			if m.RunID == current.ID && m.Role == "user" && m.Status == "completed" {
				messageID = m.ID
			}
		}
		if messageID == "" {
			return errors.New("the accepted user message is unavailable")
		}
		if _, err := tx.Workspace(current.WorkspaceID); err != nil {
			return err
		}
		service := project.New(e.Store)
		policy, err := service.CurrentPolicy(tx)
		if err != nil {
			return err
		}
		view, err := service.CreateTaskInTx(tx, project.CreateTaskInput{WorkspaceID: th.WorkspaceID, ThreadID: th.ID, Title: in.Title, Goal: in.Goal, OwnerMemberID: current.CoordinatorID, AcceptedBy: "user", SourceMessageID: messageID, InputRefs: current.Input.SourceIDs, AllowedActions: project.TaskActions(policy.Mode, current.Input)})
		if err != nil {
			return err
		}
		current.TaskID, current.TaskRevision, current.BudgetAccountID = view.Task.ID, view.Task.CurrentRevision, view.Budget.ID
		current.TaskPromotionStepID, current.TaskPromotionDigest = origin.ID, project.Digest(in)
		if err := service.EnsureRunAuthorization(tx, &current, messageID); err != nil {
			return err
		}
		manifest, err := service.PrepareContextInTx(tx, current.TaskID, current.TaskRevision, project.ContextHostInput(current))
		if err != nil {
			return err
		}
		current.ContextRef, current.InputDigest = manifest.ID, project.ContextInputDigest(view, manifest, messageID, current.Input)
		if err := tx.TaskRunStarted(&current); err != nil {
			return err
		}
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		next := origin
		next.ID, next.Status, next.Kind, next.FromStepID = w.NewID(), "pending", "continuation", origin.ID
		next.DependsOn = []string{origin.ID}
		next.Instruction = "Continue the accepted durable objective with its frozen task context. " + origin.Instruction
		for _, step := range steps {
			if step.Status != "pending" {
				continue
			}
			for i, id := range step.DependsOn {
				if id == origin.ID {
					step.DependsOn[i] = next.ID
				}
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		if err := tx.SaveStep(next); err != nil {
			return err
		}
		taskID = current.TaskID
		return tx.AppendEvent(current.ID, origin.ID, origin.AgentID, "task_promoted", map[string]any{"task_id": taskID, "title": in.Title, "source_message_id": messageID, "evidence_quote": in.EvidenceQuote})
	})
	if err != nil {
		return "", err
	}
	instruction := "Finish this invocation now. The host continues the accepted objective in a fresh execution step in this same conversation."
	if len(run.Agents) > 1 {
		instruction = "Task promoted. If teammates should execute or review this work, call handoff_task (prefer mode='relay' for peer-to-peer execution and review) or choose_workflow right now in this same invocation, then finish. Only finish without dispatching if you will execute the entire task yourself in the next step."
	}
	encoded, err := json.Marshal(map[string]any{"task_id": taskID, "accepted": true, "instruction": instruction})
	return string(encoded), err
}
