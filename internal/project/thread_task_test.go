package project

import (
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestThreadSubmissionRevisesGoalAndAppliesInboxOnce(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	inbox, err := service.SubmitInbox(t.Context(), created.Task.ID, SubmitInboxInput{
		SourceMessageID: "steering-message", ExpectedRevision: 1,
		Body: "保留 24 小时，不能新增 Redis", ApplicationPolicy: "next_boundary", IdempotencyKey: "steering-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	var revised TaskView
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Thread(th.ID)
		if err != nil {
			return err
		}
		var reviseErr error
		revised, reviseErr = service.EnsureThreadTask(tx, current, w.RunInput{Message: "实现兼容旧客户端的幂等创建"}, "new-message", "new-run")
		return reviseErr
	}); err != nil {
		t.Fatal(err)
	}
	if revised.Task.CurrentRevision != 2 || revised.Task.EvaluationGeneration != 2 {
		t.Fatalf("new submission did not revise task: %+v", revised.Task)
	}
	if !strings.Contains(revised.Revision.Goal, "兼容旧客户端") || !strings.Contains(revised.Revision.Goal, "不能新增 Redis") {
		t.Fatalf("new goal omitted current or steered requirements: %q", revised.Revision.Goal)
	}
	old, err := func() (w.TaskRevision, error) {
		var revision w.TaskRevision
		err := db.Read(t.Context(), func(tx *store.Tx) error {
			var readErr error
			revision, readErr = tx.TaskRevision(created.Task.ID, 1)
			return readErr
		})
		return revision, err
	}()
	if err != nil || old.Digest != created.Revision.Digest {
		t.Fatalf("historical revision changed: %+v, %v", old, err)
	}
	items, err := service.Inbox(t.Context(), created.Task.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox after boundary = %+v, %v", items, err)
	}
	if items[0].ID != inbox.ID || items[0].Status != "applied" || items[0].AppliedRevision != 2 || items[0].RunID != "new-run" {
		t.Fatalf("inbox was not settled at the new run boundary: %+v", items[0])
	}
}

func TestFollowUpInboxIsNotSilentlyFoldedIntoCurrentGoal(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	if _, err := service.SubmitInbox(t.Context(), created.Task.ID, SubmitInboxInput{
		SourceMessageID: "later-message", ExpectedRevision: 1, Body: "之后再评估迁移",
		ApplicationPolicy: "follow_up", IdempotencyKey: "follow-up-key",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Thread(th.ID)
		if err != nil {
			return err
		}
		_, err = service.EnsureThreadTask(tx, current, w.RunInput{Message: "完成当前幂等目标"}, "new-message", "new-run")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	view, err := service.Task(t.Context(), created.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(view.Revision.Goal, "之后再评估迁移") {
		t.Fatalf("follow-up unexpectedly changed current goal: %q", view.Revision.Goal)
	}
	items, err := service.Inbox(t.Context(), created.Task.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("follow-up inbox = %+v, %v", items, err)
	}
	item := items[0]
	if item.Status != "applied" || item.FollowUpTaskID == "" || item.FollowUpTaskID == created.Task.ID || item.RunID != "" {
		t.Fatalf("follow-up did not create an independent task receipt: %+v", item)
	}
	follow, err := service.Task(t.Context(), item.FollowUpTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if follow.Revision.Goal != "之后再评估迁移" || follow.Task.RootTaskID != follow.Task.ID || follow.Budget.ID == created.Budget.ID {
		t.Fatalf("follow-up task is not independent: %+v", follow)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		followThread, err := tx.Thread(follow.Task.ThreadID)
		if err != nil {
			return err
		}
		if !followThread.Internal || followThread.TaskID != follow.Task.ID {
			t.Fatalf("follow-up thread = %+v", followThread)
		}
		originalThread, err := tx.Thread(th.ID)
		if err != nil {
			return err
		}
		if originalThread.TaskID != created.Task.ID {
			t.Fatalf("follow-up stole the original thread task: %+v", originalThread)
		}
		runs, err := tx.Runs(followThread.ID)
		if err != nil {
			return err
		}
		if len(runs) != 0 {
			t.Fatalf("follow-up was executed instead of queued as durable work: %+v", runs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
