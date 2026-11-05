package project

import (
	"context"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// CollaborationMessage returns the immutable envelope together with every
// per-recipient observation. handled remains distinct from task completion.
func (s *Service) CollaborationMessage(ctx context.Context, id string) (MessageView, error) {
	var view MessageView
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		view.Message, err = tx.CollaborationMessage(id)
		if err != nil {
			return err
		}
		view.Receipts, err = tx.Receipts(id)
		return err
	})
	return view, err
}

func (s *Service) Questions(ctx context.Context, taskID string) ([]w.Question, error) {
	var items []w.Question
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		items, err = tx.Questions(taskID)
		return err
	})
	return items, err
}

func (s *Service) Wakeups(ctx context.Context, taskID string) ([]w.Wakeup, error) {
	var items []w.Wakeup
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		items, err = tx.Wakeups(taskID)
		return err
	})
	return items, err
}

func (s *Service) Execution(ctx context.Context, id string) (w.WorkspaceExecution, error) {
	var execution w.WorkspaceExecution
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		execution, err = tx.Execution(id)
		return err
	})
	return execution, err
}

func (s *Service) Effect(ctx context.Context, id string) (w.EffectRecord, error) {
	var effect w.EffectRecord
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		var err error
		effect, err = tx.Effect(id)
		return err
	})
	return effect, err
}

// ObserveUserWakeEvent records an authenticated browser observation as such.
// The caller cannot promote its payload to host_observed evidence.
func (s *Service) ObserveUserWakeEvent(ctx context.Context, source, eventID, observation, evidenceRef string) (w.Run, error) {
	if source == "" || eventID == "" || observation == "" || evidenceRef == "" {
		return w.Run{}, errorf("invalid_request", "wake event requires source, event ID, observation and evidence reference")
	}
	if len(observation) > 16000 || len(evidenceRef) > 1024 || len(source) > 256 || len(eventID) > 256 {
		return w.Run{}, errorf("invalid_request", "wake event exceeds its bounded observation contract")
	}
	return s.WakeTask(ctx, source, eventID, "user_observation:"+observation+" evidence:"+evidenceRef)
}
