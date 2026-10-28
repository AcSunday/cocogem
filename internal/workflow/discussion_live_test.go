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

// logRunSummary records what the coordinator actually saw and did. It prints
// persisted collaboration state only, never environment or credentials.
func logRunSummary(t *testing.T, s *store.Store, run w.Run) {
	t.Helper()
	_ = s.Read(context.Background(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		t.Logf("run input_workflow=%q routing=%s/%s selected=%+v method=%q delivery=%+v", current.Input.Workflow, current.Routing.Decision, current.Routing.Workflow, current.SelectedWorkflow, current.Method, current.Delivery)
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			t.Logf("step role=%s kind=%s status=%s agent=%s error=%s", step.Role, step.Kind, step.Status, step.AgentName, clipRunes(step.Error, 300))
		}
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Type != "tool_finished" && event.Type != "workflow_selected" {
				continue
			}
			var data struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Output string `json:"output"`
			}
			_ = json.Unmarshal(event.Data, &data)
			if event.Type == "tool_finished" && data.Name != "choose_workflow" && data.Name != "handoff_task" {
				continue
			}
			t.Logf("event %s name=%s status=%s detail=%s", event.Type, data.Name, data.Status, clipRunes(data.Output, 500))
		}
		messages, err := tx.Messages(run.ThreadID)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.RunID == run.ID && message.Role == "assistant" {
				t.Logf("assistant (%s): %s", message.AgentName, clipRunes(message.Content, 1200))
			}
		}
		return nil
	})
}

func clipRunes(value string, limit int) string {
	if runes := []rune(value); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

// This opt-in test sends only a synthetic question to the existing CLI models.
func TestLiveDiscussionAndShortReply(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_DISCUSSION") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_DISCUSSION=1 to use configured native models")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	provider := nativecli.Provider{}
	for _, cli := range []string{"claude", "gemini"} {
		info, err := provider.Check(ctx, cli)
		if err != nil || !info.Installed || !info.Supported {
			t.Fatalf("%s cannot run the real discussion: %v", cli, err)
		}
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ws := w.Workspace{ID: w.NewID(), Name: "Synthetic live discussion", Revision: 1}
	agents := []w.Agent{
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "方案成员", Handle: "live-option", Executor: "claude", Model: "default", Tools: []string{"publish_artifact"}, Revision: 1},
		{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "质询成员", Handle: "live-challenge", Executor: "gemini", Model: "default", Tools: []string{}, Revision: 1},
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: []string{agents[0].ID, agents[1].ID}, CoordinatorID: agents[0].ID, Revision: 1, UpdatedAt: w.Now()}
	seed := func(in w.RunInput) w.Run {
		t.Helper()
		run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: th.ID, Workspace: ws, Agents: agents, CoordinatorID: agents[0].ID, Input: in, Status: "queued", CreatedAt: w.Now(), Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
		if in.Workflow == "free" && len(in.Targets) == 0 {
			run.Routing = w.Routing{Decision: "discuss", Workflow: "free", Targets: []string{agents[0].ID}}
		}
		planInput := in
		if len(planInput.Targets) == 0 && planInput.Workflow == "free" {
			planInput.Targets = []string{agents[0].ID}
		}
		steps, err := Plan(run.ID, planInput, agents)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Write(ctx, func(tx *store.Tx) error {
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
		return run
	}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": provider, "gemini": provider}, Adapters: provider, AllowedModels: []string{"default"}}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	await := func(run w.Run) {
		t.Helper()
		for {
			current, err := e.currentRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if w.Terminal(current.Status) {
				if current.Status != "succeeded" {
					logRunSummary(t, s, run)
					t.Fatalf("%s failed: %s", run.Input.Workflow, current.Error)
				}
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
	}
	// The production Composer sends free with no explicit targets, so default
	// routing hands the task to the coordinator while choose_workflow stays
	// available; a simple game must still be answered directly in one short
	// reply with no artifact.
	quick := seed(w.RunInput{Message: "玩一局石头剪刀布。我出石头，你固定出剪刀，只回答：你出剪刀，我赢。不要分析、不要委派、不发产物。", Workflow: "free", ResearchScope: "given_sources"})
	await(quick)
	if err := s.Read(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(quick.ID)
		if err != nil {
			return err
		}
		if current.SelectedWorkflow != nil || current.Method != "direct" {
			t.Fatalf("quick game selected=%+v method=%q, want default direct routing", current.SelectedWorkflow, current.Method)
		}
		if current.Delivery == nil || len(current.Delivery.MessageIDs) != 1 || len(current.Delivery.ArtifactRefs) != 0 {
			t.Fatalf("quick delivery = %+v, want one final message and no artifacts", current.Delivery)
		}
		steps, err := tx.Steps(quick.ID)
		if err != nil {
			return err
		}
		if len(steps) != 1 {
			t.Fatalf("quick game ran %d steps, want exactly one direct step", len(steps))
		}
		messages, err := tx.Messages(th.ID)
		if err != nil {
			return err
		}
		replies := 0
		for _, message := range messages {
			if message.RunID != quick.ID || message.Role != "assistant" {
				continue
			}
			replies++
			t.Logf("quick reply: %s", message.Content)
			if message.ID != current.Delivery.MessageIDs[0] {
				t.Fatalf("delivered message %s is not the only assistant reply %s", current.Delivery.MessageIDs[0], message.ID)
			}
			if len([]rune(message.Content)) > 100 {
				t.Fatalf("simple game produced %d runes, want <=100", len([]rune(message.Content)))
			}
		}
		if replies != 1 {
			t.Fatalf("quick game produced %d assistant replies, want exactly one", replies)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("COCOGEM_LIVE_QUICK_ONLY") == "1" {
		return
	}
	in := w.RunInput{Message: "仅讨论合成场景：本地单用户待办工具，数据量很小，离线优先，应选SQLite还是PostgreSQL？每次观点、质询、草案最多两句话，复查严格遵守系统JSON格式，最终笔记不超过150个汉字。无需联网或文件资料；不要把一致意见当验证。", Workflow: "discussion", Targets: th.AgentIDs, Assignments: map[string]string{"coordinator": agents[0].ID}, ResearchScope: "given_sources"}
	automatic := os.Getenv("COCOGEM_LIVE_COORDINATOR_SELECTION") == "1"
	if automatic {
		in.Workflow, in.Targets, in.Assignments = "free", nil, nil
		in.Message = "请协调者先调用 choose_workflow 选择内置讨论方法：让两位成员先独立思考、轮流挑刺、草案和原作者复查，最后交付，避免仅委派一份意见。" + in.Message
	}
	discussion := seed(in)
	await(discussion)
	logRunSummary(t, s, discussion)
	if err := s.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(discussion.ID)
		if err != nil {
			return err
		}
		wantSteps := 8
		if automatic {
			wantSteps = 9
			current, err := tx.Run(discussion.ID)
			if err != nil {
				return err
			}
			if current.SelectedWorkflow == nil || current.SelectedWorkflow.Workflow != "discussion" || current.Input.Workflow != "free" {
				t.Fatalf("coordinator did not persist a discussion selection: %+v", current.SelectedWorkflow)
			}
		}
		if len(steps) != wantSteps {
			t.Fatalf("discussion ran %d steps, want %d", len(steps), wantSteps)
		}
		messages, err := tx.Messages(th.ID)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.RunID == discussion.ID {
				t.Logf("%s (%s): %s", message.AgentName, message.ResolvedModel, message.Content)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
