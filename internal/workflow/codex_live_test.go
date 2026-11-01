package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Uses the existing native connection with synthetic members and a temporary
// database. No user conversation is modified or replayed.
func TestLiveCodexCoordinatorHandoff(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_CODEX_COORDINATOR") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_CODEX_COORDINATOR=1 to use the configured Codex provider")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	model := os.Getenv("COCOGEM_LIVE_CODEX_MODEL")
	if model == "" {
		model = "default"
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ws := w.Workspace{ID: w.NewID(), Name: "Synthetic Codex handoff", Revision: 1}
	agents := []w.Agent{}
	for index, name := range []string{"协调成员", "答题成员"} {
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: "codex-check-" + string(rune('a'+index)), Executor: "codex", Model: model, Tools: []string{}, Revision: 1})
	}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: []string{agents[0].ID, agents[1].ID}, CoordinatorID: agents[0].ID, Revision: 1, UpdatedAt: w.Now()}
	in := w.RunInput{Workflow: "free", ResearchScope: "given_sources", Message: "这是一轮合成的协调交接验收。请协调成员先使用 CLI 的 list_mcp_resources 检查 server=cocogem 的资源（预期没有资源接口），再尝试 read_mcp_resource，server=cocogem、uri=cocogem://not-present（预期拒绝）。收到没有资源的反馈后继续，使用 read_skill 读取 task-handoff，再用 handoff_task 把‘只回复验收数字42’交给答题成员。不要直接替答题成员回答，不选择内置流程、不发布产物，不联网、不读写文件。交接后结束当前调用；平台回传后，由协调成员用一句话总结已收到42。"}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Agents: agents, CoordinatorID: agents[0].ID, Input: in, Status: "queued", CreatedAt: w.Now(), Routing: w.Routing{Decision: "discuss", Workflow: "free", Targets: []string{agents[0].ID}}, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
	planInput := in
	planInput.Targets = []string{agents[0].ID}
	steps, err := Plan(run.ID, planInput, agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, member := range agents {
			if err := tx.SaveAgent(member); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), run.ID); err != nil {
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
	provider := nativecli.Provider{}
	e := Engine{Store: s, Model: provider, Runners: map[string]agent.Runner{"codex": provider}, Adapters: provider, AllowedModels: []string{model}, Limits: Limits{FirstResponse: 2 * time.Minute, Stall: 3 * time.Minute, StallAction: "stop"}}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	for {
		changed := s.Changed()
		current, err := e.currentRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if w.Terminal(current.Status) {
			logRunSummary(t, s, run)
			if current.Status != "succeeded" {
				t.Fatalf("Codex coordinator failed: %s", current.Error)
			}
			if current.Delivery == nil || len(current.Delivery.MessageIDs) == 0 {
				t.Fatal("missing final delivery")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-changed:
		}
	}
	if err := s.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		var handoffs, continuations int
		for _, step := range steps {
			if step.Status != "succeeded" {
				t.Errorf("unsettled step: %s %s", step.Kind, step.Status)
			}
			if step.Kind == "handoff" {
				handoffs++
			}
			if step.Kind == "continuation" {
				continuations++
			}
		}
		if handoffs != 1 || continuations != 1 {
			t.Errorf("handoffs=%d continuations=%d", handoffs, continuations)
		}
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		notices := 0
		for _, event := range events {
			var data struct {
				Kind string `json:"kind"`
			}
			if event.Type == "native_notice" && json.Unmarshal(event.Data, &data) == nil && data.Kind == "codex.resource_unavailable" {
				notices++
			}
		}
		if notices < 2 {
			t.Errorf("resource helpers were not exercised: %d notices", notices)
		}
		t.Logf("verified real Codex coordinator: handoffs=%d continuations=%d resource_notices=%d", handoffs, continuations, notices)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
