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

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/nativecli"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (e *Engine) executeStep(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source) w.Step {
	return e.executeStepWith(ctx, run, step, history, all, sourceSnapshot, nil)
}

func (e *Engine) executeStepWith(ctx context.Context, run w.Run, step w.Step, history, all []w.Message, sourceSnapshot []w.Source, checkpoint *checkpointState) w.Step {
	memberIndex := slices.IndexFunc(run.Agents, func(a w.Agent) bool { return a.ID == step.AgentID })
	if memberIndex < 0 {
		step.Status, step.Error = "failed", "member is outside this run roster"
		step.FailureKind = failureUnknown
		if err := e.saveStep(ctx, step, "step_finished"); err != nil {
			e.report(err)
		}
		return step
	}
	a := run.Agents[memberIndex]
	var adapter agent.AdapterInfo
	var connection agent.ConnectionInfo
	connectionKnown := false
	connectionBlocked := false
	if a.Executor != "" && e.Adapters != nil {
		var checkErr error
		adapter, checkErr = e.Adapters.Check(ctx, a.Executor)
		if checkErr != nil && !adapter.Installed {
			adapter.Reason = checkErr.Error()
		}
		if adapter.Supported {
			var connectionErr error
			connection, connectionKnown, connectionErr = e.connectionInfo(ctx, a.Executor, a.Model, a.ThinkingLevel)
			if connectionErr != nil {
				adapter.Supported = false
				adapter.Reason = "连接配置不可用: " + connectionErr.Error()
				connectionBlocked = true
			}
		}
	}
	allowed := e.allowedTools(run, a, step)
	required := RequiredArtifact(step, effectiveInput(run))
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: step.ID, AgentID: a.ID, AgentName: a.DisplayName, Role: "assistant", Status: "processing", ContentFormat: w.ContentFormatChunksV1, CreatedAt: w.Now()}
	messageSeq := 0
	step.Status = "running"
	var handoff Handoff
	var attempt w.Attempt
	var scope *readScope
	var stepDeadline time.Time
	var previousDraft *w.Artifact
	var retiredSession string
	// A retry may only replay a step that has not committed a host or external
	// side effect. This flag is set before any mutating tool executes.
	var mutated atomic.Bool
	err := e.Store.Write(ctx, func(tx *store.Tx) error {
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		if current.Status != "running" {
			return errors.New("run stopped before dispatch")
		}
		if err := e.validateMember(tx, run, step.AgentID); err != nil {
			return err
		}
		existing, err := tx.StepAttempts(step.ID)
		if err != nil {
			return err
		}
		number := len(existing) + 1
		reason := "initial"
		recovery := ""
		recoveredFrom := ""
		if len(existing) > 0 {
			latest := existing[len(existing)-1]
			reason = "retry_after_interruption"
			recovery = "new_attempt_from_context"
			recoveredFrom = latest.ID
			switch {
			case latest.Status == "failed" && latest.FailureKind == string(nativecli.FailureContextOverflow):
				reason = "context_fresh"
				recovery = "context_fresh"
				retiredSession = latest.SessionID
			case latest.Status == "failed" && transientRetryableKind(latest.FailureKind):
				reason = autoRetryReason
				recovery = "new_attempt_after_transient_failure"
			}
			previousDraft = latest.DraftArtifact
		}
		// A transient retry continues the same bubble: append the new attempt's
		// text to the failed message. Interrupted checkpoints keep their own
		// partial message as recovery evidence.
		if reason == autoRetryReason {
			previous, findErr := tx.LatestStepMessage(run.ID, step.ID)
			switch {
			case findErr == nil && previous.Status != "completed" && previous.Status != "canceled" && previous.ContentFormat == w.ContentFormatChunksV1:
				seq, seqErr := tx.MessageLastChunkSeq(previous.ID)
				if seqErr != nil {
					return seqErr
				}
				message, messageSeq = previous, seq
			case findErr != nil && !errors.Is(findErr, store.ErrNotFound):
				return findErr
			}
		}
		message.Status = "processing"
		connectionChange := ""
		for i := len(existing) - 1; i >= 0; i-- {
			if existing[i].Status == "blocked" {
				continue
			}
			connectionChange = connectionChanged(existing[i], connection, connectionKnown)
			break
		}
		policy := w.ExecutionPolicy{}
		if run.ExecutionPolicy != nil {
			policy = *run.ExecutionPolicy
		}
		dispatchNow := time.Now()
		deadline, alreadyExpired := applyStepDeadline(&step, existing, policy, dispatchNow)
		attempt = w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: step.ID, Number: number, Status: "running", Reason: reason, Recovery: recovery, RecoveredFrom: recoveredFrom, SourceIDs: sourceIDs(sourceSnapshot), StartedAt: dispatchNow.UTC().Format(time.RFC3339Nano), LastProgress: dispatchNow.UTC().Format(time.RFC3339Nano), Executor: a.Executor, Model: a.Model, ResolvedModel: connection.ResolvedModel, ConnectionID: connection.Identity, Adapter: adapter.Adapter, CLIVersion: adapter.Version, CLIProgram: adapter.Program, CLIIdentity: adapter.Identity, SessionMode: "fresh"}
		// The message belongs to this exact attempt, so a retry cannot make the
		// UI attribute it to a newer attempt's model.
		message.AttemptID, message.Model, message.ResolvedModel = attempt.ID, attempt.Model, attempt.ResolvedModel
		if a.Executor != "" && e.Adapters != nil && (!adapter.Supported || connectionChange != "") {
			reason := adapter.Reason
			if reason == "" {
				reason = "adapter compatibility could not be confirmed"
			}
			attempt.Status = "blocked"
			switch {
			case connectionChange != "":
				attempt.Error = connectionChange
			case connectionBlocked:
				attempt.Error = connectionBlockedPrefix + " " + a.Executor + ": " + reason
			default:
				attempt.Error = capabilityBlockedPrefix + " " + a.Executor + ": " + reason
			}
			step.Status = "interrupted"
			if ctx.Err() != nil {
				attempt.Status = "canceled"
				attempt.Error = ctx.Err().Error()
				step.Status = "canceled"
			}
			attempt.FailureKind = "capability_blocked"
			if strings.HasPrefix(attempt.Error, connectionBlockedPrefix) || strings.HasPrefix(attempt.Error, connectionChangedPrefix) {
				attempt.FailureKind = "connection_blocked"
			}
			attempt.FinishedAt = w.Now()
			step.Error = attempt.Error
			step.FailureKind = attempt.FailureKind
			step.AttemptCount = number
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", attempt)
		}
		// A blocked preflight never reaches this point, so this is the first
		// actual dispatch of the logical step and its clock starts here.
		if step.StartedAt == "" {
			startStepClock(&step, policy, dispatchNow)
			deadline = parseMoment(step.DeadlineAt)
			alreadyExpired = expired(deadline, dispatchNow)
		}
		stepDeadline = deadline
		if len(step.DependsOn) > 0 || len(step.ContextRefs) > 0 {
			artifacts, err := tx.ArtifactsMetadata(run.ThreadID)
			if err != nil {
				return err
			}
			// The planning root is a scheduling dependency only. Its message and
			// artifacts are withheld from the handoff so a planner that answered
			// the task anyway cannot seed the independent views.
			planningRoot := planningRootStep(run)
			dependentArtifacts := []w.Artifact{}
			for _, artifact := range artifacts {
				if artifact.RunID != run.ID || artifact.StepID == planningRoot || !slices.Contains(step.DependsOn, artifact.StepID) {
					continue
				}
				preview, err := tx.ArtifactPreview(artifact.ID, artifact.Version, 6000)
				if err != nil {
					return err
				}
				dependentArtifacts = append(dependentArtifacts, preview)
			}
			messages := all
			if planningRoot != "" {
				messages = slices.DeleteFunc(slices.Clone(all), func(m w.Message) bool { return m.StepID == planningRoot })
			}
			handoff = prepareHandoff(step, run.Input.Message, messages, dependentArtifacts, sourceSnapshot)
			if err := addReferencedInputs(tx, step, run.ThreadID, &handoff); err != nil {
				return err
			}
			if err := sealHandoff(tx, &handoff); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "handoff_prepared", handoff); err != nil {
				return err
			}
		}
		limits := e.historyLimitsFor(a)
		selected := recentHistory(history, run.ID, limits.Messages, limits.Tokens)
		historyIDs := make([]string, 0, len(selected))
		for _, message := range selected {
			historyIDs = append(historyIDs, message.ID)
		}
		scope, err = buildReadScope(tx, run, step, historyIDs, sourceSnapshot)
		if err != nil {
			return err
		}
		attempt.InputPlanDigest, err = inputPlanDigest(tx, step, run, a.Model, connection, adapter, scope, allowed, sourceSnapshot)
		if err != nil {
			return err
		}
		attempt.CheckpointStart, err = checkpointDigest(tx, run.ID, run.ThreadID, step.ID, attempt.SourceIDs, previousDraft)
		if err != nil {
			return err
		}
		if alreadyExpired {
			attempt.Status = "failed"
			attempt.FailureKind = failureStepTimeout
			attempt.Error = "step_timeout: the frozen step deadline elapsed before dispatch"
			attempt.FinishedAt = w.Now()
			step.Status = "failed"
			step.FailureKind = failureStepTimeout
			step.Error = attempt.Error
			step.AttemptCount = number
			if err := tx.SaveAttempt(attempt); err != nil {
				return err
			}
			if err := tx.SaveStep(step); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", attempt); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "step_finished", step)
		}
		step.AttemptCount = number
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "step_started", step); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_started", attempt); err != nil {
			return err
		}
		if recovery == "context_fresh" && retiredSession != "" {
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "session_retired", map[string]any{"attempt_id": recoveredFrom, "session_id": retiredSession}); err != nil {
				return err
			}
		}
		return tx.AppendEvent(run.ID, step.ID, a.ID, "message_started", message)
	})
	if err != nil {
		step.Status = "failed"
		step.FailureKind = failureUnknown
		step.Error = err.Error()
		if saveErr := e.saveStep(context.WithoutCancel(ctx), step, "step_finished"); saveErr != nil {
			e.report(saveErr)
		}
		return step
	}
	if attempt.Status == "blocked" {
		return step
	}
	if step.Status == "failed" {
		// A frozen step deadline already elapsed; no CLI was launched.
		return step
	}
	var stepCtx context.Context
	var stopStep context.CancelFunc
	if !stepDeadline.IsZero() {
		stepCtx, stopStep = context.WithDeadline(ctx, stepDeadline)
	} else {
		stepCtx, stopStep = context.WithCancel(ctx)
	}
	defer stopStep()
	var internalFailure atomic.Bool
	writer := newMessageWriter(e.Store, stepCtx, 100*time.Millisecond, run.ID, step.ID, a.ID, attempt.ID, message, messageSeq, func(err error) {
		internalFailure.Store(true)
		e.report(err)
		stopStep()
	})
	var stateMu sync.Mutex
	var execWG sync.WaitGroup
	execClosed := false
	sources := slices.Clone(sourceSnapshot)
	nativeCaptured := map[string]bool{}
	// A previous attempt's draft is recovery evidence for the prompt and the
	// checkpoint digest only. It is never published on behalf of the resumed
	// step: the model must re-read, revalidate and submit it again.
	var published *w.Artifact
	publishAttempts := 0
	delegated := false
	capture := func(source w.Source) (w.Source, error) {
		source.ID = w.NewID()
		source.WorkspaceID = run.WorkspaceID
		source.ThreadID = run.ThreadID
		source.RunID = run.ID
		source.CaptureStepID = step.ID
		source.ContentDigest = contentDigest(source.Content)
		metadata := source
		metadata.Content = ""
		err := e.Store.Write(ctx, func(tx *store.Tx) error {
			if err := tx.SaveSource(source); err != nil {
				return err
			}
			return tx.AppendEvent(run.ID, step.ID, a.ID, "source_captured", metadata)
		})
		if err == nil {
			stateMu.Lock()
			// Only bounded metadata is retained in memory; exact bodies are
			// read by ID from the store when a tool explicitly requests them.
			sources = append(sources, metadata)
			stateMu.Unlock()
		}
		return source, err
	}
	execute := func(callCtx context.Context, name string, input json.RawMessage) (string, error) {
		stateMu.Lock()
		if execClosed {
			stateMu.Unlock()
			return "", errors.New("step is no longer accepting tool calls")
		}
		execWG.Add(1)
		stateMu.Unlock()
		defer execWG.Done()
		if permissionErr := e.Store.Read(callCtx, func(tx *store.Tx) error { return e.validateMember(tx, run, step.AgentID) }); permissionErr != nil {
			return "", permissionErr
		}
		if (name == "write_file" || name == "patch_file" || name == "run_test") && !slices.Contains(allowed, name) {
			return "", errors.New("tool is not granted for this step's role and engineering action")
		}
		switch name {
		case "read_skill":
			return e.readSkill(callCtx, run, step, input)
		case "read_context":
			return e.readContext(callCtx, run, step, scope, input)
		case "handoff_task":
			mutated.Store(true)
			result, handoffErr := e.submitHandoff(callCtx, run, step, input)
			if handoffErr == nil {
				stateMu.Lock()
				delegated = true
				stateMu.Unlock()
			}
			return result, handoffErr
		case "get_task":
			return e.getTask(callCtx, run, input)
		case "choose_workflow":
			mutated.Store(true)
			return e.chooseWorkflow(callCtx, run, step, input)
		case "read_source":
			stateMu.Lock()
			snapshot := slices.Clone(sources)
			stateMu.Unlock()
			return e.readSource(callCtx, run, snapshot, input)
		case "list_sources":
			stateMu.Lock()
			snapshot := slices.Clone(sources)
			stateMu.Unlock()
			return e.listSources(callCtx, run, snapshot, input)
		}
		if name == "web_search" && run.Input.ResearchScope != "web_discovery" && run.Input.ResearchScope != "auto" {
			return "", errors.New("web discovery is outside this run scope")
		}
		if name == "fetch_url" && run.Input.ResearchScope != "web_discovery" && run.Input.ResearchScope != "auto" {
			var in struct {
				URL string `json:"url"`
			}
			if err := tools.Decode(input, &in); err != nil {
				return "", err
			}
			stateMu.Lock()
			snapshot := slices.Clone(sources)
			stateMu.Unlock()
			if !slices.Contains(run.Input.SourceURLs, in.URL) && !slices.ContainsFunc(snapshot, func(s w.Source) bool { return s.URL == in.URL }) {
				return "", errors.New("URL is outside the selected given-source research scope")
			}
		}
		if name != "publish_artifact" {
			if slices.Contains([]string{"write_file", "patch_file", "run_test"}, name) {
				mutated.Store(true)
			}
			return tools.Execute(callCtx, name, input, tools.Scope{Root: run.Workspace.Root, Web: e.Web, Capture: capture})
		}
		mutated.Store(true)
		allowedPublisher, permissionErr := e.mayPublish(callCtx, run, step)
		if permissionErr != nil {
			return "", permissionErr
		}
		if !allowedPublisher {
			return "", errors.New("intermediate steps cannot publish a formal artifact; finish this step and let the final delivery step publish")
		}
		stateMu.Lock()
		if publishAttempts >= 4 {
			stateMu.Unlock()
			return "", errors.New("publication retry budget exceeded; finish this step with the accepted or previous valid content")
		}
		publishAttempts++
		stateMu.Unlock()
		var payload struct {
			Type        string      `json:"type"`
			Title       string      `json:"title"`
			Body        string      `json:"body"`
			Findings    []w.Finding `json:"findings"`
			SourceIDs   []string    `json:"source_ids"`
			Limitations []string    `json:"limitations"`
		}
		if err := tools.Decode(input, &payload); err != nil {
			return "", err
		}
		artifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: run.ThreadID, RunID: run.ID, StepID: step.ID, Type: payload.Type, Title: payload.Title, Body: payload.Body, Findings: payload.Findings, SourceIDs: payload.SourceIDs, Limitations: payload.Limitations, Feedback: "unreviewed", CreatedAt: w.Now()}
		stateMu.Lock()
		knownSources := slices.Clone(sources)
		stateMu.Unlock()
		ensureNativeObservationDisclosure(&artifact, knownSources)
		if err := ValidateArtifact(artifact, required, knownSources); err != nil {
			return "", err
		}
		if err := ValidateEngineeringEvidence(artifact, run, knownSources); err != nil {
			return "", err
		}
		// Persist the unpublished payload on the attempt immediately; a process
		// interruption must not turn a submitted draft back into guessable text.
		draft := artifact
		draftCtx, draftCancel := context.WithTimeout(context.WithoutCancel(callCtx), 5*time.Second)
		draftErr := e.Store.Write(draftCtx, func(tx *store.Tx) error {
			current, err := tx.Attempt(attempt.ID)
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
		stateMu.Lock()
		published = &artifact
		attempt.DraftArtifact = &draft
		stateMu.Unlock()
		return `{"accepted":true,"publication":"after_step_success"}`, nil
	}
	observe := func(kind string, data map[string]any) error {
		if stringValue(data["origin"]) == "native" && stepCtx.Err() != nil {
			// Revocation, user cancel or a frozen deadline already stopped
			// this step; native output emitted afterwards is not evidence.
			return nil
		}
		if kind == "tool_started" || kind == "tool_finished" {
			slog.Info(kind, "run_id", run.ID, "step_id", step.ID, "agent_id", a.ID, "tool_call_id", data["tool_call_id"], "tool", data["name"], "status", data["status"])
		}
		if kind == "tool_finished" && stringValue(data["origin"]) == "native" {
			callID := stringValue(data["tool_call_id"])
			stateMu.Lock()
			duplicate := callID != "" && nativeCaptured[callID]
			if callID != "" {
				nativeCaptured[callID] = true
			}
			stateMu.Unlock()
			if !duplicate {
				if source, ok := nativeSource(run, data); ok {
					saved, err := capture(source)
					if err != nil {
						return err
					}
					data["source_id"] = saved.ID
				}
			}
			data = nativeEventPayload(data)
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		if execClosed {
			return nil
		}
		if writer.SnapshotError() != nil {
			return writer.SnapshotError()
		}
		switch kind {
		case "text":
			text := stringValue(data["text"])
			if text == "" {
				return nil
			}
			// The adapter decides whether a frame is real activity; a missing
			// flag (test runner, non-native loop) falls back to non-blank text.
			activity, explicit := data["activity"].(bool)
			if !explicit {
				activity = strings.TrimSpace(text) != ""
			}
			if activity {
				return writer.Append(text)
			}
			// Non-progress text is still persisted exactly, just not credited
			// as liveness.
			return writer.AppendDuplicate(text)
		case "reasoning":
			// Reasoning content never enters storage; only its support flag.
			supported, _ := data["supported"].(bool)
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				return tx.AppendEvent(run.ID, step.ID, a.ID, "reasoning", map[string]any{"supported": supported})
			})
		case "native_activity":
			activityKind := stringValue(data["kind"])
			if activityKind != "reasoning" && activityKind != "text_delta" {
				return errors.New("native_activity kind must be reasoning or text_delta")
			}
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				if err := touchAttempt(tx, attempt.ID, w.Now()); err != nil {
					return err
				}
				return tx.AppendEvent(run.ID, step.ID, a.ID, "native_activity", map[string]any{"kind": activityKind})
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
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				current, err := tx.Attempt(attempt.ID)
				if err != nil {
					return err
				}
				current.LivenessWarning = warning
				if err := tx.SaveAttempt(current); err != nil {
					return err
				}
				attempt = current
				return tx.AppendEvent(run.ID, step.ID, a.ID, "liveness_warning", warning)
			})
		case "usage":
			known, _ := data["known"].(bool)
			turn := stringValue(data["semantics"]) == "turn"
			input, output := int64(intValue(data["input_tokens"])), int64(intValue(data["output_tokens"]))
			// Native usage is emitted after cancellation/stall cleanup, so the
			// write cannot depend on the stopped step context.
			usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer usageCancel()
			return e.Store.Write(usageCtx, func(tx *store.Tx) error {
				current, err := tx.Attempt(attempt.ID)
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
				attempt = current
				return tx.AppendEvent(run.ID, step.ID, a.ID, "usage_updated", data)
			})
		case "native_adapter", "native_session", "native_process", "tool_started", "tool_finished":
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				current, err := tx.Attempt(attempt.ID)
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
					if attempt.Recovery == "context_fresh" && retiredSession != "" && session == retiredSession {
						return &nativecli.Failure{Kind: nativecli.FailureProtocol, Reason: "context_fresh_reused_session: the native CLI reused the retired session; fresh recovery was not confirmed"}
					}
					current.SessionID = session
					current.SessionMode, _ = data["mode"].(string)
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
				attempt = current
				if changed {
					if err := tx.AppendEvent(run.ID, step.ID, a.ID, "cli_changed", map[string]any{"executor": current.Executor, "previous_identity": attempt.CLIIdentity, "current_identity": current.CLIIdentity, "version": current.CLIVersion}); err != nil {
						return err
					}
				}
				return tx.AppendEvent(run.ID, step.ID, a.ID, kind, data)
			})
		default:
			return e.Store.Write(ctx, func(tx *store.Tx) error {
				return tx.AppendEvent(run.ID, step.ID, a.ID, kind, data)
			})
		}
	}
	request, requestErr := e.buildRequest(stepCtx, run, step, a, allowed, required, connection, adapter, message, handoff, scope, history, all, sourceSnapshot, previousDraft, attempt)
	if requestErr != nil {
		err = requestErr
	} else if err = stepCtx.Err(); err != nil {
		// A checkpoint, user cancel or frozen deadline arrived before launch.
	} else if a.Executor != "" {
		runner := e.Runners[a.Executor]
		if runner == nil {
			runner = e.Fallback
		}
		if runner == nil {
			err = errors.New(capabilityBlockedPrefix + " " + a.Executor + ": selected CLI executor is unavailable")
		} else {
			err = runner.Run(stepCtx, request, execute, observe)
		}
	} else {
		err = (agent.Loop{Model: e.Model}).Run(stepCtx, request, execute, observe)
	}
	stateMu.Lock()
	execClosed = true
	stateMu.Unlock()
	execWG.Wait()
	if closeErr := writer.Close(); closeErr != nil && err == nil {
		err = closeErr
	} else if closeErr != nil {
		e.report(closeErr)
	}
	message = writer.Snapshot()
	stateMu.Lock()
	publishedCopy := published
	delegatedCopy := delegated
	knownSources := slices.Clone(sources)
	stateMu.Unlock()
	if err == nil && required != "" && publishedCopy == nil && !delegatedCopy {
		err = &nativecli.Failure{Kind: nativecli.FailureKind(failureArtifactContract), Reason: "required artifact was not submitted"}
	}
	runExpired := ctx.Err() == context.DeadlineExceeded
	stepDeadlineExpired := !stepDeadline.IsZero() && !time.Now().Before(stepDeadline) && errors.Is(stepCtx.Err(), context.DeadlineExceeded)
	if err == nil && stepCtx.Err() != nil {
		err = stepCtx.Err()
	}
	outcome := stepOutcome{step: "succeeded", attempt: "succeeded"}
	if err != nil {
		checkpointed := checkpoint != nil && checkpoint.by(step.ID)
		outcome = classifyStepError(err, stepCtx.Err(), checkpointed, internalFailure.Load(), runExpired, stepDeadlineExpired)
	}
	step.Status = outcome.step
	step.Error = outcome.reason
	step.FailureKind = outcome.kind
	if step.Status == "succeeded" {
		step.Error = ""
		step.FailureKind = ""
	}
	attempt.Status = outcome.attempt
	attempt.Error = outcome.reason
	attempt.FailureKind = outcome.kind
	if outcome.recovery != "" {
		attempt.Recovery = outcome.recovery
	}
	attempt.FinishedAt = w.Now()
	if step.Status != "succeeded" && publishedCopy != nil {
		draft := *publishedCopy
		attempt.DraftArtifact = &draft
	} else if step.Status == "succeeded" {
		attempt.DraftArtifact = nil
	}
	if outcome.kind == failureCheckpointInterrupt && checkpoint != nil {
		if description := checkpoint.describe(); description != "" {
			step.Error = "checkpoint_interrupted: " + description
			attempt.Error = step.Error
		}
	}
	if step.Status == "interrupted" && checkpoint != nil {
		checkpoint.request(step.ID, step.Error)
	}
	switch step.Status {
	case "succeeded":
		message.Status = "completed"
	case "interrupted":
		message.Status = "interrupted"
	case "canceled":
		message.Status = "canceled"
	default:
		message.Status = "failed"
	}
	if step.Status == "succeeded" && publishedCopy != nil {
		if validationErr := ValidateArtifact(*publishedCopy, required, knownSources); validationErr != nil {
			step.Status, message.Status, attempt.Status = "failed", "failed", "failed"
			step.Error, attempt.Error = validationErr.Error(), validationErr.Error()
			step.FailureKind, attempt.FailureKind = "artifact_contract", "artifact_contract"
			attempt.DraftArtifact = publishedCopy
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	saveErr := e.Store.Write(cleanup, func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err != nil {
			return err
		}
		// Re-read the run: this step may itself have selected a workflow.
		current, err := tx.Run(run.ID)
		if err != nil {
			return err
		}
		formal := map[string]bool{}
		for _, final := range finalSteps(effectiveInput(current), steps) {
			formal[final.ID] = true
		}
		if publishedCopy != nil && step.Status == "succeeded" && formal[step.ID] {
			if len(current.Input.ArtifactRefs) > 0 {
				ref := current.Input.ArtifactRefs[0]
				previous, err := tx.Artifact(ref.ID, ref.Version)
				if err != nil {
					return err
				}
				if previous.Type == publishedCopy.Type {
					versions, err := tx.Artifacts(run.ThreadID)
					if err != nil {
						return err
					}
					publishedCopy.ID = previous.ID
					for _, v := range versions {
						if v.ID == previous.ID {
							publishedCopy.Version = max(publishedCopy.Version, v.Version+1)
						}
					}
				}
			}
			if err := tx.SaveArtifact(*publishedCopy); err != nil {
				return err
			}
			if err := tx.AppendEvent(run.ID, step.ID, a.ID, "artifact_published", publishedCopy); err != nil {
				return err
			}
		}
		if publishedCopy != nil && step.Status == "succeeded" && !formal[step.ID] {
			// A root that delegated, or a superseded continuation, keeps its
			// submission as attempt evidence; it must never become formal.
			draft := *publishedCopy
			attempt.DraftArtifact = &draft
		}
		if stored, readErr := tx.Attempt(attempt.ID); readErr == nil {
			stored.Status = attempt.Status
			stored.Error = attempt.Error
			stored.FailureKind = attempt.FailureKind
			stored.Recovery = attempt.Recovery
			stored.RecoveredFrom = attempt.RecoveredFrom
			stored.SessionID = attempt.SessionID
			stored.SessionMode = attempt.SessionMode
			stored.DraftArtifact = attempt.DraftArtifact
			stored.UsageKnown = attempt.UsageKnown
			stored.UsageCompleteness = attempt.UsageCompleteness
			stored.InputTokens = attempt.InputTokens
			stored.OutputTokens = attempt.OutputTokens
			stored.FinishedAt = attempt.FinishedAt
			attempt = stored
		}
		if digest, err := checkpointDigest(tx, run.ID, run.ThreadID, step.ID, attempt.SourceIDs, attempt.DraftArtifact); err == nil {
			attempt.CheckpointDigest = digest
		}
		if err := tx.SaveAttempt(attempt); err != nil {
			return err
		}
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "message_finished", message); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, step.ID, a.ID, "attempt_finished", attempt); err != nil {
			return err
		}
		return tx.AppendEvent(run.ID, step.ID, a.ID, "step_finished", step)
	})
	if saveErr != nil {
		step.Status = "failed"
		step.FailureKind = failureUnknown
		step.Error = saveErr.Error()
		if updateErr := e.Store.Write(cleanup, func(tx *store.Tx) error {
			stored, readErr := tx.Attempt(attempt.ID)
			if readErr != nil {
				return readErr
			}
			stored.Status = "failed"
			stored.Error = saveErr.Error()
			stored.FailureKind = failureUnknown
			stored.FinishedAt = w.Now()
			if err := tx.SaveAttempt(stored); err != nil {
				return err
			}
			return tx.SaveStep(step)
		}); updateErr != nil {
			e.report(updateErr)
		}
		e.report(saveErr)
	}
	// Automatic retries create a fresh Attempt and Message after the failed
	// evidence is durable. Only a transient provider/connection failure with
	// no committed side effect, live context and untriggered checkpoint is
	// eligible; the persisted reason keeps the budget across restarts.
	if saveErr == nil &&
		step.Status == "failed" && attempt.Status == "failed" &&
		ctx.Err() == nil && !mutated.Load() &&
		// attempt.Number is durable and includes the current attempt, so the
		// budget survives restart and cannot be reset by relabelling reasons.
		attempt.Number <= autoRetryLimit &&
		(checkpoint == nil || !checkpoint.triggered()) &&
		transientRetryable(err) {
		delay := e.retryDelay(attempt.Number - 1)
		eventErr := e.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
			return tx.AppendEvent(run.ID, step.ID, a.ID, "retry_scheduled", map[string]any{
				"attempt_id":        attempt.ID,
				"attempt_number":    attempt.Number,
				"next_attempt":      attempt.Number + 1,
				"max_attempts":      autoRetryLimit + 1,
				"failure_kind":      attempt.FailureKind,
				"reason":            autoRetryReason,
				"backoff_millis":    delay.Milliseconds(),
				"side_effects_seen": mutated.Load(),
			})
		})
		if eventErr != nil {
			e.report(eventErr)
			return step
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return step
		case <-timer.C:
		}
		if ctx.Err() != nil || checkpoint != nil && checkpoint.triggered() {
			return step
		}
		return e.executeStepWith(ctx, run, step, history, all, sourceSnapshot, checkpoint)
	}
	return step
}
