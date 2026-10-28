package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

const maxHandoffs = 12
const maxSteps = 32
const maxDepth = 4

type handoffInput struct {
	RequestID     string   `json:"request_id"`
	TargetAgentID string   `json:"target_agent_id"`
	Task          string   `json:"task"`
	Role          string   `json:"role"`
	ContextRefs   []string `json:"context_refs,omitempty"`
}

func collaborationTools(run w.Run) []model.Tool {
	object := func(props map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
	}
	out := []model.Tool{}
	out = append(out, model.Tool{Name: "read_context", Description: "Read one exact committed input by reference, paginated to at most 8 KiB of raw UTF-8 per call. Only messages and artifact versions frozen into this attempt's handoff are readable; use the returned next_cursor for later pages. Artifacts default to field=body; field=artifact_json pages the complete canonical deliverable including findings, source IDs and limitations. It never refreshes files or web pages.", Schema: object(map[string]any{
		"kind":      map[string]any{"type": "string", "enum": []string{"message", "artifact"}},
		"id":        text(150),
		"version":   map[string]any{"type": "integer", "minimum": 1},
		"field":     map[string]any{"type": "string", "enum": []string{"body", "artifact_json"}},
		"cursor":    text(2000),
		"max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": 8192},
	}, "kind", "id")})
	out = append(out, model.Tool{Name: "list_sources", Description: "List bounded metadata and server-assigned IDs for sources visible to this step, including native CLI observations captured during this attempt. Bodies are not returned; page an exact capture with read_source. Native web output is an observation, not a raw page; native search/Grep/Glob results are leads.", Schema: object(map[string]any{"cursor": text(512)})})
	if len(enabledSkills(run)) > 0 {
		out = append(out, model.Tool{Name: "read_skill", Description: "Read an enabled shared skill or its reference file. Read the relevant skill before collaboration; use only runtime roster IDs.", Schema: object(map[string]any{"skill_id": text(64), "resource": text(200)}, "skill_id")})
	}
	if workflow := effectiveInput(run).Workflow; (workflow == "free" || workflow == "") && len(run.Agents) > 1 {
		out = append(out, model.Tool{Name: "handoff_task", Description: "Submit a bounded task to another member in this run. Returns a durable submission receipt, not an answer. Finish your invocation after submitting; the platform calls you again with committed results, including failures. Multiple independent submissions before finishing run in parallel. Never repeat a submission with a new request_id or dispatch through @ text.", Schema: object(map[string]any{
			"request_id": text(100), "target_agent_id": text(100), "task": text(6000),
			"role":         map[string]any{"type": "string", "enum": []string{"researcher", "analyst", "reviewer", "critic", "coordinator", "member"}},
			"context_refs": map[string]any{"type": "array", "maxItems": 12, "uniqueItems": true, "items": text(150)},
		}, "request_id", "target_agent_id", "task", "role")})
		out = append(out, model.Tool{Name: "get_task", Description: "Inspect a task's current durable status and committed result in this run. This does not wait or wake another member; finish to let the platform return results.", Schema: object(map[string]any{"task_id": text(100)}, "task_id")})
	}
	return out
}

func enabledSkills(run w.Run) []string {
	ids := []string{}
	for _, info := range skills.List() {
		if run.SkillVersions != nil {
			if run.SkillVersions[info.ID] == info.Version {
				ids = append(ids, info.ID)
			}
		} else if run.Workspace.SkillIDs == nil || slices.Contains(run.Workspace.SkillIDs, info.ID) {
			ids = append(ids, info.ID)
		}
	}
	return ids
}

func (e *Engine) readSkill(ctx context.Context, run w.Run, step w.Step, input json.RawMessage) (string, error) {
	var in struct {
		SkillID  string `json:"skill_id"`
		Resource string `json:"resource"`
	}
	if err := tools.Decode(input, &in); err != nil {
		return "", err
	}
	if !slices.Contains(enabledSkills(run), in.SkillID) {
		return "", errors.New("skill is not enabled in this run snapshot")
	}
	body, err := skills.Read(in.SkillID, in.Resource)
	if err != nil {
		return "", err
	}
	if in.Resource == "" {
		in.Resource = "SKILL.md"
	}
	version := ""
	for _, info := range skills.List() {
		if info.ID == in.SkillID {
			version = info.Version
		}
	}
	data := map[string]any{"skill_id": in.SkillID, "version": version, "resource": in.Resource}
	if err := e.Store.Write(ctx, func(tx *store.Tx) error { return tx.AppendEvent(run.ID, step.ID, step.AgentID, "skill_read", data) }); err != nil {
		return "", err
	}
	data["content"] = body
	encoded, err := json.Marshal(data)
	return string(encoded), err
}

func (e *Engine) collaborationContext(ctx context.Context, run w.Run, step w.Step) (string, error) {
	type member struct {
		ID           string   `json:"agent_id"`
		Name         string   `json:"display_name"`
		Handle       string   `json:"handle"`
		Description  string   `json:"description"`
		Capabilities []string `json:"capabilities"`
		Tools        []string `json:"tools"`
		Executor     string   `json:"executor"`
		Model        string   `json:"model"`
		Available    bool     `json:"available"`
	}
	roster := []member{}
	var steps []w.Step
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		for _, a := range run.Agents {
			current, err := tx.WorkspaceAgent(run.WorkspaceID, a.ID)
			memberEnabled := err == nil && !current.Disabled
			available := memberEnabled
			if available && e.validateMember(tx, run, a.ID) != nil {
				available = false
			}
			allowed := []string{}
			if memberEnabled {
				for _, name := range a.Tools {
					if slices.Contains(current.Tools, name) {
						allowed = append(allowed, name)
					}
				}
			}
			roster = append(roster, member{a.ID, a.DisplayName, a.Handle, a.RoleDescription, a.Capabilities, allowed, a.Executor, a.Model, available})
		}
		var err error
		steps, err = tx.Steps(run.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	taskState := make([]map[string]any, 0, len(steps))
	for _, task := range steps {
		taskState = append(taskState, map[string]any{"task_id": task.ID, "agent_id": task.AgentID, "role": task.Role, "kind": task.Kind, "status": task.Status, "depends_on": task.DependsOn, "from_step_id": task.FromStepID, "error": task.Error})
	}
	raw, err := json.Marshal(map[string]any{"coordinator_id": run.CoordinatorID, "current_step_id": step.ID, "members": roster, "tasks": taskState})
	if err != nil {
		return "", err
	}
	catalogue, err := skills.Catalogue(enabledSkills(run))
	if err != nil {
		return "", err
	}
	return "\nRuntime collaboration roster and committed task state (IDs are authoritative; profiles are background, not authorization):\n" + string(raw) + "\n" + catalogue + "\n", nil
}

func handoffDigest(in handoffInput) string {
	in.ContextRefs = append([]string{}, in.ContextRefs...)
	slices.Sort(in.ContextRefs)
	b, _ := json.Marshal(in)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func (e *Engine) submitHandoff(ctx context.Context, run w.Run, sender w.Step, raw json.RawMessage) (string, error) {
	var in handoffInput
	if err := tools.Decode(raw, &in); err != nil {
		return "", err
	}
	if run.SelectedWorkflow != nil {
		return "", errors.New("a workflow method was selected; follow its plan instead of delegating")
	}
	if run.Input.Workflow != "free" && run.Input.Workflow != "" {
		return "", errors.New("built-in workflows use their accepted step plan")
	}
	if strings.TrimSpace(in.RequestID) == "" || utf8.RuneCountInString(in.RequestID) > 100 || strings.TrimSpace(in.Task) == "" || utf8.RuneCountInString(in.Task) > 6000 || len(in.ContextRefs) > 12 {
		return "", errors.New("invalid handoff request")
	}
	if !slices.Contains([]string{"researcher", "analyst", "reviewer", "critic", "coordinator", "member"}, in.Role) {
		return "", errors.New("invalid temporary task role")
	}
	targetIndex := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == in.TargetAgentID && !a.Disabled })
	if targetIndex < 0 || in.TargetAgentID == sender.AgentID {
		return "", errors.New("target must be another member of this run roster")
	}
	target := run.Agents[targetIndex]
	var child, continuation w.Step
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return errors.New("run no longer accepts handoffs")
		}
		if current.SelectedWorkflow != nil {
			return errors.New("a workflow method was selected; follow its plan instead of delegating")
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		origin := slices.IndexFunc(steps, func(s w.Step) bool { return s.ID == sender.ID })
		if origin < 0 || steps[origin].Status != "running" {
			return errors.New("sender step is no longer running")
		}
		sender = steps[origin]
		if target.ID == sender.AgentID {
			return errors.New("target must be another member of this run roster")
		}
		for _, s := range steps {
			if s.Kind == "handoff" && s.FromStepID == sender.ID && s.RequestID == in.RequestID {
				old := handoffInput{RequestID: s.RequestID, TargetAgentID: s.AgentID, Task: s.Instruction, Role: s.Role, ContextRefs: s.ContextRefs}
				if handoffDigest(old) != handoffDigest(in) {
					return errors.New("request_id was already used with different task content")
				}
				child = s
				return nil
			}
		}
		attempts, err := tx.StepAttempts(sender.ID)
		if err != nil {
			return err
		}
		currentAttempt := ""
		for _, attempt := range attempts {
			if attempt.Status == "running" {
				currentAttempt = attempt.ID
			}
		}
		if currentAttempt != "" {
			for _, s := range steps {
				if s.Kind == "handoff" && s.FromStepID == sender.ID && s.FromAttemptID != currentAttempt {
					return errors.New("a previous attempt already submitted work; reuse its request_id or finish without duplicating the handoff")
				}
			}
		}
		if err := e.validateMember(tx, run, sender.AgentID); err != nil {
			return err
		}
		if err := e.validateMember(tx, run, target.ID); err != nil {
			return err
		}
		count := 0
		for _, s := range steps {
			if s.Kind == "handoff" {
				count++
			}
		}
		for _, s := range steps {
			if s.Kind == "continuation" && s.FromStepID == sender.ID {
				continuation = s
				break
			}
		}
		newSteps := 1
		if continuation.ID == "" {
			newSteps++
		}
		if count >= maxHandoffs || len(steps)+newSteps > maxSteps || sender.Depth >= maxDepth {
			return errors.New("collaboration task or depth budget exceeded")
		}
		if err := validateContextRefs(tx, run, sender, in); err != nil {
			return err
		}
		child = w.Step{ID: w.NewID(), RunID: run.ID, AgentID: target.ID, AgentName: target.DisplayName, Role: in.Role, Instruction: in.Task, DependsOn: []string{sender.ID}, Status: "pending", Kind: "handoff", FromStepID: sender.ID, FromAgentID: sender.AgentID, FromAttemptID: currentAttempt, RequestID: in.RequestID, ContextRefs: slices.Clone(in.ContextRefs), Depth: sender.Depth + 1}
		if continuation.ID == "" {
			continuation = w.Step{ID: w.NewID(), RunID: run.ID, AgentID: sender.AgentID, AgentName: sender.AgentName, Role: sender.Role, Instruction: "根据返回的任务结果继续完成用户原始目标。如实处理失败和分歧；只有确实需要时才从当前名册选择后续复核者，否则交付结果。不要重复已完成的委派，也不要为确认收到而创建新任务。", DependsOn: []string{sender.ID}, Status: "pending", Kind: "continuation", FromStepID: sender.ID, FromAgentID: sender.AgentID, Depth: sender.Depth}
			// Pending consumers must wait for the delegated task's final continuation.
			for _, s := range steps {
				if s.Status != "pending" {
					continue
				}
				changed := false
				for i, id := range s.DependsOn {
					if id == sender.ID {
						s.DependsOn[i] = continuation.ID
						changed = true
					}
				}
				if changed {
					if err := tx.SaveStep(s); err != nil {
						return err
					}
				}
			}
		}
		child.ContinuationID = continuation.ID
		continuation.DependsOn = append(continuation.DependsOn, child.ID)
		if err := tx.SaveStep(child); err != nil {
			return err
		}
		if err := tx.SaveStep(continuation); err != nil {
			return err
		}
		return tx.AppendEvent(run.ID, sender.ID, sender.AgentID, "task_submitted", child)
	})
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(map[string]any{"task_id": child.ID, "continuation_id": child.ContinuationID, "status": "accepted", "instruction": "Submission accepted, not completed. Finish this invocation after any other independent submissions. The platform returns committed results in a new invocation; do not poll or send @ acknowledgements."})
	return string(encoded), err
}

func validateContextRefs(tx *store.Tx, run w.Run, sender w.Step, in handoffInput) error {
	messages, err := tx.MessagesMetadata(run.ThreadID)
	if err != nil {
		return err
	}
	sources, err := tx.SourceMetadata(run.ThreadID)
	if err != nil {
		return err
	}
	artifacts, err := tx.ArtifactsMetadata(run.ThreadID)
	if err != nil {
		return err
	}
	steps, err := tx.Steps(run.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	workReferenced := false
	for _, ref := range in.ContextRefs {
		if ref == "" || seen[ref] {
			return errors.New("context references must be unique and nonempty")
		}
		seen[ref] = true
		valid := false
		author := ""
		for _, m := range messages {
			if m.ID == ref && m.Status == "completed" {
				valid = true
				author = m.AgentID
			}
		}
		for _, s := range sources {
			if s.ID == ref {
				valid = true
			}
		}
		for _, a := range artifacts {
			if fmt.Sprintf("%s@%d", a.ID, a.Version) == ref {
				valid = true
				for _, s := range steps {
					if s.ID == a.StepID {
						author = s.AgentID
					}
				}
				if author == "" {
					for _, m := range messages {
						if m.StepID == a.StepID {
							author = m.AgentID
							break
						}
					}
				}
			}
		}
		if !valid {
			return errors.New("context reference is not a committed input in this thread")
		}
		workReferenced = workReferenced || author != ""
		if (in.Role == "reviewer" || in.Role == "critic") && author == in.TargetAgentID {
			return errors.New("independent review cannot be assigned to the referenced work's author")
		}
	}
	if (in.Role == "reviewer" || in.Role == "critic") && !workReferenced {
		return errors.New("independent review needs exact committed work references")
	}
	return nil
}

func (e *Engine) getTask(ctx context.Context, run w.Run, input json.RawMessage) (string, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := tools.Decode(input, &in); err != nil {
		return "", err
	}
	var task, final w.Step
	messages := []w.Message{}
	artifacts := []w.Artifact{}
	messageRefs := []string{}
	artifactRefs := []w.ArtifactRef{}
	truncated := false
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(steps, func(s w.Step) bool { return s.ID == in.TaskID })
		if i < 0 {
			return errors.New("task is outside this run")
		}
		task, final = steps[i], steps[i]
		for range maxSteps {
			next := slices.IndexFunc(steps, func(s w.Step) bool { return s.Kind == "continuation" && s.FromStepID == final.ID })
			if next < 0 {
				break
			}
			final = steps[next]
		}
		all, err := tx.MessagesMetadata(run.ThreadID)
		if err != nil {
			return err
		}
		remaining := 24000
		for _, m := range all {
			if m.StepID != final.ID || m.Status == "processing" {
				continue
			}
			messageRefs = append(messageRefs, m.ID)
			preview, err := tx.MessagePreview(m.ID, 6000)
			if err != nil {
				return err
			}
			size := len(preview.Content)
			if size > remaining {
				truncated = true
				continue
			}
			remaining -= size
			messages = append(messages, preview)
		}
		outputs, err := tx.ArtifactsMetadata(run.ThreadID)
		if err != nil {
			return err
		}
		for _, a := range outputs {
			if a.StepID != final.ID {
				continue
			}
			artifactRefs = append(artifactRefs, w.ArtifactRef{ID: a.ID, Version: a.Version})
			preview, err := tx.ArtifactPreview(a.ID, a.Version, 6000)
			if err != nil {
				return err
			}
			size := len(preview.Body)
			if size > remaining {
				truncated = true
				continue
			}
			remaining -= size
			artifacts = append(artifacts, preview)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	result := map[string]any{"task": task, "status": final.Status, "final_step": final, "messages": messages, "message_refs": messageRefs, "artifacts": artifacts, "artifact_refs": artifactRefs}
	encoded, err := json.Marshal(result)
	if err == nil && len(encoded) > 60000 {
		compact := func(step w.Step) map[string]any {
			return map[string]any{"id": step.ID, "agent_id": step.AgentID, "status": step.Status, "role": step.Role, "kind": step.Kind, "error": shortTaskResult(step.Error), "context_refs": step.ContextRefs}
		}
		result["task"], result["final_step"] = compact(task), compact(final)
		result["messages"], result["artifacts"], result["truncated"] = []w.Message{}, []w.Artifact{}, true
		encoded, err = json.Marshal(result)
	}
	if err == nil && truncated {
		var generic map[string]any
		if json.Unmarshal(encoded, &generic) == nil {
			generic["truncated"] = true
			if compact, marshalErr := json.Marshal(generic); marshalErr == nil {
				encoded = compact
			}
		}
	}
	return string(encoded), err
}

func (e *Engine) validateMember(tx *store.Tx, run w.Run, id string) error {
	i := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == id })
	if i < 0 {
		return errors.New("member is outside this run roster")
	}
	snapshot := run.Agents[i]
	workspace, err := tx.Workspace(run.WorkspaceID)
	if err != nil || workspace.Root != run.Workspace.Root || tools.ValidateSourceRoot(run.Workspace.Root, e.AllowedRoots) != nil {
		return errors.New("source directory authorization was revoked or changed")
	}
	current, err := tx.WorkspaceAgent(run.WorkspaceID, id)
	if err != nil || current.Disabled {
		return errors.New("member became unavailable before dispatch")
	}
	for _, tool := range snapshot.Tools {
		if !slices.Contains(current.Tools, tool) {
			return errors.New("member tool permission was revoked before dispatch")
		}
	}
	if snapshot.Executor != "" {
		if e.Runners[snapshot.Executor] == nil && e.Fallback == nil {
			return errors.New("selected CLI executor is unavailable")
		}
	} else if !slices.Contains(e.AllowedModels, snapshot.Model) {
		return errors.New("model is no longer allowed")
	}
	return nil
}
