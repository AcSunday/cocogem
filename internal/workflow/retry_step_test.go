package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func retrySource(t *testing.T, specs []customStepSpec, statuses []string) (*store.Store, w.Run, []w.Step) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run := seedCustomRun(t, db, specs)
	var steps []w.Step
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for i := range steps {
			steps[i].Status = statuses[i]
			if err := tx.SaveStep(steps[i]); err != nil {
				return err
			}
			if statuses[i] == "succeeded" {
				if err := tx.SaveMessage(w.Message{ID: w.NewID(), RunID: run.ID, ThreadID: run.ThreadID, StepID: steps[i].ID, Role: "assistant", AgentID: steps[i].AgentID, Status: "completed", Content: "EXACT_UPSTREAM_" + steps[i].Instruction, CreatedAt: w.Now()}); err != nil {
					return err
				}
			}
		}
		run.Status, run.Error, run.FinishedAt = "failed", "synthetic member failure", w.Now()
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	return db, run, steps
}

func TestRetryStepExecutesOnlyFailedMemberAndPreservesHistory(t *testing.T) {
	db, source, old := retrySource(t, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "first"}, {Agent: 1, Role: "responder", Instruction: "failed"}, {Agent: 2, Role: "responder", Instruction: "third"}}, []string{"succeeded", "failed", "succeeded"})
	calls := 0
	runner := nativeRunnerFunc(func(_ context.Context, request model.Request, _ agent.Execute, observe agent.Observe) error {
		calls++
		if !strings.Contains(request.System, "@custom-b") {
			return errors.New("a successful member was replayed")
		}
		for _, message := range request.Messages {
			for _, block := range message.Content {
				if strings.Contains(block.Text, "EXACT_UPSTREAM_first") || strings.Contains(block.Text, "EXACT_UPSTREAM_third") {
					return errors.New("an independent retry read a completed sibling's reply")
				}
			}
		}
		return observe("text", map[string]any{"text": "Only the failed member retried."})
	})
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Limits: DefaultLimits()}
	retried, err := e.RetryStep(t.Context(), source.ID, old[1].ID, "stable-retry-member-0001")
	if err != nil {
		t.Fatal(err)
	}
	if retried.RetryOfStepID != old[1].ID || retried.Input.RetryOfRunID != source.ID || retried.Routing.Decision != "assign" || len(retried.Agents) != 1 {
		t.Fatalf("missing retry provenance: %+v", retried)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	finished := awaitRun(t, db, retried.ID)
	cancel()
	e.Wait()
	if finished.Status != "succeeded" || calls != 1 {
		t.Fatalf("status=%s calls=%d error=%s", finished.Status, calls, finished.Error)
	}
	// A response lost after completion must still return the same accepted retry.
	replay, err := e.RetryStep(t.Context(), source.ID, old[1].ID, "stable-retry-member-0001")
	if err != nil || replay.ID != retried.ID {
		t.Fatalf("settled retry replay=%+v error=%v", replay, err)
	}
	if again, err := e.RetryStep(t.Context(), source.ID, old[1].ID, "another-button-click-0001"); err != nil || again.ID != retried.ID {
		t.Fatalf("a second click replayed a settled member retry: %+v %v", again, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		original, err := tx.Run(source.ID)
		if err != nil {
			return err
		}
		steps, err := tx.Steps(retried.ID)
		if err != nil {
			return err
		}
		if original.Status != "failed" || len(steps) != 1 || steps[0].AgentID != old[1].AgentID {
			t.Fatalf("rewrote source or repeated peers: original=%s steps=%+v", original.Status, steps)
		}
		messages, err := tx.Messages(source.ThreadID)
		if err != nil {
			return err
		}
		if len(messages) != 3 {
			t.Fatalf("replayed a successful reply or user message: %+v", messages)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryStepReplacesTheUnfinishedMemberReply(t *testing.T) {
	db, source, old := retrySource(t, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "first"}, {Agent: 1, Role: "responder", Instruction: "failed"}}, []string{"succeeded", "failed"})
	partial := w.Message{ID: w.NewID(), ThreadID: source.ThreadID, RunID: source.ID, StepID: old[1].ID, AgentID: old[1].AgentID, AgentName: old[1].AgentName, Role: "assistant", Status: "failed", ContentFormat: w.ContentFormatChunksV1, Content: "unfinished draft", ContentBytes: len("unfinished draft"), CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveMessage(partial); err != nil {
			return err
		}
		return tx.AppendMessageChunk(partial.ID, 1, "unfinished draft")
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: db, Model: testutil.Model{}, AllowedModels: []string{"test"}, Limits: DefaultLimits()}
	if _, err := e.RetryStep(t.Context(), source.ID, old[1].ID, "replace-member-message-001"); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		messages, err := tx.Messages(source.ThreadID)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.ID == partial.ID {
				return errors.New("the replaced unfinished reply is still visible")
			}
		}
		var peers int
		for _, message := range messages {
			if message.RunID == source.ID && message.StepID == old[0].ID && message.Status == "completed" {
				peers++
			}
		}
		if peers != 1 {
			return fmt.Errorf("completed peer replies=%d, want 1", peers)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryStepCombinesCompletedRepairsBeforeContinuingWorkflow(t *testing.T) {
	db, source, old := retrySource(t, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "first failed"}, {Agent: 1, Role: "responder", Instruction: "second failed"}, {Agent: 2, Role: "responder", Instruction: "combine", DependsOn: []int{0, 1}}}, []string{"failed", "failed", "skipped"})
	runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		return observe("text", map[string]any{"text": "FIRST_REPAIR_RESULT"})
	})
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}}
	first, err := e.RetryStep(t.Context(), source.ID, old[0].ID, "first-member-repair-001")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	finished := awaitRun(t, db, first.ID)
	cancel()
	e.Wait()
	if finished.Status != "succeeded" {
		t.Fatal(finished.Error)
	}
	second, err := e.RetryStep(t.Context(), source.ID, old[1].ID, "second-member-repair-001")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(second.ID)
		if err != nil {
			return err
		}
		if len(steps) != 2 || steps[0].RetryOfStepID != old[1].ID || steps[1].RetryOfStepID != old[2].ID {
			t.Fatalf("lost repaired dependency or replayed first member: %+v", steps)
		}
		handoff, err := prepareStepHandoff(tx, second, steps[1], steps[1].AgentID, nil, nil)
		if err != nil {
			return err
		}
		if len(handoff.Messages) != 1 || handoff.Messages[0].Content != "FIRST_REPAIR_RESULT" || handoff.Messages[0].RunID != first.ID {
			t.Fatalf("did not reuse exact completed member repair: %+v", handoff.Messages)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryStepReusesExactUpstreamAndOnlyStartsBlockedDescendants(t *testing.T) {
	db, source, old := retrySource(t, []customStepSpec{{Agent: 0, Role: "researcher", Instruction: "evidence"}, {Agent: 1, Role: "critic", Instruction: "review", DependsOn: []int{0}}, {Agent: 0, Role: "researcher", Instruction: "failed response", DependsOn: []int{0, 1}}, {Agent: 1, Role: "critic", Instruction: "recheck", DependsOn: []int{0, 1, 2}}, {Agent: 2, Role: "coordinator", Instruction: "deliver", DependsOn: []int{0, 1, 2, 3}}}, []string{"succeeded", "succeeded", "failed", "skipped", "skipped"})
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error { return nil })}}
	retried, err := e.RetryStep(t.Context(), source.ID, old[2].ID, "retry-response-stable-001")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(retried.ID)
		if err != nil {
			return err
		}
		if len(steps) != 3 || steps[0].Instruction != old[2].Instruction || steps[1].Instruction != old[3].Instruction || steps[2].Instruction != old[4].Instruction {
			t.Fatalf("retry rebuilt completed work: %+v", steps)
		}
		if len(steps[0].DependsOn) != 0 || len(steps[0].ContextRefs) != 2 || !slices.Equal(steps[1].DependsOn, []string{steps[0].ID}) {
			t.Fatalf("missing upstream references or remapped dependencies: %+v", steps)
		}
		handoff, err := prepareStepHandoff(tx, retried, steps[0], steps[0].AgentID, nil, nil)
		if err != nil {
			return err
		}
		if len(handoff.Messages) != 2 || !strings.HasPrefix(handoff.Messages[0].Content, "EXACT_UPSTREAM_") {
			t.Fatalf("upstream evidence was lost: %+v", handoff.Messages)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryStepRejectsSuccessfulUnrelatedAndUncertainAttempts(t *testing.T) {
	db, source, old := retrySource(t, []customStepSpec{{Agent: 0, Role: "responder"}, {Agent: 1, Role: "responder"}}, []string{"succeeded", "failed"})
	e := Engine{Store: db, Model: testutil.Model{}}
	for _, id := range []string{old[0].ID, w.NewID()} {
		if _, err := e.RetryStep(t.Context(), source.ID, id, "invalid-member-retry-001"); err == nil {
			t.Fatal("accepted a step that did not fail in this run")
		}
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveAttempt(w.Attempt{ID: w.NewID(), RunID: source.ID, StepID: old[1].ID, Number: 1, Status: "uncertain", FailureKind: string(nativecli.FailureProcessUncertain)})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RetryStep(t.Context(), source.ID, old[1].ID, "uncertain-member-retry-001"); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("uncertain native execution was replayed: %v", err)
	}
}

// A member retry stays on its task revision and root budget account. It neither
// resets what automatic work already spent nor reserves automatic allowance.
func TestRetryStepKeepsTaskRevisionAndUsedRootBudget(t *testing.T) {
	db, source, old := retrySource(t, []customStepSpec{{Agent: 0, Role: "responder"}}, []string{"failed"})
	task, err := project.New(db).CreateTask(t.Context(), project.CreateTaskInput{WorkspaceID: source.WorkspaceID, ThreadID: source.ThreadID, Goal: source.Input.Message, SourceMessageID: "budget-proof", AcceptedBy: "user"})
	if err != nil {
		t.Fatal(err)
	}
	source.ID, source.TaskID, source.TaskRevision, source.BudgetAccountID = w.NewID(), task.Task.ID, task.Task.CurrentRevision, task.Budget.ID
	failed := old[0]
	failed.ID, failed.RunID = w.NewID(), source.ID
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(task.Budget.ID)
		if err != nil {
			return err
		}
		account.UsedContinuations, account.Revision = 3, account.Revision+1
		if err := tx.SaveBudgetAccount(account, account.Revision-1); err != nil {
			return err
		}
		if err := tx.InsertRun(source, "budget-source", "budget-source"); err != nil {
			return err
		}
		return tx.SaveStep(failed)
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: db, Model: testutil.Model{}}
	retried, err := e.RetryStep(t.Context(), source.ID, failed.ID, "shared-root-budget-retry-001")
	if err != nil {
		t.Fatal(err)
	}
	if retried.TaskID != task.Task.ID || retried.TaskRevision != task.Task.CurrentRevision || retried.BudgetAccountID != task.Budget.ID {
		t.Fatalf("retry changed task or budget identity: %+v", retried)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(task.Budget.ID)
		if err == nil && (account.UsedContinuations != 3 || account.ReservedRuns != 0) {
			t.Fatalf("retry reset spent budget or reserved an automatic continuation: %+v", account)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// taskRetrySource is a failed free run that belongs to a durable task. Every
// automatic allowance of its root budget is set to one so any accidental charge
// is visible immediately.
func taskRetrySource(t *testing.T) (*store.Store, project.TaskView, w.Run, w.Step) {
	t.Helper()
	db, source, old := retrySource(t, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "task work"}}, []string{"failed"})
	task, err := project.New(db).CreateTask(t.Context(), project.CreateTaskInput{WorkspaceID: source.WorkspaceID, ThreadID: source.ThreadID, Goal: source.Input.Message, SourceMessageID: "user-retry-policy", AcceptedBy: "user"})
	if err != nil {
		t.Fatal(err)
	}
	source.ID, source.TaskID, source.TaskRevision, source.BudgetAccountID = w.NewID(), task.Task.ID, task.Task.CurrentRevision, task.Budget.ID
	failed := old[0]
	failed.ID, failed.RunID = w.NewID(), source.ID
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(task.Budget.ID)
		if err != nil {
			return err
		}
		account.Policy.AutoContinuations, account.Policy.FixRounds, account.Policy.NoProgressFailures = 1, 1, 1
		account.Revision++
		if err := tx.SaveBudgetAccount(account, account.Revision-1); err != nil {
			return err
		}
		if err := tx.InsertRun(source, "task-retry-source", "task-retry-source"); err != nil {
			return err
		}
		return tx.SaveStep(failed)
	}); err != nil {
		t.Fatal(err)
	}
	return db, task, source, failed
}

func failedStep(t *testing.T, db *store.Store, runID string) w.Step {
	t.Helper()
	var failed w.Step
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(runID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.Status == "failed" {
				failed = step
				return nil
			}
		}
		return errors.New("the run has no failed step")
	}); err != nil {
		t.Fatal(err)
	}
	return failed
}

// The automatic continuation, repair-round and no-progress limits bound what
// the host starts by itself. A retry the user asks for is never one of those.
func TestUserMemberRetriesAreNeverCappedByAutomaticLimits(t *testing.T) {
	db, task, source, failed := taskRetrySource(t)
	calls := 0
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		calls++
		return errors.New("the member failed again")
	})
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Limits: DefaultLimits()}
	_, stop := startEngine(t, &e)
	defer stop()
	const clicks = 6
	runID, stepID := source.ID, failed.ID
	for click := 1; click <= clicks; click++ {
		retried, err := e.RetryStep(t.Context(), runID, stepID, fmt.Sprintf("user-retry-click-%04d-key", click))
		if err != nil {
			t.Fatalf("user retry %d was refused: %v", click, err)
		}
		if retried.AutomaticContinuation || retried.BudgetReserved {
			t.Fatalf("user retry %d was booked as an automatic continuation: automatic=%v reserved=%v", click, retried.AutomaticContinuation, retried.BudgetReserved)
		}
		finished := awaitRun(t, db, retried.ID)
		if finished.Status != "failed" || calls != click {
			t.Fatalf("user retry %d did not run: status=%s calls=%d error=%s", click, finished.Status, calls, finished.Error)
		}
		runID, stepID = retried.ID, failedStep(t, db, retried.ID).ID
	}
	view, err := project.New(db).Task(t.Context(), task.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.Status == "waiting" {
		t.Fatalf("user retries parked the task on %q", view.Task.WaitingReason)
	}
	if view.Budget.UsedContinuations != 0 || view.Budget.ReservedRuns != 0 || len(view.Budget.FixRoundsByIssue) != 0 || len(view.Budget.NoProgressByBranch) != 0 {
		t.Fatalf("user retries were charged to the automatic ledger: %+v", view.Budget)
	}
}

// A task the automatic policy already parked stays retryable by its user, and
// the retry neither erases nor adds to what the automatic ledger recorded.
func TestUserMemberRetryRunsForTaskParkedByAutomaticLimit(t *testing.T) {
	for _, reason := range []string{"no_progress", "fix_limit", "budget"} {
		t.Run(reason, func(t *testing.T) {
			db, task, source, failed := taskRetrySource(t)
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				account, err := tx.BudgetAccount(task.Budget.ID)
				if err != nil {
					return err
				}
				account.UsedContinuations = 1
				account.FixRoundsByIssue = map[string]int{source.ID: 1}
				account.NoProgressByBranch = map[string]int{source.ID: 1}
				account.Revision++
				if err := tx.SaveBudgetAccount(account, account.Revision-1); err != nil {
					return err
				}
				parked, err := tx.Task(task.Task.ID)
				if err != nil {
					return err
				}
				parked.Status, parked.WaitingReason = "waiting", reason
				return tx.SaveTask(parked, parked.CurrentRevision)
			}); err != nil {
				t.Fatal(err)
			}
			runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
				return observe("text", map[string]any{"text": "The user's retry finished the work."})
			})
			e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Limits: DefaultLimits()}
			retried, err := e.RetryStep(t.Context(), source.ID, failed.ID, "parked-task-user-retry-001")
			if err != nil {
				t.Fatalf("the user could not retry a parked task: %v", err)
			}
			_, stop := startEngine(t, &e)
			defer stop()
			if finished := awaitRun(t, db, retried.ID); finished.Status != "succeeded" {
				t.Fatalf("status=%s error=%s", finished.Status, finished.Error)
			}
			view, err := project.New(db).Task(t.Context(), task.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if view.Task.Status != "ready" || view.Task.WaitingReason != "" {
				t.Fatalf("task stayed parked after the user's retry ran: %+v", view.Task)
			}
			if view.Budget.UsedContinuations != 1 || view.Budget.FixRoundsByIssue[source.ID] != 1 {
				t.Fatalf("the user's retry rewrote the automatic ledger: %+v", view.Budget)
			}
		})
	}
}

// A message the user sends into a parked task is their own request too: the
// scheduler holds automatic continuations only.
func TestUserRunIsDispatchedWhileAutomaticLimitParksTheTask(t *testing.T) {
	db, task, source, _ := taskRetrySource(t)
	user := source
	user.ID, user.Status, user.Error, user.FinishedAt = w.NewID(), "queued", "", ""
	held := source
	held.ID, held.Status, held.Error, held.FinishedAt = w.NewID(), "queued", "", ""
	held.AutomaticContinuation = true
	other := w.Thread{ID: w.NewID(), WorkspaceID: source.WorkspaceID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(other) }); err != nil {
		t.Fatal(err)
	}
	second, err := project.New(db).CreateTask(t.Context(), project.CreateTaskInput{WorkspaceID: source.WorkspaceID, ThreadID: other.ID, Goal: "automatic follow-up", SourceMessageID: "held-automatic", AcceptedBy: "user"})
	if err != nil {
		t.Fatal(err)
	}
	held.ThreadID, held.TaskID, held.TaskRevision, held.BudgetAccountID = other.ID, second.Task.ID, second.Task.CurrentRevision, second.Budget.ID
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		for _, id := range []string{task.Task.ID, second.Task.ID} {
			parked, err := tx.Task(id)
			if err != nil {
				return err
			}
			parked.Status, parked.WaitingReason = "waiting", "no_progress"
			if err := tx.SaveTask(parked, parked.CurrentRevision); err != nil {
				return err
			}
		}
		for _, run := range []w.Run{user, held} {
			if err := tx.InsertRun(run, "parked-"+run.ID, "parked-"+run.ID); err != nil {
				return err
			}
			if err := tx.SaveStep(w.Step{ID: w.NewID(), RunID: run.ID, AgentID: run.Agents[0].ID, AgentName: run.Agents[0].DisplayName, Role: "responder", Instruction: "answer", Status: "pending"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		return observe("text", map[string]any{"text": "Answered the user's new message."})
	})
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Limits: DefaultLimits()}
	_, stop := startEngine(t, &e)
	defer stop()
	if finished := awaitRun(t, db, user.ID); finished.Status != "succeeded" {
		t.Fatalf("the user's run did not complete: status=%s error=%s", finished.Status, finished.Error)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		automatic, err := tx.Run(held.ID)
		if err == nil && automatic.Status != "queued" {
			t.Fatalf("an automatic continuation ran past its parked task: %s", automatic.Status)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
