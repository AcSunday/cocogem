package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// SavePermissionRequest upserts one request. The indexed identity/scope columns
// are the query keys; the body keeps the complete object.
func (t *Tx) SavePermissionRequest(v w.ToolPermissionRequest) error {
	return t.exec(`INSERT INTO tool_permission_requests(id,thread_id,workspace_id,run_id,step_id,attempt_id,member_id,tool_call_id,action,tool_name,normalized_scope,arguments_digest,status,permission_epoch,created_at,revision,body)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status,revision=excluded.revision,body=excluded.body`,
		v.ID, v.ThreadID, v.WorkspaceID, v.RunID, v.StepID, v.AttemptID, v.MemberID, v.ToolCallID,
		v.Action, v.ToolName, v.NormalizedResourceScope, v.ArgumentsDigest,
		v.Status, v.PermissionEpoch, v.CreatedAt, v.Revision, body(v))
}

// PermissionRequest reads one request by identity.
func (t *Tx) PermissionRequest(id string) (w.ToolPermissionRequest, error) {
	return get[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE id=?", id)
}

// ThreadPermissionRequests lists a thread's requests: pending first, then the
// most recent settled history. The bound keeps the projection small.
func (t *Tx) ThreadPermissionRequests(threadID string, limit int) ([]w.ToolPermissionRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	pending, err := list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE thread_id=? AND status='pending' ORDER BY created_at,id", threadID)
	if err != nil {
		return nil, err
	}
	settled, err := list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE thread_id=? AND status<>'pending' ORDER BY created_at DESC,id DESC LIMIT ?", threadID, limit)
	if err != nil {
		return nil, err
	}
	slices.Reverse(settled)
	return append(pending, settled...), nil
}

// RunPendingPermissionRequests lists one run's unsettled requests, used when a
// run finishes so nothing stays pending after its execution ended.
func (t *Tx) RunPendingPermissionRequests(runID string) ([]w.ToolPermissionRequest, error) {
	return list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE run_id=? AND status='pending' ORDER BY created_at,id", runID)
}

// PermissionRequestsByInvocation finds the requests bound to one exact call
// identity. Native ids restart at native-1 per attempt, so run alone is not an
// identity: run+step+attempt+tool_call is.
func (t *Tx) PermissionRequestsByInvocation(runID, stepID, attemptID, toolCallID string) ([]w.ToolPermissionRequest, error) {
	return list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE run_id=? AND step_id=? AND attempt_id=? AND tool_call_id=? ORDER BY created_at,id", runID, stepID, attemptID, toolCallID)
}

// PermissionRequestsByRunCall lists every request of one run and call id. It is
// only used to report identity mismatches, never to bind an allowance.
func (t *Tx) PermissionRequestsByRunCall(runID, toolCallID string) ([]w.ToolPermissionRequest, error) {
	return list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE run_id=? AND tool_call_id=? ORDER BY created_at,id", runID, toolCallID)
}

// PendingPreApprovals lists this invocation's pending request_tool_access
// approvals so an identical described operation is not asked twice.
func (t *Tx) PendingPreApprovals(runID, stepID, attemptID string) ([]w.ToolPermissionRequest, error) {
	return list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE run_id=? AND step_id=? AND attempt_id=? AND status='pending' AND COALESCE(json_extract(body,'$.pre_approval'),0)=1 ORDER BY created_at,id", runID, stepID, attemptID)
}

// AllowedOncePreApprovals finds an approved-but-unconsumed pre-approval for the
// exact described operation. The real call has a different call identity than
// the request tool, so the immutable operation (action, tool, scope, argument
// digest) plus run/step/attempt and epoch is the binding.
func (t *Tx) AllowedOncePreApprovals(runID, stepID, attemptID, action, tool, scope, digest string, epoch int) ([]w.ToolPermissionRequest, error) {
	return list[w.ToolPermissionRequest](t, `SELECT body FROM tool_permission_requests
		WHERE run_id=? AND step_id=? AND attempt_id=? AND action=? AND tool_name=? AND normalized_scope=? AND arguments_digest=?
		AND permission_epoch=? AND status='allowed_once' AND COALESCE(json_extract(body,'$.pre_approval'),0)=1
		ORDER BY created_at,id`, runID, stepID, attemptID, action, tool, scope, digest, epoch)
}

// AdmissionByRequest reads the single admission of a request, if any. Exactly
// one call may ever consume one approval.
func (t *Tx) AdmissionByRequest(requestID string) (w.ToolInvocationAdmission, error) {
	return get[w.ToolInvocationAdmission](t, "SELECT body FROM tool_invocation_admissions WHERE permission_request_id=?", requestID)
}

// ExpirePendingRequests settles every pending request of another epoch.
func (t *Tx) ExpirePendingRequests(now string, epoch int) error {
	rows, err := list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE status='pending' AND permission_epoch<>?", epoch)
	if err != nil {
		return err
	}
	for _, request := range rows {
		request.Status, request.ResolvedAt, request.Revision = w.PermissionExpired, now, request.Revision+1
		if err := t.SavePermissionRequest(request); err != nil {
			return err
		}
	}
	return nil
}

// SaveConversationGrant stores one thread-scoped grant.
func (t *Tx) SaveConversationGrant(v w.ConversationGrant) error {
	return t.exec(`INSERT INTO conversation_grants(id,thread_id,action,tool_matcher,normalized_scope,permission_epoch,status,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status,body=excluded.body`,
		v.ID, v.ThreadID, v.Action, v.ToolMatcher, v.NormalizedResourceScope, v.PermissionEpoch, v.Status, v.CreatedAt, body(v))
}

// ConversationGrant reads one grant by identity.
func (t *Tx) ConversationGrant(id string) (w.ConversationGrant, error) {
	return get[w.ConversationGrant](t, "SELECT body FROM conversation_grants WHERE id=?", id)
}

// ActiveConversationGrant finds a matching active grant for an exact thread,
// tool, action, scope and live epoch. A grant for one tool never admits another
// tool that happens to share the same broad action category.
func (t *Tx) ActiveConversationGrant(threadID, tool, action, scope string, epoch int) (w.ConversationGrant, error) {
	var encoded string
	err := t.tx.QueryRowContext(t.ctx, "SELECT body FROM conversation_grants WHERE thread_id=? AND tool_matcher=? AND action=? AND normalized_scope=? AND permission_epoch=? AND status='active' ORDER BY created_at,id LIMIT 1", threadID, tool, action, scope, epoch).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return w.ConversationGrant{}, ErrNotFound
	}
	if err != nil {
		return w.ConversationGrant{}, err
	}
	var value w.ConversationGrant
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return w.ConversationGrant{}, err
	}
	return value, nil
}

// ThreadConversationGrants lists a thread's grants, newest first.
func (t *Tx) ThreadConversationGrants(threadID string) ([]w.ConversationGrant, error) {
	return list[w.ConversationGrant](t, "SELECT body FROM conversation_grants WHERE thread_id=? ORDER BY created_at DESC,id DESC", threadID)
}

// RevokeConversationGrantsForThread revokes every active grant of a thread.
func (t *Tx) RevokeConversationGrantsForThread(threadID, now string) error {
	rows, err := list[w.ConversationGrant](t, "SELECT body FROM conversation_grants WHERE thread_id=? AND status='active'", threadID)
	if err != nil {
		return err
	}
	for _, grant := range rows {
		grant.Status, grant.RevokedAt = "revoked", now
		if err := t.SaveConversationGrant(grant); err != nil {
			return err
		}
	}
	return nil
}

// ExpireConversationGrants revokes grants whose epoch is no longer current.
func (t *Tx) ExpireConversationGrants(now string, epoch int) error {
	rows, err := list[w.ConversationGrant](t, "SELECT body FROM conversation_grants WHERE status='active' AND permission_epoch<>?", epoch)
	if err != nil {
		return err
	}
	for _, grant := range rows {
		grant.Status, grant.RevokedAt = "revoked", now
		if err := t.SaveConversationGrant(grant); err != nil {
			return err
		}
	}
	return nil
}

// SaveAdmission inserts or updates one admission record.
func (t *Tx) SaveAdmission(v w.ToolInvocationAdmission) error {
	return t.exec(`INSERT INTO tool_invocation_admissions(id,permission_request_id,tool_call_id,run_id,step_id,attempt_id,status,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status,body=excluded.body`,
		v.ID, v.PermissionRequestID, v.ToolCallID, v.RunID, v.StepID, v.AttemptID, v.Status, v.ReservedAt, body(v))
}

// AdmissionByRequestCall reads the single admission for a request and call.
func (t *Tx) AdmissionByRequestCall(requestID, toolCallID string) (w.ToolInvocationAdmission, error) {
	return get[w.ToolInvocationAdmission](t, "SELECT body FROM tool_invocation_admissions WHERE permission_request_id=? AND tool_call_id=?", requestID, toolCallID)
}

// Admission reads one admission by identity.
func (t *Tx) Admission(id string) (w.ToolInvocationAdmission, error) {
	return get[w.ToolInvocationAdmission](t, "SELECT body FROM tool_invocation_admissions WHERE id=?", id)
}

// ReserveAdmission consumes a one-time allowance. The request id is unique in
// this table, so a duplicate attempt (same or different call) receives
// created=false instead of a second execution.
func (t *Tx) ReserveAdmission(v w.ToolInvocationAdmission) (bool, error) {
	result, err := t.tx.ExecContext(t.ctx, `INSERT INTO tool_invocation_admissions(id,permission_request_id,tool_call_id,run_id,step_id,attempt_id,status,created_at,body)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(permission_request_id) DO NOTHING`,
		v.ID, v.PermissionRequestID, v.ToolCallID, v.RunID, v.StepID, v.AttemptID, v.Status, v.ReservedAt, body(v))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// AdmissionsByRun lists every admission of one run in creation order. It is the
// audit projection for "one decision, one effect".
func (t *Tx) AdmissionsByRun(runID string) ([]w.ToolInvocationAdmission, error) {
	return list[w.ToolInvocationAdmission](t, "SELECT body FROM tool_invocation_admissions WHERE run_id=? ORDER BY created_at,id", runID)
}

// PendingPreApprovalsOrRequests lists a thread's pending requests for the test
// helper that plays the browser click; production uses ThreadPermissionRequests.
func (t *Tx) PendingPreApprovalsOrRequests(threadID string) ([]w.ToolPermissionRequest, error) {
	return list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE thread_id=? AND status='pending' ORDER BY created_at,id", threadID)
}

// ReadAdmission reads one admission through its own transaction; used by
// settlement tests and reconciliation.
func (s *Store) ReadAdmission(ctx context.Context, id string) (w.ToolInvocationAdmission, error) {
	var value w.ToolInvocationAdmission
	err := s.Read(ctx, func(tx *Tx) error {
		v, err := tx.Admission(id)
		value = v
		return err
	})
	return value, err
}

// PendingPermissionRequestsMatching lists pending requests of a thread for one
// exact operation, excluding the source request. A conversation grant settles
// them so a second member does not have to ask again.
func (t *Tx) PendingPermissionRequestsMatching(threadID, action, tool, scope string, epoch int, excludeID string) ([]w.ToolPermissionRequest, error) {
	return list[w.ToolPermissionRequest](t, `SELECT body FROM tool_permission_requests
		WHERE thread_id=? AND status='pending' AND action=? AND tool_name=? AND normalized_scope=? AND permission_epoch=? AND id<>?
		ORDER BY created_at,id`, threadID, action, tool, scope, epoch, excludeID)
}

// CancelThreadPendingRequests settles a thread's pending permission requests.
// A soft-deleted conversation must not keep an actionable request alive.
func (t *Tx) CancelThreadPendingRequests(threadID, now string) error {
	rows, err := list[w.ToolPermissionRequest](t, "SELECT body FROM tool_permission_requests WHERE thread_id=? AND status='pending'", threadID)
	if err != nil {
		return err
	}
	for _, request := range rows {
		request.Status, request.ResolvedAt, request.Revision = w.PermissionCanceled, now, request.Revision+1
		if err := t.SavePermissionRequest(request); err != nil {
			return err
		}
	}
	return nil
}
