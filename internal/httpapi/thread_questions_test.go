package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestThreadExposesOpenQuestionsAndEmitsYieldEvents(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := w.Workspace{ID: "ws-q", Name: "QuestionsRepo", Root: root, Revision: 1, CreatedAt: w.Now()}
	member := w.Agent{ID: "coord", WorkspaceID: ws.ID, DisplayName: "协调者", Handle: "coord", Model: "default", Revision: 1, Tools: []string{"read_file"}}
	th := w.Thread{ID: "th-q", WorkspaceID: ws.ID, Title: "带确认问题会话", AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}

	svc := project.New(db)
	created, err := svc.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: ws.ID, ThreadID: th.ID, OwnerMemberID: member.ID,
		Title: "确认框测试任务", Goal: "验证会话内问题确认", SourceMessageID: "src-msg-1", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	th.TaskID = created.Task.ID
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}

	child, err := svc.DelegateTaskFromUser(t.Context(), project.DelegateTaskInput{
		ParentTaskID: created.Task.ID, OwnerMemberID: member.ID, Goal: "子任务检查", RequestID: "del-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	engine := &workflow.Engine{Store: db, AllowedRoots: []string{root}, AllowedModels: []string{"default"}}
	api := newDeliveryAPI(t, root, db, engine)

	// Start a run on the root task, ask a question, and yield the run.
	run := w.Run{
		ID: "run-yield-q", WorkspaceID: ws.ID, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: created.Task.CurrentRevision, BudgetAccountID: created.Budget.ID,
		Agents: []w.Agent{member}, CoordinatorID: member.ID, Status: "running", CreatedAt: w.Now(),
	}
	step := w.Step{ID: "step-q", RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: "coordinator", Instruction: "check", Status: "running"}
	attempt := w.Attempt{ID: "att-q", RunID: run.ID, StepID: step.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	msg := w.Message{ID: "msg-q", ThreadID: th.ID, RunID: run.ID, StepID: step.ID, AgentID: member.ID, Role: "assistant", Content: "需要你确认", Status: "processing", CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "key-q", "digest-q"); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		return tx.SaveMessage(msg)
	}); err != nil {
		t.Fatal(err)
	}

	q1, err := svc.AskQuestion(t.Context(), created.Task.ID, "task", "是否交给狸花猫修复？", []string{"确认继续", "暂不派发"}, "确认继续")
	if err != nil {
		t.Fatal(err)
	}
	q2, err := svc.AskQuestion(t.Context(), child.Task.Task.ID, "branch", "子任务确认项", []string{"A", "B"}, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.YieldRun(t.Context(), run.ID, "event", "", "question", q1.ID, ""); err != nil {
		t.Fatal(err)
	}

	// Verify YieldRun emitted question_opened and run_finished events for live SSE clients.
	var evs []w.Event
	if err := db.Read(context.Background(), func(tx *store.Tx) error {
		var readErr error
		evs, readErr = tx.Events(run.ID, 0)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	var sawOpened, sawFinished bool
	for _, ev := range evs {
		if ev.Type == "question_opened" {
			sawOpened = true
		}
		if ev.Type == "run_finished" {
			sawFinished = true
		}
	}
	if !sawOpened || !sawFinished {
		t.Fatalf("expected YieldRun to emit question_opened & run_finished, got %+v", evs)
	}

	// 1. Thread endpoint includes both root & child task open questions.
	var tv ThreadView
	api.call("GET", "/api/v1/threads/"+th.ID, nil, nil, http.StatusOK, &tv)
	if len(tv.OpenQuestions) != 2 {
		t.Fatalf("expected 2 open questions on thread view (root + child), got %+v", tv.OpenQuestions)
	}

	// 2. Answering q1 via HTTP wakes the yielded task and drops q1 from open_questions.
	var answered w.Question
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/questions/"+q1.ID+"/answers",
		QuestionAnswerInput{Answer: "确认继续", AnswerRef: "ans-1"},
		map[string]string{"If-Match": "1"},
		http.StatusOK, &answered)
	if answered.Status != "resolved" {
		t.Fatalf("expected resolved question, got %+v", answered)
	}

	var afterTV ThreadView
	api.call("GET", "/api/v1/threads/"+th.ID, nil, nil, http.StatusOK, &afterTV)
	if len(afterTV.OpenQuestions) != 1 || afterTV.OpenQuestions[0].ID != q2.ID {
		t.Fatalf("expected only child question q2 remaining open, got %+v", afterTV.OpenQuestions)
	}

	rawJSON, _ := json.Marshal(tv)
	validateResponse(t, "/api/v1/threads/{id}", "get", http.StatusOK, rawJSON)
	_ = model.Request{}
}
