package workflow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestBudgetCapacityQueuesAndAutomaticallyBackfills(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workspace := w.Workspace{ID: "capacity-workspace", Name: "capacity", Revision: 1}
	member := w.Agent{ID: "capacity-member", WorkspaceID: workspace.ID, DisplayName: "member", Handle: "member", Model: "test", Tools: tools.Names, Revision: 1}
	rootThread := w.Thread{ID: "capacity-root-thread", WorkspaceID: workspace.ID, AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Revision: 1, UpdatedAt: w.Now()}
	childThread := w.Thread{ID: "capacity-child-thread", WorkspaceID: workspace.ID, AgentIDs: []string{member.ID}, CoordinatorID: member.ID, Internal: true, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(workspace); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveThread(rootThread); err != nil {
			return err
		}
		return tx.SaveThread(childThread)
	}); err != nil {
		t.Fatal(err)
	}
	created, err := project.New(db).CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: workspace.ID, ThreadID: rootThread.ID, Goal: "share one execution slot",
		SourceMessageID: "capacity-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		account.Policy.ParallelBranches = 1
		account.Policy.WriterBranches = 1
		account.Revision++
		if err := tx.SaveBudgetAccount(account, account.Revision-1); err != nil {
			return err
		}
		child := w.Task{
			ID: "capacity-child-task", WorkspaceID: workspace.ID, ThreadID: childThread.ID,
			RootTaskID: created.Task.ID, ParentID: created.Task.ID, BudgetAccountID: account.ID,
			CollaborationScopeID: created.Task.CollaborationScopeID, CurrentRevision: 1,
			Status: "ready", CreatedAt: w.Now(), UpdatedAt: w.Now(),
		}
		if err := tx.InsertTask(child); err != nil {
			return err
		}
		return tx.InsertTaskRevision(w.TaskRevision{TaskID: child.ID, Revision: 1, Goal: "queued branch", Digest: "child-digest", CreatedAt: w.Now()})
	}); err != nil {
		t.Fatal(err)
	}

	runs := []w.Run{
		{ID: "capacity-first", WorkspaceID: workspace.ID, Workspace: workspace, ThreadID: rootThread.ID, TaskID: created.Task.ID, TaskRevision: 1, BudgetAccountID: created.Budget.ID, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{member}, CoordinatorID: member.ID, Input: w.RunInput{Message: "first", Workflow: "free", Targets: []string{member.ID}, ResearchScope: "given_sources"}, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}},
		{ID: "capacity-second", WorkspaceID: workspace.ID, Workspace: workspace, ThreadID: childThread.ID, TaskID: "capacity-child-task", TaskRevision: 1, BudgetAccountID: created.Budget.ID, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{member}, CoordinatorID: member.ID, Input: w.RunInput{Message: "second", Workflow: "free", Targets: []string{member.ID}, ResearchScope: "given_sources"}, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}},
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		for _, run := range runs {
			steps, err := Plan(run.ID, run.Input, run.Agents)
			if err != nil {
				return err
			}
			if err := tx.InsertRun(run, "key-"+run.ID, "hash-"+run.ID); err != nil {
				return err
			}
			for _, step := range steps {
				if err := tx.SaveStep(step); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	model := &gatedModel{started: make(chan struct{}, 4), release: make(chan struct{})}
	engine := Engine{Store: db, Model: model, AllowedModels: []string{"test"}, MaxParallelRuns: 2}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	select {
	case <-model.started:
	case <-time.After(10 * time.Second):
		t.Fatal("first run did not start")
	}
	select {
	case <-model.started:
		t.Fatal("second run exceeded the shared execution slot")
	case <-time.After(150 * time.Millisecond):
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		first, err := tx.Run(runs[0].ID)
		if err != nil {
			return err
		}
		second, err := tx.Run(runs[1].ID)
		if err != nil {
			return err
		}
		if !((first.Status == "running" && second.Status == "queued") || (first.Status == "queued" && second.Status == "running")) {
			t.Fatalf("shared slot admitted zero or two branches: first=%s second=%s", first.Status, second.Status)
		}
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil || account.ActiveBranches != 1 {
			t.Fatalf("shared slot ledger = %+v, %v", account, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(model.release)
	for _, run := range runs {
		if result := awaitRun(t, db, run.ID); result.Status != "succeeded" {
			t.Fatalf("run %s did not backfill: %s %s", run.ID, result.Status, result.Error)
		}
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		account, err := tx.BudgetAccount(created.Budget.ID)
		if err != nil {
			return err
		}
		if account.ActiveBranches != 0 || account.ActiveWriters != 0 {
			t.Fatalf("execution slots were not released: %+v", account)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
