package project

import (
	"errors"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestPermanentDeleteTasksRemovesOwnedTreeAndConversation(t *testing.T) {
	db, service, th := setup(t)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveAgent(w.Agent{
			ID: "owner", WorkspaceID: th.WorkspaceID, DisplayName: "Owner",
			Handle: "owner", Model: "default", Tools: []string{"read", "write"}, Revision: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	root := create(t, service, th)
	child, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ParentID: root.Task.ID, OwnerMemberID: "owner",
		Goal: "完成子任务", AllowedActions: []string{"read", "write"},
		SourceMessageID: "child-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AskQuestion(t.Context(), root.Task.ID, "task", "选择交付格式", []string{"摘要", "完整报告"}, "摘要"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitInbox(t.Context(), child.Task.ID, SubmitInboxInput{
		SourceMessageID: "inbox-source", ExpectedRevision: child.Task.CurrentRevision,
		Kind: "supplement", Body: "补充子任务说明", ApplicationPolicy: "next_boundary",
		IdempotencyKey: "inbox-delete-test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddDependency(t.Context(), w.TaskDependency{
		ProducerTaskID: child.Task.ID, ConsumerTaskID: root.Task.ID, Mode: "pinned",
	}); err != nil {
		t.Fatal(err)
	}
	criterion := root.Revision.Acceptance[0]
	if _, err := service.RecordHostCompletion(t.Context(), root.Task.ID, RecordCompletionInput{
		EvaluationGeneration: root.Task.EvaluationGeneration,
		InputSnapshotDigest:  root.Revision.Digest,
		SubjectDigest:        "subject-delete-test",
		Results: []w.CriterionResult{{
			CriterionID: criterion.CriterionID, Result: "inconclusive",
		}},
		JudgeMemberID: "user", EvidenceProducer: "host_observed",
	}); err != nil {
		t.Fatal(err)
	}

	run := w.Run{
		ID: "delete-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID,
		TaskID: root.Task.ID, Status: "succeeded", CreatedAt: w.Now(),
		Input: w.RunInput{Message: "已完成", Workflow: "free"},
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, "delete-run-key", "delete-run-hash"); err != nil {
			return err
		}
		if err := tx.SaveArtifact(w.Artifact{
			ID: "delete-artifact", Version: 1, ThreadID: th.ID, RunID: run.ID,
			Revision: 1, Type: "note", Title: "临时产物", Body: "正文",
			CreatedAt: w.Now(),
		}); err != nil {
			return err
		}
		return tx.SaveBootstrapReceipt("delete-bootstrap", "digest", th.WorkspaceID, th.ID)
	}); err != nil {
		t.Fatal(err)
	}

	deletion, err := service.PermanentDeleteTasks(t.Context(), []string{root.Task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if deletion.Count != 2 || !slices.Contains(deletion.Deleted, root.Task.ID) || !slices.Contains(deletion.Deleted, child.Task.ID) {
		t.Fatalf("deletion = %+v", deletion)
	}
	if !slices.Contains(deletion.ThreadIDs, th.ID) {
		t.Fatalf("deleted threads = %v", deletion.ThreadIDs)
	}
	for _, id := range []string{root.Task.ID, child.Task.ID} {
		if _, err := service.Task(t.Context(), id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("task %s survived: %v", id, err)
		}
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.ThreadAny(th.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("thread survived: %v", err)
		}
		if _, err := tx.BudgetAccount(root.Budget.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("budget survived: %v", err)
		}
		if _, err := tx.Artifact("delete-artifact", 1); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("artifact survived: %v", err)
		}
		if _, err := tx.Run(run.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("run survived: %v", err)
		}
		questions, err := tx.Questions(root.Task.ID)
		if err != nil || len(questions) != 0 {
			t.Fatalf("questions survived: %v %v", questions, err)
		}
		inbox, err := tx.Inbox(child.Task.ID)
		if err != nil || len(inbox) != 0 {
			t.Fatalf("inbox survived: %v %v", inbox, err)
		}
		dependencies, err := tx.Dependencies(th.WorkspaceID)
		if err != nil || len(dependencies) != 0 {
			t.Fatalf("dependencies survived: %v %v", dependencies, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPermanentDeleteTasksRefusesActiveRunAndUnresolvedEffect(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertRun(w.Run{
			ID: "active-delete-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID,
			TaskID: created.Task.ID, Status: "running", CreatedAt: w.Now(),
		}, "active-delete-key", "active-delete-hash")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PermanentDeleteTasks(t.Context(), []string{created.Task.ID}); !IsCode(err, "conflict") {
		t.Fatalf("active run deletion error = %v", err)
	}
	if _, err := service.Task(t.Context(), created.Task.ID); err != nil {
		t.Fatalf("task was removed despite active run: %v", err)
	}

	secondThread := w.Thread{
		ID: "effect-thread", WorkspaceID: th.WorkspaceID, Title: "副作用任务",
		CoordinatorID: "owner", AgentIDs: []string{"owner"}, Revision: 1, UpdatedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveThread(secondThread)
	}); err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: secondThread.ID, OwnerMemberID: "owner",
		Goal: "等待副作用核实", AllowedActions: []string{"read"}, SourceMessageID: "effect-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveEffect(w.EffectRecord{
			ID: "uncertain-delete-effect", TaskID: second.Task.ID,
			IdempotencyKey: "uncertain-delete-key", RequestDigest: "digest",
			Status: "uncertain", CreatedAt: w.Now(), UpdatedAt: w.Now(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PermanentDeleteTasks(t.Context(), []string{second.Task.ID}); !IsCode(err, "conflict") {
		t.Fatalf("unresolved effect deletion error = %v", err)
	}
}

func TestPermanentDeleteTasksRefusesValidDependencyFromOutside(t *testing.T) {
	db, service, th := setup(t)
	producer := create(t, service, th)
	secondThread := w.Thread{
		ID: "delete-second-thread", WorkspaceID: th.WorkspaceID, Title: "第二个",
		CoordinatorID: "owner", AgentIDs: []string{"owner"}, Revision: 1, UpdatedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveThread(secondThread)
	}); err != nil {
		t.Fatal(err)
	}
	consumer, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: secondThread.ID, OwnerMemberID: "owner",
		Goal: "消费依赖", AllowedActions: []string{"read"}, SourceMessageID: "consumer-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddDependency(t.Context(), w.TaskDependency{
		ProducerTaskID: producer.Task.ID, ConsumerTaskID: consumer.Task.ID, Mode: "pinned",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PermanentDeleteTasks(t.Context(), []string{producer.Task.ID}); !IsCode(err, "conflict") {
		t.Fatalf("dependency deletion error = %v", err)
	}
	if _, err := service.Task(t.Context(), producer.Task.ID); err != nil {
		t.Fatalf("producer was removed despite dependency: %v", err)
	}
	if _, err := service.PermanentDeleteTasks(t.Context(), []string{producer.Task.ID, consumer.Task.ID}); err != nil {
		t.Fatal(err)
	}
}
