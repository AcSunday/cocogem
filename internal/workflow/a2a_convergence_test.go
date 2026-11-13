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

func seedMultiMemberRun(t *testing.T, db *store.Store, root string, workflow string, assignments map[string]string, targets []string, taskIntake bool) (w.Run, []w.Agent) {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "a2a-project", Root: root, Revision: 1, ExecutionMode: w.ExecutionProjectAutonomous}
	members := []w.Agent{
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "协调者", Handle: "coord", Model: "default", Executor: "claude", Tools: w.StandardToolCatalog(), Revision: 1},
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "执行者", Handle: "exec", Model: "default", Executor: "claude", Tools: w.StandardToolCatalog(), Revision: 1},
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "质疑者", Handle: "rev", Model: "default", Executor: "claude", Tools: w.StandardToolCatalog(), Revision: 1},
	}
	agentIDs := []string{members[0].ID, members[1].ID, members[2].ID}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: agentIDs, CoordinatorID: members[0].ID, Revision: 1, UpdatedAt: w.Now()}
	in := w.RunInput{
		Message:       "修复边界空指针问题并验证",
		Workflow:      workflow,
		ResearchScope: "given_sources",
		Assignments:   assignments,
		Targets:       targets,
	}
	if workflow == "" || workflow == "free" {
		in.Workflow = "free"
		if len(in.Targets) == 0 {
			in.Targets = []string{members[0].ID}
		}
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
		Routing:       w.Routing{Decision: "discuss", Workflow: in.Workflow, Targets: slices.Clone(in.Targets), Assignments: assignments},
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

	run, _ := seedMultiMemberRun(t, db, root, "free", nil, nil, false)
	// Assign engineer and reviewer explicitly on engineering workflow.
	run.Input.Workflow = "engineering"
	run.Input.Assignments = map[string]string{
		"engineer": run.Agents[1].ID,
		"reviewer": run.Agents[2].ID,
	}
	steps, err := Plan(run.ID, run.Input, run.Agents)
	if err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionProjectAutonomous); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		existing, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, s := range existing {
			if err := tx.DeletePendingStep(s.ID); err != nil {
				return err
			}
		}
		for _, s := range steps {
			if err := tx.SaveStep(s); err != nil {
				return err
			}
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	createdTask, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, OwnerMemberID: run.Agents[0].ID,
		Goal: "修复边界空指针问题并验证", AllowedActions: []string{"read", "write", "execute", "collaborate", "publish_artifact"},
		SourceMessageID: "session-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.TaskID = createdTask.Task.ID
		current.TaskRevision = createdTask.Task.CurrentRevision
		current.BudgetAccountID = createdTask.Budget.ID
		if err := service.EnsureRunAuthorization(tx, &current, "autonomous-eng"); err != nil {
			return err
		}
		manifest, err := service.PrepareContextInTx(tx, current.TaskID, current.TaskRevision, project.ContextHostInput(current))
		if err != nil {
			return err
		}
		current.ContextRef = manifest.ID
		current.InputDigest = project.ContextInputDigest(createdTask, manifest, "autonomous-eng", current.Input)
		run = current
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}

	const critiqueToken = "REVIEWER_CHALLENGE_NIL_SLICE_9921"
	const engineerSessionID = "eng-session-1111"
	const reviewerSessionID = "rev-session-2222"
	var engineerRound atomic.Int32
	var resumedPrompt string
	var readContextSucceeded atomic.Bool
	var mu sync.Mutex

	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		prompt := request.Messages[0].Content[0].Text
		readOut, err := execute(ctx, "read_file", json.RawMessage(`{"path":"bug.go"}`))
		if err != nil {
			return err
		}
		switch {
		case strings.Contains(request.System, "Current role=engineer "):
			round := engineerRound.Add(1)
			if err := observe("native_session", map[string]any{"session_id": engineerSessionID, "mode": request.SessionMode}); err != nil {
				return err
			}
			if round == 1 {
				if request.SessionMode != "fresh" {
					return fmt.Errorf("round 1 engineer session mode = %q, want fresh", request.SessionMode)
				}
				return observe("text", map[string]any{"text": "第一轮修改完成"})
			}
			if request.SessionMode != "resume" || request.SessionID != engineerSessionID {
				return fmt.Errorf("round 2 engineer did not resume native session: mode=%q id=%q", request.SessionMode, request.SessionID)
			}
			mu.Lock()
			resumedPrompt = prompt
			mu.Unlock()
			if !strings.Contains(prompt, "session_delta/v1") || !strings.Contains(prompt, "Exact upstream handoff") || !strings.Contains(prompt, critiqueToken) {
				return fmt.Errorf("resumed engineer lost reviewer handoff: %s", prompt)
			}
			// Also verify read_context works for the reviewer's message ID embedded in handoff!
			var reviewerMsgID string
			_ = db.Read(ctx, func(tx *store.Tx) error {
				msgs, err := tx.MessagesMetadata(run.ThreadID)
				if err != nil {
					return err
				}
				for _, m := range msgs {
					if m.AgentID == run.Agents[2].ID && m.Status == "completed" {
						reviewerMsgID = m.ID
					}
				}
				return nil
			})
			if reviewerMsgID == "" {
				return errors.New("reviewer message not found")
			}
			page, err := execute(ctx, "read_context", json.RawMessage(fmt.Sprintf(`{"kind":"message","id":%q}`, reviewerMsgID)))
			if err != nil || !strings.Contains(page, critiqueToken) {
				return fmt.Errorf("read_context on resumed engineer failed: err=%v page=%s", err, page)
			}
			readContextSucceeded.Store(true)
			return observe("text", map[string]any{"text": "已针对质疑者的空切片边界问题完成修复"})

		case strings.Contains(request.System, "Current role=reviewer "):
			if err := observe("native_session", map[string]any{"session_id": reviewerSessionID, "mode": request.SessionMode}); err != nil {
				return err
			}
			if _, err := execute(ctx, "submit_review_verdict", json.RawMessage(`{"verdict":"changes_requested","summary":"需要补充空切片检查"}`)); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "发现遗漏：" + critiqueToken})

		case strings.Contains(request.System, "Current role=final_reviewer "):
			var captured struct {
				SourceID string `json:"source_id"`
			}
			if err := json.Unmarshal([]byte(readOut), &captured); err != nil || captured.SourceID == "" {
				return fmt.Errorf("read_file source_id missing: %v %s", err, readOut)
			}
			srcID := captured.SourceID
			payload, _ := json.Marshal(map[string]any{
				"type":        "engineering_report",
				"title":       "修复复核报告",
				"body":        "空切片边界已验证修复（只读示例）。",
				"source_ids":  []string{srcID},
				"limitations": []string{"仅静态读取验证"},
				"findings": []any{map[string]any{
					"title":      "空切片保护已补齐",
					"detail":     "第二轮已回应并修复",
					"status":     "confirmed",
					"severity":   "medium",
					"source_ids": []string{srcID},
				}},
			})
			if _, err := execute(ctx, "publish_artifact", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "终审通过"})
		}
		return errors.New("unexpected step role")
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
	if !readContextSucceeded.Load() || resumedPrompt == "" {
		t.Fatal("round 2 resumed engineer did not verify reviewer handoff")
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

	run, members := seedMultiMemberRun(t, db, root, "free", nil, nil, true)
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionProjectAutonomous); err != nil {
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

	run, members := seedMultiMemberRun(t, db, root, "free", nil, nil, false)
	attachSessionTask(t, db, &run)
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionProjectAutonomous); err != nil {
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

func TestBuiltInEngineeringEarlyExitAndDynamicThirdRound(t *testing.T) {
	t.Run("early exit when reviewer approves round 1", func(t *testing.T) {
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

		run2, m2 := seedMultiMemberRun(t, db, root, "free", nil, nil, false)
		in := w.RunInput{
			Message:       "检查并确认 app.go",
			Workflow:      "engineering",
			ResearchScope: "given_sources",
			Assignments:   map[string]string{"engineer": m2[1].ID, "reviewer": m2[2].ID},
		}
		planned, err := Plan(run2.ID, in, m2)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			old, _ := tx.Steps(run2.ID)
			for _, s := range old {
				_ = tx.DeletePendingStep(s.ID)
			}
			run2.Input = in
			if err := tx.SaveRun(run2); err != nil {
				return err
			}
			for _, s := range planned {
				if err := tx.SaveStep(s); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		var engCount, revCount atomic.Int32
		runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
			if _, err := execute(ctx, "read_file", json.RawMessage(`{"path":"app.go"}`)); err != nil {
				return err
			}
			if strings.Contains(request.System, "Current role=engineer ") {
				engCount.Add(1)
				return observe("text", map[string]any{"text": "第一轮检查与修复已完成"})
			}
			revCount.Add(1)
			if _, err := execute(ctx, "submit_review_verdict", json.RawMessage(`{"verdict":"approved","summary":"验证无误，无需第二轮返工"}`)); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "质疑者一审直接通过，收敛交付"})
		})
		e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		cancel, wait := startEngine(t, &e)
		defer wait()
		defer cancel()

		got := awaitRun(t, db, run2.ID)
		if got.Status != "succeeded" {
			t.Fatalf("converged engineering run failed: status=%s err=%s", got.Status, got.Error)
		}
		if engCount.Load() != 1 || revCount.Load() != 1 {
			t.Fatalf("expected 1 engineer + 1 reviewer call on early approval, got eng=%d rev=%d", engCount.Load(), revCount.Load())
		}
		if err := db.Read(t.Context(), func(tx *store.Tx) error {
			steps, err := tx.Steps(run2.ID)
			if err != nil {
				return err
			}
			if !steps[2].Converged || steps[2].Status != "skipped" || !steps[3].Converged || steps[3].Status != "skipped" {
				t.Fatalf("round 2 steps were not marked converged+skipped: %+v", steps)
			}
			if got.Delivery == nil || len(got.Delivery.MessageIDs) != 1 {
				t.Fatalf("expected 1 delivered reviewer message, got %+v", got.Delivery)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dynamic 3rd round when final_reviewer requests changes", func(t *testing.T) {
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

		run, m := seedMultiMemberRun(t, db, root, "free", nil, nil, false)
		in := w.RunInput{
			Message:       "检查 app.go",
			Workflow:      "engineering",
			ResearchScope: "given_sources",
			Assignments:   map[string]string{"engineer": m[1].ID, "reviewer": m[2].ID},
		}
		planned, err := Plan(run.ID, in, m)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			old, _ := tx.Steps(run.ID)
			for _, s := range old {
				_ = tx.DeletePendingStep(s.ID)
			}
			run.Input = in
			if err := tx.SaveRun(run); err != nil {
				return err
			}
			for _, s := range planned {
				if err := tx.SaveStep(s); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		var engRounds, finalRevCalls atomic.Int32
		runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
			readOut, err := execute(ctx, "read_file", json.RawMessage(`{"path":"app.go"}`))
			if err != nil {
				return err
			}
			switch {
			case strings.Contains(request.System, "Current role=engineer "):
				r := engRounds.Add(1)
				return observe("text", map[string]any{"text": fmt.Sprintf("工程轮次 %d 完成", r)})
			case strings.Contains(request.System, "Current role=reviewer "):
				return observe("text", map[string]any{"text": "第一轮提出修改建议"})
			case strings.Contains(request.System, "Current role=final_reviewer "):
				call := finalRevCalls.Add(1)
				if call == 1 {
					if _, err := execute(ctx, "submit_review_verdict", json.RawMessage(`{"verdict":"changes_requested","summary":"第二轮仍差一处边界，请求第三轮修复"}`)); err != nil {
						return err
					}
					return observe("text", map[string]any{"text": "仍需第三轮补齐边界"})
				}
				var captured struct {
					SourceID string `json:"source_id"`
				}
				_ = json.Unmarshal([]byte(readOut), &captured)
				payload, _ := json.Marshal(map[string]any{
					"type": "engineering_report", "title": "三轮修复终审", "body": "第三轮已全部解决。",
					"source_ids": []string{captured.SourceID}, "limitations": []string{"只读验证"},
					"findings": []any{map[string]any{"title": "已解决", "detail": "确认", "status": "confirmed", "severity": "low", "source_ids": []string{captured.SourceID}}},
				})
				if _, err := execute(ctx, "publish_artifact", payload); err != nil {
					return err
				}
				return observe("text", map[string]any{"text": "第三轮终审通过"})
			}
			return errors.New("unexpected role")
		})

		e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		cancel, wait := startEngine(t, &e)
		defer wait()
		defer cancel()

		got := awaitRun(t, db, run.ID)
		if got.Status != "succeeded" {
			t.Fatalf("3-round engineering run failed: status=%s err=%s", got.Status, got.Error)
		}
		if engRounds.Load() != 3 || finalRevCalls.Load() != 2 {
			t.Fatalf("expected 3 engineer rounds and 2 final_reviewer calls, got eng=%d finalRev=%d", engRounds.Load(), finalRevCalls.Load())
		}
	})
}

func TestBuiltInResearchEarlyConvergenceSkipsSecondRound(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	run, m := seedMultiMemberRun(t, db, "", "free", nil, nil, false)
	in := w.RunInput{
		Message:       "研究链接证据",
		Workflow:      "research",
		ResearchScope: "given_sources",
		SourceURLs:    testutil.Sources,
		Assignments: map[string]string{
			"researcher":  m[1].ID,
			"critic":      m[2].ID,
			"coordinator": m[0].ID,
		},
	}
	planned, err := Plan(run.ID, in, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		old, _ := tx.Steps(run.ID)
		for _, s := range old {
			_ = tx.DeletePendingStep(s.ID)
		}
		run.Input = in
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		for _, s := range planned {
			if err := tx.SaveStep(s); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var researcherCalls, criticCalls, coordCalls atomic.Int32
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		fetched, err := execute(ctx, "fetch_url", json.RawMessage(`{"url":"`+testutil.Sources[0]+`"}`))
		if err != nil {
			return err
		}
		var captured struct {
			SourceID string `json:"source_id"`
		}
		_ = json.Unmarshal([]byte(fetched), &captured)
		switch {
		case strings.Contains(request.System, "Current role=researcher "):
			researcherCalls.Add(1)
			return observe("text", map[string]any{"text": "第一轮研究结论充分"})
		case strings.Contains(request.System, "Current role=critic "):
			criticCalls.Add(1)
			if _, err := execute(ctx, "submit_review_verdict", json.RawMessage(`{"verdict":"approved","summary":"证据扎实无需二轮质询"}`)); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "核查无异议"})
		case strings.Contains(request.System, "Current role=coordinator "):
			coordCalls.Add(1)
			payload, _ := json.Marshal(map[string]any{
				"type": "research_report", "title": "研究汇总", "body": "汇总正文",
				"source_ids": []string{captured.SourceID}, "limitations": []string{},
				"findings": []any{map[string]any{"title": "发现", "detail": "依据充分", "status": "supported", "source_ids": []string{captured.SourceID}}},
			})
			if _, err := execute(ctx, "publish_artifact", payload); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "研究报告已发布"})
		}
		return errors.New("unexpected research role")
	})

	e := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	defer cancel()

	got := awaitRun(t, db, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("converged research run failed: status=%s err=%s", got.Status, got.Error)
	}
	if researcherCalls.Load() != 1 || criticCalls.Load() != 1 || coordCalls.Load() != 1 {
		t.Fatalf("expected 3 total calls (1 researcher, 1 critic, 1 coordinator), got %d/%d/%d", researcherCalls.Load(), criticCalls.Load(), coordCalls.Load())
	}
}
