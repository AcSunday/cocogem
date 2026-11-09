package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type projectJourneyModel struct {
	blockGoal      string
	started        chan struct{}
	release        chan struct{}
	once           sync.Once
	questionGoal   string
	questionAnswer string

	mu      sync.Mutex
	systems []string
}

func (m *projectJourneyModel) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	inputText := request.System
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type == "text" {
				inputText += "\n" + block.Text
			}
		}
	}
	m.mu.Lock()
	m.systems = append(m.systems, inputText)
	m.mu.Unlock()
	if m.blockGoal != "" && strings.Contains(inputText, m.blockGoal) {
		m.once.Do(func() { close(m.started) })
		select {
		case <-m.release:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	used := map[string]bool{}
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_use" {
				used[block.Name] = true
			}
		}
	}
	tool := func(name string, input any) (model.Response, error) {
		raw, _ := json.Marshal(input)
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: "journey-" + name, Name: name, Input: raw}}}, nil
	}
	if m.questionGoal != "" && strings.Contains(inputText, m.questionGoal) && !strings.Contains(inputText, m.questionAnswer) {
		if !used["ask_question"] {
			return tool("ask_question", map[string]any{
				"blocking_scope": "task", "body": "请选择交付格式", "options": []string{"摘要", "完整报告"}, "recommended": "摘要",
			})
		}
		if err := emit("等待用户回答问题"); err != nil {
			return model.Response{}, err
		}
		return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: "等待用户回答问题"}}}, nil
	}
	for _, definition := range request.Tools {
		if definition.Name == "fetch_url" && !used["fetch_url"] {
			return tool("fetch_url", map[string]any{"url": testutil.Sources[0]})
		}
	}
	for _, definition := range request.Tools {
		if definition.Name == "publish_artifact" && !used["publish_artifact"] {
			return tool("publish_artifact", map[string]any{
				"type": "note", "title": "HTTP 验收产物", "body": "通过真实调度和宿主工具发布的成果。",
				"findings": []any{}, "source_ids": []string{}, "limitations": []string{"确定性模型，仅验证平台链路。"},
			})
		}
	}
	if err := emit("本轮执行已完成"); err != nil {
		return model.Response{}, err
	}
	return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: "本轮执行已完成"}}}, nil
}

func startProjectJourney(t *testing.T) (*deliveryAPI, *store.Store, *workflow.Engine) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	engine := &workflow.Engine{Store: db, AllowedModels: []string{"default"}, Model: &projectJourneyModel{}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(context.Background())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); engine.Wait() })
	return newDeliveryAPI(t, root, db, engine), db, engine
}

func createProjectJourneyThread(t *testing.T, api *deliveryAPI, workspaceID, title string) w.Thread {
	t.Helper()
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+workspaceID+"/agents", nil, nil, 200, &members)
	if len(members.Items) == 0 {
		t.Fatal("project journey has no member")
	}
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+workspaceID+"/threads", w.Thread{
		Title: title, AgentIDs: []string{members.Items[0].ID}, CoordinatorID: members.Items[0].ID, Workflow: "free",
	}, nil, 200, &thread)
	// These journeys exercise durable control, not automatic intent detection.
	var task project.TaskView
	api.call("POST", "/api/v1/workspaces/"+workspaceID+"/tasks", project.CreateTaskInput{ThreadID: thread.ID, OwnerMemberID: thread.CoordinatorID, Goal: title}, nil, 200, &task)
	thread.TaskID = task.Task.ID
	return thread
}

func submitProjectJourneyRun(t *testing.T, api *deliveryAPI, thread w.Thread, goal, key string) w.Run {
	t.Helper()
	input := w.RunInput{Message: goal, Workflow: "free", Targets: thread.AgentIDs, ResearchScope: "given_sources", SourceURLs: testutil.Sources}
	var preview RoutingPreview
	api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
	input.ProposalID = preview.ProposalID
	var run w.Run
	api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": key + "-project-journey"}, 202, &run)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var view RunView
		api.call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
		if view.Run.Status == "succeeded" {
			return view.Run
		}
		if w.Terminal(view.Run.Status) {
			t.Fatalf("journey run settled as %s: %s", view.Run.Status, view.Run.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("journey run did not finish: %s", run.ID)
	return w.Run{}
}

func projectJourneyTask(t *testing.T, api *deliveryAPI, threadID string) project.TaskView {
	t.Helper()
	var thread ThreadView
	api.call("GET", "/api/v1/threads/"+threadID, nil, nil, 200, &thread)
	if thread.Thread.TaskID == "" {
		t.Fatal("run did not uplift the thread to a durable task")
	}
	var view project.TaskView
	api.call("GET", "/api/v1/tasks/"+thread.Thread.TaskID, nil, nil, 200, &view)
	return view
}

func projectJourneyArtifacts(t *testing.T, api *deliveryAPI, threadID string) ([]w.Artifact, []w.Source) {
	t.Helper()
	var artifacts Page[w.Artifact]
	api.call("GET", "/api/v1/threads/"+threadID+"/artifacts", nil, nil, 200, &artifacts)
	var thread ThreadView
	api.call("GET", "/api/v1/threads/"+threadID, nil, nil, 200, &thread)
	if len(artifacts.Items) == 0 || len(thread.Sources) == 0 {
		t.Fatalf("run produced no exact artifact/source: artifacts=%d sources=%d", len(artifacts.Items), len(thread.Sources))
	}
	return artifacts.Items, thread.Sources
}

func TestProjectSteeringAndCompletionHTTPJourney(t *testing.T) {
	api, _, _ := startProjectJourney(t)
	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "项目操作验收"}, nil, 200, &workspace)
	firstThread := createProjectJourneyThread(t, api, workspace.ID, " Steering 与验收 ")
	otherThread := createProjectJourneyThread(t, api, workspace.ID, "跨任务证据")
	submitProjectJourneyRun(t, api, firstThread, "保存当前目标", "journey-initial")
	submitProjectJourneyRun(t, api, otherThread, "另一任务的成果", "journey-other")
	created := projectJourneyTask(t, api, firstThread.ID)
	other := projectJourneyTask(t, api, otherThread.ID)

	var steering w.InboxItem
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/inbox", project.SubmitInboxInput{
		SourceMessageID: "steering-source", ExpectedRevision: 1, Body: "新增约束：只应用一次",
		ApplicationPolicy: "next_boundary", IdempotencyKey: "steering-once",
	}, nil, 200, &steering)
	submitProjectJourneyRun(t, api, firstThread, "修订后的执行任务", "journey-steered")
	steered := projectJourneyTask(t, api, firstThread.ID)
	if steered.Task.CurrentRevision != 2 || strings.Count(steered.Revision.Goal, "新增约束：只应用一次") != 1 {
		t.Fatalf("steering was not applied exactly once: %+v", steered)
	}
	var inbox InboxList
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/inbox", nil, nil, 200, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].Status != "applied" || inbox.Items[0].AppliedRevision != 2 {
		t.Fatalf("steering receipt = %+v", inbox.Items)
	}

	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/inbox", project.SubmitInboxInput{
		SourceMessageID: "follow-source", ExpectedRevision: 2, Body: "独立后续任务",
		ApplicationPolicy: "follow_up", IdempotencyKey: "follow-independent",
	}, nil, 200, &steering)
	submitProjectJourneyRun(t, api, firstThread, "当前任务的新边界", "journey-follow-boundary")
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/inbox", nil, nil, 200, &inbox)
	if len(inbox.Items) != 2 || inbox.Items[1].FollowUpTaskID == "" || inbox.Items[1].FollowUpTaskID == created.Task.ID {
		t.Fatalf("follow-up receipt = %+v", inbox.Items)
	}
	var follow project.TaskView
	api.call("GET", "/api/v1/tasks/"+inbox.Items[1].FollowUpTaskID, nil, nil, 200, &follow)
	if follow.Task.RootTaskID != follow.Task.ID || follow.Budget.ID == created.Budget.ID || follow.Revision.Goal != "独立后续任务" {
		t.Fatalf("follow-up is not independent: %+v", follow)
	}

	artifacts, sources := projectJourneyArtifacts(t, api, firstThread.ID)
	otherArtifacts, _ := projectJourneyArtifacts(t, api, otherThread.ID)
	current := projectJourneyTask(t, api, firstThread.ID)
	candidate := "artifact:" + artifacts[len(artifacts)-1].ID + "@v1"
	foreign := "artifact:" + otherArtifacts[0].ID + "@v1"
	completion := project.RecordCompletionInput{
		EvaluationGeneration: current.Task.EvaluationGeneration, InputSnapshotDigest: current.Revision.Digest,
		CandidateRefs: []string{foreign}, Results: []w.CriterionResult{{CriterionID: "outcome", Result: "passed", Evidence: []string{"source:" + sources[0].ID}}},
		JudgeMemberID: "user", EvidenceProducer: "human_confirmed",
	}
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/completions", completion, nil, 400, nil)
	completion.CandidateRefs = []string{candidate}
	completion.SubjectDigest = "browser-forged-digest"
	completion.EvidenceProducer = "host_observed"
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/completions", completion, nil, 400, nil)
	// New human acceptance no longer accepts a captured source as evidence; the
	// shared source infrastructure keeps working, but old source-bearing records
	// are the only place a source reference remains readable.
	completion.EvidenceProducer = "human_confirmed"
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/completions", completion, nil, 400, nil)
	completion.Results[0].Evidence = []string{candidate}
	var record w.CompletionRecord
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/completions", completion, nil, 200, &record)
	if record.SubjectDigest == "browser-forged-digest" || record.SubjectDigest == "" {
		t.Fatalf("completion accepted a browser subject digest: %+v", record)
	}
	completed := projectJourneyTask(t, api, firstThread.ID)
	if completed.Task.Status != "completed" || other.Task.ID == completed.Task.ID {
		t.Fatalf("exact human completion did not complete only its own task: %+v", completed.Task)
	}
}

func TestProjectBudgetPauseAndResumeHTTPJourney(t *testing.T) {
	api, _, engine := startProjectJourney(t)
	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "预算与暂停验收"}, nil, 200, &workspace)
	thread := createProjectJourneyThread(t, api, workspace.ID, "共享预算")
	submitProjectJourneyRun(t, api, thread, "建立根预算账户", "budget-initial")
	root := projectJourneyTask(t, api, thread.ID)
	adjustDecimal := func(policy w.BudgetPolicy) {
		t.Helper()
		current := projectJourneyTask(t, api, thread.ID)
		api.call("PATCH", "/api/v1/tasks/"+root.Task.ID+"/budget", TaskBudgetInput{Policy: policy}, map[string]string{"If-Match": fmt.Sprint(current.Budget.Revision)}, 200, nil)
	}
	policy := root.Budget.Policy
	policy.AutoContinuations, policy.ParallelBranches, policy.WriterBranches = 2, 1, 1
	adjustDecimal(policy)

	journey := engine.Model.(*projectJourneyModel)
	journey.blockGoal = "阻塞第一项预算执行"
	journey.started, journey.release = make(chan struct{}), make(chan struct{})
	delegate := func(goal, requestID string) project.DelegationView {
		t.Helper()
		var out project.DelegationView
		api.call("POST", "/api/v1/tasks/"+root.Task.ID+"/delegations", UserDelegationInput{
			OwnerMemberID: thread.AgentIDs[0], Goal: goal, RequestID: requestID,
		}, nil, 200, &out)
		return out
	}
	first := delegate("阻塞第一项预算执行", "budget-first-delegation")
	select {
	case <-journey.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first delegated run did not start")
	}
	second := delegate("等待预算的第二项", "budget-second-delegation")
	policy.AutoContinuations = 1
	adjustDecimal(policy)
	var secondView project.TaskView
	api.call("GET", "/api/v1/tasks/"+second.Task.Task.ID, nil, nil, 200, &secondView)
	if secondView.Task.Status != "waiting" || secondView.Task.WaitingReason != "budget" || second.Run.Status != "queued" {
		t.Fatalf("lowered budget did not preserve a waiting queued run: task=%+v run=%+v", secondView.Task, second.Run)
	}
	policy.AutoContinuations = 2
	adjustDecimal(policy)
	api.call("GET", "/api/v1/tasks/"+second.Task.Task.ID, nil, nil, 200, &secondView)
	if secondView.Task.Status != "ready" || secondView.Budget.UsedContinuations != 1 || secondView.Budget.ReservedRuns != 1 {
		t.Fatalf("budget expansion reset usage or failed to restore reservation: %+v", secondView.Budget)
	}
	api.call("POST", "/api/v1/tasks/"+root.Task.ID+"/actions", TaskActionInput{Action: "pause", RequestID: "budget-pause-root"}, nil, 200, nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var firstRun RunView
		api.call("GET", "/api/v1/runs/"+first.Run.ID, nil, nil, 200, &firstRun)
		if firstRun.Run.Status == "canceled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pause did not stop the active delegated run: %+v", firstRun.Run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	api.call("GET", "/api/v1/tasks/"+second.Task.Task.ID, nil, nil, 200, &secondView)
	var queued RunView
	api.call("GET", "/api/v1/runs/"+second.Run.ID, nil, nil, 200, &queued)
	if secondView.Task.Status != "paused" || queued.Run.Status != "queued" {
		t.Fatalf("pause canceled queued work instead of preserving it: task=%+v run=%+v", secondView.Task, queued.Run)
	}
	policy.AutoContinuations = 3
	adjustDecimal(policy)
	api.call("GET", "/api/v1/tasks/"+second.Task.Task.ID, nil, nil, 200, &secondView)
	if secondView.Task.Status != "paused" {
		t.Fatalf("budget expansion bypassed pause: %+v", secondView.Task)
	}
	api.call("POST", "/api/v1/tasks/"+root.Task.ID+"/actions", TaskActionInput{Action: "resume", RequestID: "budget-resume-root"}, nil, 200, nil)
	close(journey.release)
	deadline = time.Now().Add(5 * time.Second)
	for {
		api.call("GET", "/api/v1/runs/"+second.Run.ID, nil, nil, 200, &queued)
		if queued.Run.Status == "succeeded" {
			break
		}
		if w.Terminal(queued.Run.Status) || time.Now().After(deadline) {
			t.Fatalf("resumed queued run did not continue: %+v", queued.Run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	final := projectJourneyTask(t, api, thread.ID)
	if final.Budget.UsedContinuations != 2 || final.Budget.ReservedRuns != 0 || final.Budget.Policy.AutoContinuations != 3 {
		t.Fatalf("pause/resume or budget changes reset the shared account: %+v", final.Budget)
	}
}

func TestProjectQuestionAnswerAndBudgetContinuationHTTPJourney(t *testing.T) {
	api, _, engine := startProjectJourney(t)
	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "问题续办验收"}, nil, 200, &workspace)
	thread := createProjectJourneyThread(t, api, workspace.ID, "回答问题后继续")
	journey := engine.Model.(*projectJourneyModel)
	journey.questionGoal = "需要用户选择格式的任务"
	journey.questionAnswer = "用户答案标记：完整报告"
	input := w.RunInput{Message: journey.questionGoal, Workflow: "free", Targets: thread.AgentIDs, ResearchScope: "given_sources", SourceURLs: testutil.Sources}
	var preview RoutingPreview
	api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
	input.ProposalID = preview.ProposalID
	var initial w.Run
	api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "question-initial-run"}, 202, &initial)
	var created project.TaskView
	deadline := time.Now().Add(5 * time.Second)
	for {
		created = projectJourneyTask(t, api, thread.ID)
		if created.Task.Status == "waiting" && created.Task.WaitingReason == "question" {
			break
		}
		var current RunView
		api.call("GET", "/api/v1/runs/"+initial.ID, nil, nil, 200, &current)
		if current.Run.Status == "yielded" {
			// The task and run projections commit together, but the two HTTP
			// reads can straddle that transaction. Re-read the task rather than
			// treating a durable yield as a premature terminal state.
			continue
		}
		if w.Terminal(current.Run.Status) {
			var events EventPage
			api.call("GET", "/api/v1/runs/"+initial.ID+"/event-history", nil, nil, 200, &events)
			details := []string{}
			for _, event := range events.Items {
				if event.Type == "tool_finished" {
					details = append(details, string(event.Data))
				}
			}
			t.Fatalf("question run settled before waiting: status=%s tool_results=%s", current.Run.Status, details)
		}
		if time.Now().After(deadline) {
			t.Fatalf("question did not durably pause execution: %+v", created.Task)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var yielded RunView
	api.call("GET", "/api/v1/runs/"+initial.ID, nil, nil, 200, &yielded)
	if yielded.Run.Status != "yielded" {
		t.Fatalf("question run did not yield: %+v", yielded.Run)
	}
	var questions QuestionList
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/questions", nil, nil, 200, &questions)
	if len(questions.Items) != 1 || questions.Items[0].Status != "open" || questions.Items[0].TaskRevision != 1 {
		t.Fatalf("question projection = %+v", questions.Items)
	}
	policy := created.Budget.Policy
	policy.AutoContinuations = 1
	api.call("PATCH", "/api/v1/tasks/"+created.Task.ID+"/budget", TaskBudgetInput{Policy: policy}, map[string]string{"If-Match": fmt.Sprint(created.Budget.Revision)}, 200, nil)
	// Consume the sole continuation through a real child Run before answering.
	// The subsequent answer must remain durable without bypassing this account.
	var child project.DelegationView
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/delegations", UserDelegationInput{
		OwnerMemberID: thread.AgentIDs[0], Goal: "消耗共享续办额度", RequestID: "question-budget-consumer",
	}, nil, 200, &child)
	deadline = time.Now().Add(5 * time.Second)
	for {
		var childRun RunView
		api.call("GET", "/api/v1/runs/"+child.Run.ID, nil, nil, 200, &childRun)
		if childRun.Run.Status == "succeeded" {
			break
		}
		if w.Terminal(childRun.Run.Status) || time.Now().After(deadline) {
			t.Fatalf("budget consumer did not finish: %+v", childRun.Run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	created = projectJourneyTask(t, api, thread.ID)
	if created.Budget.UsedContinuations != 1 || created.Budget.RemainingContinuations() != 0 {
		t.Fatalf("shared budget was not exhausted by real work: %+v", created.Budget)
	}
	var answered w.Question
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/questions/"+questions.Items[0].ID+"/answers", QuestionAnswerInput{
		Answer: journey.questionAnswer, AnswerRef: "question-answer-receipt",
	}, map[string]string{"If-Match": "1"}, 200, &answered)
	if answered.Status != "resolved" {
		t.Fatalf("answer was not committed: %+v", answered)
	}
	created = projectJourneyTask(t, api, thread.ID)
	if created.Task.Status != "waiting" || created.Task.WaitingReason != "budget" {
		t.Fatalf("exhausted budget hid the answered question behind a false completion: %+v", created.Task)
	}
	var wakeups WakeupList
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/wakeups", nil, nil, 200, &wakeups)
	if len(wakeups.Items) != 1 || wakeups.Items[0].ContinuationRunID == "" {
		t.Fatalf("answer did not create its exact continuation: %+v", wakeups.Items)
	}
	var continuation RunView
	api.call("GET", "/api/v1/runs/"+wakeups.Items[0].ContinuationRunID, nil, nil, 200, &continuation)
	if continuation.Run.Status != "queued" || continuation.Run.BudgetReserved {
		t.Fatalf("budget-exhausted continuation = %+v", continuation.Run)
	}
	policy.AutoContinuations = 2
	api.call("PATCH", "/api/v1/tasks/"+created.Task.ID+"/budget", TaskBudgetInput{Policy: policy}, map[string]string{"If-Match": fmt.Sprint(created.Budget.Revision)}, 200, nil)
	deadline = time.Now().Add(5 * time.Second)
	for {
		api.call("GET", "/api/v1/runs/"+continuation.Run.ID, nil, nil, 200, &continuation)
		if continuation.Run.Status == "succeeded" {
			break
		}
		if w.Terminal(continuation.Run.Status) || time.Now().After(deadline) {
			t.Fatalf("answer continuation did not resume after budget expansion: %+v", continuation.Run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	created = projectJourneyTask(t, api, thread.ID)
	if created.Budget.UsedContinuations != 2 || created.Task.Status == "waiting" {
		t.Fatalf("answer continuation did not consume the same account: %+v", created)
	}
	journey.mu.Lock()
	systems := append([]string(nil), journey.systems...)
	journey.mu.Unlock()
	if len(systems) < 3 || !strings.Contains(systems[len(systems)-1], journey.questionAnswer) || !strings.Contains(systems[len(systems)-1], answered.ID) {
		t.Fatalf("continuation context omitted the exact answer: %+v", systems)
	}
}

func TestProjectInterruptStaysPendingUntilNextBoundary(t *testing.T) {
	api, _, engine := startProjectJourney(t)
	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "打断验收"}, nil, 200, &workspace)
	thread := createProjectJourneyThread(t, api, workspace.ID, "打断并补充")
	journey := engine.Model.(*projectJourneyModel)
	journey.blockGoal = "需要打断的执行"
	journey.started, journey.release = make(chan struct{}), make(chan struct{})
	input := w.RunInput{Message: journey.blockGoal, Workflow: "free", Targets: thread.AgentIDs, ResearchScope: "given_sources", SourceURLs: testutil.Sources}
	var preview RoutingPreview
	api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
	input.ProposalID = preview.ProposalID
	var initial w.Run
	api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "interrupt-initial-run"}, 202, &initial)
	select {
	case <-journey.started:
	case <-time.After(3 * time.Second):
		t.Fatal("interruptible run did not start")
	}
	created := projectJourneyTask(t, api, thread.ID)
	var item w.InboxItem
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/inbox", project.SubmitInboxInput{
		SourceMessageID: "interrupt-source", ExpectedRevision: 1, Body: "立即采用新的约束",
		ApplicationPolicy: "interrupt", IdempotencyKey: "interrupt-once",
	}, nil, 200, &item)
	api.call("POST", "/api/v1/runs/"+initial.ID+"/cancel", nil, nil, 200, nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var current RunView
		api.call("GET", "/api/v1/runs/"+initial.ID, nil, nil, 200, &current)
		if w.Terminal(current.Run.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("interrupt did not stop the current run: %+v", current.Run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var inbox InboxList
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/inbox", nil, nil, 200, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].Status != "pending" {
		t.Fatalf("cancel was falsely reported as model comprehension: %+v", inbox.Items)
	}
	submitProjectJourneyRun(t, api, thread, "打断后的新执行边界", "interrupt-next-boundary")
	created = projectJourneyTask(t, api, thread.ID)
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/inbox", nil, nil, 200, &inbox)
	if created.Task.CurrentRevision != 2 || !strings.Contains(created.Revision.Goal, "立即采用新的约束") ||
		inbox.Items[0].Status != "applied" || inbox.Items[0].AppliedRevision != 2 {
		t.Fatalf("interrupt was not applied exactly at the next boundary: task=%+v inbox=%+v", created, inbox.Items)
	}
}

func TestProjectControlReplayAndUnchangedModeDoNotStopNewWork(t *testing.T) {
	api, _, engine := startProjectJourney(t)
	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "控制幂等验收"}, nil, 200, &workspace)
	thread := createProjectJourneyThread(t, api, workspace.ID, "旧请求不得停止新工作")
	journey := engine.Model.(*projectJourneyModel)
	journey.blockGoal = "持续阻塞的控制测试"
	start := func(key string) w.Run {
		t.Helper()
		journey.started, journey.release, journey.once = make(chan struct{}), make(chan struct{}), sync.Once{}
		input := w.RunInput{Message: journey.blockGoal, Workflow: "free", Targets: thread.AgentIDs, ResearchScope: "given_sources"}
		var preview RoutingPreview
		api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
		input.ProposalID = preview.ProposalID
		var run w.Run
		api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": key}, 202, &run)
		select {
		case <-journey.started:
		case <-time.After(3 * time.Second):
			t.Fatal("control test run did not start")
		}
		return run
	}
	awaitStop := func(id string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			var view RunView
			api.call("GET", "/api/v1/runs/"+id, nil, nil, 200, &view)
			if w.Terminal(view.Run.Status) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("run did not stop: %+v", view.Run)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	first := start("control-replay-first")
	created := projectJourneyTask(t, api, thread.ID)
	action := TaskActionInput{Action: "pause", RequestID: "control-replay-pause"}
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/actions", action, nil, 200, nil)
	awaitStop(first.ID)
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/actions", TaskActionInput{Action: "resume", RequestID: "control-replay-resume"}, nil, 200, nil)
	second := start("control-replay-second")
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/actions", action, nil, 200, nil)
	// Re-saving the current global mode must not stop new work.
	var currentSettings w.Settings
	api.call("GET", "/api/v1/settings", nil, nil, 200, &currentSettings)
	api.call("PATCH", "/api/v1/settings", SettingsInput{ExecutionMode: ptr(currentSettings.ExecutionMode)}, map[string]string{"If-Match": fmt.Sprint(currentSettings.Revision)}, 200, nil)
	var current RunView
	api.call("GET", "/api/v1/runs/"+second.ID, nil, nil, 200, &current)
	if w.Terminal(current.Run.Status) || current.Run.Status == "canceling" {
		t.Fatalf("replayed pause or unchanged mode stopped new work: %+v", current.Run)
	}
	api.call("PATCH", "/api/v1/settings", SettingsInput{ExecutionMode: ptr(w.ExecutionControlled)}, map[string]string{"If-Match": fmt.Sprint(currentSettings.Revision + 1)}, 200, nil)
	awaitStop(second.ID)
}
