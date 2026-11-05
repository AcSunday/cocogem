package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
			if err := e.cleanupRevokedNativeSessions(ctx, grant.WorkspaceID, grant.SubjectMemberID); err != nil {
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

// cleanupRevokedNativeSessions removes only server-owned CLI state. It never
// follows a symlinked storage root or claims to erase terminal/provider history.
func (e *Engine) cleanupRevokedNativeSessions(ctx context.Context, workspaceID, memberID string) error {
	if e.NativeSessionRoot == "" {
		return nil
	}
	root, err := filepath.Abs(e.NativeSessionRoot)
	if err != nil {
		return errors.New("resolve managed native session root")
	}
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed native session root is not a private directory")
	}
	ids := []string{}
	if err := e.Store.Read(ctx, func(tx *store.Tx) error {
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
				if session.MemberID == memberID && (session.Status == "revoked" || session.Status == "quarantined") {
					ids = append(ids, session.ID)
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	var cleanupErr error
	for _, id := range ids {
		if id == "" || filepath.Base(id) != id || strings.Contains(id, string(filepath.Separator)) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("invalid managed native session identity %q", id))
			continue
		}
		path := filepath.Join(root, id)
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			cleanupErr = errors.Join(cleanupErr, errors.New("managed native session escaped its private root"))
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}
