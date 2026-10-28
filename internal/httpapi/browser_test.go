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
	"syscall"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
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
	db, err := store.Open(filepath.Join(data, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	provider := browserModel{}
	engine := &workflow.Engine{Store: db, Model: provider, Runners: map[string]agent.Runner{"claude": browserRunner{model: provider}}, Web: testutil.Web{}, AllowedModels: []string{"default"}, AllowedRoots: []string{root}}
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
	defer func() { cancel(); engine.Wait() }()
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

func (r browserRunner) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
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

func (m browserModel) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	if len(request.Messages) > 0 && len(request.Messages[0].Content) > 0 && strings.HasPrefix(request.Messages[0].Content[0].Text, "Original user task:\n等待取消：") {
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	if len(request.Messages) > 0 && len(request.Messages[0].Content) > 0 && strings.Contains(request.Messages[0].Content[0].Text, "Original user task:\n动态协作验收：") {
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
	sourceIDs := []string{}
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_use" {
				used[block.Name] = true
			}
			if block.Type == "tool_result" {
				if block.IsError {
					return model.Response{}, fmt.Errorf("browser collaboration tool failed: %s", block.Content)
				}
				var source w.Source
				if json.Unmarshal([]byte(block.Content), &source) == nil && source.ID != "" {
					sourceIDs = append(sourceIDs, source.ID)
				}
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
			if !used["publish_artifact"] {
				return call("publish_artifact", map[string]any{"type": "note", "title": "动态协作研究笔记", "body": "研究证据：稳定身份与有限交接便于核对。资料没有量化证明协作质量提升。", "source_ids": sourceIDs, "findings": []any{}, "limitations": []string{"确定性浏览器测试资料，不代表真实模型质量。"}})
			}
			return answer("动态研究结果：已读取原文并提交研究笔记；质量提升仍缺少量化证据。")
		}
		if !strings.Contains(input, "动态研究结果") || !strings.Contains(input, "动态协作研究笔记") {
			return model.Response{}, errors.New("reviewer did not receive exact committed research inputs")
		}
		return answer("动态复核结果：已核对研究消息、来源与笔记版本；保留质量收益证据不足这一异议。")
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
		role, capability, instruction = "reviewer", "fixture-review", "独立核对研究消息、来源与研究笔记的确切版本。"
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
		if len(refs) < 3 {
			return model.Response{}, errors.New("independent review needs exact message, artifact and source references")
		}
	}
	for _, member := range state.Members {
		if slices.Contains(member.Capabilities, capability) {
			return call("handoff_task", map[string]any{"request_id": "browser-" + role, "target_agent_id": member.ID, "task": instruction, "role": role, "context_refs": refs})
		}
	}
	return model.Response{}, errors.New("browser collaboration fixture cannot find the required member capability")
}
