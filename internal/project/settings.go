package project

import (
	"context"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// SettingsPatch is the field-merge body of PATCH /api/v1/settings. A nil field
// keeps the stored value, so saving the permission page cannot clear the
// collaboration defaults and saving collaboration cannot reset the permission.
// PermissionEpoch is intentionally absent: it is server-owned.
type SettingsPatch struct {
	DefaultCoordinatorID *string                       `json:"default_coordinator_id,omitempty"`
	Assignments          *map[string]map[string]string `json:"assignments,omitempty"`
	RetentionDays        *int                          `json:"retention_days,omitempty"`
	CollaborationBudget  *w.BudgetPolicy               `json:"collaboration_budget,omitempty"`
	ExecutionMode        *string                       `json:"execution_mode,omitempty"`
	// RejectEpoch marks a body that tried to set the server-owned epoch.
	RejectEpoch bool `json:"-"`
}

// SettingsChange reports which parts of the one global record changed.
type SettingsChange struct {
	Settings        w.Settings
	PermissionMoved bool
}

// UpdateSettings applies a field-merge patch under the settings CAS revision.
// A permission boundary change increments the epoch and expires earlier pending
// requests and conversation grants in the same transaction. It never rewrites a
// workspace, an existing root budget account or any historical run snapshot.
func (s *Service) UpdateSettings(ctx context.Context, expected int, patch SettingsPatch) (SettingsChange, error) {
	var change SettingsChange
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Settings()
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return errorf("revision_conflict", "settings changed; reload before saving")
		}
		if patch.RejectEpoch {
			return errorf("invalid_request", "permission_epoch is server-owned and cannot be set by a client")
		}
		next := current
		if patch.DefaultCoordinatorID != nil {
			next.DefaultCoordinatorID = *patch.DefaultCoordinatorID
		}
		if patch.Assignments != nil {
			next.Assignments = *patch.Assignments
		}
		if patch.RetentionDays != nil {
			if err := w.ValidateRetentionDays(*patch.RetentionDays); err != nil {
				return err
			}
			next.RetentionDays = *patch.RetentionDays
		}
		if patch.CollaborationBudget != nil {
			if err := w.ValidateBudgetPolicy(*patch.CollaborationBudget); err != nil {
				return err
			}
			next.CollaborationBudget = *patch.CollaborationBudget
		}
		next = w.NormalizeExecutionMode(next)
		if patch.ExecutionMode != nil {
			if !w.ValidExecutionMode(*patch.ExecutionMode) {
				return errorf("invalid_request", "execution_mode must be controlled, project_autonomous or full_access")
			}
			if *patch.ExecutionMode != current.ExecutionMode {
				next.ExecutionMode = *patch.ExecutionMode
				next.PermissionEpoch = current.PermissionEpoch + 1
				change.PermissionMoved = true
			}
		}
		if next.Assignments == nil {
			next.Assignments = map[string]map[string]string{}
		}
		if next.CollaborationBudget.AutoContinuations == 0 {
			next.CollaborationBudget = w.DefaultBudgetPolicy()
		}
		next.RetentionDays = w.EffectiveRetentionDays(next.RetentionDays)
		next = w.NormalizeSettings(next)
		agents, err := tx.GlobalAgents()
		if err != nil {
			return err
		}
		if err := w.ValidateSettings(next, agents); err != nil {
			return err
		}
		next.Revision = current.Revision + 1
		if err := tx.SaveSettings(next); err != nil {
			return err
		}
		if change.PermissionMoved {
			now := s.now()
			if err := tx.ExpirePendingRequests(now, next.PermissionEpoch); err != nil {
				return err
			}
			if err := tx.ExpireConversationGrants(now, next.PermissionEpoch); err != nil {
				return err
			}
		}
		change.Settings = next
		return nil
	})
	return change, err
}

// RescheduleTrash applies a new retention period to conversations already in
// the trash so their displayed purge time matches the setting.
func (s *Service) RescheduleTrash(ctx context.Context, retention int) error {
	return s.store.Write(ctx, func(tx *store.Tx) error { return rescheduleTrash(tx, retention) })
}

// RescheduleTrashInTx is the transaction-scoped form used when the caller
// already owns the write transaction.
func RescheduleTrashInTx(tx *store.Tx, retention int) error { return rescheduleTrash(tx, retention) }

func rescheduleTrash(tx *store.Tx, retention int) error {
	threads, err := tx.DeletedThreads()
	if err != nil {
		return err
	}
	for _, thread := range threads {
		deleted, err := time.Parse(time.RFC3339Nano, thread.DeletedAt)
		if err != nil {
			continue
		}
		purgeAt := deleted.AddDate(0, 0, w.EffectiveRetentionDays(retention)).Format(time.RFC3339Nano)
		if thread.PurgeAt == purgeAt {
			continue
		}
		thread.PurgeAt = purgeAt
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
	}
	return nil
}

// Test helper contract: a test may change the single global permission policy
// through the same service entry as HTTP, never through a workspace route.
