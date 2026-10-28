package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func seedMethodFixture(t *testing.T, toolsFor func(i int) []string) (*Engine, *store.Store, w.Run, []w.Agent, w.Step) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ws := w.Workspace{ID: w.NewID(), Name: "method", Revision: 1}
	agents := []w.Agent{}
	for i, name := range []string{"协调者", "研究员", "复核员"} {
		grant := tools.Names
		if toolsFor != nil {
			grant = toolsFor(i)
		}
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: fmt.Sprintf("member-%d", i), Model: "test", Tools: slices.Clone(grant), Revision: 1})
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: []string{agents[0].ID, agents[1].ID, agents[2].ID}, CoordinatorID: agents[0].ID, Workflow: "free", Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "running", CreatedAt: w.Now(), Agents: agents, CoordinatorID: agents[0].ID, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}, Input: w.RunInput{Workflow: "free", Message: "请分析这个问题并给出结论", Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}, Routing: w.Routing{Decision: "discuss", Workflow: "free", Targets: []string{agents[0].ID}, Explanation: "没有明确收件人，交给会话协调者"}}
	root := w.Step{ID: w.NewID(), RunID: run.ID, AgentID: agents[0].ID, AgentName: agents[0].DisplayName, Role: "responder", Instruction: "Plan the task", Status: "running"}
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, a := range agents {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "hash"); err != nil {
			return err
		}
		return tx.SaveStep(root)
	}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, AllowedModels: []string{"test"}, Web: testutil.Web{}}
	return e, s, run, agents, root
}

func runSelection(t *testing.T, e *Engine, run w.Run, step w.Step, in selectionInput) (string, error) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return e.chooseWorkflow(context.Background(), run, step, raw)
}

func selectionSteps(t *testing.T, s *store.Store, runID string) []w.Step {
	t.Helper()
	var steps []w.Step
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return steps
}

func TestCoordinatorSelectionPersistsEffectivePlan(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	in := selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}, Reason: "需要独立观点与交叉质询"}
	if _, err := runSelection(t, e, run, root, in); err != nil {
		t.Fatal(err)
	}
	receipt, err := runSelection(t, e, run, root, in)
	if err != nil {
		t.Fatalf("identical repeat rejected: %v", err)
	}
	if !strings.Contains(receipt, `"idempotent_repeat":true`) {
		t.Fatalf("repeat receipt = %s", receipt)
	}
	if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "research", Assignments: map[string]string{"researcher": agents[1].ID, "critic": agents[2].ID, "coordinator": agents[0].ID}}); err == nil || !strings.Contains(err.Error(), "already selected") {
		t.Fatalf("different plan err = %v", err)
	}
	var current w.Run
	var steps []w.Step
	var events []w.Event
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		if current, err = tx.Run(run.ID); err != nil {
			return err
		}
		if steps, err = tx.Steps(run.ID); err != nil {
			return err
		}
		events, err = tx.Events(run.ID, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if current.SelectedWorkflow == nil || current.SelectedWorkflow.Workflow != "discussion" || current.SelectedWorkflow.StepID != root.ID {
		t.Fatalf("selection = %+v", current.SelectedWorkflow)
	}
	if current.SelectedWorkflow.SelectedAt == "" {
		t.Fatal("selection was not timestamped")
	}
	// The user's accepted input is immutable; the plan uses the selection.
	if current.Input.Workflow != "free" || !slices.Equal(current.Input.Targets, []string{agents[0].ID}) || current.Input.Assignments != nil {
		t.Fatalf("run input was rewritten: %+v", current.Input)
	}
	if effectiveInput(current).Workflow != "discussion" {
		t.Fatal("effective input did not adopt the selection")
	}
	if len(steps) != 9 {
		t.Fatalf("steps = %d, want root + 8 discussion steps", len(steps))
	}
	selected := 0
	views := 0
	selectedEvents := 0
	for _, step := range steps {
		if step.ID == root.ID {
			continue
		}
		selected++
		if step.Role == "discussant" {
			views++
			if !slices.Equal(step.DependsOn, []string{root.ID}) {
				t.Fatalf("independent view deps = %v, want only the planning root", step.DependsOn)
			}
		}
	}
	if selected != 8 || views != 2 {
		t.Fatalf("selected steps = %d views=%d", selected, views)
	}
	for _, event := range events {
		if event.Type == "workflow_selected" {
			selectedEvents++
		}
	}
	if selectedEvents != 1 {
		t.Fatalf("workflow_selected events = %d, want exactly one", selectedEvents)
	}
	if RequiredArtifact(steps[len(steps)-1], effectiveInput(current)) != "note" {
		t.Fatal("selected final coordinator would not publish")
	}
}

func TestCoordinatorSelectionRejectsUnsafeScopes(t *testing.T) {
	t.Run("non coordinator", func(t *testing.T) {
		e, s, run, agents, root := seedMethodFixture(t, nil)
		root.AgentID, root.AgentName = agents[1].ID, agents[1].DisplayName
		if err := s.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveStep(root) }); err != nil {
			t.Fatal(err)
		}
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}}); err == nil {
			t.Fatal("a non-coordinator step selected a method")
		}
	})
	t.Run("explicit routing", func(t *testing.T) {
		e, s, run, agents, root := seedMethodFixture(t, nil)
		run.Routing.Decision = "assign"
		if err := s.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
			t.Fatal(err)
		}
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "research", Assignments: map[string]string{"researcher": agents[1].ID, "critic": agents[2].ID, "coordinator": agents[0].ID}}); err == nil {
			t.Fatal("explicit user routing was overridden")
		}
	})
	t.Run("built-in", func(t *testing.T) {
		e, s, run, agents, root := seedMethodFixture(t, nil)
		run.Input.Workflow = "research"
		if err := s.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
			t.Fatal(err)
		}
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}}); err == nil {
			t.Fatal("accepted built-in was rewritten")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		e, s, run, agents, root := seedMethodFixture(t, nil)
		run.Status = "canceling"
		if err := s.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
			t.Fatal(err)
		}
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}}); err == nil {
			t.Fatal("canceled run accepted a selection")
		}
	})
	t.Run("leave after selection", func(t *testing.T) {
		e, s, run, agents, root := seedMethodFixture(t, nil)
		in := selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}}
		if _, err := runSelection(t, e, run, root, in); err != nil {
			t.Fatal(err)
		}
		run.Status = "canceling"
		if err := s.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
			t.Fatal(err)
		}
		if _, err := runSelection(t, e, run, root, in); err == nil {
			t.Fatal("selection was accepted after cancellation")
		}
	})
	t.Run("committed handoff", func(t *testing.T) {
		e, s, run, agents, root := seedMethodFixture(t, nil)
		if err := s.Write(context.Background(), func(tx *store.Tx) error {
			return tx.SaveStep(w.Step{ID: w.NewID(), RunID: run.ID, AgentID: agents[1].ID, AgentName: agents[1].DisplayName, Role: "member", Status: "pending", Kind: "handoff", FromStepID: root.ID})
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}}); err == nil {
			t.Fatal("selection replaced committed delegation work")
		}
	})
}

func TestCoordinatorSelectionValidatesRosterAndRoles(t *testing.T) {
	t.Run("off roster", func(t *testing.T) {
		e, _, run, agents, root := seedMethodFixture(t, nil)
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, w.NewID()}, Assignments: map[string]string{"coordinator": agents[0].ID}}); err == nil || !strings.Contains(err.Error(), "roster") {
			t.Fatalf("off-roster target err = %v", err)
		}
	})
	t.Run("research self review", func(t *testing.T) {
		e, _, run, agents, root := seedMethodFixture(t, nil)
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "research", Assignments: map[string]string{"researcher": agents[1].ID, "critic": agents[1].ID, "coordinator": agents[0].ID}}); err == nil {
			t.Fatal("researcher reviewed its own work")
		}
	})
	t.Run("code self review", func(t *testing.T) {
		e, _, run, agents, root := seedMethodFixture(t, nil)
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": agents[1].ID, "reviewer": agents[1].ID}}); err == nil {
			t.Fatal("analyst reviewed its own work")
		}
	})
	t.Run("engineering without source root", func(t *testing.T) {
		e, _, run, agents, root := seedMethodFixture(t, nil)
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": agents[1].ID, "reviewer": agents[2].ID}}); err == nil || !strings.Contains(err.Error(), "source directory") {
			t.Fatalf("unbound code analysis err = %v", err)
		}
	})
	t.Run("engineering analysis read only", func(t *testing.T) {
		e, s, run, agents, root := seedMethodFixture(t, nil)
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		run.Workspace.Root = dir
		if err := s.Write(context.Background(), func(tx *store.Tx) error {
			if err := tx.SaveWorkspace(run.Workspace); err != nil {
				return err
			}
			return tx.SaveRun(run)
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": agents[1].ID, "reviewer": agents[2].ID}, Reason: "只读工程分析"}); err != nil {
			t.Fatal(err)
		}
		steps := selectionSteps(t, s, run.ID)
		if len(steps) != 3 {
			t.Fatalf("code analysis steps = %d, want root + analyst + reviewer", len(steps))
		}
		if steps[1].Role != "analyst" || steps[1].AgentID != agents[1].ID || !slices.Equal(steps[1].DependsOn, []string{root.ID}) {
			t.Fatalf("analyst step = %+v", steps[1])
		}
		if steps[2].Role != "reviewer" || steps[2].AgentID != agents[2].ID || !slices.Equal(steps[2].DependsOn, []string{steps[1].ID}) {
			t.Fatalf("reviewer step = %+v", steps[2])
		}
		var current w.Run
		if err := s.Read(context.Background(), func(tx *store.Tx) error {
			var err error
			current, err = tx.Run(run.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if RequiredArtifact(steps[2], effectiveInput(current)) != "code_analysis" {
			t.Fatal("selected engineering analysis would not publish a read-only report")
		}
		for i := range agents {
			if !slices.Equal(current.Agents[i].Tools, agents[i].Tools) {
				t.Fatalf("selection changed member grants: %+v", current.Agents[i])
			}
		}
	})
	t.Run("unknown role", func(t *testing.T) {
		e, _, run, agents, root := seedMethodFixture(t, nil)
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID, "analyst": agents[1].ID}}); err == nil {
			t.Fatal("unknown role accepted")
		}
	})
	t.Run("snapshot lacks publish", func(t *testing.T) {
		e, _, run, agents, root := seedMethodFixture(t, func(i int) []string {
			if i == 0 {
				return []string{"read_source"}
			}
			return tools.Names
		})
		if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}}); err == nil {
			t.Fatal("coordinator without publish_artifact was accepted")
		}
	})
	t.Run("grant revoked now", func(t *testing.T) {
		e, s, run, agents, root := seedMethodFixture(t, nil)
		revoked := agents[0]
		revoked.Tools = []string{"read_source"}
		if err := s.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveAgent(revoked) }); err != nil {
			t.Fatal(err)
		}
		_, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}})
		if err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("revoked grant err = %v", err)
		}
	})
}

func TestCoordinatorSelectionConcurrentIdempotency(t *testing.T) {
	e, _, run, agents, root := seedMethodFixture(t, nil)
	in := selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}}
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := runSelection(t, e, run, root, in)
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("identical concurrent selection failed: %v", err)
		}
	}
	if steps := selectionSteps(t, e.Store, run.ID); len(steps) != 9 {
		t.Fatalf("concurrent repeat duplicated plan: %d steps", len(steps))
	}

	e2, _, run2, agents2, root2 := seedMethodFixture(t, nil)
	plans := []selectionInput{
		{Workflow: "discussion", Targets: []string{agents2[1].ID, agents2[2].ID}, Assignments: map[string]string{"coordinator": agents2[0].ID}},
		{Workflow: "research", Assignments: map[string]string{"researcher": agents2[1].ID, "critic": agents2[2].ID, "coordinator": agents2[0].ID}},
	}
	results := make(chan error, 2)
	for _, in := range plans {
		go func(in selectionInput) {
			_, err := runSelection(t, e2, run2, root2, in)
			results <- err
		}(in)
	}
	ok := 0
	for range 2 {
		if err := <-results; err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("conflicting concurrent selections accepted = %d, want exactly one", ok)
	}
}

func TestWorkflowSelectionAndHandoffCannotMix(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	in := selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}}
	if _, err := runSelection(t, e, run, root, in); err != nil {
		t.Fatal(err)
	}
	handoff, _ := json.Marshal(handoffInput{RequestID: "after-select", TargetAgentID: agents[1].ID, Task: "独立整理材料", Role: "member"})
	if _, err := e.submitHandoff(context.Background(), run, root, handoff); err == nil {
		t.Fatal("handoff was accepted after a method selection")
	}
	if count := len(selectionSteps(t, s, run.ID)); count != 9 {
		t.Fatalf("handoff after selection created orphan steps: %d", count)
	}

	e2, s2, run2, agents2, root2 := seedMethodFixture(t, nil)
	first, _ := json.Marshal(handoffInput{RequestID: "before-select", TargetAgentID: agents2[1].ID, Task: "独立整理材料", Role: "member"})
	if _, err := e2.submitHandoff(context.Background(), run2, root2, first); err != nil {
		t.Fatal(err)
	}
	if _, err := runSelection(t, e2, run2, root2, selectionInput{Workflow: "discussion", Targets: []string{agents2[1].ID, agents2[2].ID}, Assignments: map[string]string{"coordinator": agents2[0].ID}}); err == nil {
		t.Fatal("method selection was accepted after committed delegation")
	}
	if count := len(selectionSteps(t, s2, run2.ID)); count != 3 {
		t.Fatalf("selection after handoff created orphan plan steps: %d", count)
	}
}

func TestRequiredArtifactUsesEffectiveInput(t *testing.T) {
	free := w.Run{Input: w.RunInput{Workflow: "free", Message: "task"}}
	selected := func(workflow string) w.Run {
		run := free
		run.SelectedWorkflow = &w.WorkflowSelection{Workflow: workflow}
		return run
	}
	coordinator := w.Step{ID: "final", Role: "coordinator", Kind: "workflow"}
	reviewer := w.Step{ID: "review", Role: "reviewer", Kind: "workflow"}
	root := w.Step{ID: "root", Role: "responder"}
	if got := RequiredArtifact(coordinator, effectiveInput(selected("discussion"))); got != "note" {
		t.Fatalf("selected discussion final artifact = %q, want note", got)
	}
	if got := RequiredArtifact(coordinator, effectiveInput(selected("research"))); got != "research_report" {
		t.Fatalf("selected research final artifact = %q", got)
	}
	if got := RequiredArtifact(reviewer, effectiveInput(selected("code_analysis"))); got != "code_analysis" {
		t.Fatalf("selected code analysis final artifact = %q", got)
	}
	// The free early-return still applies when no method was selected.
	if got := RequiredArtifact(coordinator, effectiveInput(free)); got != "" {
		t.Fatalf("free run gained a publication obligation: %q", got)
	}
	finals := finalSteps(effectiveInput(selected("discussion")), []w.Step{root, coordinator})
	if len(finals) != 1 || finals[0].ID != coordinator.ID {
		t.Fatalf("selected delivery finals = %+v, want the plan coordinator, not the planning root", finals)
	}
	if effectiveInput(selected("discussion")).Workflow != "discussion" || free.Input.Workflow != "free" {
		t.Fatal("effective input mutated the original run input")
	}
}

func TestSelectionEngineeringIsAlwaysReadOnlyAnalysis(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	in := selectionInput{Workflow: "engineering", Assignments: map[string]string{"analyst": agents[1].ID, "reviewer": agents[2].ID}}
	if _, err := runSelection(t, e, run, root, in); err == nil || !strings.Contains(err.Error(), "bound source directory") {
		t.Fatalf("engineering selection without a root err = %v", err)
	}
	bound, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run.Workspace.Root = bound
	e.AllowedRoots = []string{bound}
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(run.Workspace); err != nil {
			return err
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	// The selectable vocabulary never includes the developer role.
	if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "engineering", Assignments: map[string]string{"developer": agents[1].ID, "reviewer": agents[2].ID}}); err == nil || !strings.Contains(err.Error(), "does not use role developer") {
		t.Fatalf("selection authorized developer err = %v", err)
	}
	if _, err := runSelection(t, e, run, root, in); err != nil {
		t.Fatal(err)
	}
	current, steps := readRunAndSteps(t, s, run.ID)
	if current.SelectedWorkflow == nil || current.SelectedWorkflow.Workflow != "engineering" || len(steps) != 3 {
		t.Fatalf("engineering selection = %+v steps=%d", current.SelectedWorkflow, len(steps))
	}
	if steps[1].Role != "analyst" || steps[2].Role != "reviewer" {
		t.Fatalf("engineering analysis roles = %s, %s", steps[1].Role, steps[2].Role)
	}
	effective := effectiveInput(current)
	if effective.Workflow != "engineering" || effective.EngineeringAction != w.EngineeringAnalyze {
		t.Fatalf("effective engineering input = %+v", effective)
	}
	for _, role := range []string{"analyst", "reviewer"} {
		for _, name := range []string{"write_file", "patch_file"} {
			if EngineeringToolAllowed(effective, role, name) {
				t.Fatalf("selected engineering analysis authorized %s for %s", name, role)
			}
		}
	}
	if EngineeringToolAllowed(effective, "reviewer", "run_test") {
		t.Fatal("selected engineering analysis authorized project tests")
	}
	if RequiredArtifact(steps[2], effective) != "code_analysis" {
		t.Fatal("selected engineering analysis did not require a code_analysis report")
	}
}

func TestSelectionToolSchemaEnumeratesRuntimeRoster(t *testing.T) {
	_, _, run, agents, root := seedMethodFixture(t, nil)
	disabled := w.Agent{ID: w.NewID(), DisplayName: "停用成员", Disabled: true, Revision: 1}
	run.Agents = append(run.Agents, disabled)
	defs := selectionTools(run, root)
	if len(defs) != 1 {
		t.Fatalf("selection tools = %d", len(defs))
	}
	want := []string{agents[0].ID, agents[1].ID, agents[2].ID}
	props := defs[0].Schema["properties"].(map[string]any)
	member := props["targets"].(map[string]any)["items"].(map[string]any)
	if !slices.Equal(member["enum"].([]string), want) {
		t.Fatalf("target enum = %v, want %v", member["enum"], want)
	}
	workflows := props["workflow"].(map[string]any)["enum"].([]string)
	if !slices.Contains(workflows, "engineering") {
		t.Fatalf("workflow enum = %v, want engineering", workflows)
	}
	roles := props["assignments"].(map[string]any)["properties"].(map[string]any)
	for _, role := range []string{"researcher", "critic", "coordinator", "analyst", "reviewer"} {
		if !slices.Equal(roles[role].(map[string]any)["enum"].([]string), want) {
			t.Fatalf("%s enum = %v, want %v", role, roles[role], want)
		}
	}
	if _, ok := roles["developer"]; ok {
		t.Fatal("selection schema offered developer, which would imply develop authorization")
	}
}

func saveSelectionSettings(t *testing.T, s *store.Store, value w.Settings) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveSettings(value) }); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorSelectionUnavailableDefaultsAreCorrectable(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	saveSelectionSettings(t, s, w.Settings{Revision: 2, Assignments: map[string]map[string]string{
		"discussion": {"coordinator": w.NewID()},
	}})
	if _, err := runSelection(t, e, run, root, selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unavailable default err = %v", err)
	}
	current, steps := readRunAndSteps(t, s, run.ID)
	if current.SelectedWorkflow != nil || len(steps) != 1 {
		t.Fatalf("unusable default persisted a selection: %+v steps=%d", current.SelectedWorkflow, len(steps))
	}

	e2, s2, run2, agents2, root2 := seedMethodFixture(t, nil)
	saveSelectionSettings(t, s2, w.Settings{Revision: 2, DefaultCoordinatorID: w.NewID()})
	if _, err := runSelection(t, e2, run2, root2, selectionInput{Workflow: "discussion", Targets: []string{agents2[1].ID, agents2[2].ID}}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unavailable default coordinator err = %v", err)
	}
}

func TestCoordinatorSelectionRetryIgnoresChangedDefaults(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	saveSelectionSettings(t, s, w.Settings{Revision: 2, Assignments: map[string]map[string]string{
		"discussion": {"coordinator": agents[2].ID},
	}})
	in := selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}}
	if _, err := runSelection(t, e, run, root, in); err != nil {
		t.Fatal(err)
	}
	saveSelectionSettings(t, s, w.Settings{Revision: 3, Assignments: map[string]map[string]string{
		"discussion": {"coordinator": agents[1].ID},
	}})
	receipt, err := runSelection(t, e, run, root, in)
	if err != nil {
		t.Fatalf("same body conflicted after defaults changed: %v", err)
	}
	if !strings.Contains(receipt, `"idempotent_repeat":true`) {
		t.Fatalf("receipt = %s", receipt)
	}
	current, _ := readRunAndSteps(t, s, run.ID)
	if current.SelectedWorkflow == nil || current.SelectedWorkflow.Assignments["coordinator"] != agents[2].ID {
		t.Fatalf("accepted default was rewritten: %+v", current.SelectedWorkflow)
	}
}

// selectionModel drives one run: the coordinator selects a method, then every
// role answers in its own voice and the final role publishes one artifact.
type selectionModel struct {
	t           *testing.T
	store       *store.Store
	run         w.Run
	choice      selectionInput
	publishType string
	rootAnswer  string
	rootOrder   []string

	readRootBeforeAnswer bool

	mu               sync.Mutex
	calls            int
	unexpectedChoose bool
	rootSawChoose    bool
	texts            map[string]string
	published        map[string]string
	rootResults      []string
	rootResultErrors []bool
	readErrors       []string
}

func (m *selectionModel) Complete(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
	answer := func(text string) (model.Response, error) {
		if err := emit(text); err != nil {
			return model.Response{}, err
		}
		return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: text}}}, nil
	}
	tool := func(name string, input any) (model.Response, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return model.Response{}, err
		}
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: w.NewID(), Name: name, Input: raw}}}, nil
	}
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	var steps []w.Step
	if err := m.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(m.run.ID)
		return err
	}); err != nil {
		return model.Response{}, err
	}
	var step w.Step
	for _, s := range steps {
		if s.Status == "running" && strings.Contains(r.System, "You are "+s.AgentName+" (") {
			step = s
			break
		}
	}
	if step.ID == "" {
		return model.Response{}, errors.New("no running step matches the request")
	}
	hasChoose := slices.ContainsFunc(r.Tools, func(tool model.Tool) bool { return tool.Name == "choose_workflow" })
	hasHandoff := slices.ContainsFunc(r.Tools, func(tool model.Tool) bool { return tool.Name == "handoff_task" })
	hasPublish := slices.ContainsFunc(r.Tools, func(tool model.Tool) bool { return tool.Name == "publish_artifact" })
	m.mu.Lock()
	if step.Role == "responder" {
		m.rootSawChoose = hasChoose
	} else if hasChoose {
		m.unexpectedChoose = true
	}
	m.mu.Unlock()
	contextText := ""
	if len(r.Messages) > 0 && len(r.Messages[0].Content) > 0 {
		contextText = r.Messages[0].Content[0].Text
	}
	toolResult := len(r.Messages) > 1 && len(r.Messages[len(r.Messages)-1].Content) > 0 && r.Messages[len(r.Messages)-1].Content[0].Type == "tool_result"
	record := func(role, text string) {
		m.mu.Lock()
		if m.texts == nil {
			m.texts = map[string]string{}
		}
		m.texts[role+"\x00"+step.ID] = text
		m.mu.Unlock()
	}
	switch step.Role {
	case "responder":
		order := m.rootOrder
		if len(order) == 0 && m.choice.Workflow != "" {
			order = []string{"choose"}
		}
		m.mu.Lock()
		index := len(m.rootResults)
		m.mu.Unlock()
		if toolResult {
			last := r.Messages[len(r.Messages)-1].Content[0]
			m.mu.Lock()
			m.rootResults = append(m.rootResults, last.Content)
			m.rootResultErrors = append(m.rootResultErrors, last.IsError)
			m.mu.Unlock()
			index++
		}
		if index < len(order) {
			if index == 0 && m.rootAnswer != "" {
				if err := emit(m.rootAnswer); err != nil {
					return model.Response{}, err
				}
			}
			switch order[index] {
			case "choose":
				if !hasChoose {
					return model.Response{}, errors.New("planning root was not offered choose_workflow")
				}
				return tool("choose_workflow", m.choice)
			case "publish":
				return tool("publish_artifact", map[string]any{"type": "note", "title": "规划根提交", "body": "规划阶段提交", "findings": []any{}, "source_ids": []any{}, "limitations": []any{}})
			default:
				return model.Response{}, fmt.Errorf("unknown root action %q", order[index])
			}
		}
		return answer("已选择协作方法，交由平台执行。")
	case "discussant":
		m.mu.Lock()
		for key, text := range m.texts {
			if strings.HasPrefix(key, "discussant\x00") && strings.Contains(contextText, text) {
				m.t.Errorf("independent view read a sibling view: %s", text)
			}
		}
		m.mu.Unlock()
		if m.rootAnswer != "" && strings.Contains(contextText, m.rootAnswer) {
			m.t.Errorf("independent view read the planning root's answer")
		}
		if m.readRootBeforeAnswer {
			if !strings.Contains(contextText, m.run.Input.Message) {
				m.t.Error("original user task is not readable")
			}
			if !strings.Contains(r.System, "workflow="+m.choice.Workflow) {
				m.t.Error("selected method is not visible")
			}
			if toolResult {
				last := r.Messages[len(r.Messages)-1].Content[0]
				m.mu.Lock()
				m.readErrors = append(m.readErrors, last.Content)
				m.mu.Unlock()
			} else {
				id, err := completedMessageForRole(m.store, m.run, "responder")
				if err != nil {
					return model.Response{}, err
				}
				return tool("read_context", map[string]any{"kind": "message", "id": id})
			}
		}
		text := "独立观点 " + step.AgentName + " " + step.ID
		record("discussant", text)
		return answer(text)
	case "critic":
		text := "交叉质询 " + step.AgentName + " " + step.ID
		record("critic", text)
		return answer(text)
	case "synthesizer":
		text := "综合草案 " + step.ID
		record("synthesizer", text)
		return answer(text)
	case "checker":
		check := `{"status":"confirmed","correction":""}`
		if step.AgentName == "复核员" {
			check = `{"status":"disputed","correction":"请保留复核员提出的分歧。"}`
		}
		record("checker", check)
		return answer(check)
	case "researcher":
		text := "研究结论 " + step.ID
		record("researcher", text)
		return answer(text)
	case "coordinator", "reviewer":
		if toolResult {
			if hasHandoff {
				m.t.Error("selected plan steps still received handoff_task")
			}
			return answer("最终交付完成。")
		}
		if !hasPublish {
			return model.Response{}, errors.New("final role was not offered publish_artifact")
		}
		kind := m.publishType
		if kind == "" {
			kind = "note"
		}
		m.mu.Lock()
		if m.published == nil {
			m.published = map[string]string{}
		}
		m.published[step.ID] = kind
		m.mu.Unlock()
		return tool("publish_artifact", map[string]any{"type": kind, "title": "结论", "body": "最终交付内容", "findings": []any{}, "source_ids": []any{}, "limitations": []any{}})
	}
	return model.Response{}, fmt.Errorf("unexpected role %s", step.Role)
}

func startSelectionEngine(t *testing.T, e *Engine) {
	t.Helper()
	_, stop := startEngine(t, e)
	t.Cleanup(stop)
}

func completedMessageForRole(s *store.Store, run w.Run, role string) (string, error) {
	var steps []w.Step
	var messages []w.Message
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		if steps, err = tx.Steps(run.ID); err != nil {
			return err
		}
		messages, err = tx.Messages(run.ThreadID)
		return err
	}); err != nil {
		return "", err
	}
	for _, step := range steps {
		if step.Role != role || step.Status != "succeeded" {
			continue
		}
		for _, message := range messages {
			if message.StepID == step.ID && message.Status == "completed" {
				return message.ID, nil
			}
		}
	}
	return "", errors.New("no completed planning root message")
}

func readRunAndSteps(t *testing.T, s *store.Store, id string) (w.Run, []w.Step) {
	t.Helper()
	var run w.Run
	var steps []w.Step
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		if run, err = tx.Run(id); err != nil {
			return err
		}
		steps, err = tx.Steps(id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return run, steps
}

func TestSelectedDiscussionRunsFiveStagesAndDelivers(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	run.Status = "queued"
	root.Status = "pending"
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.SaveStep(root)
	}); err != nil {
		t.Fatal(err)
	}
	m := &selectionModel{t: t, store: s, run: run, choice: selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}, Reason: "需要独立观点与交叉质询"}}
	e.Model = m
	startSelectionEngine(t, e)
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status=%s err=%s", got.Status, got.Error)
	}
	if got.Method != "discussion" || got.SelectedWorkflow == nil || got.SelectedWorkflow.Workflow != "discussion" {
		t.Fatalf("method=%q selection=%+v", got.Method, got.SelectedWorkflow)
	}
	if got.Delivery == nil || len(got.Delivery.ArtifactRefs) != 1 || len(got.Delivery.MessageIDs) == 0 {
		t.Fatalf("delivery = %+v", got.Delivery)
	}
	_, steps := readRunAndSteps(t, s, run.ID)
	if len(steps) != 9 {
		t.Fatalf("steps = %d, want 9", len(steps))
	}
	roles := map[string]int{}
	var final w.Step
	for _, step := range steps {
		roles[step.Role]++
		if step.Role == "coordinator" {
			final = step
		}
	}
	if roles["discussant"] != 2 || roles["critic"] != 2 || roles["synthesizer"] != 1 || roles["checker"] != 2 || roles["coordinator"] != 1 {
		t.Fatalf("five-stage roles = %+v", roles)
	}
	var artifacts []w.Artifact
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		artifacts, err = tx.Artifacts(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	published := artifacts[0]
	if published.StepID != final.ID || published.Type != "note" {
		t.Fatalf("delivered artifact = %+v, want the plan coordinator's note", published)
	}
	if len(got.Delivery.ArtifactRefs) != 1 || got.Delivery.ArtifactRefs[0].ID != published.ID || got.Delivery.ArtifactRefs[0].Version != published.Version {
		t.Fatalf("delivery refs = %+v, artifact %s@%d", got.Delivery.ArtifactRefs, published.ID, published.Version)
	}
	for _, messageID := range got.Delivery.MessageIDs {
		if messageID == "" {
			t.Fatal("empty delivery message id")
		}
	}
	if !m.rootSawChoose {
		t.Fatal("planning root was not offered the selection tool")
	}
	if m.unexpectedChoose {
		t.Fatal("selected plan steps were offered a second method selection")
	}
}

func TestSelectedResearchRunsAcceptedThreeStepPlan(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	run.Status = "queued"
	root.Status = "pending"
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.SaveStep(root)
	}); err != nil {
		t.Fatal(err)
	}
	m := &selectionModel{t: t, store: s, run: run, publishType: "research_report", choice: selectionInput{Workflow: "research", Assignments: map[string]string{"researcher": agents[1].ID, "critic": agents[2].ID, "coordinator": agents[0].ID}, Reason: "需要证据"}}
	e.Model = m
	startSelectionEngine(t, e)
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status=%s err=%s", got.Status, got.Error)
	}
	if got.Method != "research" {
		t.Fatalf("method = %q", got.Method)
	}
	_, steps := readRunAndSteps(t, s, run.ID)
	if len(steps) != 4 {
		t.Fatalf("steps = %d, want root + 3", len(steps))
	}
	roles := []string{}
	for _, step := range steps[1:] {
		roles = append(roles, step.Role)
	}
	if !slices.Equal(roles, []string{"researcher", "critic", "coordinator"}) {
		t.Fatalf("research roles = %v", roles)
	}
	if got.Delivery == nil || len(got.Delivery.ArtifactRefs) != 1 {
		t.Fatalf("research delivery = %+v", got.Delivery)
	}
}

func TestSelectedViewsCannotReadPlanningRootAnswer(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	run.Status = "queued"
	root.Status = "pending"
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.SaveStep(root)
	}); err != nil {
		t.Fatal(err)
	}
	const secret = "规划根的完整答案：最终答案是 42。"
	m := &selectionModel{
		t: t, store: s, run: run,
		choice:               selectionInput{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}},
		rootAnswer:           secret,
		rootOrder:            []string{"choose"},
		readRootBeforeAnswer: true,
	}
	e.Model = m
	startSelectionEngine(t, e)
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status=%s err=%s", got.Status, got.Error)
	}
	if len(m.readErrors) != 2 {
		t.Fatalf("read_context attempts = %d, want one per independent view", len(m.readErrors))
	}
	for _, result := range m.readErrors {
		if !strings.Contains(result, "outside this attempt's committed context") {
			t.Fatalf("planning root message was readable: %s", result)
		}
	}
	var messages []w.Message
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		messages, err = tx.Messages(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range messages {
		if message.StepID == root.ID && message.Status == "completed" && strings.Contains(message.Content, secret) {
			found = true
		}
	}
	if !found {
		t.Fatal("fixture did not persist the planning root's answer")
	}
}

func TestPlanningRootPublicationFollowsPersistedSelection(t *testing.T) {
	newRun := func(t *testing.T) (*Engine, *store.Store, w.Run, []w.Agent, w.Step) {
		t.Helper()
		e, s, run, agents, root := seedMethodFixture(t, nil)
		run.Status = "queued"
		root.Status = "pending"
		if err := s.Write(context.Background(), func(tx *store.Tx) error {
			if err := tx.SaveRun(run); err != nil {
				return err
			}
			return tx.SaveStep(root)
		}); err != nil {
			t.Fatal(err)
		}
		return e, s, run, agents, root
	}
	research := func(agents []w.Agent) selectionInput {
		return selectionInput{Workflow: "research", Assignments: map[string]string{"researcher": agents[1].ID, "critic": agents[2].ID, "coordinator": agents[0].ID}}
	}
	artifactsOf := func(t *testing.T, s *store.Store, run w.Run) []w.Artifact {
		t.Helper()
		var artifacts []w.Artifact
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			artifacts, err = tx.Artifacts(run.ThreadID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return artifacts
	}
	rootAttempt := func(t *testing.T, s *store.Store, run w.Run, root w.Step) w.Attempt {
		t.Helper()
		for _, attempt := range loadAttempts(t, s, run.ID) {
			if attempt.StepID == root.ID {
				return attempt
			}
		}
		t.Fatal("planning root attempt not found")
		return w.Attempt{}
	}
	assertPlanDelivery := func(t *testing.T, s *store.Store, got w.Run, root w.Step) {
		t.Helper()
		artifacts := artifactsOf(t, s, got)
		if len(artifacts) != 1 || artifacts[0].StepID == root.ID {
			t.Fatalf("selected plan artifact = %+v", artifacts)
		}
		if got.Delivery == nil || !slices.Equal(got.Delivery.ArtifactRefs, []w.ArtifactRef{{ID: artifacts[0].ID, Version: artifacts[0].Version}}) {
			t.Fatalf("delivery = %+v", got.Delivery)
		}
	}

	t.Run("publish before selection stays a draft", func(t *testing.T) {
		e, s, run, agents, root := newRun(t)
		m := &selectionModel{t: t, store: s, run: run, publishType: "research_report", rootOrder: []string{"publish", "choose"}, choice: research(agents)}
		e.Model = m
		startSelectionEngine(t, e)
		got := awaitRun(t, s, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("status=%s err=%s", got.Status, got.Error)
		}
		if len(m.rootResultErrors) != 2 || m.rootResultErrors[0] || m.rootResultErrors[1] {
			t.Fatalf("root tool results = %v %v", m.rootResults, m.rootResultErrors)
		}
		if got.SelectedWorkflow == nil || got.SelectedWorkflow.Workflow != "research" {
			t.Fatalf("selection = %+v", got.SelectedWorkflow)
		}
		assertPlanDelivery(t, s, got, root)
		if rootAttempt(t, s, got, root).DraftArtifact == nil {
			t.Fatal("pre-selection publication was not kept as an attempt draft")
		}
	})

	t.Run("publish after selection is rejected", func(t *testing.T) {
		e, s, run, agents, root := newRun(t)
		m := &selectionModel{t: t, store: s, run: run, publishType: "research_report", rootOrder: []string{"choose", "publish"}, choice: research(agents)}
		e.Model = m
		startSelectionEngine(t, e)
		got := awaitRun(t, s, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("status=%s err=%s", got.Status, got.Error)
		}
		if len(m.rootResultErrors) != 2 || m.rootResultErrors[0] || !m.rootResultErrors[1] {
			t.Fatalf("root tool results = %v %v", m.rootResults, m.rootResultErrors)
		}
		if !strings.Contains(m.rootResults[1], "intermediate steps cannot publish") {
			t.Fatalf("publication rejection = %s", m.rootResults[1])
		}
		assertPlanDelivery(t, s, got, root)
		if rootAttempt(t, s, got, root).DraftArtifact != nil {
			t.Fatal("rejected publication left a draft")
		}
	})

	t.Run("free run without selection may publish", func(t *testing.T) {
		e, s, run, _, root := newRun(t)
		m := &selectionModel{t: t, store: s, run: run, rootOrder: []string{"publish"}}
		e.Model = m
		startSelectionEngine(t, e)
		got := awaitRun(t, s, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("status=%s err=%s", got.Status, got.Error)
		}
		if got.SelectedWorkflow != nil || got.Method != "direct" {
			t.Fatalf("selection=%+v method=%q", got.SelectedWorkflow, got.Method)
		}
		if len(m.rootResultErrors) != 1 || m.rootResultErrors[0] {
			t.Fatalf("root publication = %v %v", m.rootResults, m.rootResultErrors)
		}
		artifacts := artifactsOf(t, s, got)
		if len(artifacts) != 1 || artifacts[0].StepID != root.ID {
			t.Fatalf("free root artifact = %+v", artifacts)
		}
		if got.Delivery == nil || !slices.Equal(got.Delivery.ArtifactRefs, []w.ArtifactRef{{ID: artifacts[0].ID, Version: artifacts[0].Version}}) {
			t.Fatalf("free root delivery = %+v", got.Delivery)
		}
	})
}

type directModel struct {
	mu    sync.Mutex
	calls int
}

func (m *directModel) Complete(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	text := "我选择讨论方法，但这句话只是自述，不会派发。"
	if err := emit(text); err != nil {
		return model.Response{}, err
	}
	return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: text}}}, nil
}

func TestDirectShortAnswerNeedsNoClassification(t *testing.T) {
	e, s, run, _, root := seedMethodFixture(t, nil)
	run.Status = "queued"
	root.Status = "pending"
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.SaveStep(root)
	}); err != nil {
		t.Fatal(err)
	}
	m := &directModel{}
	e.Model = m
	startSelectionEngine(t, e)
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status=%s err=%s", got.Status, got.Error)
	}
	if m.calls != 1 {
		t.Fatalf("model calls = %d, want exactly one direct answer", m.calls)
	}
	if got.SelectedWorkflow != nil {
		t.Fatalf("model self-description dispatched a plan: %+v", got.SelectedWorkflow)
	}
	if got.Method != "direct" {
		t.Fatalf("method = %q, want direct", got.Method)
	}
	_, steps := readRunAndSteps(t, s, run.ID)
	if len(steps) != 1 {
		t.Fatalf("self-described method created steps: %d", len(steps))
	}
	if got.Delivery == nil || len(got.Delivery.MessageIDs) != 1 || len(got.Delivery.ArtifactRefs) != 0 {
		t.Fatalf("direct delivery = %+v", got.Delivery)
	}
	events := []w.Event{}
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		events, err = tx.Events(run.ID, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(event w.Event) bool { return event.Type == "method_recorded" }) {
		t.Fatal("direct method was not recorded as an event")
	}
}

func TestRestartKeepsSelectedMethodWithoutReclassification(t *testing.T) {
	e, s, run, agents, root := seedMethodFixture(t, nil)
	selection := &w.WorkflowSelection{Workflow: "discussion", Targets: []string{agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[0].ID}, StepID: root.ID, SelectedAt: w.Now()}
	run.SelectedWorkflow = selection
	run.Status = "running"
	root.Status = "succeeded"
	plan, err := Plan(run.ID, effectiveInput(run), agents)
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan {
		if len(plan[i].DependsOn) == 0 {
			plan[i].DependsOn = []string{root.ID}
		}
	}
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		if err := tx.SaveStep(root); err != nil {
			return err
		}
		if err := tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: root.ID, AgentID: agents[0].ID, AgentName: agents[0].DisplayName, Role: "assistant", Content: "已选择讨论方法。", Status: "completed", CreatedAt: w.Now()}); err != nil {
			return err
		}
		for _, step := range plan {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := &selectionModel{t: t, store: s, run: run}
	e.Model = m
	startSelectionEngine(t, e)
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status=%s err=%s", got.Status, got.Error)
	}
	if got.Method != "discussion" {
		t.Fatalf("method = %q after restart", got.Method)
	}
	if m.unexpectedChoose {
		t.Fatal("restarted run was offered a new method selection")
	}
	if got.Delivery == nil || len(got.Delivery.ArtifactRefs) != 1 {
		t.Fatalf("restarted delivery = %+v", got.Delivery)
	}
	if got.RecoveryCount == 0 {
		t.Fatal("run was not recovered before dispatch")
	}
}
