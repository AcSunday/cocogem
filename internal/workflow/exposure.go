package workflow

import (
	"context"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// recordAttemptExposure appends only after the host successfully produced the
// exact page. Failed or unauthorized reads never become evidence of exposure.
func (e *Engine) recordAttemptExposure(ctx context.Context, attemptID string, refs ...string) error {
	if attemptID == "" || len(refs) == 0 {
		return nil
	}
	return e.Store.Write(ctx, func(tx *store.Tx) error {
		attempt, err := tx.Attempt(attemptID)
		if err != nil || attempt.ExecutionSessionRef == "" {
			return err
		}
		session, err := tx.Session(attempt.ExecutionSessionRef)
		if err != nil {
			return err
		}
		if session.Status == "revoked" || session.Status == "quarantined" {
			return w.ErrConflict
		}
		session.ExposedRefs = append(session.ExposedRefs, refs...)
		slices.Sort(session.ExposedRefs)
		session.ExposedRefs = slices.Compact(session.ExposedRefs)
		session.ExposureDigest = contentDigest(strings.Join(session.ExposedRefs, "\n"))
		session.UpdatedAt = w.Now()
		return tx.SaveSession(session)
	})
}
