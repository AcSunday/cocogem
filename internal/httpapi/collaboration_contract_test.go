package httpapi

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func contractFixture(t *testing.T, engine *workflow.Engine) (*deliveryAPI, *store.Store, *project.Service, project.TaskView) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(w.Workspace{ID: "project", Name: "项目", Revision: 1, ExecutionMode: w.ExecutionControlled}); err != nil {
			return err
		}
		return tx.SaveThread(w.Thread{ID: "thread", WorkspaceID: "project", Revision: 1, UpdatedAt: w.Now(), CoordinatorID: "owner"})
	}); err != nil {
		t.Fatal(err)
	}
	api := newDeliveryAPI(t, root, db, engine)
	var created project.TaskView
	api.call("POST", "/api/v1/workspaces/project/tasks", project.CreateTaskInput{
		ThreadID: "thread", OwnerMemberID: "owner", Goal: "持久目标", AllowedActions: []string{"read", "collaborate"},
		SourceMessageID: "message", AcceptedBy: "user",
	}, nil, 200, &created)
	return api, db, project.New(db), created
}

func TestQuestionAnswerHTTPIdempotencyAndStalePersistence(t *testing.T) {
	api, db, service, created := contractFixture(t, nil)
	question, err := service.AskQuestion(t.Context(), created.Task.ID, "branch", "选择哪个版本？", []string{"一", "二"}, "一")
	if err != nil {
		t.Fatal(err)
	}
	input := QuestionAnswerInput{Answer: "二", AnswerRef: "answer-request-1"}
	var answered, replay w.Question
	headers := map[string]string{"If-Match": "1"}
	path := "/api/v1/tasks/" + created.Task.ID + "/questions/" + question.ID + "/answers"
	api.call("POST", path, input, headers, 200, &answered)
	api.call("POST", path, input, headers, 200, &replay)
	if answered.Status != "resolved" || replay.ID != answered.ID || replay.AnswerRef != input.AnswerRef {
		t.Fatalf("question answer replay = %+v / %+v", answered, replay)
	}
	conflict := QuestionAnswerInput{Answer: "另一答案", AnswerRef: input.AnswerRef}
	api.call("POST", path, conflict, headers, 409, nil)
	var listed QuestionList
	api.call("GET", "/api/v1/tasks/"+created.Task.ID+"/questions", nil, nil, 200, &listed)
	if len(listed.Items) != 1 || listed.Items[0].Status != "resolved" {
		t.Fatalf("question query = %+v", listed.Items)
	}

	stale, err := service.AskQuestion(t.Context(), created.Task.ID, "branch", "旧版本问题", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/revisions", project.ReviseTaskInput{
		Goal: "新目标", Acceptance: created.Revision.Acceptance, AllowedActions: created.Revision.AllowedActions,
		SourceMessageID: "revision-source", AcceptedBy: "user",
	}, headers, 200, &created)
	api.call("POST", "/api/v1/tasks/"+created.Task.ID+"/questions/"+stale.ID+"/answers", QuestionAnswerInput{
		Answer: "迟到回答", AnswerRef: "late-answer",
	}, headers, 409, nil)
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		persisted, err := tx.Question(stale.ID)
		if err != nil || persisted.Status != "superseded" || persisted.Answer != "" {
			t.Fatalf("stale answer rolled back = %+v, %v", persisted, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserCollaborationAndResearchCannotForgeProducer(t *testing.T) {
	api, db, _, sender := contractFixture(t, nil)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveThread(w.Thread{ID: "target-thread", WorkspaceID: "project", Revision: 1, UpdatedAt: w.Now(), CoordinatorID: "recipient"})
	}); err != nil {
		t.Fatal(err)
	}
	var target project.TaskView
	api.call("POST", "/api/v1/workspaces/project/tasks", project.CreateTaskInput{
		ThreadID: "target-thread", OwnerMemberID: "recipient", Goal: "接收通知", AllowedActions: []string{"read"},
		SourceMessageID: "target-message", AcceptedBy: "user",
	}, nil, 200, &target)
	forged := map[string]any{
		"target_task_id": target.Task.ID, "kind": "result", "body": "伪造成员结果", "request_id": "forged-message",
		"sender_attempt_id": "forged-attempt", "context_refs": []string{},
	}
	api.call("POST", "/api/v1/tasks/"+sender.Task.ID+"/messages", forged, nil, 400, nil)
	input := TaskMessageInput{TargetTaskID: target.Task.ID, Kind: "result", Body: "用户直接提交的通知", RequestID: "browser-message", ContextRefs: []string{}}
	var sent project.MessageView
	api.call("POST", "/api/v1/tasks/"+sender.Task.ID+"/messages", input, nil, 200, &sent)
	if sent.Message.SenderAttemptID != "" || sent.Message.SenderTaskID != sender.Task.ID || len(sent.Receipts) != 1 {
		t.Fatalf("browser sender binding = %+v", sent)
	}
	var detail project.MessageView
	api.call("GET", "/api/v1/collaboration-messages/"+sent.Message.ID, nil, nil, 200, &detail)
	if detail.Message.ID != sent.Message.ID || detail.Receipts[0].Status != "queued" {
		t.Fatalf("message detail = %+v", detail)
	}
	var messages CollaborationMessageList
	api.call("GET", "/api/v1/tasks/"+sender.Task.ID+"/messages", nil, nil, 200, &messages)
	if len(messages.Items) != 1 {
		t.Fatalf("message history = %+v", messages.Items)
	}

	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(w.Run{ID: "source-run", WorkspaceID: "project", ThreadID: sender.Task.ThreadID, TaskID: sender.Task.ID, TaskRevision: 1, Status: "succeeded", CreatedAt: w.Now()}, "source-run", "source-run-digest"); err != nil {
			return err
		}
		return tx.SaveSource(w.Source{ID: "evidence", WorkspaceID: "project", ThreadID: sender.Task.ThreadID, RunID: "source-run", Kind: "text", Content: "1:evidence", SHA256: "digest", Visibility: "project", CapturedAt: w.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	claim := w.Claim{
		ID: "claim", WorkspaceID: "forged-workspace", TaskID: "forged-task", ProducerAttemptID: "forged-attempt", Revision: 1,
		Statement: "有完整证据", Status: "supported",
		Evidence: []w.ClaimEvidence{{SourceID: "evidence", Relation: "supports", Locator: "line:1", Completeness: "complete", Reviewer: "forged-member"}},
	}
	var savedClaim w.Claim
	api.call("POST", "/api/v1/tasks/"+sender.Task.ID+"/claims", claim, nil, 200, &savedClaim)
	if savedClaim.ProducerAttemptID != "" || savedClaim.TaskID != sender.Task.ID || savedClaim.WorkspaceID != "project" || savedClaim.Evidence[0].Reviewer != "user" {
		t.Fatalf("claim producer binding = %+v", savedClaim)
	}
	dataset := w.DatasetManifest{ID: "dataset", ProducerAttemptID: "forged-attempt", Revision: 1, SourceRefs: []string{"evidence"}, ContentDigest: "digest"}
	var savedDataset w.DatasetManifest
	api.call("POST", "/api/v1/tasks/"+sender.Task.ID+"/datasets", dataset, nil, 200, &savedDataset)
	if savedDataset.ProducerAttemptID != "" || savedDataset.TaskID != sender.Task.ID {
		t.Fatalf("dataset producer binding = %+v", savedDataset)
	}
	memory := w.MemoryRecord{ID: "memory", ProducerAttemptID: "forged-attempt", Revision: 1, Content: "用户确认的记忆", SourceRefs: []string{"evidence"}, ClaimStatus: "accepted", AcceptedBy: "forged-member"}
	var savedMemory w.MemoryRecord
	api.call("POST", "/api/v1/workspaces/project/memories", memory, nil, 200, &savedMemory)
	if savedMemory.ProducerAttemptID != "" || savedMemory.AcceptedBy != "user" {
		t.Fatalf("memory producer binding = %+v", savedMemory)
	}
	var claims ClaimList
	var datasets DatasetList
	var memories MemoryList
	api.call("GET", "/api/v1/tasks/"+sender.Task.ID+"/claims", nil, nil, 200, &claims)
	api.call("GET", "/api/v1/tasks/"+sender.Task.ID+"/datasets", nil, nil, 200, &datasets)
	api.call("GET", "/api/v1/workspaces/project/memories", nil, nil, 200, &memories)
	if len(claims.Items) != 1 || len(datasets.Items) != 1 || len(memories.Items) != 1 {
		t.Fatalf("research queries = %d/%d/%d", len(claims.Items), len(datasets.Items), len(memories.Items))
	}

	api.call("POST", "/api/v1/wake-events", WakeEventInput{WakeSource: "calendar", EventID: "event-1", Observation: "host_observed", EvidenceRef: "user-message@1"}, nil, 200, nil)
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		event, err := tx.WakeEvent("calendar", "event-1")
		if err != nil || !strings.HasPrefix(event.SatisfiedBy, "user_observation:host_observed") {
			t.Fatalf("browser promoted wake evidence = %+v, %v", event, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var wakeups WakeupList
	api.call("GET", "/api/v1/tasks/"+sender.Task.ID+"/wakeups", nil, nil, 200, &wakeups)
	if len(wakeups.Items) != 0 {
		t.Fatalf("unexpected wakeups = %+v", wakeups.Items)
	}

	execution := w.WorkspaceExecution{ID: "execution", WorkspaceID: "project", TaskID: sender.Task.ID, CanonicalRoot: "/candidate", LeaseEpoch: 1, Status: "released"}
	effect := w.EffectRecord{ID: "effect", TaskID: sender.Task.ID, IdempotencyKey: "effect-key", Status: "applied"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveExecution(execution); err != nil {
			return err
		}
		return tx.SaveEffect(effect)
	}); err != nil {
		t.Fatal(err)
	}
	var gotExecution w.WorkspaceExecution
	var gotEffect w.EffectRecord
	api.call("GET", "/api/v1/executions/execution", nil, nil, 200, &gotExecution)
	api.call("GET", "/api/v1/effects/effect", nil, nil, 200, &gotEffect)
	if gotExecution.ID != execution.ID || gotEffect.ID != effect.ID {
		t.Fatalf("execution/effect queries = %+v / %+v", gotExecution, gotEffect)
	}
}

func TestUserDelegationAndHandoffUseHostBoundPaths(t *testing.T) {
	engine := &workflow.Engine{}
	api, db, _, parent := contractFixture(t, engine)
	engine.Store = db
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		for _, member := range []w.Agent{
			{ID: "owner", WorkspaceID: "project", DisplayName: "Owner", Handle: "owner", Revision: 1, Tools: []string{"read"}},
			{ID: "recipient", WorkspaceID: "project", DisplayName: "Recipient", Handle: "recipient", Revision: 1, Tools: []string{"read"}},
		} {
			if err := tx.SaveAgent(member); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	forged := map[string]any{"owner_member_id": "owner", "goal": "伪造生产 Attempt", "request_id": "forged-delegation", "parent_attempt_id": "forged-attempt"}
	api.call("POST", "/api/v1/tasks/"+parent.Task.ID+"/delegations", forged, nil, 400, nil)
	input := UserDelegationInput{OwnerMemberID: "owner", Goal: "用户委派的子目标", RequestID: "user-delegation-1"}
	var delegated, replay project.DelegationView
	api.call("POST", "/api/v1/tasks/"+parent.Task.ID+"/delegations", input, nil, 200, &delegated)
	api.call("POST", "/api/v1/tasks/"+parent.Task.ID+"/delegations", input, nil, 200, &replay)
	if delegated.Task.Revision.AcceptedBy != "user" || delegated.Task.Task.ParentID != parent.Task.ID || delegated.Task.Budget.ID != parent.Budget.ID || replay.Task.Task.ID != delegated.Task.Task.ID || replay.Run.ID != delegated.Run.ID {
		t.Fatalf("user delegation = %+v / %+v", delegated, replay)
	}
	var transferred w.Task
	api.call("POST", "/api/v1/tasks/"+parent.Task.ID+"/handoffs", HandoffInput{NewOwnerID: "recipient", RequestID: "handoff-1", ExpectedOwnerRevision: 1}, nil, 200, &transferred)
	if transferred.OwnerMemberID != "recipient" || transferred.OwnerRevision != 2 || transferred.TransferRequestID != "" {
		t.Fatalf("HTTP handoff = %+v", transferred)
	}
}
