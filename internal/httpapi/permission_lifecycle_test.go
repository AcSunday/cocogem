package httpapi

import (
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// permissionLifecycleFixture seeds a conversation with an active conversation
// grant and a pending request, plus a completed run they reference.
func permissionLifecycleFixture(t *testing.T) (*deliveryAPI, *store.Store, string, string) {
	t.Helper()
	api, db, workspaceID, threadID, _ := permissionFixture(t)
	run := w.Run{ID: "lifecycle-run", ThreadID: threadID, WorkspaceID: workspaceID, Status: "succeeded", CreatedAt: w.Now(), FinishedAt: w.Now()}
	request := w.ToolPermissionRequest{
		ID: "lifecycle-request", ThreadID: threadID, WorkspaceID: workspaceID, RunID: run.ID,
		StepID: "step", AttemptID: "attempt", MemberID: "member", ToolCallID: "call",
		Origin: w.OriginHost, Action: "write", ToolName: "write_file",
		NormalizedResourceScope: "/tmp/outside", ArgumentsDigest: "digest",
		PermissionEpoch: 1, Status: w.PermissionPending, Revision: 1, CreatedAt: w.Now(),
	}
	grant := w.ConversationGrant{ID: "lifecycle-grant", ThreadID: threadID, Action: "write", ToolMatcher: "write_file",
		NormalizedResourceScope: "/tmp/outside", PermissionEpoch: 1, SourceRequestID: request.ID, Status: "active", CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, "lifecycle-key", "lifecycle-hash"); err != nil {
			return err
		}
		if err := tx.SavePermissionRequest(request); err != nil {
			return err
		}
		return tx.SaveConversationGrant(grant)
	}); err != nil {
		t.Fatal(err)
	}
	return api, db, workspaceID, threadID
}

// TestTrashRevokesConversationAuthority proves a soft delete ends execution
// authority but keeps the history readable, and a restore never revives it.
func TestTrashRevokesConversationAuthority(t *testing.T) {
	api, db, _, threadID := permissionLifecycleFixture(t)
	var reply DeleteReply
	api.call("DELETE", "/api/v1/threads/"+threadID, nil, map[string]string{"If-Match": "1"}, 200, &reply)
	if reply.PurgeAt == "" {
		t.Fatal("trash did not schedule a purge time")
	}
	var request w.ToolPermissionRequest
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		request, err = tx.PermissionRequest("lifecycle-request")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if request.Status != w.PermissionCanceled {
		t.Fatalf("pending request survived the trash: %+v", request)
	}
	var grant w.ConversationGrant
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		grant, err = tx.ConversationGrant("lifecycle-grant")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if grant.Status != "revoked" || grant.RevokedAt == "" {
		t.Fatalf("conversation grant survived the trash: %+v", grant)
	}
	// Restore keeps the records readable but the authority stays revoked.
	api.call("POST", "/api/v1/threads/"+threadID+"/restore", map[string]any{}, map[string]string{"If-Match": "2"}, 200, nil)
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		grant, err = tx.ConversationGrant("lifecycle-grant")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if grant.Status != "revoked" {
		t.Fatalf("restore revived a revoked grant: %+v", grant)
	}
}

// TestPermanentDeleteRemovesChildrenFirst proves the permission rows do not break
// the foreign keys on permanent deletion.
func TestPermanentDeleteRemovesChildrenFirst(t *testing.T) {
	api, db, _, threadID := permissionLifecycleFixture(t)
	api.call("DELETE", "/api/v1/threads/"+threadID, nil, map[string]string{"If-Match": "1"}, 200, nil)
	api.call("DELETE", "/api/v1/threads/"+threadID+"/permanent", nil, map[string]string{"If-Match": "2"}, 200, nil)
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.PermissionRequest("lifecycle-request"); err == nil {
			t.Fatal("permission request survived permanent deletion")
		}
		if _, err := tx.ConversationGrant("lifecycle-grant"); err == nil {
			t.Fatal("conversation grant survived permanent deletion")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestWorkspaceDeletionRemovesPermissionRows proves workspace destruction also
// clears the permission tables through its threads.
func TestWorkspaceDeletionRemovesPermissionRows(t *testing.T) {
	api, db, workspaceID, threadID := permissionLifecycleFixture(t)
	api.call("DELETE", "/api/v1/workspaces/"+workspaceID, nil, nil, 200, nil)
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.PermissionRequest("lifecycle-request"); err == nil {
			t.Fatal("permission request survived workspace deletion")
		}
		if _, err := tx.ConversationGrant("lifecycle-grant"); err == nil {
			t.Fatal("conversation grant survived workspace deletion")
		}
		if _, err := tx.ThreadAny(threadID); err == nil {
			t.Fatal("conversation survived workspace deletion")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestExpiredTrashPurgeRemovesPermissionRows proves retention cleanup removes the
// permission rows before the run/thread rows.
func TestExpiredTrashPurgeRemovesPermissionRows(t *testing.T) {
	api, db, _, threadID := permissionLifecycleFixture(t)
	_ = api
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		thread, err := tx.ThreadAny(threadID)
		if err != nil {
			return err
		}
		// Older than the retention window, so the reschedule step keeps it expired.
		past := time.Now().AddDate(0, 0, -30).UTC().Format(time.RFC3339Nano)
		thread.DeletedAt, thread.PurgeAt = past, past
		thread.Revision++
		return tx.SaveThread(thread)
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db}
	count, err := server.PurgeExpiredThreads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("purged %d threads", count)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.PermissionRequest("lifecycle-request"); err == nil {
			t.Fatal("permission request survived the purge")
		}
		if _, err := tx.ThreadAny(threadID); err == nil {
			t.Fatal("purged thread still exists")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRevokeGrantClosesPendingAndReportsActive proves revoking a conversation
// grant cancels the requests it still covered and reports remaining active runs
// rather than claiming a clean stop.
func TestRevokeGrantClosesPendingAndReportsActive(t *testing.T) {
	api, db, workspaceID, threadID, _ := permissionFixture(t)
	run := w.Run{ID: "active-run", ThreadID: threadID, WorkspaceID: workspaceID, Status: "running", CreatedAt: w.Now()}
	request := w.ToolPermissionRequest{
		ID: "covered-request", ThreadID: threadID, WorkspaceID: workspaceID, RunID: run.ID,
		StepID: "step", AttemptID: "attempt", MemberID: "member", ToolCallID: "call",
		Origin: w.OriginHost, Action: "write", ToolName: "write_file",
		NormalizedResourceScope: "/tmp/outside", ArgumentsDigest: "digest",
		PermissionEpoch: 1, Status: w.PermissionPending, Revision: 1, CreatedAt: w.Now(),
	}
	grant := w.ConversationGrant{ID: "active-grant", ThreadID: threadID, Action: "write", ToolMatcher: "write_file",
		NormalizedResourceScope: "/tmp/outside", PermissionEpoch: 1, SourceRequestID: request.ID, Status: "active", CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, "active-key", "active-hash"); err != nil {
			return err
		}
		if err := tx.SavePermissionRequest(request); err != nil {
			return err
		}
		return tx.SaveConversationGrant(grant)
	}); err != nil {
		t.Fatal(err)
	}
	var revoked w.ConversationGrant
	api.call("POST", "/api/v1/permission-grants/active-grant/revoke", map[string]any{}, nil, 200, &revoked)
	if revoked.Status != "revoked" || revoked.AffectedActiveRuns != 1 {
		t.Fatalf("revocation report = %+v", revoked)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		stored, err := tx.PermissionRequest("covered-request")
		if err != nil {
			return err
		}
		if stored.Status != w.PermissionCanceled {
			t.Fatalf("covered pending request survived revocation: %+v", stored)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
