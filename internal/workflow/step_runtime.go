package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// stepRuntime owns the mutable state shared by one step's tool handler, event
// observer and settlement. Keeping it in one place makes the locking rules and
// the lifetime of an attempt explicit instead of relying on closure capture.
type stepRuntime struct {
	ctx     context.Context
	stepCtx context.Context
	engine  *Engine

	run   w.Run
	step  w.Step
	agent w.Agent

	allowed  []string
	required string
	scope    *readScope

	mu             sync.Mutex
	writer         *messageWriter
	attempt        w.Attempt
	sources        []w.Source
	nativeCaptured map[string]bool
	published      *w.Artifact
	publishCount   int
	yieldRequest   *yieldToolInput
	delegated      bool
	taskPromoted   bool
	closed         bool
	execWG         sync.WaitGroup
	mutated        atomic.Bool
	internalFailed atomic.Bool

	// toolCallID is the current host tool call being executed; admission and
	// request identity are bound to it.
	toolCallID string
	// admissionID is the reserved one-time allowance consumed by this call.
	admissionID string
	// awaiting holds the tool calls suspended on a human permission decision.
	awaiting map[string]bool
	// pauseBudget is the test seam for the executing tool's pausable deadline.
	// Production leaves it nil and uses the native bridge hook attached to the
	// tool-call context.
	pauseBudget func(context.Context) func()
	// nativeAdmissions maps one native call identity to the one-time admission it
	// consumed, so each call settles with its own real outcome.
	nativeAdmissions map[string]string
	// hostCallSeq numbers host tool calls that arrive without a protocol identity
	// (direct engine calls, legacy callers). Each call still gets its own stable
	// identity so a one-time allowance can never be shared by two calls.
	hostCallSeq atomic.Int64

	retiredSession string
}

func (x *stepRuntime) capture(source w.Source) (w.Source, error) {
	source.ID = w.NewID()
	source.WorkspaceID = x.run.WorkspaceID
	source.ThreadID = x.run.ThreadID
	source.RunID = x.run.ID
	source.CaptureStepID = x.step.ID
	source.ContentDigest = contentDigest(source.Content)
	if source.Visibility == "" {
		source.Visibility = "task_chain"
	}
	_, source.ExposureDigest = w.DisclosureForSources([]w.Source{source})
	metadata := source
	metadata.Content = ""
	metadata.OriginalContent = ""
	captureCtx := x.ctx
	if source.Kind == "command" {
		var cancel context.CancelFunc
		captureCtx, cancel = context.WithTimeout(context.WithoutCancel(x.ctx), 5*time.Second)
		defer cancel()
	}
	err := x.engine.Store.Write(captureCtx, func(tx *store.Tx) error {
		if err := tx.SaveSource(source); err != nil {
			return err
		}
		if err := project.SaveDatasetForSource(tx, x.run, source); err != nil {
			return err
		}
		return tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, "source_captured", metadata)
	})
	if err == nil {
		err = x.engine.recordAttemptExposure(captureCtx, x.attemptSnapshot().ID, "source_id:"+source.ID+":"+source.ContentDigest)
	}
	if err == nil {
		x.mu.Lock()
		// Only bounded metadata is retained in memory; exact bodies are read by
		// ID from the store when a tool explicitly requests them.
		x.sources = append(x.sources, metadata)
		x.mu.Unlock()
	}
	return source, err
}

// attemptSnapshot returns a consistent copy of the attempt record. Concurrent
// native tool hooks may run while the event observer updates it, so callers must
// never read the field directly outside the runtime mutex.
func (x *stepRuntime) attemptSnapshot() w.Attempt {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.attempt
}

// setAttempt replaces the runtime attempt snapshot under the same mutex.
func (x *stepRuntime) setAttempt(value w.Attempt) {
	x.mu.Lock()
	x.attempt = value
	x.mu.Unlock()
}

// setDraftArtifact records the unpublished draft on the runtime copy.
func (x *stepRuntime) setDraftArtifact(value *w.Artifact) {
	x.mu.Lock()
	x.attempt.DraftArtifact = value
	x.mu.Unlock()
}

// baseContext returns a non-nil context even for the direct unit-test call
// paths that build a stepRuntime without one.
func (x *stepRuntime) baseContext() context.Context {
	if x.ctx != nil {
		return x.ctx
	}
	return context.Background()
}

// pauseToolDeadline pauses the executing tool's own bounded deadline while a
// human permission decision is pending. The native bridge attaches that budget
// to the tool-call context; a direct engine caller without one gets a no-op, so
// the approved effect keeps its ordinary finite timeout.
func (x *stepRuntime) pauseToolDeadline(ctx context.Context) func() {
	if x.pauseBudget != nil {
		return x.pauseBudget(ctx)
	}
	return nativecli.PauseToolDeadline(ctx)
}

func (x *stepRuntime) executeTool(callCtx context.Context, name string, input json.RawMessage) (string, error) {
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return "", errors.New("step is no longer accepting tool calls")
	}
	if x.yieldRequest != nil {
		x.mu.Unlock()
		return "", errors.New("finish the invocation after yield_run; no further tools are admitted")
	}
	if x.taskPromoted && name != "promote_to_task" {
		x.mu.Unlock()
		return "", errors.New("finish the intake invocation after task promotion; execution continues in a fresh step")
	}
	x.execWG.Add(1)
	x.mu.Unlock()
	defer x.execWG.Done()
	// Every call carries its own identity and its own admission. Concurrent
	// calls must never share either: one approval is exactly one effect.
	callID := x.currentCallID(callCtx)
	authorization, permissionErr := x.authorizeCall(callCtx, callID, name, input)
	if permissionErr != nil {
		return "", permissionErr
	}
	output, callErr := x.executeAuthorizedTool(callCtx, callID, authorization, name, input)
	// Settle with the real outcome. A canceled/uncertain call is unknown, never
	// success; a failed call is failure.
	x.settleAdmission(callCtx, authorization.admission, output, callErr)
	return output, callErr
}

// callAuthorization carries the result of the pre-execution permission check for
// exactly one call.
type callAuthorization struct {
	admission *w.ToolInvocationAdmission
	// scope is the host-normalized target the user approved, empty when the call
	// was admitted by mode/role without a decision.
	scope string
	// approved marks that a human decision authorized this call.
	approved bool
}

// authorizeCall runs the live permission boundary for one host tool call and
// returns the consumed one-time admission, if any.
func (x *stepRuntime) authorizeCall(callCtx context.Context, callID, name string, input json.RawMessage) (callAuthorization, error) {
	if permissionErr := x.engine.Store.Read(callCtx, func(tx *store.Tx) error {
		if err := x.engine.validateMember(tx, x.run, x.step.AgentID); err != nil {
			return err
		}
		allowed, err := filterAuthorizedTools(tx, x.run, x.step.AgentID, []string{name})
		if err != nil {
			return err
		}
		// request_tool_access is the request path itself and carries no action
		// class; it is never "outside the bound".
		if x.run.GrantSnapshot != "" && len(allowed) == 0 && name != "request_tool_access" {
			return errors.New("tool is outside the current project action bound")
		}
		return nil
	}); permissionErr != nil {
		return callAuthorization{}, permissionErr
	}
	// The live permission boundary decides whether this concrete effect may run,
	// needs a user decision, or must be refused. It runs before any side effect.
	// request_tool_access is the request path itself and never needs admission.
	if name != "request_tool_access" {
		admission, scope, approved, err := x.decideHostAccess(callCtx, callID, name, input)
		if err != nil {
			return callAuthorization{}, err
		}
		if !x.toolAllowedForRole(name) {
			return callAuthorization{}, errors.New("tool is not granted for this step's role and engineering action")
		}
		return callAuthorization{admission: admission, scope: scope, approved: approved}, nil
	}
	return callAuthorization{}, nil
}

// toolAllowedForRole reports whether this step's role/engineering ceiling lists
// the mutating tool. Read-only tools are governed by the action bound instead.
func (x *stepRuntime) toolAllowedForRole(name string) bool {
	switch name {
	case "write_file", "patch_file", "run_test", "run_command", "view_image", "publish_file":
		return slices.Contains(x.allowed, name)
	}
	return true
}

// executeAuthorizedTool dispatches one already-authorized call.
func (x *stepRuntime) executeAuthorizedTool(callCtx context.Context, callID string, authorization callAuthorization, name string, input json.RawMessage) (string, error) {
	switch name {
	case "request_tool_access":
		return x.requestAccess(callCtx, callID, input)
	case "promote_to_task":
		x.mutated.Store(true)
		result, err := x.engine.promoteTask(callCtx, x.run, x.step, input)
		if err == nil {
			x.mu.Lock()
			x.taskPromoted = true
			x.delegated = true
			x.mu.Unlock()
		}
		return result, err
	case "publish_file":
		return x.publishFiles(callCtx, input)
	case "view_image":
		return x.viewImage(callCtx, input)
	case "external_publish":
		return x.externalPublish(callCtx, input)
	case "read_skill":
		return x.engine.readSkill(callCtx, x.run, x.step, input)
	case "read_context":
		return x.engine.readContextForAttempt(callCtx, x.run, x.step, x.scope, input, x.attemptSnapshot().ID)
	case "handoff_task":
		x.mutated.Store(true)
		result, handoffErr := x.engine.submitHandoff(callCtx, x.run, x.step, input)
		if handoffErr == nil {
			x.mu.Lock()
			x.delegated = true
			x.mu.Unlock()
		}
		return result, handoffErr
	case "get_task":
		return x.engine.getTask(callCtx, x.run, input)
	case "choose_workflow":
		x.mutated.Store(true)
		return x.engine.chooseWorkflow(callCtx, x.run, x.step, input)
	case "read_source":
		x.mu.Lock()
		snapshot := slices.Clone(x.sources)
		x.mu.Unlock()
		return x.engine.readSourceForAttempt(callCtx, x.run, snapshot, input, x.attemptSnapshot().ID)
	case "list_sources":
		x.mu.Lock()
		snapshot := slices.Clone(x.sources)
		x.mu.Unlock()
		return x.engine.listSources(callCtx, x.run, snapshot, input)
	}
	if result, handled, err := x.executeProjectTool(callCtx, name, input); handled {
		return result, err
	}
	if name == "web_search" && x.run.Input.ResearchScope != "web_discovery" && x.run.Input.ResearchScope != "auto" {
		return "", errors.New("web discovery is outside this run scope")
	}
	if name == "fetch_url" && x.run.Input.ResearchScope != "web_discovery" && x.run.Input.ResearchScope != "auto" {
		var in struct {
			URL string `json:"url"`
		}
		if err := tools.Decode(input, &in); err != nil {
			return "", err
		}
		x.mu.Lock()
		snapshot := slices.Clone(x.sources)
		x.mu.Unlock()
		if !slices.Contains(x.run.Input.SourceURLs, in.URL) && !slices.ContainsFunc(snapshot, func(s w.Source) bool { return s.URL == in.URL }) {
			return "", errors.New("URL is outside the selected given-source research scope")
		}
	}
	if name != "publish_artifact" {
		if slices.Contains([]string{"write_file", "patch_file", "run_test", "run_command"}, name) {
			x.mutated.Store(true)
		}
		workDir := ""
		if slices.Contains(x.allowed, "run_command") {
			switch x.run.EffectiveExecutionMode {
			case w.ExecutionFullAccess, w.ExecutionProjectAutonomous:
				workDir = x.run.TaskDirectory
			}
		}
		scope := tools.Scope{Root: x.run.ProjectRoot(), WorkDir: workDir, ExecutionMode: x.run.EffectiveExecutionMode, Web: x.engine.Web, Capture: x.capture}
		if authorization.approved {
			// A human decision authorized this exact call. A command runs under the
			// sandbox with the approved target; a file tool may resolve an outside
			// path only under the approved scope.
			scope.ApprovedCommand = true
			if authorization.scope != "" && !x.scopeInsideProject(authorization.scope) {
				scope.ApprovedRoots = []string{authorization.scope}
			}
		}
		return tools.Execute(callCtx, name, input, scope)
	}
	if !slices.Contains(x.allowed, "publish_artifact") {
		return "", errors.New("artifact publication is not granted for this step")
	}
	return x.acceptArtifact(callCtx, input)
}

func (x *stepRuntime) acceptArtifact(callCtx context.Context, input json.RawMessage) (string, error) {
	x.mutated.Store(true)
	allowedPublisher, permissionErr := x.engine.mayPublish(callCtx, x.run, x.step)
	if permissionErr != nil {
		return "", permissionErr
	}
	if !allowedPublisher {
		return "", errors.New("intermediate steps cannot publish a formal artifact; finish this step and let the final delivery step publish")
	}
	x.mu.Lock()
	if x.publishCount >= 4 {
		x.mu.Unlock()
		return "", errors.New("publication retry budget exceeded; finish this step with the accepted or previous valid content")
	}
	x.publishCount++
	x.mu.Unlock()
	var payload struct {
		Type        string           `json:"type"`
		Title       string           `json:"title"`
		Body        string           `json:"body"`
		Findings    []w.Finding      `json:"findings"`
		SourceIDs   []string         `json:"source_ids"`
		Limitations []string         `json:"limitations"`
		Files       []w.ArtifactFile `json:"files,omitempty"`
	}
	if err := tools.Decode(input, &payload); err != nil {
		return "", err
	}
	artifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: x.run.ThreadID, RunID: x.run.ID, StepID: x.step.ID, Type: payload.Type, Title: payload.Title, Body: payload.Body, Findings: payload.Findings, SourceIDs: payload.SourceIDs, Limitations: payload.Limitations, Feedback: "unreviewed", CreatedAt: w.Now()}
	artifact.Files = payload.Files
	x.mu.Lock()
	knownSources := slices.Clone(x.sources)
	x.mu.Unlock()
	attemptID := x.attemptSnapshot().ID
	if err := x.engine.Store.Read(callCtx, func(tx *store.Tx) error {
		var err error
		artifact.Visibility, artifact.ExposureDigest, err = project.InheritAttemptDisclosure(tx, x.run, attemptID, knownSources)
		return err
	}); err != nil {
		return "", err
	}
	ensureNativeObservationDisclosure(&artifact, knownSources)
	if err := ValidateArtifact(artifact, x.required, knownSources); err != nil {
		return "", err
	}
	if err := ValidateEngineeringEvidence(artifact, x.run, knownSources); err != nil {
		return "", err
	}
	// Persist the unpublished payload on the attempt immediately; a process
	// interruption must not turn a submitted draft back into guessable text.
	draft := artifact
	draftCtx, draftCancel := context.WithTimeout(context.WithoutCancel(callCtx), 5*time.Second)
	draftErr := x.engine.Store.Write(draftCtx, func(tx *store.Tx) error {
		current, err := tx.Attempt(attemptID)
		if err != nil {
			return err
		}
		current.DraftArtifact = &draft
		return tx.SaveAttempt(current)
	})
	draftCancel()
	if draftErr != nil {
		return "", draftErr
	}
	x.mu.Lock()
	x.published = &artifact
	x.mu.Unlock()
	x.setDraftArtifact(&draft)
	return `{"accepted":true,"publication":"after_step_success"}`, nil
}

func (x *stepRuntime) observeEvent(kind string, data map[string]any) error {
	if kind == "native_action" || kind == "tool_started" && stringValue(data["origin"]) == "native" && slices.Contains([]string{"Bash", "Write", "Edit", "bash", "write", "edit", "run_shell_command", "write_file", "replace"}, stringValue(data["name"])) {
		x.mutated.Store(true)
	}
	if stringValue(data["origin"]) == "native" && x.stepCtx != nil && x.stepCtx.Err() != nil {
		// Revocation, user cancel or a frozen deadline already stopped this
		// step; native output emitted afterwards is not evidence.
		return nil
	}
	if kind == "tool_started" || kind == "tool_finished" {
		slog.Info(kind, "run_id", x.run.ID, "step_id", x.step.ID, "agent_id", x.agent.ID, "tool_call_id", data["tool_call_id"], "tool", data["name"], "status", data["status"])
	}
	if kind == "tool_finished" && stringValue(data["origin"]) == "native" {
		callID := stringValue(data["tool_call_id"])
		x.settleNativeAdmission(callID, stringValue(data["status"]))
		x.mu.Lock()
		duplicate := callID != "" && x.nativeCaptured[callID]
		if callID != "" {
			x.nativeCaptured[callID] = true
		}
		x.mu.Unlock()
		if !duplicate {
			if source, ok := nativeSource(x.run, data); ok {
				saved, err := x.capture(source)
				if err != nil {
					return err
				}
				data["source_id"] = saved.ID
			}
		}
		data = nativeEventPayload(data)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return nil
	}
	// Direct engine/test callers may not stream a message; observation still
	// records durable events for them.
	if x.writer != nil && x.writer.SnapshotError() != nil {
		return x.writer.SnapshotError()
	}
	switch kind {
	case "text":
		text := stringValue(data["text"])
		if text == "" {
			return nil
		}
		// The adapter decides whether a frame is real activity; a missing flag
		// (test runner, non-native loop) falls back to non-blank text.
		activity, explicit := data["activity"].(bool)
		if !explicit {
			activity = strings.TrimSpace(text) != ""
		}
		if x.writer == nil {
			return nil
		}
		if activity {
			return x.writer.Append(text)
		}
		// Non-progress text is still persisted exactly, just not credited as
		// liveness.
		return x.writer.AppendDuplicate(text)
	case "reasoning":
		// Reasoning content never enters storage; only its support flag.
		supported, _ := data["supported"].(bool)
		return x.engine.Store.Write(x.ctx, func(tx *store.Tx) error {
			return tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, "reasoning", map[string]any{"supported": supported})
		})
	case "native_activity":
		activityKind := stringValue(data["kind"])
		// permission_wait heartbeats keep a suspended approval from being
		// treated as CLI silence; they are activity, never progress.
		if activityKind != "reasoning" && activityKind != "text_delta" && activityKind != "permission_wait" {
			return errors.New("native_activity kind must be reasoning, text_delta or permission_wait")
		}
		return x.engine.Store.Write(x.ctx, func(tx *store.Tx) error {
			if err := touchAttempt(tx, x.attempt.ID, w.Now()); err != nil {
				return err
			}
			return tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, "native_activity", map[string]any{"kind": activityKind})
		})
	case "liveness_warning":
		warning := &w.LivenessWarning{
			State:          stringValue(data["state"]),
			SilenceSeconds: max(0, intValue(data["silence_seconds"])),
			LastEventType:  stringValue(data["last_event_type"]),
			ProcessAlive:   boolValue(data["process_alive"]),
			ToolActive:     boolValue(data["tool_active"]),
			ObservedAt:     w.Now(),
		}
		return x.engine.Store.Write(x.ctx, func(tx *store.Tx) error {
			current, err := tx.Attempt(x.attempt.ID)
			if err != nil {
				return err
			}
			current.LivenessWarning = warning
			if err := tx.SaveAttempt(current); err != nil {
				return err
			}
			x.attempt = current
			return tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, "liveness_warning", warning)
		})
	case "usage":
		known, _ := data["known"].(bool)
		turn := stringValue(data["semantics"]) == "turn"
		input, output := int64(intValue(data["input_tokens"])), int64(intValue(data["output_tokens"]))
		// Native usage is emitted after cancellation/stall cleanup, so the write
		// cannot depend on the stopped step context.
		usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(x.ctx), 5*time.Second)
		defer usageCancel()
		return x.engine.Store.Write(usageCtx, func(tx *store.Tx) error {
			current, err := tx.Attempt(x.attempt.ID)
			if err != nil {
				return err
			}
			if turn {
				if known {
					current.InputTokens += input
					current.OutputTokens += output
					if current.UsageCompleteness == "" || current.UsageCompleteness == "known" {
						current.UsageCompleteness = "known"
					} else {
						current.UsageCompleteness = "partial"
					}
				} else if current.UsageCompleteness == "known" || current.InputTokens > 0 || current.OutputTokens > 0 {
					current.UsageCompleteness = "partial"
				} else {
					current.UsageCompleteness = "unknown"
				}
			} else if known {
				current.InputTokens, current.OutputTokens = input, output
				current.UsageCompleteness = "known"
			} else if input > 0 || output > 0 {
				// The native CLI can report one side of a cumulative pair.
				if input > 0 {
					current.InputTokens = input
				}
				if output > 0 {
					current.OutputTokens = output
				}
				current.UsageCompleteness = "partial"
			} else if current.UsageCompleteness == "" {
				current.UsageCompleteness = "unknown"
			}
			current.UsageKnown = current.UsageCompleteness == "known"
			if err := tx.SaveAttempt(current); err != nil {
				return err
			}
			x.attempt = current
			return tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, "usage_updated", data)
		})
	case "native_adapter", "native_session", "native_process", "tool_started", "tool_finished":
		return x.engine.Store.Write(x.ctx, func(tx *store.Tx) error {
			current, err := tx.Attempt(x.attempt.ID)
			if err != nil {
				return err
			}
			changed := false
			if identity, _ := data["identity"].(string); identity != "" && current.CLIIdentity != "" && current.CLIIdentity != identity {
				changed = true
			}
			switch kind {
			case "native_adapter":
				current.CLIVersion, _ = data["version"].(string)
				current.CLIProgram, _ = data["program"].(string)
				current.CLIIdentity, _ = data["identity"].(string)
				current.Adapter, _ = data["adapter"].(string)
			case "native_session":
				session := stringValue(data["session_id"])
				mode := stringValue(data["mode"])
				if session == "" || mode != x.attempt.SessionMode {
					return &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: "native CLI changed the acknowledged session mode or omitted its identity"}
				}
				if mode == "resume" && session != x.attempt.SessionID {
					return &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: "native CLI resumed a different session identity"}
				}
				if x.attempt.Recovery == "context_fresh" && x.retiredSession != "" && session == x.retiredSession {
					return &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: "context_fresh_reused_session: the native CLI reused the retired session; fresh recovery was not confirmed"}
				}
				current.SessionID = session
				current.SessionMode = mode
			case "native_process":
				current.Process = &w.ProcessRef{PID: intValue(data["pid"]), PGID: intValue(data["pgid"]), Start: stringValue(data["start"]), Executable: stringValue(data["executable"])}
			}
			if kind == "tool_started" || kind == "tool_finished" {
				current.LastProgress = w.Now()
				current.LivenessWarning = nil
			}
			if err := tx.SaveAttempt(current); err != nil {
				return err
			}
			if kind == "native_session" {
				if err := updateNativeSession(tx, current, current.SessionID); err != nil {
					return err
				}
			}
			x.attempt = current
			if changed {
				if err := tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, "cli_changed", map[string]any{"executor": current.Executor, "previous_identity": x.attempt.CLIIdentity, "current_identity": current.CLIIdentity, "version": current.CLIVersion}); err != nil {
					return err
				}
			}
			return tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, kind, data)
		})
	default:
		return x.engine.Store.Write(x.ctx, func(tx *store.Tx) error {
			return tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, kind, data)
		})
	}
}
