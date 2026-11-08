package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestFreeFileTaskExposesNativeWriteThroughRealCatalog is the acceptance that
// caught the gap: a free (non-engineering) responder in project_autonomous must
// receive both the host write_file and the CLI-native Write/Edit tools from the
// real catalog path, and the run must produce the actual bytes in a temporary
// directory. A reviewer step must stay read-only.
func TestFreeFileTaskExposesNativeWriteThroughRealCatalog(t *testing.T) {
	db, service, th, member, root := newFreeTaskFixture(t)
	mode := w.ExecutionProjectAutonomous
	var current w.Settings
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSettings(t.Context(), current.Revision, project.SettingsPatch{ExecutionMode: &mode}); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: th.ID, OwnerMemberID: member.ID,
		Goal: "写一个文件到临时目录", SourceMessageID: "free-file", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "free-native-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: created.Task.CurrentRevision, BudgetAccountID: created.Budget.ID,
		Input:  w.RunInput{Message: "写一个文件到临时目录", Workflow: "free", Targets: []string{member.ID}},
		Agents: []w.Agent{member}, CoordinatorID: member.ID, Status: "running", CreatedAt: w.Now(),
	}
	step := w.Step{ID: "free-native-step", RunID: run.ID, AgentID: member.ID, Role: "responder", Status: "running"}
	attempt := w.Attempt{ID: "free-native-attempt", RunID: run.ID, StepID: step.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	engine := &Engine{Store: db, AllowedRoots: []string{root}, AllowedModels: []string{"default"}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		live, err := tx.Workspace(th.WorkspaceID)
		if err != nil {
			return err
		}
		run.Workspace = live
		if err := service.EnsureRunAuthorization(tx, &run, "free-file"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		run.Status = "queued"
		step.Status = "pending"
		if err := tx.InsertRun(run, "free-native-key", "free-native-digest"); err != nil {
			return err
		}
		return tx.SaveStep(step)
	}); err != nil {
		t.Fatal(err)
	}
	prep := &stepRuntime{engine: engine, run: run, step: step, agent: member, attempt: attempt}
	allowed := engine.allowedTools(run, member, step)
	if !slices.Contains(allowed, "write_file") {
		t.Fatalf("free responder catalog lacks write_file: %v", allowed)
	}
	request, err := engine.buildRequest(t.Context(), run, step, member, allowed, "", agent.ConnectionInfo{}, agent.AdapterInfo{}, w.Message{}, Handoff{}, nil, nil, nil, nil, nil, attempt)
	if err != nil {
		t.Fatal(err)
	}
	native := nativecli.NativeExecutionTools(member.Executor, allowed, run.Input.ResearchScope, run.EffectiveExecutionMode)
	if len(native) == 0 {
		t.Fatalf("free autonomous responder exposes no native tools (allowed=%v)", allowed)
	}
	foundNativeWrite := false
	for _, name := range request.NativeTools {
		if name == "Write" || name == "Edit" || name == "write" || name == "edit" {
			foundNativeWrite = true
		}
	}
	if !foundNativeWrite {
		t.Fatalf("free autonomous responder native catalog lacks a write tool: native=%v request_native=%v", native, request.NativeTools)
	}
	foundHostWrite := false
	for _, tool := range request.Tools {
		if tool.Name == "write_file" {
			foundHostWrite = true
		}
	}
	if !foundHostWrite {
		t.Fatal("free autonomous responder host catalog lacks write_file")
	}
	// Reviewer stays read-only.
	reviewerAgent := member
	reviewerAgent.Executor = ""
	reviewerStep := w.Step{ID: "free-native-review", RunID: run.ID, AgentID: member.ID, Role: "reviewer", Status: "running"}
	reviewerAllowed := engine.allowedTools(run, reviewerAgent, reviewerStep)
	if slices.Contains(reviewerAllowed, "write_file") || slices.Contains(reviewerAllowed, "patch_file") {
		t.Fatalf("reviewer received a write tool: %v", reviewerAllowed)
	}
	if nativecli.NativeExecutionTools("claude", reviewerAllowed, run.Input.ResearchScope, run.EffectiveExecutionMode) != nil {
		if slices.Contains(nativecli.NativeExecutionTools("claude", reviewerAllowed, run.Input.ResearchScope, run.EffectiveExecutionMode), "Write") {
			t.Fatal("reviewer received a native write tool")
		}
	}
	_ = prep

	// The real run loop writes temporary bytes through the host tool.
	target := filepath.Join(root, "written.txt")
	runner := &testRunner{run: func(ctx context.Context, req model.Request, call int, execute agent.Execute, observe agent.Observe) error {
		// The host write tool takes a project-relative path; the platform MCP
		// boundary is the same one the member actually uses.
		payload, _ := json.Marshal(map[string]any{"path": "written.txt", "content": "free file bytes"})
		if _, err := execute(ctx, "write_file", payload); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "已写入临时文件。"})
	}}
	engine.Runners = map[string]agent.Runner{"claude": runner}
	engine.Fallback = runner
	engine.Model = nil
	run.Agents[0].Executor = "claude"
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.Agents = run.Agents
		return tx.SaveRun(current)
	}); err != nil {
		t.Fatal(err)
	}
	started, wait := startEngine(t, engine)
	defer wait()
	_ = started
	got := awaitRun(t, db, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("free file step = %+v", got)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "free file bytes" {
		t.Fatalf("free file task bytes = %q %v", data, err)
	}
}
