package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type deliveryAPI struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	csrf   string
}

func newDeliveryAPI(t *testing.T, root string, db *store.Store, engine *workflow.Engine) *deliveryAPI {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	app, err := New(Config{Host: server.Listener.Addr().String(), AuthFile: filepath.Join(root, "auth.json"), WebDir: root, Models: []string{"default"}, Roots: []string{root}, ModelConfigured: true, Model: testutil.Model{}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = app.Handler()
	server.Start()
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	api := &deliveryAPI{t: t, server: server, client: client}
	var session SessionReply
	api.call("POST", "/auth/setup", PasswordInput{Password: "delivery-test-password"}, nil, 200, &session)
	api.csrf = session.CSRFToken
	return api
}

// call sends an authenticated request and returns the raw body. A "csrf"
// header overrides the session token so tests can prove the mutation gate.
func (a *deliveryAPI) call(method, path string, input any, headers map[string]string, status int, target any) []byte {
	a.t.Helper()
	body := []byte("{}")
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			a.t.Fatal(err)
		}
		body = encoded
	}
	req, err := http.NewRequestWithContext(a.t.Context(), method, a.server.URL+path, bytes.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if method != "GET" {
		token := a.csrf
		if value, ok := headers["csrf"]; ok {
			token = value
		}
		req.Header.Set("X-CSRF-Token", token)
	}
	for key, value := range headers {
		if key != "csrf" {
			req.Header.Set(key, value)
		}
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != status {
		a.t.Fatalf("%s %s: status=%d body=%s err=%v", method, path, resp.StatusCode, raw, err)
	}
	if target != nil && json.Unmarshal(raw, target) != nil {
		a.t.Fatalf("invalid API response: %s", raw)
	}
	return raw
}

func (a *deliveryAPI) unauthenticated(path string) int {
	a.t.Helper()
	client := &http.Client{}
	resp, err := client.Get(a.server.URL + path)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestQueuedSubmissionRecordsExplicitDelivery proves the acceptance path starts
// the delivery contract before any execution. Canceling a queued run must not
// turn it into legacy history that exposes a draft artifact.
func TestQueuedSubmissionRecordsExplicitDelivery(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The engine is deliberately not started: the run must stay queued so the
	// queued-cancellation path is exercised.
	engine := &workflow.Engine{Store: db, Model: testutil.Model{}, Web: testutil.Web{}}
	api := newDeliveryAPI(t, dir, db, engine)

	var ws w.Workspace
	api.call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "交付契约", Root: dir}, nil, 200, &ws)
	var members Page[w.Agent]
	api.call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	if len(members.Items) == 0 {
		t.Fatal("no workspace member")
	}
	var thread w.Thread
	api.call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "排队取消", AgentIDs: []string{members.Items[0].ID}, CoordinatorID: members.Items[0].ID, Workflow: "free"}, nil, 200, &thread)
	input := w.RunInput{Message: "排队后取消，草稿不得成为正式产物", Workflow: "free", Targets: []string{members.Items[0].ID}}
	var preview RoutingPreview
	api.call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
	input.ProposalID = preview.ProposalID
	submitted := api.call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "delivery-queued-cancel"}, 202, nil)
	validateResponse(t, "/api/v1/threads/{id}/runs", "post", 202, submitted)
	var accepted map[string]any
	if err := json.Unmarshal(submitted, &accepted); err != nil {
		t.Fatal(err)
	}
	runID, _ := accepted["id"].(string)
	delivery, ok := accepted["delivery"].(map[string]any)
	if runID == "" || !ok {
		t.Fatalf("accepted run lost its explicit delivery: %s", submitted)
	}
	for _, field := range []string{"message_ids", "artifact_refs"} {
		items, ok := delivery[field].([]any)
		if !ok || len(items) != 0 {
			t.Fatalf("delivery.%s must be an empty array, got %s", field, submitted)
		}
	}
	if got := api.unauthenticated("/api/v1/threads/" + thread.ID + "/artifacts"); got != 401 {
		t.Fatalf("artifacts leaked without a session: %d", got)
	}
	api.call("POST", "/api/v1/runs/"+runID+"/cancel", nil, map[string]string{"csrf": "not-the-session-token"}, 403, nil)
	canceled := api.call("POST", "/api/v1/runs/"+runID+"/cancel", nil, nil, 200, nil)
	validateResponse(t, "/api/v1/runs/{id}/cancel", "post", 200, canceled)
	var run w.Run
	if err := json.Unmarshal(canceled, &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "canceled" || run.Delivery == nil || len(run.Delivery.MessageIDs) != 0 || len(run.Delivery.ArtifactRefs) != 0 {
		t.Fatalf("queued cancellation = %+v", run)
	}
	var stepID string
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil && len(steps) > 0 {
			stepID = steps[0].ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draft := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: thread.ID, RunID: run.ID, StepID: stepID, Type: "note", Title: "草稿", Body: "canceled draft body", Feedback: "unreviewed"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveArtifact(draft) }); err != nil {
		t.Fatal(err)
	}
	listed := api.call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, nil)
	validateResponse(t, "/api/v1/threads/{id}/artifacts", "get", 200, listed)
	var artifacts Page[w.Artifact]
	if err := json.Unmarshal(listed, &artifacts); err != nil {
		t.Fatal(err)
	}
	if len(artifacts.Items) != 0 {
		t.Fatalf("a canceled new run exposed its draft: %+v", artifacts.Items)
	}
	exact := api.call("GET", "/api/v1/artifacts/"+draft.ID+"/versions/1", nil, nil, 200, nil)
	if !bytes.Contains(exact, []byte(draft.Body)) {
		t.Fatalf("the draft is no longer traceable by exact version: %s", exact)
	}
}

// TestThreadArtifactsFollowRunDelivery covers the thread artifact consumer:
// only a succeeded run's exact delivered versions are formal, while legacy
// terminal runs recorded no delivery and keep their existing artifacts.
func TestThreadArtifactsFollowRunDelivery(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	api := newDeliveryAPI(t, t.TempDir(), db, nil)

	ws := w.Workspace{ID: w.NewID(), Name: "交付过滤", Revision: 1, CreatedAt: w.Now()}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "交付过滤", Revision: 1, UpdatedAt: w.Now()}
	finalMessage := w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: "run-succeeded", Role: "assistant", Content: "最终交付", Status: "completed", CreatedAt: w.Now()}
	intermediateMessage := w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: "run-succeeded", Role: "assistant", Content: "中间结果", Status: "completed", CreatedAt: w.Now()}
	run := func(id, status string, delivery *w.Delivery) w.Run {
		return w.Run{ID: id, WorkspaceID: ws.ID, ThreadID: thread.ID, Status: status, CreatedAt: w.Now(), Input: w.RunInput{Message: "问题"}, Delivery: delivery}
	}
	runs := []w.Run{
		run("run-running", "running", &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}),
		run("run-failed", "failed", &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}),
		run("run-canceled", "canceled", &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}),
		run("run-succeeded", "succeeded", &w.Delivery{MessageIDs: []string{finalMessage.ID}, ArtifactRefs: []w.ArtifactRef{{ID: "artifact-final", Version: 2}}}),
		run("run-legacy", "succeeded", nil),
		run("run-legacy-failed", "failed", nil),
		run("run-legacy-canceled", "canceled", nil),
	}
	artifact := func(id, runID string, version int, body string) w.Artifact {
		return w.Artifact{ID: id, Version: version, Revision: 1, ThreadID: thread.ID, RunID: runID, StepID: "step-" + runID, Type: "note", Title: id, Body: body, Feedback: "unreviewed", CreatedAt: w.Now()}
	}
	artifacts := []w.Artifact{
		artifact("artifact-running", "run-running", 1, "running draft"),
		artifact("artifact-failed", "run-failed", 1, "failed draft"),
		artifact("artifact-canceled", "run-canceled", 1, "canceled draft"),
		artifact("artifact-final", "run-succeeded", 1, "superseded version"),
		artifact("artifact-final", "run-succeeded", 2, "delivered version"),
		artifact("artifact-legacy", "run-legacy", 1, "legacy succeeded artifact"),
		artifact("artifact-legacy-failed", "run-legacy-failed", 1, "legacy failed artifact"),
		artifact("artifact-legacy-canceled", "run-legacy-canceled", 1, "legacy canceled artifact"),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		for _, item := range runs {
			if err := tx.InsertRun(item, item.ID, "hash-"+item.ID); err != nil {
				return err
			}
		}
		for _, message := range []w.Message{intermediateMessage, finalMessage} {
			if err := tx.SaveMessage(message); err != nil {
				return err
			}
		}
		for _, item := range artifacts {
			if err := tx.SaveArtifact(item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	listed := api.call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, nil)
	validateResponse(t, "/api/v1/threads/{id}/artifacts", "get", 200, listed)
	var page Page[w.Artifact]
	if err := json.Unmarshal(listed, &page); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, item := range page.Items {
		got[fmt.Sprintf("%s@%d", item.ID, item.Version)] = true
	}
	want := map[string]bool{"artifact-final@2": true, "artifact-legacy@1": true, "artifact-legacy-failed@1": true, "artifact-legacy-canceled@1": true}
	if len(page.Items) != len(want) {
		t.Fatalf("thread artifacts = %+v", page.Items)
	}
	for key := range want {
		if !got[key] {
			t.Fatalf("missing delivered artifact %s in %+v", key, page.Items)
		}
	}
	for _, key := range []string{"artifact-running@1", "artifact-failed@1", "artifact-canceled@1", "artifact-final@1"} {
		if got[key] {
			t.Fatalf("undelivered artifact %s was listed as formal", key)
		}
	}
	// Drafts and superseded versions remain reachable by exact id/version.
	var first w.Artifact
	api.call("GET", "/api/v1/artifacts/artifact-final/versions/1", nil, nil, 200, &first)
	if first.Body != "superseded version" {
		t.Fatalf("exact version endpoint = %+v", first)
	}
	api.call("GET", "/api/v1/artifacts/artifact-running/versions/1", nil, nil, 200, &first)
	if first.Body != "running draft" {
		t.Fatalf("running draft exact version = %+v", first)
	}

	runBody := api.call("GET", "/api/v1/runs/run-succeeded", nil, nil, 200, nil)
	validateResponse(t, "/api/v1/runs/{id}", "get", 200, runBody)
	var view RunView
	if err := json.Unmarshal(runBody, &view); err != nil {
		t.Fatal(err)
	}
	if view.Run.Delivery == nil || len(view.Run.Delivery.MessageIDs) != 1 || view.Run.Delivery.MessageIDs[0] != finalMessage.ID {
		t.Fatalf("delivery message IDs are ambiguous: %+v", view.Run.Delivery)
	}
	if len(view.Run.Delivery.ArtifactRefs) != 1 || view.Run.Delivery.ArtifactRefs[0] != (w.ArtifactRef{ID: "artifact-final", Version: 2}) {
		t.Fatalf("delivery artifact refs = %+v", view.Run.Delivery.ArtifactRefs)
	}

	// The thread snapshot carries the same authoritative delivery, and the
	// referenced message resolves to exactly the completed final message.
	threadBody := api.call("GET", "/api/v1/threads/"+thread.ID, nil, nil, 200, nil)
	validateResponse(t, "/api/v1/threads/{id}", "get", 200, threadBody)
	var snapshot ThreadView
	if err := json.Unmarshal(threadBody, &snapshot); err != nil {
		t.Fatal(err)
	}
	deliveryMatches := 0
	for _, item := range snapshot.Runs {
		if item.ID != "run-succeeded" {
			continue
		}
		deliveryMatches++
		if item.Delivery == nil || len(item.Delivery.MessageIDs) != 1 || item.Delivery.MessageIDs[0] != finalMessage.ID {
			t.Fatalf("thread view delivery = %+v", item.Delivery)
		}
	}
	if deliveryMatches != 1 {
		t.Fatalf("thread view lost the succeeded run: %+v", snapshot.Runs)
	}
	resolved := map[string]int{}
	for _, message := range snapshot.Messages.Items {
		resolved[message.ID]++
		if message.ID == finalMessage.ID && message.Content != "最终交付" {
			t.Fatalf("delivered message = %+v", message)
		}
	}
	if resolved[finalMessage.ID] != 1 || resolved[intermediateMessage.ID] != 1 {
		t.Fatalf("thread messages = %+v", snapshot.Messages.Items)
	}
	// Every delivered message ID resolves to exactly one completed message;
	// the intermediate result is not silently promoted.
	if slices.Contains(view.Run.Delivery.MessageIDs, intermediateMessage.ID) {
		t.Fatal("an intermediate message was treated as the delivery")
	}

	if got := api.unauthenticated("/api/v1/threads/" + thread.ID + "/artifacts"); got != 401 {
		t.Fatalf("artifacts leaked without a session: %d", got)
	}
	api.call("PATCH", "/api/v1/artifacts/artifact-final/versions/2", FeedbackInput{Feedback: "accepted"}, map[string]string{"If-Match": "1", "csrf": "not-the-session-token"}, 403, nil)
}
