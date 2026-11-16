package workflow

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestDeliveredChainPublishesOnlyTheTerminalArtifact is the delivery-contract
// regression for the dynamic kernel: an addressed member's reply is delivered,
// and no report template is required for success.
func TestDeliveredChainPublishesOnlyTheTerminalArtifact(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := w.Workspace{ID: w.NewID(), Name: "delivery", Root: root, Revision: 1}
	agents := []w.Agent{}
	for i, name := range []string{"甲", "乙", "丙"} {
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: "member-" + string(rune('a'+i)), Model: "test", Tools: slices.Clone(tools.Names), Revision: 1})
	}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: []string{agents[0].ID, agents[1].ID, agents[2].ID}, CoordinatorID: agents[2].ID, Revision: 1, UpdatedAt: w.Now()}
	input := w.RunInput{Message: "Review the synthetic file", Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: thread.ID, Workspace: ws, Agents: agents, CoordinatorID: agents[2].ID, Input: input, Status: "queued", CreatedAt: w.Now(), Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
	steps, err := Plan(run.ID, input, agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, a := range agents {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), run.ID); err != nil {
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
	e := Engine{Store: s, Model: testutil.Model{}, Web: testutil.Web{}, AllowedModels: []string{"test"}, AllowedRoots: []string{root}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("run failed: %s", got.Error)
	}
	var storedSteps []w.Step
	if err := s.Read(ctx, func(tx *store.Tx) error {
		var readErr error
		storedSteps, readErr = tx.Steps(run.ID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(storedSteps) != len(steps) || storedSteps[0].Role != "responder" {
		t.Fatalf("unexpected plan: %+v", storedSteps)
	}
	if got.Delivery == nil || len(got.Delivery.MessageIDs) != 1 {
		t.Fatalf("delivery must be the addressed member's reply: %+v", got.Delivery)
	}
}
