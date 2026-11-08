package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// SaveSourceImport persists an original independently of any execution. The
// per-thread bound keeps metadata listing and local storage finite.
func (t *Tx) SaveSourceImport(v w.SourceImport) error {
	var count int
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM source_imports WHERE thread_id=?", v.ThreadID).Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		return w.ErrConflict
	}
	return t.exec("INSERT INTO source_imports(id,workspace_id,thread_id,body) VALUES(?,?,?,?)", v.ID, v.WorkspaceID, v.ThreadID, body(v))
}

// SourceImport reads the original and exact parse outcome.
func (t *Tx) SourceImport(id string) (w.SourceImport, error) {
	return get[w.SourceImport](t, "SELECT body FROM source_imports WHERE id=?", id)
}

// SourceImportMetadata omits both derived and original bodies from list views.
func (t *Tx) SourceImportMetadata(threadID string) ([]w.SourceImport, error) {
	return list[w.SourceImport](t, "SELECT json_remove(body,'$.content','$.original_content') FROM source_imports WHERE thread_id=? ORDER BY rowid LIMIT 100", threadID)
}

// DeleteSourceImport permits deletion only before an immutable Source exists.
func (t *Tx) DeleteSourceImport(id string) error {
	if _, err := t.Source(id); err == nil {
		return w.ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return t.exec("DELETE FROM source_imports WHERE id=?", id)
}

// PublishSourceImports freezes selected successful imports into the first Run.
// Existing captures are verified rather than inserted or rewritten.
func (t *Tx) PublishSourceImports(run w.Run, ids []string) error {
	if len(ids) > 5 {
		return w.ErrConflict
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return w.ErrConflict
		}
		seen[id] = true
		imported, err := t.SourceImport(id)
		if err != nil {
			return err
		}
		if imported.ThreadID != run.ThreadID || imported.WorkspaceID != run.WorkspaceID || imported.OriginalContent == "" {
			return w.ErrConflict
		}
		if existing, err := t.Source(id); err == nil {
			if existing.ThreadID != run.ThreadID || existing.WorkspaceID != run.WorkspaceID || existing.OriginalDigest != imported.OriginalDigest || existing.ContentDigest != imported.ContentDigest || existing.ParseStatus != imported.ParseStatus {
				return w.ErrConflict
			}
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		sum := sha256.Sum256([]byte(imported.Content))
		contentDigest := imported.ContentDigest
		if contentDigest == "" {
			contentDigest = hex.EncodeToString(sum[:])
		}
		source := w.Source{
			ID: imported.ID, WorkspaceID: imported.WorkspaceID, ThreadID: imported.ThreadID, RunID: run.ID,
			Kind: imported.Kind, Title: imported.Title, Path: imported.FileName, MediaType: imported.MediaType,
			StartLine: imported.StartLine, EndLine: imported.EndLine, SHA256: imported.SHA256,
			Content: imported.Content, ContentDigest: contentDigest, CapturedAt: imported.CapturedAt, Truncated: imported.Truncated,
			OriginalContent: imported.OriginalContent, OriginalDigest: imported.OriginalDigest,
			ParseStatus: imported.ParseStatus, ParseError: imported.ParseError, ParseQuality: imported.ParseQuality,
			ParserVersion: imported.ParserVersion, SourceLocator: imported.SourceLocator, Visibility: imported.Visibility,
		}
		if err := t.SaveSource(source); err != nil {
			return err
		}
	}
	return nil
}
