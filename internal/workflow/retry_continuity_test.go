package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// freeCollaborationSource is a failed free run whose conversation holds three
// members with the first as coordinator, the way an unassigned user message is
// accepted. link wires step kinds and parents after the plan exists.
func freeCollaborationSource(t *testing.T, specs []customStepSpec, statuses []string, link func(run *w.Run, steps []w.Step)) (*store.Store, w.Run, []w.Step) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	source := seedCustomRun(t, db, specs)
	var steps []w.Step
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		thread, err := tx.Thread(source.ThreadID)
		if err != nil {
			return err
		}
		for _, member := range source.Agents {
			thread.AgentIDs = append(thread.AgentIDs, member.ID)
		}
		thread.CoordinatorID = source.Agents[0].ID
		thread.Revision++
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if steps, err = tx.Steps(source.ID); err != nil {
			return err
		}
		source.CoordinatorID = thread.CoordinatorID
		source.Routing = w.Routing{Decision: "discuss", Workflow: "free", Targets: []string{thread.CoordinatorID}}
		for i := range steps {
			steps[i].Status = statuses[i]
		}
		if link != nil {
			link(&source, steps)
		}
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if step.Status != "succeeded" {
				continue
			}
			if err := tx.SaveMessage(w.Message{ID: w.NewID(), RunID: source.ID, ThreadID: source.ThreadID, StepID: step.ID, Role: "assistant", AgentID: step.AgentID, Status: "completed", Content: "EXACT_UPSTREAM_" + step.Instruction, CreatedAt: w.Now()}); err != nil {
				return err
			}
		}
		// A settled run is immutable, so everything above is written before it
		// becomes terminal.
		source.Status, source.Error, source.FinishedAt = "failed", "synthetic member failure", w.Now()
		return tx.SaveRun(source)
	}); err != nil {
		t.Fatal(err)
	}
	return db, source, steps
}

func toolNames(request model.Request) []string {
	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// memberScript answers for whichever member the request addresses.
type memberScript struct {
	mu    sync.Mutex
	calls []string
	tools map[string][]string
	text  map[string]string
	act   map[string]func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error
}

func (m *memberScript) run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	handle := ""
	for _, candidate := range []string{"custom-a", "custom-b", "custom-c"} {
		if strings.Contains(request.System, "@"+candidate) {
			handle = candidate
		}
	}
	prompt := strings.Builder{}
	for _, message := range request.Messages {
		for _, block := range message.Content {
			prompt.WriteString(block.Text)
		}
	}
	m.mu.Lock()
	m.calls = append(m.calls, handle)
	if m.tools == nil {
		m.tools = map[string][]string{}
	}
	if m.text == nil {
		m.text = map[string]string{}
	}
	m.tools[handle] = toolNames(request)
	m.text[handle] = prompt.String()
	act := m.act[handle]
	m.mu.Unlock()
	if act == nil {
		return observe("text", map[string]any{"text": "reply from " + handle})
	}
	return act(ctx, request, execute, observe)
}

func runRetry(t *testing.T, db *store.Store, script *memberScript, sourceID, stepID string) (w.Run, []w.Step) {
	t.Helper()
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(script.run)}, Limits: DefaultLimits()}
	retried, err := e.RetryStep(t.Context(), sourceID, stepID, "continuity-retry-"+stepID[:8]+"-key")
	if err != nil {
		t.Fatal(err)
	}
	_, stop := startEngine(t, &e)
	finished := awaitRun(t, db, retried.ID)
	stop()
	var steps []w.Step
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(retried.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return finished, steps
}

func deliveredText(t *testing.T, db *store.Store, run w.Run) string {
	t.Helper()
	text := strings.Builder{}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		if run.Delivery == nil {
			return nil
		}
		for _, id := range run.Delivery.MessageIDs {
			preview, err := tx.MessagePreview(id, 6000)
			if err != nil {
				return err
			}
			text.WriteString(preview.Content)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return text.String()
}

func relayTo(target w.Agent, task string) func(context.Context, model.Request, agent.Execute, agent.Observe) error {
	return func(ctx context.Context, _ model.Request, execute agent.Execute, observe agent.Observe) error {
		input, _ := json.Marshal(map[string]any{"request_id": "continue-after-retry", "target_agent_id": target.ID, "task": task, "role": "member", "mode": "relay"})
		if _, err := execute(ctx, "handoff_task", input); err != nil {
			return fmt.Errorf("the retried member could not hand off: %w", err)
		}
		return observe("text", map[string]any{"text": "handed off"})
	}
}

// The coordinator's planning step failed before it could dispatch anyone. Its
// retry must be able to do what the original step could: reach the whole
// conversation roster and choose a method.
func TestRetriedPlanningRootKeepsItsRosterAndCollaborationTools(t *testing.T) {
	db, source, steps := freeCollaborationSource(t, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "coordinate the work"}}, []string{"failed"}, nil)
	script := &memberScript{act: map[string]func(context.Context, model.Request, agent.Execute, agent.Observe) error{
		"custom-a": relayTo(source.Agents[1], "carry out the work the coordinator planned"),
	}}
	finished, retried := runRetry(t, db, script, source.ID, steps[0].ID)
	if finished.Status != "succeeded" {
		t.Fatalf("status=%s error=%s", finished.Status, finished.Error)
	}
	if len(finished.Agents) != 3 || finished.Routing.Decision != "discuss" {
		t.Fatalf("the retry lost the conversation roster or its routing: members=%d decision=%q", len(finished.Agents), finished.Routing.Decision)
	}
	for _, name := range []string{"handoff_task", "choose_workflow"} {
		if !slices.Contains(script.tools["custom-a"], name) {
			t.Fatalf("the retried coordinator was not offered %s: %v", name, script.tools["custom-a"])
		}
	}
	if !slices.Equal(script.calls, []string{"custom-a", "custom-b"}) || len(retried) != 2 || retried[1].Kind != "relay" {
		t.Fatalf("the follow-up member never ran: calls=%v steps=%+v", script.calls, retried)
	}
	if got := deliveredText(t, db, finished); got != "reply from custom-b" {
		t.Fatalf("delivery = %q, want the follow-up member's reply", got)
	}
}

// Retry runs queued before a retried step kept its place recorded it as an
// assigned, stand-alone repair with a one-member roster. Retrying such a run
// again restores what the step could do where its retry chain started.
func TestRetryOfAnEarlierStandAloneRetryRestoresTheOriginalStep(t *testing.T) {
	tests := []struct {
		name        string
		specs       []customStepSpec
		statuses    []string
		link        func(run *w.Run, steps []w.Step)
		failed      int
		earlierKind string
		wantKind    string
	}{
		{
			name:     "the coordinator's planning root",
			specs:    []customStepSpec{{Agent: 0, Role: "responder", Instruction: "coordinate the work"}},
			statuses: []string{"failed"},
			wantKind: "",
		},
		{
			name: "the execution step after task promotion",
			specs: []customStepSpec{
				{Agent: 0, Role: "task_intake", Instruction: "intake"},
				{Agent: 0, Role: "responder", Instruction: "continue the accepted objective", DependsOn: []int{0}, Kind: "continuation"},
			},
			statuses: []string{"succeeded", "failed"},
			link: func(run *w.Run, steps []w.Step) {
				run.TaskPromotionStepID = steps[0].ID
				steps[1].FromStepID = steps[0].ID
			},
			failed: 1, earlierKind: kindRetryResult, wantKind: kindRetryRoot,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, source, steps := freeCollaborationSource(t, test.specs, test.statuses, test.link)
			earlier := source
			earlier.ID, earlier.Status, earlier.Error, earlier.FinishedAt = w.NewID(), "queued", "", ""
			earlier.Input.RetryOfRunID, earlier.RetryOfStepID = source.ID, steps[test.failed].ID
			earlier.Routing.Decision, earlier.TaskPromotionStepID = "assign", ""
			earlier.Agents = source.Agents[:1]
			failed := steps[test.failed]
			failed.ID, failed.RunID, failed.RetryOfStepID = w.NewID(), earlier.ID, steps[test.failed].ID
			failed.Kind, failed.FromStepID, failed.DependsOn = test.earlierKind, "", nil
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				if err := tx.InsertRun(earlier, "earlier-stand-alone-retry", "earlier-stand-alone-retry"); err != nil {
					return err
				}
				if err := tx.SaveStep(failed); err != nil {
					return err
				}
				earlier.Status, earlier.Error, earlier.FinishedAt = "failed", "the member failed again", w.Now()
				return tx.SaveRun(earlier)
			}); err != nil {
				t.Fatal(err)
			}
			script := &memberScript{}
			finished, retried := runRetry(t, db, script, earlier.ID, failed.ID)
			if finished.Status != "succeeded" {
				t.Fatalf("status=%s error=%s", finished.Status, finished.Error)
			}
			if len(finished.Agents) != 3 || finished.Routing.Decision != "discuss" {
				t.Fatalf("roster or routing was not restored: members=%d decision=%q", len(finished.Agents), finished.Routing.Decision)
			}
			if len(retried) != 1 || retried[0].Kind != test.wantKind {
				t.Fatalf("the step did not regain its place: %+v", retried)
			}
			for _, name := range []string{"handoff_task", "choose_workflow", "publish_artifact"} {
				if !slices.Contains(script.tools["custom-a"], name) {
					t.Fatalf("the restored step was not offered %s: %v", name, script.tools["custom-a"])
				}
			}
		})
	}
}

// After promote_to_task the work runs in a fresh execution step. When that step
// fails, its retry is still the run's executor: it dispatches teammates and
// owns the delivery, files included.
func TestRetriedExecutionStepAfterPromotionStillDispatchesAndDelivers(t *testing.T) {
	db, source, steps := freeCollaborationSource(t,
		[]customStepSpec{{Agent: 0, Role: "task_intake", Instruction: "intake"}, {Agent: 0, Role: "responder", Instruction: "continue the accepted objective", DependsOn: []int{0}, Kind: "continuation"}},
		[]string{"succeeded", "failed"},
		func(run *w.Run, steps []w.Step) {
			run.TaskPromotionStepID = steps[0].ID
			steps[1].FromStepID = steps[0].ID
		})
	script := &memberScript{}
	finished, retried := runRetry(t, db, script, source.ID, steps[1].ID)
	if finished.Status != "succeeded" {
		t.Fatalf("status=%s error=%s", finished.Status, finished.Error)
	}
	if len(retried) != 1 || retried[0].Kind != "retry_root" {
		t.Fatalf("the retried executor is not the head of the delivery chain: %+v", retried)
	}
	for _, name := range []string{"handoff_task", "choose_workflow", "publish_artifact"} {
		if !slices.Contains(script.tools["custom-a"], name) {
			t.Fatalf("the retried executor was not offered %s: %v", name, script.tools["custom-a"])
		}
	}
	if strings.Contains(script.text["custom-a"], "select another workflow") {
		t.Fatal("the retried executor was told not to organise the work it still has to dispatch")
	}
}

// A relay chain stops at the member that failed. Retrying that member resumes
// the chain: it can hand the work on to the next member.
func TestRetriedRelayMemberCanRelayOnward(t *testing.T) {
	db, source, steps := freeCollaborationSource(t,
		[]customStepSpec{{Agent: 0, Role: "responder", Instruction: "coordinate"}, {Agent: 1, Role: "engineer", Instruction: "implement", DependsOn: []int{0}, Kind: "relay"}},
		[]string{"succeeded", "failed"},
		func(_ *w.Run, steps []w.Step) { steps[1].FromStepID = steps[0].ID })
	script := &memberScript{act: map[string]func(context.Context, model.Request, agent.Execute, agent.Observe) error{
		"custom-b": relayTo(source.Agents[2], "review the implementation"),
	}}
	finished, retried := runRetry(t, db, script, source.ID, steps[1].ID)
	if finished.Status != "succeeded" {
		t.Fatalf("status=%s error=%s", finished.Status, finished.Error)
	}
	if !slices.Equal(script.calls, []string{"custom-b", "custom-c"}) || len(retried) != 2 {
		t.Fatalf("the chain did not resume: calls=%v steps=%+v", script.calls, retried)
	}
	if retried[0].Kind != "retry_root" || finished.Routing.Decision != "assign" {
		t.Fatalf("a relay member became a planning root: kind=%q decision=%q", retried[0].Kind, finished.Routing.Decision)
	}
	if slices.Contains(script.tools["custom-b"], "choose_workflow") {
		t.Fatal("a retried relay member was offered workflow selection")
	}
	if got := deliveredText(t, db, finished); got != "reply from custom-c" {
		t.Fatalf("delivery = %q, want the next member's reply", got)
	}
}

// A delegated member failed and the delegator already wrapped up without its
// result. When the retry succeeds the result goes back to the delegator in a
// fresh continuation, instead of being left where nobody reads it.
func TestRetriedDelegatedMemberReturnsItsResultToTheDelegator(t *testing.T) {
	db, source, steps := freeCollaborationSource(t,
		[]customStepSpec{
			{Agent: 0, Role: "responder", Instruction: "coordinate"},
			{Agent: 1, Role: "member", Instruction: "delegated subtask", DependsOn: []int{0}, Kind: "handoff"},
			{Agent: 0, Role: "responder", Instruction: "earlier synthesis", DependsOn: []int{0, 1}, Kind: "continuation"},
		},
		[]string{"succeeded", "failed", "succeeded"},
		func(_ *w.Run, steps []w.Step) {
			steps[1].FromStepID, steps[1].ContinuationID = steps[0].ID, steps[2].ID
			steps[2].FromStepID = steps[0].ID
		})
	script := &memberScript{act: map[string]func(context.Context, model.Request, agent.Execute, agent.Observe) error{
		"custom-b": func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
			return observe("text", map[string]any{"text": "REPAIRED_SUBTASK_RESULT"})
		},
		"custom-a": func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
			return observe("text", map[string]any{"text": "UPDATED_SYNTHESIS"})
		},
	}}
	finished, retried := runRetry(t, db, script, source.ID, steps[1].ID)
	if finished.Status != "succeeded" {
		t.Fatalf("status=%s error=%s", finished.Status, finished.Error)
	}
	if !slices.Equal(script.calls, []string{"custom-b", "custom-a"}) || len(retried) != 2 {
		t.Fatalf("the delegator was not called back: calls=%v steps=%+v", script.calls, retried)
	}
	if retried[1].RetryOfStepID != steps[2].ID || !slices.Equal(retried[1].DependsOn, []string{retried[0].ID}) {
		t.Fatalf("the fresh continuation is not bound to the repaired result: %+v", retried[1])
	}
	seen := script.text["custom-a"]
	if !strings.Contains(seen, "REPAIRED_SUBTASK_RESULT") || !strings.Contains(seen, "EXACT_UPSTREAM_earlier synthesis") {
		t.Fatalf("the delegator did not receive the repaired result next to its earlier reply:\n%s", seen)
	}
	if got := deliveredText(t, db, finished); got != "UPDATED_SYNTHESIS" {
		t.Fatalf("delivery = %q, want the delegator's updated reply", got)
	}
}

// A second repaired member reports to the delegator's newest reply, not to the
// one the first repair already replaced.
func TestSecondRepairedMemberReachesTheLatestDelegatorReply(t *testing.T) {
	db, source, steps := freeCollaborationSource(t,
		[]customStepSpec{
			{Agent: 0, Role: "responder", Instruction: "coordinate"},
			{Agent: 1, Role: "member", Instruction: "first subtask", DependsOn: []int{0}, Kind: "handoff"},
			{Agent: 2, Role: "member", Instruction: "second subtask", DependsOn: []int{0}, Kind: "handoff"},
			{Agent: 0, Role: "responder", Instruction: "earlier synthesis", DependsOn: []int{0, 1, 2}, Kind: "continuation"},
		},
		[]string{"succeeded", "failed", "failed", "succeeded"},
		func(_ *w.Run, steps []w.Step) {
			for _, child := range []int{1, 2} {
				steps[child].FromStepID, steps[child].ContinuationID = steps[0].ID, steps[3].ID
			}
			steps[3].FromStepID = steps[0].ID
		})
	first := &memberScript{act: map[string]func(context.Context, model.Request, agent.Execute, agent.Observe) error{
		"custom-a": func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
			return observe("text", map[string]any{"text": "SYNTHESIS_AFTER_FIRST_REPAIR"})
		},
	}}
	if finished, _ := runRetry(t, db, first, source.ID, steps[1].ID); finished.Status != "succeeded" {
		t.Fatalf("first repair: status=%s error=%s", finished.Status, finished.Error)
	}
	second := &memberScript{}
	finished, _ := runRetry(t, db, second, source.ID, steps[2].ID)
	if finished.Status != "succeeded" {
		t.Fatalf("second repair: status=%s error=%s", finished.Status, finished.Error)
	}
	seen := second.text["custom-a"]
	if !strings.Contains(seen, "SYNTHESIS_AFTER_FIRST_REPAIR") || strings.Contains(seen, "EXACT_UPSTREAM_earlier synthesis") {
		t.Fatalf("the delegator was handed a reply its first repair already replaced:\n%s", seen)
	}
}

// A built-in workflow keeps its accepted plan: the retry roster is the plan's
// members and no retried step may reorganise the work.
func TestRetriedBuiltInStepKeepsItsAcceptedPlan(t *testing.T) {
	db, source, steps := freeCollaborationSource(t,
		[]customStepSpec{{Agent: 0, Role: "researcher", Instruction: "evidence"}, {Agent: 1, Role: "critic", Instruction: "review", DependsOn: []int{0}}},
		[]string{"succeeded", "failed"},
		func(run *w.Run, _ []w.Step) {
			run.Input.Workflow = "research"
			run.Routing = w.Routing{Decision: "assign", Workflow: "research"}
		})
	script := &memberScript{}
	finished, retried := runRetry(t, db, script, source.ID, steps[1].ID)
	if finished.Status != "succeeded" {
		t.Fatalf("status=%s error=%s", finished.Status, finished.Error)
	}
	if len(finished.Agents) != 1 || len(retried) != 1 || retried[0].Kind == kindRetryRoot {
		t.Fatalf("a built-in retry changed its roster or plan: members=%d steps=%+v", len(finished.Agents), retried)
	}
	for _, name := range []string{"handoff_task", "choose_workflow"} {
		if slices.Contains(script.tools["custom-b"], name) {
			t.Fatalf("a built-in workflow step was offered %s", name)
		}
	}
	if !strings.Contains(script.text["custom-b"], "accepted plan") {
		t.Fatal("the built-in retry lost its accepted-plan instruction")
	}
}
