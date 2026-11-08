package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestTaskThreadMigrationPreservesOldIdentityAndHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(schema, "parent_id TEXT, thread_id TEXT REFERENCES threads(id)", "parent_id TEXT, thread_id TEXT NOT NULL REFERENCES threads(id)", 1)
	if _, err := legacy.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	before := &Store{db: legacy, changed: make(chan struct{})}
	task := w.Task{ID: "old-task", WorkspaceID: "ws", ThreadID: "thread", CurrentRevision: 1, RootTaskID: "old-task", BudgetAccountID: "budget", Status: "completed", UpdatedAt: w.Now()}
	revision := w.TaskRevision{TaskID: task.ID, Revision: 1, Goal: "旧目标不可变", Digest: "original-digest"}
	run := w.Run{ID: "old-run", WorkspaceID: "ws", ThreadID: "thread", TaskID: task.ID, Status: "succeeded", CreatedAt: w.Now()}
	if err := before.Write(t.Context(), func(tx *Tx) error {
		if err := tx.SaveWorkspace(w.Workspace{ID: "ws", Name: "已有空间", Revision: 1}); err != nil {
			return err
		}
		if err := tx.SaveThread(w.Thread{ID: "thread", WorkspaceID: "ws", TaskID: task.ID, Revision: 1}); err != nil {
			return err
		}
		if err := tx.InsertTask(task); err != nil {
			return err
		}
		if err := tx.InsertTaskRevision(revision); err != nil {
			return err
		}
		if err := tx.InsertBudgetAccount(w.BudgetAccount{ID: "budget", RootTaskID: task.ID, Revision: 1, UsedContinuations: 7}); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "old-key", "old-hash"); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: "old-message", ThreadID: "thread", RunID: run.ID, Role: "assistant", Status: "completed", Content: "历史正文保持原样"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := before.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if err := after.Write(t.Context(), func(tx *Tx) error {
		got, err := tx.Task(task.ID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, task) {
			t.Fatalf("task changed: %+v", got)
		}
		old, err := tx.TaskRevision(task.ID, 1)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(old, revision) {
			t.Fatal("immutable goal changed")
		}
		message, err := tx.Message("old-message")
		if err != nil {
			return err
		}
		if message.Content != "历史正文保持原样" {
			t.Fatal("history changed")
		}
		account, err := tx.BudgetAccount("budget")
		if err != nil {
			return err
		}
		if account.UsedContinuations != 7 {
			t.Fatal("budget reset")
		}
		return tx.InsertTask(w.Task{ID: "new-unstarted", WorkspaceID: "ws", CurrentRevision: 1, RootTaskID: "new-unstarted", BudgetAccountID: "new-budget", Status: "ready"})
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := after.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke a reference")
	}
}
