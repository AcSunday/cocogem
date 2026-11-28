package workflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestFullAccessOrdinaryChatAdmitsRunCommandAndNativeTools(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	root := t.TempDir()
	outsideDir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(outsideDir); err == nil {
		outsideDir = resolved
	}
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("PORT=3000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "note.txt"), []byte("external ok\n"), 0644); err != nil {
		t.Fatal(err)
	}

	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionFullAccess); err != nil {
		t.Fatal(err)
	}

	ws := w.Workspace{ID: "ws-full", Name: "FullRepo", Root: root, Revision: 1, CreatedAt: w.Now()}
	unboundWS := w.Workspace{ID: "ws-unbound", Name: "General", Root: "", Revision: 1, CreatedAt: w.Now()}
	member := w.Agent{
		ID: "member-full", WorkspaceID: ws.ID, DisplayName: "全权助手", Handle: "helper",
		Model: "gpt-5", Revision: 1,
		Tools: []string{"read_file", "write_file", "run_command", "run_test", "publish_artifact"},
	}
	th := w.Thread{ID: "th-full", WorkspaceID: ws.ID, Title: "普通对话查资料", CoordinatorID: member.ID, AgentIDs: []string{member.ID}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveWorkspace(unboundWS); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveMember(w.WorkspaceMember{WorkspaceID: ws.ID, AgentID: member.ID, Enabled: true, Tools: member.Tools, Revision: 1}); err != nil {
			return err
		}
		if err := tx.SaveMember(w.WorkspaceMember{WorkspaceID: unboundWS.ID, AgentID: member.ID, Enabled: true, Tools: member.Tools, Revision: 1}); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Ordinary chat (TaskIntake: true, TaskID: "") in full_access receives full actions, not read-only.
	run := w.Run{
		ID: "run-chat-full", WorkspaceID: ws.ID, ThreadID: th.ID,
		TaskIntake:             true,
		RequestedExecutionMode: w.ExecutionFullAccess,
		EffectiveExecutionMode: w.ExecutionFullAccess,
		Input:                  w.RunInput{Message: "查一下环境配置并跑个命令"},
		Agents:                 []w.Agent{member},
		CoordinatorID:          member.ID,
		Workspace:              ws,
		Status:                 "running",
		CreatedAt:              w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return service.EnsureRunAuthorization(tx, &run, "user-msg-1")
	}); err != nil {
		t.Fatal(err)
	}
	for _, need := range []string{"read", "write", "execute", "run_test"} {
		if !slices.Contains(run.GrantActions, need) {
			t.Fatalf("expected full_access intake run GrantActions to include %q, got %v", need, run.GrantActions)
		}
	}

	// Native capabilities are preserved in ordinary full_access chat (unless step.ReadOnly is set).
	codexMember := member
	codexMember.Executor = "codex"
	step := w.Step{ID: "step-chat", RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: "responder", Instruction: "check", Status: "running"}
	attempt := w.Attempt{ID: "att-chat", RunID: run.ID, StepID: step.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	engine := &Engine{Store: db, AllowedRoots: []string{root}, AllowedModels: []string{"gpt-5"}}
	allowedTools := []string{"read_file", "write_file", "run_command", "run_test"}
	req, err := engine.buildRequest(
		t.Context(), run, step, codexMember, allowedTools, "",
		agent.ConnectionInfo{}, agent.AdapterInfo{},
		w.Message{ID: w.NewID(), RunID: run.ID, StepID: step.ID},
		Handoff{}, nil, nil, nil, nil, nil, attempt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !req.TrustedExecution || len(req.NativeTools) == 0 {
		t.Fatalf("expected TrustedExecution and native tools in full_access ordinary chat, got trusted=%v native=%v", req.TrustedExecution, req.NativeTools)
	}

	// Explicit step.ReadOnly strips write/command access & sets req.ReadOnly even in full_access.
	readOnlyStep := step
	readOnlyStep.ID = "step-ro"
	readOnlyStep.ReadOnly = true
	roAttempt := w.Attempt{ID: "att-ro", RunID: run.ID, StepID: readOnlyStep.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	roReq, err := engine.buildRequest(
		t.Context(), run, readOnlyStep, codexMember, []string{"read_file"}, "",
		agent.ConnectionInfo{}, agent.AdapterInfo{},
		w.Message{ID: w.NewID(), RunID: run.ID, StepID: readOnlyStep.ID},
		Handoff{}, nil, nil, nil, nil, nil, roAttempt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !roReq.ReadOnly || roReq.TrustedExecution {
		t.Fatalf("expected ReadOnly step to force ReadOnly=true TrustedExecution=false, got readOnly=%v trusted=%v", roReq.ReadOnly, roReq.TrustedExecution)
	}

	// 2. PlanOnly strictly keeps ordinary chat read-only even in full_access.
	planRun := run
	planRun.ID = "run-chat-plan"
	planRun.TaskID = ""
	planRun.TaskRevision = 0
	planRun.GrantActions = nil
	planRun.Input = w.RunInput{Message: "只做方案", PlanOnly: true}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return service.EnsureRunAuthorization(tx, &planRun, "user-msg-plan")
	}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(planRun.GrantActions, "execute") || slices.Contains(planRun.GrantActions, "write") {
		t.Fatalf("PlanOnly in full_access must forbid write/execute in GrantActions, got %v", planRun.GrantActions)
	}

	// 3. Unbound workspace (ws.Root == "") falls back to run.TaskDirectory for run_command and executes in outside CWD.
	taskDir := t.TempDir()
	unboundRun := w.Run{
		ID: "run-unbound", WorkspaceID: unboundWS.ID, ThreadID: th.ID,
		TaskIntake: true, TaskDirectory: taskDir,
		RequestedExecutionMode: w.ExecutionFullAccess, EffectiveExecutionMode: w.ExecutionFullAccess,
		Input: w.RunInput{Message: "run pwd in external dir"}, Agents: []w.Agent{member}, CoordinatorID: member.ID,
		Workspace: unboundWS, Status: "running", CreatedAt: w.Now(),
	}
	unboundStep := w.Step{ID: "step-unbound", RunID: unboundRun.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: "responder", Instruction: "check", Status: "running"}
	unboundAttempt := w.Attempt{ID: "att-unbound", RunID: unboundRun.ID, StepID: unboundStep.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &unboundRun, "user-msg-unbound"); err != nil {
			return err
		}
		if err := tx.InsertRun(unboundRun, "key-unbound", "digest-unbound"); err != nil {
			return err
		}
		if err := tx.SaveStep(unboundStep); err != nil {
			return err
		}
		return tx.SaveAttempt(unboundAttempt)
	}); err != nil {
		t.Fatal(err)
	}
	rt := &stepRuntime{
		ctx: t.Context(), engine: engine, run: unboundRun, step: unboundStep, agent: member, attempt: unboundAttempt,
		allowed: allowedTools, nativeCaptured: map[string]bool{},
	}
	out, err := rt.executeTool(t.Context(), "run_command", json.RawMessage(`{"argv":["pwd"],"cwd":"`+outsideDir+`"}`))
	if err != nil {
		t.Fatalf("run_command with external cwd in full_access unbound chat failed: %v", err)
	}
	if !strings.Contains(out, outsideDir) {
		t.Fatalf("expected pwd output to contain %q, got %q", outsideDir, out)
	}

	// 4. Verify CheckToolAccess admits execute & sensitive_path in full_access ordinary chat, but denies PlanOnly.
	planDecision, err := service.CheckToolAccess(t.Context(), project.AccessRequest{
		Run: planRun, Name: "run_command", Action: "execute", Scope: root,
		Arguments: json.RawMessage(`{"argv":["pwd"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if planDecision.Allowed {
		t.Fatalf("expected PlanOnly run to block run_command, got %+v", planDecision)
	}
	fullDecision, err := service.CheckToolAccess(t.Context(), project.AccessRequest{
		Run: run, Name: "run_command", Action: "execute", Scope: outsideDir,
		Risk: "sensitive_path", Arguments: json.RawMessage(`{"argv":["pwd"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fullDecision.Allowed {
		t.Fatalf("expected full_access ordinary chat to admit run_command outside root/sensitive_path, got %+v", fullDecision)
	}

	// Verify step.ReadOnly refuses run_command via runtime permission gate even in full_access.
	rtRO := &stepRuntime{
		ctx: t.Context(), engine: engine, run: run, step: readOnlyStep, agent: member, attempt: roAttempt,
		allowed: []string{"read_file"}, nativeCaptured: map[string]bool{},
	}
	if _, roErr := rtRO.executeTool(t.Context(), "run_command", json.RawMessage(`{"argv":["pwd"]}`)); roErr == nil {
		t.Fatalf("expected explicit step.ReadOnly to deny run_command even in full_access")
	}

	// 5. Direct ReadFile via Execute on .env.example under ExecutionFullAccess.
	res, err := tools.Execute(t.Context(), "read_file", json.RawMessage(`{"path":".env.example"}`), tools.Scope{
		Root: root, ExecutionMode: w.ExecutionFullAccess,
		Capture: func(s w.Source) (w.Source, error) { return s, nil },
	})
	if err != nil || !strings.Contains(res, "PORT=3000") {
		t.Fatalf("expected .env.example readable under ExecutionFullAccess, err=%v res=%q", err, res)
	}
}
