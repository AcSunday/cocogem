package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func seedMultiMemberRun(t *testing.T, db *store.Store, root string, targets []string, taskIntake bool) (w.Run, []w.Agent) {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "a2a-project", Root: root, Revision: 1, ExecutionMode: w.ExecutionWorkspaceAutonomous}
	members := []w.Agent{
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "协调者", Handle: "coord", Model: "default", Executor: "claude", Tools: w.StandardToolCatalog(), Revision: 1},
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "执行者", Handle: "exec", Model: "default", Executor: "claude", Tools: w.StandardToolCatalog(), Revision: 1},
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "质疑者", Handle: "rev", Model: "default", Executor: "claude", Tools: w.StandardToolCatalog(), Revision: 1},
	}
	agentIDs := []string{members[0].ID, members[1].ID, members[2].ID}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: agentIDs, CoordinatorID: members[0].ID, Revision: 1, UpdatedAt: w.Now()}
	in := w.RunInput{
		Message:       "修复边界空指针问题并验证",
		ResearchScope: "given_sources",
		Targets:       targets,
	}
	if len(in.Targets) == 0 {
		in.Targets = []string{members[0].ID}
	}
	runID := w.NewID()
	steps, err := Plan(runID, in, members)
	if err != nil {
		t.Fatal(err)
	}
	userMessage := w.Message{
		ID: w.NewID(), ThreadID: th.ID, RunID: runID, Role: "user",
		Content: in.Message, Status: "completed", CreatedAt: w.Now(),
	}
	run := w.Run{
		ID:            runID,
		WorkspaceID:   ws.ID,
		Workspace:     ws,
		ThreadID:      th.ID,
		CoordinatorID: members[0].ID,
		Status:        "queued",
		CreatedAt:     w.Now(),
		Agents:        members,
		Input:         in,
		Routing:       w.Routing{Decision: "discuss", Targets: slices.Clone(in.Targets)},
		TaskIntake:    taskIntake,
		Delivery:      &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}},
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, m := range members {
			if err := tx.SaveAgent(m); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "a2a-seed"); err != nil {
			return err
		}
		if err := tx.SaveMessage(userMessage); err != nil {
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
	return run, members
}

// TestSessionResumePreservesReviewerHandoffAcrossRounds is the A26 regression:
// a relay reviewer that requests one more repair round hands its exact critique
// back to the author, and the author's resumed native session receives the
// incremental critique instead of a replay of the whole conversation.
func TestSessionResumePreservesReviewerHandoffAcrossRounds(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bug.go"), []byte(`package bug`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	run, members := seedMultiMemberRun(t, db, root, nil, false)
	attachSessionTask(t, db, &run)
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionWorkspaceAutonomous); err != nil {
		t.Fatal(err)
	}

	const critiqueToken = "REVIEWER_CHALLENGE_NIL_SLICE_9921"
	const executorSessionID = "exec-session-1111"
	const reviewerSessionID = "rev-session-2222"
	var executorRounds atomic.Int32
	var resumedPrompt string
	var mu sync.Mutex

	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		prompt := request.Messages[0].Content[0].Text
		if _, err := execute(ctx, "read_file", json.RawMessage(`{"path":"bug.go"}`)); err != nil {
			return err
		}
		switch {
		case strings.Contains(request.System, members[0].DisplayName):
			payload, _ := json.Marshal(map[string]any{
				"request_id": "coord-to-exec", "target_agent_id": members[1].ID, "role": "engineer", "mode": "relay", "task": "修复空指针并交复核",
			})
			if _, err := execute(ctx, "handoff_task", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "已派单给执行者"})
		case strings.Contains(request.System, members[1].DisplayName):
			round := executorRounds.Add(1)
			if err := observe("native_session", map[string]any{"session_id": executorSessionID, "mode": request.SessionMode}); err != nil {
				return err
			}
			if round == 1 {
				if request.SessionMode != "fresh" {
					return fmt.Errorf("round 1 executor session mode = %q, want fresh", request.SessionMode)
				}
				payload, _ := json.Marshal(map[string]any{
					"request_id": "exec-to-rev", "target_agent_id": members[2].ID, "role": "reviewer", "mode": "relay", "task": "请复核空切片边界",
				})
				if _, err := execute(ctx, "handoff_task", payload); err != nil {
					return err
				}
				return observe("text", map[string]any{"text": "第一轮修改完成"})
			}
			if request.SessionMode != "resume" || request.SessionID != executorSessionID {
				return fmt.Errorf("round 2 executor did not resume native session: mode=%q id=%q", request.SessionMode, request.SessionID)
			}
			mu.Lock()
			resumedPrompt = prompt
			mu.Unlock()
			if !strings.Contains(prompt, "session_delta/v1") || !strings.Contains(prompt, "Exact upstream handoff") || !strings.Contains(prompt, critiqueToken) {
				return fmt.Errorf("resumed executor lost the reviewer handoff: %s", prompt)
			}
			return observe("text", map[string]any{"text": "已针对质疑者的空切片边界问题完成修复"})

		case strings.Contains(request.System, members[2].DisplayName):
			if err := observe("native_session", map[string]any{"session_id": reviewerSessionID, "mode": request.SessionMode}); err != nil {
				return err
			}
			if _, err := execute(ctx, "submit_review_verdict", json.RawMessage(`{"verdict":"changes_requested","summary":"需要补充空切片检查"}`)); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{
				"request_id": "rev-to-exec", "target_agent_id": members[1].ID, "role": "engineer", "mode": "relay", "task": "请补充空切片保护",
			})
			if _, err := execute(ctx, "handoff_task", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "发现遗漏：" + critiqueToken})
		}
		return errors.New("unexpected member")
	})

	checker := sessionChecker{
		adapter:    agent.AdapterInfo{ID: "claude", Supported: true, Installed: true, Version: "2.1.318", Identity: "claude-v1", Adapter: "claude-stream-json", Capabilities: []string{"stream_json", "managed_session", "session_resume_ack=supported"}},
		connection: agent.ConnectionInfo{ResolvedModel: "claude-actual", Identity: "claude-provider"},
	}
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checker, NativeSessionRoot: filepath.Join(t.TempDir(), "sessions"), Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()

	got := awaitRun(t, db, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("run failed: status=%s kind=%s err=%s", got.Status, got.FailureKind, got.Error)
	}
	if resumedPrompt == "" || executorRounds.Load() != 2 {
		t.Fatalf("round 2 resumed executor did not verify the reviewer handoff: rounds=%d", executorRounds.Load())
	}
}

func TestSingleTurnPromoteAndRelayA2AConvergesInOneRound(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	run, members := seedMultiMemberRun(t, db, root, nil, true)
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionWorkspaceAutonomous); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if err := service.EnsureRunAuthorization(tx, &current, "relay-intake"); err != nil {
			return err
		}
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}

	var coordinatorCalls, engineerCalls, reviewerCalls atomic.Int32
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		prompt := request.Messages[0].Content[0].Text
		switch {
		case strings.Contains(request.System, members[0].DisplayName):
			coordinatorCalls.Add(1)
			// Promote AND relay in the SAME single CLI invocation!
			promotePayload, _ := json.Marshal(map[string]any{
				"title":          "修复空指针问题",
				"goal":           "修复空指针并由质疑者复核",
				"evidence_quote": "修复边界空指针问题并验证",
			})
			if _, err := execute(ctx, "promote_to_task", promotePayload); err != nil {
				return fmt.Errorf("promote_to_task failed: %w", err)
			}
			relayPayload, _ := json.Marshal(map[string]any{
				"request_id":      "coord-to-exec",
				"target_agent_id": members[1].ID,
				"role":            "engineer",
				"mode":            "relay",
				"task":            "修复空指针问题，完成后 relay 给质疑者复核",
			})
			if _, err := execute(ctx, "handoff_task", relayPayload); err != nil {
				return fmt.Errorf("in-place relay handoff after promote_to_task failed: %w", err)
			}
			return observe("text", map[string]any{"text": "已立项并交由执行者处理。"})

		case strings.Contains(request.System, members[1].DisplayName):
			engineerCalls.Add(1)
			relayToReviewer, _ := json.Marshal(map[string]any{
				"request_id":      "exec-to-rev",
				"target_agent_id": members[2].ID,
				"role":            "reviewer",
				"mode":            "relay",
				"task":            "请复核修复结果，若无异议请直接收敛结束",
			})
			if _, err := execute(ctx, "handoff_task", relayToReviewer); err != nil {
				return fmt.Errorf("engineer relay to reviewer failed: %w", err)
			}
			return observe("text", map[string]any{"text": "EXECUTOR_FIXED_NIL_GUARD_42"})

		case strings.Contains(request.System, members[2].DisplayName):
			reviewerCalls.Add(1)
			if !strings.Contains(prompt, "EXECUTOR_FIXED_NIL_GUARD_42") {
				return fmt.Errorf("reviewer did not receive executor's relay handoff: %s", prompt)
			}
			// Reviewer has NO objections: finishes directly without calling handoff_task!
			return observe("text", map[string]any{"text": "质疑者复核通过，无其他意见，直接交付。"})
		}
		return errors.New("unexpected member")
	})

	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()

	got := awaitRun(t, db, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("relay run status=%s kind=%s err=%s", got.Status, got.FailureKind, got.Error)
	}
	if coordinatorCalls.Load() != 1 || engineerCalls.Load() != 1 || reviewerCalls.Load() != 1 {
		t.Fatalf("unexpected CLI call counts: coordinator=%d engineer=%d reviewer=%d (want 1, 1, 1)", coordinatorCalls.Load(), engineerCalls.Load(), reviewerCalls.Load())
	}
	if got.Method != "delegation" || got.TaskID == "" {
		t.Fatalf("expected task-promoted delegation method, got method=%q task=%q", got.Method, got.TaskID)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		if len(steps) != 3 {
			t.Fatalf("expected exactly 3 steps (intake -> engineer relay -> reviewer relay), got %d: %+v", len(steps), steps)
		}
		if got.Delivery == nil || len(got.Delivery.MessageIDs) != 1 {
			t.Fatalf("unexpected delivery: %+v", got.Delivery)
		}
		finalMsg, err := tx.MessagePreview(got.Delivery.MessageIDs[0], 4096)
		if err != nil {
			return err
		}
		if finalMsg.AgentID != members[2].ID || !strings.Contains(finalMsg.Content, "质疑者复核通过") {
			t.Fatalf("delivery did not land on terminal relay reviewer: %+v", finalMsg)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRelayA2APingPongWhenReviewerRequestsChanges(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	run, members := seedMultiMemberRun(t, db, root, nil, false)
	attachSessionTask(t, db, &run)
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionWorkspaceAutonomous); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if err := service.EnsureRunAuthorization(tx, &current, "relay-pingpong"); err != nil {
			return err
		}
		manifest, err := service.PrepareContextInTx(tx, current.TaskID, current.TaskRevision, project.ContextHostInput(current))
		if err != nil {
			return err
		}
		current.ContextRef = manifest.ID
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}

	var execTurns, revTurns atomic.Int32
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		prompt := request.Messages[0].Content[0].Text
		switch {
		case strings.Contains(request.System, members[0].DisplayName):
			payload, _ := json.Marshal(map[string]any{
				"request_id": "c1", "target_agent_id": members[1].ID, "role": "engineer", "mode": "relay", "task": "修复问题并交质疑者复核",
			})
			if _, err := execute(ctx, "handoff_task", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "协调者派单给执行者"})

		case strings.Contains(request.System, members[1].DisplayName):
			turn := execTurns.Add(1)
			_ = observe("native_session", map[string]any{"session_id": "exec-sess", "mode": request.SessionMode})
			if turn == 2 && !strings.Contains(prompt, "EDGE_CASE_NEGATIVE_INDEX") {
				return fmt.Errorf("resumed executor missed reviewer objection on turn 2: %s", prompt)
			}
			payload, _ := json.Marshal(map[string]any{
				"request_id": fmt.Sprintf("e-%d", turn), "target_agent_id": members[2].ID, "role": "reviewer", "mode": "relay", "task": "请复核",
			})
			if _, err := execute(ctx, "handoff_task", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": fmt.Sprintf("EXEC_ROUND_%d_DONE", turn)})

		case strings.Contains(request.System, members[2].DisplayName):
			turn := revTurns.Add(1)
			_ = observe("native_session", map[string]any{"session_id": "rev-sess", "mode": request.SessionMode})
			if turn == 1 {
				payload, _ := json.Marshal(map[string]any{
					"request_id": "r-1", "target_agent_id": members[1].ID, "role": "engineer", "mode": "relay", "task": "补充负数下标保护",
				})
				if _, err := execute(ctx, "handoff_task", payload); err != nil {
					return err
				}
				return observe("text", map[string]any{"text": "发现问题：EDGE_CASE_NEGATIVE_INDEX"})
			}
			if !strings.Contains(prompt, "EXEC_ROUND_2_DONE") {
				return fmt.Errorf("resumed reviewer missed executor round 2 reply: %s", prompt)
			}
			return observe("text", map[string]any{"text": "第二轮复核无异议，通过！"})
		}
		return errors.New("unexpected agent")
	})

	checker := sessionChecker{
		adapter:    agent.AdapterInfo{ID: "claude", Supported: true, Installed: true, Version: "2.1.318", Identity: "claude-v1", Adapter: "claude-stream-json", Capabilities: []string{"stream_json", "managed_session", "session_resume_ack=supported"}},
		connection: agent.ConnectionInfo{ResolvedModel: "claude-actual", Identity: "claude-provider"},
	}
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checker, NativeSessionRoot: filepath.Join(t.TempDir(), "sessions"), Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()

	got := awaitRun(t, db, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("ping-pong relay failed: status=%s err=%s", got.Status, got.Error)
	}
	if execTurns.Load() != 2 || revTurns.Load() != 2 {
		t.Fatalf("execTurns=%d revTurns=%d, want 2 and 2", execTurns.Load(), revTurns.Load())
	}
}

// TestReviewerApprovalConvergesWithoutAnotherRound is the A08 regression: an
// approving reviewer ends the chain immediately, and a reviewer that keeps asking
// for changes is stopped after the two-round rework limit.
func TestReviewerApprovalConvergesWithoutAnotherRound(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte(`package app`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run, members := seedMultiMemberRun(t, db, root, nil, false)
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionWorkspaceAutonomous); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if err := service.EnsureRunAuthorization(tx, &current, "approve-once"); err != nil {
			return err
		}
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}

	var executorRounds, reviewerRounds atomic.Int32
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		if _, err := execute(ctx, "read_file", json.RawMessage(`{"path":"app.go"}`)); err != nil {
			return err
		}
		switch {
		case strings.Contains(request.System, members[0].DisplayName):
			payload, _ := json.Marshal(map[string]any{
				"request_id": "coord-to-exec", "target_agent_id": members[1].ID, "role": "engineer", "mode": "relay", "task": "检查并修复",
			})
			if _, err := execute(ctx, "handoff_task", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "已派单"})
		case strings.Contains(request.System, members[1].DisplayName):
			executorRounds.Add(1)
			payload, _ := json.Marshal(map[string]any{
				"request_id": "exec-to-rev", "target_agent_id": members[2].ID, "role": "reviewer", "mode": "relay", "task": "请复核",
			})
			if _, err := execute(ctx, "handoff_task", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "修改完成"})
		case strings.Contains(request.System, members[2].DisplayName):
			reviewerRounds.Add(1)
			if _, err := execute(ctx, "submit_review_verdict", json.RawMessage(`{"verdict":"approved","summary":"验证无误，无需返工"}`)); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "复核通过，收敛交付"})
		}
		return errors.New("unexpected member")
	})
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, db, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("approved relay run failed: status=%s err=%s", got.Status, got.Error)
	}
	if executorRounds.Load() != 1 || reviewerRounds.Load() != 1 {
		t.Fatalf("an approval must end the chain immediately: executor=%d reviewer=%d", executorRounds.Load(), reviewerRounds.Load())
	}
	if got.Delivery == nil || len(got.Delivery.MessageIDs) != 1 {
		t.Fatalf("expected exactly one delivered reply: %+v", got.Delivery)
	}
}

// TestReworkLimitStopsTheThirdRelayRound is the A08/A16 convergence valve: after
// two changes_requested rounds on the same issue chain the host refuses a third
// relay rework and the reviewer records the remaining disagreement instead.
func TestReworkLimitStopsTheThirdRelayRound(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte(`package app`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run, members := seedMultiMemberRun(t, db, root, nil, false)
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionWorkspaceAutonomous); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if err := service.EnsureRunAuthorization(tx, &current, "rework-limit"); err != nil {
			return err
		}
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}

	var executorRounds, reviewerRounds atomic.Int32
	var thirdRejected string
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		if _, err := execute(ctx, "read_file", json.RawMessage(`{"path":"app.go"}`)); err != nil {
			return err
		}
		switch {
		case strings.Contains(request.System, members[0].DisplayName):
			payload, _ := json.Marshal(map[string]any{
				"request_id": "coord-to-exec", "target_agent_id": members[1].ID, "role": "engineer", "mode": "relay", "task": "修复问题",
			})
			if _, err := execute(ctx, "handoff_task", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "已派单"})
		case strings.Contains(request.System, members[1].DisplayName):
			round := executorRounds.Add(1)
			payload, _ := json.Marshal(map[string]any{
				"request_id": fmt.Sprintf("exec-%d", round), "target_agent_id": members[2].ID, "role": "reviewer", "mode": "relay", "task": "请复核",
			})
			if _, err := execute(ctx, "handoff_task", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": fmt.Sprintf("第 %d 轮修复完成", round)})
		case strings.Contains(request.System, members[2].DisplayName):
			round := reviewerRounds.Add(1)
			if _, err := execute(ctx, "submit_review_verdict", json.RawMessage(`{"verdict":"changes_requested","summary":"仍需返工"}`)); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{
				"request_id": fmt.Sprintf("rev-%d", round), "target_agent_id": members[1].ID, "role": "engineer", "mode": "relay", "task": "请再修一轮",
			})
			result, err := execute(ctx, "handoff_task", payload)
			if err != nil {
				thirdRejected = err.Error()
				return observe("text", map[string]any{"text": "返工已达上限，按现状交付并保留分歧"})
			}
			_ = result
			return observe("text", map[string]any{"text": "提出返工意见"})
		}
		return errors.New("unexpected member")
	})
	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()
	got := awaitRun(t, db, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("rework-limit run failed: status=%s err=%s", got.Status, got.Error)
	}
	if thirdRejected == "" {
		t.Fatal("the third relay rework round was not rejected")
	}
	if !strings.Contains(thirdRejected, "two rework rounds") {
		t.Fatalf("unexpected rejection reason: %q", thirdRejected)
	}
	if reviewerRounds.Load() != 2 {
		_ = db.Read(t.Context(), func(tx *store.Tx) error {
			steps, _ := tx.Steps(run.ID)
			for _, step := range steps {
				t.Logf("step kind=%s role=%s agent=%s from=%s verdict=%s status=%s err=%s", step.Kind, step.Role, liveSafeError(step.AgentID), liveSafeError(step.FromAgentID), step.ReviewVerdict, step.Status, liveSafeError(step.Error))
			}
			return nil
		})
		t.Fatalf("expected exactly two reviewer rounds before the rework limit, got %d", reviewerRounds.Load())
	}
}
