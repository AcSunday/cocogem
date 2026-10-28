package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// Live acceptance is opt-in: it spawns the user's existing native CLI and may
// incur provider cost. It never changes keys, endpoints, providers or models.
const liveLongTaskTimeout = 12 * time.Minute

const (
	liveSourceLines = 32500
	liveWindowLines = 500
	liveWindows     = liveSourceLines / liveWindowLines

	// liveTailMarker is unique tail text that never appears in the task prompt,
	// so its presence proves the model read the captured file tail.
	liveTailMarker = "sample basis is unknown"
)

// liveEvidenceTokens are the exact tail facts a genuine analysis must preserve.
// They stay out of the prompt so a model cannot pass by copying the task text.
var liveEvidenceTokens = []string{"否定条件", "2026-10-27", "3.25 USD/kg", "冲突输入", "4.10 USD/kg", "证据不足"}

func liveSourceFile() string {
	lines := make([]string, liveSourceLines)
	for i := range lines {
		// 18-byte lines keep the 32,500-line file under tools.ReadFile's 1 MiB
		// budget while the final 500-line capture stays above the 8 KiB preview.
		lines[i] = fmt.Sprintf("%05d price sample", i+1)
	}
	lines[liveSourceLines-3] = "否定条件: 3.25 USD/kg only holds for the complete sample; otherwise the conclusion does not hold."
	lines[liveSourceLines-2] = "日期: 2026-10-27; 冲突输入: another source reports 4.10 USD/kg."
	lines[liveSourceLines-1] = "证据不足: sample basis is unknown; preserve this limitation."
	return strings.Join(lines, "\n")
}

func liveTaskPrompt() string {
	return fmt.Sprintf(`这是一次真实 native CLI 长任务验收，只使用临时合成资料，不访问网络、真实项目或私人文件。

资料 evidence.txt 共 %d 行。请按角色完成：
1. researcher：用 read_file 以 start_line=1,501,1001,... 每次 500 行的顺序读取全部资料，共 %d 个窗口、每个窗口一次独立工具调用。不要合并窗口、跳过或减少调用次数，也不要声称未实际读取的窗口。最后一个窗口（start_line=%d）的预览不包含文件尾部，必须对该窗口的来源用 read_source 分页读取到 has_more=false，取得尾部反证。发布 note，body 和 findings 必须原样保留资料尾部的否定条件、日期、两组价格与单位、冲突输入及证据不足，source_ids 使用含尾部反证的 captured source id，不能只写引用 ID。
2. critic：独立用 read_context 分页读取 researcher 已提交消息直到 has_more=false，并用 read_source 分页读取其引用的来源到尾部；发布 note，保留冲突输入与证据不足。
3. coordinator：用 read_context 分页读取 researcher 与 critic 的已提交消息和产物确切版本直到 has_more=false，并用 read_source 分页核对来源尾部；发布 research_report，body 与 findings 必须原样保留上述尾部否定条件、日期、两组价格与单位、冲突输入及证据不足，source_ids 指向真实捕获。

报告内容必须来自实际读取的资料，不要照抄本任务描述。工具调用次数和 source 捕获由平台持久记录并验收，模型自述不算数。不要调用 web_search/fetch_url，不要声称执行代码或在线验证。`, liveSourceLines, liveWindows, liveSourceLines-liveWindowLines+1)
}

// liveAttemptEvidence keeps only non-secret attempt metadata; raw provider
// errors and connection details are deliberately excluded.
type liveAttemptEvidence struct {
	StepRole      string `json:"step_role"`
	Number        int    `json:"number"`
	Status        string `json:"status"`
	FailureKind   string `json:"failure_kind,omitempty"`
	Executor      string `json:"executor,omitempty"`
	Model         string `json:"model,omitempty"`
	ResolvedModel string `json:"resolved_model,omitempty"`
	CLIVersion    string `json:"cli_version,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	SessionMode   string `json:"session_mode,omitempty"`
}

// liveRunEvidence is the sanitized summary logged and optionally exported.
type liveRunEvidence struct {
	CLI                 string                    `json:"cli"`
	CLIVersion          string                    `json:"cli_version,omitempty"`
	Adapter             string                    `json:"adapter,omitempty"`
	Isolation           string                    `json:"isolation,omitempty"`
	RunID               string                    `json:"run_id,omitempty"`
	RunStatus           string                    `json:"run_status,omitempty"`
	FailureKind         string                    `json:"failure_kind,omitempty"`
	Attempts            []liveAttemptEvidence     `json:"attempts,omitempty"`
	ToolCounts          map[string]map[string]int `json:"tool_counts,omitempty"`
	ReadFileWindows     int                       `json:"read_file_windows,omitempty"`
	ResearcherTools     int                       `json:"researcher_tools,omitempty"`
	ResearcherReads     int                       `json:"researcher_reads,omitempty"`
	ResearcherPublished bool                      `json:"researcher_published,omitempty"`
	ContextComplete     map[string]bool           `json:"context_complete,omitempty"`
	SourceTail          map[string]bool           `json:"source_tail,omitempty"`
}

func liveAttempts(roleByStep map[string]string, attempts []w.Attempt) []liveAttemptEvidence {
	out := make([]liveAttemptEvidence, 0, len(attempts))
	for _, attempt := range attempts {
		role := roleByStep[attempt.StepID]
		if role == "" {
			role = "unknown"
		}
		out = append(out, liveAttemptEvidence{StepRole: role, Number: attempt.Number, Status: attempt.Status, FailureKind: attempt.FailureKind, Executor: attempt.Executor, Model: attempt.Model, ResolvedModel: attempt.ResolvedModel, CLIVersion: attempt.CLIVersion, SessionID: attempt.SessionID, SessionMode: attempt.SessionMode})
	}
	return out
}

// writeLiveEvidence writes a 0600 sanitized summary to the root-owned path.
func writeLiveEvidence(path string, evidence liveRunEvidence) error {
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// TestLiveLongTaskEvidence exercises the real CLI only when explicitly enabled:
// COCOGEM_CHECK_LIVE_LONG_TASK=1 and optionally COCOGEM_LIVE_LONG_TASK_CLI.
func TestLiveLongTaskEvidence(t *testing.T) {
	if os.Getenv("COCOGEM_CHECK_LIVE_LONG_TASK") != "1" {
		t.Skip("set COCOGEM_CHECK_LIVE_LONG_TASK=1 to run the real native CLI long-task acceptance")
	}
	raw := os.Getenv("COCOGEM_LIVE_LONG_TASK_CLI")
	if strings.TrimSpace(raw) == "" {
		raw = "claude"
	}
	clis := []string{}
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || slices.Contains(clis, candidate) {
			continue
		}
		if !slices.Contains([]string{"claude", "codex", "gemini"}, candidate) {
			t.Fatalf("COCOGEM_LIVE_LONG_TASK_CLI must be claude, codex or gemini, got %q", candidate)
		}
		clis = append(clis, candidate)
	}
	if len(clis) == 0 {
		t.Fatal("no live CLI selected")
	}
	for _, cli := range clis {
		t.Run(cli, func(t *testing.T) {
			runLiveLongTask(t, cli)
		})
	}
}

func runLiveLongTask(t *testing.T, cli string) {
	ctx, cancel := context.WithTimeout(context.Background(), liveLongTaskTimeout)
	defer cancel()
	provider := nativecli.Provider{}
	adapter, err := provider.Check(ctx, cli)
	if err != nil {
		t.Fatalf("real CLI isolation check failed for %s: %v", cli, err)
	}
	if !adapter.Supported {
		t.Fatalf("real CLI %s is not supported: %s", cli, adapter.Reason)
	}
	t.Logf("live adapter: executor=%s version=%s adapter=%s isolation=%s identity=%s", adapter.ID, adapter.Version, adapter.Adapter, adapter.Isolation, adapter.Identity)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "evidence.txt"), []byte(liveSourceFile()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine := &workflow.Engine{Store: db, Model: provider, Runners: map[string]agent.Runner{cli: provider}, Adapters: provider, AllowedRoots: []string{dir}, AllowedModels: []string{"default"}, Web: tools.NewWeb("")}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	server := httptest.NewUnstartedServer(nil)
	app, err := New(Config{Host: server.Listener.Addr().String(), AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, Roots: []string{dir}, ModelConfigured: true, Model: provider, Executors: []ExecutorInfo{{ID: cli, Installed: true, Supported: true, Version: adapter.Version, Adapter: adapter.Adapter, Isolation: adapter.Isolation, Identity: adapter.Identity, Capabilities: adapter.Capabilities}}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = app.Handler()
	server.Start()
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar, client.Timeout = jar, 30*time.Second
	csrf := ""
	call := func(method, path string, input any, headers map[string]string, status int, target any) {
		t.Helper()
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatalf("%s %s: status=%d body=%s err=%v", method, path, resp.StatusCode, raw, err)
		}
		if target != nil && json.Unmarshal(raw, target) != nil {
			t.Fatalf("invalid API response: %s", raw)
		}
	}
	var session SessionReply
	call("POST", "/auth/setup", PasswordInput{Password: "live-long-task-password"}, nil, 200, &session)
	csrf = session.CSRFToken
	var ws w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "真实长任务验收", Root: dir}, nil, 200, &ws)
	var members Page[w.Agent]
	call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	if len(members.Items) != 3 {
		t.Fatalf("members=%+v", members.Items)
	}
	ids := []string{}
	for _, member := range members.Items {
		if member.Executor != cli {
			t.Fatalf("member %s did not use the selected CLI %s: %+v", member.DisplayName, cli, member)
		}
		ids = append(ids, member.ID)
	}
	var thread w.Thread
	call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "真实长任务证据复核", AgentIDs: ids, CoordinatorID: ids[2], Workflow: "research"}, nil, 200, &thread)
	input := w.RunInput{Message: liveTaskPrompt(), Workflow: "research", ResearchScope: "given_sources", Assignments: map[string]string{"researcher": ids[0], "critic": ids[1], "coordinator": ids[2]}}
	var preview RoutingPreview
	call("POST", "/api/v1/threads/"+thread.ID+"/routing-preview", input, nil, 200, &preview)
	if preview.ProposalID == "" {
		t.Fatalf("routing failed: %+v", preview)
	}
	input.ProposalID = preview.ProposalID
	var (
		run                 w.Run
		view                RunView
		researcherTools     int
		researcherReads     int
		researcherPublished bool
		contextComplete     = map[string]bool{}
		sourceTail          = map[string]bool{}
		readWindows         = map[int]bool{}
	)
	summary := liveRunEvidence{CLI: cli, CLIVersion: adapter.Version, Adapter: adapter.Adapter, Isolation: adapter.Isolation, ToolCounts: map[string]map[string]int{}}
	call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "live-long-task-evidence"}, 202, &run)
	// The summary runs even after t.Fatal so a failed real call leaves useful,
	// sanitized metadata (and an optional 0600 JSON file) behind.
	defer func() {
		if run.ID == "" {
			return
		}
		summary.RunID = run.ID
		summary.RunStatus = view.Run.Status
		summary.FailureKind = view.Run.FailureKind
		roles := map[string]string{}
		for _, step := range view.Steps {
			roles[step.ID] = step.Role
		}
		summary.Attempts = liveAttempts(roles, view.Attempts)
		summary.ReadFileWindows = len(readWindows)
		summary.ResearcherTools = researcherTools
		summary.ResearcherReads = researcherReads
		summary.ResearcherPublished = researcherPublished
		summary.ContextComplete = contextComplete
		summary.SourceTail = sourceTail
		raw, err := json.Marshal(summary)
		if err != nil {
			t.Logf("live evidence summary marshal failed: %v", err)
			return
		}
		t.Logf("live long task evidence summary: %s", raw)
		if path := os.Getenv("COCOGEM_LIVE_EVIDENCE_FILE"); path != "" {
			if err := writeLiveEvidence(path, summary); err != nil {
				t.Logf("live evidence file %s was not written: %v", path, err)
			} else {
				t.Logf("wrote sanitized live evidence to %s", path)
			}
		}
	}()
	for {
		wake := db.Changed()
		call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
		if w.Terminal(view.Run.Status) {
			break
		}
		select {
		case <-wake:
		case <-ctx.Done():
			t.Fatalf("live long task exceeded %s without a terminal state (run=%s status=%s)", liveLongTaskTimeout, run.ID, view.Run.Status)
		}
	}
	roleByStep := map[string]string{}
	for _, step := range view.Steps {
		roleByStep[step.ID] = step.Role
	}
	after := int64(0)
	expectedSeq := int64(0)
	for page := 0; page < 200; page++ {
		var events EventPage
		call("GET", fmt.Sprintf("/api/v1/runs/%s/event-history?after=%d&limit=500", run.ID, after), nil, nil, 200, &events)
		for _, event := range events.Items {
			expectedSeq++
			if event.Seq != expectedSeq {
				t.Fatalf("live event sequence gap: got %d want %d", event.Seq, expectedSeq)
			}
			role := roleByStep[event.StepID]
			if role == "" {
				role = "unknown"
			}
			switch event.Type {
			case "tool_finished":
				var data struct{ Name, Status, Output string }
				if err := json.Unmarshal(event.Data, &data); err != nil {
					t.Fatal(err)
				}
				counts := summary.ToolCounts[role]
				if counts == nil {
					counts = map[string]int{}
					summary.ToolCounts[role] = counts
				}
				counts[data.Name]++
				if role == "researcher" {
					researcherTools++
					if data.Name == "read_file" || data.Name == "read_source" {
						researcherReads++
					}
				}
				if data.Status != "ok" {
					continue
				}
				if role == "researcher" && data.Name == "read_file" {
					// The 500-line cap makes source_truncated true when the model
					// omits end_line; the returned window is still exact.
					var window struct {
						StartLine int `json:"start_line"`
						EndLine   int `json:"end_line"`
					}
					if json.Unmarshal([]byte(data.Output), &window) == nil && window.StartLine > 0 && window.EndLine == window.StartLine+liveWindowLines-1 {
						readWindows[window.StartLine] = true
					}
				}
				if data.Name == "read_context" {
					var page struct {
						Committed bool   `json:"committed"`
						HasMore   bool   `json:"has_more"`
						Content   string `json:"content"`
					}
					if json.Unmarshal([]byte(data.Output), &page) == nil && page.Committed && !page.HasMore {
						contextComplete[role] = true
					}
				}
				if data.Name == "read_source" {
					var page struct {
						Content string `json:"content"`
						HasMore bool   `json:"has_more"`
					}
					if json.Unmarshal([]byte(data.Output), &page) == nil && !page.HasMore && strings.Contains(page.Content, liveTailMarker) {
						sourceTail[role] = true
					}
				}
			case "artifact_published":
				if role == "researcher" {
					researcherPublished = true
				}
			}
			after = event.Seq
		}
		if events.NextCursor == "" {
			break
		}
	}
	if view.Run.Status != "succeeded" {
		t.Fatalf("live run failed: status=%s failure_kind=%s", view.Run.Status, view.Run.FailureKind)
	}
	for _, attempt := range view.Attempts {
		if attempt.Status != "succeeded" {
			t.Fatalf("attempt %s/%d did not succeed: %s %s", attempt.StepID, attempt.Number, attempt.Status, attempt.FailureKind)
		}
		if attempt.Executor != cli || attempt.CLIVersion == "" || attempt.CLIIdentity == "" || attempt.SessionID == "" || attempt.SessionMode != "fresh" {
			t.Fatalf("attempt identity metadata is incomplete: %+v", attempt)
		}
		t.Logf("run=%s step_role=%s attempt=%s executor=%s model=%s resolved=%s cli_version=%s cli_identity=%s session=%s failure=%q", run.ID, roleByStep[attempt.StepID], attempt.ID, attempt.Executor, attempt.Model, attempt.ResolvedModel, attempt.CLIVersion, attempt.CLIIdentity, attempt.SessionID, attempt.FailureKind)
	}
	var artifacts Page[w.Artifact]
	call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, &artifacts)
	var report *w.Artifact
	for i, artifact := range artifacts.Items {
		if artifact.Type == "research_report" {
			report = &artifacts.Items[i]
		}
	}
	if report == nil || len(report.SourceIDs) == 0 {
		t.Fatalf("live run published no sourced research report: %+v", artifacts.Items)
	}
	reportText, _ := json.Marshal(report)
	for _, token := range liveEvidenceTokens {
		if !bytes.Contains(reportText, []byte(token)) {
			t.Fatalf("live report lost required evidence token %q: %s", token, reportText)
		}
	}
	tailSource := false
	for _, id := range report.SourceIDs {
		var source w.Source
		call("GET", "/api/v1/sources/"+id, nil, nil, 200, &source)
		if source.Content != "" && source.SHA256 != "" && strings.Contains(source.Content, liveTailMarker) {
			tailSource = true
		}
	}
	if !tailSource {
		t.Fatal("report sources did not include the captured tail evidence")
	}
	if !researcherPublished {
		t.Fatal("researcher never published a note")
	}
	missingWindows := []int{}
	for start := 1; start <= liveSourceLines; start += liveWindowLines {
		if !readWindows[start] {
			missingWindows = append(missingWindows, start)
		}
	}
	if len(missingWindows) > 0 {
		t.Fatalf("researcher captured %d/%d independent read_file windows; missing start_line=%v", len(readWindows), liveWindows, missingWindows)
	}
	if researcherReads < liveWindows || researcherTools <= liveWindows {
		t.Fatalf("researcher reads=%d tools=%d; the live acceptance needs %d independent read_file windows plus tail evidence", researcherReads, researcherTools, liveWindows)
	}
	if !contextComplete["critic"] || !contextComplete["coordinator"] {
		t.Fatalf("critic/coordinator did not complete a paged read_context: %+v", contextComplete)
	}
	if !sourceTail["researcher"] || !sourceTail["critic"] || !sourceTail["coordinator"] {
		t.Fatalf("roles did not page a captured source to its tail: %+v", sourceTail)
	}
	if view.Run.Status != "succeeded" || view.Run.FailureKind != "" {
		t.Fatalf("live run metadata is not a clean success: status=%s failure=%s", view.Run.Status, view.Run.FailureKind)
	}
	t.Logf("live long task verified: cli=%s version=%s run=%s researcher_tools=%d researcher_reads=%d windows=%d critic_context=%v coordinator_context=%v researcher_tail=%v critic_tail=%v coordinator_tail=%v", cli, adapter.Version, run.ID, researcherTools, researcherReads, len(readWindows), contextComplete["critic"], contextComplete["coordinator"], sourceTail["researcher"], sourceTail["critic"], sourceTail["coordinator"])
}
