package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCollaborationTools(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		workflow  string
		members   int
		skillIDs  []string
		wantNames []string
	}{
		{name: "free collaboration", workflow: "free", members: 3, wantNames: []string{"read_context", "list_sources", "read_skill", "handoff_task", "get_task"}},
		{name: "implicit free collaboration", members: 2, wantNames: []string{"read_context", "list_sources", "read_skill", "handoff_task", "get_task"}},
		{name: "fixed workflow", workflow: "research", members: 3, wantNames: []string{"read_context", "list_sources", "read_skill"}},
		{name: "single member", workflow: "free", members: 1, wantNames: []string{"read_context", "list_sources", "read_skill"}},
		{name: "skills disabled", workflow: "free", members: 3, skillIDs: []string{}, wantNames: []string{"read_context", "list_sources", "handoff_task", "get_task"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			run := w.Run{Input: w.RunInput{Workflow: tt.workflow}, Agents: make([]w.Agent, tt.members), Workspace: w.Workspace{SkillIDs: tt.skillIDs}}
			defs := collaborationTools(run)
			names := []string{}
			for _, def := range defs {
				names = append(names, def.Name)
			}
			if !slices.Equal(names, tt.wantNames) {
				t.Fatalf("tools = %v, want %v", names, tt.wantNames)
			}
		})
	}
	defs := collaborationTools(w.Run{Agents: make([]w.Agent, 2)})
	for _, def := range defs {
		t.Run("schema/"+def.Name, func(t *testing.T) {
			t.Parallel()
			schema := compileCollaborationSchema(t, def)
			var valid map[string]any
			switch def.Name {
			case "read_context":
				valid = map[string]any{"kind": "artifact", "id": w.NewID(), "version": 1}
			case "read_skill":
				valid = map[string]any{"skill_id": "task-handoff"}
			case "handoff_task":
				valid = map[string]any{"request_id": "research-1", "target_agent_id": w.NewID(), "task": "Compare the allowed sources", "role": "researcher"}
			case "get_task":
				valid = map[string]any{"task_id": w.NewID()}
			case "list_sources":
				valid = map[string]any{}
			}
			if err := schema.Validate(valid); err != nil {
				t.Fatalf("valid tool input rejected: %v", err)
			}
			if def.Name != "list_sources" {
				if err := schema.Validate(map[string]any{}); err == nil {
					t.Fatal("schema allowed missing required fields")
				}
			}
			valid["shell"] = "unregistered"
			if err := schema.Validate(valid); err == nil {
				t.Fatal("schema allowed unknown fields")
			}
		})
	}
}

func TestReadSkillUsesSnapshotAndRecordsReceipt(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	version := skillVersion(t, "task-handoff")
	run.SkillVersions = map[string]string{"task-handoff": version}
	run.Workspace.SkillIDs = []string{"independent-review"}
	for _, resource := range []string{"", "references/contract.md"} {
		response, err := e.readSkill(t.Context(), run, sender, collaborationJSON(t, map[string]any{"skill_id": "task-handoff", "resource": resource}))
		if err != nil {
			t.Fatal(err)
		}
		var receipt struct {
			ID       string `json:"skill_id"`
			Version  string `json:"version"`
			Resource string `json:"resource"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal([]byte(response), &receipt); err != nil {
			t.Fatal(err)
		}
		wantResource := resource
		if wantResource == "" {
			wantResource = "SKILL.md"
		}
		wantContent, _ := skills.Read("task-handoff", wantResource)
		if receipt.ID != "task-handoff" || receipt.Version != version || receipt.Resource != wantResource || receipt.Content != wantContent {
			t.Fatalf("incorrect versioned skill receipt: %+v", receipt)
		}
	}
	events := collaborationEvents(t, e.Store, run.ID)
	if len(events) != 2 {
		t.Fatalf("skill read events = %d, want 2", len(events))
	}
	for _, event := range events {
		var receipt map[string]any
		if err := json.Unmarshal(event.Data, &receipt); err != nil {
			t.Fatal(err)
		}
		if event.Type != "skill_read" || event.StepID != sender.ID || event.AgentID != sender.AgentID || receipt["version"] != version || receipt["content"] != nil {
			t.Fatalf("incorrect persisted skill receipt: %+v, %v", event, receipt)
		}
	}
	for _, tt := range []struct {
		name string
		run  w.Run
		raw  json.RawMessage
	}{
		{name: "disabled by snapshot", run: run, raw: collaborationJSON(t, map[string]any{"skill_id": "independent-review"})},
		{name: "unknown skill", run: run, raw: collaborationJSON(t, map[string]any{"skill_id": "unknown"})},
		{name: "resource escape", run: run, raw: collaborationJSON(t, map[string]any{"skill_id": "task-handoff", "resource": "../BOOTSTRAP.md"})},
		{name: "unknown field", run: run, raw: json.RawMessage(`{"skill_id":"task-handoff","shell":"x"}`)},
		{name: "trailing JSON", run: run, raw: json.RawMessage(`{"skill_id":"task-handoff"} {}`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if result, err := e.readSkill(t.Context(), tt.run, sender, tt.raw); err == nil || result != "" {
				t.Fatalf("forbidden read returned %q, %v", result, err)
			}
		})
	}
	for _, tt := range []struct {
		name     string
		versions map[string]string
		ids      []string
	}{
		{name: "all disabled in workspace", ids: []string{}},
		{name: "all disabled in snapshot", versions: map[string]string{}},
		{name: "stale version", versions: map[string]string{"task-handoff": "previous-version"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			copy := run
			copy.SkillVersions, copy.Workspace.SkillIDs = tt.versions, tt.ids
			if result, err := e.readSkill(t.Context(), copy, sender, json.RawMessage(`{"skill_id":"task-handoff"}`)); err == nil || result != "" {
				t.Fatalf("disabled or stale skill returned %q, %v", result, err)
			}
			for _, def := range collaborationTools(copy) {
				if def.Name == "read_skill" {
					t.Fatal("read_skill advertised without readable skills")
				}
			}
		})
	}
	if events := collaborationEvents(t, e.Store, run.ID); len(events) != 2 {
		t.Fatalf("failed reads wrote successful receipts: %v", events)
	}
}

func TestSubmitHandoffIsDurableAndIdempotent(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	message, source, _ := collaborationEvidence(t, e.Store, run, run.Agents[2].ID)
	in := handoffInput{RequestID: "research-1", TargetAgentID: run.Agents[1].ID, Task: "Compare the captured evidence", Role: "researcher", ContextRefs: []string{message.ID, source.ID}}
	first := submitCollaboration(t, e, run, sender, in)
	in.ContextRefs[0], in.ContextRefs[1] = in.ContextRefs[1], in.ContextRefs[0]
	second := submitCollaboration(t, e, run, sender, in)
	if first != second {
		t.Fatalf("retry changed receipt: first=%+v second=%+v", first, second)
	}
	steps := collaborationSteps(t, e.Store, run.ID)
	if len(steps) != 7 {
		t.Fatalf("retry duplicated tasks: %+v", steps)
	}
	child, continuation := collaborationStep(t, steps, first.TaskID), collaborationStep(t, steps, first.ContinuationID)
	if child.Kind != "handoff" || child.AgentID != in.TargetAgentID || child.FromStepID != sender.ID || child.FromAgentID != sender.AgentID || child.Status != "pending" || child.Depth != sender.Depth+1 || !slices.Equal(child.DependsOn, []string{sender.ID}) {
		t.Fatalf("incorrect durable task: %+v", child)
	}
	if continuation.Kind != "continuation" || continuation.AgentID != sender.AgentID || !slices.Contains(continuation.DependsOn, child.ID) || !slices.Contains(continuation.DependsOn, sender.ID) {
		t.Fatalf("incorrect return continuation: %+v", continuation)
	}
	events := collaborationEvents(t, e.Store, run.ID)
	if len(events) != 1 || events[0].Type != "task_submitted" {
		t.Fatalf("retry duplicated submission event: %+v", events)
	}
	in.Task = "Different work under the same request ID"
	if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil {
		t.Fatal("idempotency collision was accepted")
	}
	if len(collaborationSteps(t, e.Store, run.ID)) != 7 || len(collaborationEvents(t, e.Store, run.ID)) != 1 {
		t.Fatal("rejected collision changed durable state")
	}
	// A second independent submission shares the sender's continuation.
	in.RequestID, in.TargetAgentID = "research-2", run.Agents[2].ID
	third := submitCollaboration(t, e, run, sender, in)
	if third.TaskID == first.TaskID || third.ContinuationID != first.ContinuationID {
		t.Fatalf("parallel submissions did not share a return: %+v, %+v", first, third)
	}
	continuation = collaborationStep(t, collaborationSteps(t, e.Store, run.ID), first.ContinuationID)
	if !slices.Contains(continuation.DependsOn, first.TaskID) || !slices.Contains(continuation.DependsOn, third.TaskID) {
		t.Fatalf("continuation lost a parallel dependency: %+v", continuation)
	}
}

func TestSubmitHandoffRetryPreservesReceiptAfterRevocationAndBudget(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	in := handoffInput{RequestID: "stable-request", TargetAgentID: run.Agents[1].ID, Task: "A bounded task", Role: "member"}
	first := submitCollaboration(t, e, run, sender, in)
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		for _, id := range []string{sender.AgentID, in.TargetAgentID} {
			profile, err := tx.Agent(id)
			if err != nil {
				return err
			}
			profile.DisplayName += "-edited"
			profile.Disabled = true
			profile.Revision++
			if err := tx.SaveAgent(profile); err != nil {
				return err
			}
		}
		for i := 1; i < maxHandoffs; i++ {
			if err := tx.SaveStep(w.Step{ID: w.NewID(), RunID: run.ID, Status: "succeeded", Kind: "handoff", AgentID: in.TargetAgentID, RequestID: fmt.Sprintf("old-%d", i)}); err != nil {
				return err
			}
		}
		return nil
	})
	before := len(collaborationSteps(t, e.Store, run.ID))
	// Omitted and explicitly empty optional references describe the same request.
	raw := collaborationJSON(t, map[string]any{"request_id": in.RequestID, "target_agent_id": in.TargetAgentID, "task": in.Task, "role": in.Role, "context_refs": []string{}})
	response, err := e.submitHandoff(t.Context(), run, sender, raw)
	if err != nil {
		t.Fatal(err)
	}
	var retry collaborationReceipt
	if err := json.Unmarshal([]byte(response), &retry); err != nil {
		t.Fatal(err)
	}
	if retry != first || len(collaborationSteps(t, e.Store, run.ID)) != before || len(collaborationEvents(t, e.Store, run.ID)) != 1 {
		t.Fatalf("retry changed the accepted task after current policy changed: first=%+v retry=%+v", first, retry)
	}
	in.RequestID = "new-request"
	if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil {
		t.Fatal("a new request bypassed current policy after idempotent replay")
	}
}

func TestSubmitHandoffRejectsInvalidTargetsAndInput(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		mutate func(*testing.T, *Engine, *w.Run, *w.Step, *handoffInput)
	}{
		{name: "unknown target", mutate: func(_ *testing.T, _ *Engine, _ *w.Run, _ *w.Step, in *handoffInput) { in.TargetAgentID = w.NewID() }},
		{name: "self target", mutate: func(_ *testing.T, _ *Engine, _ *w.Run, sender *w.Step, in *handoffInput) {
			in.TargetAgentID = sender.AgentID
		}},
		{name: "display name instead of ID", mutate: func(_ *testing.T, _ *Engine, run *w.Run, _ *w.Step, in *handoffInput) {
			in.TargetAgentID = run.Agents[1].DisplayName
		}},
		{name: "disabled in snapshot", mutate: func(_ *testing.T, _ *Engine, run *w.Run, _ *w.Step, _ *handoffInput) { run.Agents[1].Disabled = true }},
		{name: "workspace member outside run roster", mutate: func(t *testing.T, e *Engine, run *w.Run, _ *w.Step, in *handoffInput) {
			outside := run.Agents[2]
			run.Agents = run.Agents[:2]
			in.TargetAgentID = outside.ID
		}},
		{name: "fixed workflow", mutate: func(_ *testing.T, _ *Engine, run *w.Run, _ *w.Step, _ *handoffInput) { run.Input.Workflow = "research" }},
		{name: "empty request ID", mutate: func(_ *testing.T, _ *Engine, _ *w.Run, _ *w.Step, in *handoffInput) { in.RequestID = " " }},
		{name: "oversized request ID", mutate: func(_ *testing.T, _ *Engine, _ *w.Run, _ *w.Step, in *handoffInput) {
			in.RequestID = strings.Repeat("r", 101)
		}},
		{name: "empty task", mutate: func(_ *testing.T, _ *Engine, _ *w.Run, _ *w.Step, in *handoffInput) { in.Task = "\n " }},
		{name: "oversized task", mutate: func(_ *testing.T, _ *Engine, _ *w.Run, _ *w.Step, in *handoffInput) {
			in.Task = strings.Repeat("t", 6001)
		}},
		{name: "unknown role", mutate: func(_ *testing.T, _ *Engine, _ *w.Run, _ *w.Step, in *handoffInput) { in.Role = "administrator" }},
		{name: "canceled run", mutate: func(t *testing.T, e *Engine, run *w.Run, _ *w.Step, _ *handoffInput) {
			collaborationWrite(t, e.Store, func(tx *store.Tx) error { copy := *run; copy.Status = "canceling"; return tx.SaveRun(copy) })
		}},
		{name: "sender no longer running", mutate: func(t *testing.T, e *Engine, _ *w.Run, sender *w.Step, _ *handoffInput) {
			collaborationWrite(t, e.Store, func(tx *store.Tx) error { copy := *sender; copy.Status = "succeeded"; return tx.SaveStep(copy) })
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, run, sender := collaborationFixture(t)
			in := handoffInput{RequestID: "task-1", TargetAgentID: run.Agents[1].ID, Task: "A bounded task", Role: "member"}
			tt.mutate(t, e, &run, &sender, &in)
			if result, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil || result != "" {
				t.Fatalf("invalid handoff returned %q, %v", result, err)
			}
			if len(collaborationSteps(t, e.Store, run.ID)) != 5 || len(collaborationEvents(t, e.Store, run.ID)) != 0 {
				t.Fatal("rejected request changed durable state")
			}
		})
	}
}

func TestSubmitHandoffRechecksCurrentGrants(t *testing.T) {
	t.Parallel()
	for _, who := range []string{"sender", "target"} {
		for _, change := range []string{"profile disabled", "membership disabled", "membership removed", "global tool revoked", "local tool revoked"} {
			t.Run(who+"/"+change, func(t *testing.T) {
				t.Parallel()
				e, run, sender := collaborationFixture(t)
				id := run.Agents[1].ID
				if who == "sender" {
					id = sender.AgentID
				}
				collaborationWrite(t, e.Store, func(tx *store.Tx) error {
					switch change {
					case "profile disabled", "global tool revoked":
						current, err := tx.Agent(id)
						if err != nil {
							return err
						}
						current.Revision++
						if change == "profile disabled" {
							current.Disabled = true
						} else {
							current.Tools = []string{"read_source"}
						}
						return tx.SaveAgent(current)
					case "membership removed":
						return tx.DeleteMember(run.WorkspaceID, id)
					default:
						member, err := tx.Member(run.WorkspaceID, id)
						if err != nil {
							return err
						}
						member.Revision++
						if change == "membership disabled" {
							member.Enabled = false
						} else {
							member.Tools = []string{"read_source"}
						}
						return tx.SaveMember(member)
					}
				})
				in := handoffInput{RequestID: "task-1", TargetAgentID: run.Agents[1].ID, Task: "A bounded task", Role: "member"}
				if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil {
					t.Fatalf("handoff survived %s %s", who, change)
				}
				if len(collaborationSteps(t, e.Store, run.ID)) != 5 {
					t.Fatal("permission failure left a task behind")
				}
			})
		}
	}
}

func TestSubmitHandoffRequiresExactCommittedReferences(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		refs func(w.Message, w.Source, w.Artifact) []string
		bad  bool
	}{
		{name: "committed message", refs: func(m w.Message, _ w.Source, _ w.Artifact) []string { return []string{m.ID} }},
		{name: "captured source", refs: func(_ w.Message, s w.Source, _ w.Artifact) []string { return []string{s.ID} }},
		{name: "exact artifact version", refs: func(_ w.Message, _ w.Source, a w.Artifact) []string {
			return []string{fmt.Sprintf("%s@%d", a.ID, a.Version)}
		}},
		{name: "unknown reference", bad: true, refs: func(w.Message, w.Source, w.Artifact) []string { return []string{w.NewID()} }},
		{name: "artifact without version", bad: true, refs: func(_ w.Message, _ w.Source, a w.Artifact) []string { return []string{a.ID} }},
		{name: "wrong artifact version", bad: true, refs: func(_ w.Message, _ w.Source, a w.Artifact) []string {
			return []string{fmt.Sprintf("%s@%d", a.ID, a.Version+1)}
		}},
		{name: "empty reference", bad: true, refs: func(w.Message, w.Source, w.Artifact) []string { return []string{""} }},
		{name: "duplicate reference", bad: true, refs: func(m w.Message, _ w.Source, _ w.Artifact) []string { return []string{m.ID, m.ID} }},
		{name: "too many references", bad: true, refs: func(m w.Message, _ w.Source, _ w.Artifact) []string {
			out := make([]string, 13)
			for i := range out {
				out[i] = m.ID
			}
			return out
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, run, sender := collaborationFixture(t)
			m, s, a := collaborationEvidence(t, e.Store, run, run.Agents[2].ID)
			in := handoffInput{RequestID: "task-1", TargetAgentID: run.Agents[1].ID, Task: "Inspect these inputs", Role: "member", ContextRefs: tt.refs(m, s, a)}
			_, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in))
			if (err != nil) != tt.bad {
				t.Fatalf("reference validation error = %v, want rejected=%v", err, tt.bad)
			}
		})
	}
	for _, status := range []string{"processing", "failed", "interrupted"} {
		t.Run("uncommitted message/"+status, func(t *testing.T) {
			t.Parallel()
			e, run, sender := collaborationFixture(t)
			m, _, _ := collaborationEvidence(t, e.Store, run, run.Agents[2].ID)
			m.Status = status
			collaborationWrite(t, e.Store, func(tx *store.Tx) error { return tx.SaveMessage(m) })
			in := handoffInput{RequestID: "task-1", TargetAgentID: run.Agents[1].ID, Task: "Inspect this input", Role: "member", ContextRefs: []string{m.ID}}
			if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil {
				t.Fatal("unfinished message accepted as committed input")
			}
		})
	}
	t.Run("other thread", func(t *testing.T) {
		t.Parallel()
		e, run, sender := collaborationFixture(t)
		other := seedRun(t, e.Store)
		m, s, a := collaborationEvidence(t, e.Store, other, other.Agents[2].ID)
		for _, ref := range []string{m.ID, s.ID, fmt.Sprintf("%s@%d", a.ID, a.Version)} {
			in := handoffInput{RequestID: "task-1", TargetAgentID: run.Agents[1].ID, Task: "Inspect this input", Role: "member", ContextRefs: []string{ref}}
			if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil {
				t.Fatalf("cross-thread reference accepted: %s", ref)
			}
		}
	})
}

func TestIndependentReviewExcludesReferencedAuthor(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"reviewer", "critic"} {
		for _, reference := range []string{"message", "artifact", "source only", "no work"} {
			t.Run(role+"/"+reference, func(t *testing.T) {
				t.Parallel()
				e, run, sender := collaborationFixture(t)
				m, source, a := collaborationEvidence(t, e.Store, run, run.Agents[1].ID)
				refs := []string{}
				if reference == "message" {
					refs = []string{m.ID}
				}
				if reference == "artifact" {
					refs = []string{fmt.Sprintf("%s@%d", a.ID, a.Version)}
				}
				if reference == "source only" {
					refs = []string{source.ID}
				}
				in := handoffInput{RequestID: "review-1", TargetAgentID: run.Agents[1].ID, Task: "Independently verify this work", Role: role, ContextRefs: refs}
				if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil {
					t.Fatal("self-review or review without work reference was accepted")
				}
				if reference == "message" || reference == "artifact" {
					in.TargetAgentID = run.Agents[2].ID
					submitCollaboration(t, e, run, sender, in)
				}
			})
		}
	}
}

func TestSubmitHandoffUsesDurableSenderDepth(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	durable := sender
	durable.Depth = maxDepth
	collaborationWrite(t, e.Store, func(tx *store.Tx) error { return tx.SaveStep(durable) })
	in := handoffInput{RequestID: "depth-bypass", TargetAgentID: run.Agents[1].ID, Task: "A bounded task", Role: "member"}
	if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil {
		t.Fatal("stale sender depth bypassed the durable collaboration budget")
	}
}

func TestSubmitHandoffUsesDurableSenderIdentity(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	forged := sender
	forged.AgentID = run.Agents[2].ID
	in := handoffInput{RequestID: "self-bypass", TargetAgentID: sender.AgentID, Task: "A bounded task", Role: "member"}
	if _, err := e.submitHandoff(t.Context(), run, forged, collaborationJSON(t, in)); err == nil {
		t.Fatal("non-authoritative sender identity bypassed the self-handoff check")
	}
}

func TestSubmitHandoffReusesContinuationAtFinalStepSlot(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	first := submitCollaboration(t, e, run, sender, handoffInput{RequestID: "first", TargetAgentID: run.Agents[1].ID, Task: "A bounded task", Role: "member"})
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for len(steps) < maxSteps-1 {
			step := w.Step{ID: w.NewID(), RunID: run.ID, Status: "succeeded", AgentID: run.Agents[1].ID}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			steps = append(steps, step)
		}
		return nil
	})
	second := submitCollaboration(t, e, run, sender, handoffInput{RequestID: "second", TargetAgentID: run.Agents[2].ID, Task: "Another independent bounded task", Role: "member"})
	if first.ContinuationID != second.ContinuationID || len(collaborationSteps(t, e.Store, run.ID)) != maxSteps {
		t.Fatal("existing continuation did not reuse the last available step slot")
	}
}

func TestSubmitHandoffEnforcesBudgets(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		handoffs  int
		stepCount int
		depth     int
		bad       bool
	}{
		{name: "handoff limit", handoffs: maxHandoffs, bad: true},
		{name: "last handoff allowed", handoffs: maxHandoffs - 1},
		{name: "step limit", stepCount: maxSteps - 1, bad: true},
		{name: "last step pair allowed", stepCount: maxSteps - 2},
		{name: "depth limit", depth: maxDepth, bad: true},
		{name: "last depth allowed", depth: maxDepth - 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, run, sender := collaborationFixture(t)
			sender.Depth = tt.depth
			collaborationWrite(t, e.Store, func(tx *store.Tx) error {
				if err := tx.SaveStep(sender); err != nil {
					return err
				}
				for i := 0; i < tt.handoffs; i++ {
					if err := tx.SaveStep(w.Step{ID: w.NewID(), RunID: run.ID, Status: "succeeded", Kind: "handoff", AgentID: run.Agents[1].ID, RequestID: fmt.Sprintf("old-%d", i)}); err != nil {
						return err
					}
				}
				steps, err := tx.Steps(run.ID)
				if err != nil {
					return err
				}
				for len(steps) < tt.stepCount {
					step := w.Step{ID: w.NewID(), RunID: run.ID, Status: "succeeded", AgentID: run.Agents[1].ID}
					if err := tx.SaveStep(step); err != nil {
						return err
					}
					steps = append(steps, step)
				}
				return nil
			})
			before := len(collaborationSteps(t, e.Store, run.ID))
			in := handoffInput{RequestID: "new-task", TargetAgentID: run.Agents[1].ID, Task: "A bounded task", Role: "member"}
			_, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in))
			if (err != nil) != tt.bad {
				t.Fatalf("budget result = %v, want rejected=%v", err, tt.bad)
			}
			want := before + 2
			if tt.bad {
				want = before
			}
			if got := len(collaborationSteps(t, e.Store, run.ID)); got != want {
				t.Fatalf("step count = %d, want %d", got, want)
			}
		})
	}
}

func TestNestedHandoffRewiresDownstreamDependencies(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	original := collaborationSteps(t, e.Store, run.ID)
	first := submitCollaboration(t, e, run, sender, handoffInput{RequestID: "first", TargetAgentID: run.Agents[1].ID, Task: "Research", Role: "researcher"})
	steps := collaborationSteps(t, e.Store, run.ID)
	for _, old := range original[1:] {
		consumer := collaborationStep(t, steps, old.ID)
		if slices.Contains(consumer.DependsOn, sender.ID) || !slices.Contains(consumer.DependsOn, first.ContinuationID) {
			t.Fatalf("consumer did not wait for first return: %+v", consumer)
		}
	}
	worker := collaborationStep(t, steps, first.TaskID)
	worker.Status = "running"
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		sender.Status = "succeeded"
		if err := tx.SaveStep(sender); err != nil {
			return err
		}
		return tx.SaveStep(worker)
	})
	nested := submitCollaboration(t, e, run, worker, handoffInput{RequestID: "nested", TargetAgentID: run.Agents[2].ID, Task: "Collect supporting evidence", Role: "member"})
	steps = collaborationSteps(t, e.Store, run.ID)
	outerReturn := collaborationStep(t, steps, first.ContinuationID)
	innerReturn := collaborationStep(t, steps, nested.ContinuationID)
	child := collaborationStep(t, steps, nested.TaskID)
	if slices.Contains(outerReturn.DependsOn, worker.ID) || !slices.Contains(outerReturn.DependsOn, innerReturn.ID) {
		t.Fatalf("outer return bypasses nested completion: %+v", outerReturn)
	}
	if !slices.Equal(innerReturn.DependsOn, []string{worker.ID, child.ID}) || innerReturn.AgentID != worker.AgentID || child.Depth != worker.Depth+1 {
		t.Fatalf("incorrect nested return graph: return=%+v child=%+v", innerReturn, child)
	}
	if !slices.Equal(child.DependsOn, []string{worker.ID}) {
		t.Fatalf("nested child waits on its own continuation: %+v", child)
	}
	for _, old := range original[1:] {
		consumer := collaborationStep(t, steps, old.ID)
		if !slices.Contains(consumer.DependsOn, outerReturn.ID) || slices.Contains(consumer.DependsOn, child.ID) {
			t.Fatalf("consumer lost outer return: %+v", consumer)
		}
	}
}

func TestCollaborationContextReportsCurrentAvailabilityAndGrants(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	run.CoordinatorID = sender.AgentID
	run.Agents[1].Capabilities = []string{"source-verification"}
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		member, err := tx.Member(run.WorkspaceID, run.Agents[1].ID)
		if err != nil {
			return err
		}
		member.Tools = []string{"read_source"}
		if err := tx.SaveMember(member); err != nil {
			return err
		}
		return tx.DeleteMember(run.WorkspaceID, run.Agents[2].ID)
	})
	text, err := e.collaborationContext(t.Context(), run, sender)
	if err != nil {
		t.Fatal(err)
	}
	var roster struct {
		CoordinatorID string `json:"coordinator_id"`
		CurrentStepID string `json:"current_step_id"`
		Members       []struct {
			ID           string   `json:"agent_id"`
			Available    bool     `json:"available"`
			Tools        []string `json:"tools"`
			Capabilities []string `json:"capabilities"`
		} `json:"members"`
		Tasks []w.Step `json:"tasks"`
	}
	start := strings.Index(text, "{")
	if start < 0 {
		t.Fatal("roster missing structured state")
	}
	if err := json.NewDecoder(strings.NewReader(text[start:])).Decode(&roster); err != nil {
		t.Fatal(err)
	}
	if roster.CoordinatorID != sender.AgentID || roster.CurrentStepID != sender.ID || len(roster.Tasks) != 5 || len(roster.Members) != 3 {
		t.Fatalf("incomplete runtime context: %+v", roster)
	}
	if roster.Members[1].Available || !slices.Equal(roster.Members[1].Tools, []string{"read_source"}) || !slices.Equal(roster.Members[1].Capabilities, []string{"source-verification"}) {
		t.Fatalf("local grants or capabilities missing: %+v", roster.Members[1])
	}
	if roster.Members[2].Available || len(roster.Members[2].Tools) != 0 {
		t.Fatalf("revoked member shown as available: %+v", roster.Members[2])
	}
}

func TestGetTaskIsRunScopedAndReturnsCommittedOutputs(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	m, _, a := collaborationEvidence(t, e.Store, run, sender.AgentID)
	response, err := e.getTask(t.Context(), run, collaborationJSON(t, map[string]any{"task_id": m.StepID}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Task      w.Step       `json:"task"`
		Messages  []w.Message  `json:"messages"`
		Artifacts []w.Artifact `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		t.Fatal(err)
	}
	if result.Task.ID != m.StepID || len(result.Messages) != 1 || result.Messages[0].ID != m.ID || len(result.Artifacts) != 1 || result.Artifacts[0].ID != a.ID {
		t.Fatalf("missing exact task outputs: %+v", result)
	}
	other := seedRun(t, e.Store)
	foreign := collaborationSteps(t, e.Store, other.ID)[0]
	for _, id := range []string{foreign.ID, w.NewID()} {
		if response, err := e.getTask(t.Context(), run, collaborationJSON(t, map[string]any{"task_id": id})); err == nil || response != "" {
			t.Fatalf("out-of-run task returned %q, %v", response, err)
		}
	}
}

func TestHandoffDeliversExactHistoricalInputs(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	oldRun := run
	oldRun.ID, oldRun.Status = w.NewID(), "succeeded"
	oldStep := w.Step{ID: w.NewID(), RunID: oldRun.ID, AgentID: sender.AgentID, Status: "succeeded"}
	m := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: oldRun.ID, StepID: oldStep.ID, AgentID: sender.AgentID, Role: "assistant", Status: "completed", Content: "historical-exact-message"}
	a := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: oldRun.ID, StepID: oldStep.ID, Type: "note", Title: "Old finding", Body: "historical-exact-artifact-v1"}
	newer := a
	newer.Version, newer.Body = 2, "do-not-substitute-newer-artifact-v2"
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		if err := tx.InsertRun(oldRun, w.NewID(), "history"); err != nil {
			return err
		}
		if err := tx.SaveStep(oldStep); err != nil {
			return err
		}
		if err := tx.SaveMessage(m); err != nil {
			return err
		}
		if err := tx.SaveArtifact(a); err != nil {
			return err
		}
		return tx.SaveArtifact(newer)
	})
	refs := []string{m.ID, fmt.Sprintf("%s@%d", a.ID, a.Version)}
	receipt := submitCollaboration(t, e, run, sender, handoffInput{RequestID: "review-history", TargetAgentID: run.Agents[1].ID, Task: "Review the exact historical message and artifact", Role: "reviewer", ContextRefs: refs})
	collaborationWrite(t, e.Store, func(tx *store.Tx) error { sender.Status = "succeeded"; return tx.SaveStep(sender) })
	child := collaborationStep(t, collaborationSteps(t, e.Store, run.ID), receipt.TaskID)
	called := false
	e.Web = testutil.Web{}
	e.Model = collaborationInspectModel(func(_ context.Context, request model.Request, _ func(string) error) (model.Response, error) {
		called = true
		text := request.Messages[0].Content[0].Text
		for _, expected := range []string{m.ID, a.ID, a.Body, m.Content, refs[1]} {
			if !strings.Contains(text, expected) {
				t.Errorf("delegated context omitted exact input %q", expected)
			}
		}
		if strings.Contains(text, newer.Body) {
			t.Error("newer artifact replaced explicitly selected historical version")
		}
		return model.Response{StopReason: "end_turn"}, nil
	})
	// Empty history proves selected refs are delivered separately from recent-history retention.
	got := e.executeStep(t.Context(), run, child, nil, nil, nil)
	if got.Status != "succeeded" || !called {
		t.Fatalf("delegated step failed: %+v called=%v", got, called)
	}
}

func TestGetTaskTracksOwnNestedFinalState(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	first := submitCollaboration(t, e, run, sender, handoffInput{RequestID: "outer", TargetAgentID: run.Agents[1].ID, Task: "Research", Role: "researcher"})
	worker := collaborationStep(t, collaborationSteps(t, e.Store, run.ID), first.TaskID)
	worker.Status = "running"
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		sender.Status = "succeeded"
		if err := tx.SaveStep(sender); err != nil {
			return err
		}
		return tx.SaveStep(worker)
	})
	nested := submitCollaboration(t, e, run, worker, handoffInput{RequestID: "nested", TargetAgentID: run.Agents[2].ID, Task: "Inspect evidence", Role: "member"})
	worker.Status = "succeeded"
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		if err := tx.SaveStep(worker); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: worker.ID, AgentID: worker.AgentID, Status: "completed", Content: "Only a submission acknowledgement"})
	})
	for _, status := range []string{"pending", "running", "failed", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			final := collaborationStep(t, collaborationSteps(t, e.Store, run.ID), nested.ContinuationID)
			final.Status = status
			final.Error = ""
			if status == "failed" {
				final.Error = "nested aggregation failed"
			}
			collaborationWrite(t, e.Store, func(tx *store.Tx) error {
				if err := tx.SaveStep(final); err != nil {
					return err
				}
				if status == "succeeded" {
					return tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: final.ID, AgentID: final.AgentID, Status: "completed", Content: "Final nested result"})
				}
				return nil
			})
			response, err := e.getTask(t.Context(), run, collaborationJSON(t, map[string]any{"task_id": first.TaskID}))
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Task     w.Step      `json:"task"`
				Status   string      `json:"status"`
				Final    w.Step      `json:"final_step"`
				Messages []w.Message `json:"messages"`
			}
			if err := json.Unmarshal([]byte(response), &result); err != nil {
				t.Fatal(err)
			}
			if result.Task.ID != worker.ID || result.Final.ID != final.ID || result.Status != status || result.Final.Error != final.Error {
				t.Fatalf("get_task reported invocation completion instead of nested final state: %+v", result)
			}
			if status == "succeeded" {
				if len(result.Messages) != 1 || result.Messages[0].Content != "Final nested result" {
					t.Fatalf("get_task lost final nested result: %+v", result)
				}
			} else if len(result.Messages) != 0 {
				t.Fatalf("submission acknowledgement exposed as final result: %+v", result)
			}
		})
	}
}

func TestGetTaskBoundsWholeResponseIncludingFindings(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	sender.Instruction = strings.Repeat("t", 6000)
	findings := []w.Finding{}
	for i := 0; i < 50; i++ {
		findings = append(findings, w.Finding{Title: fmt.Sprintf("Finding %d", i), Detail: strings.Repeat("d", 950), Status: "insufficient_evidence", SourceIDs: []string{}})
	}
	a := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: sender.ID, Type: "note", Title: "Large valid report", Body: strings.Repeat("b", 6000), Findings: findings, SourceIDs: []string{}, Limitations: []string{}}
	publication := collaborationJSON(t, map[string]any{"type": a.Type, "title": a.Title, "body": a.Body, "findings": a.Findings, "source_ids": a.SourceIDs, "limitations": a.Limitations})
	if len(publication) > 65536 {
		t.Fatalf("fixture exceeds accepted tool input budget: %d", len(publication))
	}
	if err := ValidateArtifact(a, "", nil); err != nil {
		t.Fatalf("fixture is not a valid publication: %v", err)
	}
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		if err := tx.SaveStep(sender); err != nil {
			return err
		}
		if err := tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: sender.ID, AgentID: sender.AgentID, Status: "completed", Content: strings.Repeat("m", 20000)}); err != nil {
			return err
		}
		return tx.SaveArtifact(a)
	})
	response, err := e.getTask(t.Context(), run, collaborationJSON(t, map[string]any{"task_id": sender.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if len(response) > 65536 {
		t.Fatalf("accepted task output would fatally exceed the MCP observation budget: %d bytes", len(response))
	}
	if !strings.Contains(response, "truncated") {
		t.Fatal("bounded response omitted explicit truncation marker")
	}
}

func TestSubmitHandoffUnicodeLimitMatchesSchema(t *testing.T) {
	t.Parallel()
	e, run, sender := collaborationFixture(t)
	in := handoffInput{RequestID: "unicode-task", TargetAgentID: run.Agents[1].ID, Task: strings.Repeat("猫", 2100), Role: "member"}
	var def model.Tool
	for _, tool := range collaborationTools(run) {
		if tool.Name == "handoff_task" {
			def = tool
		}
	}
	var value any
	if err := json.Unmarshal(collaborationJSON(t, in), &value); err != nil {
		t.Fatal(err)
	}
	if err := compileCollaborationSchema(t, def).Validate(value); err != nil {
		t.Fatalf("input should fit advertised character limit: %v", err)
	}
	submitCollaboration(t, e, run, sender, in)
}

func TestShortTaskResultPreservesUTF8(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("a", 5999) + "猫" + strings.Repeat("尾", 1000)
	got := shortTaskResult(value)
	if !utf8.ValidString(got) {
		t.Fatal("task result truncation cut a Unicode code point")
	}
	if len(got) >= len(value) || !strings.Contains(got, "truncated") {
		t.Fatal("large result was not explicitly truncated")
	}
}

func TestUnusedUnavailableRosterMemberDoesNotBlockDirectAnswer(t *testing.T) {
	t.Parallel()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	run := seedFreeRun(t, s)
	collaborationWrite(t, s, func(tx *store.Tx) error {
		profile, err := tx.Agent(run.Agents[2].ID)
		if err != nil {
			return err
		}
		profile.Disabled = true
		return tx.SaveAgent(profile)
	})
	called := false
	m := collaborationInspectModel(func(_ context.Context, _ model.Request, emit func(string) error) (model.Response, error) {
		called = true
		if err := emit("Direct answer without delegation"); err != nil {
			return model.Response{}, err
		}
		return model.Response{StopReason: "end_turn"}, nil
	})
	e := Engine{Store: s, Model: m, AllowedModels: []string{"test"}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" || !called {
		t.Fatalf("unused unavailable teammate blocked a direct answer: %+v called=%v", got, called)
	}
}

func TestNestedReturnFailureIsDeliveredToOuterCaller(t *testing.T) {
	t.Parallel()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	run := seedFreeRun(t, s)
	returned := false
	m := collaborationInspectModel(func(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
		answer := func(text string) (model.Response, error) {
			if err := emit(text); err != nil {
				return model.Response{}, err
			}
			return model.Response{StopReason: "end_turn"}, nil
		}
		if len(request.Messages) > 1 {
			return answer("Submission accepted")
		}
		text := request.Messages[0].Content[0].Text
		var roster struct {
			CurrentStepID string `json:"current_step_id"`
		}
		start := strings.Index(text, "{")
		if start < 0 {
			return model.Response{}, errors.New("missing roster")
		}
		if err := json.NewDecoder(strings.NewReader(text[start:])).Decode(&roster); err != nil {
			return model.Response{}, err
		}
		var steps []w.Step
		if err := s.Read(ctx, func(tx *store.Tx) error { var err error; steps, err = tx.Steps(run.ID); return err }); err != nil {
			return model.Response{}, err
		}
		step := collaborationStep(t, steps, roster.CurrentStepID)
		call := func(target, task string) (model.Response, error) {
			in := handoffInput{RequestID: task, TargetAgentID: target, Task: task, Role: "member"}
			return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: w.NewID(), Name: "handoff_task", Input: collaborationJSON(t, in)}}}, nil
		}
		switch {
		case step.Kind == "":
			return call(run.Agents[1].ID, "outer-work")
		case step.Kind == "handoff" && step.AgentID == run.Agents[1].ID:
			return call(run.Agents[2].ID, "nested-work")
		case step.Kind == "handoff":
			return answer("Leaf result")
		case step.Kind == "continuation" && step.AgentID == run.Agents[1].ID:
			return model.Response{}, errors.New("nested return failed to aggregate")
		case step.Kind == "continuation":
			returned = true
			if !strings.Contains(text, "nested return failed to aggregate") {
				t.Error("nested final failure was hidden from outer caller")
			}
			return answer("Nested work failed; preserving the limitation")
		}
		return model.Response{}, errors.New("unexpected test step")
	})
	e := Engine{Store: s, Model: m, AllowedModels: []string{"test"}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if !returned || got.Status != "failed" || !strings.Contains(got.Error, "nested return failed to aggregate") {
		t.Fatalf("nested failure did not return and settle honestly: run=%+v returned=%v", got, returned)
	}
	for _, step := range collaborationSteps(t, s, run.ID) {
		if step.Status == "pending" || step.Status == "running" {
			t.Fatalf("nested failure left an unsettled task: %+v", step)
		}
	}
}

func TestRestartInterruptsDynamicChildrenAndContinuations(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run := seedFreeRun(t, s)
	run.Status = "running"
	sender := collaborationSteps(t, s, run.ID)[0]
	sender.Status = "running"
	collaborationWrite(t, s, func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.SaveStep(sender)
	})
	e := Engine{Store: s, AllowedModels: []string{"test"}}
	receipt := submitCollaboration(t, &e, run, sender, handoffInput{RequestID: "before-restart", TargetAgentID: run.Agents[1].ID, Task: "Read sources", Role: "member"})
	worker := collaborationStep(t, collaborationSteps(t, s, run.ID), receipt.TaskID)
	worker.Status = "running"
	partial := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: worker.ID, AgentID: worker.AgentID, Status: "processing", Content: "Preserved partial result"}
	collaborationWrite(t, s, func(tx *store.Tx) error {
		sender.Status = "succeeded"
		if err := tx.SaveStep(sender); err != nil {
			return err
		}
		if err := tx.SaveStep(worker); err != nil {
			return err
		}
		if err := tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: worker.ID, Number: 1, Status: "running", Reason: "initial", StartedAt: w.Now(), Process: &w.ProcessRef{PID: 999999, PGID: 999999, Start: "Mon Jan  1 00:00:00 2001"}}); err != nil {
			return err
		}
		return tx.SaveMessage(partial)
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	called := false
	e = Engine{Store: s, Model: collaborationInspectModel(func(_ context.Context, _ model.Request, emit func(string) error) (model.Response, error) {
		called = true
		if err := emit("resumed dynamic result"); err != nil {
			return model.Response{}, err
		}
		return model.Response{StopReason: "end_turn"}, nil
	}), AllowedModels: []string{"test"}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" || !called || got.RecoveryCount != 1 {
		t.Fatalf("restart did not continue dynamic work: run=%+v called=%v", got, called)
	}
	steps := collaborationSteps(t, s, run.ID)
	for _, id := range []string{receipt.TaskID, receipt.ContinuationID} {
		if step := collaborationStep(t, steps, id); step.Status != "succeeded" {
			t.Fatalf("dynamic step did not resume: %+v", step)
		}
	}
	handoffs := 0
	for _, step := range steps {
		if step.Kind == "handoff" {
			handoffs++
		}
	}
	if handoffs != 1 {
		t.Fatalf("restart duplicated a handoff: %d", handoffs)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		messages, err := tx.Messages(run.ThreadID)
		if err != nil {
			return err
		}
		preserved := false
		for _, message := range messages {
			if message.Content == partial.Content && message.Status == "interrupted" {
				preserved = true
			}
		}
		if !preserved {
			return fmt.Errorf("partial result was not preserved honestly: %+v", messages)
		}
		attempts, err := tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		seen := 0
		for _, attempt := range attempts {
			if attempt.StepID == receipt.TaskID {
				seen++
				if attempt.Number == 1 && attempt.Status != "interrupted" {
					return fmt.Errorf("first attempt status=%s", attempt.Status)
				}
				if attempt.Number == 2 && attempt.Status != "succeeded" {
					return fmt.Errorf("second attempt status=%s", attempt.Status)
				}
			}
		}
		if seen != 2 {
			return fmt.Errorf("retry attempts missing: %+v", attempts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type collaborationInspectModel func(context.Context, model.Request, func(string) error) (model.Response, error)

func (m collaborationInspectModel) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	return m(ctx, request, emit)
}

type collaborationReceipt struct {
	TaskID         string `json:"task_id"`
	ContinuationID string `json:"continuation_id"`
	Status         string `json:"status"`
}

func TestIndependentReviewRejectsTheTargetsOwnReferences(t *testing.T) {
	e, run, sender := collaborationFixture(t)
	own, _, _ := collaborationEvidence(t, e.Store, run, run.Agents[2].ID)
	in := handoffInput{RequestID: "self-review", TargetAgentID: run.Agents[2].ID, Task: "Review this evidence", Role: "reviewer", ContextRefs: []string{own.ID}}
	if result, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err == nil || result != "" {
		t.Fatal("a reviewer was assigned their own referenced work")
	} else {
		for _, want := range []string{"target reviewer", own.ID, "omit the target's own replies"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("rejection is not correctable (%q missing): %v", want, err)
			}
		}
	}
	// The same work remains reviewable by a member who did not author it.
	in.RequestID, in.TargetAgentID = "independent-review", run.Agents[1].ID
	if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in)); err != nil {
		t.Fatalf("independent review target was refused: %v", err)
	}
}

func collaborationFixture(t *testing.T) (*Engine, w.Run, w.Step) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	run := seedRun(t, s)
	run.Input = w.RunInput{Workflow: "free", Message: "Compare the available evidence", Targets: []string{run.Agents[0].ID}}
	run.Status = "running"
	var sender w.Step
	collaborationWrite(t, s, func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		sender = steps[0]
		sender.Status, sender.Role = "running", "member"
		return tx.SaveStep(sender)
	})
	return &Engine{Store: s, AllowedModels: []string{"test"}}, run, sender
}

func collaborationEvidence(t *testing.T, s *store.Store, run w.Run, authorID string) (w.Message, w.Source, w.Artifact) {
	t.Helper()
	stepID := ""
	for _, step := range collaborationSteps(t, s, run.ID) {
		if step.AgentID == authorID {
			stepID = step.ID
			break
		}
	}
	if stepID == "" {
		t.Fatal("evidence author missing from seeded steps")
	}
	m := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: stepID, AgentID: authorID, Role: "assistant", Content: "Evidence-based finding", Status: "completed"}
	source := w.Source{ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "web", Content: "Captured evidence"}
	a := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: stepID, Type: "note", Title: "Finding", Body: "Evidence-based finding"}
	collaborationWrite(t, s, func(tx *store.Tx) error {
		if err := tx.SaveMessage(m); err != nil {
			return err
		}
		if err := tx.SaveSource(source); err != nil {
			return err
		}
		return tx.SaveArtifact(a)
	})
	return m, source, a
}

func submitCollaboration(t *testing.T, e *Engine, run w.Run, sender w.Step, in handoffInput) collaborationReceipt {
	t.Helper()
	response, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in))
	if err != nil {
		t.Fatal(err)
	}
	var receipt collaborationReceipt
	if err := json.Unmarshal([]byte(response), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.TaskID == "" || receipt.ContinuationID == "" || receipt.Status != "accepted" {
		t.Fatalf("invalid submission receipt: %+v", receipt)
	}
	return receipt
}

func collaborationJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func collaborationWrite(t *testing.T, s *store.Store, fn func(*store.Tx) error) {
	t.Helper()
	if err := s.Write(t.Context(), fn); err != nil {
		t.Fatal(err)
	}
}

func collaborationSteps(t *testing.T, s *store.Store, runID string) []w.Step {
	t.Helper()
	var result []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error { var err error; result, err = tx.Steps(runID); return err }); err != nil {
		t.Fatal(err)
	}
	return result
}

func collaborationStep(t *testing.T, steps []w.Step, id string) w.Step {
	t.Helper()
	for _, step := range steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("step %s missing", id)
	return w.Step{}
}

func collaborationEvents(t *testing.T, s *store.Store, runID string) []w.Event {
	t.Helper()
	var result []w.Event
	if err := s.Read(context.Background(), func(tx *store.Tx) error { var err error; result, err = tx.Events(runID, 0); return err }); err != nil {
		t.Fatal(err)
	}
	return result
}

func skillVersion(t *testing.T, id string) string {
	t.Helper()
	for _, info := range skills.List() {
		if info.ID == id {
			return info.Version
		}
	}
	t.Fatalf("skill %s missing", id)
	return ""
}

func compileCollaborationSchema(t *testing.T, def model.Tool) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	resource := "https://cocogem.test/" + def.Name + ".json"
	var value any
	if err := json.Unmarshal(collaborationJSON(t, def.Schema), &value); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(resource, value); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}
