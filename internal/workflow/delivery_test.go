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

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func openDeliveryStore(t *testing.T) *store.Store {
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
	return s
}

func deliverySteps(t *testing.T, s *store.Store, runID string) []w.Step {
	t.Helper()
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return steps
}

func deliveryMessages(t *testing.T, s *store.Store, run w.Run) []w.Message {
	t.Helper()
	var messages []w.Message
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		messages, err = tx.Messages(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return messages
}

func runningStepFor(ctx context.Context, s *store.Store, run w.Run, request model.Request) (w.Step, error) {
	var steps []w.Step
	if err := s.Read(ctx, func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(run.ID)
		return err
	}); err != nil {
		return w.Step{}, err
	}
	for _, step := range steps {
		if step.Status == "running" && strings.Contains(request.System, "You are "+step.AgentName+" (") {
			return step, nil
		}
	}
	return w.Step{}, errors.New("running step not found")
}

func submitDeliveryHandoff(ctx context.Context, execute agent.Execute, requestID, targetID, task string) error {
	raw, _ := json.Marshal(handoffInput{RequestID: requestID, TargetAgentID: targetID, Task: task, Role: "member"})
	_, err := execute(ctx, "handoff_task", raw)
	return err
}

func completedMessageID(t *testing.T, messages []w.Message, stepID string) string {
	t.Helper()
	for _, message := range messages {
		if message.StepID == stepID && message.Status == "completed" {
			return message.ID
		}
	}
	t.Fatalf("no completed message for step %s", stepID)
	return ""
}

func TestFreeShortAnswerRecordsExplicitDeliveryMessage(t *testing.T) {
	s := openDeliveryStore(t)
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer briefly"}})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		return observe("text", map[string]any{"text": "直接短答"})
	})}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("run = %s: %s", got.Status, got.Error)
	}
	steps := deliverySteps(t, s, run.ID)
	messages := deliveryMessages(t, s, run)
	if got.Delivery == nil || len(got.Delivery.MessageIDs) != 1 || len(got.Delivery.ArtifactRefs) != 0 {
		t.Fatalf("delivery = %+v", got.Delivery)
	}
	if got.Delivery.MessageIDs[0] != completedMessageID(t, messages, steps[0].ID) {
		t.Fatalf("delivery did not reference the compound step's completed message: %+v", got.Delivery)
	}
}

func TestFreeMultipleRootsKeepEveryFinalReply(t *testing.T) {
	s := openDeliveryStore(t)
	run := seedCustomRun(t, s, []customStepSpec{
		{Agent: 0, Role: "responder", Instruction: "delegate then answer"},
		{Agent: 1, Role: "responder", Instruction: "answer directly"},
	})
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		step, err := runningStepFor(ctx, s, run, request)
		if err != nil {
			return err
		}
		switch step.Kind {
		case "":
			if step.AgentID == run.Agents[0].ID {
				return submitDeliveryHandoff(ctx, execute, "delegate", run.Agents[2].ID, "collect evidence")
			}
			return observe("text", map[string]any{"text": "第二个根目标的答复"})
		case "handoff":
			return observe("text", map[string]any{"text": "委派成员的结果"})
		case "continuation":
			return observe("text", map[string]any{"text": "第一个根目标的最终答复"})
		}
		return fmt.Errorf("unexpected step kind %q", step.Kind)
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("run = %s: %s", got.Status, got.Error)
	}
	steps := deliverySteps(t, s, run.ID)
	messages := deliveryMessages(t, s, run)
	byID := map[string]w.Step{}
	for _, step := range steps {
		byID[step.ID] = step
	}
	if got.Delivery == nil || len(got.Delivery.MessageIDs) != 2 {
		t.Fatalf("delivery = %+v", got.Delivery)
	}
	delivered := map[string]bool{}
	for _, id := range got.Delivery.MessageIDs {
		delivered[id] = true
	}
	secondRoot := steps[0]
	if secondRoot.AgentID != run.Agents[1].ID {
		secondRoot = steps[1]
	}
	if !delivered[completedMessageID(t, messages, secondRoot.ID)] {
		t.Fatal("direct root reply was not delivered")
	}
	continuation := ""
	for _, step := range steps {
		if step.Kind == "continuation" && step.FromStepID == runStepID(t, steps, run.Agents[0].ID) {
			continuation = step.ID
		}
	}
	if continuation == "" || !delivered[completedMessageID(t, messages, continuation)] {
		t.Fatalf("delegating root's final reply was not delivered: %+v", got.Delivery)
	}
	for _, message := range messages {
		if message.Status == "completed" && byID[message.StepID].Kind == "handoff" && delivered[message.ID] {
			t.Fatal("a delegated member task was delivered as final")
		}
	}
}

func runStepID(t *testing.T, steps []w.Step, agentID string) string {
	t.Helper()
	for _, step := range steps {
		if step.Kind == "" && step.AgentID == agentID {
			return step.ID
		}
	}
	t.Fatalf("root step for %s is missing", agentID)
	return ""
}

func TestFreeRootChainDeliversOnlyItsLastContinuation(t *testing.T) {
	s := openDeliveryStore(t)
	run := seedFreeRun(t, s)
	enableExecutor(t, s, &run, "claude")
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		step, err := runningStepFor(ctx, s, run, request)
		if err != nil {
			return err
		}
		switch step.Kind {
		case "":
			return submitDeliveryHandoff(ctx, execute, "first", run.Agents[1].ID, "study the material")
		case "handoff":
			if step.AgentID == run.Agents[1].ID {
				return submitDeliveryHandoff(ctx, execute, "nested", run.Agents[2].ID, "collect supporting evidence")
			}
			return observe("text", map[string]any{"text": "worker result"})
		case "continuation":
			if step.AgentID != run.Agents[0].ID {
				return observe("text", map[string]any{"text": "delegated return"})
			}
			if step.FromStepID == runStepID(t, deliverySteps(t, s, run.ID), run.Agents[0].ID) {
				return submitDeliveryHandoff(ctx, execute, "second", run.Agents[2].ID, "verify the result")
			}
			return observe("text", map[string]any{"text": "root final answer"})
		}
		return fmt.Errorf("unexpected step kind %q", step.Kind)
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("run = %s: %s", got.Status, got.Error)
	}
	steps := deliverySteps(t, s, run.ID)
	messages := deliveryMessages(t, s, run)
	rootID := runStepID(t, steps, run.Agents[0].ID)
	firstReturn, secondReturn := "", ""
	for _, step := range steps {
		if step.Kind != "continuation" {
			continue
		}
		if step.FromStepID == rootID {
			firstReturn = step.ID
		}
		if firstReturn != "" && step.FromStepID == firstReturn {
			secondReturn = step.ID
		}
	}
	if firstReturn == "" || secondReturn == "" {
		t.Fatalf("root continuation chain missing: %+v", steps)
	}
	if got.Delivery == nil || !slices.Equal(got.Delivery.MessageIDs, []string{completedMessageID(t, messages, secondReturn)}) {
		t.Fatalf("delivery = %+v, want only the last continuation", got.Delivery)
	}
	if len(got.Delivery.ArtifactRefs) != 0 {
		t.Fatalf("artifact refs = %+v", got.Delivery.ArtifactRefs)
	}
}

func TestIntermediateStepsCannotPublishFormalArtifacts(t *testing.T) {
	t.Run("built-in researcher", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedRun(t, s)
		enableExecutor(t, s, &run, "claude")
		var rejection error
		runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
			payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Intermediate", "body": "not a final artifact", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			switch {
			case strings.Contains(request.System, "role=researcher"):
				_, rejection = execute(ctx, "publish_artifact", payload)
				return observe("text", map[string]any{"text": "研究结果"})
			case strings.Contains(request.System, "role=critic"):
				return observe("text", map[string]any{"text": "复核意见"})
			default:
				report, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Final", "body": "final research report", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
				_, err := execute(ctx, "publish_artifact", report)
				return err
			}
		})
		e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		cancel, wait := startEngine(t, &e)
		defer wait()
		defer cancel()
		got := awaitRun(t, s, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("run = %s: %s", got.Status, got.Error)
		}
		if rejection == nil || !strings.Contains(rejection.Error(), "intermediate steps cannot publish") {
			t.Fatalf("intermediate publication was not rejected: %v", rejection)
		}
		var artifacts []w.Artifact
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			artifacts, err = tx.Artifacts(run.ThreadID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 1 || artifacts[0].Type != "research_report" {
			t.Fatalf("artifacts = %+v", artifacts)
		}
		if got.Delivery == nil || !slices.Equal(got.Delivery.ArtifactRefs, []w.ArtifactRef{{ID: artifacts[0].ID, Version: artifacts[0].Version}}) {
			t.Fatalf("delivery artifact refs = %+v", got.Delivery)
		}
	})

	t.Run("free delegated task", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedFreeRun(t, s)
		enableExecutor(t, s, &run, "claude")
		var rejection error
		runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
			step, err := runningStepFor(ctx, s, run, request)
			if err != nil {
				return err
			}
			switch step.Kind {
			case "":
				return submitDeliveryHandoff(ctx, execute, "delegate", run.Agents[1].ID, "study the material")
			case "handoff":
				payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Delegated", "body": "not final", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
				_, rejection = execute(ctx, "publish_artifact", payload)
				return observe("text", map[string]any{"text": "委派结果"})
			default:
				return observe("text", map[string]any{"text": "根目标最终答复"})
			}
		})
		e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		cancel, wait := startEngine(t, &e)
		defer wait()
		defer cancel()
		got := awaitRun(t, s, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("run = %s: %s", got.Status, got.Error)
		}
		if rejection == nil || !strings.Contains(rejection.Error(), "intermediate steps cannot publish") {
			t.Fatalf("delegated publication was not rejected: %v", rejection)
		}
		var artifacts []w.Artifact
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			artifacts, err = tx.Artifacts(run.ThreadID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 0 {
			t.Fatalf("delegated task published a formal artifact: %+v", artifacts)
		}
	})

	t.Run("nested delegated continuation", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedFreeRun(t, s)
		enableExecutor(t, s, &run, "claude")
		var rejection error
		nestedToolOffered := false
		runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
			step, err := runningStepFor(ctx, s, run, request)
			if err != nil {
				return err
			}
			switch step.Kind {
			case "":
				return submitDeliveryHandoff(ctx, execute, "root-child", run.Agents[1].ID, "delegate the evidence collection")
			case "handoff":
				if step.AgentID == run.Agents[1].ID {
					return submitDeliveryHandoff(ctx, execute, "nested-child", run.Agents[2].ID, "collect supporting evidence")
				}
				return observe("text", map[string]any{"text": "最深层委派结果"})
			case "continuation":
				// Only the delegating child's return is nested; the root's
				// return is the platform's final delivery target.
				if step.AgentID == run.Agents[1].ID {
					nestedToolOffered = slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "publish_artifact" })
					payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Nested", "body": "not a final artifact", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
					_, rejection = execute(ctx, "publish_artifact", payload)
				}
				return observe("text", map[string]any{"text": "返回结果"})
			}
			return fmt.Errorf("unexpected step kind %q", step.Kind)
		})
		e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		cancel, wait := startEngine(t, &e)
		defer wait()
		defer cancel()
		got := awaitRun(t, s, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("run = %s: %s", got.Status, got.Error)
		}
		if rejection == nil || !strings.Contains(rejection.Error(), "intermediate steps cannot publish") {
			t.Fatalf("nested continuation publication was not rejected: %v", rejection)
		}
		if nestedToolOffered {
			t.Fatal("nested continuation was offered the publish_artifact tool")
		}
		var artifacts []w.Artifact
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			artifacts, err = tx.Artifacts(run.ThreadID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 0 {
			t.Fatalf("nested continuation published a formal artifact: %+v", artifacts)
		}
	})
}

func TestDeliveredArtifactUsesExactVersionAndAllowsEmptyReply(t *testing.T) {
	s := openDeliveryStore(t)
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "publish a versioned note"}})
	previous := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: w.NewID(), Type: "note", Title: "Previous", Body: "first version", Feedback: "unreviewed"}
	run.Input.ArtifactRefs = []w.ArtifactRef{{ID: previous.ID, Version: 1}}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.SaveArtifact(previous)
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, execute agent.Execute, _ agent.Observe) error {
		payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Next", "body": "second version", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
		_, err := execute(ctx, "publish_artifact", payload)
		return err
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("run = %s: %s", got.Status, got.Error)
	}
	if got.Delivery == nil || len(got.Delivery.MessageIDs) != 0 || !slices.Equal(got.Delivery.ArtifactRefs, []w.ArtifactRef{{ID: previous.ID, Version: 2}}) {
		t.Fatalf("delivery = %+v", got.Delivery)
	}
	var artifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		artifacts, err = tx.Artifacts(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || !w.IsDeliveredArtifact(got, artifacts[1]) || w.IsDeliveredArtifact(got, artifacts[0]) {
		t.Fatalf("exact version filter accepted the wrong version: %+v", artifacts)
	}
}

func TestBlankFinalReplyIsNotADelivery(t *testing.T) {
	emits := map[string]func(agent.Observe) error{
		"no output":       func(agent.Observe) error { return nil },
		"whitespace only": func(observe agent.Observe) error { return observe("text", map[string]any{"text": " \n\t　 "}) },
	}
	for name, emit := range emits {
		t.Run(name, func(t *testing.T) {
			s := openDeliveryStore(t)
			run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
				return emit(observe)
			})}, Web: testutil.Web{}}
			cancel, wait := startEngine(t, &e)
			defer wait()
			defer cancel()
			awaitRun(t, s, run.ID)
			assertSettledDeliveryFailure(t, s, run.ID)
		})
	}
}

func TestFinalReplyWithBlankPrefixStillCounts(t *testing.T) {
	s := openDeliveryStore(t)
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	answer := strings.Repeat(" ", 6000) + "可见结论"
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		return observe("text", map[string]any{"text": answer})
	})}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" || got.Delivery == nil || len(got.Delivery.MessageIDs) != 1 {
		t.Fatalf("run = %s: %s delivery=%+v", got.Status, got.Error, got.Delivery)
	}
	messages := deliveryMessages(t, s, run)
	steps := deliverySteps(t, s, run.ID)
	if got.Delivery.MessageIDs[0] != completedMessageID(t, messages, steps[0].ID) {
		t.Fatalf("delivery did not reference the final reply: %+v", got.Delivery)
	}
}

func TestDeliveryUsesCompletedMessageNotLatestAttempt(t *testing.T) {
	s := openDeliveryStore(t)
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	steps := deliverySteps(t, s, run.ID)
	step := steps[0]
	step.Status = "succeeded"
	completed := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: step.ID, AgentID: step.AgentID, Role: "assistant", Status: "completed", Content: "committed answer"}
	failed := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: step.ID, AgentID: step.AgentID, Role: "assistant", Status: "failed", Content: "abandoned attempt"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.SaveMessage(completed); err != nil {
			return err
		}
		return tx.SaveMessage(failed)
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s}
	e.finish(t.Context(), run, "succeeded", nil)
	var got w.Run
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		got, err = tx.Run(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.Status != "succeeded" || got.Delivery == nil || !slices.Equal(got.Delivery.MessageIDs, []string{completed.ID}) {
		t.Fatalf("delivery = %+v status=%s", got.Delivery, got.Status)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Type != "run_finished" {
				continue
			}
			var finished w.Run
			if err := json.Unmarshal(event.Data, &finished); err != nil {
				return err
			}
			if finished.Delivery == nil || !slices.Equal(finished.Delivery.MessageIDs, got.Delivery.MessageIDs) {
				return fmt.Errorf("run_finished event delivery = %+v", finished.Delivery)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFailureAndCancellationRecordNoDelivery(t *testing.T) {
	t.Run("cancel queued", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
		run.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
		if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
			t.Fatal(err)
		}
		e := Engine{Store: s}
		if _, err := e.Cancel(t.Context(), run.ID); err != nil {
			t.Fatal(err)
		}
		var got w.Run
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			got, err = tx.Run(run.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got.Status != "canceled" || got.Delivery == nil || len(got.Delivery.MessageIDs) != 0 || len(got.Delivery.ArtifactRefs) != 0 {
			t.Fatalf("canceled run = %+v", got)
		}
	})

	t.Run("failed after publication keeps only a draft", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "publish then fail"}})
		runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, execute agent.Execute, _ agent.Observe) error {
			payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Draft", "body": "never delivered", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
			if _, err := execute(ctx, "publish_artifact", payload); err != nil {
				return err
			}
			return errors.New("intentional failure after publication")
		})
		e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		cancel, wait := startEngine(t, &e)
		defer wait()
		defer cancel()
		got := awaitRun(t, s, run.ID)
		if got.Status != "failed" || got.Delivery == nil || len(got.Delivery.MessageIDs) != 0 || len(got.Delivery.ArtifactRefs) != 0 {
			t.Fatalf("failed run = %+v", got)
		}
		var artifacts []w.Artifact
		var attempts []w.Attempt
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			if artifacts, err = tx.Artifacts(run.ThreadID); err != nil {
				return err
			}
			attempts, err = tx.Attempts(run.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 0 {
			t.Fatalf("failed run published formal artifacts: %+v", artifacts)
		}
		if len(attempts) != 1 || attempts[0].DraftArtifact == nil {
			t.Fatalf("published draft was not preserved as attempt evidence: %+v", attempts)
		}
	})
}

func TestRestartRecoveryRecordsDeliveryAndLeavesLegacyHistory(t *testing.T) {
	s := openDeliveryStore(t)
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	steps := deliverySteps(t, s, run.ID)
	steps[0].Status = "succeeded"
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, AgentID: steps[0].AgentID, Role: "assistant", Status: "completed", Content: "recovered answer"}
	run.Status = "running"
	run.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		return tx.SaveMessage(message)
	}); err != nil {
		t.Fatal(err)
	}

	legacy := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "historical"}})
	legacySteps := deliverySteps(t, s, legacy.ID)
	legacySteps[0].Status = "succeeded"
	legacy.Status = "succeeded"
	legacyArtifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: legacy.ThreadID, RunID: legacy.ID, StepID: legacySteps[0].ID, Type: "note", Title: "Historical", Body: "legacy artifact", Feedback: "unreviewed"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(legacy); err != nil {
			return err
		}
		if err := tx.SaveStep(legacySteps[0]); err != nil {
			return err
		}
		if err := tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: legacy.ThreadID, RunID: legacy.ID, StepID: legacySteps[0].ID, AgentID: legacySteps[0].AgentID, Role: "assistant", Status: "completed", Content: "legacy answer"}); err != nil {
			return err
		}
		return tx.SaveArtifact(legacyArtifact)
	}); err != nil {
		t.Fatal(err)
	}

	recoveredLegacy := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "legacy running"}})
	recoveredLegacySteps := deliverySteps(t, s, recoveredLegacy.ID)
	recoveredLegacySteps[0].Status = "succeeded"
	recoveredLegacy.Status = "running"
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(recoveredLegacy); err != nil {
			return err
		}
		if err := tx.SaveStep(recoveredLegacySteps[0]); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: recoveredLegacy.ThreadID, RunID: recoveredLegacy.ID, StepID: recoveredLegacySteps[0].ID, AgentID: recoveredLegacySteps[0].AgentID, Role: "assistant", Status: "completed", Content: "legacy recovered answer"})
	}); err != nil {
		t.Fatal(err)
	}

	e := Engine{Store: s}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()

	var recovered w.Run
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		recovered, err = tx.Run(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "succeeded" || recovered.Delivery == nil || !slices.Equal(recovered.Delivery.MessageIDs, []string{message.ID}) {
		t.Fatalf("recovered run = %+v delivery=%+v", recovered, recovered.Delivery)
	}

	var historical w.Run
	var historicalArtifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		if historical, err = tx.Run(legacy.ID); err != nil {
			return err
		}
		historicalArtifacts, err = tx.Artifacts(legacy.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if historical.Delivery != nil {
		t.Fatalf("legacy history gained a guessed delivery: %+v", historical.Delivery)
	}
	if got := w.FinalArtifacts(historical, historicalArtifacts); len(got) != 1 || got[0].ID != legacyArtifact.ID {
		t.Fatalf("legacy artifacts were hidden: %+v", got)
	}

	var recoveredOld w.Run
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		recoveredOld, err = tx.Run(recoveredLegacy.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if recoveredOld.Status != "succeeded" || recoveredOld.Delivery != nil {
		t.Fatalf("legacy recovery gained a guessed delivery: %+v", recoveredOld)
	}
}

func TestDeliveryExcludesHistoricalIntermediateArtifacts(t *testing.T) {
	s := openDeliveryStore(t)
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	steps := deliverySteps(t, s, run.ID)
	intermediate := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, Type: "note", Title: "Intermediate note", Body: "kept for traceability", Feedback: "unreviewed"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveArtifact(intermediate) }); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		if !strings.Contains(request.System, "role=coordinator") {
			return observe("text", map[string]any{"text": "intermediate reply"})
		}
		payload, _ := json.Marshal(map[string]any{"type": "research_report", "title": "Final", "body": "final report", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
		_, err := execute(ctx, "publish_artifact", payload)
		return err
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("run = %s: %s", got.Status, got.Error)
	}
	var artifacts []w.Artifact
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		artifacts, err = tx.Artifacts(run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("historical artifact was deleted: %+v", artifacts)
	}
	final := artifacts[0]
	if final.ID == intermediate.ID {
		final = artifacts[1]
	}
	if got.Delivery == nil || !slices.Equal(got.Delivery.ArtifactRefs, []w.ArtifactRef{{ID: final.ID, Version: final.Version}}) {
		t.Fatalf("delivery = %+v", got.Delivery)
	}
	if w.IsDeliveredArtifact(got, intermediate) {
		t.Fatal("historical intermediate artifact was marked as final")
	}
	if delivered := w.FinalArtifacts(got, artifacts); len(delivered) != 1 || delivered[0].ID != final.ID {
		t.Fatalf("final artifact filter = %+v", delivered)
	}
}

func TestRootDraftAndLastContinuationPublication(t *testing.T) {
	t.Run("delegating root artifact stays a draft", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedFreeRun(t, s)
		enableExecutor(t, s, &run, "claude")
		runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
			step, err := runningStepFor(ctx, s, run, request)
			if err != nil {
				return err
			}
			switch step.Kind {
			case "":
				payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Root draft", "body": "not final", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
				if _, err := execute(ctx, "publish_artifact", payload); err != nil {
					return err
				}
				return submitDeliveryHandoff(ctx, execute, "delegate", run.Agents[1].ID, "study the material")
			case "handoff":
				return observe("text", map[string]any{"text": "委派结果"})
			default:
				return observe("text", map[string]any{"text": "根目标最终答复"})
			}
		})
		e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		cancel, wait := startEngine(t, &e)
		defer wait()
		defer cancel()
		got := awaitRun(t, s, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("run = %s: %s", got.Status, got.Error)
		}
		steps := deliverySteps(t, s, run.ID)
		messages := deliveryMessages(t, s, run)
		rootID := runStepID(t, steps, run.Agents[0].ID)
		var continuation string
		for _, step := range steps {
			if step.Kind == "continuation" && step.FromStepID == rootID {
				continuation = step.ID
			}
		}
		if got.Delivery == nil || !slices.Equal(got.Delivery.MessageIDs, []string{completedMessageID(t, messages, continuation)}) || len(got.Delivery.ArtifactRefs) != 0 {
			t.Fatalf("delivery = %+v", got.Delivery)
		}
		var artifacts []w.Artifact
		var attempts []w.Attempt
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			if artifacts, err = tx.Artifacts(run.ThreadID); err != nil {
				return err
			}
			attempts, err = tx.Attempts(run.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 0 {
			t.Fatalf("delegating root published a formal artifact: %+v", artifacts)
		}
		draftKept := false
		for _, attempt := range attempts {
			if attempt.StepID == rootID && attempt.DraftArtifact != nil {
				draftKept = true
			}
		}
		if !draftKept {
			t.Fatalf("delegating root draft was not preserved: %+v", attempts)
		}
	})

	t.Run("last continuation may publish", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedFreeRun(t, s)
		enableExecutor(t, s, &run, "claude")
		runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
			step, err := runningStepFor(ctx, s, run, request)
			if err != nil {
				return err
			}
			switch step.Kind {
			case "":
				return submitDeliveryHandoff(ctx, execute, "delegate", run.Agents[1].ID, "study the material")
			case "handoff":
				return observe("text", map[string]any{"text": "委派结果"})
			default:
				payload, _ := json.Marshal(map[string]any{"type": "note", "title": "Final note", "body": "optional structured delivery", "findings": []any{}, "source_ids": []string{}, "limitations": []string{}})
				if _, err := execute(ctx, "publish_artifact", payload); err != nil {
					return err
				}
				return observe("text", map[string]any{"text": "根目标最终答复"})
			}
		})
		e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		cancel, wait := startEngine(t, &e)
		defer wait()
		defer cancel()
		got := awaitRun(t, s, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("run = %s: %s", got.Status, got.Error)
		}
		if got.Delivery == nil || len(got.Delivery.ArtifactRefs) != 1 || got.Delivery.ArtifactRefs[0].Version != 1 {
			t.Fatalf("delivery = %+v", got.Delivery)
		}
		var artifacts []w.Artifact
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			artifacts, err = tx.Artifacts(run.ThreadID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(artifacts) != 1 || !w.IsDeliveredArtifact(got, artifacts[0]) {
			t.Fatalf("last continuation artifact was not delivered: %+v", artifacts)
		}
	})
}

func TestIncompleteFinalDeliveryFailsSettlement(t *testing.T) {
	t.Run("missing committed result", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
		steps := deliverySteps(t, s, run.ID)
		steps[0].Status = "succeeded"
		run.Status = "running"
		run.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
		if err := s.Write(t.Context(), func(tx *store.Tx) error {
			if err := tx.SaveRun(run); err != nil {
				return err
			}
			return tx.SaveStep(steps[0])
		}); err != nil {
			t.Fatal(err)
		}
		e := Engine{Store: s}
		e.finish(t.Context(), run, "succeeded", nil)
		assertSettledDeliveryFailure(t, s, run.ID)
	})

	t.Run("failed final tail", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
		steps := deliverySteps(t, s, run.ID)
		steps[0].Status = "failed"
		run.Status = "running"
		run.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
		message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, AgentID: steps[0].AgentID, Role: "assistant", Status: "completed", Content: "untrusted tail"}
		if err := s.Write(t.Context(), func(tx *store.Tx) error {
			if err := tx.SaveRun(run); err != nil {
				return err
			}
			if err := tx.SaveStep(steps[0]); err != nil {
				return err
			}
			return tx.SaveMessage(message)
		}); err != nil {
			t.Fatal(err)
		}
		e := Engine{Store: s}
		e.finish(t.Context(), run, "succeeded", nil)
		assertSettledDeliveryFailure(t, s, run.ID)
	})

	t.Run("every root target needs a result", func(t *testing.T) {
		s := openDeliveryStore(t)
		run := seedCustomRun(t, s, []customStepSpec{
			{Agent: 0, Role: "responder", Instruction: "first answer"},
			{Agent: 1, Role: "responder", Instruction: "second answer"},
		})
		steps := deliverySteps(t, s, run.ID)
		run.Status = "running"
		run.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
		message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, AgentID: steps[0].AgentID, Role: "assistant", Status: "completed", Content: "first answer"}
		artifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, Type: "note", Title: "First answer", Body: "first answer", Feedback: "unreviewed"}
		if err := s.Write(t.Context(), func(tx *store.Tx) error {
			if err := tx.SaveRun(run); err != nil {
				return err
			}
			for _, step := range steps {
				step.Status = "succeeded"
				if err := tx.SaveStep(step); err != nil {
					return err
				}
			}
			if err := tx.SaveMessage(message); err != nil {
				return err
			}
			return tx.SaveArtifact(artifact)
		}); err != nil {
			t.Fatal(err)
		}
		e := Engine{Store: s}
		e.finish(t.Context(), run, "succeeded", nil)
		assertSettledDeliveryFailure(t, s, run.ID)
		var artifacts []w.Artifact
		var got w.Run
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			if got, err = tx.Run(run.ID); err != nil {
				return err
			}
			artifacts, err = tx.Artifacts(run.ThreadID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if delivered := w.FinalArtifacts(got, artifacts); len(delivered) != 0 {
			t.Fatalf("incomplete run exposed an artifact: %+v", delivered)
		}
	})
}

func assertSettledDeliveryFailure(t *testing.T, s *store.Store, runID string) {
	t.Helper()
	var got w.Run
	var events []w.Event
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		if got, err = tx.Run(runID); err != nil {
			return err
		}
		events, err = tx.Events(runID, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.FailureKind != failureArtifactContract || !strings.Contains(got.Error, "final delivery is incomplete") {
		t.Fatalf("incomplete delivery did not fail the run: %+v", got)
	}
	if got.Delivery == nil || len(got.Delivery.MessageIDs) != 0 || len(got.Delivery.ArtifactRefs) != 0 {
		t.Fatalf("failed delivery = %+v", got.Delivery)
	}
	finished := false
	for _, event := range events {
		if event.Type != "run_finished" {
			continue
		}
		var settled w.Run
		if err := json.Unmarshal(event.Data, &settled); err != nil {
			t.Fatal(err)
		}
		if settled.Status == "failed" && strings.Contains(settled.Error, "final delivery is incomplete") {
			finished = true
		}
	}
	if !finished {
		t.Fatalf("missing failed run_finished event: %+v", events)
	}
}

func TestFreeRunWithoutToolsDoesNotRequireArtifact(t *testing.T) {
	s := openDeliveryStore(t)
	ws := w.Workspace{ID: w.NewID(), Name: "short", Revision: 1}
	member := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "短答成员", Handle: "short", Model: "test", Tools: nil, Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{member}, Input: w.RunInput{Workflow: "free", Message: "直接回答", Targets: []string{member.ID}, ResearchScope: "given_sources"}}
	steps, err := Plan(run.ID, run.Input, run.Agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "short"); err != nil {
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
	e := Engine{Store: s, Model: testutil.Model{}, AllowedModels: []string{"test"}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" || got.Delivery == nil || len(got.Delivery.MessageIDs) != 1 || len(got.Delivery.ArtifactRefs) != 0 {
		t.Fatalf("short answer without tools = %+v delivery=%+v", got, got.Delivery)
	}
}
