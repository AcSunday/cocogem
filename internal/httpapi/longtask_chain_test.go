package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// longTaskBody is streamed by the fixture CLI as one visible frame. It stays
// well under the 4 MiB frame limit while exceeding every old cumulative text
// and preview bound.
func longTaskBody() string {
	return longTaskStart + "\n" + strings.Repeat("合成长文证据保留原始来源与否定条件。", 5000) + "\n" + longTaskEvidence
}

const (
	longTaskStart      = "长任务正文起始标记：以下为合成验收证据。"
	longTaskEvidence   = "末页反证：否定条件——若只使用单一来源，则 3.25 USD/kg 为平均价格的结论不成立；日期 2026-10-27；冲突输入：另一来源报告 4.10 USD/kg；证据不足：无法确认样本口径。"
	longTaskSourceTail = "来源末页反证：否定条件——3.25 USD/kg 不是无条件均价；日期 2026-10-27；冲突输入：4.10 USD/kg；证据不足：样本口径未知。"
)

func longTaskSourceFile() string {
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = fmt.Sprintf("合成来源第 %03d 行：价格与口径必须和日期一起核对。", i+1)
	}
	lines[len(lines)-2] = longTaskSourceTail
	lines[len(lines)-1] = "文件结束：以上反证必须进入最终产物。"
	return strings.Join(lines, "\n")
}

// longTaskSourceCapture mirrors tools.ReadFile's exact line-numbered capture.
func longTaskSourceCapture() string {
	lines := strings.Split(longTaskSourceFile(), "\n")
	var capture strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&capture, "%d: %s\n", i+1, line)
	}
	return capture.String()
}

type longTaskSourcePage struct {
	ID               string `json:"id"`
	SourceID         string `json:"source_id"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	Path             string `json:"path"`
	SHA256           string `json:"sha256"`
	Content          string `json:"content"`
	Offset           int    `json:"offset"`
	TotalBytes       int    `json:"total_bytes"`
	HasMore          bool   `json:"has_more"`
	NextCursor       string `json:"next_cursor"`
	SourceTruncated  bool   `json:"source_truncated"`
	PreviewTruncated bool   `json:"preview_truncated"`
}

type longTaskContextPage struct {
	Kind          string   `json:"kind"`
	ID            string   `json:"id"`
	Version       int      `json:"version"`
	Content       string   `json:"content"`
	TotalBytes    int      `json:"total_bytes"`
	HasMore       bool     `json:"has_more"`
	NextCursor    string   `json:"next_cursor"`
	Committed     bool     `json:"committed"`
	ContentFormat string   `json:"content_format"`
	SourceIDs     []string `json:"source_ids"`
	StepID        string   `json:"step_id"`
}

// TestLongTaskHelperProcess is the PATH shim. It is the test binary, never an
// installed CLI or a model service.
// longTaskModel scripts a host-driven long task: it pages the captured source,
// performs more than one hundred tool calls, streams a long body and publishes
// one report. It is deterministic and does not impersonate a provider.
type longTaskModel struct {
	step  int
	reads int
	calls int
}

func (m *longTaskModel) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	tool := func(name string, input any) model.Response {
		raw, _ := json.Marshal(input)
		// Each call needs a unique identity within the attempt.
		m.calls++
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: fmt.Sprintf("long-%s-%d", name, m.calls), Name: name, Input: raw}}}
	}
	allowed := func(name string) bool {
		for _, def := range request.Tools {
			if def.Name == name {
				return true
			}
		}
		return false
	}
	switch m.step {
	case 0:
		m.step++
		if !allowed("read_file") {
			return model.Response{}, errors.New("read_file was not offered")
		}
		return tool("read_file", map[string]any{"path": "evidence.md"}), nil
	case 1:
		// One hundred same-parameter reads: the host must never charge a
		// cumulative tool-call budget against a long but legitimate task.
		m.reads++
		if m.reads <= 100 {
			return tool("read_source", map[string]any{"source_id": longTaskSourceID(request)}), nil
		}
		m.step++
		payload := map[string]any{
			"type":  "research_report",
			"title": "长任务研究报告",
			"body":  "# 长任务研究报告\n\n## 范围\n合成资料 evidence.md。\n\n## 方法\n多次分页读取来源并核对尾部反证。\n\n## 发现\n" + longTaskEvidence + "\n\n## 分歧与限制\n冲突输入与否定条件保留如下：" + longTaskEvidence + "\n",
			"findings": []any{
				map[string]any{"title": "尾部反证已进入结论", "detail": longTaskEvidence, "status": "supported", "source_ids": []string{longTaskSourceID(request)}},
				map[string]any{"title": "冲突来源与证据不足", "detail": "另一来源报告 4.10 USD/kg；3.25 USD/kg 的口径无法确认，证据不足。", "status": "insufficient_evidence", "source_ids": []any{}},
			},
			"source_ids":  []string{longTaskSourceID(request)},
			"limitations": []string{"合成测试资料；真实 provider 质量另行验证。"},
		}
		if !allowed("publish_artifact") {
			return model.Response{}, errors.New("publish_artifact was not offered")
		}
		return tool("publish_artifact", payload), nil
	}
	text := longTaskBody()
	// Stream the exact body as bounded frames; the host assembles them into one
	// chunked message rather than a single oversized frame.
	for len(text) > 0 {
		size := min(4000, len(text))
		for size < len(text) && !utf8.RuneStart(text[size]) {
			size--
		}
		if size == 0 {
			size = min(4000, len(text))
			for size < len(text) && !utf8.RuneStart(text[size]) {
				size++
			}
		}
		if err := emit(text[:size]); err != nil {
			return model.Response{}, err
		}
		text = text[size:]
	}
	return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: longTaskBody()}}}, nil
}

// longTaskSourceID reads the source ID from the most recent tool result. The
// host returns exact capture identity in the tool payload, which is the only
// authoritative source identifier available to a model.
func longTaskSourceID(request model.Request) string {
	for i := len(request.Messages) - 1; i >= 0; i-- {
		for j := len(request.Messages[i].Content) - 1; j >= 0; j-- {
			block := request.Messages[i].Content[j]
			if block.Type != "tool_result" {
				continue
			}
			var payload struct {
				ID       string `json:"id"`
				SourceID string `json:"source_id"`
			}
			if json.Unmarshal([]byte(block.Content), &payload) == nil {
				if payload.SourceID != "" {
					return payload.SourceID
				}
				if payload.ID != "" {
					return payload.ID
				}
			}
		}
	}
	return ""
}

func longTaskReadContextPages(call func(string, any, any, string) error, kind, id string, version int) (string, int, error) {
	var last longTaskContextPage
	var body strings.Builder
	tailPage := -1
	cursor := ""
	for page := 0; page < 128; page++ {
		arguments := map[string]any{"kind": kind, "id": id}
		if version > 0 {
			arguments["version"] = version
		}
		if cursor != "" {
			arguments["cursor"] = cursor
		}
		var result longTaskContextPage
		if err := call("read_context", arguments, &result, fmt.Sprintf("page=%d", page)); err != nil {
			return "", -1, err
		}
		if !result.Committed || result.ID != id || (kind == "artifact" && result.Version != version) {
			return "", -1, errors.New("read_context returned an uncommitted or mismatched version")
		}
		if kind == "message" && result.ContentFormat != w.ContentFormatChunksV1 {
			return "", -1, errors.New("read_context returned an unexpected message format")
		}
		body.WriteString(result.Content)
		last = result
		if strings.Contains(result.Content, longTaskEvidence) && tailPage < 0 {
			tailPage = page
		}
		if !result.HasMore {
			break
		}
		if result.NextCursor == "" {
			return "", -1, errors.New("read_context page has no cursor")
		}
		cursor = result.NextCursor
	}
	if last.HasMore {
		return "", -1, errors.New("read_context did not terminate")
	}
	if body.Len() != last.TotalBytes {
		return "", -1, fmt.Errorf("read_context pages restored %d of %d bytes", body.Len(), last.TotalBytes)
	}
	return body.String(), tailPage, nil
}

func longTaskReadSourcePages(call func(string, any, any, string) error, sourceID string) (string, int, error) {
	var last longTaskSourcePage
	var body strings.Builder
	tailPage := -1
	cursor := ""
	for page := 0; page < 64; page++ {
		arguments := map[string]any{"source_id": sourceID}
		if cursor != "" {
			arguments["cursor"] = cursor
		}
		var result longTaskSourcePage
		if err := call("read_source", arguments, &result, fmt.Sprintf("page=%d", page)); err != nil {
			return "", -1, err
		}
		if result.SourceID != sourceID || result.SHA256 == "" {
			return "", -1, errors.New("read_source lost its exact capture identity")
		}
		body.WriteString(result.Content)
		last = result
		if strings.Contains(result.Content, longTaskSourceTail) && tailPage < 0 {
			tailPage = page
		}
		if !result.HasMore {
			break
		}
		if result.NextCursor == "" {
			return "", -1, errors.New("read_source page has no cursor")
		}
		cursor = result.NextCursor
	}
	if last.HasMore {
		return "", -1, errors.New("read_source did not terminate")
	}
	if body.Len() != last.TotalBytes {
		return "", -1, fmt.Errorf("read_source pages restored %d of %d bytes", body.Len(), last.TotalBytes)
	}
	return body.String(), tailPage, nil
}

func longTaskHandoff(prompt string) (workflow.Handoff, error) {
	var handoff workflow.Handoff
	_, rawMessages, ok := strings.Cut(prompt, "User messages and upstream evidence:\n")
	if !ok {
		return handoff, errors.New("structured prompt missing")
	}
	var messages []model.Message
	if json.Unmarshal([]byte(rawMessages), &messages) != nil || len(messages) == 0 || len(messages[0].Content) == 0 {
		return handoff, errors.New("invalid prompt messages")
	}
	_, raw, ok := strings.Cut(messages[0].Content[0].Text, nativeHandoffHeader)
	if !ok || json.NewDecoder(strings.NewReader(raw)).Decode(&handoff) != nil {
		return handoff, errors.New("exact upstream handoff missing")
	}
	if len(handoff.MessageRefs) == 0 || len(handoff.ArtifactRefs) != 0 || len(handoff.Artifacts) != 0 || len(handoff.SourceIDs) == 0 {
		return handoff, errors.New("handoff exact references are missing or leaked an intermediate artifact")
	}
	return handoff, nil
}

func longTaskResearch(call func(string, any, any, string) error, record func(string, string)) error {
	var first longTaskSourcePage
	if err := call("read_file", map[string]any{"path": "evidence.md"}, &first, "evidence.md"); err != nil {
		return err
	}
	if first.SourceID == "" || first.Content == "" || first.TotalBytes <= 8192 || !first.HasMore || first.SourceTruncated {
		return errors.New("read_file did not return a paged exact capture")
	}
	if strings.Contains(first.Content, longTaskSourceTail) {
		return errors.New("source tail was incorrectly inside the first preview")
	}
	// Same-parameter review reads are legitimate and must never be blocked.
	for i := 0; i < 3; i++ {
		var repeated longTaskSourcePage
		if err := call("read_source", map[string]any{"source_id": first.SourceID}, &repeated, "same-parameter"); err != nil {
			return err
		}
		if repeated.Content != first.Content || repeated.Offset != 0 {
			return errors.New("same-parameter re-read was blocked or changed")
		}
	}
	content, tailPage, err := longTaskReadSourcePages(call, first.SourceID)
	if err != nil {
		return err
	}
	if tailPage < 1 || !strings.Contains(content, longTaskSourceTail) {
		return errors.New("source tail evidence was not recovered through later pages")
	}
	record("evidence", fmt.Sprintf("source_total=%d tail_page=%d", len(content), tailPage))
	return nil
}

// longTaskDynamicMember pages the captured source to its tail, keeps the exact
// evidence, then publishes the single terminal report.
func longTaskDynamicMember(call func(string, any, any, string) error, record func(string, string)) error {
	var first longTaskSourcePage
	if err := call("read_file", map[string]any{"path": "evidence.md"}, &first, "evidence.md"); err != nil {
		return err
	}
	if first.SourceID == "" || first.Content == "" || first.TotalBytes <= 8192 || !first.HasMore || first.SourceTruncated {
		return errors.New("read_file did not return a paged exact capture")
	}
	if strings.Contains(first.Content, longTaskSourceTail) {
		return errors.New("source tail was incorrectly inside the first preview")
	}
	// Same-parameter review reads are legitimate and must never be blocked.
	for i := 0; i < 3; i++ {
		var repeated longTaskSourcePage
		if err := call("read_source", map[string]any{"source_id": first.SourceID}, &repeated, "same-parameter"); err != nil {
			return err
		}
		if repeated.Content != first.Content || repeated.Offset != 0 {
			return errors.New("same-parameter re-read was blocked or changed")
		}
	}
	source, tailPage, err := longTaskReadSourcePages(call, first.SourceID)
	if err != nil {
		return err
	}
	if tailPage < 1 || !strings.Contains(source, longTaskSourceTail) {
		return errors.New("source tail evidence was not recovered through later pages")
	}
	record("evidence", fmt.Sprintf("source_total=%d tail_page=%d", len(source), tailPage))
	payload := map[string]any{
		"type":  "research_report",
		"title": "长任务研究报告",
		"body":  "# 长任务研究报告\n\n## 范围\n合成资料 evidence.md。\n\n## 方法\n在 100 次以上宿主工具调用后分页取回来源尾部反证。\n\n## 发现\n" + longTaskEvidence + "\n\n## 分歧与限制\n冲突输入与否定条件保留如下：" + longTaskEvidence + "\n",
		"findings": []any{
			map[string]any{"title": "尾部反证已进入结论", "detail": longTaskEvidence, "status": "supported", "source_ids": []string{first.SourceID}},
			map[string]any{"title": "冲突来源与证据不足", "detail": "另一来源报告 4.10 USD/kg；3.25 USD/kg 的口径无法确认，证据不足。", "status": "insufficient_evidence", "source_ids": []string{}},
		},
		"source_ids": []string{first.SourceID},
		"limitations": []string{
			"合成测试资料；真实 provider 质量另行验证。",
		},
	}
	var receipt map[string]any
	if err := call("publish_artifact", payload, &receipt, "research_report"); err != nil {
		return err
	}
	if receipt["publication"] != "after_step_success" {
		return errors.New("report was not staged for terminal publication")
	}
	return nil
}

func TestLongTaskEvidenceThroughAuthenticatedAPI(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	quotedBinary := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	shim := "#!/bin/sh\nexec " + quotedBinary + " -test.run=^TestLongTaskHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, "invocations.txt")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("COCOGEM_LONG_TASK_HELPER", "1")
	t.Setenv("COCOGEM_LONG_TASK_LEDGER", ledger)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	sourceFile := longTaskSourceFile()
	if err := os.WriteFile(filepath.Join(dir, "evidence.md"), []byte(sourceFile), 0600); err != nil {
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
	// The scripted host loop drives this long-task acceptance through an
	// explicit runner; the real CLI boundary is covered separately.
	engine := &workflow.Engine{Store: db, Model: &longTaskModel{}, Runners: map[string]agent.Runner{"claude": hostRunner{}}, AllowedRoots: []string{dir}, AllowedModels: []string{"default"}, Web: tools.NewWeb("")}
	ctx, cancel := context.WithCancel(t.Context())
	if err := engine.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); engine.Wait() }()
	server := httptest.NewUnstartedServer(nil)
	app, err := New(Config{Host: server.Listener.Addr().String(), AuthFile: filepath.Join(dir, "auth.json"), WebDir: dir, Models: []string{"default"}, Roots: []string{dir}, ModelConfigured: true, Model: &longTaskModel{}, Executors: []ExecutorInfo{{ID: "claude", Installed: true, Supported: true}}}, db, engine)
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
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(encoded))
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
	call("POST", "/auth/setup", PasswordInput{Password: "long-task-password"}, nil, 200, &session)
	csrf = session.CSRFToken
	var ws w.Workspace
	call("POST", "/api/v1/workspaces", WorkspaceInput{Name: "长任务验收", Root: dir}, nil, 200, &ws)
	var members Page[w.Agent]
	call("GET", "/api/v1/workspaces/"+ws.ID+"/agents", nil, nil, 200, &members)
	if len(members.Items) != 3 {
		t.Fatalf("members=%+v", members.Items)
	}
	ids := []string{}
	for _, member := range members.Items {
		ids = append(ids, member.ID)
	}
	var thread w.Thread
	call("POST", "/api/v1/workspaces/"+ws.ID+"/threads", w.Thread{Title: "长任务证据链", AgentIDs: ids, CoordinatorID: ids[2]}, nil, 200, &thread)
	input := w.RunInput{Message: "用给定合成资料完成长任务证据复核。", ResearchScope: "given_sources"}
	var run w.Run
	call("POST", "/api/v1/threads/"+thread.ID+"/runs", input, map[string]string{"Idempotency-Key": "long-task-evidence-chain"}, 202, &run)
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	for {
		wake := db.Changed()
		var view RunView
		call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &view)
		if w.Terminal(view.Run.Status) {
			if view.Run.Status != "succeeded" || len(view.Steps) != 1 {
				diagnostic, _ := os.ReadFile(ledger + ".error")
				t.Fatalf("run=%+v steps=%+v fixture=%s", view.Run, view.Steps, diagnostic)
			}
			break
		}
		select {
		case <-wake:
		case <-deadline.C:
			diagnostic, _ := os.ReadFile(ledger + ".error")
			var snapshot RunView
			call("GET", "/api/v1/runs/"+run.ID, nil, nil, 200, &snapshot)
			var events EventPage
			call("GET", fmt.Sprintf("/api/v1/runs/%s/event-history?after=0&limit=50", run.ID), nil, nil, 200, &events)
			kinds := []string{}
			for _, event := range events.Items {
				kinds = append(kinds, event.Type)
			}
			debugLedger, _ := os.ReadFile(ledger + ".debug")
			ledgerText, _ := os.ReadFile(ledger)
			errText, _ := os.ReadFile(filepath.Join(filepath.Dir(ledger), "fixture.err"))
			t.Fatalf("long native process research did not settle: %s mode=%s task=%s status=%s steps=%+v events=%v ledger=%s err=%s debug=%s", diagnostic, snapshot.Run.EffectiveExecutionMode, snapshot.Run.TaskID, snapshot.Run.Status, snapshot.Steps, kinds, ledgerText, errText, debugLedger)
		}
	}

	var threadView ThreadView
	call("GET", "/api/v1/threads/"+thread.ID, nil, nil, 200, &threadView)
	expected := longTaskBody()
	var longMessage *w.Message
	for i, message := range threadView.Messages.Items {
		if message.ContentBytes > 256<<10 && message.Role == "assistant" {
			longMessage = &threadView.Messages.Items[i]
		}
	}
	if longMessage == nil {
		t.Fatalf("no long assistant message in timeline: %+v", threadView.Messages.Items)
	}
	if longMessage.Status != "completed" || longMessage.Format() != w.ContentFormatChunksV1 || !longMessage.ContentTruncated || longMessage.ContentBytes != len(expected) {
		t.Fatalf("long message metadata = %+v, want %d bytes", longMessage, len(expected))
	}
	if len(longMessage.Content) > w.MaxMessagePreviewBytes || !utf8.ValidString(longMessage.Content) || !strings.HasPrefix(expected, longMessage.Content) {
		t.Fatalf("timeline preview is not a bounded UTF-8 prefix: %d bytes", len(longMessage.Content))
	}
	assembled := strings.Builder{}
	cursor := ""
	pages := 0
	for {
		path := "/api/v1/runs/" + run.ID + "/messages/" + longMessage.ID + "/content?limit=32"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page MessageContentPage
		call("GET", path, nil, nil, 200, &page)
		pages++
		if page.ContentFormat != w.ContentFormatChunksV1 || page.ContentBytes != len(expected) || !utf8.ValidString(page.Content) {
			t.Fatalf("content page %d = %+v", pages, page)
		}
		assembled.WriteString(page.Content)
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatal("cursor returned without more content")
			}
			break
		}
		if page.NextCursor == "" || pages > 8 {
			t.Fatal("message content paging did not terminate")
		}
		cursor = page.NextCursor
	}
	if pages < 2 || assembled.String() != expected {
		t.Fatalf("HTTP body restored %d of %d bytes across %d pages", assembled.Len(), len(expected), pages)
	}
	if !strings.Contains(assembled.String(), longTaskEvidence) {
		t.Fatal("tail counter-evidence missing from the full HTTP body")
	}

	var artifacts Page[w.Artifact]
	call("GET", "/api/v1/threads/"+thread.ID+"/artifacts", nil, nil, 200, &artifacts)
	if len(artifacts.Items) != 1 || artifacts.Items[0].Type != "research_report" {
		var dbg EventPage
		call("GET", fmt.Sprintf("/api/v1/runs/%s/event-history?after=0&limit=200", run.ID), nil, nil, 200, &dbg)
		toolsUsed := []string{}
		for _, event := range dbg.Items {
			if event.Type == "tool_finished" || event.Type == "tool_failed" || event.Type == "artifact_published" {
				toolsUsed = append(toolsUsed, event.Type+":"+string(event.Data))
			}
		}
		t.Fatalf("published artifacts=%+v events=%v", artifacts.Items, toolsUsed)
	}
	report := &artifacts.Items[0]
	if report.Version != 1 || len(report.SourceIDs) != 1 {
		t.Fatalf("artifact version or source attribution lost: %+v", report)
	}
	reportText, _ := json.Marshal(report)
	for _, token := range []string{"否定条件", "2026-10-27", "3.25 USD/kg", "冲突输入", "4.10 USD/kg", "证据不足"} {
		if !bytes.Contains(reportText, []byte(token)) {
			t.Fatalf("final report lost evidence token %q: %s", token, reportText)
		}
	}
	var source w.Source
	call("GET", "/api/v1/sources/"+report.SourceIDs[0], nil, nil, 200, &source)
	sum := sha256.Sum256([]byte(sourceFile))
	if source.Kind != "file" || source.Path != "evidence.md" || source.Content != longTaskSourceCapture() || source.SHA256 != hex.EncodeToString(sum[:]) || source.Truncated {
		t.Fatalf("captured source did not preserve the exact file: kind=%s path=%s bytes=%d sha=%s truncated=%v", source.Kind, source.Path, len(source.Content), source.SHA256, source.Truncated)
	}
	if !strings.Contains(source.Content, longTaskSourceTail) {
		t.Fatal("captured source lost its tail evidence")
	}

	// Persisted events are paged and checked without retaining the full history.
	after := int64(0)
	expectedSeq := int64(0)
	toolFinishes, contextReads, deltas := 0, 0, 0
	artifactSeq := int64(-1)
	for page := 0; page < 20; page++ {
		var events EventPage
		call("GET", fmt.Sprintf("/api/v1/runs/%s/event-history?after=%d&limit=500", run.ID, after), nil, nil, 200, &events)
		for _, event := range events.Items {
			expectedSeq++
			if event.Seq != expectedSeq {
				t.Fatalf("event sequence gap: got %d want %d", event.Seq, expectedSeq)
			}
			switch event.Type {
			case "tool_finished":
				toolFinishes++
				var data struct{ Name, Status string }
				if err := json.Unmarshal(event.Data, &data); err != nil {
					t.Fatal(err)
				}
				if data.Name == "read_context" && data.Status == "ok" {
					contextReads++
				}
			case "artifact_published":
				if artifactSeq < 0 {
					artifactSeq = int64(toolFinishes)
				}
			case "message_delta":
				deltas++
			}
			after = event.Seq
		}
		if events.NextCursor == "" {
			break
		}
	}
	if artifactSeq < 101 || toolFinishes < 101 {
		t.Fatalf("artifact published after %d tool finishes (want >100); total=%d", artifactSeq, toolFinishes)
	}
	// A single addressed member has no upstream handoff to page; the streaming
	// body must still produce multiple deltas.
	if deltas < 2 {
		t.Fatalf("message_deltas=%d", deltas)
	}
	var finalEvents struct {
		Type string `json:"type"`
	}
	var last EventPage
	call("GET", fmt.Sprintf("/api/v1/runs/%s/event-history?after=%d&limit=1", run.ID, after-1), nil, nil, 200, &last)
	if len(last.Items) != 1 || json.Unmarshal(last.Items[0].Data, &finalEvents) != nil || last.Items[0].Type != "run_finished" {
		t.Fatalf("terminal event was not flushed last: %+v", last.Items)
	}

	var sameParameterReads int
	for _, event := range func() []w.Event {
		var all []w.Event
		if err := db.Read(t.Context(), func(tx *store.Tx) error {
			var readErr error
			all, readErr = tx.Events(run.ID, 0)
			return readErr
		}); err != nil {
			t.Fatal(err)
		}
		return all
	}() {
		if event.Type != "tool_finished" {
			continue
		}
		var data struct{ Name, Status string }
		if json.Unmarshal(event.Data, &data) == nil && data.Name == "read_source" && data.Status == "ok" {
			sameParameterReads++
		}
	}
	if sameParameterReads < 100 {
		t.Fatalf("same-parameter reads=%d, want at least 100", sameParameterReads)
	}
}

// hostRunner delegates to a scripted host loop, so the long-task acceptance
// exercises the same tool boundary without pretending to be a real CLI.
type hostRunner struct{}

func (hostRunner) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	model := &longTaskModel{}
	return (agent.Loop{Model: model, MaxRounds: 200, MaxTools: 500}).Run(ctx, request, execute, observe)
}
