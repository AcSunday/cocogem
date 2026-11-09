package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestRetryOrdinaryIntakeKeepsReadAuthorization is the intake-retry regression: a
// plain conversation (TaskIntake, no Task yet) retries a failed member step and
// must be re-authorized under the live policy, obtaining a real host read without
// creating a Task or a budget account.
func TestRetryOrdinaryIntakeKeepsReadAuthorization(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	const marker = "ORDINARY_INTAKE_RETRY_BYTES"
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	source := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "task_intake", Instruction: "Explain the public fixture text"}})
	source.TaskIntake = true
	source.CoordinatorID = source.Agents[0].ID
	source.Input.Message = "解释这段公开文本，不需要创建任务"
	service := project.New(db)
	var failedStep w.Step
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		workspace, err := tx.Workspace(source.WorkspaceID)
		if err != nil {
			return err
		}
		workspace.Root = root
		if err := tx.SaveWorkspace(workspace); err != nil {
			return err
		}
		source.Workspace = workspace
		thread, err := tx.Thread(source.ThreadID)
		if err != nil {
			return err
		}
		thread.AgentIDs, thread.CoordinatorID = []string{source.Agents[0].ID}, source.Agents[0].ID
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := service.EnsureRunAuthorization(tx, &source, "intake-user-message"); err != nil {
			return err
		}
		source.Status = "queued"
		if err := tx.SaveRun(source); err != nil {
			return err
		}
		steps, err := tx.Steps(source.ID)
		if err != nil {
			return err
		}
		failedStep = steps[0]
		failedStep.Status = "failed"
		if err := tx.SaveStep(failedStep); err != nil {
			return err
		}
		source.Status, source.Error, source.FinishedAt = "failed", "synthetic intake failure", w.Now()
		return tx.SaveRun(source)
	}); err != nil {
		t.Fatal(err)
	}
	var original w.Run
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		original, err = tx.Run(source.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if original.TaskID != "" || original.GrantSnapshot == "" || original.PermissionEpoch == 0 {
		t.Fatalf("fixture is not a genuine authorized intake run: %+v", original)
	}
	var calls, reads atomic.Int32
	runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		calls.Add(1)
		if !slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "read_file" }) {
			return fmt.Errorf("intake retry lost its read_file menu")
		}
		const callID = "intake-retry-read"
		input := json.RawMessage(`{"path":"fixture.txt"}`)
		if err := observe("tool_started", map[string]any{"tool_call_id": callID, "name": "read_file", "input": input}); err != nil {
			return err
		}
		output, err := execute(agent.WithToolCall(ctx, callID), "read_file", input)
		if err != nil {
			return fmt.Errorf("intake retry could not read: %w", err)
		}
		if !strings.Contains(output, marker) {
			return fmt.Errorf("intake retry did not obtain the real bytes")
		}
		reads.Add(1)
		if err := observe("tool_finished", map[string]any{"tool_call_id": callID, "name": "read_file", "status": "ok", "output": output}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "这段公开文本是一份说明。"})
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Limits: DefaultLimits()}
	retried, err := engine.RetryStep(t.Context(), source.ID, failedStep.ID, "intake-retry-key-0001")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Wait()
	var finished w.Run
	for {
		changed := db.Changed()
		if err := db.Read(ctx, func(tx *store.Tx) error {
			var err error
			finished, err = tx.Run(retried.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if w.Terminal(finished.Status) {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("intake retry did not settle")
		}
	}
	if finished.Status != "succeeded" || calls.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("intake retry: status=%s calls=%d reads=%d epoch=%d actions=%v error=%s",
			finished.Status, calls.Load(), reads.Load(), finished.PermissionEpoch, finished.GrantActions, finished.Error)
	}
	if finished.PermissionEpoch < 1 || len(finished.GrantActions) == 0 {
		t.Fatalf("intake retry was not re-authorized: %+v", finished)
	}
	if finished.TaskID != "" || finished.BudgetAccountID != "" {
		t.Fatal("intake retry created a Task or budget")
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		thread, err := tx.Thread(source.ThreadID)
		if err != nil {
			return err
		}
		if thread.TaskID != "" {
			return fmt.Errorf("ordinary chat thread gained a Task")
		}
		tasks, err := tx.Tasks(source.WorkspaceID)
		if err != nil {
			return err
		}
		if len(tasks) != 0 {
			return fmt.Errorf("intake retry created %d Tasks", len(tasks))
		}
		stored, err := tx.Run(source.ID)
		if err != nil {
			return err
		}
		if stored.Status != original.Status || stored.TaskID != original.TaskID {
			return fmt.Errorf("intake retry rewrote the original Run")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
