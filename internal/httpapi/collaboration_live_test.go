package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// This opt-in acceptance uses real configured model services and only synthetic text.
// It never reads user project files, adds credentials, or changes native CLI settings.
func TestLiveMixedCLICollaboration(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_COLLABORATION") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_COLLABORATION=1 to use configured native models")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 14*time.Minute)
	defer cancel()
	provider := nativecli.Provider{}
	executors := []ExecutorInfo{}
	available := []string{}
	runners := map[string]agent.Runner{}
	for _, info := range nativecli.Discover(ctx) {
		executors = append(executors, ExecutorInfo{ID: info.ID, Installed: info.Installed, Supported: info.Supported, Version: info.Version, Reason: info.Reason})
		t.Logf("CLI %s installed=%t supported=%t", info.ID, info.Installed, info.Supported)
		if info.Installed && info.Supported {
			available = append(available, info.ID)
			runners[info.ID] = provider
		} else {
			t.Logf("CLI %s support blocker: %s", info.ID, info.Reason)
		}
	}
	if len(available) < 2 {
		t.Fatal("mixed-CLI acceptance requires at least two supported installed CLIs")
	}
	choose := func(preferred, fallback string) string {
		if slices.Contains(available, preferred) {
			return preferred
		}
		return fallback
	}
	coordinatorCLI := choose("claude", available[0])
	researcherCLI := ""
	for _, id := range available {
		if id != coordinatorCLI {
			researcherCLI = id
			break
		}
	}
	if coordinatorCLI != "codex" {
		researcherCLI = choose("codex", researcherCLI)
	}
	reviewerCLI := choose("gemini", coordinatorCLI)
	if configured := os.Getenv("COCOGEM_LIVE_REVIEWER_CLI"); configured != "" {
		if !slices.Contains(available, configured) {
			t.Fatal("configured acceptance reviewer CLI is not installed and supported")
		}
		reviewerCLI = configured
		t.Logf("acceptance explicitly uses reviewer CLI %s; other installed CLIs are not provider-verified by this run", reviewerCLI)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("public acceptance"), 0600); err != nil {
		t.Fatal("create isolated frontend fixture")
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal("create isolated acceptance database")
	}
	defer db.Close()
	engine := &workflow.Engine{Store: db, Model: provider, Runners: runners, AllowedModels: []string{"default"}, Web: tools.NewWeb("")}
	if err := engine.Start(ctx); err != nil {
		t.Fatal("start isolated scheduler")
	}
	defer func() { cancel(); engine.Wait() }()
	server := httptest.NewUnstartedServer(nil)
	app, err := New(Config{Host: server.Listener.Addr().String(), AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, ModelConfigured: true, Model: provider, Executors: executors}, db, engine)
	if err != nil {
		t.Fatal("create isolated HTTP application")
	}
	server.Config.Handler = app.Handler()
	server.Start()
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("create local session jar")
	}
	client := server.Client()
	client.Jar, client.Timeout = jar, 30*time.Second
	csrf := ""
	call := func(method, path string, input any, headers map[string]string, want int, target any) {
		t.Helper()
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal("encode acceptance request")
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal("build acceptance request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("local acceptance HTTP %s failed", method)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil || resp.StatusCode != want {
			var failure struct {
				Error struct{ Code string }
			}
			_ = json.Unmarshal(raw, &failure)
			t.Fatalf("local acceptance HTTP %s status=%d expected=%d code=%s", method, resp.StatusCode, want, failure.Error.Code)
		}
		if target != nil {
			if err := json.Unmarshal(raw, target); err != nil {
				t.Fatal("decode acceptance HTTP response")
			}
		}
	}
	var session SessionReply
	call("POST", "/auth/setup", PasswordInput{Password: w.NewID()}, nil, http.StatusOK, &session)
	csrf = session.CSRFToken
	profiles := []w.Agent{
		{DisplayName: "调度成员", Handle: "acceptance-coordinator", RoleDescription: "根据运行时名册选择研究与独立复核成员，接收结果并发布最终 note。", Capabilities: []string{"fixture orchestration"}, Executor: coordinatorCLI, Model: "default", Tools: []string{"publish_artifact"}},
		{DisplayName: "研究成员", Handle: "acceptance-researcher", RoleDescription: "计算合成资料中的加法，先读取 task-handoff 技能，输出 RESEARCH_PUBLIC_SUM=42；本任务不再委派。", Capabilities: []string{"fixture arithmetic researcher"}, Executor: researcherCLI, Model: "default", Tools: []string{}},
		{DisplayName: "复核成员", Handle: "acceptance-reviewer", RoleDescription: "先读取 independent-review 技能，独立核对另一成员的已提交计算，输出 REVIEW_PUBLIC_SUM=42；本任务不再委派。", Capabilities: []string{"independent fixture reviewer"}, Executor: reviewerCLI, Model: "default", Tools: []string{}},
	}
	for i := range profiles {
		call("POST", "/api/v1/agents", profiles[i], nil, http.StatusOK, &profiles[i])
	}
	var workspace w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "Isolated public collaboration acceptance"}, nil, http.StatusOK, &workspace)
	ids := []string{profiles[0].ID, profiles[1].ID, profiles[2].ID}
	var thread w.Thread
	call("POST", "/api/v1/workspaces/"+workspace.ID+"/threads", w.Thread{Title: "Public arithmetic handoff acceptance", AgentIDs: ids, CoordinatorID: ids[0], Workflow: "free"}, nil, http.StatusOK, &thread)
	input := w.RunInput{Workflow: "free", Targets: []string{ids[0]}, ResearchScope: "given_sources", Message: liveCollaborationTask}
	var preview RoutingPreview
	call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, http.StatusOK, &preview)
	if preview.ProposalID == "" || preview.Routing.Decision == "clarify" || !slices.Equal(preview.Routing.Targets, []string{ids[0]}) {
		t.Fatal("explicit coordinator routing did not produce a dispatch proposal")
	}
	input.ProposalID = preview.ProposalID
	var run w.Run
	call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "live-public-collaboration-" + w.NewID()}, http.StatusAccepted, &run)
	t.Logf("real acceptance started: coordinator=%s researcher=%s reviewer=%s", coordinatorCLI, researcherCLI, reviewerCLI)

	// Read the actual browser SSE stream, including each persisted terminal event.
	streamClient := server.Client()
	streamClient.Jar, streamClient.Timeout = jar, 0
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/runs/"+run.ID+"/events", nil)
	if err != nil {
		t.Fatal("create acceptance event request")
	}
	response, err := streamClient.Do(req)
	if err != nil {
		t.Fatal("open acceptance event stream")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("acceptance SSE endpoint did not return an authenticated event stream")
	}
	events := []w.Event{}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event w.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal("invalid acceptance SSE data")
		}
		if len(events) > 0 && event.Seq != events[len(events)-1].Seq+1 {
			t.Fatal("SSE event sequence lost continuity")
		}
		events = append(events, event)
		if event.Type == "step_finished" {
			var step w.Step
			if err := json.Unmarshal(event.Data, &step); err != nil {
				t.Fatal("invalid terminal step event")
			}
			t.Logf("real step role=%s kind=%s status=%s error=%s", step.Role, step.Kind, step.Status, step.Error)
		}
		if event.Type == "run_finished" {
			break
		}
	}
	if scanner.Err() != nil {
		t.Fatal("acceptance event stream ended before terminal state")
	}
	var view RunView
	call("GET", "/api/v1/runs/"+run.ID, nil, nil, http.StatusOK, &view)
	if view.Run.Status != "succeeded" {
		t.Fatalf("real collaboration status=%s error=%s", view.Run.Status, view.Run.Error)
	}
	var conversation ThreadView
	call("GET", "/api/v1/threads/"+thread.ID+"?limit=100", nil, nil, http.StatusOK, &conversation)
	var artifacts Page[w.Artifact]
	call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, http.StatusOK, &artifacts)
	verifyLiveCollaboration(t, view, events, conversation.Messages.Items, artifacts.Items, profiles)
}

const liveCollaborationTask = `这是一次合成公开资料的多成员协作验收。资料只有：样本甲 17 单位，样本乙 25 单位。目标是核对合计 42；不访问本机项目、凭证、文件或外部网页，不执行命令。只使用本轮允许的 Cocogem 工具。
你是协调者，请根据运行时成员名册与能力标签选择队友，完成恰好两次顺序交接：
1. 先 read_skill 读取 task-orchestration 和 task-handoff。选择具备 fixture arithmetic researcher 能力的成员，通过 handoff_task 以 researcher 角色派发计算任务。要求该成员先读取 task-handoff，再独立计算 17+25，返回含 RESEARCH_PUBLIC_SUM=42 的简短结论，不再委派，不发布产物。提交后结束当前调用，让平台执行与回传。
2. 平台再次调用你并送回已提交研究结果后，读取 independent-review。选择具备 independent fixture reviewer 能力、且不是研究作者的成员，以 critic 角色派发独立复核，context_refs 必须包含平台实际给出的研究结论消息 ID。让复核者读取 independent-review，独立核对 17+25 与研究结论，返回含 REVIEW_PUBLIC_SUM=42 的结论，不再委派，不发布产物。提交后结束当前调用，让平台执行与回传。
3. 平台再次调用你并送回已提交复核结果后，读取 synthesize-results。确认两项任务均成功、复核者不是作者，然后调用 publish_artifact 发布且仅发布一份 type=note 的最终产物：标题为公开合成协作验收，正文含 ACCEPTANCE_PUBLIC_SUM=42、研究与复核的任务 ID、实际结果与局限；findings=[]、source_ids=[]，limitations 写明只有合成输入、没有外部资料或真实项目验证。完成最终回复并结束，不创建更多任务。
同一交接在工具校验被拒时修正参数并保持 request_id，不轮询等待、不用 @ 重复派发、不把 accepted 回执当成完成结果。已接收的下游任务如果执行失败，不重新派发、不更换成员重试；发布说明失败的 note 并结束。如果实际计算、工具或任务失败，如实报告，不伪造验收通过。`

func verifyLiveCollaboration(t *testing.T, view RunView, events []w.Event, messages []w.Message, artifacts []w.Artifact, profiles []w.Agent) {
	t.Helper()
	handoffs, continuations := []w.Step{}, 0
	for _, step := range view.Steps {
		if step.Status != "succeeded" {
			t.Errorf("unsettled/failed acceptance step kind=%s role=%s status=%s", step.Kind, step.Role, step.Status)
		}
		if step.Kind == "handoff" {
			handoffs = append(handoffs, step)
		}
		if step.Kind == "continuation" && step.AgentID == profiles[0].ID {
			continuations++
		}
	}
	if len(handoffs) != 2 || continuations != 2 || len(view.Steps) != 5 {
		t.Fatalf("real acceptance handoffs=%d coordinator_continuations=%d total_steps=%d", len(handoffs), continuations, len(view.Steps))
	}
	if handoffs[0].AgentID != profiles[1].ID || handoffs[0].Role != "researcher" || handoffs[1].AgentID != profiles[2].ID || handoffs[1].Role != "critic" {
		t.Fatal("dynamic selections did not match current capability roster")
	}
	research := w.Message{}
	for _, message := range messages {
		if message.StepID == handoffs[0].ID && message.Status == "completed" && strings.Contains(message.Content, "RESEARCH_PUBLIC_SUM=42") {
			research = message
		}
	}
	if research.ID == "" || research.AgentID == handoffs[1].AgentID || !slices.Contains(handoffs[1].ContextRefs, research.ID) {
		t.Fatal("independent review lacks the exact committed non-author research message")
	}
	if !slices.ContainsFunc(messages, func(message w.Message) bool {
		return message.StepID == handoffs[1].ID && message.Status == "completed" && strings.Contains(message.Content, "REVIEW_PUBLIC_SUM=42")
	}) {
		t.Fatal("reviewer did not return a completed independent result")
	}
	if len(artifacts) != 1 || artifacts[0].Type != "note" || !strings.Contains(artifacts[0].Body, "ACCEPTANCE_PUBLIC_SUM=42") || !strings.Contains(artifacts[0].Body, handoffs[0].ID) || !strings.Contains(artifacts[0].Body, handoffs[1].ID) {
		t.Fatal("final committed note does not reference both completed task IDs and acceptance result")
	}
	loaded := map[string]map[string]bool{}
	sessions, submitted := map[string]bool{}, 0
	for _, event := range events {
		switch event.Type {
		case "skill_read":
			var receipt struct {
				SkillID string `json:"skill_id"`
				Version string `json:"version"`
			}
			if err := json.Unmarshal(event.Data, &receipt); err != nil || receipt.Version == "" || receipt.Version != view.Run.SkillVersions[receipt.SkillID] {
				t.Fatal("skill-read receipt does not match immutable run version")
			}
			if loaded[event.AgentID] == nil {
				loaded[event.AgentID] = map[string]bool{}
			}
			loaded[event.AgentID][receipt.SkillID] = true
		case "native_session":
			var session struct{ Executor, Mode, CWD string }
			if err := json.Unmarshal(event.Data, &session); err != nil || session.Mode != "fresh" || session.CWD != "isolated" {
				t.Fatal("native collaboration did not use isolated fresh sessions")
			}
			sessions[session.Executor] = true
		case "task_submitted":
			submitted++
		}
	}
	for _, required := range []struct{ agent, skill string }{{profiles[0].ID, "task-orchestration"}, {profiles[0].ID, "task-handoff"}, {profiles[1].ID, "task-handoff"}, {profiles[2].ID, "independent-review"}, {profiles[0].ID, "synthesize-results"}} {
		if !loaded[required.agent][required.skill] {
			t.Errorf("missing actual skill read: %s", required.skill)
		}
	}
	if len(sessions) < 2 || submitted != 2 {
		t.Fatalf("native CLI families=%d durable submissions=%d", len(sessions), submitted)
	}
	t.Logf("real acceptance passed: CLI families=%d steps=%d handoffs=%d coordinator_continuations=%d skill_read_members=%d final_note=%s@%d", len(sessions), len(view.Steps), submitted, continuations, len(loaded), artifacts[0].ID, artifacts[0].Version)
}
