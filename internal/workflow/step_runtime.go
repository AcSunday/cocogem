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
	delegated      bool
	closed         bool
	execWG         sync.WaitGroup
	mutated        atomic.Bool
	internalFailed atomic.Bool

	retiredSession string
}

func (x *stepRuntime) capture(source w.Source) (w.Source, error) {
	source.ID = w.NewID()
	source.WorkspaceID = x.run.WorkspaceID
	source.ThreadID = x.run.ThreadID
	source.RunID = x.run.ID
	source.CaptureStepID = x.step.ID
	source.ContentDigest = contentDigest(source.Content)
	metadata := source
	metadata.Content = ""
	err := x.engine.Store.Write(x.ctx, func(tx *store.Tx) error {
		if err := tx.SaveSource(source); err != nil {
			return err
		}
		return tx.AppendEvent(x.run.ID, x.step.ID, x.agent.ID, "source_captured", metadata)
	})
	if err == nil {
		x.mu.Lock()
		// Only bounded metadata is retained in memory; exact bodies are read by
		// ID from the store when a tool explicitly requests them.
		x.sources = append(x.sources, metadata)
		x.mu.Unlock()
	}
	return source, err
}

func (x *stepRuntime) executeTool(callCtx context.Context, name string, input json.RawMessage) (string, error) {
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return "", errors.New("step is no longer accepting tool calls")
	}
	x.execWG.Add(1)
	x.mu.Unlock()
	defer x.execWG.Done()
	if permissionErr := x.engine.Store.Read(callCtx, func(tx *store.Tx) error {
		return x.engine.validateMember(tx, x.run, x.step.AgentID)
	}); permissionErr != nil {
		return "", permissionErr
	}
	if (name == "write_file" || name == "patch_file" || name == "run_test") && !slices.Contains(x.allowed, name) {
		return "", errors.New("tool is not granted for this step's role and engineering action")
	}
	switch name {
	case "read_skill":
		return x.engine.readSkill(callCtx, x.run, x.step, input)
	case "read_context":
		return x.engine.readContext(callCtx, x.run, x.step, x.scope, input)
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
		return x.engine.readSource(callCtx, x.run, snapshot, input)
	case "list_sources":
		x.mu.Lock()
		snapshot := slices.Clone(x.sources)
		x.mu.Unlock()
		return x.engine.listSources(callCtx, x.run, snapshot, input)
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
		if slices.Contains([]string{"write_file", "patch_file", "run_test"}, name) {
			x.mutated.Store(true)
		}
		return tools.Execute(callCtx, name, input, tools.Scope{Root: x.run.Workspace.Root, Web: x.engine.Web, Capture: x.capture})
	}
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
	artifact := w.Artifact{ID: w.NewID(), Version: 1, Revision: 1, ThreadID: x.run.ThreadID, RunID: x.run.ID, StepID: x.step.ID, Type: payload.Type, Title: payload.Title, Body: payload.Body, Findings: payload.Findings, SourceIDs: payload.SourceIDs, Limitations: payload.Limitations, Feedback: "unreviewed", CreatedAt: w.Now()}
	x.mu.Lock()
	knownSources := slices.Clone(x.sources)
	x.mu.Unlock()
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
		current, err := tx.Attempt(x.attempt.ID)
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
	x.attempt.DraftArtifact = &draft
	x.mu.Unlock()
	return `{"accepted":true,"publication":"after_step_success"}`, nil
}

func (x *stepRuntime) observeEvent(kind string, data map[string]any) error {
	if stringValue(data["origin"]) == "native" && x.stepCtx.Err() != nil {
		// Revocation, user cancel or a frozen deadline already stopped this
		// step; native output emitted afterwards is not evidence.
		return nil
	}
	if kind == "tool_started" || kind == "tool_finished" {
		slog.Info(kind, "run_id", x.run.ID, "step_id", x.step.ID, "agent_id", x.agent.ID, "tool_call_id", data["tool_call_id"], "tool", data["name"], "status", data["status"])
	}
	if kind == "tool_finished" && stringValue(data["origin"]) == "native" {
		callID := stringValue(data["tool_call_id"])
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
	if x.writer.SnapshotError() != nil {
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
		if activityKind != "reasoning" && activityKind != "text_delta" {
			return errors.New("native_activity kind must be reasoning or text_delta")
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
				if x.attempt.Recovery == "context_fresh" && x.retiredSession != "" && session == x.retiredSession {
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
