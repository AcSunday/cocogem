package httpapi

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// permissionFixture seeds one workspace, thread, member, task and run so the
// permission endpoints can be exercised over real HTTP.
func permissionFixture(t *testing.T) (api *deliveryAPI, db *store.Store, workspaceID, threadID, memberID string) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	api = newDeliveryAPI(t, root, db, nil)
	var workspace w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "权限"}, nil, 200, &workspace)
	var members struct{ Items []w.Agent }
	api.call("GET", "/api/v1/workspaces/"+workspace.ID+"/agents", nil, nil, 200, &members)
	memberID = members.Items[0].ID
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+workspace.ID+"/threads", w.Thread{Title: "权限会话", AgentIDs: []string{memberID}, CoordinatorID: memberID}, nil, 200, &thread)
	return api, db, workspace.ID, thread.ID, memberID
}

// TestPermissionEndpointsThreeStateAndIdempotency exercises the frozen HTTP
// contract: pending listing, the three decisions, idempotent replay, conflicting
// replay, stale CAS and revocation.
func TestPermissionEndpointsThreeStateAndIdempotency(t *testing.T) {
	api, db, workspaceID, threadID, _ := permissionFixture(t)
	request := w.ToolPermissionRequest{
		ID: "req-1", ThreadID: threadID, WorkspaceID: workspaceID, RunID: "run",
		StepID: "step", AttemptID: "attempt", MemberID: "member", MemberName: "member",
		ToolCallID: "call-1", Origin: w.OriginHost, Action: "write", ToolName: "write_file",
		NormalizedResourceScope: "/tmp/outside", ArgumentsDigest: "digest",
		Reason: "write outside", PermissionEpoch: 1, Status: w.PermissionPending, Revision: 1, CreatedAt: w.Now(),
	}
	// A request needs a real run row for its foreign key; the delivery API helper
	// seeds the thread only, so persist directly with a matching run.
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveAgent(w.Agent{ID: "member", DisplayName: "member", Handle: "member", Model: "default", Tools: []string{"write_file"}, Revision: 1}); err != nil {
			return err
		}
		if err := tx.SaveMember(w.WorkspaceMember{WorkspaceID: workspaceID, AgentID: "member", Enabled: true, Tools: []string{"write_file"}, Revision: 1}); err != nil {
			return err
		}
		if err := tx.InsertRun(w.Run{ID: "run", ThreadID: threadID, WorkspaceID: workspaceID, Status: "running", CreatedAt: w.Now()}, "key", "hash"); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: "step", RunID: "run", AgentID: "member", Role: "responder", Status: "running"}); err != nil {
			return err
		}
		if err := tx.SaveAttempt(w.Attempt{ID: "attempt", RunID: "run", StepID: "step", Number: 1, Status: "running", StartedAt: w.Now()}); err != nil {
			return err
		}
		return tx.SavePermissionRequest(request)
	}); err != nil {
		t.Fatal(err)
	}
	var list PermissionRequestList
	api.call("GET", "/api/v1/threads/"+threadID+"/permission-requests", nil, nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].Status != w.PermissionPending {
		t.Fatalf("pending listing = %+v", list.Items)
	}
	var decided w.ToolPermissionRequest
	api.call("POST", "/api/v1/permission-requests/req-1/decisions",
		PermissionDecisionInput{Decision: w.DecisionAllowOnce, RequestID: "decision-1"},
		map[string]string{"If-Match": "1"}, 200, &decided)
	if decided.Status != w.PermissionAllowedOnce {
		t.Fatalf("decision = %+v", decided)
	}
	// Identical replay is idempotent.
	var replay w.ToolPermissionRequest
	api.call("POST", "/api/v1/permission-requests/req-1/decisions",
		PermissionDecisionInput{Decision: w.DecisionAllowOnce, RequestID: "decision-1"},
		map[string]string{"If-Match": fmt.Sprint(decided.Revision)}, 200, &replay)
	if replay.Status != w.PermissionAllowedOnce || replay.Revision != decided.Revision {
		t.Fatalf("replay = %+v", replay)
	}
	// A different decision conflicts.
	api.call("POST", "/api/v1/permission-requests/req-1/decisions",
		PermissionDecisionInput{Decision: w.DecisionDeny, RequestID: "decision-2"},
		map[string]string{"If-Match": fmt.Sprint(decided.Revision)}, 409, nil)
	// A stale revision on a fresh pending request is rejected.
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		second := request
		second.ID, second.ToolCallID, second.Revision = "req-2", "call-2", 3
		return tx.SavePermissionRequest(second)
	}); err != nil {
		t.Fatal(err)
	}
	api.call("POST", "/api/v1/permission-requests/req-2/decisions",
		PermissionDecisionInput{Decision: w.DecisionDeny, RequestID: "decision-3"},
		map[string]string{"If-Match": "1"}, 409, nil)
	var denied w.ToolPermissionRequest
	api.call("POST", "/api/v1/permission-requests/req-2/decisions",
		PermissionDecisionInput{Decision: w.DecisionDeny, RequestID: "decision-3"},
		map[string]string{"If-Match": "3"}, 200, &denied)
	if denied.Status != w.PermissionDenied {
		t.Fatalf("deny = %+v", denied)
	}
}

// TestPermissionGrantsAreListedAndRevocable proves the conversation grant view and
// its idempotent revocation over HTTP.
func TestPermissionGrantsAreListedAndRevocable(t *testing.T) {
	api, db, _, threadID, _ := permissionFixture(t)
	grant := w.ConversationGrant{
		ID: "grant-1", ThreadID: threadID, Action: "write", ToolMatcher: "write_file",
		NormalizedResourceScope: "/tmp/outside", PermissionEpoch: 1, SourceRequestID: "req",
		Status: "active", CreatedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveConversationGrant(grant) }); err != nil {
		t.Fatal(err)
	}
	var list ConversationGrantList
	api.call("GET", "/api/v1/threads/"+threadID+"/permission-grants", nil, nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].ToolMatcher != "write_file" {
		t.Fatalf("grant listing = %+v", list.Items)
	}
	var revoked w.ConversationGrant
	api.call("POST", "/api/v1/permission-grants/grant-1/revoke", map[string]any{}, nil, 200, &revoked)
	if revoked.Status != "revoked" || revoked.RevokedAt == "" {
		t.Fatalf("revocation = %+v", revoked)
	}
	var again w.ConversationGrant
	api.call("POST", "/api/v1/permission-grants/grant-1/revoke", map[string]any{}, nil, 200, &again)
	if again.Status != revoked.Status || again.RevokedAt != revoked.RevokedAt {
		t.Fatalf("repeated revocation changed the record: %+v / %+v", again, revoked)
	}
}

// TestTaskDeliveriesEndpointIsNarrowSourceFree proves the TaskDetail projection is
// served by the API and contains only the delivered versions.
func TestTaskDeliveriesEndpointIsNarrowSourceFree(t *testing.T) {
	api, db, workspaceID, threadID, memberID := permissionFixture(t)
	// Create the task through the thread-bound route so the foreign keys exist.
	var view struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	api.call("POST", "/api/v1/workspaces/"+workspaceID+"/tasks", map[string]any{"thread_id": threadID, "owner_member_id": memberID, "goal": "验收", "source_message_id": "m", "accepted_by": "user"}, nil, 200, &view)
	if view.Task.ID == "" {
		t.Fatal("task was not created")
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(w.Run{
			ID: "delivery-run", ThreadID: threadID, WorkspaceID: workspaceID, TaskID: view.Task.ID,
			Status: "succeeded", CreatedAt: w.Now(),
			Delivery: &w.Delivery{MessageIDs: []string{"delivered"}, ArtifactRefs: []w.ArtifactRef{{ID: "artifact", Version: 2}}},
		}, "key", "hash"); err != nil {
			return err
		}
		for _, artifact := range []w.Artifact{
			{ID: "artifact", Version: 1, Revision: 1, ThreadID: threadID, RunID: "delivery-run", Title: "旧版"},
			{ID: "artifact", Version: 2, Revision: 1, ThreadID: threadID, RunID: "delivery-run", Title: "最终"},
		} {
			if err := tx.SaveArtifact(artifact); err != nil {
				return err
			}
		}
		return tx.SaveMessage(w.Message{ID: "delivered", RunID: "delivery-run", ThreadID: threadID, Role: "assistant", Status: "completed", Content: "交付正文"})
	}); err != nil {
		t.Fatal(err)
	}
	var deliveries struct {
		TaskID    string       `json:"task_id"`
		Artifacts []w.Artifact `json:"artifacts"`
		Messages  []w.Message  `json:"messages"`
	}
	api.call("GET", "/api/v1/tasks/"+view.Task.ID+"/deliveries", nil, nil, 200, &deliveries)
	if deliveries.TaskID != view.Task.ID {
		t.Fatalf("task_id = %q", deliveries.TaskID)
	}
	if len(deliveries.Artifacts) != 1 || deliveries.Artifacts[0].Version != 2 {
		t.Fatalf("artifacts = %+v", deliveries.Artifacts)
	}
	if len(deliveries.Messages) != 1 || deliveries.Messages[0].ID != "delivered" {
		t.Fatalf("messages = %+v", deliveries.Messages)
	}
	body, _ := json.Marshal(deliveries)
	if containsKey(body, "sources") {
		t.Fatalf("delivery projection leaked sources: %s", body)
	}
}

func containsKey(body []byte, key string) bool {
	return json.Valid(body) && jsonContains(body, key)
}

func jsonContains(body []byte, key string) bool {
	return len(body) > 0 && string(body) != "" && fmt.Sprintf("%q", key) != "" && indexOf(body, []byte(fmt.Sprintf("%q", key))) >= 0
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}
