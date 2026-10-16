package workflow

import (
	"context"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedRun(t *testing.T, s *store.Store) w.Run {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "demo", Revision: 1}
	agents := []w.Agent{}
	for _, name := range []string{"布偶猫", "缅因猫", "橘猫"} {
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: "cat-" + string(rune('a'+len(agents))), Model: "demo", Tools: tools.Names, Revision: 1})
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "queued", CreatedAt: w.Now(), Agents: agents, Demo: true, Input: w.RunInput{Message: "比较方法", Workflow: "research", Assignments: map[string]string{"researcher": agents[0].ID, "critic": agents[1].ID, "coordinator": agents[2].ID}}}
	steps, err := Plan(run.ID, run.Input, agents)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), func(tx *store.Tx) error {
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
		for _, step := range steps {
			if err := tx.SaveStep(step); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestQueuedCancellationSettlesPlan(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := seedRun(t, s)
	e := Engine{Store: s}
	if _, err := e.Cancel(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	s.Read(context.Background(), func(tx *store.Tx) error {
		steps, err := tx.Steps(r.ID)
		for _, step := range steps {
			if step.Status != "skipped" {
				t.Fatal("unsettled step", step.Status)
			}
		}
		return err
	})
}

func TestDispatchRevalidatesPolicyAndBoundsHistoricalSources(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke bool
		large  bool
	}{{"revoked model", true, false}, {"large old sources", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedRun(t, s)
			if tc.large {
				if err := s.Write(context.Background(), func(tx *store.Tx) error {
					for i := 0; i < 5; i++ {
						if err := tx.SaveSource(w.Source{ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "web", Title: "old", Content: strings.Repeat("x", 32768)}); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			models := []string{"demo"}
			if tc.revoke {
				models = []string{"other"}
			}
			e := Engine{Store: s, Model: model.Demo{}, Web: tools.NewWeb("", true), AllowedModels: models, Demo: true}
			ctx, cancel := context.WithCancel(context.Background())
			if err := e.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); e.Wait() }()
			got := awaitRun(t, s, run.ID)
			want := "succeeded"
			if tc.revoke {
				want = "failed"
			}
			if got.Status != want {
				t.Fatal(got.Status, got.Error)
			}
			if tc.revoke {
				s.Read(ctx, func(tx *store.Tx) error {
					steps, err := tx.Steps(run.ID)
					for _, step := range steps {
						if step.Status != "skipped" {
							t.Fatal("pending after dispatch failure")
						}
					}
					return err
				})
			}
		})
	}
}

func TestTerminalPersistenceFailureIsReported(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	r := seedRun(t, s)
	s.Close()
	e := Engine{Store: s, Errors: make(chan error, 1)}
	e.finish(context.Background(), r, "failed", nil)
	select {
	case <-e.Errors:
	default:
		t.Fatal("terminal persistence error hidden")
	}
}

func awaitRun(t *testing.T, s *store.Store, id string) w.Run {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		wake := s.Changed()
		var r w.Run
		if err := s.Read(context.Background(), func(tx *store.Tx) error { var err error; r, err = tx.Run(id); return err }); err != nil {
			t.Fatal(err)
		}
		if w.Terminal(r.Status) {
			return r
		}
		select {
		case <-deadline.C:
			t.Fatal("run did not settle")
		case <-wake:
		}
	}
}

func TestResearchExecutionAndCancellation(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "research", true: "cancel"}[cancelRun], func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedRun(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			e := Engine{Store: s, AllowedModels: []string{"demo"}, Model: model.Demo{}, Web: tools.NewWeb("", true), Demo: true}
			if err := e.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); e.Wait() }()
			if cancelRun {
				if _, err := e.Cancel(context.Background(), run.ID); err != nil {
					t.Fatal(err)
				}
			}
			result := awaitRun(t, s, run.ID)
			want := "succeeded"
			if cancelRun {
				want = "canceled"
			}
			if result.Status != want {
				t.Fatalf("status=%s error=%s", result.Status, result.Error)
			}
			if !cancelRun {
				if err := s.Read(ctx, func(tx *store.Tx) error {
					as, err := tx.Artifacts(run.ThreadID)
					if len(as) != 1 || as[0].Type != "research_report" {
						t.Fatalf("artifacts=%+v", as)
					}
					ss, _ := tx.Sources(run.ThreadID)
					if len(ss) < 3 {
						t.Fatal("members did not read sources")
					}
					es, _ := tx.Events(run.ID, 0)
					handoffs := 0
					for _, ev := range es {
						if ev.Type == "handoff_prepared" {
							handoffs++
						}
					}
					if handoffs != 2 {
						t.Fatal("missing handoffs", handoffs)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
