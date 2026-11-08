package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/execution"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// TestBrowserServer is a test-binary fixture; it is never linked into the product server.
func TestBrowserServer(t *testing.T) {
	if os.Getenv("COCOGEM_E2E") != "1" {
		t.Skip("started only by browser test setup")
	}
	data := os.Getenv("COCOGEM_DATA_DIR")
	root := filepath.Join(data, "fixtures")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "项目 空间", "空目录"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Browser test fixture\n\nStable identity is separate from display names and model settings.\nWorkflows have finite dependencies and capture sources.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A real file outside the bound fixture root. The permission E2E asks a member
	// to read it, so the browser must exercise a real host approval instead of a
	// mock: nothing happens until the user decides in the UI.
	permissionFile := filepath.Join(data, "permission-outside.txt")
	if err := os.WriteFile(permissionFile, []byte("PERMISSION_E2E_OUTSIDE_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("COCOGEM_E2E_PERMISSION_FILE", permissionFile); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(data, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	provider := browserModel{}
	engine := &workflow.Engine{Store: db, Model: provider, Runners: map[string]agent.Runner{"claude": browserRunner{model: provider}}, Web: testutil.Web{}, AllowedModels: []string{"default"}, AllowedRoots: []string{root}}
	engine.Executions = execution.New(db, filepath.Join(data, "executions"), "browser-fixture")
	listener, err := net.Listen("tcp", os.Getenv("COCOGEM_E2E_LISTEN"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	app, err := New(Config{Host: listener.Addr().String(), AuthFile: filepath.Join(data, "auth.json"), WebDir: os.Getenv("COCOGEM_E2E_WEB_DIR"), Models: []string{"default"}, Roots: []string{root}, ModelConfigured: true, Model: provider, Executors: []ExecutorInfo{{ID: "claude", Installed: true, Supported: true, Version: "test-fixture"}}}, db, engine)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	maintenanceDone := app.StartMaintenance(ctx)
	defer func() { cancel(); engine.Wait(); maintenanceDone() }()
	server := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("Cocogem: http://%s/\n", listener.Addr())
	select {
	case <-ctx.Done():
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		<-done
	case err := <-done:
		t.Fatal(err)
	}
}

// The cancellation scenario waits for the UI instead of racing a short reply.
type browserModel struct{ testutil.Model }

// browserRunner keeps the ordinary bounded Go loop for existing scenarios and
// serves only the explicit long-task browser fixture with a test-only runner.
// The product loop limits are untouched.
type browserRunner struct{ model model.Model }

var browserMemberRetryOnce sync.Map

func (r browserRunner) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	if len(request.Messages) > 0 && len(request.Messages[0].Content) > 0 && strings.Contains(request.Messages[0].Content[0].Text, "成员重试浏览器验收：") {
		if strings.Contains(request.System, "@orange") {
			if _, retried := browserMemberRetryOnce.LoadOrStore("orange", true); !retried {
				if err := observe("text", map[string]any{"text": "橘猫的首次回复未完成。"}); err != nil {
					return err
				}
				return &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: "Gemini returned an empty, truncated or malformed response; this step did not complete"}
			}
		}
		return observe("text", map[string]any{"text": "成员回复已完成（成员重试合成验证）。"})
	}
	if len(request.Messages) > 0 && len(request.Messages[0].Content) > 0 && slices.ContainsFunc(request.Tools, func(tool model.Tool) bool { return tool.Name == "promote_to_task" }) {
		text := request.Messages[0].Content[0].Text
		for _, marker := range []string{"文件交付浏览器验收：", "任务触发浏览器验收："} {
			if strings.Contains(text, "Original user task:\n"+marker) {
				goal := strings.SplitN(strings.SplitN(text, "Original user task:\n", 2)[1], "\n", 2)[0]
				raw, _ := json.Marshal(map[string]string{"title": "浏览器合成任务", "goal": goal, "evidence_quote": goal})
				_, err := execute(ctx, "promote_to_task", raw)
				return err
			}
		}
	}
	if browserOriginalTaskHasPrefix(request, "Original user task:\n权限审批浏览器验收：") || browserOriginalTaskHasPrefix(request, "权限审批浏览器验收：") {
		return r.runPermissionFixture(ctx, request, execute, observe)
	}
	if len(request.Messages) > 0 && len(request.Messages[0].Content) > 0 && strings.Contains(request.Messages[0].Content[0].Text, "Original user task:\n文件交付浏览器验收：") {
		if !request.TrustedExecution || request.WorkingDirectory == "" {
			return fmt.Errorf("file fixture requires authorized task execution")
		}
		files, err := filepath.Glob(filepath.Join(request.WorkingDirectory, "inputs", "*"))
		if err != nil || len(files) != 1 {
			return fmt.Errorf("expected one original attachment")
		}
		data, err := os.ReadFile(files[0])
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(request.WorkingDirectory, "result.custom"), append([]byte("processed:"), data...), 0600); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"paths": []string{"result.custom"}, "title": "文件处理完成", "body": "已读取附件原件并生成可下载文件。"})
		if _, err := execute(ctx, "publish_file", raw); err != nil {
			return err
		}
		return observe("text", map[string]any{"text": "已处理附件并交付成品。"})
	}
	if len(request.Messages) > 0 && len(request.Messages[0].Content) > 0 && strings.Contains(request.Messages[0].Content[0].Text, "Original user task:\n长任务浏览器验收：") {
		return browserLongTask(ctx, execute, observe)
	}
	return agent.Loop{Model: r.model}.Run(ctx, request, execute, observe)
}

const (
	browserLongStart = "长任务正文起始标记：浏览器合成预览。"
	browserLongTail  = "末页反证：否定条件 3.25 USD/kg；日期 2026-10-27；冲突输入 4.10 USD/kg；证据不足。"
)

func browserLongBody() string {
	return browserLongStart + "\n" + strings.Repeat("浏览器合成长文证据。", 4000) + "\n" + browserLongTail
}

// browserLongTask emits more than 500 persisted events and a body larger than
// the 64 KiB preview, so the browser must page both within bounded windows.
func browserLongTask(ctx context.Context, execute agent.Execute, observe agent.Observe) error {
	calls := 0
	call := func(name string, input any) (string, error) {
		calls++
		raw, err := json.Marshal(input)
		if err != nil {
			return "", err
		}
		id := fmt.Sprintf("long-%d", calls)
		if err := observe("tool_started", map[string]any{"tool_call_id": id, "name": name, "input": raw}); err != nil {
			return "", err
		}
		started := time.Now()
		output, callErr := execute(ctx, name, raw)
		status := "ok"
		if callErr != nil {
			status, output = "error", "Tool error: "+callErr.Error()
		}
		if err := observe("tool_finished", map[string]any{"tool_call_id": id, "name": name, "status": status, "output": output, "duration_ms": time.Since(started).Milliseconds()}); err != nil {
			return "", err
		}
		return output, callErr
	}
	output, err := call("read_file", map[string]any{"path": "README.md"})
	if err != nil {
		return err
	}
	var captured struct {
		SourceID string `json:"source_id"`
		ID       string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &captured); err != nil || captured.SourceID == "" {
		return errors.New("long-task browser fixture could not capture README.md")
	}
	// Repeated same-parameter reviews are legitimate host activity and must not
	// be blocked; they also give the browser more than 500 persisted events.
	for i := 0; i < 270; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := call("read_source", map[string]any{"source_id": captured.SourceID}); err != nil {
			return err
		}
	}
	runes := []rune(browserLongBody())
	const segments = 8
	for i := range segments {
		chunk := string(runes[i*len(runes)/segments : (i+1)*len(runes)/segments])
		if err := observe("text", map[string]any{"text": chunk}); err != nil {
			return err
		}
	}
	return nil
}

const (
	browserStreamMarker = "Original user task:\n流式界面验收："
	browserStreamFirst  = "流式第一段：可见的阶段性正文。"
	browserStreamSecond = "流式第二段：持续追加的正文。"
	// The browser fixture needs two visible updates while the run is still
	// processing; 400ms clears the requested 350ms minimum with scheduling slack.
	browserStreamGap = 400 * time.Millisecond
)

// browserStreaming emits two parts with ctx-aware pauses so browser tests can
// observe the first part before the second and both before the run finishes.
func browserStreaming(ctx context.Context, emit func(string) error) (model.Response, error) {
	parts := []string{browserStreamFirst, browserStreamSecond}
	for _, part := range parts {
		if err := emit(part); err != nil {
			return model.Response{}, err
		}
		if err := browserStreamWait(ctx, browserStreamGap); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: strings.Join(parts, "")}}}, nil
}

func browserStreamWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// browserOriginalTaskHasPrefix recognizes the explicit user-task field after
// the host's frozen context brief, without matching a source or member profile.
func browserOriginalTaskHasPrefix(request model.Request, prefix string) bool {
	if len(request.Messages) == 0 || len(request.Messages[0].Content) == 0 {
		return false
	}
	input := request.Messages[0].Content[0].Text
	return strings.HasPrefix(input, prefix) || strings.Contains(input, "\n"+prefix)
}

func TestBrowserStreamingFixturePacesParts(t *testing.T) {
	request := model.Request{Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Frozen accepted task context:\n{\"task\":\"流式界面验收\"}\nOriginal user task:\n流式界面验收：给前端做确定性流式断言。"}}}}}
	parts := []string{}
	emittedAt := []time.Time{}
	response, err := browserModel{}.Complete(context.Background(), request, func(text string) error {
		parts = append(parts, text)
		emittedAt = append(emittedAt, time.Now())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0] != browserStreamFirst || parts[1] != browserStreamSecond {
		t.Fatalf("streaming fixture parts=%q", parts)
	}
	if gap := emittedAt[1].Sub(emittedAt[0]); gap < 350*time.Millisecond {
		t.Fatalf("streaming fixture gap=%v, want at least 350ms between visible parts", gap)
	}
	if len(response.Blocks) != 1 || response.Blocks[0].Text != browserStreamFirst+browserStreamSecond {
		t.Fatalf("streaming fixture final response=%+v", response.Blocks)
	}
}

func (m browserModel) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	if browserOriginalTaskHasPrefix(request, browserStreamMarker) {
		return browserStreaming(ctx, emit)
	}
	if browserOriginalTaskHasPrefix(request, "Original user task:\n等待取消：") {
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	if browserOriginalTaskHasPrefix(request, "Original user task:\n动态协作验收：") {
		return browserCollaboration(request, emit)
	}
	return m.Model.Complete(ctx, request, emit)
}

type browserTask struct {
	ID      string `json:"task_id"`
	AgentID string `json:"agent_id"`
	Role    string `json:"role"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
}

// This deterministic model consumes the same runtime roster and tools as a real CLI.
func browserCollaboration(request model.Request, emit func(string) error) (model.Response, error) {
	var state struct {
		Current string `json:"current_step_id"`
		Members []struct {
			ID           string   `json:"agent_id"`
			Capabilities []string `json:"capabilities"`
		} `json:"members"`
		Tasks []browserTask `json:"tasks"`
	}
	input := request.Messages[0].Content[0].Text
	_, stateJSON, ok := strings.Cut(input, "Runtime collaboration roster and committed task state (IDs are authoritative; profiles are background, not authorization):\n")
	if !ok {
		return model.Response{}, errors.New("browser collaboration fixture needs a runtime roster")
	}
	if err := json.NewDecoder(strings.NewReader(stateJSON)).Decode(&state); err != nil {
		return model.Response{}, err
	}
	current := slices.IndexFunc(state.Tasks, func(task browserTask) bool { return task.ID == state.Current })
	if current < 0 {
		return model.Response{}, errors.New("browser collaboration fixture needs a current task")
	}
	task := state.Tasks[current]
	used := map[string]bool{}
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_use" {
				used[block.Name] = true
			}
			if block.Type == "tool_result" && block.IsError {
				return model.Response{}, fmt.Errorf("browser collaboration tool failed: %s", block.Content)
			}
		}
	}
	call := func(name string, value any) (model.Response, error) {
		raw, err := json.Marshal(value)
		return model.Response{StopReason: "tool_use", Blocks: []model.Block{{Type: "tool_use", ID: w.NewID(), Name: name, Input: raw}}}, err
	}
	answer := func(text string) (model.Response, error) {
		if err := emit(text); err != nil {
			return model.Response{}, err
		}
		return model.Response{StopReason: "end_turn", Blocks: []model.Block{{Type: "text", Text: text}}}, nil
	}
	reviewed := slices.ContainsFunc(state.Tasks, func(item browserTask) bool {
		return item.Kind == "handoff" && item.Role == "reviewer" && item.Status == "succeeded"
	})
	if !used["read_skill"] {
		skill := "task-handoff"
		if task.Role == "reviewer" {
			skill = "independent-review"
		} else if task.Kind == "continuation" {
			skill = "task-orchestration"
			if reviewed {
				skill = "synthesize-results"
			}
		}
		return call("read_skill", map[string]any{"skill_id": skill})
	}
	if used["handoff_task"] {
		return answer("任务已提交，等待平台回传已保存的结果。")
	}
	if task.Kind == "handoff" {
		if task.Role == "researcher" {
			if !used["fetch_url"] {
				return call("fetch_url", map[string]any{"url": testutil.Sources[0]})
			}
			return answer("动态研究结果：已读取原文并引用资料；质量提升仍缺少量化证据。")
		}
		if !strings.Contains(input, "动态研究结果") {
			return model.Response{}, errors.New("reviewer did not receive the committed research message")
		}
		return answer("动态复核结果：已核对研究消息与来源；保留质量收益证据不足这一异议。")
	}
	if task.Kind == "continuation" && reviewed {
		if !strings.Contains(input, "动态复核结果") {
			return model.Response{}, errors.New("final coordinator did not receive review results")
		}
		if !used["publish_artifact"] {
			return call("publish_artifact", map[string]any{"type": "note", "title": "动态协作验收纪要", "body": "# 动态协作验收纪要\n\n研究成员读取了资料，独立复核成员核对了确切消息和产物版本。\n\n保留异议：资料不能证明质量提升，仍需真实任务验证。\n\n限制：仅验证浏览器、服务端调度和工具契约，未调用真实模型。", "source_ids": []string{}, "findings": []any{}, "limitations": []string{"合成模型，不代表真实 CLI 或供应商验证。"}})
		}
		return answer("动态协作最终汇总：研究与独立复核已完成，保留证据不足；最终纪要已保存。")
	}
	role, capability, instruction := "researcher", "fixture-research", "研究给定资料并发布研究笔记，保留证据限制。"
	refs := []string{}
	if task.Kind == "continuation" {
		role, capability, instruction = "reviewer", "fixture-review", "独立核对研究消息与来源的确切版本。"
		_, handoffJSON, found := strings.Cut(input, nativeHandoffHeader)
		var handoff workflow.Handoff
		if !found {
			return model.Response{}, errors.New("coordinator has no committed handoff")
		}
		if err := json.NewDecoder(strings.NewReader(handoffJSON)).Decode(&handoff); err != nil {
			return model.Response{}, err
		}
		for _, message := range handoff.Messages {
			if strings.Contains(message.Content, "动态研究结果") {
				refs = append(refs, message.ID)
			}
		}
		for _, artifact := range handoff.Artifacts {
			refs = append(refs, fmt.Sprintf("%s@%d", artifact.ID, artifact.Version))
		}
		refs = append(refs, handoff.SourceIDs...)
		if len(refs) < 2 {
			return model.Response{}, errors.New("independent review needs exact message and source references")
		}
	}
	for _, member := range state.Members {
		if slices.Contains(member.Capabilities, capability) {
			return call("handoff_task", map[string]any{"request_id": "browser-" + role, "target_agent_id": member.ID, "task": instruction, "role": role, "context_refs": refs})
		}
	}
	return model.Response{}, errors.New("browser collaboration fixture cannot find the required member capability")
}

// runPermissionFixture asks for one real host read outside the bound project root
// through the ordinary executeTool path. The step only continues after the UI
// decision; a denial returns the real error and publishes nothing.
func (r browserRunner) runPermissionFixture(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	path := os.Getenv("COCOGEM_E2E_PERMISSION_FILE")
	if path == "" {
		return fmt.Errorf("permission fixture path is not configured")
	}
	// The fixture must use the same honest lifecycle as the real bridge: a stable
	// call identity, tool_started before the dispatch, and tool_finished only with
	// the actual result. Nothing is reported as succeeded before the host returns.
	const callID = "browser-permission-read"
	raw, _ := json.Marshal(map[string]string{"path": path})
	if err := observe("tool_started", map[string]any{"tool_call_id": callID, "name": "read_file", "origin": "host", "input": raw}); err != nil {
		return err
	}
	started := time.Now()
	output, err := execute(agent.WithToolCall(ctx, callID), "read_file", raw)
	status := "succeeded"
	if err != nil {
		status = "denied"
	}
	if obsErr := observe("tool_finished", map[string]any{"tool_call_id": callID, "name": "read_file", "origin": "host", "status": status, "output": output, "duration_ms": time.Since(started).Milliseconds()}); obsErr != nil {
		return obsErr
	}
	if err != nil {
		return observe("text", map[string]any{"text": "授权未通过：" + err.Error()})
	}
	content := "已读取"
	if strings.Contains(output, "PERMISSION_E2E_OUTSIDE_CONTENT") {
		content = "PERMISSION_E2E_OUTSIDE_CONTENT"
	}
	artifact, _ := json.Marshal(map[string]any{
		"type": "note", "title": "权限审批浏览器验收", "body": "审批后读取内容：" + content,
		"findings": []any{}, "source_ids": []string{}, "limitations": []string{"浏览器合成验证据此文件与临时目录。"},
	})
	const publishCallID = "browser-permission-publish"
	if err := observe("tool_started", map[string]any{"tool_call_id": publishCallID, "name": "publish_artifact", "origin": "host", "input": artifact}); err != nil {
		return err
	}
	publishStarted := time.Now()
	receipt, err := execute(agent.WithToolCall(ctx, publishCallID), "publish_artifact", artifact)
	publishStatus := "succeeded"
	if err != nil {
		publishStatus = "failed"
	}
	if obsErr := observe("tool_finished", map[string]any{"tool_call_id": publishCallID, "name": "publish_artifact", "origin": "host", "status": publishStatus, "output": receipt, "duration_ms": time.Since(publishStarted).Milliseconds()}); obsErr != nil {
		return obsErr
	}
	if err != nil {
		return err
	}
	return observe("text", map[string]any{"text": "权限审批浏览器验收完成。"})
}

// TestPermissionBrowserFixtureWaitsForDecision proves the browser fixture itself
// uses the ordinary host path: a denied execute produces no artifact, and an
// allowed one publishes the real bytes it read.
func TestPermissionBrowserFixtureWaitsForDecision(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "permission-outside.txt")
	if err := os.WriteFile(outside, []byte("PERMISSION_E2E_OUTSIDE_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCOGEM_E2E_PERMISSION_FILE", outside)
	request := model.Request{Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "Original user task:\n权限审批浏览器验收：读取项目外文件。"}}}}}
	var published map[string]any
	events := []string{}
	record := func(kind string, _ map[string]any) error { return nil }
	executeDeny := func(_ context.Context, name string, _ json.RawMessage) (string, error) {
		if name == "publish_artifact" {
			t.Fatal("denied fixture published an artifact")
		}
		return "", errors.New("permission_denied: the user denied this operation")
	}
	texts := []string{}
	observeDeny := func(kind string, data map[string]any) error {
		if kind == "tool_started" || kind == "tool_finished" {
			events = append(events, kind+":"+fmt.Sprint(data["tool_call_id"])+":"+fmt.Sprint(data["status"]))
		}
		if kind == "text" {
			if text, ok := data["text"].(string); ok {
				texts = append(texts, text)
			}
		}
		return nil
	}
	if err := (browserRunner{}).runPermissionFixture(context.Background(), request, executeDeny, observeDeny); err != nil {
		t.Fatalf("denied fixture returned an error: %v", err)
	}
	if len(texts) == 0 || !strings.Contains(texts[0], "授权未通过") {
		t.Fatalf("denial was not reported: %v", texts)
	}
	// Honest lifecycle: start before the dispatch, finish with the real denial.
	if len(events) != 2 || events[0] != "tool_started:browser-permission-read:<nil>" || events[1] != "tool_finished:browser-permission-read:denied" {
		t.Fatalf("denied fixture lifecycle = %v", events)
	}
	_ = record
	executeAllow := func(_ context.Context, name string, input json.RawMessage) (string, error) {
		switch name {
		case "read_file":
			return "1\tPERMISSION_E2E_OUTSIDE_CONTENT", nil
		case "publish_artifact":
			if err := json.Unmarshal(input, &published); err != nil {
				t.Fatal(err)
			}
			return `{"accepted":true}`, nil
		}
		return "", errors.New("unexpected tool")
	}
	texts = nil
	events = nil
	if err := (browserRunner{}).runPermissionFixture(context.Background(), request, executeAllow, observeDeny); err != nil {
		t.Fatalf("allowed fixture failed: %v", err)
	}
	if published == nil || !strings.Contains(fmt.Sprint(published["body"]), "PERMISSION_E2E_OUTSIDE_CONTENT") {
		t.Fatalf("allowed fixture did not publish the real content: %+v", published)
	}
	// The same call row moves pending -> succeeded only after the real read, then
	// the publish follows its own start/finish.
	want := []string{
		"tool_started:browser-permission-read:<nil>",
		"tool_finished:browser-permission-read:succeeded",
		"tool_started:browser-permission-publish:<nil>",
		"tool_finished:browser-permission-publish:succeeded",
	}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("allowed fixture lifecycle = %v, want %v", events, want)
	}
}
