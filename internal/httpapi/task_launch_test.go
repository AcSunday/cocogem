package httpapi

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestSavedTaskLaunchCreatesExactlyOneConversationAndRun(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := newDeliveryAPI(t, root, db, nil)
	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "任务驱动"}, nil, 200, &ws)
	input := project.CreateTaskInput{RequestID: "create-task-stable", Title: "调查缓存", Goal: "对比缓存方案并交付报告"}
	var saved, replayed project.TaskView
	raw := api.call("POST", "/api/v1/workspaces/"+ws.ID+"/tasks", input, nil, 200, &saved)
	validateResponse(t, "/api/v1/workspaces/{id}/tasks", "post", 200, raw)
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/tasks", input, nil, 200, &replayed)
	if saved.Task.ID != replayed.Task.ID || saved.Task.ThreadID != "" || saved.Task.Status != "ready" {
		t.Fatalf("saved tasks: %+v %+v", saved.Task, replayed.Task)
	}
	input.Goal = "changed goal"
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/tasks", input, nil, 409, nil)
	var threads Page[w.Thread]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/threads", nil, nil, 200, &threads)
	if len(threads.Items) != 0 {
		t.Fatal("saving a task created a conversation")
	}
	var first, second w.Run
	raw = api.call("POST", "/api/v1/tasks/"+saved.Task.ID+"/start", map[string]any{}, nil, 202, &first)
	validateResponse(t, "/api/v1/tasks/{id}/start", "post", 202, raw)
	api.call("POST", "/api/v1/tasks/"+saved.Task.ID+"/start", map[string]any{}, nil, 202, &second)
	if first.ID != second.ID || first.ThreadID == "" || first.TaskID != saved.Task.ID || first.TaskRevision != 1 {
		t.Fatalf("launch replay: %+v %+v", first, second)
	}
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/threads", nil, nil, 200, &threads)
	if len(threads.Items) != 1 || threads.Items[0].Title != saved.Task.Title || threads.Items[0].TaskID != saved.Task.ID {
		t.Fatalf("conversation: %+v", threads.Items)
	}
	var child project.TaskView
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/tasks", project.CreateTaskInput{RequestID: "child-stable", ParentID: saved.Task.ID, Title: "读取基准", Goal: "独立收集缓存基准"}, nil, 200, &child)
	if child.Task.ThreadID != "" || child.Budget.ID != saved.Budget.ID || child.Task.RootTaskID != saved.Task.ID {
		t.Fatalf("child scope: %+v", child)
	}
	var childRun w.Run
	api.call("POST", "/api/v1/tasks/"+child.Task.ID+"/start", map[string]any{}, nil, 202, &childRun)
	if childRun.ThreadID == first.ThreadID || childRun.BudgetAccountID != first.BudgetAccountID {
		t.Fatalf("child launch: %+v", childRun)
	}
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/threads", nil, nil, 200, &threads)
	if len(threads.Items) != 1 {
		t.Fatal("internal child conversations polluted ordinary navigation")
	}
}

func TestDeletingWorkspaceAlsoRemovesUnstartedTasks(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := newDeliveryAPI(t, root, db, nil)
	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "待办清理"}, nil, 200, &ws)
	var task project.TaskView
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/tasks", project.CreateTaskInput{RequestID: "unstarted", Goal: "尚未开始的任务"}, nil, 200, &task)
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.DeleteWorkspace(ws.ID) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		items, err := tx.AllTasks()
		if len(items) != 0 {
			t.Fatal("unstarted task survived deletion")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskLaunchPrioritizesExplicitRecipientOverConversationWorkflow(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := newDeliveryAPI(t, root, db, nil)
	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "指派任务"}, nil, 200, &ws)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	member := members.Items[0]
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "文档比较", Workflow: "research", AgentIDs: []string{member.ID}, CoordinatorID: member.ID}, nil, 200, &thread)
	var saved project.TaskView
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/tasks", project.CreateTaskInput{RequestID: "explicit-task", ThreadID: thread.ID, Goal: "@" + member.Handle + " 对比两份文档的变化"}, nil, 200, &saved)
	var run w.Run
	api.call("POST", "/api/v1/tasks/"+saved.Task.ID+"/start", map[string]any{}, nil, 202, &run)
	if run.Input.Workflow != "free" || !slices.Equal(run.Input.Targets, []string{member.ID}) || len(run.Input.Assignments) != 0 {
		t.Fatalf("task ignored its explicit recipient: %+v", run.Input)
	}
}
