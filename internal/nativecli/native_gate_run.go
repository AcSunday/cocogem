package nativecli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
)

// RunPiNative is the project_autonomous Pi entry point. It owns the whole
// per-attempt lifecycle (bridge, sandbox, gate broker, egress proxy, private
// extension, CLI process) so the host only has to decide whether to call it.
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
	if request.ExecutionMode != "workspace_autonomous" && !request.ReadOnly {
		return errors.New("the gated native path is only for workspace_autonomous or read-only turns")
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
	if request.SessionMode == "resume" && (!managedNativeSession(request) || request.SessionID == "") {
		return errors.New("a managed session resume requires its exact native session identity")
	}
	if adapter == "pi" && managedNativeSession(request) {
		lock, err := prepareManagedSessionDirectory(ctx, request.SessionStorageRoot, adapter)
		if err != nil {
			return err
		}
		defer lock.Close()
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
	live := newLiveness(ctx, cancel, observe, request.FirstResponseTimeout, request.StallTimeout, request.StallAction)
	gate.live = live
	effectiveProxy := p.effectiveProxy(request, false)
	var nativeNames []string
	var cmd *exec.Cmd
	switch adapter {
	case "pi":
		nativeNames = piGateToolNames(request)
		session, err := preparePiNativeSession(request, base, workdir, gate, live, launch, "", effectiveProxy)
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
		if effectiveProxy != "" {
			cmd.Env = overrideEnv(cmd.Env, nativeProxyEnv(effectiveProxy, os.Getenv("NO_PROXY")+","+os.Getenv("no_proxy")))
		}
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

// piGateToolNames is the native catalog the gated Pi step declares.
func piGateToolNames(request model.Request) []string {
	return []string{"read", "bash", "edit", "write"}
}
