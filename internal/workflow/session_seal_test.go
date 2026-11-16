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
