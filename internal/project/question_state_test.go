package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestAskQuestionAtomicallyMarksTheTaskWaiting is the A21 regression: a
// task-blocking question writes the attention state in the same transaction,
// and answering the last open question restores the task to ready.
func TestAskQuestionAtomicallyMarksTheTaskWaiting(t *testing.T) {
	_, service, th := setup(t)
	created := create(t, service, th)
	question, err := service.AskQuestion(t.Context(), created.Task.ID, "task", "选择交付格式", []string{"摘要", "完整"}, "摘要")
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.Status != "waiting" || view.Task.WaitingReason != "question" {
		t.Fatalf("task attention state = %+v", view.Task)
	}
	if _, err := service.AnswerQuestion(t.Context(), created.Task.ID, question.ID, "摘要", "answer-once", created.Task.CurrentRevision); err != nil {
		t.Fatal(err)
	}
	view, err = service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.Status != "ready" || view.Task.WaitingReason != "" {
		t.Fatalf("answered task did not return to ready: %+v", view.Task)
	}
	// A second open question keeps the task waiting after the first is answered.
	first, err := service.AskQuestion(t.Context(), created.Task.ID, "task", "第一个问题", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AskQuestion(t.Context(), created.Task.ID, "task", "第二个问题", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AnswerQuestion(t.Context(), created.Task.ID, first.ID, "答", "answer-two", created.Task.CurrentRevision); err != nil {
		t.Fatal(err)
	}
	view, err = service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Task.Status != "waiting" || view.Task.WaitingReason != "question" {
		t.Fatalf("a remaining question stopped waiting: %+v", view.Task)
	}
	_ = store.ErrNotFound
}

// TestParentTaskStaysRunningWhileChildrenAreActive is the A22 parent gate: a
// parent with unfinished children must not jump into review on its own.
func TestParentTaskStaysRunningWhileChildrenAreActive(t *testing.T) {
	db, service, th := setup(t)
	root := create(t, service, th)
	childThread := w.Thread{ID: "child-thread", WorkspaceID: th.WorkspaceID, Title: "child", CoordinatorID: "owner", AgentIDs: []string{"owner"}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(childThread) }); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ParentID: root.Task.ID, ThreadID: childThread.ID,
		OwnerMemberID: "owner", Goal: "child goal", AllowedActions: []string{"read"},
		SourceMessageID: "child-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	board, err := service.TaskBoard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var rootView TaskView
	for _, view := range board.Tasks {
		if view.Task.ID == root.Task.ID {
			rootView = view
		}
	}
	if rootView.ActiveChildren != 1 {
		t.Fatalf("root active children = %d", rootView.ActiveChildren)
	}
	if rootView.Task.Status == "completed" {
		t.Fatalf("a parent with an active child was already complete: %+v", rootView.Task)
	}
	// Completing the child leaves no active child.
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		task := child.Task
		task.Status = "completed"
		task.UpdatedAt = w.Now()
		return tx.SaveTask(task, task.CurrentRevision)
	}); err != nil {
		t.Fatal(err)
	}
	board, err = service.TaskBoard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range board.Tasks {
		if view.Task.ID == root.Task.ID && view.ActiveChildren != 0 {
			t.Fatalf("a completed child still counted as active: %+v", view)
		}
	}
}
