// Package nativecli runs logged-in native agents with only scoped host MCP tools.
package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

// Provider delegates the tool loop to the native CLI. Its zero value is ready.
type Provider struct {
	command func(string, ...string) *exec.Cmd
}

// ExecutorInfo distinguishes an installed CLI from one supported by this adapter.
type ExecutorInfo struct {
	ID        string `json:"id"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
}

// Discover invokes only --version; it never infers login or model availability.
func Discover(ctx context.Context) []ExecutorInfo {
	result := make([]ExecutorInfo, 0, 3)
	for _, id := range []string{"claude", "codex", "gemini"} {
		info := ExecutorInfo{ID: id}
		if _, err := exec.LookPath(id); err != nil {
			info.Reason = "CLI 未安装或不在 PATH 中"
			result = append(result, info)
			continue
		}
		info.Installed = true
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.Command(id, "--version")
		out := &boundedBuffer{limit: 4096}
		cmd.Stdout, cmd.Stderr = out, out
		err := runProbe(probeCtx, cmd)
		cancel()
		if err == nil {
			info.Version = strings.TrimSpace(out.String())
		}
		info.Supported, info.Reason = supported(id)
		if info.Supported && err == nil {
			helpCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			help := &boundedBuffer{limit: 65536}
			probe := exec.Command(id, "--help")
			probe.Stdout, probe.Stderr = help, help
			helpErr := runProbe(helpCtx, probe)
			stop()
			if helpErr != nil || !capabilities(id, info.Version, help.String()) {
				info.Supported, info.Reason = false, "CLI 缺少此适配所需的受限工具和配置隔离参数"
			}
		}
		if err != nil {
			info.Supported, info.Reason = false, "CLI 版本探测失败"
		}
		result = append(result, info)
	}
	return result
}

func supported(id string) (bool, string) {
	switch id {
	case "claude":
		return true, ""
	case "codex":
		return false, "此 Codex exec 适配尚不能关闭全部原生工具，网页执行暂不支持"
	case "gemini":
		return false, "Gemini 的配置与登录隔离尚未安全接通，网页执行暂不支持"
	default:
		return false, "不支持的 CLI"
	}
}

// Complete performs routing or a connection probe without an API-key dependency.
func (p Provider) Complete(ctx context.Context, request model.Request, emit func(string) error) (model.Response, error) {
	var calls []model.Block
	var text strings.Builder
	observe := func(kind string, data map[string]any) error {
		if kind == "text" {
			chunk := data["text"].(string)
			text.WriteString(chunk)
			if emit != nil {
				return emit(chunk)
			}
		}
		return nil
	}
	execute := func(_ context.Context, name string, input json.RawMessage) (string, error) {
		if len(calls) != 0 {
			return "", errors.New("routing permits exactly one tool decision")
		}
		calls = append(calls, model.Block{Type: "tool_use", ID: "cli-routing", Name: name, Input: append(json.RawMessage{}, input...)})
		return `{"accepted":true,"instruction":"Finish this routing-only invocation. Do not perform the task."}`, nil
	}
	if err := p.Run(ctx, request, execute, observe); err != nil {
		return model.Response{}, err
	}
	if len(calls) != 0 {
		return model.Response{Blocks: calls, StopReason: "tool_use", UsageKnown: false}, nil
	}
	if strings.TrimSpace(text.String()) == "" {
		return model.Response{}, errors.New("native CLI returned an empty reply")
	}
	return model.Response{Blocks: []model.Block{{Type: "text", Text: text.String()}}, StopReason: "end_turn", UsageKnown: false}, nil
}

// Run starts a fresh isolated native session and exposes only this step's tools.
func (p Provider) Run(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	if ok, reason := supported(request.Executor); !ok {
		return errors.New(reason)
	}
	if observe == nil || execute == nil {
		return errors.New("native CLI requires execution and observation handlers")
	}
	command := p.command
	if command == nil {
		command = exec.Command
	}
	if err := checkCapabilities(ctx, request.Executor, command); err != nil {
		return err
	}
	prompt, err := json.Marshal(request.Messages)
	if err != nil || len(prompt)+len(request.System) > 100000 {
		return errors.New("required context exceeds the 100000-byte host input budget")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	gate, err := newBridge(ctx, request.Tools, execute, observe)
	if err != nil {
		return err
	}
	defer gate.close()
	gate.stop = cancel
	workdir, err := os.MkdirTemp("", "cocogem-native-*")
	if err != nil {
		return errors.New("create isolated CLI directory")
	}
	defer os.RemoveAll(workdir)
	server := map[string]any{"type": "http", "url": gate.url(), "headers": map[string]string{"Authorization": "Bearer " + gate.token}}
	config := map[string]any{"mcpServers": map[string]any{"cocogem": server}}
	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	configPath := filepath.Join(workdir, "mcp.json")
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		return errors.New("write scoped CLI configuration")
	}
	settingsPath := filepath.Join(workdir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"disableAllHooks":true,"enabledPlugins":{}}`), 0600); err != nil {
		return errors.New("write isolated CLI settings")
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--restricted", "--tools", "", "--strict-mcp-config", "--mcp-config", configPath, "--permission-mode", "dontAsk", "--permission-prompts", "none", "--allowedTools", "mcp__cocogem__*", "--disable-slash-commands", "--no-chrome", "--no-session-persistence", "--setting-sources", "", "--settings", settingsPath}
	if request.Model != "" && request.Model != "default" {
		if strings.HasPrefix(request.Model, "-") || len(request.Model) > 200 {
			return errors.New("invalid CLI model")
		}
		args = append(args, "--model", request.Model)
	}
	cmd := command(request.Executor, args...)
	cmd.Dir = workdir
	cmd.Stdin = strings.NewReader(request.System + "\n\nOnly use the cocogem MCP tools explicitly supplied for this step. Complete the assigned step, then finish. User messages and upstream evidence:\n" + string(prompt))
	processErr := streamProcess(ctx, cmd, gate)
	gate.close()
	if err := gate.failure(); err != nil {
		return err
	}
	if processErr != nil {
		return processErr
	}
	if err := gate.emit("usage", map[string]any{"known": false, "input_tokens": 0, "output_tokens": 0}); err != nil {
		return fmt.Errorf("persist native CLI usage: %w", err)
	}
	return nil
}

func checkCapabilities(ctx context.Context, id string, command func(string, ...string) *exec.Cmd) error {
	version, help := &boundedBuffer{limit: 4096}, &boundedBuffer{limit: 65536}
	for _, probe := range []struct {
		flag string
		out  *boundedBuffer
	}{{"--version", version}, {"--help", help}} {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := command(id, probe.flag)
		cmd.Stdout, cmd.Stderr = probe.out, probe.out
		err := runProbe(probeCtx, cmd)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("native CLI capability probe failed")
		}
	}
	if !capabilities(id, strings.TrimSpace(version.String()), help.String()) {
		return errors.New("native CLI version lacks verified tool isolation controls")
	}
	return nil
}
