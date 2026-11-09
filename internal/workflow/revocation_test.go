package workflow

import (
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestRevocationRecheckSeparatesStepStopFromDescendantVerification(t *testing.T) {
	for _, withProcess := range []bool{false, true} {
		name := "settled steps without native process"
		if withProcess {
			name = "native process descendants remain uncertain"
		}
		t.Run(name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			run := seedCustomRun(t, db, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
			attachSessionTask(t, db, &run)
			var attempt w.Attempt
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				current, err := tx.Run(run.ID)
				if err != nil {
					return err
				}
				current.Status = "succeeded"
				current.FinishedAt = w.Now()
				if err := tx.SaveRun(current); err != nil {
					return err
				}
				steps, err := tx.Steps(run.ID)
				if err != nil {
					return err
				}
				steps[0].Status = "succeeded"
				if err := tx.SaveStep(steps[0]); err != nil {
					return err
				}
				if withProcess {
					identity, err := proc.Self()
					if err != nil {
						return err
					}
					attempt = w.Attempt{
						ID: "native-attempt", RunID: run.ID, StepID: steps[0].ID, Number: 1,
						Status: "succeeded", StartedAt: w.Now(), FinishedAt: w.Now(),
						Process: &w.ProcessRef{PID: identity.PID, PGID: identity.PGID, Start: identity.Start},
					}
					return tx.SaveAttempt(attempt)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			service := project.New(db)
			if _, err := service.BeginRevocation(t.Context(), run.WorkspaceID, run.Agents[0].ID, "revoke-source"); err != nil {
				t.Fatal(err)
			}
			engine := Engine{Store: db}
			if err := engine.reconcileRevocations(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertStatus := func(want string) {
				t.Helper()
				if err := db.Read(t.Context(), func(tx *store.Tx) error {
					grants, err := tx.Grants(run.WorkspaceID)
					if err != nil {
						return err
					}
					if len(grants) == 0 || grants[0].Status != want {
						t.Fatalf("grant status = %+v, want %s", grants, want)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if !withProcess {
				assertStatus("revoked")
				return
			}
			assertStatus("revocation_uncertain")
			if err := engine.reconcileRevocations(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertStatus("revocation_uncertain")
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				attempt.Process = nil
				return tx.SaveAttempt(attempt)
			}); err != nil {
				t.Fatal(err)
			}
			if err := engine.reconcileRevocations(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertStatus("revoked")
		})
	}
}
