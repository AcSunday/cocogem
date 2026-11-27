package workflow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestContextOverflowSealsTheNativeSession is the A28 regression: an
// overflowing native context is retired for good, so the fresh continuation can
// never resume the exact session that overflowed, while the sealed row and its
// exposure evidence stay readable.
func TestContextOverflowSealsTheNativeSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	attachSessionTask(t, db, &run)
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionFullAccess); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		return service.EnsureRunAuthorization(tx, &current, "overflow-source")
	}); err != nil {
		t.Fatal(err)
	}
	// First attempt acknowledges a resumable native session, then overflows.
	var openings []string
	runner := &testRunner{run: func(ctx context.Context, request model.Request, call int, _ agent.Execute, observe agent.Observe) error {
		openings = append(openings, request.SessionMode)
		if call == 1 {
			if err := observe("native_session", map[string]any{"session_id": "session-overflowing", "mode": "fresh"}); err != nil {
				return err
			}
			return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "context overflow"}
		}
		if request.SessionMode == "resume" {
			t.Errorf("a fresh continuation resumed the sealed session: %+v", request)
		}
		if err := observe("native_session", map[string]any{"session_id": "session-replacement", "mode": "fresh"}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "continued in a fresh session"})
	}}
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &engine)
	defer wait()
	awaitRun(t, db, run.ID)
	if _, err := engine.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	got := awaitRun(t, db, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("fresh continuation did not succeed: %+v", got)
	}
	if len(openings) < 2 || openings[0] != "fresh" || openings[1] != "fresh" {
		t.Fatalf("continuation did not open a fresh session: %v", openings)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		sessions, err := tx.Sessions(run.TaskID)
		if err != nil {
			return err
		}
		var sealed, replacement *w.ExecutionSession
		for index := range sessions {
			switch sessions[index].NativeSessionID {
			case "session-overflowing":
				sealed = &sessions[index]
			case "session-replacement":
				replacement = &sessions[index]
			}
		}
		if sealed == nil || sealed.Status != "sealed" {
			t.Fatalf("overflowing session was not sealed: %+v", sessions)
		}
		if replacement == nil {
			t.Fatalf("replacement session was not recorded: %+v", sessions)
		}
		if replacement.Status == "sealed" || sessionReuseEligible(*sealed, sealed.Fingerprint, sealed.VisibilityEpoch) {
			t.Fatalf("sealed session stayed reusable: sealed=%+v replacement=%+v", sealed, replacement)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResumedFailureAutoSealsAndOpensFreshSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := project.New(db)
	if _, err := setGlobalMode(t, db, service, w.ExecutionFullAccess); err != nil {
		t.Fatal(err)
	}
	run1 := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "first"}})
	attachSessionTask(t, db, &run1)

	brokenSessionID := "11111111-1111-4111-8111-111111111111"
	freshSessionID := "22222222-2222-4222-8222-222222222222"
	var call int
	var modes []string
	runner := nativeRunnerFunc(func(_ context.Context, request model.Request, _ agent.Execute, observe agent.Observe) error {
		call++
		modes = append(modes, request.SessionMode)
		if err := observe("native_adapter", map[string]any{"executor": "claude", "version": "2.1.318", "identity": "claude-v1", "adapter": "claude-stream-json"}); err != nil {
			return err
		}
		if err := observe("native_process", map[string]any{"pid": 100 + call, "pgid": 100 + call, "start": "now", "executable": "/bin/claude"}); err != nil {
			return err
		}
		if call == 1 {
			if err := observe("native_session", map[string]any{"session_id": brokenSessionID, "mode": request.SessionMode}); err != nil {
				return err
			}
			return observe("text", map[string]any{"text": "first turn ok"})
		}
		if call == 2 {
			if err := observe("native_session", map[string]any{"session_id": brokenSessionID, "mode": request.SessionMode}); err != nil {
				return err
			}
			return &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: "simulated protocol failure on resumed session"}
		}
		if err := observe("native_session", map[string]any{"session_id": freshSessionID, "mode": request.SessionMode}); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "recovered in fresh session"})
	})
	checker := sessionChecker{
		adapter:    agent.AdapterInfo{ID: "claude", Supported: true, Installed: true, Version: "2.1.318", Identity: "claude-v1", Adapter: "claude-stream-json", Capabilities: []string{"stream_json", "managed_session", "session_resume_ack=supported"}},
		connection: agent.ConnectionInfo{ResolvedModel: "claude-actual", Identity: "claude-provider"},
	}
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}, Adapters: checker, NativeSessionRoot: filepath.Join(t.TempDir(), "native-sessions")}
	cancel, wait := startEngine(t, &engine)
	defer func() {
		cancel()
		wait()
	}()
	if got := awaitRun(t, db, run1.ID); got.Status != "succeeded" {
		t.Fatalf("initial run failed: %+v", got)
	}

	run2 := run1
	run2.ID = w.NewID()
	run2.Status = "queued"
	run2.Error = ""
	run2.FailureKind = ""
	run2.CreatedAt = w.Now()
	run2.StartedAt = ""
	run2.FinishedAt = ""
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &run2, "seal-source-2"); err != nil {
			return err
		}
		if err := tx.InsertRun(run2, w.NewID(), "seal-resume-hash"); err != nil {
			return err
		}
		return tx.SaveStep(w.Step{ID: w.NewID(), RunID: run2.ID, AgentID: run2.Agents[0].ID, AgentName: run2.Agents[0].DisplayName, Role: "responder", Instruction: "resume and recover", Status: "pending"})
	}); err != nil {
		t.Fatal(err)
	}

	got := awaitRun(t, db, run2.ID)
	if got.Status != "succeeded" {
		t.Fatalf("resumed run failed after auto-seal: status=%s err=%s modes=%v", got.Status, got.Error, modes)
	}
	if len(modes) != 3 || modes[0] != "fresh" || modes[1] != "resume" || modes[2] != "fresh" {
		t.Fatalf("observed session modes = %v, want [fresh resume fresh]", modes)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		sessions, err := tx.Sessions(run2.TaskID)
		if err != nil {
			return err
		}
		var sealed, replacement *w.ExecutionSession
		for index := range sessions {
			switch sessions[index].NativeSessionID {
			case brokenSessionID:
				sealed = &sessions[index]
			case freshSessionID:
				replacement = &sessions[index]
			}
		}
		if sealed == nil || sealed.Status != "sealed" {
			t.Fatalf("broken session was not sealed: %+v", sessions)
		}
		if replacement == nil || replacement.Status != "active" || replacement.Acknowledgement != "fresh_acknowledged" {
			t.Fatalf("replacement session was not fresh_acknowledged active: %+v", replacement)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
