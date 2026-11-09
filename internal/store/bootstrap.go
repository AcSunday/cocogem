package store

import (
	"database/sql"
	"errors"
)

// BootstrapReceipt retains only identities; the response is always rebuilt from
// the current project, member and conversation records.
type BootstrapReceipt struct {
	WorkspaceID string
	ThreadID    string
}

// BootstrapReceipt reads a stable first-input request without exposing arbitrary JSON.
func (t *Tx) BootstrapReceipt(key string) (BootstrapReceipt, string, error) {
	var out BootstrapReceipt
	var digest string
	err := t.tx.QueryRowContext(t.ctx, "SELECT workspace_id,thread_id,request_digest FROM bootstrap_receipts WHERE key=?", key).Scan(&out.WorkspaceID, &out.ThreadID, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return out, "", ErrNotFound
	}
	if err != nil {
		return out, "", err
	}
	return out, digest, nil
}

// SaveBootstrapReceipt atomically records the first-input replay identity.
func (t *Tx) SaveBootstrapReceipt(key, digest, workspaceID, threadID string) error {
	return t.exec("INSERT INTO bootstrap_receipts(key,request_digest,workspace_id,thread_id) VALUES(?,?,?,?)", key, digest, workspaceID, threadID)
}
