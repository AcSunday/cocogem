package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestManagedSessionResumesWithExactDeltaAfterProcessRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "developer", Instruction: "implement the accepted objective"}})
	created := attachSessionTask(t, db, &run)
	service := project.New(db)
	if _, err := service.SetExecutionMode(t.Context(), run.WorkspaceID, w.ExecutionFullAccess, "full-access-choice", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if err := service.EnsureRunAuthorization(tx, &current, "full-access-choice"); err != nil {
			return err
		}
		manifest, err := service.PrepareContextInTx(tx, current.TaskID, current.TaskRevision, project.ContextHostInput(current))
		if err != nil {
			return err
		}
		current.ContextRef = manifest.ID
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		run = current
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	const nativeID = "11111111-1111-4111-8111-111111111111"
	var mu sync.Mutex
	requests := []model.Request{}
	runner := nativeRunnerFunc(func(_ context.Context, request model.Request, _ agent.Execute, observe agent.Observe) error {
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		if request.SessionMode == "resume" && request.SessionID != nativeID {
			return observe("native_session", map[string]any{"session_id": "22222222-2222-4222-8222-222222222222", "mode": "resume"})
		}
		if err := observe("native_session", map[string]any{"session_id": nativeID, "mode": request.SessionMode}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "managed work completed"})
	})
	checker := sessionChecker{
		adapter:    agent.AdapterInfo{ID: "claude", Supported: true, Installed: true, Version: "2.1.318", Identity: "claude-v1", Adapter: "claude-stream-json", Capabilities: []string{"stream_json", "managed_session", "session_resume_ack=supported"}},
		connection: agent.ConnectionInfo{ResolvedModel: "claude-actual", Identity: "claude-provider"},
	}
	sessionRoot := filepath.Join(t.TempDir(), "native-sessions")
	firstEngine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checker, NativeSessionRoot: sessionRoot}
	firstCancel, firstWait := startEngine(t, &firstEngine)
	if got := awaitRun(t, db, run.ID); got.Status != "succeeded" {
		firstCancel()
		firstWait()
		t.Fatalf("first managed run = %+v", got)
	}
	firstCancel()
	firstWait()
	firstAttempt := loadAttempts(t, db, run.ID)[0]
	if firstAttempt.SessionMode != "fresh" || firstAttempt.SessionID != nativeID || firstAttempt.ExecutionSessionRef == "" {
		t.Fatalf("first managed attempt = %+v", firstAttempt)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		session, err := tx.Session(firstAttempt.ExecutionSessionRef)
		if err != nil {
			return err
		}
		if session.Acknowledgement != "fresh_acknowledged" || len(session.AcknowledgedExposedRefs) == 0 {
			t.Fatalf("initial input was not acknowledged: %+v", session)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen both the durable work record and the server-owned CLI session root,
	// as a new server process would after a restart.
	db, err = store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service = project.New(db)
	if _, err := service.SaveProjectRecord(t.Context(), w.ProjectRecord{
		ID: "new-runtime-constraint", WorkspaceID: run.WorkspaceID, Kind: "decision",
		Content: "新增约束：续接必须保留现有幂等键", AcceptedBy: "user", Visibility: "project",
	}); err != nil {
		t.Fatal(err)
	}
	second := run
	second.ID = "managed-resume-run"
	second.Status = "queued"
	second.CreatedAt = w.Now()
	second.StartedAt, second.FinishedAt = "", ""
	second.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &second, "resume-source"); err != nil {
			return err
		}
		manifest, err := service.PrepareContextInTx(tx, second.TaskID, second.TaskRevision, project.ContextHostInput(second))
		if err != nil {
			return err
		}
		second.ContextRef = manifest.ID
		second.InputDigest = project.ContextInputDigest(created, manifest, "resume-source", second.Input)
		if err := tx.InsertRun(second, "managed-resume-key", "managed-resume-hash"); err != nil {
			return err
		}
		return tx.SaveStep(w.Step{ID: "managed-resume-step", RunID: second.ID, AgentID: run.Agents[0].ID, AgentName: run.Agents[0].DisplayName, Role: "developer", Instruction: "continue with the new accepted constraint", Status: "pending"})
	}); err != nil {
		t.Fatal(err)
	}
	secondEngine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checker, NativeSessionRoot: sessionRoot}
	secondCancel, secondWait := startEngine(t, &secondEngine)
	defer secondWait()
	if got := awaitRun(t, db, second.ID); got.Status != "succeeded" {
		secondCancel()
		t.Fatalf("resumed managed run = %+v", got)
	}
	secondCancel()
	secondAttempt := loadAttempts(t, db, second.ID)[0]
	if secondAttempt.ExecutionSessionRef != firstAttempt.ExecutionSessionRef || secondAttempt.SessionMode != "resume" || secondAttempt.SessionID != nativeID {
		t.Fatalf("restart did not resume the exact execution session: first=%+v second=%+v", firstAttempt, secondAttempt)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("captured %d requests, want fresh and resume", len(requests))
	}
	freshText := requests[0].Messages[0].Content[0].Text
	resumeText := requests[1].Messages[0].Content[0].Text
	if strings.Contains(freshText, "session_delta/v1") || !strings.Contains(freshText, created.Revision.Goal) {
		t.Fatalf("fresh request did not carry the full work input: %s", freshText)
	}
	for _, wanted := range []string{"session_delta/v1", "新增约束：续接必须保留现有幂等键", "new-runtime-constraint", "continue with the new accepted constraint"} {
		if !strings.Contains(resumeText, wanted) {
			t.Fatalf("resume delta omitted %q: %s", wanted, resumeText)
		}
	}
	if requests[1].SessionStorageRoot == "" || requests[1].SessionStorageRoot != requests[0].SessionStorageRoot || requests[1].SessionContextEpoch <= requests[0].SessionContextEpoch {
		t.Fatalf("managed storage or context epoch regressed: first=%+v second=%+v", requests[0], requests[1])
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		session, err := tx.Session(firstAttempt.ExecutionSessionRef)
		if err != nil {
			return err
		}
		if session.Acknowledgement != "resume_acknowledged" || session.ContextEpoch != requests[1].SessionContextEpoch {
			t.Fatalf("resume receipt was not persisted: %+v", session)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSessionReuseBoundaries(t *testing.T) {
	session := w.ExecutionSession{ID: "managed", TaskID: "task", MemberID: "member", Role: "developer", ExecutionBranch: "branch", Fingerprint: "fingerprint", VisibilityEpoch: 3, NativeSessionID: "native", Acknowledgement: "fresh_acknowledged", Status: "active"}
	if !sessionReuseEligible(session, session.Fingerprint, session.VisibilityEpoch) {
		t.Fatal("acknowledged session should be eligible at the same boundary")
	}
	for _, test := range []struct {
		name        string
		fingerprint string
		epoch       int
		mutate      func(*w.ExecutionSession)
	}{
		{name: "model or task fingerprint", fingerprint: "other", epoch: 3},
		{name: "visibility epoch", fingerprint: "fingerprint", epoch: 4},
		{name: "role", fingerprint: "fingerprint", epoch: 3, mutate: func(s *w.ExecutionSession) { s.Role = "reviewer" }},
		{name: "execution branch", fingerprint: "fingerprint", epoch: 3, mutate: func(s *w.ExecutionSession) { s.ExecutionBranch = "other" }},
		{name: "member", fingerprint: "fingerprint", epoch: 3, mutate: func(s *w.ExecutionSession) { s.MemberID = "other" }},
		{name: "unacknowledged", fingerprint: "fingerprint", epoch: 3, mutate: func(s *w.ExecutionSession) { s.Acknowledgement = "awaiting_runner_ack" }},
		{name: "revoked", fingerprint: "fingerprint", epoch: 3, mutate: func(s *w.ExecutionSession) { s.Status = "revoked" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := session
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			eligible := candidate.MemberID == session.MemberID && candidate.Role == session.Role && candidate.ExecutionBranch == session.ExecutionBranch && sessionReuseEligible(candidate, test.fingerprint, test.epoch)
			if eligible {
				t.Fatalf("session crossed a managed reuse boundary: %+v", candidate)
			}
		})
	}
}
