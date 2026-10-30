package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Every accepted built-in plan must publish exactly one formal deliverable from
// its designated final role and deliver only that version.
func TestBuiltInPlansDeliverOnlyTheirFinalArtifact(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    func(agents []w.Agent) w.RunInput
		expected string
		final    int
	}{
		{
			name: "research",
			input: func(agents []w.Agent) w.RunInput {
				return w.RunInput{Message: "Compare the synthetic sources", Workflow: "research", ResearchScope: "given_sources", SourceURLs: testutil.Sources, Assignments: map[string]string{"researcher": agents[0].ID, "critic": agents[1].ID, "coordinator": agents[2].ID}}
			},
			expected: "research_report",
			final:    4,
		},
		{
			name: "read-only engineering analysis",
			input: func(agents []w.Agent) w.RunInput {
				return w.RunInput{Message: "Analyze the synthetic file", Workflow: "engineering", EngineeringAction: w.EngineeringAnalyze, ResearchScope: "given_sources", Assignments: map[string]string{"analyst": agents[0].ID, "reviewer": agents[1].ID}}
			},
			expected: "code_analysis",
			final:    3,
		},
		{
			name: "legacy code analysis",
			input: func(agents []w.Agent) w.RunInput {
				return w.RunInput{Message: "Analyze the synthetic file", Workflow: "code_analysis", ResearchScope: "given_sources", Assignments: map[string]string{"analyst": agents[0].ID, "reviewer": agents[1].ID}}
			},
			expected: "code_analysis",
			final:    3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("line one\nline two\nidentity and duty\nroles stay separate\nline five\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ws := w.Workspace{ID: w.NewID(), Name: "delivery", Root: root, Revision: 1}
			agents := []w.Agent{}
			for i, name := range []string{"甲", "乙", "丙"} {
				agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: "member-" + string(rune('a'+i)), Model: "test", Tools: slices.Clone(tools.Names), Revision: 1})
			}
			thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: []string{agents[0].ID, agents[1].ID, agents[2].ID}, CoordinatorID: agents[2].ID, Revision: 1, UpdatedAt: w.Now()}
			input := tc.input(agents)
			if input.Workflow == "research" {
				ws.Root = ""
			}
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
			var artifacts []w.Artifact
			if err := s.Read(ctx, func(tx *store.Tx) error {
				var readErr error
				if storedSteps, readErr = tx.Steps(run.ID); readErr != nil {
					return readErr
				}
				artifacts, readErr = tx.Artifacts(thread.ID)
				return readErr
			}); err != nil {
				t.Fatal(err)
			}
			if len(storedSteps) != len(steps) || storedSteps[tc.final].Role != "final_reviewer" && storedSteps[tc.final].Role != "coordinator" {
				t.Fatalf("unexpected plan: %+v", storedSteps)
			}
			if len(artifacts) != 1 || artifacts[0].Type != tc.expected || artifacts[0].StepID != storedSteps[tc.final].ID {
				encoded, _ := json.Marshal(artifacts)
				t.Fatalf("formal artifacts = %s", encoded)
			}
			if got.Delivery == nil || len(got.Delivery.ArtifactRefs) != 1 || got.Delivery.ArtifactRefs[0].ID != artifacts[0].ID || got.Delivery.ArtifactRefs[0].Version != artifacts[0].Version {
				t.Fatalf("delivery refs = %+v, artifact %s@%d", got.Delivery, artifacts[0].ID, artifacts[0].Version)
			}
		})
	}
}
