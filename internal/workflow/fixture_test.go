package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// seedMethodFixture builds one running coordinator root step over a three-member
// roster. It is the shared fixture for the dynamic-kernel behaviours that used to
// be exercised through the retired preset method selection.
func seedMethodFixture(t *testing.T, toolsFor func(i int) []string) (*Engine, *store.Store, w.Run, []w.Agent, w.Step) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ws := w.Workspace{ID: w.NewID(), Name: "method", Revision: 1}
	agents := []w.Agent{}
	for i, name := range []string{"协调者", "执行者", "复核员"} {
		grant := tools.Names
		if toolsFor != nil {
			grant = toolsFor(i)
		}
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: fmt.Sprintf("member-%d", i), Model: "test", Tools: slices.Clone(grant), Revision: 1})
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, AgentIDs: []string{agents[0].ID, agents[1].ID, agents[2].ID}, CoordinatorID: agents[0].ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "running", CreatedAt: w.Now(), Agents: agents, CoordinatorID: agents[0].ID, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}, Input: w.RunInput{Message: "请分析这个问题并给出结论", Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}, Routing: w.Routing{Decision: "discuss", Targets: []string{agents[0].ID}, Explanation: "没有明确收件人，交给会话协调者"}}
	root := w.Step{ID: w.NewID(), RunID: run.ID, AgentID: agents[0].ID, AgentName: agents[0].DisplayName, Role: "responder", Instruction: "Answer the task", Status: "running"}
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, a := range agents {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "hash"); err != nil {
			return err
		}
		return tx.SaveStep(root)
	}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, AllowedModels: []string{"test"}, Web: testutil.Web{}}
	return e, s, run, agents, root
}
