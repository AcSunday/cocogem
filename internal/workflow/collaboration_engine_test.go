package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type collaborationModel struct {
	t               *testing.T
	store           *store.Store
	run             w.Run
	mu              sync.Mutex
	parallel        int
	maxParallel     int
	failWorker      bool
	nested          bool
	parallelWorkers bool
	barrier         chan struct{}
	workersStarted  int
}

func (m *collaborationModel) Complete(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
	answer := func(text string) (model.Response, error) {
		if err := emit(text); err != nil {
			return model.Response{}, err
		}
		return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: text}}}, nil
	}
	m.mu.Lock()
	m.parallel++
	m.maxParallel = max(m.maxParallel, m.parallel)
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.parallel--; m.mu.Unlock() }()
	if len(r.Messages) > 1 && r.Messages[len(r.Messages)-1].Role == "user" && len(r.Messages[len(r.Messages)-1].Content) > 0 && r.Messages[len(r.Messages)-1].Content[0].Type == "tool_result" {
		return answer("任务已提交，等待平台回传。")
	}
	var steps []w.Step
	if err := m.store.Read(ctx, func(tx *store.Tx) error { var err error; steps, err = tx.Steps(m.run.ID); return err }); err != nil {
		return model.Response{}, err
	}
	var step w.Step
	for _, s := range steps {
		if s.Status == "running" && strings.Contains(r.System, "You are "+s.AgentName+" (") {
			step = s
			break
		}
	}
	if step.ID == "" {
		return model.Response{}, errors.New("missing current running task")
	}
	if !strings.Contains(r.Messages[0].Content[0].Text, "Runtime collaboration roster") {
		m.t.Error("runtime roster missing")
	}
	call := func(in handoffInput) model.Response {
		raw, _ := json.Marshal(in)
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: w.NewID(), Name: "handoff_task", Input: raw}}}
	}
	if step.Kind == "" {
		if m.parallelWorkers {
			first := call(handoffInput{RequestID: "parallel-a", TargetAgentID: m.run.Agents[1].ID, Task: "独立分析材料A", Role: "member"})
			second := call(handoffInput{RequestID: "parallel-b", TargetAgentID: m.run.Agents[2].ID, Task: "独立分析材料B", Role: "member"})
			first.Blocks = append(first.Blocks, second.Blocks...)
			return first, nil
		}
		return call(handoffInput{RequestID: "research", TargetAgentID: m.run.Agents[1].ID, Task: "研究给定材料并说明结论", Role: "researcher"}), nil
	}
	if step.Kind == "handoff" {
		if m.parallelWorkers {
			m.mu.Lock()
			m.workersStarted++
			if m.workersStarted == 2 {
				close(m.barrier)
			}
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return model.Response{}, ctx.Err()
			case <-m.barrier:
			}
		}
		if m.nested && step.AgentID == m.run.Agents[1].ID {
			return call(handoffInput{RequestID: "nested", TargetAgentID: m.run.Agents[2].ID, Task: "独立整理材料", Role: "member"}), nil
		}
		if m.failWorker {
			return model.Response{}, errors.New("intentional worker failure")
		}
		return answer("研究结果：材料支持结论，未调用外部来源。")
	}
	if step.Kind == "continuation" && step.AgentID == m.run.Agents[1].ID {
		if !strings.Contains(r.Messages[0].Content[0].Text, "研究结果") {
			m.t.Error("nested result missing")
		}
		return answer("嵌套结果已整合")
	}
	if step.Kind == "continuation" {
		taskInput := r.Messages[0].Content[0].Text
		if m.failWorker {
			if !strings.Contains(taskInput, "intentional worker failure") {
				m.t.Error("failure not returned")
			}
			return answer("研究任务失败，保留缺口")
		}
		if m.nested && !strings.Contains(taskInput, "嵌套结果已整合") {
			m.t.Error("outer continuation started without final nested result")
		}
		return answer("最终汇总：已读取成员结果。")
	}
	return model.Response{}, errors.New("unexpected task")
}

func seedFreeRun(t *testing.T, s *store.Store) w.Run {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "collaboration", Revision: 1}
	agents := []w.Agent{}
	for _, name := range []string{"研究协调者", "资料分析员", "独立复核员"} {
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: "member-" + string(rune('a'+len(agents))), Model: "test", Tools: tools.Names, Revision: 1})
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: []string{agents[0].ID, agents[1].ID, agents[2].ID}, CoordinatorID: agents[0].ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "queued", CreatedAt: w.Now(), Agents: agents, CoordinatorID: agents[0].ID, Input: w.RunInput{Workflow: "free", Message: "请研究给定材料并协作完成", Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}}
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, a := range agents {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "hash"); err != nil {
			return err
		}
		return tx.SaveStep(w.Step{ID: w.NewID(), RunID: run.ID, AgentID: agents[0].ID, AgentName: agents[0].DisplayName, Role: "responder", Instruction: "Complete original task using team when relevant", Status: "pending"})
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestDynamicCollaborationReturnsCommittedResults(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fail, nested bool
	}{{"return", false, false}, {"failure return", true, false}, {"nested return", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedFreeRun(t, s)
			m := &collaborationModel{t: t, store: s, run: run, failWorker: tc.fail, nested: tc.nested}
			e := Engine{Store: s, Model: m, Web: testutil.Web{}, AllowedModels: []string{"test"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := e.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); e.Wait() }()
			got := awaitRun(t, s, run.ID)
			want := "succeeded"
			if tc.fail {
				want = "failed"
			}
			if got.Status != want {
				t.Fatalf("status=%s err=%s", got.Status, got.Error)
			}
			if err := s.Read(ctx, func(tx *store.Tx) error {
				messages, err := tx.Messages(run.ThreadID)
				if err != nil {
					return err
				}
				needle := "最终汇总"
				if tc.fail {
					needle = "研究任务失败"
				}
				if !slices.ContainsFunc(messages, func(m w.Message) bool { return m.Status == "completed" && strings.Contains(m.Content, needle) }) {
					t.Fatalf("missing final callback: %+v", messages)
				}
				steps, err := tx.Steps(run.ID)
				for _, step := range steps {
					if step.Status == "pending" || step.Status == "running" {
						t.Errorf("unsettled step %+v", step)
					}
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIndependentHandoffsRunInParallel(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedFreeRun(t, s)
	m := &collaborationModel{t: t, store: s, run: run, parallelWorkers: true, barrier: make(chan struct{})}
	e := Engine{Store: s, Model: m, Web: testutil.Web{}, AllowedModels: []string{"test"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("parallel run %s: %s", got.Status, got.Error)
	}
	if m.maxParallel < 2 {
		t.Fatal("independent tasks did not overlap")
	}
}

type cancelCollaborationModel struct {
	*collaborationModel
	entered chan struct{}
}

func (m cancelCollaborationModel) Complete(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
	if strings.Contains(r.System, "Current role=researcher ") {
		close(m.entered)
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	return m.collaborationModel.Complete(ctx, r, emit)
}

func TestCancelDynamicRunStopsWorkerAndReturn(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedFreeRun(t, s)
	m := cancelCollaborationModel{collaborationModel: &collaborationModel{t: t, store: s, run: run}, entered: make(chan struct{})}
	e := Engine{Store: s, Model: m, Web: testutil.Web{}, AllowedModels: []string{"test"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	select {
	case <-m.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker not started")
	}
	if _, err := e.Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	if got.Status != "canceled" {
		t.Fatalf("run=%+v", got)
	}
	if err := s.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.Kind == "continuation" && step.Status != "skipped" {
				t.Fatalf("return executed after stop: %+v", step)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPageSelectedDirectoryUsesFileToolsWithoutStartupRoots(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		name := "page-selected"
		if restricted {
			name = "outside-host-limit"
		}
		t.Run(name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedFreeRun(t, s)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("Public page-selected directory fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			run.Workspace.Root = root
			run.Agents[0].Executor = "claude"
			if err := s.Write(t.Context(), func(tx *store.Tx) error {
				if err := tx.SaveWorkspace(run.Workspace); err != nil {
					return err
				}
				if err := tx.SaveAgent(run.Agents[0]); err != nil {
					return err
				}
				return tx.SaveRun(run)
			}); err != nil {
				t.Fatal(err)
			}
			called := false
			runner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
				called = true
				result, err := execute(ctx, "read_file", json.RawMessage(`{"path":"README.md"}`))
				if err != nil {
					return err
				}
				if !strings.Contains(result, "Public page-selected directory fixture") {
					return errors.New("bound file not read")
				}
				return observe("text", map[string]any{"text": "已读取指定目录的公开资料"})
			})
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, AllowedModels: []string{"test"}, Web: testutil.Web{}}
			if restricted {
				other, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				e.AllowedRoots = []string{other}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := e.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); e.Wait() }()
			got := awaitRun(t, s, run.ID)
			want := "succeeded"
			if restricted {
				want = "failed"
			}
			if got.Status != want {
				t.Fatalf("run %s %s", got.Status, got.Error)
			}
			if called == restricted {
				t.Fatal("dispatch failed to respect explicit host boundary")
			}
		})
	}
}
