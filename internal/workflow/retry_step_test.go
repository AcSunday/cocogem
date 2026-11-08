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
		if err == nil && (account.UsedContinuations != 3 || account.ReservedRuns != 1) {
			t.Fatalf("retry reset spent budget or did not reserve capacity: %+v", account)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
