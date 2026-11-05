package project

import (
	"context"
	"slices"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Grants returns the durable project authorization audit view.
func (s *Service) Grants(ctx context.Context, workspaceID string) ([]w.Grant, error) {
	var grants []w.Grant
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		grants, err = tx.Grants(workspaceID)
		return err
	})
	return grants, err
}

// BeginRevocation first advances the epoch and closes admission. Process
// cancellation is deliberately performed by the caller outside this transaction.
func (s *Service) BeginRevocation(ctx context.Context, workspaceID, memberID, source string) (w.Grant, error) {
	if source == "" {
		return w.Grant{}, errorf("invalid_request", "grant revocation requires a user source")
	}
	var grant w.Grant
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		grant, err = tx.Grant(grantID(workspaceID, memberID))
		if err != nil {
			return err
		}
		if grant.Status == "revoked" || grant.Status == "revoking" || grant.Status == "revocation_uncertain" {
			return nil
		}
		grant.Status = "revoking"
		grant.SourceMessageID = source
		grant.Epoch++
		grant.Revision++
		if err := tx.SaveGrant(grant); err != nil {
			return err
		}
		tasks, err := tx.Tasks(workspaceID)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			sessions, err := tx.Sessions(task.ID)
			if err != nil {
				return err
			}
			for _, session := range sessions {
				if session.MemberID != memberID || session.Status == "revoked" {
					continue
				}
				session.Status = "revoked"
				session.UpdatedAt = s.now()
				if err := tx.SaveSession(session); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return grant, err
}

// FinishRevocation settles only after no related member step is still running.
// An uncertain stop remains visible and retryable; it is never reported as revoked.
func (s *Service) FinishRevocation(ctx context.Context, workspaceID, memberID string, stopUncertain bool) (w.Grant, error) {
	var grant w.Grant
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		var err error
		grant, err = tx.Grant(grantID(workspaceID, memberID))
		if err != nil {
			return err
		}
		if grant.Status == "revoked" {
			return nil
		}
		if grant.Status != "revoking" && grant.Status != "revocation_uncertain" {
			return errorf("conflict", "grant is not undergoing revocation")
		}
		uncertain := stopUncertain
		if !uncertain {
			runs, err := tx.Active()
			if err != nil {
				return err
			}
			for _, run := range runs {
				if run.WorkspaceID != workspaceID || !slices.ContainsFunc(run.Agents, func(member w.Agent) bool { return member.ID == memberID }) {
					continue
				}
				steps, err := tx.Steps(run.ID)
				if err != nil {
					return err
				}
				if slices.ContainsFunc(steps, func(step w.Step) bool {
					return step.AgentID == memberID && step.Status == "running"
				}) {
					uncertain = true
					break
				}
			}
		}
		grant.Status = "revoked"
		grant.RevokedAt = s.now()
		if uncertain {
			grant.Status = "revocation_uncertain"
			grant.RevokedAt = ""
		}
		grant.Revision++
		return tx.SaveGrant(grant)
	})
	return grant, err
}
