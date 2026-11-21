package workflow

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestLiveCollaborationLoopAcceptance exercises the single dynamic kernel with a
// real CLI: a parallel read-only fan-out, a peer-to-peer relay handoff and a
// bounded failure diagnostic. It asserts host-observable structure, never a
// preset plan template.
func TestLiveCollaborationLoopAcceptance(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_LOOP") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_LOOP=1 to run the real native collaboration-loop acceptance")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatal("the real claude CLI is required for this acceptance")
	}
	t.Setenv("ENABLE_TOOL_SEARCH", "false")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	provider := nativecli.Provider{}
	adapter, err := provider.Check(ctx, "claude")
	if err != nil || !adapter.Installed || !adapter.Supported || adapter.Isolation != "enforced" {
		t.Fatalf("real claude preflight failed: installed=%t supported=%t isolation=%s err=%v", adapter.Installed, adapter.Supported, adapter.Isolation, err)
	}
	t.Logf("preflight claude version=%s adapter=%s isolation=%s", liveSafeError(adapter.Version), liveSafeError(adapter.Adapter), liveSafeError(adapter.Isolation))

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(root, "evidence.md")
	evidence := "# Synthetic loop evidence\n\nLOOP_EVIDENCE_TOKEN=42\nThe sample is synthetic and supports no external claim.\n"
	if err := os.WriteFile(evidencePath, []byte(evidence), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ws := w.Workspace{ID: w.NewID(), Name: "live loop acceptance", Root: root, Revision: 1}
	toolGrant := []string{"read_file", "list_files", "list_sources", "read_source", "publish_artifact"}
	agents := []w.Agent{}
	for i, name := range []string{"执行成员", "复核成员", "协调成员"} {
		agents = append(agents, w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: name, Handle: "live-loop-" + string(rune('a'+i)), RoleDescription: "合成协作循环验收成员。", Executor: "claude", Model: "default", Tools: slices.Clone(toolGrant), Revision: 1})
	}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "live loop acceptance", AgentIDs: []string{agents[0].ID, agents[1].ID, agents[2].ID}, CoordinatorID: agents[2].ID, Revision: 1, UpdatedAt: w.Now()}
	if err := s.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, a := range agents {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{
		Store:         s,
		Model:         provider,
		Runners:       map[string]agent.Runner{"claude": provider},
		Adapters:      provider,
		AllowedRoots:  []string{root},
		AllowedModels: []string{"default"},
		Limits:        Limits{RunTimeout: 20 * time.Minute, StepTimeout: 8 * time.Minute, FirstResponse: 2 * time.Minute, Stall: 4 * time.Minute, StallAction: "stop"},
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()

	seed := func(in w.RunInput) w.Run {
		t.Helper()
		run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: th.ID, Workspace: ws, Agents: agents, CoordinatorID: agents[2].ID, Input: in, Status: "queued", CreatedAt: w.Now(), Routing: w.Routing{Decision: "assign", Targets: slices.Clone(in.Targets)}, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
		steps, err := Plan(run.ID, in, agents)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Write(ctx, func(tx *store.Tx) error {
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
		return run
	}
	await := func(run w.Run) w.Run {
		t.Helper()
		for {
			wake := s.Changed()
			var current w.Run
			if err := s.Read(ctx, func(tx *store.Tx) error {
				var readErr error
				current, readErr = tx.Run(run.ID)
				return readErr
			}); err != nil {
				if ctx.Err() != nil {
					t.Fatalf("live loop run %s did not settle: %v", run.ID, ctx.Err())
				}
				t.Fatal(err)
			}
			if w.Terminal(current.Status) {
				return current
			}
			select {
			case <-ctx.Done():
				t.Fatalf("live loop run %s did not settle before its deadline (status=%s)", run.ID, current.Status)
			case <-wake:
			}
		}
	}
	load := func(runID, threadID string) (w.Run, []w.Step, []w.Message, []w.Source, []w.Artifact, []w.Event) {
		t.Helper()
		var run w.Run
		var steps []w.Step
		var messages []w.Message
		var sources []w.Source
		var artifacts []w.Artifact
		var events []w.Event
		if err := s.Read(context.Background(), func(tx *store.Tx) error {
			var readErr error
			if run, readErr = tx.Run(runID); readErr != nil {
				return readErr
			}
			if steps, readErr = tx.Steps(runID); readErr != nil {
				return readErr
			}
			if messages, readErr = tx.Messages(threadID); readErr != nil {
				return readErr
			}
			if sources, readErr = tx.Sources(threadID); readErr != nil {
				return readErr
			}
			if artifacts, readErr = tx.Artifacts(threadID); readErr != nil {
				return readErr
			}
			events, readErr = tx.Events(runID, 0)
			return readErr
		}); err != nil {
			t.Fatal(err)
		}
		return run, steps, messages, sources, artifacts, events
	}
	toolFinished := func(events []w.Event, stepID, name string) int {
		count := 0
		for _, event := range events {
			if event.Type != "tool_finished" || event.StepID != stepID {
				continue
			}
			var data struct{ Name, Status string }
			if json.Unmarshal(event.Data, &data) == nil && data.Name == name && data.Status == "ok" {
				count++
			}
		}
		return count
	}

	t.Run("single addressed member answers", func(t *testing.T) {
		message := "这是隔离的合成验收。请读取绑定目录中的 evidence.md，原样写出 LOOP_EVIDENCE_TOKEN 的值，并简短说明该样例能支持什么、不能支持什么。只使用该目录，不要修改文件。"
		in := w.RunInput{Message: message, Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}
		run := seed(in)
		got := await(run)
		final, steps, messages, _, _, _ := load(run.ID, th.ID)
		logVisibleSteps(t, steps, messages)
		if got.Status != "succeeded" {
			for _, step := range steps {
				t.Logf("step role=%s status=%s failure_kind=%s error=%q", step.Role, step.Status, step.FailureKind, liveSafeError(step.Error))
			}
			t.Fatalf("direct run failed: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		if len(steps) != 1 || steps[0].AgentID != agents[0].ID || steps[0].ReadOnly {
			t.Fatalf("a single addressed member must be one writable step: %+v", steps)
		}
		quoted := slices.ContainsFunc(messages, func(m w.Message) bool {
			return m.RunID == run.ID && m.Role == "assistant" && strings.Contains(m.Content, "LOOP_EVIDENCE_TOKEN")
		})
		if !quoted {
			t.Fatal("the reply did not quote the bound evidence")
		}
		if final.Delivery == nil || len(final.Delivery.MessageIDs) == 0 {
			t.Fatalf("direct delivery missing: %+v", final.Delivery)
		}
		t.Logf("direct accepted: step=%s", liveSafeError(steps[0].ID))
	})

	t.Run("parallel members are independent and read-only", func(t *testing.T) {
		before, err := os.ReadFile(evidencePath)
		if err != nil {
			t.Fatal(err)
		}
		message := "这是隔离的合成并行验收。请各自独立回答：evidence.md 中的标记值是什么、该样例有什么限制？不要修改文件，不要运行测试。"
		in := w.RunInput{Message: message, Targets: []string{agents[0].ID, agents[1].ID}, ResearchScope: "given_sources"}
		run := seed(in)
		got := await(run)
		_, steps, messages, _, _, events := load(run.ID, th.ID)
		logVisibleSteps(t, steps, messages)
		if got.Status != "succeeded" {
			for _, step := range steps {
				t.Logf("parallel step agent=%s status=%s failure_kind=%s error=%q", liveSafeError(step.AgentID), step.Status, step.FailureKind, liveSafeError(step.Error))
			}
			t.Fatalf("parallel run failed: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		if len(steps) != 2 {
			t.Fatalf("parallel fan-out must create one step per member: %+v", steps)
		}
		for _, step := range steps {
			if !step.ReadOnly {
				t.Fatalf("parallel blind pass must be read-only: %+v", step)
			}
			if len(step.DependsOn) != 0 {
				t.Fatalf("independent views must not depend on a sibling: %+v", step.DependsOn)
			}
		}
		for _, event := range events {
			if event.Type != "tool_finished" {
				continue
			}
			var data struct{ Name, Status string }
			_ = json.Unmarshal(event.Data, &data)
			if data.Status == "ok" && (data.Name == "write_file" || data.Name == "patch_file" || data.Name == "run_test") {
				t.Fatalf("a read-only parallel step executed %s", data.Name)
			}
		}
		after, err := os.ReadFile(evidencePath)
		if err != nil || string(after) != string(before) {
			t.Fatal("a read-only parallel pass changed the synthetic project")
		}
		t.Logf("parallel accepted: steps=%d", len(steps))
	})

	t.Run("relay handoff reaches an independent reviewer", func(t *testing.T) {
		message := "请执行成员读取 evidence.md 并写出标记值，然后用 handoff_task(mode='relay') 交给复核成员独立核对同一个文件；复核成员只回答核对结论。不要发布产物。"
		in := w.RunInput{Message: message, Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}
		run := seed(in)
		got := await(run)
		_, steps, messages, _, _, events := load(run.ID, th.ID)
		logVisibleSteps(t, steps, messages)
		if got.Status != "succeeded" {
			for _, step := range steps {
				t.Logf("relay step kind=%s role=%s status=%s failure_kind=%s error=%q", step.Kind, step.Role, step.Status, step.FailureKind, liveSafeError(step.Error))
			}
			t.Fatalf("relay run failed: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		relayed := slices.IndexFunc(steps, func(s w.Step) bool { return s.Kind == "relay" })
		if relayed < 0 {
			t.Fatalf("no relay step was created: %+v", steps)
		}
		if steps[relayed].AgentID != agents[1].ID {
			t.Fatalf("relay did not reach the reviewer: %+v", steps[relayed])
		}
		if steps[relayed].Role == "reviewer" || steps[relayed].Role == "critic" {
			t.Logf("reviewer read_context successes=%d", toolFinished(events, steps[relayed].ID, "read_context"))
		}
		if got.Delivery == nil || len(got.Delivery.MessageIDs) == 0 {
			t.Fatalf("relay delivery missing: %+v", got.Delivery)
		}
		t.Logf("relay accepted: steps=%d", len(steps))
	})

	t.Run("failure diagnostics follow-up", func(t *testing.T) {
		failed := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: th.ID, Workspace: ws, Agents: agents, CoordinatorID: agents[2].ID, Input: w.RunInput{Message: "synthetic prior failure"}, Status: "failed", FailureKind: "tool_unavailable", Error: "LOOP_DIAG_ERROR_TOKEN: synthetic host failure", CreatedAt: "2026-10-01T00:00:00Z", Delivery: nil}
		if err := s.Write(ctx, func(tx *store.Tx) error {
			if err := tx.InsertRun(failed, w.NewID(), "failed-"+failed.ID); err != nil {
				return err
			}
			return tx.SaveStep(w.Step{ID: w.NewID(), RunID: failed.ID, AgentID: agents[0].ID, AgentName: agents[0].DisplayName, Role: "responder", Status: "failed", FailureKind: "tool_unavailable", Error: "native CLI requested an unapproved native tool: bash"})
		}); err != nil {
			t.Fatal(err)
		}
		message := "这是隔离的合成失败诊断验收。上一轮 Run 失败了。请从最近的失败/取消/中断 Run 诊断元数据中读出失败步骤的 failure_kind 和真实错误，原样写出 LOOP_DIAG_ERROR_TOKEN 与 tool_unavailable；诊断只说明运行状态，不是证据，不能引用为来源，也不要重试或编造。简短回答。"
		in := w.RunInput{Message: message, Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}
		run := seed(in)
		got := await(run)
		if got.Status != "succeeded" {
			t.Fatalf("diagnostic follow-up failed: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		_, _, messages, _, _, _ := load(run.ID, th.ID)
		quoted := slices.ContainsFunc(messages, func(m w.Message) bool {
			return m.RunID == run.ID && m.Role == "assistant" && strings.Contains(m.Content, "LOOP_DIAG_ERROR_TOKEN") && strings.Contains(m.Content, "tool_unavailable")
		})
		if !quoted {
			t.Fatal("the follow-up did not quote the bounded host failure diagnostics")
		}
		t.Logf("failure diagnostics follow-up accepted")
	})
}
