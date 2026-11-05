package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type yieldingModel struct{ calls int }

func (m *yieldingModel) Complete(_ context.Context, r model.Request, emit func(string) error) (model.Response, error) {
	m.calls++
	if m.calls == 1 {
		found := false
		for _, tool := range r.Tools {
			found = found || tool.Name == "yield_run"
		}
		if !found {
			return model.Response{}, &missingYieldToolError{}
		}
		input, _ := json.Marshal(map[string]any{
			"predicate": "event", "observation_cursor": "cursor-7", "wake_source": "host", "event_id": "tool-event-7",
		})
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: "yield-call", Name: "yield_run", Input: input}}}, nil
	}
	if !strings.Contains(r.Messages[len(r.Messages)-1].Content[0].Content, "after_runner_return") {
		return model.Response{}, &missingYieldToolError{}
	}
	if err := emit("registered wait"); err != nil {
		return model.Response{}, err
	}
	return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: "registered wait"}}}, nil
}

type missingYieldToolError struct{}

func (*missingYieldToolError) Error() string { return "yield tool or settlement response missing" }

func TestYieldToolSettlesOnlyAfterModelInvocationReturns(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	member := w.Agent{ID: "owner", WorkspaceID: "yield-ws", DisplayName: "Owner", Handle: "owner", Model: "test", Tools: tools.Names, Revision: 1}
	ws := w.Workspace{ID: member.WorkspaceID, Name: "yield", Revision: 1, ExecutionMode: w.ExecutionControlled}
	th := w.Thread{ID: "yield-thread", WorkspaceID: ws.ID, AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: ws.ID, ThreadID: th.ID, OwnerMemberID: member.ID, Goal: "wait for host event",
		AllowedActions: []string{"read", "collaborate"}, SourceMessageID: "yield-create", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "tool-yield-run", WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: 1, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{member}, CoordinatorID: member.ID,
		Input: w.RunInput{Message: "wait for host event", Workflow: "free", ResearchScope: "given_sources"},
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &run, "yield-create"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "tool-yield", "tool-yield-digest"); err != nil {
			return err
		}
		return tx.SaveStep(w.Step{ID: "tool-yield-step", RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: "responder", Instruction: "wait", Status: "pending"})
	}); err != nil {
		t.Fatal(err)
	}
	fixture := &yieldingModel{}
	engine := Engine{Store: db, Model: fixture, AllowedModels: []string{"test"}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	settled := awaitRun(t, db, run.ID)
	if settled.Status != "yielded" || fixture.calls != 2 {
		t.Fatalf("tool yield run = %+v, model calls=%d", settled, fixture.calls)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil || len(steps) != 1 || steps[0].Status != "yielded" {
			t.Fatalf("tool yield steps = %+v, %v", steps, err)
		}
		attempts, err := tx.Attempts(run.ID)
		if err != nil || len(attempts) != 1 || attempts[0].Status != "yielded" {
			t.Fatalf("tool yield attempts = %+v, %v", attempts, err)
		}
		messages, err := tx.Messages(th.ID)
		if err != nil || len(messages) != 1 || messages[0].Status != "yielded" || messages[0].Content != "registered wait" {
			t.Fatalf("runner output was not closed before yield: %+v, %v", messages, err)
		}
		wakeup, err := tx.WakeupBySourceEvent("host", "tool-event-7")
		if err != nil || wakeup.YieldedRunID != run.ID || wakeup.ObservationCursor != "cursor-7" || wakeup.Status != "pending" {
			t.Fatalf("tool yield wakeup = %+v, %v", wakeup, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
