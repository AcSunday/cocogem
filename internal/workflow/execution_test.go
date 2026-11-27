package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestEngineExecutionManagerWritesOnlyCandidate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedRun(t, db)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("source baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	enableExecutor(t, db, &run, "claude")
	created, err := project.New(db).CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, Goal: "write an isolated candidate",
		SourceMessageID: "source-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		workspace, err := tx.Workspace(run.WorkspaceID)
		if err != nil {
			return err
		}
		workspace.Root = root
		if err := tx.SaveWorkspace(workspace); err != nil {
			return err
		}
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Workspace = workspace
		current.TaskID = created.Task.ID
		current.TaskRevision = created.Task.CurrentRevision
		current.BudgetAccountID = created.Budget.ID
		current.WriteBranch = true
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}

	base := defaultRunner()
	runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
		if request.WorkspaceRoot == "" || filepath.Clean(request.WorkspaceRoot) == filepath.Clean(root) {
			return errors.New("runner received the source root instead of a candidate")
		}
		if strings.Contains(request.System, "Current role=responder") {
			if err := os.WriteFile(filepath.Join(request.WorkspaceRoot, "candidate.txt"), []byte("candidate change"), 0600); err != nil {
				return err
			}
		}
		return base.run(ctx, request, attempt, execute, observe)
	}}
	engine := Engine{
		Store: db, Executions: execution.New(db, filepath.Join(t.TempDir(), "executions"), "engine-owner"),
		Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{},
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	result := awaitRun(t, db, run.ID)
	cancel()
	engine.Wait()
	if result.Status != "succeeded" {
		t.Fatalf("run status=%s error=%s", result.Status, result.Error)
	}
	if result.ExecutionRef == "" || result.ExecutionRoot == "" || filepath.Clean(result.ExecutionRoot) == filepath.Clean(root) {
		t.Fatalf("run execution boundary = %+v", result)
	}
	candidate, err := os.ReadFile(filepath.Join(result.ExecutionRoot, "candidate.txt"))
	if err != nil || string(candidate) != "candidate change" {
		t.Fatalf("candidate content = %q, %v", candidate, err)
	}
	if _, err := os.Stat(filepath.Join(root, "candidate.txt")); !os.IsNotExist(err) {
		t.Fatalf("candidate write changed source: %v", err)
	}
	original, err := os.ReadFile(filepath.Join(root, "main.txt"))
	if err != nil || string(original) != "source baseline" {
		t.Fatalf("source content = %q, %v", original, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		workspaceExecution, err := tx.Execution(result.ExecutionRef)
		if err != nil {
			return err
		}
		if workspaceExecution.Status != "completed" {
			t.Fatalf("execution lease did not settle: %+v", workspaceExecution)
		}
		effect, err := tx.EffectByKey(created.Task.ID, "execution:"+result.ExecutionRef)
		if err != nil {
			return err
		}
		if effect.Status != "applied" || effect.BeforeDigest == "" || effect.BeforeDigest == effect.AfterDigest {
			t.Fatalf("candidate effect = %+v", effect)
		}
		task, err := tx.Task(created.Task.ID)
		if err != nil {
			return err
		}
		if task.Status != "ready" || task.EvaluationStatus != "incomplete" {
			t.Fatalf("successful run changed objective completion: %+v", task)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteToTaskOversizedNonGitRootContinuesDirectly(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedRun(t, db)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A 33 MiB file exceeds the isolated file-copy limit (32 MiB/file) on a non-git root.
	big, err := os.Create(filepath.Join(root, "dataset.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(33 << 20); err != nil {
		big.Close()
		t.Fatal(err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	enableExecutor(t, db, &run, "claude")
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		workspace, err := tx.Workspace(run.WorkspaceID)
		if err != nil {
			return err
		}
		workspace.Root = root
		if err := tx.SaveWorkspace(workspace); err != nil {
			return err
		}
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Workspace = workspace
		current.TaskIntake = true
		current.Input.Message = "把工作区里的配置修复一下"
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{
			ID: "intake-user", ThreadID: run.ThreadID, RunID: run.ID,
			Role: "user", Content: current.Input.Message, Status: "completed", CreatedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}

	calls := 0
	var continuationRoot string
	runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
		calls++
		if calls == 1 {
			raw, _ := json.Marshal(taskPromotionInput{
				Title:         "修复配置",
				Goal:          "把工作区里的配置修复一下",
				EvidenceQuote: "把工作区里的配置修复一下",
			})
			if _, err := execute(ctx, "promote_to_task", raw); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "任务已建立，准备继续执行。"})
		}
		continuationRoot = request.WorkspaceRoot
		return observe("text", map[string]any{"text": "已在绑定目录中完成修复。"})
	}}
	engine := Engine{
		Store:      db,
		Executions: execution.New(db, filepath.Join(t.TempDir(), "executions"), "engine-owner"),
		Runners:    map[string]agent.Runner{"claude": runner},
		Web:        testutil.Web{},
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	result := awaitRun(t, db, run.ID)
	cancel()
	engine.Wait()

	if result.Status != "succeeded" {
		t.Fatalf("run status=%s error=%q, want succeeded without checkpoint interrupt", result.Status, result.Error)
	}
	if calls != 2 {
		t.Fatalf("step invocations = %d, want 2 (intake + continuation)", calls)
	}
	if filepath.Clean(continuationRoot) != filepath.Clean(root) {
		t.Fatalf("continuation WorkspaceRoot = %q, want direct root %q", continuationRoot, root)
	}
	if result.WriteBranch {
		t.Fatalf("expected WriteBranch=false after degrading oversized non-git isolation")
	}
	var sawNotice bool
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, ev := range events {
			if ev.Type == "native_notice" && strings.Contains(string(ev.Data), "candidate_isolation_skipped") {
				sawNotice = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !sawNotice {
		t.Fatal("expected candidate_isolation_skipped native_notice event")
	}
}

func TestSchedulerLoopErrorFailsRunInsteadOfCheckpointInterrupted(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedRun(t, db)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enableExecutor(t, db, &run, "claude")
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		workspace, err := tx.Workspace(run.WorkspaceID)
		if err != nil {
			return err
		}
		workspace.Root = root
		if err := tx.SaveWorkspace(workspace); err != nil {
			return err
		}
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Workspace = workspace
		current.TaskIntake = true
		current.Input.Message = "执行一个会触发目录失效的任务"
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{
			ID: "fail-user", ThreadID: run.ThreadID, RunID: run.ID,
			Role: "user", Content: current.Input.Message, Status: "completed", CreatedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}

	runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
		raw, _ := json.Marshal(taskPromotionInput{
			Title:         "目录失效测试",
			Goal:          "执行一个会触发目录失效的任务",
			EvidenceQuote: "执行一个会触发目录失效的任务",
		})
		if _, err := execute(ctx, "promote_to_task", raw); err != nil {
			return err
		}
		// Remove the bound root so the mid-flight prepareExecution fails with a real I/O error.
		if err := os.RemoveAll(root); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "任务已创建。"})
	}}
	engine := Engine{
		Store:      db,
		Executions: execution.New(db, filepath.Join(t.TempDir(), "executions"), "engine-owner"),
		Runners:    map[string]agent.Runner{"claude": runner},
		Web:        testutil.Web{},
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	result := awaitRun(t, db, run.ID)
	cancel()
	engine.Wait()

	if result.Status != "failed" {
		t.Fatalf("run status=%s error=%q, want failed (never interrupted)", result.Status, result.Error)
	}
	if strings.Contains(result.Error, "checkpoint preserved for explicit resume") {
		t.Fatalf("run error masked real failure as checkpoint interrupt: %q", result.Error)
	}
}

func TestOversizedNonGitRootThreeConsecutiveRunsDoNotLeakActiveWriters(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seed := seedRun(t, db)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(33 << 20); err != nil {
		big.Close()
		t.Fatal(err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	enableExecutor(t, db, &seed, "claude")
	created, err := project.New(db).CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: seed.WorkspaceID, ThreadID: seed.ThreadID, Goal: "consecutive degraded runs",
		SourceMessageID: "src-msg", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		ws, err := tx.Workspace(seed.WorkspaceID)
		if err != nil {
			return err
		}
		ws.Root = root
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		seed.Workspace = ws
		seed.TaskID = created.Task.ID
		seed.TaskRevision = created.Task.CurrentRevision
		seed.BudgetAccountID = created.Budget.ID
		seed.WriteBranch = true
		return tx.SaveRun(seed)
	}); err != nil {
		t.Fatal(err)
	}

	runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
		return observe("text", map[string]any{"text": "完成本轮任务。"})
	}}
	engine := Engine{
		Store:      db,
		Executions: execution.New(db, filepath.Join(t.TempDir(), "executions"), "engine-owner"),
		Runners:    map[string]agent.Runner{"claude": runner},
		Web:        testutil.Web{},
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() {
		cancel()
		engine.Wait()
	}()

	checkAccountZero := func(label string) {
		t.Helper()
		if err := db.Read(t.Context(), func(tx *store.Tx) error {
			acc, err := tx.BudgetAccount(created.Budget.ID)
			if err != nil {
				return err
			}
			if acc.ActiveWriters != 0 || acc.ActiveBranches != 0 {
				t.Fatalf("%s: leaked budget counters active_writers=%d active_branches=%d", label, acc.ActiveWriters, acc.ActiveBranches)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Run 1 (seeded)
	res1 := awaitRun(t, db, seed.ID)
	if res1.Status != "succeeded" || res1.WriteBranch {
		t.Fatalf("run 1: status=%s write_branch=%v err=%q", res1.Status, res1.WriteBranch, res1.Error)
	}
	checkAccountZero("after run 1")

	// Runs 2 and 3 (default WriterBranches is 2; a leak would block run 3 on ErrExecutionBusy).
	for i := 2; i <= 3; i++ {
		nextID := w.NewID()
		nextRun := seed
		nextRun.ID = nextID
		nextRun.Status = "queued"
		nextRun.WriteBranch = true
		nextRun.BudgetStarted = false
		nextRun.BudgetReserved = false
		nextRun.ExecutionRef = ""
		nextRun.ExecutionRoot = ""
		nextRun.TaskDirectory = ""
		nextRun.CreatedAt = w.Now()
		nextRun.FinishedAt = ""
		nextRun.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
		step := w.Step{
			ID: w.NewID(), RunID: nextID, AgentID: seed.Agents[0].ID,
			AgentName: seed.Agents[0].DisplayName, Role: "responder",
			Instruction: "execute turn", Status: "pending",
		}
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			if err := tx.InsertRun(nextRun, "key-"+nextID, "hash-"+nextID); err != nil {
				return err
			}
			return tx.SaveStep(step)
		}); err != nil {
			t.Fatal(err)
		}
		res := awaitRun(t, db, nextID)
		if res.Status != "succeeded" {
			t.Fatalf("run %d: status=%s err=%q (expected succeeded without ErrExecutionBusy)", i, res.Status, res.Error)
		}
		if res.WriteBranch {
			t.Fatalf("run %d: expected WriteBranch=false after degradation", i)
		}
		checkAccountZero("after run")
	}
}

func TestReviewSandboxDegradesToReadOnlyBeforeToolPlanning(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedRun(t, db)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(root, "oversized.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(33 << 20); err != nil {
		big.Close()
		t.Fatal(err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	enableExecutor(t, db, &run, "claude")
	var reviewStepID string
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		ws, err := tx.Workspace(run.WorkspaceID)
		if err != nil {
			return err
		}
		ws.Root = root
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Workspace = ws
		current.WriteBranch = false
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].Role = "reviewer"
		steps[0].ReadOnly = false
		reviewStepID = steps[0].ID
		return tx.SaveStep(steps[0])
	}); err != nil {
		t.Fatal(err)
	}

	var capturedReq model.Request
	runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
		capturedReq = request
		return observe("text", map[string]any{"text": "只读复核完成。"})
	}}
	engine := Engine{
		Store:      db,
		Executions: execution.New(db, filepath.Join(t.TempDir(), "executions"), "engine-owner"),
		Runners:    map[string]agent.Runner{"claude": runner},
		Web:        testutil.Web{},
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	result := awaitRun(t, db, run.ID)
	cancel()
	engine.Wait()

	if result.Status != "succeeded" {
		t.Fatalf("review run status=%s err=%q", result.Status, result.Error)
	}
	if !capturedReq.ReadOnly {
		t.Fatalf("expected request.ReadOnly=true after review sandbox degradation")
	}
	for _, forbidden := range []string{"write_file", "patch_file", "run_command", "run_test"} {
		if slices.ContainsFunc(capturedReq.Tools, func(tool model.Tool) bool { return tool.Name == forbidden }) {
			t.Fatalf("forbidden mutating tool %q was still injected into degraded reviewer request", forbidden)
		}
	}
	var sawNotice, sawReadOnlyStart bool
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		if len(steps) != 1 || !steps[0].ReadOnly {
			t.Fatalf("persisted review step ReadOnly=%v, want true", steps)
		}
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, ev := range events {
			if ev.Type == "native_notice" && strings.Contains(string(ev.Data), "verify_sandbox_skipped") {
				sawNotice = true
			}
			if ev.Type == "step_started" && ev.StepID == reviewStepID {
				var st w.Step
				_ = json.Unmarshal(ev.Data, &st)
				sawReadOnlyStart = st.ReadOnly
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !sawNotice {
		t.Fatal("expected verify_sandbox_skipped native_notice event")
	}
	if !sawReadOnlyStart {
		t.Fatal("expected step_started event to already carry ReadOnly=true")
	}
}

func TestCanceledAndTimedOutStepsDoNotReportCheckpointPreserved(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		run := seedRun(t, db)
		enableExecutor(t, db, &run, "claude")
		started := make(chan struct{})
		runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}}
		engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		ctx, cancel := context.WithCancel(t.Context())
		if err := engine.Start(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		<-started
		if _, err := engine.Cancel(t.Context(), run.ID); err != nil {
			cancel()
			t.Fatal(err)
		}
		result := awaitRun(t, db, run.ID)
		cancel()
		engine.Wait()
		if result.Status != "canceled" {
			t.Fatalf("status=%s err=%q, want canceled", result.Status, result.Error)
		}
		if strings.Contains(result.Error, "checkpoint preserved for explicit resume") {
			t.Fatalf("canceled run leaked checkpoint message: %q", result.Error)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		run := seedRun(t, db)
		enableExecutor(t, db, &run, "claude")
		if err := db.Write(t.Context(), func(tx *store.Tx) error {
			current, err := tx.Run(run.ID)
			if err != nil {
				return err
			}
			current.ExecutionPolicy = &w.ExecutionPolicy{RunTimeout: 60 * time.Millisecond}
			return tx.SaveRun(current)
		}); err != nil {
			t.Fatal(err)
		}
		runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
			<-ctx.Done()
			return ctx.Err()
		}}
		engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
		ctx, cancel := context.WithCancel(t.Context())
		if err := engine.Start(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		result := awaitRun(t, db, run.ID)
		cancel()
		engine.Wait()
		if result.Status != "failed" || result.FailureKind != failureRunTimeout {
			t.Fatalf("status=%s kind=%s err=%q, want failed/run_timeout", result.Status, result.FailureKind, result.Error)
		}
		if strings.Contains(result.Error, "checkpoint preserved for explicit resume") {
			t.Fatalf("timed out run leaked checkpoint message: %q", result.Error)
		}
	})
}

func TestRetryAfterOversizedNonGitDegradeSucceeds(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedRun(t, db)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(root, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(33 << 20); err != nil {
		big.Close()
		t.Fatal(err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	enableExecutor(t, db, &run, "claude")
	created, err := project.New(db).CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, Goal: "retry degraded task",
		SourceMessageID: "retry-src", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		ws, err := tx.Workspace(run.WorkspaceID)
		if err != nil {
			return err
		}
		ws.Root = root
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Workspace = ws
		current.TaskID = created.Task.ID
		current.TaskRevision = created.Task.CurrentRevision
		current.BudgetAccountID = created.Budget.ID
		current.WriteBranch = true
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}

	attemptNum := 0
	var retryRoot string
	runner := &testRunner{run: func(ctx context.Context, request model.Request, attempt int, execute agent.Execute, observe agent.Observe) error {
		attemptNum++
		if attemptNum == 1 {
			return errors.New("synthetic step failure")
		}
		retryRoot = request.WorkspaceRoot
		return observe("text", map[string]any{"text": "重试成功。"})
	}}
	engine := Engine{
		Store:      db,
		Executions: execution.New(db, filepath.Join(t.TempDir(), "executions"), "engine-owner"),
		Runners:    map[string]agent.Runner{"claude": runner},
		Web:        testutil.Web{},
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() {
		cancel()
		engine.Wait()
	}()

	first := awaitRun(t, db, run.ID)
	if first.Status != "failed" || first.WriteBranch {
		t.Fatalf("first run: status=%s write_branch=%v err=%q", first.Status, first.WriteBranch, first.Error)
	}
	retried, err := engine.Retry(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retried.WriteBranch {
		t.Fatalf("retried run should inherit degraded WriteBranch=false")
	}
	second := awaitRun(t, db, retried.ID)
	if second.Status != "succeeded" {
		t.Fatalf("retried run: status=%s err=%q", second.Status, second.Error)
	}
	if filepath.Clean(retryRoot) != filepath.Clean(root) {
		t.Fatalf("retried WorkspaceRoot=%q, want %q", retryRoot, root)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		acc, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		if acc.ActiveWriters != 0 || acc.ActiveBranches != 0 {
			t.Fatalf("post-retry budget counters leaked: writers=%d branches=%d", acc.ActiveWriters, acc.ActiveBranches)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
