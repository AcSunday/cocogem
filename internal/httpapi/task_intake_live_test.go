package httpapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestLiveTaskIntakeDecisions(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_TASK_INTAKE") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_TASK_INTAKE=1 to evaluate the configured native provider")
	}
	cli := os.Getenv("COCOGEM_TASK_INTAKE_CLI")
	if cli == "" {
		cli = "codex"
	}
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := nativecli.Provider{}
	engine := &workflow.Engine{Store: db, Model: provider, Runners: map[string]agent.Runner{cli: provider}, Adapters: provider, AllowedModels: []string{"default"}}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	api := newDeliveryAPI(t, root, db, engine)
	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "真实意图评测"}, nil, 200, &ws)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	member := members.Items[0]
	member.Executor = cli
	api.call("PATCH", "/api/v1/agents/"+member.ID, member, map[string]string{"If-Match": fmt.Sprint(member.Revision)}, 200, &member)
	for _, tc := range []struct {
		name, prompt string
		task         bool
	}{
		{"simple", "用一句话解释什么是幂等性。", false},
		{"negated", "只解释，不要创建任务也不要执行：修复并测试一个程序时应先检查什么？用一句话回答。", false},
		{"hypothetical", "如果以后让你编写一份报告，你会先问哪些问题？现在只讨论，不要执行。用一句话回答。", false},
		{"deliverable", "请根据以下材料整理并交付一份简短报告：简单问答与持续任务应当分开；持续任务需要目标、进度和成果。报告包含结论与两条验收要求。", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var th w.Thread
			api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: tc.name, AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Workflow: "free"}, nil, 200, &th)
			in := w.RunInput{Message: tc.prompt, Workflow: "free", ResearchScope: "given_sources"}
			var preview RoutingPreview
			api.call("POST", "/api/v1/threads/"+th.ID+"/routing-preview", in, nil, 200, &preview)
			in.ProposalID = preview.ProposalID
			var run w.Run
			api.call("POST", "/api/v1/threads/"+th.ID+"/runs", in, map[string]string{"Idempotency-Key": w.NewID()}, 202, &run)
			for {
				var view RunView
				api.call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
				if w.Terminal(view.Run.Status) {
					if view.Run.Status != "succeeded" {
						t.Fatalf("native run failed: %s", view.Run.Error)
					}
					if (view.Run.TaskID != "") != tc.task {
						t.Fatalf("task=%q want created=%v", view.Run.TaskID, tc.task)
					}
					if tc.task {
						var task project.TaskView
						api.call("GET", "/api/v1/tasks/"+view.Run.TaskID, nil, nil, 200, &task)
						if task.Task.Status == "completed" {
							t.Fatal("run success bypassed acceptance")
						}
					}
					t.Logf("real %s decision: task_created=%v, method=%s", cli, tc.task, view.Run.Method)
					return
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(400 * time.Millisecond):
				}
			}
		})
	}
}
