package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

// RunGeminiNative is the project_autonomous Gemini entry point. It owns the
// whole per-attempt lifecycle (bridge, sandbox, gate broker, egress proxy,
// private hook, CLI process) so the host only has to decide whether to call it.
//
// provider.go should dispatch here when
//
//	request.Executor == "gemini" && request.ExecutionMode == "project_autonomous" && request.AuthorizeNative != nil
//
// and fall back to the existing print path for full_access or a nil callback.
func (p Provider) RunGeminiNative(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	return p.runGateAdapter(ctx, request, execute, observe, "gemini")
}

// RunPiNative is the project_autonomous Pi entry point with the same contract.
func (p Provider) RunPiNative(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe) error {
	return p.runGateAdapter(ctx, request, execute, observe, "pi")
}

// runGateAdapter prepares one gated native attempt. The adapter name selects the
// CLI-specific pieces; everything else (bridge, sandbox, proxy, broker, process
// lifecycle) is shared so behaviour cannot silently diverge.
func (p Provider) runGateAdapter(ctx context.Context, request model.Request, execute agent.Execute, observe agent.Observe, adapter string) error {
	if observe == nil || execute == nil {
		return errors.New("native CLI requires execution and observation handlers")
	}
	if request.AuthorizeNative == nil {
		return errors.New("project-autonomous native execution requires the host approval callback")
	}
	if request.ExecutionMode != "project_autonomous" {
		return errors.New("the gated native path is only for project_autonomous")
	}
	adapterInfo, err := p.Check(ctx, adapter)
	if err != nil {
		return err
	}
	command := p.command
	if command == nil {
		command = exec.Command
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gate, err := newBridge(ctx, request.Tools, execute, observe)
	if err != nil {
		return err
	}
	defer gate.close()
	gate.stop = cancel
	// The private gate is the native-tool boundary; the legacy parser must
	// accept the CLI's own built-in tool events for this gated path.
	gate.trustedNative = true
	gate.setRequired(request.RequiredTools)
	gate.sessionMode, gate.sessionID = request.SessionMode, request.SessionID
	if request.SessionStorageRoot != "" {
		return errors.New("a native gated attempt does not support a managed resumable session")
	}
	base, err := os.MkdirTemp("", "cocogem-native-*")
	if err != nil {
		return errors.New("create isolated CLI directory")
	}
	defer os.RemoveAll(base)
	workdir := filepath.Join(base, ".cocogem")
	if err := os.MkdirAll(workdir, 0700); err != nil {
		return errors.New("create isolated CLI directory")
	}
	prompt, err := json.Marshal(request.Messages)
	if err != nil {
		return newFailure(FailureHostInputLimit, "required context could not be encoded for the native CLI")
	}
	if budget := model.InputTokenBudget(request.ContextSize); budget > 0 && model.EstimateTokens(string(prompt))+model.EstimateTokens(request.System) > budget {
		return newFailure(FailureHostInputLimit, "required context exceeds the %d-token host input budget", budget)
	}
	launch := resolveLaunchPlan(request)
	if adapter == "gemini" && launch == nil {
		info, err := p.InspectExecutionConnection(ctx, adapter, request.Model, request.ThinkingLevel)
		if err != nil {
			return err
		}
		if request.ExpectedConnection.Identity != "" && request.ExpectedConnection.Identity != info.Identity {
			return errors.New("Gemini connection changed before the gated launch")
		}
		plan, ok := cachedPlan(adapter, info.Identity)
		if !ok {
			return errors.New("Gemini connection snapshot is unavailable")
		}
		launch = &plan
	}
	live := newLiveness(ctx, cancel, observe, request.FirstResponseTimeout, request.StallTimeout, request.StallAction)
	gate.live = live
	var nativeNames []string
	var cmd *exec.Cmd
	switch adapter {
	case "gemini":
		nativeNames = geminiGateToolNames(request)
		session, err := prepareGeminiNativeSession(request, base, workdir, gate, live, launch, "")
		if err != nil {
			return err
		}
		defer session.close()
		args, env, err := session.nativeArgs()
		if err != nil {
			return err
		}
		if err := gate.emit("native_adapter", map[string]any{
			"executor": adapterInfo.ID, "version": adapterInfo.Version, "program": adapterInfo.Program,
			"identity": adapterInfo.Identity, "adapter": "gemini-hook-gate", "isolation": "per-call-sandbox",
			"native_tools": nativeNames,
		}); err != nil {
			return err
		}
		gate.setNative(nativeNames)
		cmd = command("gemini", args...)
		cmd.Dir = request.WorkspaceRoot
		cmd.Env = removeEnv(overrideEnv(baseEnvironment(cmd.Env), launch.geminiGatedEnv), launch.geminiGatedUnset)
		cmd.Env = overrideEnv(cmd.Env, env)
		if err := configureGeminiSSE(cmd, workdir); err != nil {
			return err
		}
		inputText := request.System + "\n\nComplete the assigned step with the supplied native and Cocogem tools. Prefer the native tools for file and command work; use the Cocogem tools for member handoff and final delivery.\nUser messages and upstream evidence:\n" + string(prompt)
		cmd.Stdin = strings.NewReader(inputText)
		live.start()
		defer live.stop()
		processErr := p.streamGeminiNative(ctx, cmd, gate, live, session)
		return finishGateAttempt(parent, ctx, gate, live, processErr)
	case "pi":
		nativeNames = piGateToolNames(request)
		session, err := preparePiNativeSession(request, base, workdir, gate, live, launch, "")
		if err != nil {
			return err
		}
		defer session.close()
		args, env, err := session.nativeArgs()
		if err != nil {
			return err
		}
		if err := gate.emit("native_adapter", map[string]any{
			"executor": adapterInfo.ID, "version": adapterInfo.Version, "program": adapterInfo.Program,
			"identity": adapterInfo.Identity, "adapter": "pi-rpc-gate", "isolation": "per-call-sandbox",
			"native_tools": nativeNames,
		}); err != nil {
			return err
		}
		gate.setNative(nativeNames)
		cmd = command("pi", args...)
		cmd.Dir = request.WorkspaceRoot
		cmd.Env = overrideEnv(mergeBaseEnvironment(cmd.Env, launchEnvironment(launch)), env)
		live.start()
		defer live.stop()
		inputText := request.System + "\n\nComplete the assigned step with the supplied native and Cocogem tools. Prefer the native tools for file and command work; use the Cocogem tools for member handoff and final delivery.\nUser messages and upstream evidence:\n" + string(prompt)
		processErr := p.streamPiNative(ctx, cmd, gate, live, session, inputText)
		return finishGateAttempt(parent, ctx, gate, live, processErr)
	}
	return errors.New("unsupported gated native adapter")
}

// finishGateAttempt joins every independent failure of one gated attempt.
func finishGateAttempt(parent, ctx context.Context, gate *bridge, live *liveness, processErr error) error {
	usageErr := gate.emitUsage()
	gate.close()
	var failures []error
	failures = appendFailure(failures, parent.Err())
	failures = appendFailure(failures, live.failure())
	failures = appendFailure(failures, gate.requireTools())
	failures = appendFailure(failures, gate.failure())
	failures = appendFailure(failures, processErr)
	failures = appendFailure(failures, usageErr)
	_ = ctx
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	return nil
}

// baseEnvironment keeps the provider-injected environment when present.
func baseEnvironment(current []string) []string {
	if len(current) != 0 {
		return current
	}
	return os.Environ()
}

// mergeBaseEnvironment prefers the provider-injected env, then the frozen launch.
func mergeBaseEnvironment(current, fallback []string) []string {
	if len(current) != 0 {
		return current
	}
	if len(fallback) != 0 {
		return fallback
	}
	return os.Environ()
}

// resolveLaunchPlan reuses the connection frozen for this attempt.
func resolveLaunchPlan(request model.Request) *connectionPlan {
	if request.ExpectedConnection.Identity == "" {
		return nil
	}
	if plan, ok := cachedPlan(request.Executor, request.ExpectedConnection.Identity); ok {
		return &plan
	}
	return nil
}

func launchEnvironment(plan *connectionPlan) []string {
	if plan == nil || len(plan.piEnv) == 0 {
		return os.Environ()
	}
	return append([]string{}, plan.piEnv...)
}

// geminiGateToolNames is the native catalog the gated Gemini step declares.
func geminiGateToolNames(request model.Request) []string {
	return []string{"read_file", "write_file", "replace", "glob", "grep_search", "list_directory", "run_shell_command"}
}

// piGateToolNames is the native catalog the gated Pi step declares.
func piGateToolNames(request model.Request) []string {
	return []string{"read", "bash", "edit", "write"}
}
