package project

import (
	"context"
	"errors"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// SubmitInboxInput is one user steering operation, applied only at a declared boundary.
type SubmitInboxInput struct {
	SourceMessageID   string `json:"source_message_id"`
	ExpectedRevision  int    `json:"expected_revision"`
	Kind              string `json:"kind"`
	Body              string `json:"body"`
	ApplicationPolicy string `json:"application_policy"`
	IdempotencyKey    string `json:"idempotency_key"`
}

// SubmitInbox durably accepts input even while a native invocation cannot receive it.
func (s *Service) SubmitInbox(ctx context.Context, taskID string, in SubmitInboxInput) (w.InboxItem, error) {
	var item w.InboxItem
	if strings.TrimSpace(in.Body) == "" || len([]rune(in.Body)) > 16000 || in.SourceMessageID == "" || in.IdempotencyKey == "" {
		return item, errorf("invalid_request", "inbox requires a body, source message and stable idempotency key")
	}
	if in.ApplicationPolicy == "" {
		in.ApplicationPolicy = "next_boundary"
	}
	if in.Kind == "" {
		in.Kind = "supplement"
	}
	if !map[string]bool{"next_boundary": true, "interrupt": true, "follow_up": true}[in.ApplicationPolicy] {
		return item, errorf("invalid_request", "unknown inbox application policy")
	}
	requestDigest := Digest(in)
	err := s.store.Write(ctx, func(tx *store.Tx) error {
		task, err := tx.Task(taskID)
		if err != nil {
			return err
		}
		if previous, err := tx.InboxByKey(taskID, in.IdempotencyKey); err == nil {
			if previous.RequestDigest != requestDigest {
				return errorf("conflict", "the same inbox key was reused for different input")
			}
			item = previous
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if task.Status == "paused" || task.Status == "canceled" {
			return errorf("authority_revoked", "paused or canceled tasks do not accept new execution input")
		}
		if in.ExpectedRevision != task.CurrentRevision {
			return errorf("revision_conflict", "inbox input targets an older task revision")
		}
		now := s.now()
		item = w.InboxItem{
			ID: w.NewID(), TaskID: taskID, SourceMessageID: in.SourceMessageID,
			ExpectedRevision: in.ExpectedRevision, Kind: in.Kind, Body: in.Body,
			ApplicationPolicy: in.ApplicationPolicy, Status: "pending", RunID: task.CurrentRunID,
			IdempotencyKey: in.IdempotencyKey, RequestDigest: requestDigest, CreatedAt: now, UpdatedAt: now,
		}
		return tx.SaveInbox(item)
	})
	return item, err
}

// Inbox returns durable steering receipts, including items not yet applied by a runner.
func (s *Service) Inbox(ctx context.Context, taskID string) ([]w.InboxItem, error) {
	var items []w.InboxItem
	err := s.store.Read(ctx, func(tx *store.Tx) error {
		if _, err := tx.Task(taskID); err != nil {
			return err
		}
		var err error
		items, err = tx.Inbox(taskID)
		return err
	})
	return items, err
}
