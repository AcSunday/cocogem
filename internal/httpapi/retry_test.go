package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func seedRetrySource(t *testing.T, db *store.Store, status string) (w.Thread, w.Run) {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "retry", Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "retry", Revision: 1, UpdatedAt: w.Now()}
	agent := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "重试成员", Handle: "retry-member", Model: "test", Tools: []string{"read_file", "publish_artifact"}, Revision: 1}
	thread.AgentIDs, thread.CoordinatorID = []string{agent.ID}, agent.ID
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: status, CreatedAt: w.Now(), FinishedAt: w.Now(), Agents: []w.Agent{agent}, CoordinatorID: agent.ID, Input: w.RunInput{Message: "需要重试的任务", Workflow: "free", Targets: []string{agent.ID}, ResearchScope: "given_sources"}, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
	if status == "failed" {
		run.Error = "protocol failure"
	}
	steps, err := workflow.Plan(run.ID, run.Input, run.Agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(agent); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "retry-"+run.ID); err != nil {
			return err
		}
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return thread, run
}

func TestRetryEndpointQueuesLinkedRunAndIsIdempotent(t *testing.T) {
	server, db, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	server.engine.Model = testutil.Model{}
	server.engine.AllowedModels = []string{"test"}
	thread, source := seedRetrySource(t, db, "failed")
	response := apiCall(server, http.MethodPost, "/api/v1/runs/"+source.ID+"/retry", map[string]any{}, cookie, csrf, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}
	var retried w.Run
	if err := json.Unmarshal(response.Body.Bytes(), &retried); err != nil {
		t.Fatal(err)
	}
	if retried.ID == source.ID || retried.Status != "queued" || retried.ThreadID != thread.ID || retried.Input.RetryOfRunID != source.ID {
		t.Fatalf("retry response did not link the source: %+v", retried)
	}
	again := apiCall(server, http.MethodPost, "/api/v1/runs/"+source.ID+"/retry", map[string]any{}, cookie, csrf, nil)
	if again.Code != http.StatusAccepted {
		t.Fatalf("repeated retry status=%d body=%s", again.Code, again.Body.String())
	}
	var duplicate w.Run
	if err := json.Unmarshal(again.Body.Bytes(), &duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != retried.ID {
		t.Fatalf("repeated retry queued another run: %s != %s", duplicate.ID, retried.ID)
	}
	if err := db.Read(context.Background(), func(tx *store.Tx) error {
		active, err := tx.Active()
		if err != nil {
			return err
		}
		if len(active) != 1 || active[0].ID != retried.ID {
			t.Fatalf("active runs after repeated retry: %+v", active)
		}
		messages, err := tx.Messages(thread.ID)
		if err != nil {
			return err
		}
		if len(messages) != 0 {
			t.Fatalf("retry duplicated a user message: %+v", messages)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryEndpointRejectsSuccessfulRun(t *testing.T) {
	server, db, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	server.engine.Model = testutil.Model{}
	_, source := seedRetrySource(t, db, "succeeded")
	response := apiCall(server, http.MethodPost, "/api/v1/runs/"+source.ID+"/retry", map[string]any{}, cookie, csrf, nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("successful retry status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRetryEndpointAcceptsExactFailedStepAndRequiresStableKey(t *testing.T) {
	server, db, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	server.engine.Model = testutil.Model{}
	server.engine.AllowedModels = []string{"test"}
	_, source := seedRetrySource(t, db, "failed")
	var failed w.Step
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(source.ID)
		if err != nil {
			return err
		}
		failed = steps[0]
		failed.Status, failed.Error = "failed", "Gemini did not confirm success"
		return tx.SaveStep(failed)
	}); err != nil {
		t.Fatal(err)
	}
	input := RetryInput{StepID: failed.ID}
	path := "/api/v1/runs/" + source.ID + "/retry"
	if got := apiCall(server, http.MethodPost, path, input, cookie, csrf, nil); got.Code != 400 {
		t.Fatalf("missing key accepted: %d %s", got.Code, got.Body.String())
	}
	headers := map[string]string{"Idempotency-Key": "exact-member-retry-key-0001"}
	response := apiCall(server, http.MethodPost, path, input, cookie, csrf, headers)
	if response.Code != 202 {
		t.Fatalf("retry failed: %d %s", response.Code, response.Body.String())
	}
	validateResponse(t, "/api/v1/runs/{id}/retry", "post", 202, response.Body.Bytes())
	var retry w.Run
	if err := json.Unmarshal(response.Body.Bytes(), &retry); err != nil {
		t.Fatal(err)
	}
	if retry.RetryOfStepID != failed.ID {
		t.Fatalf("retry ignored step: %+v", retry)
	}
	if got := apiCall(server, http.MethodPost, path, input, cookie, csrf, headers); got.Code != 202 {
		t.Fatalf("retry replay failed: %s", got.Body.String())
	}
	if got := apiCall(server, http.MethodPost, path, RetryInput{StepID: w.NewID()}, cookie, csrf, headers); got.Code != 409 {
		t.Fatal("reused a retry key for another step")
	}
}
