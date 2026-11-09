package workflow

import (
	"context"

	"github.com/AcSunday/cocogem/internal/contextplan"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// prepareRetryContext creates a new execution segment for the same accepted
// objective. Current effective project records are rebound, while a changed
// task revision fails closed instead of silently retargeting this retry.
func prepareRetryContext(tx *store.Tx, run *w.Run) error {
	if run.TaskID == "" {
		// An ordinary conversation (intake, no durable Task yet) still carries a
		// frozen read-only authorization. A retry is a new Run, so it must freeze
		// the live policy and the intake ceiling itself instead of inheriting an
		// empty identity; it does not create a Task or a budget account.
		if !run.TaskIntake {
			return nil
		}
		return project.New(nil).EnsureRunAuthorization(tx, run, "retry:"+run.ContinuedFromRun)
	}
	revision, err := tx.TaskRevision(run.TaskID, run.TaskRevision)
	if err != nil {
		return err
	}
	authorization := project.New(nil)
	if err := authorization.EnsureRunAuthorization(tx, run, revision.SourceMessageID); err != nil {
		return err
	}
	manifest, err := authorization.PrepareContextInTx(tx, run.TaskID, run.TaskRevision, project.ContextHostInput(*run))
	if err != nil {
		return err
	}
	run.ContextRef = manifest.ID
	run.InputDigest = project.ContextInputDigest(project.TaskView{Revision: revision}, manifest, revision.SourceMessageID, run.Input)
	return nil
}

func filterAuthorizedTools(tx *store.Tx, run w.Run, memberID string, names []string) ([]string, error) {
	return project.New(nil).FilterAuthorizedTools(tx, run, memberID, names)
}

// runContextBrief reads only the manifest frozen for this Run. Legacy Runs
// without a manifest retain their original recovery behavior.
func (e *Engine) runContextBrief(ctx context.Context, run w.Run) (string, error) {
	if run.ContextRef == "" {
		return "", nil
	}
	var brief string
	err := e.Store.Read(ctx, func(tx *store.Tx) error {
		manifest, err := tx.ContextManifest(run.ContextRef)
		if err != nil {
			return err
		}
		if manifest.TaskID != run.TaskID || manifest.TaskRevision != run.TaskRevision {
			return w.ErrConflict
		}
		brief = manifest.Brief
		return nil
	})
	return brief, err
}

// readSourcePage keeps the existing per-step scope check and additionally
// binds project-context reads to an exact manifest entry and durable receipt.
func (e *Engine) readSourcePage(ctx context.Context, run w.Run, source w.Source, cursor string, maxBytes int) (w.TextPage, error) {
	if run.ContextRef == "" {
		return w.PageText(w.TextRef{Kind: "source", ID: source.ID}, source.Content, cursor, maxBytes)
	}
	var page w.TextPage
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		manifest, err := tx.ContextManifest(run.ContextRef)
		if err != nil {
			return err
		}
		if manifest.TaskID != run.TaskID || manifest.TaskRevision != run.TaskRevision {
			return w.ErrConflict
		}
		appended, err := tx.ContextScopeEntries(manifest.ID)
		if err != nil {
			return err
		}
		manifest.Items = append(manifest.Items, appended...)
		var receipt w.ReadReceipt
		page, receipt, err = contextplan.ReadPage(manifest, source.ID, source.Content, contextplan.Digest(source.Content), cursor, maxBytes)
		if err != nil {
			return err
		}
		receipt.ID = w.NewID()
		receipt.CreatedAt = w.Now()
		return tx.SaveReadReceipt(receipt)
	})
	return page, err
}
