package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func projectTools(run w.Run) []model.Tool {
	if run.TaskID == "" {
		return nil
	}
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
	}
	refs := map[string]any{"type": "array", "maxItems": 32, "uniqueItems": true, "items": text(180)}
	out := []model.Tool{}
	if slices.Contains(run.GrantActions, "read") {
		out = append(out, model.Tool{Name: "read_task", Description: "Read a durable task and current accepted revision inside this task's authorized collaboration scope.", Schema: object(map[string]any{"task_id": text(120)}, "task_id")})
	}
	if slices.Contains(run.GrantActions, "collaborate") {
		out = append(out,
			model.Tool{Name: "ask_question", Description: "Ask the user for information or a decision that is required to continue this task. Do not ask about reversible assumptions you can state yourself. The current task invocation waits for the exact answer and then uses a budgeted continuation.", Schema: object(map[string]any{
				"blocking_scope": map[string]any{"type": "string", "enum": []string{"task"}},
				"body":           text(16000),
				"options":        map[string]any{"type": "array", "maxItems": 16, "uniqueItems": true, "items": text(1000)},
				"recommended":    text(1000),
			}, "blocking_scope", "body")},
			model.Tool{Name: "send_task_message", Description: "Send an exact correlated message to another task. Sender identity, attempt exposure and budget account are bound by the host.", Schema: object(map[string]any{
				"target_task_id": text(120), "kind": map[string]any{"type": "string", "enum": []string{"question", "evidence", "change_notice", "result"}},
				"body": text(16000), "request_id": text(120), "reply_to": text(120), "context_refs": refs,
				"requires_response": map[string]any{"type": "boolean"},
			}, "target_task_id", "kind", "body", "request_id")},
			model.Tool{Name: "delegate_task", Description: "Create one owned child task and its first budgeted run. The host derives parent identity, authorized actions, exposure and shared budget; this does not transfer parent ownership.", Schema: object(map[string]any{
				"owner_member_id": text(120), "goal": text(16000), "request_id": text(120), "context_refs": refs,
			}, "owner_member_id", "goal", "request_id")},
			model.Tool{Name: "yield_run", Description: "Finish this invocation and wait without polling for an exact host event or RFC3339 deadline. The host commits the yield only after the runner returns and only when no sibling branch is active.", Schema: object(map[string]any{
				"predicate": text(120), "observation_cursor": text(1000), "wake_source": text(120), "event_id": text(180),
			}, "predicate", "wake_source", "event_id")},
		)
	}
	if slices.Contains(run.GrantActions, "publish_artifact") {
		out = append(out,
			model.Tool{Name: "save_claim", Description: "Save a proposed or unresolved research claim with exact evidence locators. The host binds the producing attempt and inherits all exposure restrictions.", Schema: object(map[string]any{
				"statement": text(12000), "applicability": text(4000), "status": map[string]any{"type": "string", "enum": []string{"proposed", "unresolved"}},
				"method": text(4000), "assumptions": map[string]any{"type": "array", "maxItems": 32, "items": text(1000)},
				"limitations": map[string]any{"type": "array", "maxItems": 32, "items": text(1000)}, "as_of": text(80), "review_after": text(80),
				"evidence": map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "object", "properties": map[string]any{
					"source_id": text(180), "relation": map[string]any{"type": "string", "enum": []string{"supports", "contradicts", "contextualizes", "fails_to_detect"}},
					"locator": text(1000), "completeness": map[string]any{"type": "string", "enum": []string{"complete", "partial", "unavailable"}},
				}, "required": []string{"source_id", "relation", "locator", "completeness"}, "additionalProperties": false}},
			}, "statement", "status", "evidence")},
			model.Tool{Name: "save_memory", Description: "Save a candidate memory derived from exact sources and this attempt. It is not accepted merely because the model saved it.", Schema: object(map[string]any{
				"scope": text(120), "kind": text(80), "content": text(12000), "source_refs": refs,
				"source_locator": text(1000), "valid_from": text(80), "review_after": text(80),
			}, "scope", "kind", "content", "source_refs")},
			model.Tool{Name: "analyze_csv", Description: "Compute deterministic numeric statistics from one authorized immutable CSV capture and publish an SVG chart artifact. Uses no shell, network or original-file read; missing and invalid cells are counted explicitly.", Schema: object(map[string]any{
				"source_id": text(180), "column": text(256), "request_id": text(120),
			}, "source_id", "column", "request_id")},
			model.Tool{Name: "save_dataset", Description: "Save a dataset manifest for exact captured sources. No parser, statistics or chart capability is implied.", Schema: object(map[string]any{
				"source_refs": refs, "schema": text(8000), "time_range": text(1000), "acquired_at": text(80), "available_at": text(80),
				"processing_steps": map[string]any{"type": "array", "maxItems": 32, "items": text(1000)}, "missing_rules": text(4000),
				"content_digest": text(160), "location": text(1000),
			}, "source_refs", "schema", "acquired_at", "missing_rules")},
		)
	}
	return out
}

type yieldToolInput struct {
	Predicate         string `json:"predicate"`
	ObservationCursor string `json:"observation_cursor,omitempty"`
	WakeSource        string `json:"wake_source"`
	EventID           string `json:"event_id"`
}

func (x *stepRuntime) executeProjectTool(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
	service := project.New(x.engine.Store)
	respond := func(value any, err error) (string, bool, error) {
		if err != nil {
			return "", true, err
		}
		encoded, err := json.Marshal(value)
		return string(encoded), true, err
	}
	switch name {
	case "read_task":
		var in struct {
			TaskID string `json:"task_id"`
		}
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		err := x.engine.Store.Read(ctx, func(tx *store.Tx) error {
			current, err := tx.Task(x.run.TaskID)
			if err != nil {
				return err
			}
			target, err := tx.Task(in.TaskID)
			if err != nil {
				return err
			}
			if current.WorkspaceID != target.WorkspaceID || current.CollaborationScopeID != target.CollaborationScopeID {
				return errors.New("task is outside this attempt's collaboration scope")
			}
			return nil
		})
		if err != nil {
			return "", true, err
		}
		view, err := service.Task(ctx, in.TaskID)
		return respond(view, err)
	case "send_task_message":
		var in struct {
			TargetTaskID     string   `json:"target_task_id"`
			Kind             string   `json:"kind"`
			Body             string   `json:"body"`
			RequestID        string   `json:"request_id"`
			ReplyTo          string   `json:"reply_to,omitempty"`
			ContextRefs      []string `json:"context_refs,omitempty"`
			RequiresResponse bool     `json:"requires_response,omitempty"`
		}
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		view, err := service.SendTaskMessage(ctx, project.SendTaskMessageInput{
			SenderTaskID: x.run.TaskID, SenderAttemptID: x.attempt.ID, TargetTaskID: in.TargetTaskID,
			Kind: in.Kind, Body: in.Body, RequestID: in.RequestID, ReplyTo: in.ReplyTo,
			ContextRefs: in.ContextRefs, RequiresResponse: in.RequiresResponse,
		})
		if err == nil {
			x.mutated.Store(true)
		}
		return respond(view, err)
	case "delegate_task":
		var in struct {
			OwnerMemberID string   `json:"owner_member_id"`
			Goal          string   `json:"goal"`
			RequestID     string   `json:"request_id"`
			ContextRefs   []string `json:"context_refs,omitempty"`
		}
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		view, err := service.DelegateTask(ctx, project.DelegateTaskInput{
			ParentTaskID: x.run.TaskID, ParentAttemptID: x.attempt.ID, OwnerMemberID: in.OwnerMemberID,
			Goal: in.Goal, RequestID: in.RequestID, ContextRefs: in.ContextRefs,
		})
		if err == nil {
			x.mutated.Store(true)
		}
		return respond(view, err)
	case "ask_question":
		var in struct {
			BlockingScope string   `json:"blocking_scope"`
			Body          string   `json:"body"`
			Options       []string `json:"options"`
			Recommended   string   `json:"recommended"`
		}
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		question, err := service.AskQuestion(ctx, x.run.TaskID, in.BlockingScope, in.Body, in.Options, in.Recommended)
		if err != nil {
			return "", true, err
		}
		x.mu.Lock()
		if x.yieldRequest != nil {
			x.mu.Unlock()
			return "", true, errors.New("a wait is already requested for this invocation")
		}
		x.yieldRequest = &yieldToolInput{Predicate: "event", WakeSource: "question", EventID: question.ID}
		x.mu.Unlock()
		return respond(question, nil)
	case "yield_run":
		var in yieldToolInput
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		x.mu.Lock()
		if x.yieldRequest != nil {
			x.mu.Unlock()
			return "", true, errors.New("yield is already requested for this invocation")
		}
		x.yieldRequest = &in
		x.mu.Unlock()
		return `{"accepted":true,"settlement":"after_runner_return"}`, true, nil
	case "save_claim":
		var in w.Claim
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		in.ID, in.WorkspaceID, in.TaskID, in.ProducerAttemptID, in.Revision = w.NewID(), x.run.WorkspaceID, x.run.TaskID, x.attempt.ID, 1
		value, err := service.SaveClaim(ctx, in)
		return respond(value, err)
	case "save_memory":
		var in w.MemoryRecord
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		in.ID, in.WorkspaceID, in.ProducerAttemptID, in.Revision = w.NewID(), x.run.WorkspaceID, x.attempt.ID, 1
		in.ClaimStatus, in.AcceptedBy = "candidate", "task:"+x.run.TaskID
		value, err := service.SaveMemory(ctx, in)
		return respond(value, err)
	case "analyze_csv":
		var in struct {
			SourceID  string `json:"source_id"`
			Column    string `json:"column"`
			RequestID string `json:"request_id"`
		}
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		value, err := service.AnalyzeCSV(ctx, project.AnalyzeCSVInput{
			TaskID: x.run.TaskID, RunID: x.run.ID, StepID: x.step.ID, AttemptID: x.attempt.ID,
			SourceID: in.SourceID, Column: in.Column, RequestID: in.RequestID,
		})
		if err == nil {
			x.mutated.Store(true)
		}
		return respond(value, err)
	case "save_dataset":
		var in w.DatasetManifest
		if err := tools.Decode(input, &in); err != nil {
			return "", true, err
		}
		in.ID, in.WorkspaceID, in.TaskID, in.ProducerAttemptID, in.Revision = w.NewID(), x.run.WorkspaceID, x.run.TaskID, x.attempt.ID, 1
		value, err := service.SaveDataset(ctx, in)
		return respond(value, err)
	default:
		return "", false, nil
	}
}
