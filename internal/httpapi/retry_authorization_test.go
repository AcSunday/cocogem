package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// memberRetryWriter is a test runner that performs one real host write through
// the ordinary execute hook, so the retry journey proves an actual effect.
type memberRetryWriter struct {
	target string
}

func (r memberRetryWriter) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	payload, _ := json.Marshal(map[string]any{"path": r.target, "content": "retry effect bytes"})
	if _, err := execute(ctx, "write_file", payload); err != nil {
		return err
	}
	return observe("text", map[string]any{"text": "重试完成并写入临时文件。"})
}

// TestMemberRetryReauthorizesUnderLivePolicy is the P1 retry regression: a failed
// member step of a real Task is retried, and the new Run must be re-authorized
// under the live global policy (non-empty mode, epoch and action ceiling) without
// mutating the old Run or resetting the root budget. The retried execution then
// produces a real temporary-file effect.
func TestMemberRetryReauthorizesUnderLivePolicy(t *testing.T) {
	area := t.TempDir()
	root := filepath.Join(area, "project")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.WriteFile(filepath.Join(area, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(area, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := &workflow.Engine{Store: db, AllowedRoots: []string{root}, AllowedModels: []string{"default"}, Limits: workflow.DefaultLimits()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	api := newDeliveryAPI(t, area, db, engine)

	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "重试授权", Root: root}, nil, 200, &workspace)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+workspace.ID+"/agents", nil, nil, 200, &members)
	member := members.Items[0]
	member.Executor = "claude"
	api.call("PATCH", "/api/v1/agents/"+member.ID, member, map[string]string{"If-Match": "1"}, 200, &member)
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+workspace.ID+"/threads", w.Thread{Title: "重试授权会话", AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Workflow: "free"}, nil, 200, &thread)

	// Seed an accepted Task with a real failed run, then retry that member step.
	service := project.New(db)
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: workspace.ID, ThreadID: thread.ID, OwnerMemberID: member.ID,
		Goal: "重试后写入临时文件", SourceMessageID: "retry-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := w.Run{
		ID: "retry-source-run", WorkspaceID: workspace.ID, ThreadID: thread.ID, TaskID: created.Task.ID,
		TaskRevision: created.Task.CurrentRevision, BudgetAccountID: created.Budget.ID,
		Input:  w.RunInput{Message: "重试后写入临时文件", Workflow: "free", Targets: []string{member.ID}},
		Agents: []w.Agent{member}, CoordinatorID: member.ID, Status: "running", CreatedAt: w.Now(),
	}
	failedStep := w.Step{ID: "retry-target-step", RunID: failed.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: "responder", Instruction: "write", Status: "failed", Error: "protocol failure"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		live, err := tx.Workspace(workspace.ID)
		if err != nil {
			return err
		}
		failed.Workspace = live
		if err := service.EnsureRunAuthorization(tx, &failed, "retry-source"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&failed); err != nil {
			return err
		}
		failed.Status, failed.FinishedAt = "failed", w.Now()
		failed.Error = "protocol failure"
		if err := tx.InsertRun(failed, "retry-source-key", "retry-source-digest"); err != nil {
			return err
		}
		return tx.SaveStep(failedStep)
	}); err != nil {
		t.Fatal(err)
	}

	// The retry executes under the live policy with a real temporary effect.
	engine.Runners = map[string]agent.Runner{"claude": memberRetryWriter{target: "retry-effect.txt"}}
	engine.Fallback = memberRetryWriter{target: "retry-effect.txt"}
	var retried w.Run
	api.call("POST", "/api/v1/runs/"+failed.ID+"/retry", map[string]any{"step_id": failedStep.ID},
		map[string]string{"Idempotency-Key": "retry-authorization-key-0001"}, 202, &retried)
	if retried.ID == failed.ID || retried.ID == "" {
		t.Fatalf("retry did not create a new run: %+v", retried)
	}

	deadline := time.Now().Add(10 * time.Second)
	var final w.Run
	for time.Now().Before(deadline) {
		var view struct {
			Run w.Run `json:"run"`
		}
		api.call("GET", "/api/v1/runs/"+retried.ID, nil, nil, 200, &view)
		final = view.Run
		if w.Terminal(final.Status) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != "succeeded" {
		t.Fatalf("retried run = %+v", final)
	}
	if final.PermissionEpoch < 1 || final.RequestedExecutionMode == "" || final.EffectiveExecutionMode == "" {
		t.Fatalf("retried run was not re-authorized: %+v", final)
	}
	if len(final.GrantActions) == 0 || final.GrantSnapshot == "" {
		t.Fatalf("retried run has an empty action ceiling: %+v", final)
	}
	if final.TaskID != created.Task.ID || final.TaskRevision != created.Task.CurrentRevision {
		t.Fatalf("retry changed the accepted objective: %+v", final)
	}
	// The root budget is shared, never reset by the retry.
	var budget w.BudgetAccount
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/budget", nil, nil, 200, &budget)
	if budget.ID != created.Budget.ID {
		t.Fatalf("retry reset the root budget: %+v", budget)
	}
	// The old run keeps its own frozen history.
	var oldView struct {
		Run w.Run `json:"run"`
	}
	api.call("GET", "/api/v1/runs/"+failed.ID, nil, nil, 200, &oldView)
	if oldView.Run.Status != "failed" || oldView.Run.TaskRevision != created.Task.CurrentRevision {
		t.Fatalf("retry mutated the source run: %+v", oldView.Run)
	}
	// The approved host effect really produced its bytes.
	if data, err := os.ReadFile(filepath.Join(root, "retry-effect.txt")); err != nil || string(data) != "retry effect bytes" {
		t.Fatalf("retried effect bytes = %q %v", data, err)
	}
}
