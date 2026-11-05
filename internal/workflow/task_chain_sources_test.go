package workflow

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestTaskChainSourcesAllowOnlyAuthorizedInternalThreads(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workspace := w.Workspace{ID: "source-workspace", Name: "sources", Revision: 1}
	rootThread := w.Thread{ID: "source-root-thread", WorkspaceID: workspace.ID, CoordinatorID: "owner", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(workspace); err != nil {
			return err
		}
		return tx.SaveThread(rootThread)
	}); err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	root, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: workspace.ID, ThreadID: rootThread.ID, OwnerMemberID: "owner",
		Goal: "integrate child evidence", SourceMessageID: "root-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	childThread := w.Thread{ID: "source-child-thread", WorkspaceID: workspace.ID, CoordinatorID: "owner", Internal: true, Revision: 1, UpdatedAt: w.Now()}
	otherThread := w.Thread{ID: "source-other-thread", WorkspaceID: workspace.ID, CoordinatorID: "owner", Internal: true, Revision: 1, UpdatedAt: w.Now()}
	plainThread := w.Thread{ID: "source-plain-thread", WorkspaceID: workspace.ID, CoordinatorID: "owner", Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		for _, thread := range []w.Thread{childThread, otherThread, plainThread} {
			if err := tx.SaveThread(thread); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: workspace.ID, ParentID: root.Task.ID, ThreadID: childThread.ID,
		OwnerMemberID: "owner", Goal: "produce child evidence", SourceMessageID: "child-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: workspace.ID, ThreadID: otherThread.ID, OwnerMemberID: "owner",
		Goal: "unrelated evidence", SourceMessageID: "other-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: workspace.ID, ParentID: root.Task.ID, ThreadID: plainThread.ID,
		OwnerMemberID: "owner", Goal: "non-internal evidence", SourceMessageID: "plain-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	childRun := w.Run{ID: "source-child-run", WorkspaceID: workspace.ID, ThreadID: childThread.ID, TaskID: child.Task.ID, Status: "succeeded", CreatedAt: w.Now()}
	otherRun := w.Run{ID: "source-other-run", WorkspaceID: workspace.ID, ThreadID: otherThread.ID, TaskID: other.Task.ID, Status: "succeeded", CreatedAt: w.Now()}
	plainRun := w.Run{ID: "source-plain-run", WorkspaceID: workspace.ID, ThreadID: plainThread.ID, TaskID: plain.Task.ID, Status: "succeeded", CreatedAt: w.Now()}
	childSource := w.Source{ID: "child-source", WorkspaceID: workspace.ID, ThreadID: childThread.ID, RunID: childRun.ID, Kind: "file", Title: "child", Content: "cross-thread original"}
	otherSource := w.Source{ID: "other-source", WorkspaceID: workspace.ID, ThreadID: otherThread.ID, RunID: otherRun.ID, Kind: "file", Title: "secret", Content: "unrelated secret"}
	plainSource := w.Source{ID: "plain-source", WorkspaceID: workspace.ID, ThreadID: plainThread.ID, RunID: plainRun.ID, Kind: "file", Title: "plain", Content: "non-internal secret"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		for _, run := range []w.Run{childRun, otherRun, plainRun} {
			if err := tx.InsertRun(run, "key-"+run.ID, "hash-"+run.ID); err != nil {
				return err
			}
		}
		for _, source := range []w.Source{childSource, otherSource, plainSource} {
			if err := tx.SaveSource(source); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	run := w.Run{ID: "source-root-run", WorkspaceID: workspace.ID, ThreadID: rootThread.ID, TaskID: root.Task.ID}
	var visible []w.Source
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		visible, err = taskChainSources(tx, run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].ID != childSource.ID {
		t.Fatalf("task-chain source snapshot = %+v", visible)
	}
	engine := Engine{Store: db}
	out, err := engine.readSource(t.Context(), run, visible, json.RawMessage(`{"source_id":"child-source","max_bytes":5}`))
	if err != nil || !strings.Contains(out, `"content":"cross"`) {
		t.Fatalf("cross-thread source read = %s, %v", out, err)
	}
	listed, err := engine.listSources(t.Context(), run, visible, json.RawMessage(`{}`))
	if err != nil || !strings.Contains(listed, childSource.ID) || strings.Contains(listed, otherSource.ID) {
		t.Fatalf("task-chain source list = %s, %v", listed, err)
	}
	for _, source := range []w.Source{otherSource, plainSource} {
		if _, err := engine.readSource(t.Context(), run, []w.Source{source}, json.RawMessage(`{"source_id":"`+source.ID+`"}`)); err == nil {
			t.Fatalf("unauthorized source %s was readable", source.ID)
		}
	}
}
