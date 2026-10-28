package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestDiscussionPlanBuildsBoundedFiveStageDAG(t *testing.T) {
	agents := make([]w.Agent, 0, 4)
	for i, name := range []string{"A", "B", "C", "D"} {
		agentTools := []string{"read_source"}
		if i == 3 {
			agentTools = append(agentTools, "publish_artifact")
		}
		agents = append(agents, w.Agent{ID: fmt.Sprintf("member-%d", i), DisplayName: name, Handle: fmt.Sprintf("member-%d", i), Tools: agentTools})
	}
	in := w.RunInput{Workflow: "discussion", Targets: []string{"member-0", "member-1", "member-2", "member-3"}, Assignments: map[string]string{"coordinator": "member-3"}}
	plan, err := Plan("run", in, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 14 {
		t.Fatalf("steps = %d, want 3N+2 = 14", len(plan))
	}
	views, critiques := plan[:4], plan[4:8]
	draft, checks := plan[8], plan[9:13]
	final := plan[13]
	ids := func(steps []w.Step) []string {
		out := make([]string, len(steps))
		for i, step := range steps {
			out[i] = step.ID
		}
		return out
	}
	for i, step := range views {
		if step.Role != "discussant" || len(step.DependsOn) != 0 || step.AgentID != in.Targets[i] {
			t.Fatalf("independent view %d is not snapshot-isolated: %+v", i, step)
		}
	}
	for i, step := range critiques {
		want := append(ids(views), ids(critiques[:i])...)
		if step.Role != "critic" || !slices.Equal(step.DependsOn, want) {
			t.Fatalf("critique %d deps = %v, want %v", i, step.DependsOn, want)
		}
	}
	if draft.Role != "synthesizer" || draft.AgentID != "member-3" || !slices.Equal(draft.DependsOn, append(ids(views), ids(critiques)...)) {
		t.Fatalf("draft is not an intermediate synthesis of every view and critique: %+v", draft)
	}
	for i, step := range checks {
		want := []string{draft.ID, views[i].ID, critiques[i].ID}
		if step.Role != "checker" || step.AgentID != in.Targets[i] || !slices.Equal(step.DependsOn, want) {
			t.Fatalf("check %d is not the author reviewing the draft against its own view and revised critique: %+v", i, step)
		}
	}
	if final.Role != "coordinator" || !slices.Equal(final.DependsOn, append([]string{draft.ID}, ids(checks)...)) {
		t.Fatalf("final delivery does not consume the draft and every check: %+v", final)
	}
	for _, step := range plan[:len(plan)-1] {
		if RequiredArtifact(step, in) != "" {
			t.Fatalf("intermediate role %s has a publication obligation", step.Role)
		}
	}
	if RequiredArtifact(final, in) != "note" {
		t.Fatalf("final delivery artifact = %q, want note", RequiredArtifact(final, in))
	}
	// Only the final coordinator needs publish_artifact; the same member's
	// intermediate synthesizer step must not require it.
	in.Assignments = map[string]string{"coordinator": "member-2"}
	if _, err := Plan("run", in, agents); err == nil {
		t.Fatal("discussion accepted a coordinator without publish_artifact")
	}
	for _, bad := range []w.RunInput{
		{Workflow: "discussion", Targets: []string{"member-0"}, Assignments: map[string]string{"coordinator": "member-3"}},
		{Workflow: "discussion", Targets: []string{"member-0", "member-0"}, Assignments: map[string]string{"coordinator": "member-3"}},
		{Workflow: "discussion", Targets: []string{"member-0", "member-1", "member-2", "member-3", "member-0"}, Assignments: map[string]string{"coordinator": "member-3"}},
	} {
		if _, err := Plan("run", bad, agents); err == nil {
			t.Fatalf("invalid discussion selection was accepted: %+v", bad)
		}
	}
}

func TestDiscussionStagesSeeOnlyCommittedDependencies(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run, steps := seedDiscussionRun(t, s)
	if len(steps) != 11 {
		t.Fatalf("steps = %d, want 11", len(steps))
	}
	views, critiques := steps[:3], steps[3:6]
	draft, checks := steps[6], steps[7:10]
	final := steps[10]
	if draft.Role != "synthesizer" || final.Role != "coordinator" {
		t.Fatalf("unexpected stage roles: %s / %s", draft.Role, final.Role)
	}
	viewByAgent, critiqueByAgent := map[string]string{}, map[string]string{}
	for i, step := range views {
		viewByAgent[step.AgentID] = step.ID
		critiqueByAgent[step.AgentID] = critiques[i].ID
	}

	var mu sync.Mutex
	texts := map[string]string{}
	started := []string{}
	textOf := func(id string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		text, ok := texts[id]
		return text, ok
	}
	arrived := 0
	release := make(chan struct{})

	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		step, err := runningDiscussionStep(ctx, s, run, request.System)
		if err != nil {
			return err
		}
		text := fmt.Sprintf("%s reply from %s [%s]", step.Role, step.AgentName, step.ID)
		mu.Lock()
		texts[step.ID] = text
		started = append(started, step.ID)
		mu.Unlock()

		switch step.Role {
		case "discussant":
			if _, found, err := discussionHandoff(request); err != nil {
				return err
			} else if found {
				return errors.New("independent view received an upstream handoff")
			}
			mu.Lock()
			arrived++
			last := arrived == len(views)
			mu.Unlock()
			if err := assertNoCompletedReplies(ctx, s, run); err != nil {
				return err
			}
			if last {
				close(release)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("independent views were not dispatched concurrently")
			}
		case "critic":
			if err := assertOnlyRunningStep(ctx, s, run, step.ID); err != nil {
				return err
			}
			i := slices.IndexFunc(critiques, func(s w.Step) bool { return s.ID == step.ID })
			if i < 0 {
				return errors.New("critic step is outside the plan")
			}
			expectedSteps := append(stepIDs(views), stepIDs(critiques[:i])...)
			if err := assertDiscussionHandoff(ctx, execute, request, expectedSteps, textOf); err != nil {
				return err
			}
			unsettled, err := checkStatusDependencies(ctx, s, run, expectedSteps)
			if err != nil {
				return err
			}
			if len(unsettled) != 0 {
				return fmt.Errorf("critic started before %v succeeded", unsettled)
			}
		case "synthesizer":
			if err := assertOnlyRunningStep(ctx, s, run, step.ID); err != nil {
				return err
			}
			expectedSteps := append(stepIDs(views), stepIDs(critiques)...)
			if err := assertDiscussionHandoff(ctx, execute, request, expectedSteps, textOf); err != nil {
				return err
			}
			unsettled, err := checkStatusDependencies(ctx, s, run, expectedSteps)
			if err != nil {
				return err
			}
			if len(unsettled) != 0 {
				return fmt.Errorf("synthesis started before %v succeeded", unsettled)
			}
		case "checker":
			text = fmt.Sprintf(`{"status":"disputed","correction":"%s 的修订：请保留按任务判断，而不是一律多人协作。[%s]"}`, step.AgentName, step.ID)
			mu.Lock()
			texts[step.ID] = text
			mu.Unlock()
			// The revised position lives in this member's own critique.
			expectedSteps := []string{draft.ID, viewByAgent[step.AgentID], critiqueByAgent[step.AgentID]}
			if err := assertDiscussionHandoff(ctx, execute, request, expectedSteps, textOf); err != nil {
				return err
			}
			if err := assertDiscussionForbidden(ctx, s, run, execute, expectedSteps); err != nil {
				return err
			}
			unsettled, err := checkStatusDependencies(ctx, s, run, []string{draft.ID})
			if err != nil {
				return err
			}
			if len(unsettled) != 0 {
				return errors.New("check started before the draft synthesis succeeded")
			}
		case "coordinator":
			expectedSteps := append([]string{draft.ID}, stepIDs(checks)...)
			if err := assertDiscussionHandoff(ctx, execute, request, expectedSteps, textOf); err != nil {
				return err
			}
			if err := assertDiscussionForbidden(ctx, s, run, execute, expectedSteps); err != nil {
				return err
			}
			unsettled, err := checkStatusDependencies(ctx, s, run, stepIDs(checks))
			if err != nil {
				return err
			}
			if len(unsettled) != 0 {
				return fmt.Errorf("final delivery started before %v succeeded", unsettled)
			}
			if !slices.Contains(request.RequiredTools, "publish_artifact") {
				return errors.New("final delivery has no publication obligation")
			}
			handoff, found, err := discussionHandoff(request)
			if err != nil {
				return err
			}
			if !found {
				return errors.New("final delivery received no upstream handoff")
			}
			disputed := 0
			for _, message := range handoff.Messages {
				if !slices.ContainsFunc(checks, func(check w.Step) bool { return check.ID == message.StepID }) {
					continue
				}
				record, err := parseDiscussionCheck(message.Content)
				if err != nil {
					return fmt.Errorf("final handoff check %s is not a valid record: %w", message.StepID, err)
				}
				if record.Status == "disputed" && strings.Contains(record.Correction, "修订") {
					disputed++
				}
			}
			if disputed != len(checks) {
				return fmt.Errorf("final handoff carries %d disputed corrections, want %d", disputed, len(checks))
			}
			finalContext := request.Messages[0].Content[0].Text
			if !strings.Contains(finalContext, "Contributor check records") || !strings.Contains(finalContext, "status=disputed") || !strings.Contains(finalContext, "的修订") {
				return errors.New("final request did not consume the structured check corrections")
			}
			consensusClaim, _ := json.Marshal(map[string]any{"type": "note", "title": "讨论纪要", "body": "所有成员确认一致。", "findings": []any{map[string]any{"title": "共识", "detail": "checker 状态均为 confirmed", "status": "supported", "source_ids": []string{}}}, "source_ids": []string{}, "limitations": []string{"自动化测试样例。"}})
			if _, err := execute(ctx, "publish_artifact", consensusClaim); err == nil {
				return errors.New("model-asserted agreement was accepted as a supported finding")
			}
			payload, _ := json.Marshal(map[string]any{"type": "note", "title": "讨论纪要", "body": "最终交付：结合复查意见，保留真实分歧。", "findings": []any{}, "source_ids": []string{}, "limitations": []string{"自动化测试样例。"}})
			if _, err := execute(ctx, "publish_artifact", payload); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected discussion role %q", step.Role)
		}
		if step.Role != "coordinator" && slices.Contains(request.RequiredTools, "publish_artifact") {
			return fmt.Errorf("%s must not have a publication obligation", step.Role)
		}
		return observe("text", map[string]any{"text": text})
	})

	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	settled := awaitRun(t, s, run.ID)
	if settled.Status != "succeeded" {
		t.Fatalf("run status = %s, error = %s", settled.Status, settled.Error)
	}

	mu.Lock()
	order := slices.Clone(started)
	mu.Unlock()
	position := func(id string) int { return slices.Index(order, id) }
	for _, view := range views {
		for _, critique := range critiques {
			if position(view.ID) > position(critique.ID) {
				t.Fatalf("cross-critique %s ran before independent view %s", critique.ID, view.ID)
			}
		}
	}
	for i := 1; i < len(critiques); i++ {
		if position(critiques[i-1].ID) > position(critiques[i].ID) {
			t.Fatalf("cross-critique %d did not finish before critique %d started", i-1, i)
		}
	}
	if position(draft.ID) < position(critiques[len(critiques)-1].ID) {
		t.Fatal("synthesis started before cross-critique completed")
	}
	for _, check := range checks {
		if position(check.ID) < position(draft.ID) || position(final.ID) < position(check.ID) {
			t.Fatalf("check %s ran outside the draft-to-final window", check.ID)
		}
	}
	if position(final.ID) != len(order)-1 {
		t.Fatal("final delivery was not the last invocation")
	}

	var artifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		artifacts, readErr = tx.Artifacts(run.ThreadID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Type != "note" || artifacts[0].StepID != final.ID {
		t.Fatalf("only the final delivery may publish: %+v", artifacts)
	}
}

func TestDiscussionCheckReceiptValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		content    string
		status     string
		correction string
		bad        bool
	}{
		{"confirmed", `{"status":"confirmed","correction":""}`, "confirmed", "", false},
		{"disputed", `{"status":"disputed","correction":"请按任务判断"}`, "disputed", "请按任务判断", false},
		{"single fenced object", "```json\n{\"status\":\"disputed\",\"correction\":\"修正\"}\n```", "disputed", "修正", false},
		{"plain agreement", "我同意草案。", "", "", true},
		{"oversized receipt", `{"status":"confirmed","correction":"` + strings.Repeat("x", 4096) + `"}`, "", "", true},
		{"unknown status", `{"status":"agree","correction":""}`, "", "", true},
		{"disputed without correction", `{"status":"disputed","correction":"  "}`, "", "", true},
		{"extra field", `{"status":"confirmed","correction":"","analysis":"x"}`, "", "", true},
		{"trailing prose", `{"status":"confirmed","correction":""} ok`, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check, err := parseDiscussionCheck(tc.content)
			if (err != nil) != tc.bad {
				t.Fatalf("err = %v, want bad=%v", err, tc.bad)
			}
			if tc.bad {
				return
			}
			if check.Status != tc.status || check.Correction != tc.correction {
				t.Fatalf("check = %+v, want %s / %q", check, tc.status, tc.correction)
			}
		})
	}
}

func TestDiscussionInvalidCheckerReceiptStopsFinalDelivery(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run, _ := seedDiscussionRun(t, s)
	finalRan := false
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		step, err := runningDiscussionStep(ctx, s, run, request.System)
		if err != nil {
			return err
		}
		if step.Role == "coordinator" {
			finalRan = true
			return errors.New("final delivery ran despite an invalid checker receipt")
		}
		return observe("text", map[string]any{"text": "复查：基本同意草案。"})
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "invalid contributor check") {
		t.Fatalf("run = %s / %q, want failure naming the invalid checker receipt", got.Status, got.Error)
	}
	if finalRan {
		t.Fatal("final delivery ran despite an invalid checker receipt")
	}
}

func seedDiscussionRun(t *testing.T, s *store.Store) (w.Run, []w.Step) {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "discussion", Revision: 1}
	names := []string{"布偶猫", "缅因猫", "橘猫", "暹罗猫"}
	agents := make([]w.Agent, 0, len(names))
	for i, name := range names {
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: fmt.Sprintf("cat-%c", 'a'+i), Model: "default", Executor: "claude", Tools: tools.Names, Revision: 1})
	}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "queued", CreatedAt: w.Now(), Agents: agents, Input: w.RunInput{Message: "比较协作方案并保留真实分歧", Workflow: "discussion", ResearchScope: "given_sources", Targets: []string{agents[0].ID, agents[1].ID, agents[2].ID}, Assignments: map[string]string{"coordinator": agents[3].ID}}}
	steps, err := Plan(run.ID, run.Input, agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, a := range agents {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "discussion"); err != nil {
			return err
		}
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return run, steps
}

var discussionHandle = regexp.MustCompile(`\(@([^)]+)\)`)

func runningDiscussionStep(ctx context.Context, s *store.Store, run w.Run, system string) (w.Step, error) {
	match := discussionHandle.FindStringSubmatch(system)
	if match == nil {
		return w.Step{}, errors.New("request has no member handle")
	}
	agentID := ""
	for _, a := range run.Agents {
		if a.Handle == match[1] {
			agentID = a.ID
		}
	}
	if agentID == "" {
		return w.Step{}, fmt.Errorf("unknown member handle %q", match[1])
	}
	var found w.Step
	err := s.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.Status == "running" && step.AgentID == agentID {
				found = step
			}
		}
		return nil
	})
	if err != nil {
		return w.Step{}, err
	}
	if found.ID == "" {
		return w.Step{}, fmt.Errorf("no running step for %s", match[1])
	}
	return found, nil
}

const discussionHandoffMarker = "Exact upstream handoff (messages, artifact versions, findings and source IDs; bodies are bounded previews, use read_context with the exact id/version for more):"

func discussionHandoff(request model.Request) (Handoff, bool, error) {
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type != "text" {
				continue
			}
			_, rest, found := strings.Cut(block.Text, discussionHandoffMarker)
			if !found {
				continue
			}
			var handoff Handoff
			if err := json.NewDecoder(strings.NewReader(rest)).Decode(&handoff); err != nil {
				return Handoff{}, false, err
			}
			return handoff, true, nil
		}
	}
	return Handoff{}, false, nil
}

func assertDiscussionHandoff(ctx context.Context, execute agent.Execute, request model.Request, expected []string, textOf func(string) (string, bool)) error {
	handoff, found, err := discussionHandoff(request)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("dependent stage received no upstream handoff")
	}
	byStep := map[string]string{}
	for _, message := range handoff.Messages {
		byStep[message.StepID] = message.ID
	}
	if len(byStep) != len(expected) {
		return fmt.Errorf("handoff carries %d upstream steps, want %d", len(byStep), len(expected))
	}
	for _, stepID := range expected {
		id, ok := byStep[stepID]
		if !ok {
			return fmt.Errorf("handoff is missing committed dependency %s", stepID)
		}
		want, ok := textOf(stepID)
		if !ok {
			return fmt.Errorf("upstream text for %s is missing", stepID)
		}
		got, err := discussionReadMessage(ctx, execute, id)
		if err != nil {
			return fmt.Errorf("read upstream %s: %w", stepID, err)
		}
		if got != want {
			return fmt.Errorf("upstream %s content = %q, want %q", stepID, got, want)
		}
	}
	return nil
}

func assertDiscussionForbidden(ctx context.Context, s *store.Store, run w.Run, execute agent.Execute, allowed []string) error {
	messages, err := discussionMessages(ctx, s, run)
	if err != nil {
		return err
	}
	permitted := map[string]bool{}
	for _, id := range allowed {
		permitted[id] = true
	}
	for _, message := range messages {
		if message.RunID != run.ID || message.Role != "assistant" || message.Status != "completed" || permitted[message.StepID] {
			continue
		}
		if _, err := discussionReadMessage(ctx, execute, message.ID); err == nil {
			return fmt.Errorf("read_context exposed non-dependency message from step %s", message.StepID)
		}
	}
	return nil
}

func discussionReadMessage(ctx context.Context, execute agent.Execute, id string) (string, error) {
	raw, _ := json.Marshal(map[string]any{"kind": "message", "id": id, "max_bytes": 8192})
	result, err := execute(ctx, "read_context", raw)
	if err != nil {
		return "", err
	}
	var page struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(result), &page); err != nil {
		return "", err
	}
	return page.Content, nil
}

func discussionMessages(ctx context.Context, s *store.Store, run w.Run) ([]w.Message, error) {
	var messages []w.Message
	err := s.Read(ctx, func(tx *store.Tx) error {
		var err error
		messages, err = tx.MessagesMetadata(run.ThreadID)
		return err
	})
	return messages, err
}

func assertNoCompletedReplies(ctx context.Context, s *store.Store, run w.Run) error {
	messages, err := discussionMessages(ctx, s, run)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.RunID == run.ID && message.Role == "assistant" && message.Status == "completed" {
			return errors.New("independent view started after a sibling reply was already committed")
		}
	}
	return nil
}

func assertOnlyRunningStep(ctx context.Context, s *store.Store, run w.Run, stepID string) error {
	var running []string
	if err := s.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.Status == "running" {
				running = append(running, step.ID)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if len(running) != 1 || running[0] != stepID {
		return fmt.Errorf("serial stage started with running steps %v", running)
	}
	return nil
}

func checkStatusDependencies(ctx context.Context, s *store.Store, run w.Run, ids []string) ([]string, error) {
	unsettled := []string{}
	err := s.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, id := range ids {
			i := slices.IndexFunc(steps, func(step w.Step) bool { return step.ID == id })
			if i < 0 || steps[i].Status != "succeeded" {
				unsettled = append(unsettled, id)
			}
		}
		return nil
	})
	return unsettled, err
}

func stepIDs(steps []w.Step) []string {
	ids := make([]string, len(steps))
	for i, step := range steps {
		ids[i] = step.ID
	}
	return ids
}
