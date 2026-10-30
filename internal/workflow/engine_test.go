package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func seedRun(t *testing.T, s *store.Store) w.Run {
	t.Helper()
	ws := w.Workspace{ID: w.NewID(), Name: "test", Revision: 1}
	agents := []w.Agent{}
	for _, name := range []string{"布偶猫", "缅因猫", "橘猫"} {
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: "cat-" + string(rune('a'+len(agents))), Model: "test", Tools: tools.Names, Revision: 1})
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Status: "queued", CreatedAt: w.Now(), Agents: agents, Input: w.RunInput{Message: "比较方法", Workflow: "research", ResearchScope: "given_sources", SourceURLs: testutil.Sources, Assignments: map[string]string{"researcher": agents[0].ID, "critic": agents[1].ID, "coordinator": agents[2].ID}}}
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
						if err := tx.SaveSource(w.Source{ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "web", Title: strings.Repeat("x", 120000), URL: "https://example.com/" + strings.Repeat("x", 3900), Content: strings.Repeat("x", 32768)}); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			models := []string{"test"}
			if tc.revoke {
				models = []string{"other"}
			}
			e := Engine{Store: s, Model: testutil.Model{}, Web: testutil.Web{}, AllowedModels: models}
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

func TestStartupRecoveryPreservesPartialEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run := seedRun(t, s)
	var stepID string
	var attemptID string
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		run.Status = "running"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		steps[0].Status = "running"
		stepID = steps[0].ID
		if err := tx.SaveStep(steps[0]); err != nil {
			return err
		}
		attempt := w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: stepID, Number: 1, Status: "running", Reason: "initial", StartedAt: w.Now(), Process: &w.ProcessRef{PID: 999999, PGID: 999999, Start: "Mon Jan  1 00:00:00 2001"}}
		attemptID = attempt.ID
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: stepID, Content: "partial result", Status: "processing"})
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	e := Engine{Store: s, Model: testutil.Model{}, Web: testutil.Web{}, AllowedModels: []string{"test"}}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	got := awaitRun(t, s, run.ID)
	if got.Status != "succeeded" || got.RecoveryCount != 1 {
		t.Fatalf("recovery did not continue from the checkpoint: %+v", got)
	}
	if err := s.Read(ctx, func(tx *store.Tx) error {
		messages, err := tx.Messages(run.ThreadID)
		if err != nil {
			return err
		}
		preserved := false
		for _, message := range messages {
			if message.Content == "partial result" && message.Status == "interrupted" {
				preserved = true
			}
		}
		if !preserved {
			t.Fatalf("partial history was overwritten: %+v", messages)
		}
		attempts, err := tx.Attempts(run.ID)
		if err != nil {
			return err
		}
		seen := []w.Attempt{}
		for _, attempt := range attempts {
			if attempt.StepID == stepID {
				seen = append(seen, attempt)
			}
		}
		if len(seen) != 2 || seen[0].ID != attemptID || seen[0].Status != "interrupted" || seen[1].Status != "succeeded" {
			t.Fatalf("attempts were not kept as separate executions: %+v", seen)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactRevisionAndFIFORecovery(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := seedRun(t, s)
	second := seedRun(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	e := Engine{Store: s, Model: testutil.Model{}, Web: testutil.Web{}, AllowedModels: []string{"test"}}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	one := awaitRun(t, s, first.ID)
	two := awaitRun(t, s, second.ID)
	if one.Status != "succeeded" || two.Status != "succeeded" || one.FinishedAt > two.FinishedAt {
		t.Fatal("FIFO was not preserved")
	}
	var original w.Artifact
	var next w.Run
	if err := s.Write(ctx, func(tx *store.Tx) error {
		artifacts, err := tx.Artifacts(first.ThreadID)
		if err != nil {
			return err
		}
		original = artifacts[0]
		next = first
		next.ID = w.NewID()
		next.Status = "queued"
		next.Error = ""
		next.CreatedAt = w.Now()
		next.FinishedAt = ""
		next.Input.ArtifactRefs = []w.ArtifactRef{{ID: original.ID, Version: 1}}
		if err := tx.InsertRun(next, w.NewID(), "next"); err != nil {
			return err
		}
		steps, err := Plan(next.ID, next.Input, next.Agents)
		if err != nil {
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
	if got := awaitRun(t, s, next.ID); got.Status != "succeeded" {
		t.Fatal(got.Error)
	}
	if err := s.Read(ctx, func(tx *store.Tx) error {
		v1, err := tx.Artifact(original.ID, 1)
		if err != nil {
			return err
		}
		v2, err := tx.Artifact(original.ID, 2)
		if err != nil {
			return err
		}
		if v1.RunID == v2.RunID || v1.Body != original.Body {
			t.Fatal("version overwritten")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStepCommitFailureCannotLeaveProcessingBubble(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	fault, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close()
	if _, err := fault.Exec(`CREATE TRIGGER fail_step_completion BEFORE UPDATE ON steps WHEN NEW.status='succeeded' BEGIN SELECT RAISE(FAIL,'test completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := Engine{Store: s, Model: testutil.Model{}, Web: testutil.Web{}, AllowedModels: []string{"test"}}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	result := awaitRun(t, s, run.ID)
	if result.Status != "failed" {
		t.Fatal(result.Status)
	}
	if err := s.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		for _, step := range steps {
			if step.Status == "pending" || step.Status == "running" {
				t.Fatal("unsettled step", step)
			}
		}
		messages, err := tx.Messages(run.ThreadID)
		for _, m := range messages {
			if m.Status == "processing" {
				t.Fatal("processing message after terminal")
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.Errors:
	default:
		t.Fatal("commit failure was hidden")
	}
}

func TestSearchRevokedBeforeQueuedDispatch(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	if err := s.Write(context.Background(), func(tx *store.Tx) error { run.Input.ResearchScope = "web_discovery"; return tx.SaveRun(run) }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := Engine{Store: s, Model: testutil.Model{}, Web: tools.NewWeb(""), AllowedModels: []string{"test"}}
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); e.Wait() }()
	if result := awaitRun(t, s, run.ID); result.Status != "failed" || !strings.Contains(result.Error, "search") {
		t.Fatal(result)
	}
	s.Read(ctx, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		for _, step := range steps {
			if step.Status != "skipped" {
				t.Fatal("model step launched")
			}
		}
		return err
	})
}

func TestLegacyWorkspaceCanDispatchWithoutConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.ExecContext(t.Context(), "UPDATE workspaces SET body=json_set(body,'$.consent_version',0) WHERE id=?", run.WorkspaceID)
	closeErr := legacy.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("legacy workspace: %v, close: %v", err, closeErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	engine := Engine{Store: s, Model: testutil.Model{}, Web: testutil.Web{}, AllowedModels: []string{"test"}}
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	if got := awaitRun(t, s, run.ID); got.Status != "succeeded" {
		t.Fatalf("legacy dispatch: %+v", got)
	}
}

func awaitRun(t *testing.T, s *store.Store, id string) w.Run {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
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
			e := Engine{Store: s, AllowedModels: []string{"test"}, Model: testutil.Model{}, Web: testutil.Web{}}
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
					if handoffs != 4 {
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

func TestMessageSnapshotsAttemptModelAndSurvivesMemberEdit(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	enableExecutor(t, s, &run, "claude")
	checker := &staticChecker{info: agent.AdapterInfo{ID: "claude", Installed: true, Supported: true}}
	inspector := &staticInspector{staticChecker: checker, connection: agent.ConnectionInfo{ResolvedModel: "resolved-model", Identity: "connection-identity"}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": defaultRunner()}, Adapters: inspector, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	result := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if result.Status != "succeeded" {
		t.Fatalf("status=%s error=%s", result.Status, result.Error)
	}
	messages := func() []w.Message {
		var items []w.Message
		if err := s.Read(context.Background(), func(tx *store.Tx) error {
			var err error
			items, err = tx.Messages(run.ThreadID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return items
	}
	var attempts []w.Attempt
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		attempts, err = tx.Attempts(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	byAttempt := map[string]w.Attempt{}
	for _, attempt := range attempts {
		byAttempt[attempt.ID] = attempt
	}
	before := messages()
	assistant := 0
	for _, message := range before {
		if message.Role != "assistant" {
			continue
		}
		assistant++
		attempt, ok := byAttempt[message.AttemptID]
		if !ok || attempt.StepID != message.StepID || attempt.RunID != message.RunID {
			t.Fatalf("message is not bound to its own attempt: %+v", message)
		}
		if message.Model != "default" || message.ResolvedModel != "resolved-model" || message.ResolvedModel != attempt.ResolvedModel {
			t.Fatalf("message model snapshot = %+v", message)
		}
	}
	if assistant != 5 {
		t.Fatalf("assistant messages = %d", assistant)
	}
	// Editing the reusable member profile must not rewrite already produced history.
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		for i := range run.Agents {
			run.Agents[i].Model = "edited-model"
			if err := tx.SaveAgent(run.Agents[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, message := range messages() {
		if message.Role == "assistant" && (message.Model != "default" || message.ResolvedModel != "resolved-model") {
			t.Fatalf("member edit rewrote message history: %+v", message)
		}
	}
}

type nativeRunnerFunc func(context.Context, model.Request, agent.Execute, agent.Observe) error

func (f nativeRunnerFunc) Run(ctx context.Context, r model.Request, execute agent.Execute, observe agent.Observe) error {
	return f(ctx, r, execute, observe)
}

func TestNativeStepsConsumeExactPublishedUpstreamArtifacts(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		for i := range run.Agents {
			run.Agents[i].Executor = "claude"
			run.Agents[i].Model = "default"
			if err := tx.SaveAgent(run.Agents[i]); err != nil {
				return err
			}
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		// A historical intermediate artifact stays readable in the handoff, but
		// new intermediate steps may not publish formal artifacts.
		if err := tx.SaveArtifact(w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, Type: "note", Title: "Structured finding", Body: "upstream-only-structured-finding", Feedback: "unreviewed"}); err != nil {
			return err
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	criticSawArtifact := false
	runner := nativeRunnerFunc(func(ctx context.Context, r model.Request, execute agent.Execute, observe agent.Observe) error {
		if r.Executor != "claude" {
			return fmt.Errorf("wrong executor %s", r.Executor)
		}
		if strings.Contains(r.System, "role=critic") {
			if !strings.Contains(r.Messages[0].Content[0].Text, "upstream-only-structured-finding") {
				return fmt.Errorf("critic did not receive the structured upstream artifact")
			}
			criticSawArtifact = true
			return observe("text", map[string]any{"text": "Independent critique preserves insufficient evidence."})
		}
		if !strings.Contains(r.System, "role=coordinator") {
			return observe("text", map[string]any{"text": "intermediate result"})
		}
		payload := map[string]any{"type": "research_report", "title": "Structured result", "body": "upstream-only-structured-finding", "source_ids": []string{}, "limitations": []string{"fixture only"}, "findings": []any{map[string]any{"title": "Uncertain claim", "detail": "upstream-only-structured-finding", "status": "insufficient_evidence", "source_ids": []string{}}}}
		raw, _ := json.Marshal(payload)
		_, err := execute(ctx, "publish_artifact", raw)
		return err
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "succeeded" || !criticSawArtifact {
		t.Fatalf("status=%s err=%s criticSaw=%v", got.Status, got.Error, criticSawArtifact)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Type == "handoff_prepared" {
				var h Handoff
				if err := json.Unmarshal(event.Data, &h); err != nil {
					return err
				}
				if len(h.Artifacts) == 0 || h.Artifacts[0].Version != 1 {
					return fmt.Errorf("handoff lacks exact published artifact")
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRevokedMemberToolStopsQueuedDispatch(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		a := run.Agents[0]
		a.Tools = []string{"publish_artifact"}
		a.Revision++
		return tx.SaveAgent(a)
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	e := Engine{Store: s, Model: testutil.Model{}, Web: testutil.Web{}, AllowedModels: []string{"test"}}
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "failed" || !strings.Contains(got.Error, "tool permission was revoked") {
		t.Fatalf("run=%+v", got)
	}
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		for _, step := range steps {
			if step.Status != "skipped" {
				return fmt.Errorf("step launched after revocation: %s", step.Status)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDependencySkipsPreserveOriginalNativeFailure(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		for i := range run.Agents {
			run.Agents[i].Executor = "claude"
			if err := tx.SaveAgent(run.Agents[i]); err != nil {
				return err
			}
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(context.Context, model.Request, agent.Execute, agent.Observe) error {
		return fmt.Errorf("upstream model connection unavailable")
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Web: testutil.Web{}}
	ctx, cancel := context.WithCancel(t.Context())
	if err := e.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	got := awaitRun(t, s, run.ID)
	cancel()
	e.Wait()
	if got.Status != "failed" || got.Error != "upstream model connection unavailable" {
		t.Fatalf("lost original failure: %+v", got)
	}
}
