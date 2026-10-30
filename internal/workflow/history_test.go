package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func saveHistoryMessage(t *testing.T, s *store.Store, message w.Message) {
	t.Helper()
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveMessage(message) }); err != nil {
		t.Fatal(err)
	}
}

func priorCompletedRun(t *testing.T, s *store.Store, run w.Run, messages []w.Message) w.Run {
	t.Helper()
	prior := run
	prior.ID, prior.Status, prior.FinishedAt = w.NewID(), "succeeded", w.Now()
	prior.CreatedAt = "2026-10-01T00:00:00Z"
	prior.Delivery = nil
	step := w.Step{ID: w.NewID(), RunID: prior.ID, AgentID: run.Agents[0].ID, AgentName: run.Agents[0].DisplayName, Role: "responder", Status: "succeeded"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(prior, w.NewID(), "history-"+prior.ID); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		for i := range messages {
			messages[i].ThreadID, messages[i].RunID, messages[i].StepID = run.ThreadID, prior.ID, step.ID
			messages[i].AgentID, messages[i].Status = run.Agents[0].ID, "completed"
		}
		for _, message := range messages {
			if err := tx.SaveMessage(message); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return prior
}

func TestSharedHistoryAndCurrentRunInliningStayInsideFrozenScope(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/state.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	run.Input = w.RunInput{Workflow: "free", Message: "Check the historical finding", Targets: []string{run.Agents[0].ID}}
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
		t.Fatal(err)
	}
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		steps, readErr = tx.Steps(run.ID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	sender := steps[0]
	sender.Kind, sender.DependsOn, sender.Status = "continuation", []string{steps[1].ID}, "running"
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveStep(sender) }); err != nil {
		t.Fatal(err)
	}
	historyUser := w.Message{ID: w.NewID(), Role: "user", Content: "earlier synthetic question"}
	historyReply := w.Message{ID: w.NewID(), Role: "assistant", AgentName: run.Agents[0].DisplayName, Content: "earlier exact historical finding"}
	prior := priorCompletedRun(t, s, run, []w.Message{historyUser, historyReply})
	inScope := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[1].ID, AgentID: steps[1].AgentID, AgentName: steps[1].AgentName, Role: "assistant", Status: "completed", Content: "in-scope current-run reply"}
	outside := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[2].ID, AgentID: steps[2].AgentID, AgentName: steps[2].AgentName, Role: "assistant", Status: "completed", Content: "outside-scope sibling reply"}
	uncommitted := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[1].ID, AgentID: steps[1].AgentID, Role: "assistant", Status: "processing", Content: "uncommitted processing output"}
	for _, message := range []w.Message{inScope, outside, uncommitted} {
		saveHistoryMessage(t, s, message)
	}
	var all []w.Message
	var scope *readScope
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		if all, readErr = tx.MessagePreviews(run.ThreadID, 6000); readErr != nil {
			return readErr
		}
		selected := recentHistory(all, run.ID, 12, 16000)
		ids := make([]string, 0, len(selected))
		for _, message := range selected {
			ids = append(ids, message.ID)
		}
		scope, readErr = buildReadScope(tx, run, sender, ids, nil)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s}
	request, err := e.buildRequest(t.Context(), run, sender, run.Agents[0], nil, "", agent.ConnectionInfo{}, agent.AdapterInfo{}, w.Message{ID: w.NewID(), RunID: run.ID, StepID: sender.ID}, Handoff{}, scope, all, all, nil, nil, w.Attempt{})
	if err != nil {
		t.Fatal(err)
	}
	text := request.Messages[0].Content[0].Text
	for _, want := range []string{
		"Shared history message_id=" + historyReply.ID + " agent_id=" + run.Agents[0].ID + " step_id=" + priorStepID(t, s, prior.ID) + " run_id=" + prior.ID + " status=completed",
		"Committed current-run message message_id=" + inScope.ID,
		inScope.Content,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("request is missing %q", want)
		}
	}
	for _, absent := range []string{outside.Content, uncommitted.Content} {
		if strings.Contains(text, absent) {
			t.Fatalf("request inlined content outside the frozen scope: %q", absent)
		}
	}
	// The history ID advertised in the prompt is readable through the same
	// frozen scope, not just display text.
	if _, ok := scope.messages[historyReply.ID]; !ok {
		t.Fatal("shared history message is not readable through read_context")
	}
}

func TestDeliveredHistoryArtifactExactVersionIsReadableAndHandedOff(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/state.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	run.Input = w.RunInput{Workflow: "free", Message: "请独立复核刚才那份研究报告", Targets: []string{run.Agents[0].ID}}
	run.Status = "running"
	var sender w.Step
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		sender = steps[0]
		sender.Status = "running"
		return tx.SaveStep(sender)
	}); err != nil {
		t.Fatal(err)
	}
	author := run.Agents[0]
	reviewer := run.Agents[1]

	const tail = "REPORT_TAIL_COUNTEREXAMPLE_5A1C"
	const newerToken = "NEVER_DELIVERED_NEW_VERSION_TOKEN"
	const undeliveredToken = "UNDELIVERED_ARTIFACT_TOKEN"
	const legacyToken = "LEGACY_WITHOUT_DELIVERY_TOKEN"
	priorID, priorStepID := w.NewID(), w.NewID()
	priorUser := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: priorID, StepID: priorStepID, AgentID: author.ID, AgentName: author.DisplayName, Role: "user", Content: "合成历史研究问题", Status: "completed"}
	summary := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: priorID, StepID: priorStepID, AgentID: author.ID, AgentName: author.DisplayName, Role: "assistant", Content: "研究已完成，报告已发布；这里只是发布摘要，不包含报告正文。", Status: "completed"}
	reportBody := strings.Repeat("合成研究背景。", 600) + "\n## 反例与限制\n" + tail
	report := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: priorID, StepID: priorStepID, Type: "research_report", Title: "合成历史研究报告", Body: reportBody, Limitations: []string{"合成样本限制。"}}
	newer := report
	newer.Version, newer.Revision, newer.Body = 2, 2, newerToken
	undelivered := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: priorID, StepID: priorStepID, Type: "note", Title: "未交付草稿", Body: undeliveredToken}
	prior := run
	prior.ID, prior.Status, prior.CreatedAt, prior.FinishedAt = priorID, "succeeded", "2026-10-20T00:00:00Z", "2026-10-20T00:02:00Z"
	prior.Delivery = &w.Delivery{MessageIDs: []string{summary.ID}, ArtifactRefs: []w.ArtifactRef{{ID: report.ID, Version: 1}}}

	legacyID, legacyStepID := w.NewID(), w.NewID()
	legacyReply := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: legacyID, StepID: legacyStepID, AgentID: author.ID, AgentName: author.DisplayName, Role: "assistant", Content: "旧历史没有交付记录的回复", Status: "completed"}
	legacy := prior
	legacy.ID, legacy.Status, legacy.CreatedAt, legacy.FinishedAt = legacyID, "succeeded", "2026-10-18T00:00:00Z", "2026-10-18T00:02:00Z"
	legacy.Delivery = nil
	legacyArtifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: legacyID, StepID: legacyStepID, Type: "research_report", Title: "旧历史报告", Body: legacyToken}

	otherThread := w.Thread{ID: w.NewID(), WorkspaceID: run.WorkspaceID, Revision: 1, UpdatedAt: w.Now()}
	otherRunID := w.NewID()
	otherArtifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: otherThread.ID, RunID: otherRunID, StepID: w.NewID(), Type: "research_report", Title: "其他会话报告", Body: "OTHER_THREAD_TOKEN"}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(legacy, w.NewID(), "legacy-"+legacyID); err != nil {
			return err
		}
		if err := tx.InsertRun(prior, w.NewID(), "prior-"+priorID); err != nil {
			return err
		}
		if err := tx.SaveMessage(legacyReply); err != nil {
			return err
		}
		for _, message := range []w.Message{priorUser, summary} {
			if err := tx.SaveMessage(message); err != nil {
				return err
			}
		}
		for _, artifact := range []w.Artifact{report, newer, undelivered, legacyArtifact} {
			if err := tx.SaveArtifact(artifact); err != nil {
				return err
			}
		}
		if err := tx.SaveThread(otherThread); err != nil {
			return err
		}
		if err := tx.InsertRun(w.Run{ID: otherRunID, WorkspaceID: run.WorkspaceID, ThreadID: otherThread.ID, Status: "succeeded", CreatedAt: "2026-10-19T00:00:00Z", Delivery: &w.Delivery{MessageIDs: []string{}, ArtifactRefs: []w.ArtifactRef{{ID: otherArtifact.ID, Version: 1}}}}, w.NewID(), "other-"+otherRunID); err != nil {
			return err
		}
		return tx.SaveArtifact(otherArtifact)
	}); err != nil {
		t.Fatal(err)
	}

	e := Engine{Store: s, AllowedModels: []string{"test"}}
	var all []w.Message
	var scope *readScope
	var planDigest string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		if all, readErr = tx.MessagePreviews(run.ThreadID, 6000); readErr != nil {
			return readErr
		}
		selected := recentHistory(all, run.ID, 12, 16000)
		ids := make([]string, 0, len(selected))
		for _, message := range selected {
			ids = append(ids, message.ID)
		}
		if scope, readErr = buildReadScope(tx, run, sender, ids, nil); readErr != nil {
			return readErr
		}
		planDigest, readErr = e.planDigestFor(tx, run, sender, agent.AdapterInfo{}, agent.ConnectionInfo{}, nil)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if versions := scope.artifacts[report.ID]; len(versions) != 1 || versions[0].version != 1 {
		t.Fatalf("delivered artifact versions = %+v", versions)
	}
	for _, id := range []string{undelivered.ID, legacyArtifact.ID, otherArtifact.ID} {
		if len(scope.artifacts[id]) != 0 {
			t.Fatalf("artifact %s outside the selected delivery entered the frozen scope", id)
		}
	}
	var expectedDigest string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		expectedDigest, readErr = inputPlanDigest(tx, sender, run, author.Model, agent.ConnectionInfo{}, agent.AdapterInfo{}, scope, e.allowedTools(run, author, sender), nil)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	if planDigest != expectedDigest {
		t.Fatal("planDigestFor selected a different frozen scope than the dispatch snapshot")
	}

	request, err := e.buildRequest(t.Context(), run, sender, author, nil, "", agent.ConnectionInfo{}, agent.AdapterInfo{}, w.Message{ID: w.NewID(), RunID: run.ID, StepID: sender.ID}, Handoff{}, scope, all, all, nil, nil, w.Attempt{})
	if err != nil {
		t.Fatal(err)
	}
	text := request.Messages[0].Content[0].Text
	for _, want := range []string{report.ID + "@1", "type=research_report", `title="合成历史研究报告"`, "field=artifact_json"} {
		if !strings.Contains(text, want) {
			t.Fatalf("request does not expose the exact delivered artifact metadata %q", want)
		}
	}
	for _, absent := range []string{tail, newerToken, undeliveredToken, legacyToken, "OTHER_THREAD_TOKEN"} {
		if strings.Contains(text, absent) {
			t.Fatalf("request exposed artifact content outside the exact delivered version: %q", absent)
		}
	}

	readAll := func(step w.Step, frozen *readScope, id string, version int) (string, error) {
		var content strings.Builder
		cursor := ""
		for range 16 {
			raw, _ := json.Marshal(map[string]any{"kind": "artifact", "id": id, "version": version, "field": "artifact_json", "cursor": cursor})
			result, err := e.readContext(t.Context(), run, step, frozen, raw)
			if err != nil {
				return "", err
			}
			var page struct {
				Content    string `json:"content"`
				NextCursor string `json:"next_cursor"`
				HasMore    bool   `json:"has_more"`
			}
			if err := json.Unmarshal([]byte(result), &page); err != nil {
				return "", err
			}
			content.WriteString(page.Content)
			if !page.HasMore {
				return content.String(), nil
			}
			if page.NextCursor == "" {
				return "", fmt.Errorf("artifact page has no next cursor")
			}
			cursor = page.NextCursor
		}
		return "", fmt.Errorf("artifact pagination did not terminate")
	}
	body, err := readAll(sender, scope, report.ID, 1)
	if err != nil {
		t.Fatalf("the root cannot page the exact delivered artifact: %v", err)
	}
	if !strings.Contains(body, tail) {
		t.Fatal("the exact delivered artifact page omitted its tail")
	}
	for _, candidate := range []struct {
		id      string
		version int
	}{{report.ID, 2}, {undelivered.ID, 1}, {legacyArtifact.ID, 1}, {otherArtifact.ID, 1}} {
		raw, _ := json.Marshal(map[string]any{"kind": "artifact", "id": candidate.id, "version": candidate.version})
		if _, err := e.readContext(t.Context(), run, sender, scope, raw); err == nil {
			t.Fatalf("artifact %s@%d should stay unreadable", candidate.id, candidate.version)
		}
	}

	if _, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, handoffInput{
		RequestID:     "review-delivered-report",
		TargetAgentID: reviewer.ID,
		Task:          "独立复核这份研究报告的结论与限制",
		Role:          "reviewer",
		ContextRefs:   []string{summary.ID, report.ID + "@1"},
	})); err != nil {
		t.Fatalf("handoff with the exact delivered artifact was rejected: %v", err)
	}
	var child w.Step
	var childScope *readScope
	var prepared Handoff
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(steps, func(step w.Step) bool { return step.Kind == "handoff" })
		if index < 0 {
			return fmt.Errorf("handoff step missing")
		}
		child = steps[index]
		previews, err := tx.MessagePreviews(run.ThreadID, 6000)
		if err != nil {
			return err
		}
		selected := recentHistory(previews, run.ID, 12, 16000)
		ids := make([]string, 0, len(selected))
		for _, message := range selected {
			ids = append(ids, message.ID)
		}
		childScope, err = buildReadScope(tx, run, child, ids, nil)
		if err != nil {
			return err
		}
		prepared = prepareHandoff(child, run.Input.Message, previews, nil, nil)
		if err := addReferencedInputs(tx, child, run.ThreadID, &prepared); err != nil {
			return err
		}
		return sealHandoff(tx, &prepared)
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(prepared.ArtifactRefs, []w.ArtifactRef{{ID: report.ID, Version: 1}}) {
		t.Fatalf("reviewer handoff artifact refs = %+v", prepared.ArtifactRefs)
	}
	if versions := childScope.artifacts[report.ID]; len(versions) != 1 || versions[0].version != 1 {
		t.Fatalf("reviewer frozen versions = %+v", versions)
	}
	reviewed, err := readAll(child, childScope, report.ID, 1)
	if err != nil || !strings.Contains(reviewed, tail) {
		t.Fatalf("the reviewer cannot page the exact delivered artifact: %v", err)
	}
	if _, err := readAll(child, childScope, report.ID, 2); err == nil {
		t.Fatal("the reviewer could read a newer undelivered version")
	}
}

func priorStepID(t *testing.T, s *store.Store, runID string) string {
	t.Helper()
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		steps, err = tx.Steps(runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("prior run steps = %d", len(steps))
	}
	return steps[0].ID
}

// A newest completed round that exceeds the history byte budget must remain
// readable: real IDs and authors stay in the prompt, the body preview says it
// was cut, and read_context can page the exact committed reply.
func TestOverBudgetSharedHistoryStaysVisibleAndReadable(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/state.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	run.Input = w.RunInput{Workflow: "free", Message: "Review the latest research report", Targets: []string{run.Agents[0].ID}}
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
		t.Fatal(err)
	}
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		steps, readErr = tx.Steps(run.ID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	sender := steps[0]
	history := []w.Message{{ID: w.NewID(), Role: "user", Content: "research question"}}
	finalReply := w.NewID()
	for _, id := range []string{w.NewID(), w.NewID(), w.NewID(), w.NewID(), finalReply} {
		history = append(history, w.Message{ID: id, Role: "assistant", AgentName: run.Agents[0].DisplayName, Content: strings.Repeat("e", 4000)})
	}
	priorCompletedRun(t, s, run, history)
	var all []w.Message
	var scope *readScope
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		if all, readErr = tx.MessagePreviews(run.ThreadID, 6000); readErr != nil {
			return readErr
		}
		selected := recentHistory(all, run.ID, 12, 16000)
		ids := make([]string, 0, len(selected))
		for _, message := range selected {
			ids = append(ids, message.ID)
		}
		scope, readErr = buildReadScope(tx, run, sender, ids, nil)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s, AllowedModels: []string{"test"}}
	request, err := e.buildRequest(t.Context(), run, sender, run.Agents[0], nil, "", agent.ConnectionInfo{}, agent.AdapterInfo{}, w.Message{ID: w.NewID(), RunID: run.ID, StepID: sender.ID}, Handoff{}, scope, all, all, nil, nil, w.Attempt{})
	if err != nil {
		t.Fatal(err)
	}
	text := request.Messages[0].Content[0].Text
	for _, want := range []string{
		"Shared history message_id=" + finalReply,
		"preview truncated: content_bytes=4000; read_context kind=message id=" + finalReply,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("over-budget history is missing %q", want)
		}
	}
	raw, _ := json.Marshal(map[string]any{"kind": "message", "id": finalReply})
	result, err := e.readContext(t.Context(), run, sender, scope, raw)
	if err != nil {
		t.Fatalf("the advertised final reply is not readable: %v", err)
	}
	var page struct {
		Content   string `json:"content"`
		Committed bool   `json:"committed"`
		HasMore   bool   `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(result), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Committed || page.HasMore || page.Content != strings.Repeat("e", 4000) {
		t.Fatalf("read_context did not return the exact final reply: committed=%t has_more=%t bytes=%d", page.Committed, page.HasMore, len(page.Content))
	}
}

// The dispatch digest and the resume re-derivation must select the same
// over-budget history window; otherwise a context-fresh replay guard would
// wrongly allow a mechanical retry.
func TestOverBudgetHistoryKeepsResumePlanDigestStable(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/state.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "answer"}})
	history := []w.Message{{ID: w.NewID(), Role: "user", Content: "research question"}}
	for i := 0; i < 5; i++ {
		history = append(history, w.Message{ID: w.NewID(), Role: "assistant", Content: strings.Repeat(string(rune('a'+i)), 4000)})
	}
	priorCompletedRun(t, s, run, history)
	runner := &testRunner{run: func(context.Context, model.Request, int, agent.Execute, agent.Observe) error {
		return &nativecli.Failure{Kind: nativecli.FailureContextOverflow, Reason: "context window exhausted"}
	}}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	if got := awaitRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("first overflow did not interrupt: %+v", got)
	}
	cancel()
	wait()
	// The first fresh attempt is allowed; the guard only blocks replaying a
	// fresh attempt whose inputs did not change.
	second := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancelSecond, waitSecond := startEngine(t, &second)
	defer waitSecond()
	if _, err := second.Resume(t.Context(), run.ID); err != nil {
		t.Fatalf("the first context-fresh attempt was refused: %v", err)
	}
	if got := awaitRun(t, s, run.ID); got.Status != "interrupted" {
		t.Fatalf("fresh attempt did not overflow: %+v", got)
	}
	attempts := loadAttempts(t, s, run.ID)
	if len(attempts) != 2 || attempts[1].Recovery != "context_fresh" || attempts[1].InputPlanDigest == "" {
		t.Fatalf("fresh attempt identity missing: %+v", attempts)
	}
	if _, err := second.Resume(t.Context(), run.ID); err == nil || !strings.Contains(err.Error(), "context_overflow_replay_blocked") {
		t.Fatalf("over-budget history changed the resume digest: %v", err)
	}
	cancelSecond()
}

func TestFollowUpDiagnosticsExposeRecentRunStateButNotRawInput(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/state.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	run.Input = w.RunInput{Workflow: "free", Message: "What failed last time?", Targets: []string{run.Agents[0].ID}}
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
		t.Fatal(err)
	}
	oldest := ""
	for i := 0; i < 4; i++ {
		failed := run
		failed.ID = w.NewID()
		failed.Status = "failed"
		failed.FailureKind = "tool_unavailable"
		failed.Error = fmt.Sprintf("failure-%d real error: No such tool available", i)
		failed.CreatedAt = fmt.Sprintf("2026-10-0%dT00:00:00Z", i+1)
		failed.Delivery = nil
		step := w.Step{ID: w.NewID(), RunID: failed.ID, AgentID: run.Agents[0].ID, AgentName: run.Agents[0].DisplayName, Role: "researcher", Status: "failed", FailureKind: "tool_unavailable", Error: "native CLI requested an unapproved native tool: bash\nraw stack line"}
		if err := s.Write(t.Context(), func(tx *store.Tx) error {
			if err := tx.InsertRun(failed, w.NewID(), "failed-"+failed.ID); err != nil {
				return err
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			return tx.AppendEvent(failed.ID, step.ID, step.AgentID, "tool_finished", map[string]any{"name": "bash", "input": "SECRET_RAW_TOOL_INPUT"})
		}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = failed.ID
		}
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		completed := run
		completed.ID, completed.Status, completed.CreatedAt, completed.Delivery = w.NewID(), "succeeded", "2026-10-09T00:00:00Z", nil
		return tx.InsertRun(completed, w.NewID(), "succeeded")
	}); err != nil {
		t.Fatal(err)
	}
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		steps, readErr = tx.Steps(run.ID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	e := Engine{Store: s}
	text, err := e.collaborationContext(t.Context(), run, steps[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"run_id":"`, `"step_id":"`, `"agent_id":"`, `"role":"researcher"`, `"status":"failed"`, `"failure_kind":"tool_unavailable"`, "real error: No such tool available", "not committed evidence"} {
		if !strings.Contains(text, want) {
			t.Fatalf("follow-up context is missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, oldest) {
		t.Fatal("more than three recent failed runs were exposed")
	}
	if strings.Contains(text, "SECRET_RAW_TOOL_INPUT") || strings.Contains(text, "bash\nraw stack line") {
		t.Fatal("diagnostic context exposed raw tool input or multiline stderr")
	}
	if !strings.Contains(text, "bash raw stack line") {
		t.Fatal("diagnostic context lost the bounded host error")
	}
	if strings.Count(text, `"run_id":"`) != 3 {
		t.Fatalf("diagnostics are not bounded to three runs: %s", text)
	}
}

func TestHandoffRejectsShortAndUncommittedReferencesWithCorrectableError(t *testing.T) {
	e, run, sender := collaborationFixture(t)
	m, _, _ := collaborationEvidence(t, e.Store, run, run.Agents[2].ID)
	failed := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: m.StepID, AgentID: run.Agents[2].ID, Role: "assistant", Status: "failed", Content: "uncommitted failure output"}
	processing := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: m.StepID, AgentID: run.Agents[2].ID, Role: "assistant", Status: "processing", Content: "in-flight output"}
	collaborationWrite(t, e.Store, func(tx *store.Tx) error {
		if err := tx.SaveMessage(failed); err != nil {
			return err
		}
		return tx.SaveMessage(processing)
	})
	before := len(collaborationSteps(t, e.Store, run.ID))
	for _, ref := range []string{m.ID[:8], m.StepID, failed.ID, processing.ID} {
		in := handoffInput{RequestID: "bad-ref", TargetAgentID: run.Agents[1].ID, Task: "Review this", Role: "reviewer", ContextRefs: []string{ref}}
		result, err := e.submitHandoff(t.Context(), run, sender, collaborationJSON(t, in))
		if err == nil || result != "" {
			t.Fatalf("reference %q was accepted", ref)
		}
		for _, want := range []string{ref, "complete message_id", "Shared history", "get_task"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error for %q is not correctable (%q): %v", ref, want, err)
			}
		}
	}
	if len(collaborationSteps(t, e.Store, run.ID)) != before {
		t.Fatal("rejected references changed durable state")
	}
}

func TestSharedHistoryMessageFlowsToReviewerThroughHandoffAndReadContext(t *testing.T) {
	s, err := store.Open(t.TempDir() + "/state.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedRun(t, s)
	run.Input = w.RunInput{Workflow: "free", Message: "Have the exact earlier finding reviewed", Targets: []string{run.Agents[0].ID}}
	run.Status = "running"
	for i := range run.Agents {
		run.Agents[i].Executor, run.Agents[i].Model = "claude", "default"
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		for _, a := range run.Agents {
			if err := tx.SaveAgent(a); err != nil {
				return err
			}
		}
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	historyUser := w.Message{ID: w.NewID(), Role: "user", Content: "synthetic historical question"}
	historyReply := w.Message{ID: w.NewID(), Role: "assistant", AgentName: run.Agents[0].DisplayName, Content: "exact-historical-finding-token"}
	priorCompletedRun(t, s, run, []w.Message{historyUser, historyReply})
	var steps []w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		steps, readErr = tx.Steps(run.ID)
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	sender := steps[0]
	sender.Status = "running"
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveStep(sender) }); err != nil {
		t.Fatal(err)
	}
	var submitted bool
	senderRunner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		text := request.Messages[0].Content[0].Text
		line := "Shared history message_id=" + historyReply.ID
		if !strings.Contains(text, line) {
			return fmt.Errorf("sender never saw the exact shared-history ID: %s", text)
		}
		raw, _ := json.Marshal(map[string]any{"request_id": "history-review", "target_agent_id": run.Agents[1].ID, "task": "Review the exact historical finding", "role": "reviewer", "context_refs": []string{historyReply.ID}})
		if _, err := execute(ctx, "handoff_task", raw); err != nil {
			return err
		}
		submitted = true
		return observe("text", map[string]any{"text": "submitted"})
	})
	previews := func() []w.Message {
		var messages []w.Message
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var readErr error
			messages, readErr = tx.MessagePreviews(run.ThreadID, 6000)
			return readErr
		}); err != nil {
			t.Fatal(err)
		}
		return messages
	}
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": senderRunner}, Web: testutil.Web{}}
	got := e.executeStep(t.Context(), run, sender, previews(), previews(), nil)
	if got.Status != "succeeded" || !submitted {
		t.Fatalf("sender step = %+v submitted=%v", got, submitted)
	}
	var child w.Step
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var readErr error
		steps, readErr = tx.Steps(run.ID)
		if readErr != nil {
			return readErr
		}
		for _, step := range steps {
			if step.Kind == "handoff" {
				child = step
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(child.ContextRefs, []string{historyReply.ID}) {
		t.Fatalf("child refs = %v, want the exact full message ID %s", child.ContextRefs, historyReply.ID)
	}
	childRunner := nativeRunnerFunc(func(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
		raw, _ := json.Marshal(map[string]any{"kind": "message", "id": historyReply.ID})
		result, err := execute(ctx, "read_context", raw)
		if err != nil {
			return err
		}
		var page struct {
			Content   string `json:"content"`
			Committed bool   `json:"committed"`
		}
		if err := json.Unmarshal([]byte(result), &page); err != nil {
			return err
		}
		if !page.Committed || page.Content != historyReply.Content {
			return fmt.Errorf("read_context returned %q committed=%v", page.Content, page.Committed)
		}
		return observe("text", map[string]any{"text": "exact historical finding reviewed"})
	})
	e.Runners["claude"] = childRunner
	child.Status = "pending"
	got = e.executeStep(t.Context(), run, child, previews(), previews(), nil)
	if got.Status != "succeeded" {
		t.Fatalf("reviewer step = %+v", got)
	}
}
