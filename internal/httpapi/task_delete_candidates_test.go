package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestPermanentTaskDeletionReclaimsCandidateAndIO is the A29 task-deletion arm:
// a permanently deleted durable task leaves neither its candidate worktree nor
// its file exchange area on disk, and the bound project stays untouched.
func TestPermanentTaskDeletionReclaimsCandidateAndIO(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "main.txt"), []byte("baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := &workflow.Engine{
		Store: db, Limits: workflow.DefaultLimits(),
		Executions: execution.New(db, filepath.Join(dir, "executions"), "owner"),
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Host: "local.test", AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir,
		Models: []string{"default"}, ModelConfigured: true,
	}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	auth := authCall(server, "POST", "/auth/setup", PasswordInput{Password: testPassword}, nil, "")
	if auth.Code != 200 {
		t.Fatal(auth.Body.String())
	}
	cookie := auth.Result().Cookies()[0]
	var session SessionReply
	if err := json.Unmarshal(auth.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	ws := w.Workspace{ID: "ws", Name: "task", Root: project, Revision: 1, CreatedAt: w.Now()}
	thread := w.Thread{ID: "thread", WorkspaceID: ws.ID, Title: "task", Revision: 1, UpdatedAt: w.Now()}
	now := w.Now()
	task := w.Task{
		ID: "task", WorkspaceID: ws.ID, ThreadID: thread.ID, RootTaskID: "task",
		BudgetAccountID: "budget", CurrentRevision: 1, Status: "completed",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertTask(task); err != nil {
			return err
		}
		// The ledger references the task row, so it is inserted after it.
		if err := tx.InsertBudgetAccount(w.BudgetAccount{
			ID: "budget", RootTaskID: task.ID, Revision: 1,
			Policy: w.DefaultBudgetPolicy(), UpdatedAt: now,
		}); err != nil {
			return err
		}
		return tx.InsertTaskRevision(w.TaskRevision{
			TaskID: task.ID, Revision: 1, Goal: "deliver", Digest: "digest", CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "run", WorkspaceID: ws.ID, ThreadID: thread.ID, TaskID: task.ID,
		Workspace: ws, WriteBranch: true,
	}
	candidate, err := engine.Executions.Prepare(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	ioDir, err := engine.Executions.IODirectory(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ioDir, "outputs", "report.md"), []byte("delivered"), 0600); err != nil {
		t.Fatal(err)
	}

	deleted := apiCall(server, "POST", "/api/v1/tasks/delete", TaskDeleteInput{TaskIDs: []string{task.ID}}, cookie, session.CSRFToken, nil)
	if deleted.Code != 200 {
		t.Fatalf("delete task = %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(candidate.Path); !os.IsNotExist(err) {
		t.Fatalf("candidate directory survived task deletion: %v", err)
	}
	if _, err := os.Stat(ioDir); !os.IsNotExist(err) {
		t.Fatalf("IO exchange area survived task deletion: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(project, "main.txt")); err != nil || string(data) != "baseline" {
		t.Fatalf("task deletion changed the bound project: %q %v", data, err)
	}
}
