package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// This fixture chooses a tool explicitly; semantic quality is covered by the
// separate opt-in native-provider test, not claimed by deterministic tests.
type taskIntakeRunner struct{ calls atomic.Int32 }

func (runner *taskIntakeRunner) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	runner.calls.Add(1)
	if slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "promote_to_task" }) {
		text := request.Messages[0].Content[0].Text
		if len(request.NativeTools) == 0 && !request.TrustedExecution {
			if strings.Contains(text, "按方案交付报告") {
				raw, _ := json.Marshal(map[string]string{"title": "构建交付", "goal": "按照讨论方案交付一份报告", "evidence_quote": "按方案交付报告"})
				if _, err := execute(ctx, "promote_to_task", raw); err != nil {
					return err
				}
				if _, err := execute(ctx, "publish_artifact", []byte(`{}`)); err == nil {
					return fmt.Errorf("intake continued after promotion")
				}
				return nil
			}
		} else {
			return fmt.Errorf("task-free intake received native execution")
		}
	}
	return observe("text", map[string]any{"text": "合成测试回复，未调用真实供应商。"})
}

func TestChatStaysLightweightThenPromotesInPlace(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runner := &taskIntakeRunner{}
	engine := &workflow.Engine{Store: db, Model: testutil.Model{}, Runners: map[string]agent.Runner{"claude": runner}, Executions: execution.New(db, filepath.Join(root, "executions"), "intake-fixture"), AllowedModels: []string{"default"}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	api := newDeliveryAPI(t, root, db, engine)
	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "按需任务"}, nil, 200, &ws)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	member := members.Items[0]
	member.Executor = "claude"
	api.call("PATCH", "/api/v1/agents/"+member.ID, member, map[string]string{"If-Match": fmt.Sprint(member.Revision)}, 200, &member)
	var th w.Thread
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "先讨论", AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Workflow: "free"}, nil, 200, &th)
	submit := func(message, key string) w.Run {
		in := w.RunInput{Message: message, Workflow: "free", ResearchScope: "auto"}
		var preview RoutingPreview
		api.call("POST", "/api/v1/threads/"+th.ID+"/routing-preview", in, nil, 200, &preview)
		in.ProposalID = preview.ProposalID
		var run w.Run
		api.call("POST", "/api/v1/threads/"+th.ID+"/runs", in, map[string]string{"Idempotency-Key": key}, 202, &run)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var view RunView
			api.call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
			if w.Terminal(view.Run.Status) {
				if view.Run.Status != "succeeded" {
					t.Fatalf("run: %+v", view.Run)
				}
				return view.Run
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("intake did not settle")
		return w.Run{}
	}
	first := submit("解释一下方案的取舍", "chat-first-stable-key")
	if first.TaskID != "" {
		t.Fatal("ordinary chat created a task")
	}
	var board project.TaskBoard
	api.call("GET", "/api/v1/tasks", nil, nil, 200, &board)
	if len(board.Tasks) != 0 {
		t.Fatal("chat polluted task board")
	}
	second := submit("按方案交付报告", "chat-second-stable-key")
	if second.TaskID == "" || second.ThreadID != first.ThreadID || second.TaskDirectory == "" {
		t.Fatalf("promotion: %+v", second)
	}
	api.call("GET", "/api/v1/tasks", nil, nil, 200, &board)
	if len(board.Tasks) != 1 || board.Tasks[0].Task.ThreadID != th.ID {
		t.Fatalf("board: %+v", board)
	}
	var history ThreadView
	api.call("GET", "/api/v1/threads/"+th.ID, nil, nil, 200, &history)
	if len(history.Runs) != 2 || history.Runs[0].TaskID != "" || history.Thread.TaskID != second.TaskID {
		t.Fatal("promotion rewrote historical run identity")
	}
	if runner.calls.Load() != 3 {
		t.Fatalf("unexpected classification calls: %d", runner.calls.Load())
	}
}
