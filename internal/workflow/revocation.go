package workflow

import (
	"context"
	"errors"

	"github.com/AcSunday/cocogem/internal/proc"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// RevocationStopUncertain separates Step settlement from descendant verification.
// A recorded native process group is checked, but a dead group still does not
// prove that an escaped descendant exited; that boundary remains uncertain.
func (e *Engine) RevocationStopUncertain(ctx context.Context, workspaceID, memberID string) (bool, error) {
	processes := []w.ProcessRef{}
	uncertain := false
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		runs, err := tx.WorkspaceRuns(workspaceID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			steps, err := tx.Steps(run.ID)
			if err != nil {
				return err
			}
			for _, step := range steps {
				if step.AgentID != memberID {
					continue
				}
				if step.Status == "running" {
					uncertain = true
				}
				attempts, err := tx.StepAttempts(step.ID)
				if err != nil {
					return err
				}
				for _, attempt := range attempts {
					if attempt.Process != nil {
						processes = append(processes, *attempt.Process)
					}
				}
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	if uncertain {
		return true, nil
	}
	for _, process := range processes {
		if process.PGID <= 0 {
			return true, nil
		}
		alive, err := proc.GroupAlive(process.PGID)
		if err != nil || alive {
			return true, nil
		}
		return true, nil
	}
	return false, nil
}

func (e *Engine) reconcileRevocations(ctx context.Context) error {
	var pending []w.Grant
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
		var err error
		pending, err = tx.PendingGrants()
		return err
	}); err != nil {
		return err
	}
	service := project.New(e.Store)
	var result error
	for _, grant := range pending {
		stopUncertain, err := e.RevocationStopUncertain(ctx, grant.WorkspaceID, grant.SubjectMemberID)
		if err != nil {
			result = errors.Join(result, err)
			stopUncertain = true
		}
		if err := e.CancelMember(ctx, grant.WorkspaceID, grant.SubjectMemberID); err != nil {
			result = errors.Join(result, err)
			stopUncertain = true
		}
		if !stopUncertain {
			if err := e.sealRevokedNativeSessions(ctx, grant.WorkspaceID, grant.SubjectMemberID); err != nil {
				result = errors.Join(result, err)
				stopUncertain = true
			}
		}
		if _, err := service.FinishRevocation(ctx, grant.WorkspaceID, grant.SubjectMemberID, stopUncertain); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

// sealRevokedNativeSessions retires the revoked member's session rows.
//
// Native CLI state now lives in each CLI's own directory (~/.claude, ~/.codex,
// ...), which belongs to the user: removal only marks the Cocogem ledger as
// revoked so no --resume is ever attempted again, and the host never touches
// those directories.
func (e *Engine) sealRevokedNativeSessions(ctx context.Context, workspaceID, memberID string) error {
	return e.Store.Write(ctx, func(tx *store.Tx) error {
		sessions, err := tx.SessionsByMember(workspaceID, memberID)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if session.Status == "revoked" {
				continue
			}
			session.Status = "revoked"
			session.NativeSessionID = ""
			session.Acknowledgement = "revoked"
			session.UpdatedAt = w.Now()
			if err := tx.SaveSession(session); err != nil {
				return err
			}
		}
		return nil
	})
}
