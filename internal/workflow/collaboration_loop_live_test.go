package workflow

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// liveReviewedText keeps synthetic step and report bodies readable for
// independent review while applying the same credential redaction as
// liveSafeError. Only synthetic acceptance output is logged.
func liveReviewedText(value string) string {
	value = liveBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = liveSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, "=:"); index >= 0 {
			return match[:index+1] + "[redacted]"
		}
		return "[redacted]"
	})
	if len(value) > 6000 {
		return trimBytes(value, 6000) + "\n…[logged body truncated]"
	}
	return value
}

func logVisibleSteps(t *testing.T, steps []w.Step, messages []w.Message) {
	t.Helper()
	for _, step := range steps {
		for _, message := range messages {
			if message.RunID != step.RunID || message.StepID != step.ID || message.Role != "assistant" || message.Status != "completed" {
				continue
			}
			t.Logf("visible step role=%s status=%s message_id=%s:\n%s", step.Role, step.Status, liveSafeError(message.ID), liveReviewedText(message.Content))
		}
	}
}

func logFinalArtifact(t *testing.T, artifact w.Artifact) {
	t.Helper()
	t.Logf("final artifact type=%s title=%q id=%s version=%d source_ids=%d findings=%d limitations=%d body:\n%s", artifact.Type, liveSafeError(artifact.Title), liveSafeError(artifact.ID), artifact.Version, len(artifact.SourceIDs), len(artifact.Findings), len(artifact.Limitations), liveReviewedText(artifact.Body))
	for _, finding := range artifact.Findings {
		t.Logf("artifact finding status=%s severity=%s title=%q detail=%s sources=%v", finding.Status, finding.Severity, liveSafeError(finding.Title), liveReviewedText(finding.Detail), finding.SourceIDs)
	}
	for _, limitation := range artifact.Limitations {
		t.Logf("artifact limitation: %s", liveReviewedText(limitation))
	}
}

// This opt-in acceptance drives the accepted research/read-only-analysis plans
// through the real native CLI and the user's existing default model. It uses
// only a synthetic temporary project and SQLite database; no user project,
// history, credential file or database is read. Provider failures are reported
// as failures, never relabeled as success.
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
	for i, name := range []string{"研究成员", "质疑成员", "协调成员"} {
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

	seed := func(in w.RunInput, planInput w.RunInput) w.Run {
		t.Helper()
		run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: th.ID, Workspace: ws, Agents: agents, CoordinatorID: agents[2].ID, Input: in, Status: "queued", CreatedAt: w.Now(), Routing: w.Routing{Decision: "assign", Workflow: "free", Targets: []string{agents[0].ID}}, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
		steps, err := Plan(run.ID, planInput, agents)
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
					t.Fatalf("live loop run %s did not settle: %v", run.Input.Workflow, ctx.Err())
				}
				t.Fatal(err)
			}
			if w.Terminal(current.Status) {
				return current
			}
			select {
			case <-ctx.Done():
				t.Fatalf("live loop run %s did not settle before its deadline (status=%s)", run.Input.Workflow, current.Status)
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
	roles := func(steps []w.Step) []string {
		out := []string{}
		for _, step := range steps {
			out = append(out, step.Role)
		}
		return out
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

	var research w.Run
	t.Run("research five steps", func(t *testing.T) {
		// The user prompt states only the question and scope. Step roles, tools
		// and reads come from the accepted plan and runtime guidance, so this is
		// evidence of natural use rather than a hand-written procedure.
		message := "请研究这个合成问题：绑定目录中的 evidence.md 能支持什么结论、有哪些限制？只使用该绑定目录，不要读取用户项目或网络，也不要修改文件。请把 LOOP_EVIDENCE_TOKEN=42 作为唯一事实原样保留，引用实际读到的证据，并如实保留不确定或未决之处。"
		in := w.RunInput{Message: message, Workflow: "research", ResearchScope: "given_sources", Assignments: map[string]string{"researcher": agents[0].ID, "critic": agents[1].ID, "coordinator": agents[2].ID}}
		research = seed(in, in)
		got := await(research)
		research = got
		if got.Status != "succeeded" {
			_, steps, messages, _, _, _ := load(research.ID, th.ID)
			for _, step := range steps {
				t.Logf("step role=%s status=%s failure_kind=%s error=%q", step.Role, step.Status, step.FailureKind, liveSafeError(step.Error))
			}
			for _, m := range messages {
				if m.RunID == research.ID && m.Role == "assistant" {
					t.Logf("assistant %s: %s", m.Role, liveSafeError(m.Content))
				}
			}
			t.Fatalf("research run failed: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		final, steps, messages, sources, artifacts, events := load(research.ID, th.ID)
		logVisibleSteps(t, steps, messages)
		var report *w.Artifact
		for i := range artifacts {
			if artifacts[i].RunID == research.ID && artifacts[i].Type == "research_report" {
				report = &artifacts[i]
			}
		}
		if report != nil {
			logFinalArtifact(t, *report)
		}
		if !slices.Equal(roles(steps), []string{"researcher", "critic", "researcher", "critic", "coordinator"}) {
			t.Fatalf("research roles = %v", roles(steps))
		}
		if !slices.Equal(steps[3].DependsOn, []string{steps[0].ID, steps[1].ID, steps[2].ID}) || !slices.Equal(steps[4].DependsOn, []string{steps[0].ID, steps[1].ID, steps[2].ID, steps[3].ID}) {
			t.Fatalf("exact research dependency chain missing: step3=%v step4=%v", steps[3].DependsOn, steps[4].DependsOn)
		}
		for _, step := range steps {
			if step.Status != "succeeded" {
				t.Fatalf("research step role=%s status=%s error=%q", step.Role, step.Status, liveSafeError(step.Error))
			}
		}
		// read_context is only mandatory when a preview was cut. The visible
		// step bodies and the structural dependency chain are logged for
		// independent review of whether each challenge was answered.
		for _, index := range []int{2, 3, 4} {
			t.Logf("step %d (%s) read_context successes=%d", index+1, steps[index].Role, toolFinished(events, steps[index].ID, "read_context"))
		}
		if report == nil || report.StepID != steps[4].ID || len(report.SourceIDs) == 0 {
			t.Fatalf("research delivery is not the coordinator's single report: %+v", artifacts)
		}
		if final.Delivery == nil || len(final.Delivery.ArtifactRefs) != 1 || final.Delivery.ArtifactRefs[0].ID != report.ID || final.Delivery.ArtifactRefs[0].Version != report.Version {
			t.Fatalf("research delivery refs = %+v, report %s@%d", final.Delivery, report.ID, report.Version)
		}
		fileSources := map[string]bool{}
		originalSources := map[string]bool{}
		sourceKinds := map[string]string{}
		for _, source := range sources {
			sourceKinds[source.ID] = source.Kind
			strength := evidenceStrength(source.Kind)
			if strength == "original" && tools.IsSearchResultURL(source.URL) {
				strength = "lead"
			}
			if strength != "lead" {
				originalSources[source.ID] = true
			}
			if source.Kind == "file" || source.Kind == "native_file" {
				fileSources[source.ID] = true
			}
		}
		for _, id := range report.SourceIDs {
			t.Logf("research report source id=%s kind=%s original=%t file=%t", liveSafeError(id), sourceKinds[id], originalSources[id], fileSources[id])
			if _, ok := sourceKinds[id]; !ok {
				t.Fatalf("research report cites an uncaptured source %s", liveSafeError(id))
			}
		}
		if !slices.ContainsFunc(report.SourceIDs, func(id string) bool { return originalSources[id] }) {
			t.Fatalf("research report cites no read original source: %v", report.SourceIDs)
		}
		// Match ValidateArtifact exactly: a supported finding needs at least one
		// read original, all cited sources must be captured, and leads may sit
		// alongside originals. A lead-only conclusion stays invalid because
		// publication already rejected it; disputed findings need captured
		// evidence but are not required to be supported by an original.
		for _, finding := range report.Findings {
			hasOriginal := false
			for _, id := range finding.SourceIDs {
				if _, ok := sourceKinds[id]; !ok {
					t.Fatalf("research finding %q cites an uncaptured source %s", finding.Title, liveSafeError(id))
				}
				if originalSources[id] {
					hasOriginal = true
				}
			}
			if finding.Status == "supported" && !hasOriginal {
				t.Fatalf("research finding %q is supported by leads only: %v kinds=%v", finding.Title, finding.SourceIDs, sourceKinds)
			}
		}
		t.Logf("research accepted: steps=%d report=%s sources=%d events=%d", len(steps), liveSafeError(report.ID), len(sources), len(events))
	})

	t.Run("historical result reviewed by another member", func(t *testing.T) {
		if research.ID == "" {
			t.Fatal("run the research subtest first in the same invocation")
		}
		_, researchSteps, messages, sources, researchArtifacts, _ := load(research.ID, th.ID)
		if len(researchSteps) != 5 || research.Status != "succeeded" {
			t.Fatal("research prerequisite missing")
		}
		committed := map[string]bool{}
		for _, m := range messages {
			if m.RunID == research.ID && m.Status == "completed" {
				committed[m.ID] = true
			}
		}
		realArtifacts := map[string]bool{}
		for _, artifact := range researchArtifacts {
			if artifact.RunID == research.ID {
				realArtifacts[fmt.Sprintf("%s@%d", artifact.ID, artifact.Version)] = true
			}
		}
		sourceIDs := map[string]bool{}
		for _, source := range sources {
			if source.RunID == research.ID {
				sourceIDs[source.ID] = true
			}
		}
		// Natural follow-up: no tool names, IDs, step list or hand-written
		// procedure. The coordinator must find the prior report in Shared
		// history and delegate with a real committed reference.
		message := "请另一位成员独立复核刚才那份研究报告的结论与限制。"
		in := w.RunInput{Message: message, Workflow: "free", Targets: []string{agents[2].ID}, ResearchScope: "given_sources"}
		run := seed(in, in)
		got := await(run)
		_, steps, runMessages, _, _, events := load(run.ID, th.ID)
		logVisibleSteps(t, steps, runMessages)
		if got.Status != "succeeded" {
			for _, step := range steps {
				t.Logf("history step kind=%s role=%s status=%s failure_kind=%s error=%q", step.Kind, step.Role, step.Status, step.FailureKind, liveSafeError(step.Error))
			}
			t.Fatalf("historical review run failed: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		var child *w.Step
		for i := range steps {
			if steps[i].Kind == "handoff" {
				child = &steps[i]
			}
		}
		if child == nil || child.Role != "reviewer" || child.Status != "succeeded" {
			t.Fatalf("historical reviewer handoff missing: %+v", steps)
		}
		if len(child.ContextRefs) == 0 {
			t.Fatal("reviewer handoff has no exact references")
		}
		referencedMessage := false
		for _, ref := range child.ContextRefs {
			switch {
			case committed[ref]:
				referencedMessage = true
			case realArtifacts[ref], sourceIDs[ref]:
			default:
				t.Fatalf("context ref %q is not a real committed input of the research run: %v", liveSafeError(ref), child.ContextRefs)
			}
		}
		if !referencedMessage {
			t.Fatalf("acceptance requires a real committed message reference: %v", child.ContextRefs)
		}
		// A complete handoff preview is sufficient; only a cut preview must be
		// paged with read_context. Log actual usage instead of requiring it.
		t.Logf("reviewer read_context successes=%d refs=%d", toolFinished(events, child.ID, "read_context"), len(child.ContextRefs))
		if got.Delivery == nil || len(got.Delivery.MessageIDs) == 0 {
			t.Fatalf("follow-up delivery missing: %+v", got.Delivery)
		}
		delivered := false
		for _, m := range runMessages {
			if m.RunID == run.ID && m.Status == "completed" && slices.Contains(got.Delivery.MessageIDs, m.ID) {
				delivered = true
			}
		}
		if !delivered {
			t.Fatal("follow-up delivery does not reference a committed current-run message")
		}
		t.Logf("history review accepted: refs=%d reviewer_step=%s", len(child.ContextRefs), liveSafeError(child.ID))
	})

	t.Run("read-only analysis four steps", func(t *testing.T) {
		before, err := os.ReadFile(evidencePath)
		if err != nil {
			t.Fatal(err)
		}
		message := "这是隔离的合成只读代码分析。请只读分析绑定目录中的 evidence.md：它支持什么结论、有哪些限制？不要修改文件、运行测试或声称运行时验证；引用实际读到的文件证据，保持简短。"
		in := w.RunInput{Message: message, Workflow: "code_analysis", ResearchScope: "given_sources", Assignments: map[string]string{"analyst": agents[0].ID, "reviewer": agents[1].ID}}
		run := seed(in, in)
		got := await(run)
		if got.Status != "succeeded" {
			_, steps, messages, _, _, _ := load(run.ID, th.ID)
			for _, step := range steps {
				t.Logf("analysis step role=%s status=%s failure_kind=%s error=%q", step.Role, step.Status, step.FailureKind, liveSafeError(step.Error))
			}
			for _, m := range messages {
				if m.RunID == run.ID && m.Role == "assistant" {
					t.Logf("assistant %s: %s", m.Role, liveSafeError(m.Content))
				}
			}
			t.Fatalf("analysis run failed: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		final, steps, messages, _, artifacts, events := load(run.ID, th.ID)
		logVisibleSteps(t, steps, messages)
		var report *w.Artifact
		for i := range artifacts {
			if artifacts[i].RunID == run.ID && artifacts[i].Type == "code_analysis" {
				report = &artifacts[i]
			}
		}
		if report != nil {
			logFinalArtifact(t, *report)
		}
		if !slices.Equal(roles(steps), []string{"analyst", "critic", "analyst", "final_reviewer"}) {
			t.Fatalf("analysis roles = %v", roles(steps))
		}
		if !slices.Contains(steps[3].DependsOn, steps[0].ID) || !slices.Contains(steps[3].DependsOn, steps[1].ID) || !slices.Contains(steps[3].DependsOn, steps[2].ID) {
			t.Fatalf("final reviewer deps do not include the initial analysis, review and response: %v", steps[3].DependsOn)
		}
		if report == nil || report.StepID != steps[3].ID || len(report.SourceIDs) == 0 {
			t.Fatalf("analysis delivery is not the final reviewer's report: %+v", artifacts)
		}
		if final.Delivery == nil || len(final.Delivery.ArtifactRefs) != 1 || final.Delivery.ArtifactRefs[0].ID != report.ID {
			t.Fatalf("analysis delivery refs = %+v", final.Delivery)
		}
		for _, event := range events {
			if event.Type != "tool_finished" {
				continue
			}
			var data struct{ Name, Status string }
			_ = json.Unmarshal(event.Data, &data)
			if data.Name == "write_file" || data.Name == "patch_file" || data.Name == "run_test" || (data.Name == "bash" && data.Status == "ok") {
				t.Fatalf("read-only analysis executed %s with status %s", data.Name, data.Status)
			}
		}
		after, err := os.ReadFile(evidencePath)
		if err != nil || string(after) != string(before) {
			t.Fatal("read-only analysis changed the synthetic project")
		}
		t.Logf("analysis accepted: steps=%d report=%s sources=%d", len(steps), liveSafeError(report.ID), len(report.SourceIDs))
	})

	t.Run("failure diagnostics follow-up", func(t *testing.T) {
		failed := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: th.ID, Workspace: ws, Agents: agents, CoordinatorID: agents[2].ID, Input: w.RunInput{Message: "synthetic prior failure"}, Status: "failed", FailureKind: "tool_unavailable", Error: "LOOP_DIAG_ERROR_TOKEN: synthetic host failure", CreatedAt: "2026-10-01T00:00:00Z", Delivery: nil}
		if err := s.Write(ctx, func(tx *store.Tx) error {
			if err := tx.InsertRun(failed, w.NewID(), "failed-"+failed.ID); err != nil {
				return err
			}
			return tx.SaveStep(w.Step{ID: w.NewID(), RunID: failed.ID, AgentID: agents[0].ID, AgentName: agents[0].DisplayName, Role: "researcher", Status: "failed", FailureKind: "tool_unavailable", Error: "native CLI requested an unapproved native tool: bash"})
		}); err != nil {
			t.Fatal(err)
		}
		message := "这是隔离的合成失败诊断验收。上一轮 Run 失败了。请从最近的失败/取消/中断 Run 诊断元数据中读出失败步骤的 failure_kind 和真实错误，原样写出 LOOP_DIAG_ERROR_TOKEN 与 tool_unavailable；诊断只说明运行状态，不是证据，不能引用为来源，也不要重试或编造。简短回答。"
		in := w.RunInput{Message: message, Workflow: "free", Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}
		run := seed(in, in)
		got := await(run)
		if got.Status != "succeeded" {
			t.Fatalf("diagnostic follow-up failed: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		_, _, messages, _, _, _ := load(run.ID, th.ID)
		quoted := false
		for _, m := range messages {
			if m.RunID == run.ID && m.Role == "assistant" && strings.Contains(m.Content, "LOOP_DIAG_ERROR_TOKEN") && strings.Contains(m.Content, "tool_unavailable") {
				quoted = true
			}
		}
		if !quoted {
			t.Fatal("the follow-up did not quote the bounded host failure diagnostics")
		}
		t.Logf("failure diagnostics follow-up accepted")
	})

	t.Run("tool error continues", func(t *testing.T) {
		message := "这是隔离的合成工具错误恢复验收。先调用 mcp__cocogem__read_file 读取不存在的 missing.md，记录真实错误文本；然后不要放弃，改为读取 evidence.md，简短回答你看到的真实错误和恢复结果。不要编造文件内容。"
		in := w.RunInput{Message: message, Workflow: "free", Targets: []string{agents[0].ID}, ResearchScope: "given_sources"}
		run := seed(in, in)
		got := await(run)
		if got.Status != "succeeded" {
			t.Fatalf("tool-error follow-up did not continue: kind=%s error=%q", got.FailureKind, liveSafeError(got.Error))
		}
		_, _, _, _, _, events := load(run.ID, th.ID)
		errored, recovered := false, false
		for _, event := range events {
			if event.Type != "tool_finished" {
				continue
			}
			var data struct{ Name, Status string }
			_ = json.Unmarshal(event.Data, &data)
			if data.Name != "read_file" && data.Name != "Read" {
				continue
			}
			if data.Name == "read_file" && data.Status == "error" {
				errored = true
			}
			if errored && data.Status == "ok" {
				recovered = true
			}
		}
		if !errored || !recovered {
			t.Fatalf("tool error was not continued: errored=%t recovered=%t", errored, recovered)
		}
		t.Logf("tool error follow-up accepted")
	})
}

// TestLiveDeliveredHistoryArtifactReview is a separately runnable opt-in
// acceptance for exact delivered-artifact discovery. It seeds a synthetic
// succeeded research run whose delivery pairs a short publication summary with
// a long research_report, then asks the real CLI in natural language to have
// another member review the report. The report body carries a counterexample
// beyond every bounded preview, so only read_context of the exact id@version
// reaches it. This is evidence that a real model can take over committed
// synthetic history; it claims no fresh research or network quality.
func TestLiveDeliveredHistoryArtifactReview(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_LOOP") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_LOOP=1 to run the real delivered-history artifact acceptance")
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
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ws := w.Workspace{ID: w.NewID(), Name: "live delivered history acceptance", Root: root, Revision: 1}
	toolGrant := []string{"read_file", "list_files", "list_sources", "read_source", "publish_artifact"}
	author := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "原研究成员", Handle: "live-history-author", RoleDescription: "合成历史研究作者。", Executor: "claude", Model: "default", Tools: slices.Clone(toolGrant), Revision: 1}
	reviewer := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "复核成员", Handle: "live-history-reviewer", RoleDescription: "合成独立复核成员。", Executor: "claude", Model: "default", Tools: slices.Clone(toolGrant), Revision: 1}
	coordinator := w.Agent{ID: w.NewID(), WorkspaceID: ws.ID, DisplayName: "协调成员", Handle: "live-history-coordinator", RoleDescription: "合成协调成员。", Executor: "claude", Model: "default", Tools: slices.Clone(toolGrant), Revision: 1}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "live delivered history acceptance", AgentIDs: []string{author.ID, reviewer.ID, coordinator.ID}, CoordinatorID: coordinator.ID, Revision: 1, UpdatedAt: w.Now()}

	// The summary is deliberately short and says it is not the report. The
	// counterexample sits past every 6000-byte preview, so no summary or
	// handoff preview can produce it.
	const counterexample = "REPORT_TAIL_COUNTEREXAMPLE_9C3F"
	reportBody := strings.Repeat("合成研究背景说明。", 600) + "\n## 正文反例与限制\n" + counterexample + "：只有报告正文才包含的尾部反例。\n"
	if len(reportBody) <= 6000 || strings.Index(reportBody, counterexample) <= 6000 {
		t.Fatalf("fixture counterexample is not beyond the preview budget: bytes=%d index=%d", len(reportBody), strings.Index(reportBody, counterexample))
	}
	priorID, priorStepID := w.NewID(), w.NewID()
	priorUser := w.Message{ID: w.NewID(), ThreadID: th.ID, RunID: priorID, StepID: priorStepID, AgentID: author.ID, AgentName: author.DisplayName, Role: "user", Content: "请研究这个合成问题并给出带限制的报告。", Status: "completed", CreatedAt: "2026-10-20T00:00:00Z"}
	summary := w.Message{ID: w.NewID(), ThreadID: th.ID, RunID: priorID, StepID: priorStepID, AgentID: author.ID, AgentName: author.DisplayName, Role: "assistant", Content: "研究已完成，报告已作为本轮交付产物发布；这只是发布摘要，不包含报告正文。", Status: "completed", CreatedAt: "2026-10-20T00:01:00Z"}
	report := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: th.ID, RunID: priorID, StepID: priorStepID, Type: "research_report", Title: "合成历史研究报告", Body: reportBody, Limitations: []string{"合成样本只覆盖一个文件，不能外推到其他目录。"}, CreatedAt: "2026-10-20T00:01:00Z"}
	prior := w.Run{ID: priorID, WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Agents: []w.Agent{author}, CoordinatorID: author.ID, Input: w.RunInput{Message: priorUser.Content, Workflow: "research", ResearchScope: "given_sources", Assignments: map[string]string{"researcher": author.ID, "critic": author.ID, "coordinator": author.ID}}, Status: "succeeded", CreatedAt: "2026-10-20T00:00:00Z", FinishedAt: "2026-10-20T00:02:00Z", Delivery: &w.Delivery{MessageIDs: []string{summary.ID}, ArtifactRefs: []w.ArtifactRef{{ID: report.ID, Version: 1}}}}

	message := "请另一位成员独立复核刚才那份研究报告的结论与限制，核对报告正文而不是只看发布摘要，并把正文里的关键反例回传给你。"
	in := w.RunInput{Message: message, Workflow: "free", Targets: []string{coordinator.ID}, ResearchScope: "given_sources"}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, Workspace: ws, ThreadID: th.ID, Agents: []w.Agent{reviewer, coordinator}, CoordinatorID: coordinator.ID, Input: in, Status: "queued", CreatedAt: w.Now(), Routing: w.Routing{Decision: "assign", Workflow: "free", Targets: []string{coordinator.ID}}, Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{}}}
	steps, err := Plan(run.ID, in, run.Agents)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, a := range []w.Agent{author, reviewer, coordinator} {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		if err := tx.InsertRun(prior, w.NewID(), "prior-"+priorID); err != nil {
			return err
		}
		if err := tx.SaveStep(w.Step{ID: priorStepID, RunID: priorID, AgentID: author.ID, AgentName: author.DisplayName, Role: "coordinator", Status: "succeeded"}); err != nil {
			return err
		}
		for _, m := range []w.Message{priorUser, summary} {
			if err := tx.SaveMessage(m); err != nil {
				return err
			}
		}
		if err := tx.SaveArtifact(report); err != nil {
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

	var finished w.Run
	for finished.ID == "" {
		var current w.Run
		wake := s.Changed()
		if err := s.Read(ctx, func(tx *store.Tx) error {
			var readErr error
			current, readErr = tx.Run(run.ID)
			return readErr
		}); err != nil {
			if ctx.Err() != nil {
				t.Fatalf("delivered-history acceptance did not settle: %v", ctx.Err())
			}
			t.Fatal(err)
		}
		if w.Terminal(current.Status) {
			finished = current
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("delivered-history acceptance did not settle before its deadline (status=%s)", current.Status)
		case <-wake:
		}
	}

	var loadedSteps []w.Step
	var loadedMessages []w.Message
	var loadedEvents []w.Event
	if err := s.Read(context.Background(), func(tx *store.Tx) error {
		var readErr error
		if loadedSteps, readErr = tx.Steps(run.ID); readErr != nil {
			return readErr
		}
		if loadedMessages, readErr = tx.Messages(th.ID); readErr != nil {
			return readErr
		}
		loadedEvents, readErr = tx.Events(run.ID, 0)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	logVisibleSteps(t, loadedSteps, loadedMessages)
	if finished.Status != "succeeded" {
		for _, step := range loadedSteps {
			t.Logf("history artifact step kind=%s role=%s status=%s failure_kind=%s error=%q", step.Kind, step.Role, step.Status, step.FailureKind, liveSafeError(step.Error))
		}
		t.Fatalf("delivered-history acceptance failed: kind=%s error=%q", finished.FailureKind, liveSafeError(finished.Error))
	}
	var reviewed *w.Step
	for i := range loadedSteps {
		if loadedSteps[i].Kind == "handoff" && loadedSteps[i].Role == "reviewer" {
			reviewed = &loadedSteps[i]
		}
	}
	exactRef := fmt.Sprintf("%s@%d", report.ID, report.Version)
	if reviewed == nil || reviewed.Status != "succeeded" {
		t.Fatalf("exact-artifact reviewer handoff missing: %+v", loadedSteps)
	}
	if !slices.Contains(reviewed.ContextRefs, exactRef) {
		t.Fatalf("reviewer handoff did not carry the exact delivered artifact %s: refs=%v", exactRef, reviewed.ContextRefs)
	}
	readExact := false
	for _, event := range loadedEvents {
		if event.Type != "tool_started" || event.StepID != reviewed.ID {
			continue
		}
		var data struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(event.Data, &data) != nil || data.Name != "read_context" {
			continue
		}
		var call struct {
			Kind    string `json:"kind"`
			ID      string `json:"id"`
			Version int    `json:"version"`
			Field   string `json:"field"`
		}
		if json.Unmarshal(data.Input, &call) != nil {
			continue
		}
		if call.Kind == "artifact" && call.ID == report.ID && call.Version == report.Version && call.Field == "artifact_json" {
			readExact = true
		}
	}
	if !readExact {
		t.Fatalf("reviewer never read the exact delivered artifact %s with field=artifact_json", exactRef)
	}
	if strings.Contains(summary.Content, counterexample) {
		t.Fatal("fixture summary unexpectedly contains the body-only counterexample")
	}
	reviewedQuote, relayedQuote := false, false
	for _, m := range loadedMessages {
		if m.RunID != run.ID || m.Status != "completed" || m.Role != "assistant" {
			continue
		}
		if reviewed != nil && m.StepID == reviewed.ID && strings.Contains(m.Content, counterexample) {
			reviewedQuote = true
		}
		for _, id := range finished.Delivery.MessageIDs {
			if m.ID == id && strings.Contains(m.Content, counterexample) {
				relayedQuote = true
			}
		}
	}
	if !reviewedQuote {
		t.Fatal("reviewer did not quote the report-body-only counterexample")
	}
	if !relayedQuote {
		t.Fatal("the delivered coordinator result did not relay the body-only counterexample")
	}
	t.Logf("delivered-history artifact accepted: report=%s refs=%v reviewer=%s", liveSafeError(exactRef), reviewed.ContextRefs, liveSafeError(reviewed.ID))
}
