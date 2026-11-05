package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
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
		if strings.Contains(request.System, "Current role=researcher") {
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
