package store

import (
	"database/sql"
	"errors"
)

// TaskCreationReceipt makes a user's saved task stable across network retries.
func (t *Tx) TaskCreationReceipt(workspaceID, requestID string) (string, string, error) {
	var id, digest string
	err := t.tx.QueryRowContext(t.ctx, "SELECT task_id,request_digest FROM task_creation_receipts WHERE workspace_id=? AND request_id=?", workspaceID, requestID).Scan(&id, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return id, digest, nil
}

func (t *Tx) SaveTaskCreationReceipt(workspaceID, requestID, digest, taskID string) error {
	return t.exec("INSERT INTO task_creation_receipts(workspace_id,request_id,request_digest,task_id) VALUES(?,?,?,?)", workspaceID, requestID, digest, taskID)
}
