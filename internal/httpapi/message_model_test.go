package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestThreadMessagesKeepAttemptModelAndBackfillLegacyRequestedModel(t *testing.T) {
	server, db, cookie, _ := pagingServer(t, workflow.DefaultLimits())
	ws := w.Workspace{ID: w.NewID(), Name: "models", Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "models", Revision: 1, UpdatedAt: w.Now()}
	member := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "研究成员", Handle: "researcher", Model: "historical-model", Revision: 1}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: thread.ID, Status: "succeeded", CreatedAt: w.Now(), Agents: []w.Agent{member}}
	if err := db.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		return tx.InsertRun(run, w.NewID(), "hash")
	}); err != nil {
		t.Fatal(err)
	}
	stepID := w.NewID()
	save := func(message w.Message) {
		message.ID, message.ThreadID, message.RunID, message.StepID = w.NewID(), thread.ID, run.ID, stepID
		message.Status, message.CreatedAt = "completed", w.Now()
		if err := db.Write(context.Background(), func(tx *store.Tx) error { return tx.SaveMessage(message) }); err != nil {
			t.Fatal(err)
		}
	}
	attemptID := w.NewID()
	save(w.Message{AttemptID: attemptID, AgentID: member.ID, AgentName: member.DisplayName, Role: "assistant", Model: "requested", ResolvedModel: "resolved", Content: "snapshot"})
	save(w.Message{AgentID: member.ID, AgentName: member.DisplayName, Role: "assistant", Content: "legacy-first-attempt"})
	save(w.Message{AgentID: member.ID, AgentName: member.DisplayName, Role: "assistant", Content: "legacy-retry"})
	save(w.Message{AgentID: member.ID, AgentName: member.DisplayName, Role: "user", Content: "user-question"})
	save(w.Message{AgentID: w.NewID(), AgentName: "unknown", Role: "assistant", Content: "unknown-member"})

	rw := authCall(server, "GET", "/api/v1/threads/"+thread.ID, nil, cookie, "")
	if rw.Code != 200 {
		t.Fatalf("thread = %d %s", rw.Code, rw.Body.String())
	}
	validateResponse(t, "/api/v1/threads/{id}", "get", 200, rw.Body.Bytes())
	var view ThreadView
	if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	byContent := map[string]w.Message{}
	for _, message := range view.Messages.Items {
		byContent[message.Content] = message
	}
	snapshot := byContent["snapshot"]
	if snapshot.AttemptID != attemptID || snapshot.Model != "requested" || snapshot.ResolvedModel != "resolved" {
		t.Fatalf("attempt model snapshot was not returned: %+v", snapshot)
	}
	for _, content := range []string{"legacy-first-attempt", "legacy-retry"} {
		legacy := byContent[content]
		if legacy.Model != "historical-model" || legacy.ResolvedModel != "" || legacy.AttemptID != "" {
			t.Fatalf("legacy %s guessed a model instead of the run's requested one: %+v", content, legacy)
		}
	}
	if question := byContent["user-question"]; question.Model != "" || question.ResolvedModel != "" {
		t.Fatalf("user message carries a model: %+v", question)
	}
	if unknown := byContent["unknown-member"]; unknown.Model != "" || unknown.ResolvedModel != "" {
		t.Fatalf("message outside the run roster was assigned a model: %+v", unknown)
	}
}
