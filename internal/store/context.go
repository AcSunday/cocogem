package store

import (
	"github.com/AcSunday/cocogem/internal/contextplan"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// appendSourceContext authorizes only evidence captured by this exact Run. It
// is a separate append log, so the submitted startup manifest stays immutable.
func (t *Tx) appendSourceContext(source w.Source) error {
	if source.RunID == "" {
		return nil
	}
	run, err := t.Run(source.RunID)
	if err != nil {
		return err
	}
	if run.ContextRef == "" {
		return nil
	}
	if run.WorkspaceID != source.WorkspaceID || run.ThreadID != source.ThreadID {
		return w.ErrConflict
	}
	manifest, err := t.ContextManifest(run.ContextRef)
	if err != nil {
		return err
	}
	if manifest.TaskID != run.TaskID || manifest.TaskRevision != run.TaskRevision {
		return w.ErrConflict
	}
	item := w.ContextItem{Ref: source.ID, Kind: "source", Digest: contextplan.Digest(source.Content), Mode: "ref", Reason: "managed capture by this run"}
	return t.exec("INSERT INTO context_scope_entries(manifest_id,ref,body) VALUES(?,?,?) ON CONFLICT(manifest_id,ref) DO NOTHING", manifest.ID, source.ID, body(item))
}

// ContextScopeEntries lists legal post-submission captures in append order.
func (t *Tx) ContextScopeEntries(manifestID string) ([]w.ContextItem, error) {
	return list[w.ContextItem](t, "SELECT body FROM context_scope_entries WHERE manifest_id=? ORDER BY rowid", manifestID)
}
