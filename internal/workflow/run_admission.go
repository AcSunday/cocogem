package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/AcSunday/cocogem/internal/skills"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// runAdmission is the committed state a worker needs to start a run.
type runAdmission struct {
	run     w.Run
	history []w.Message
	steps   []w.Step
}

// admitRun freezes the policy, validates the workspace and members, and moves
// the queued run to running in one transaction. It returns the latest run even
// on failure so the caller can settle or suspend the correct snapshot.
func (e *Engine) admitRun(ctx context.Context, run w.Run) (runAdmission, error) {
	var history []w.Message
	var steps []w.Step
	var adopted bool
	// The policy, start and absolute deadline are adopted and persisted in the
	// same preflight transaction, before any deadline-bound context exists.
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Thread(current.ThreadID); err != nil {
			return err
		}
		if current.Status != "queued" {
			return w.ErrConflict
		}
		currentAttempts, err := tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		initialSteps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		now := time.Now()
		adoptedAt, adoptedNow := adoptPolicy(&current, e.Limits, currentAttempts, now)
		adopted = adoptedNow
		if adoptStepClocks(initialSteps, currentAttempts, *current.ExecutionPolicy, adoptedAt) {
			for _, initial := range initialSteps {
				if err := tx.SaveStep(initial); err != nil {
					return err
				}
			}
		}
		run = current
		if run.Delivery == nil {
			// A queued run accepted under the delivery contract starts with an
			// explicit empty delivery. A failure before it succeeds must not
			// look like legacy history that exposes draft artifacts. Legacy
			// runs already succeeded stay nil and are settled by recovery.
			run.Delivery = &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}
		}
		if run.SkillSnapshot != "" {
			hash, skillErr := skills.SnapshotHash(run.Workspace.SkillIDs)
			if skillErr != nil || hash != run.SkillSnapshot {
				return errors.New("shared skill version changed before dispatch; start a new run")
			}
		}
		if e.Model == nil && len(e.Runners) == 0 && e.Fallback == nil {
			return errors.New("model is unavailable at dispatch")
		}
		if run.Input.ResearchScope == "web_discovery" && (e.Web == nil || !e.Web.SearchEnabled()) {
			return errors.New("online search is unavailable at dispatch")
		}
		if err := tools.ValidateSourceRoot(run.Workspace.Root, e.AllowedRoots); err != nil {
			return errors.New("source directory authorization was revoked")
		}
		ws, err := tx.Workspace(run.WorkspaceID)
		if err != nil {
			return err
		}
		if ws.Root != run.Workspace.Root {
			return errors.New("source binding changed before dispatch")
		}
		for _, initial := range initialSteps {
			if initial.Status == "pending" {
				if err := e.validateMember(tx, run, initial.AgentID); err != nil {
					return err
				}
			}
		}
		history, err = tx.MessagePreviews(run.ThreadID, e.historyPreviewBytes(run))
		if err != nil {
			return err
		}
		steps, err = tx.Steps(run.ID)
		if err != nil {
			return err
		}
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		kind := "run_started"
		if run.RecoveryCount > 0 {
			kind = "run_resumed"
		}
		if err := tx.AppendEvent(run.ID, "", "", kind, run); err != nil {
			return err
		}
		if adopted {
			return tx.AppendEvent(run.ID, "", "", "legacy_policy_adopted", map[string]any{"adopted_at": run.PolicyAdoptedAt, "policy": run.ExecutionPolicy})
		}
		return nil
	})
	return runAdmission{run: run, history: history, steps: steps}, err
}
