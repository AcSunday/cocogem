package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type sessionChecker struct {
	adapter    agent.AdapterInfo
	connection agent.ConnectionInfo
}

func (c sessionChecker) Check(context.Context, string) (agent.AdapterInfo, error) {
	return c.adapter, nil
}

func (c sessionChecker) InspectExecutionConnection(context.Context, string, string, string) (agent.ConnectionInfo, error) {
	return c.connection, nil
}

func attachSessionTask(t *testing.T, db *store.Store, run *w.Run) project.TaskView {
	t.Helper()
	service := project.New(db)
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, OwnerMemberID: run.Agents[0].ID,
		Goal: "continue from durable work across members and CLIs", AllowedActions: []string{"read", "collaborate"},
		SourceMessageID: "session-source", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		current.TaskID = created.Task.ID
		current.TaskRevision = created.Task.CurrentRevision
		current.BudgetAccountID = created.Budget.ID
		if err := service.EnsureRunAuthorization(tx, &current, "session-source"); err != nil {
			return err
		}
		manifest, err := service.PrepareContextInTx(tx, current.TaskID, current.TaskRevision, project.ContextHostInput(current))
		if err != nil {
			return err
		}
		current.ContextRef = manifest.ID
		current.InputDigest = project.ContextInputDigest(created, manifest, "session-source", current.Input)
		if err := tx.SaveRun(current); err != nil {
			return err
		}
		*run = current
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return created
}

func TestAttemptRecordsFreshSessionFingerprintAndExposure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	attachSessionTask(t, db, &run)
	checker := sessionChecker{
		adapter:    agent.AdapterInfo{ID: "claude", Installed: true, Supported: true, Version: "2.1.0", Program: "/usr/local/bin/claude", Identity: "cli-v1", Adapter: "claude-stream-json", Isolation: "candidate", Capabilities: []string{"stream_json"}},
		connection: agent.ConnectionInfo{ResolvedModel: "claude-actual", Identity: "provider-account-v1"},
	}
	runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		if err := observe("native_session", map[string]any{"session_id": "fresh-native-session", "mode": "fresh"}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "fresh result"})
	})
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checker}
	_, wait := startEngine(t, &engine)
	defer wait()
	if got := awaitRun(t, db, run.ID); got.Status != "succeeded" {
		t.Fatalf("fresh run = %+v", got)
	}
	attempt := loadAttempts(t, db, run.ID)[0]
	if attempt.ExecutionSessionRef == "" || attempt.SessionFingerprint == "" || attempt.SessionMode != "fresh" {
		t.Fatalf("attempt session identity = %+v", attempt)
	}
	var session w.ExecutionSession
	var steps []w.Step
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		session, err = tx.Session(attempt.ExecutionSessionRef)
		if err != nil {
			return err
		}
		steps, err = tx.Steps(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if session.Fingerprint != attempt.SessionFingerprint || session.Status != "fresh_only" || session.Acknowledgement != "fresh_started_resume_unsupported" || session.NativeSessionID != "fresh-native-session" {
		t.Fatalf("fresh session was presented as reusable: %+v", session)
	}
	if session.VisibilityEpoch != max(1, run.PermissionEpoch) || len(session.ExposedRefs) == 0 || session.ExposureDigest == "" {
		t.Fatalf("session exposure was not recorded: %+v", session)
	}
	if sessionReuseEligible(session, session.Fingerprint, session.VisibilityEpoch) {
		t.Fatal("fresh session became reusable without a resume ACK")
	}
	baseInput := sessionInput{run: run, step: steps[0], member: run.Agents[0], adapter: checker.adapter, connection: checker.connection, attempt: attempt}
	variants := []struct {
		name   string
		mutate func(*sessionInput)
	}{
		{name: "role", mutate: func(in *sessionInput) { in.step.Role = "reviewer" }},
		{name: "connection", mutate: func(in *sessionInput) { in.connection.Identity = "provider-account-v2" }},
		{name: "execution root", mutate: func(in *sessionInput) { in.run.ExecutionRoot = "/tmp/other-execution" }},
		{name: "grant", mutate: func(in *sessionInput) { in.run.GrantSnapshot = "new-grant-epoch" }},
		{name: "permission epoch", mutate: func(in *sessionInput) { in.run.PermissionEpoch++ }},
		{name: "context protocol", mutate: func(in *sessionInput) { in.run.ContextRef = "" }},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			input := baseInput
			variant.mutate(&input)
			if err := db.Read(t.Context(), func(tx *store.Tx) error {
				changed, err := prepareExecutionSession(tx, input)
				if err != nil {
					return err
				}
				if changed.Fingerprint == session.Fingerprint {
					t.Fatal("session fingerprint did not change")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTaskColdStartAcrossThreadMemberAndCLI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	created := attachSessionTask(t, db, &run)
	firstRunner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		if err := observe("native_session", map[string]any{"session_id": "claude-native", "mode": "fresh"}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "first member completed work"})
	})
	firstChecker := sessionChecker{
		adapter:    agent.AdapterInfo{ID: "claude", Supported: true, Installed: true, Version: "2.1.0", Identity: "claude-v1", Adapter: "claude-stream-json"},
		connection: agent.ConnectionInfo{ResolvedModel: "claude-actual", Identity: "claude-provider"},
	}
	firstEngine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": firstRunner}, Adapters: firstChecker}
	firstCancel, firstWait := startEngine(t, &firstEngine)
	if got := awaitRun(t, db, run.ID); got.Status != "succeeded" {
		t.Fatalf("first run = %+v", got)
	}
	firstCancel()
	firstWait()
	firstAttempt := loadAttempts(t, db, run.ID)[0]
	firstEvidence := w.Source{ID: "first-work-evidence", WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "file", Content: "durable work evidence"}
	newMember := run.Agents[0]
	newMember.ID = "codex-member"
	newMember.Handle = "codex-member"
	newMember.Aliases = nil
	newMember.Executor = "codex"
	newThread := w.Thread{ID: "cold-start-thread", WorkspaceID: run.WorkspaceID, TaskID: run.TaskID, AgentIDs: []string{newMember.ID}, CoordinatorID: newMember.ID, Internal: true, Revision: 1, UpdatedAt: w.Now()}
	second := run
	second.ID = "cold-start-run"
	second.ThreadID = newThread.ID
	second.Status = "queued"
	second.CreatedAt = w.Now()
	second.StartedAt = ""
	second.FinishedAt = ""
	second.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
	second.Agents = []w.Agent{newMember}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveSource(firstEvidence); err != nil {
			return err
		}
		if err := tx.SaveAgent(newMember); err != nil {
			return err
		}
		if err := tx.SaveThread(newThread); err != nil {
			return err
		}
		service := project.New(nil)
		if err := service.EnsureRunAuthorization(tx, &second, "cold-start-source"); err != nil {
			return err
		}
		manifest, err := service.PrepareContextInTx(tx, second.TaskID, second.TaskRevision, project.ContextHostInput(second))
		if err != nil {
			return err
		}
		second.ContextRef = manifest.ID
		if err := tx.InsertRun(second, "cold-start-key", "cold-start-hash"); err != nil {
			return err
		}
		return tx.SaveStep(w.Step{ID: "cold-start-step", RunID: second.ID, AgentID: newMember.ID, AgentName: newMember.DisplayName, Role: "responder", Instruction: "continue from the exact work record", Status: "pending"})
	}); err != nil {
		t.Fatal(err)
	}
	secondRunner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		prompt := request.Messages[0].Content[0].Text
		if !strings.Contains(prompt, created.Revision.Goal) || !strings.Contains(prompt, firstEvidence.ID) {
			t.Errorf("cold-start prompt omitted work record or evidence: %s", prompt)
		}
		read, err := execute(ctx, "read_source", jsonRaw(`{"source_id":"first-work-evidence"}`))
		if err != nil || !strings.Contains(read, "durable work evidence") {
			t.Errorf("cold-start source read = %s, %v", read, err)
		}
		if err := observe("native_session", map[string]any{"session_id": "codex-native", "mode": "fresh"}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "second member continued from durable work"})
	})
	secondChecker := sessionChecker{
		adapter:    agent.AdapterInfo{ID: "codex", Supported: true, Installed: true, Version: "0.100.0", Identity: "codex-v1", Adapter: "codex-exec-json"},
		connection: agent.ConnectionInfo{ResolvedModel: "codex-actual", Identity: "codex-provider"},
	}
	secondEngine := Engine{Store: db, Runners: map[string]agent.Runner{"codex": secondRunner}, Adapters: secondChecker}
	_, secondWait := startEngine(t, &secondEngine)
	defer secondWait()
	if got := awaitRun(t, db, second.ID); got.Status != "succeeded" {
		t.Fatalf("cold-start run = %+v", got)
	}
	secondAttempt := loadAttempts(t, db, second.ID)[0]
	if secondAttempt.Executor != "codex" || secondAttempt.SessionID != "codex-native" || secondAttempt.SessionMode != "fresh" || secondAttempt.ExecutionSessionRef == firstAttempt.ExecutionSessionRef || secondAttempt.SessionFingerprint == firstAttempt.SessionFingerprint {
		t.Fatalf("cold start reused or mislabeled native state: first=%+v second=%+v", firstAttempt, secondAttempt)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		task, err := tx.Task(created.Task.ID)
		if err != nil {
			return err
		}
		if task.Status != "ready" || task.EvaluationStatus != "incomplete" {
			t.Fatalf("cold-start execution completed the objective without acceptance: %+v", task)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func jsonRaw(value string) []byte { return []byte(value) }

// TestPermissionEpochChangeInvalidatesResumableSession proves a full_access ->
// controlled -> full_access round trip cannot revive a managed session created
// under the earlier permission boundary. The new epoch changes the fingerprint and
// visibility, and no automatic execution is authorized from the old session.
func TestPermissionEpochChangeInvalidatesResumableSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	attachSessionTask(t, db, &run)
	adapter := agent.AdapterInfo{ID: "claude", Installed: true, Supported: true, Version: "2.1.0", Program: "/usr/local/bin/claude", Identity: "cli-v1", Adapter: "claude-stream-json", Capabilities: []string{"session_resume_ack=supported"}}
	connection := agent.ConnectionInfo{ResolvedModel: "claude-actual", Identity: "provider-account-v1"}
	steps := []w.Step{{ID: "session-step", RunID: run.ID, AgentID: run.Agents[0].ID, AgentName: run.Agents[0].DisplayName, Role: "responder", Status: "running"}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run.EffectiveExecutionMode = w.ExecutionFullAccess
	run.RequestedExecutionMode = w.ExecutionFullAccess
	var live w.Settings
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		live, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	run.PermissionEpoch = live.PermissionEpoch
	first := w.ExecutionSession{
		ID: "old-session", TaskID: run.TaskID, MemberID: run.Agents[0].ID, Role: steps[0].Role,
		Fingerprint: "fingerprint-v1", VisibilityEpoch: live.PermissionEpoch, Status: "active",
		Acknowledgement: "resume_acknowledged", NativeSessionID: "native-old",
		CreatedAt: w.Now(), UpdatedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveSession(first) }); err != nil {
		t.Fatal(err)
	}
	base := sessionInput{run: run, step: steps[0], member: run.Agents[0], adapter: adapter, connection: connection, attempt: w.Attempt{ID: "a1", RunID: run.ID, StepID: steps[0].ID, Number: 1, Status: "running", StartedAt: w.Now()}}
	var before, after w.ExecutionSession
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		before, err = prepareExecutionSession(tx, base)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if before.VisibilityEpoch != live.PermissionEpoch {
		t.Fatalf("baseline visibility epoch = %d, want %d", before.VisibilityEpoch, live.PermissionEpoch)
	}
	// Permission tightened and later restored: the live epoch is now 5.
	tightened := base
	tightened.run.PermissionEpoch = live.PermissionEpoch + 1
	tightened.run.EffectiveExecutionMode = w.ExecutionControlled
	tightened.run.RequestedExecutionMode = w.ExecutionControlled
	restored := base
	restored.run.PermissionEpoch = live.PermissionEpoch + 2
	restored.run.EffectiveExecutionMode = w.ExecutionFullAccess
	restored.run.RequestedExecutionMode = w.ExecutionFullAccess
	for _, input := range []sessionInput{tightened, restored} {
		if err := db.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			after, err = prepareExecutionSession(tx, input)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if after.Fingerprint == before.Fingerprint {
			t.Fatalf("epoch change kept the fingerprint: %+v", after)
		}
		if sessionReuseEligible(first, after.Fingerprint, after.VisibilityEpoch) {
			t.Fatalf("old session is reusable after an epoch change: %+v", after)
		}
	}
}
