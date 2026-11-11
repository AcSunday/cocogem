package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type liveCoordinatorProfile struct {
	Name         string   `json:"name"`
	Executor     string   `json:"executor"`
	Model        string   `json:"model"`
	Thinking     string   `json:"thinking"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
}

type liveCoordinatorWorker struct{}

func (liveCoordinatorWorker) Run(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
	return observe("text", map[string]any{"text": "COCOGEM_COORDINATOR_42"})
}

type liveCoordinatorAdapters struct{ nativecli.Provider }

func (a liveCoordinatorAdapters) Check(ctx context.Context, executor string) (agent.AdapterInfo, error) {
	if executor == "fixture" {
		return agent.AdapterInfo{ID: executor, Installed: true, Supported: true, Version: "fixture", Identity: "fixture"}, nil
	}
	return a.Provider.Check(ctx, executor)
}

func (a liveCoordinatorAdapters) InspectExecutionConnection(ctx context.Context, executor, selected, thinking string) (agent.ConnectionInfo, error) {
	if executor == "fixture" {
		return agent.ConnectionInfo{ResolvedModel: "fixture", Identity: "fixture"}, nil
	}
	return a.Provider.InspectExecutionConnection(ctx, executor, selected, thinking)
}

// Opt in with a JSON array of profiles. Only the configured native model is
// real: all application data and the handoff recipient are isolated fixtures.
func TestLiveCoordinatorProfiles(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_COORDINATORS") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_COORDINATORS=1 for real-provider coordinator checks")
	}
	var profiles []liveCoordinatorProfile
	if err := json.Unmarshal([]byte(os.Getenv("COCOGEM_LIVE_COORDINATOR_PROFILES")), &profiles); err != nil || len(profiles) == 0 {
		t.Fatal("COCOGEM_LIVE_COORDINATOR_PROFILES must be a nonempty JSON array")
	}
	phase := os.Getenv("COCOGEM_LIVE_COORDINATOR_PHASE")
	if phase != "" && phase != "handoff" {
		t.Fatal("COCOGEM_LIVE_COORDINATOR_PHASE must be empty or handoff")
	}
	for _, profile := range profiles {
		if profile.Name == "" || profile.Executor == "" || profile.Model == "" {
			t.Fatal("each coordinator profile needs a name, executor and model")
		}
		t.Run(profile.Name, func(t *testing.T) {
			t.Parallel()
			checkLiveCoordinator(t, profile)
		})
	}
}

func checkLiveCoordinator(t *testing.T, profile liveCoordinatorProfile) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Minute)
	defer cancel()
	area := t.TempDir()
	root := filepath.Join(area, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(area, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := w.Workspace{ID: w.NewID(), Name: "isolated coordinator check", Root: root, Revision: 1}
	coordinator := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: profile.Name, Handle: "coordinator", Executor: profile.Executor, Model: profile.Model, ThinkingLevel: profile.Thinking, RoleDescription: profile.Description, Capabilities: profile.Capabilities, Revision: 1}
	worker := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "验收助手", Handle: "fixture-helper", RoleDescription: "只回复给定的验收标记", Executor: "fixture", Model: "fixture", Revision: 1}
	thread := func(title string, members []string) w.Thread {
		return w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: title, AgentIDs: members, CoordinatorID: coordinator.ID, Workflow: "free", Revision: 1, UpdatedAt: w.Now()}
	}
	chatThread := thread("isolated chat", []string{coordinator.ID})
	taskThread := thread("isolated task", []string{coordinator.ID})
	handoffThread := thread("isolated handoff", []string{coordinator.ID, worker.ID})
	writeThread := thread("isolated file", []string{coordinator.ID})
	if err := db.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, member := range []w.Agent{coordinator, worker} {
			if err := tx.SaveAgent(member); err != nil {
				return err
			}
		}
		for _, th := range []w.Thread{chatThread, taskThread, handoffThread, writeThread} {
			if err := tx.SaveThread(th); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	provider := nativecli.Provider{}
	engine := &workflow.Engine{
		Store: db, Model: provider, Runners: map[string]agent.Runner{profile.Executor: provider, "fixture": liveCoordinatorWorker{}},
		Adapters: liveCoordinatorAdapters{provider}, AllowedRoots: []string{root}, AllowedModels: []string{profile.Model},
		Web: tools.NewWeb(""), Executions: execution.New(db, filepath.Join(area, "executions"), "coordinator-check"),
		NativeSessionRoot: filepath.Join(area, "sessions"), Limits: workflow.Limits{FirstResponse: 90 * time.Second, Stall: 90 * time.Second, StallAction: "stop"},
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	api := newDeliveryAPIWithModel(t, area, db, engine, provider)
	var settings w.Settings
	api.call("GET", "/api/v1/settings", nil, nil, 200, &settings)
	if settings.ExecutionMode != w.ExecutionProjectAutonomous {
		t.Fatalf("expected project-autonomous execution, got %q", settings.ExecutionMode)
	}
	wait := func(run w.Run, phase string) []w.Step {
		t.Helper()
		for {
			var view RunView
			api.call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
			if w.Terminal(view.Run.Status) {
				if view.Run.Status != "succeeded" {
					t.Fatalf("%s failed: kind=%s error=%s", phase, view.Run.FailureKind, view.Run.Error)
				}
				var messages []w.Message
				if err := db.Read(ctx, func(tx *store.Tx) error { var err error; messages, err = tx.Messages(view.Run.ThreadID); return err }); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, message := range messages {
					if message.RunID == run.ID && message.AgentID == coordinator.ID && message.Role == "assistant" && strings.Contains(message.Content, "COCOGEM_COORDINATOR_42") {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s did not return the requested synthetic answer", phase)
				}
				t.Logf("%s passed", phase)
				return view.Steps
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s timed out: %v", phase, ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
	if os.Getenv("COCOGEM_LIVE_COORDINATOR_PHASE") != "handoff" {
		input := w.RunInput{Message: "隔离验收：只回复 COCOGEM_COORDINATOR_42，不使用工具、不创建任务。", Workflow: "free", ResearchScope: "given_sources"}
		var preview RoutingPreview
		api.call("POST", "/api/v1/threads/"+chatThread.ID+"/routing-preview", input, nil, 200, &preview)
		if preview.ProposalID == "" {
			t.Fatal("chat preview did not produce a proposal")
		}
		input.ProposalID = preview.ProposalID
		var chat w.Run
		api.call("POST", "/api/v1/threads/"+chatThread.ID+"/runs", input, map[string]string{"Idempotency-Key": w.NewID()}, 202, &chat)
		wait(chat, "chat")
	}
	startTask := func(th w.Thread, title, goal string) w.Run {
		t.Helper()
		var task project.TaskView
		api.call("POST", "/api/v1/workspaces/"+ws.ID+"/tasks", project.CreateTaskInput{RequestID: w.NewID(), ThreadID: th.ID, OwnerMemberID: coordinator.ID, Title: title, Goal: goal}, nil, 200, &task)
		var run w.Run
		api.call("POST", "/api/v1/tasks/"+task.Task.ID+"/start", map[string]any{}, map[string]string{"Idempotency-Key": w.NewID()}, 202, &run)
		return run
	}
	if os.Getenv("COCOGEM_LIVE_COORDINATOR_PHASE") != "handoff" {
		wait(startTask(taskThread, "隔离任务", "这是隔离的协调者验收任务。只回复 COCOGEM_COORDINATOR_42，不调用工具、不读写文件、不执行命令、不联网、不委派工作。"), "project task")
	}
	steps := wait(startTask(handoffThread, "隔离交接", "这是隔离的协调者交接验收。使用 handoff_task 把任务‘只回复 COCOGEM_COORDINATOR_42’交给验收助手。结束本次调用，等待对方结果后再汇报标记。不要选择内置流程，不读写文件，不执行命令，不访问网络。"), "handoff")
	var handedOff, resumed bool
	continuationID := ""
	for _, step := range steps {
		handedOff = handedOff || step.Kind == "handoff" && step.AgentID == worker.ID && step.Status == "succeeded"
		if step.Kind == "continuation" && step.AgentID == coordinator.ID && step.Status == "succeeded" {
			resumed, continuationID = true, step.ID
		}
	}
	if !handedOff || !resumed {
		t.Fatalf("coordinator did not hand off and resume: handoff=%v continuation=%v", handedOff, resumed)
	}
	var handoffMessages []w.Message
	if err := db.Read(ctx, func(tx *store.Tx) error {
		var err error
		handoffMessages, err = tx.Messages(handoffThread.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var reported bool
	for _, message := range handoffMessages {
		reported = reported || message.StepID == continuationID && message.AgentID == coordinator.ID && strings.Contains(message.Content, "COCOGEM_COORDINATOR_42")
	}
	if !reported {
		t.Fatal("coordinator continuation did not report the handoff result")
	}
	if os.Getenv("COCOGEM_LIVE_COORDINATOR_PHASE") == "handoff" {
		return
	}
	fixture := filepath.Join(root, "fixture.txt")
	if err := os.WriteFile(fixture, []byte("VALUE=OLD\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wait(startTask(writeThread, "隔离文件修改", "这是临时项目验收。把项目目录的 fixture.txt 中 VALUE=OLD 改为 VALUE=NEW，其他字节保持不变。只改这个文件，不执行命令，不访问网络。完成后回复 COCOGEM_COORDINATOR_42。"), "project file change")
	data, err := os.ReadFile(fixture)
	if err != nil || string(data) != "VALUE=NEW\n" {
		t.Fatalf("project file did not match the requested bytes: %q, %v", data, err)
	}
}

// Opt in with the same profiles JSON as the coordinator check. Each profile is
// named only inside a synthetic failure record, so the message must stay
// routable without assigning that member: a background mention can never block
// the send or dispatch the wrong work.
func TestLiveRoutingKeepsFailureRecordsOutOfDispatch(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_COORDINATORS") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_COORDINATORS=1 for real-provider routing checks")
	}
	var profiles []liveCoordinatorProfile
	if err := json.Unmarshal([]byte(os.Getenv("COCOGEM_LIVE_COORDINATOR_PROFILES")), &profiles); err != nil || len(profiles) == 0 {
		t.Fatal("COCOGEM_LIVE_COORDINATOR_PROFILES must be a nonempty JSON array")
	}
	for _, profile := range profiles {
		if profile.Name == "" || profile.Executor == "" || profile.Model == "" {
			t.Fatal("each routing profile needs a name, executor and model")
		}
		t.Run(profile.Name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			target := w.Agent{ID: "target", DisplayName: profile.Name, Handle: "target", Executor: profile.Executor, Model: profile.Model, ThinkingLevel: profile.Thinking}
			coordinator := w.Agent{ID: "coordinator", DisplayName: "验收助手", Handle: "fixture-helper", Executor: "fixture", Model: "fixture"}
			message := "帮我修一下这个 bug。\n失败成员是" + profile.Name + "，executor=" + profile.Executor + "，model=fixture/model。\n不要修改其他目录。"
			got, err := workflow.ResolveRouting(ctx, nativecli.Provider{}, w.RunInput{Message: message}, w.Thread{CoordinatorID: coordinator.ID, AgentIDs: []string{target.ID, coordinator.ID}}, []w.Agent{target, coordinator})
			if err != nil {
				t.Fatalf("routing failed: %v", err)
			}
			if got.Decision == "assign" && slices.Contains(got.Targets, target.ID) {
				t.Fatalf("the failure-record member was assigned work: %+v", got)
			}
			t.Logf("decision=%s workflow=%s targets=%v explanation=%q", got.Decision, got.Workflow, got.Targets, got.Explanation)
		})
	}
}
