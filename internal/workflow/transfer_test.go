package workflow

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type transferBlockingRunner struct {
	once    sync.Once
	started chan struct{}
}

func (r *transferBlockingRunner) Run(ctx context.Context, _ model.Request, _ agent.Execute, _ agent.Observe) error {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return ctx.Err()
}

func TestEngineTransferStopsRunningInvocationBeforeOwnerCAS(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := w.Agent{ID: "owner", WorkspaceID: "transfer-ws", DisplayName: "Owner", Handle: "owner", Executor: "claude", Model: "default", Revision: 1}
	recipient := w.Agent{ID: "recipient", WorkspaceID: owner.WorkspaceID, DisplayName: "Recipient", Handle: "recipient", Executor: "claude", Model: "default", Revision: 1}
	ws := w.Workspace{ID: owner.WorkspaceID, Name: "transfer", Revision: 1, ExecutionMode: w.ExecutionControlled}
	th := w.Thread{ID: "transfer-thread", WorkspaceID: ws.ID, AgentIDs: []string{owner.ID, recipient.ID}, CoordinatorID: owner.ID, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, member := range []w.Agent{owner, recipient} {
			if err := tx.SaveAgent(member); err != nil {
				return err
			}
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: ws.ID, ThreadID: th.ID, OwnerMemberID: owner.ID, Goal: "stop before ownership transfer",
		AllowedActions: []string{"read"}, SourceMessageID: "transfer-create", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: "transfer-running-run", WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, TaskID: created.Task.ID, TaskRevision: 1, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{owner}, CoordinatorID: owner.ID, Input: w.RunInput{Message: "wait", Workflow: "free"}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &run, "transfer-create"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "transfer-running", "transfer-running-digest"); err != nil {
			return err
		}
		return tx.SaveStep(w.Step{ID: "transfer-running-step", RunID: run.ID, AgentID: owner.ID, AgentName: owner.DisplayName, Role: "responder", Instruction: "wait", Status: "pending"})
	}); err != nil {
		t.Fatal(err)
	}
	runner := &transferBlockingRunner{started: make(chan struct{})}
	engine := Engine{Store: db, Runners: map[string]agent.Runner{"claude": runner}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	<-runner.started
	transferred, err := engine.TransferTask(t.Context(), created.Task.ID, recipient.ID, "engine-transfer-1", 1)
	if err != nil || transferred.OwnerMemberID != recipient.ID || transferred.TransferRequestID != "" {
		t.Fatalf("engine transfer = %+v, %v", transferred, err)
	}
	settled := awaitRun(t, db, run.ID)
	if settled.Status != "canceled" {
		t.Fatalf("old invocation was not stopped: %+v", settled)
	}
}
