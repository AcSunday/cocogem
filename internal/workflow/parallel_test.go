package workflow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

type gatedModel struct {
	inner   testutil.Model
	started chan struct{}
	release chan struct{}
}

func (m *gatedModel) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
	return m.inner.Complete(ctx, request, emit)
}

func seedParallelFreeRun(t *testing.T, s *store.Store) w.Run {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "parallel", Revision: 1}
	agent := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "并行成员", Handle: "parallel-cat", Model: "test", Tools: tools.Names, Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "parallel", AgentIDs: []string{agent.ID}, CoordinatorID: agent.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: thread.ID, Status: "queued", CreatedAt: w.Now(), Agents: []w.Agent{agent}, CoordinatorID: agent.ID, Input: w.RunInput{Message: "独立会话并行", Workflow: "free", Targets: []string{agent.ID}, ResearchScope: "given_sources"}, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
	steps, err := Plan(run.ID, run.Input, run.Agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(agent); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "parallel-"+run.ID); err != nil {
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
	return run
}

func TestIndependentConversationsRunConcurrently(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := seedParallelFreeRun(t, s)
	second := seedParallelFreeRun(t, s)
	model := &gatedModel{started: make(chan struct{}, 8), release: make(chan struct{})}
	e := Engine{Store: s, Model: model, AllowedModels: []string{"test"}, MaxParallelRuns: 2}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	for i := 0; i < 2; i++ {
		select {
		case <-model.started:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of 2 conversations started; a single worker serialized them", i)
		}
	}
	close(model.release)
	for _, run := range []w.Run{first, second} {
		if got := awaitRun(t, s, run.ID); got.Status != "succeeded" {
			t.Fatalf("parallel run %s: %s", got.Status, got.Error)
		}
	}
}

func TestParallelismFallsBackToDefault(t *testing.T) {
	e := Engine{}
	if got := e.maxParallelRuns(); got != DefaultMaxParallelRuns {
		t.Fatalf("parallelism=%d, want %d", got, DefaultMaxParallelRuns)
	}
	e.MaxParallelRuns = 1
	if got := e.maxParallelRuns(); got != 1 {
		t.Fatalf("parallelism=%d, want 1", got)
	}
}
