package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// These review regressions exercise the real stepRuntime dispatcher and durable
// admission records. Only synthetic bytes and commands in t.TempDir are used.
// They do not call a model, native CLI, network, or the user's running server.

type reviewEffectFixture struct {
	runtime *stepRuntime
	db      *store.Store
	service *project.Service
	root    string
	outside string
}

func newReviewEffectFixture(t *testing.T, mode string) reviewEffectFixture {
	t.Helper()
	db := openDeliveryStore(t)
	area := t.TempDir()
	root, outside := filepath.Join(area, "workspace"), filepath.Join(area, "external.txt")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("PUBLIC_EXACT_EXTERNAL_READ_PROOF"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	outside, err = filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	workspace := w.Workspace{ID: "review-effect-workspace", Name: "Review fixture", Root: root, Revision: 1}
	member := w.Agent{
		ID: "review-effect-member", WorkspaceID: workspace.ID, DisplayName: "Fixture member", Handle: "review-effect-member",
		Tools: tools.AllNames, Revision: 1,
	}
	thread := w.Thread{ID: "review-effect-thread", WorkspaceID: workspace.ID, CoordinatorID: member.ID, AgentIDs: []string{member.ID}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(workspace); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		settings, err := tx.Settings()
		if err != nil {
			return err
		}
		settings.ExecutionMode, settings.Revision = mode, settings.Revision+1
		return tx.SaveSettings(settings)
	}); err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: workspace.ID, ThreadID: thread.ID, OwnerMemberID: member.ID,
		Goal:            "Read the exact authorized fixture or execute the exact temporary command",
		AllowedActions:  []string{"read", "write", "run_test", "execute", "network", "collaborate", "publish_artifact", "native_read", "native_write", "native_execute"},
		SourceMessageID: "review-effect-user-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "review-effect-run", WorkspaceID: workspace.ID, Workspace: workspace, ThreadID: thread.ID,
		TaskID: created.Task.ID, TaskRevision: 1, BudgetAccountID: created.Budget.ID,
		TaskDirectory: root, Status: "running", CreatedAt: w.Now(),
		Agents: []w.Agent{member}, CoordinatorID: member.ID,
		Input: w.RunInput{Workflow: "engineering", Message: "Perform the authorized fixture operation", ResearchScope: "auto", Assignments: map[string]string{"engineer": member.ID}},
	}
	step := w.Step{ID: "review-effect-step", RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: "engineer", Status: "running"}
	attempt := w.Attempt{ID: "review-effect-attempt", RunID: run.ID, StepID: step.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := service.EnsureRunAuthorization(tx, &run, "review-effect-user-message"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "review-effect-run-key", "review-effect-run-digest"); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Store: db, AllowedModels: []string{""}, Limits: DefaultLimits()}
	// A zero messageWriter is enough because these operations emit no text;
	// SnapshotError still checks a real mutex and its persistence error state.
	runtime := &stepRuntime{
		ctx: t.Context(), stepCtx: t.Context(), engine: engine,
		run: run, step: step, attempt: attempt, agent: member,
		allowed: append([]string{}, tools.AllNames...), writer: &messageWriter{},
		nativeCaptured: map[string]bool{},
	}
	return reviewEffectFixture{runtime: runtime, db: db, service: service, root: root, outside: outside}
}

type reviewEffectOutcome struct {
	output string
	err    error
}

// A realistic browser answer loop. It observes persisted pending requests and
// decides them through the project service. If a receipt leads to a second
// prompt, the test sees the second durable request rather than auto-answering it.
func reviewEffectWaitRequest(t *testing.T, f reviewEffectFixture, ctx context.Context, excluded map[string]bool, done <-chan reviewEffectOutcome) w.ToolPermissionRequest {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		requests, err := f.service.ThreadPermissionRequests(ctx, f.runtime.run.ThreadID, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, request := range requests {
			if request.Status == w.PermissionPending && !excluded[request.ID] {
				return request
			}
		}
		select {
		case outcome := <-done:
			t.Fatalf("tool returned before an authorization prompt: output=%q err=%v", outcome.output, outcome.err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for an execution-bound authorization prompt")
		case <-ticker.C:
		}
	}
}

func reviewEffectApprove(t *testing.T, f reviewEffectFixture, request w.ToolPermissionRequest) {
	t.Helper()
	if _, err := f.service.DecidePermissionRequest(t.Context(), request.ID, request.Revision, project.DecisionInput{
		Decision: w.DecisionAllowOnce, RequestID: "review-effect-decision-" + request.ID,
	}); err != nil {
		t.Fatal(err)
	}
}

func reviewEffectAdmission(t *testing.T, f reviewEffectFixture, request w.ToolPermissionRequest, callID string) w.ToolInvocationAdmission {
	t.Helper()
	var admission w.ToolInvocationAdmission
	if err := f.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		admission, err = tx.AdmissionByRequestCall(request.ID, callID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return admission
}

// A01: approving an outside read has to make those exact bytes readable. Merely
// returning an error from the old root-only dispatcher is not a valid approval.
func TestReviewWorkflowExternalReadExecutesAfterOneApproval(t *testing.T) {
	f := newReviewEffectFixture(t, w.ExecutionControlled)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"path": f.outside})
	done := make(chan reviewEffectOutcome, 1)
	go func() {
		output, err := f.runtime.executeTool(agent.WithToolCall(ctx, "review-external-read"), "read_file", raw)
		done <- reviewEffectOutcome{output, err}
	}()
	request := reviewEffectWaitRequest(t, f, ctx, nil, done)
	reviewEffectApprove(t, f, request)
	var outcome reviewEffectOutcome
	select {
	case outcome = <-done:
	case <-ctx.Done():
		t.Fatal("approved exact read did not return")
	}
	if outcome.err != nil || !strings.Contains(outcome.output, "PUBLIC_EXACT_EXTERNAL_READ_PROOF") {
		t.Errorf("approved exact external read did not produce the bytes: output=%q err=%v", outcome.output, outcome.err)
	}
	admission := reviewEffectAdmission(t, f, request, "review-external-read")
	if outcome.err == nil && admission.Status != w.AdmissionCompleted {
		t.Errorf("successful read admission was not completed: %+v", admission)
	}
	if outcome.err != nil && admission.Status == w.AdmissionCompleted {
		t.Errorf("failed approved read was falsely audited as completed: %+v", admission)
	}
}

// A02: controlled commands are requestable. The approval authorizes this exact
// temporary effect, without changing the global mode or the Run to full_access.
func TestReviewWorkflowControlledCommandExecutesAfterOneApproval(t *testing.T) {
	f := newReviewEffectFixture(t, w.ExecutionControlled)
	marker := filepath.Join(f.root, "approved-command-marker")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"argv": []string{"/usr/bin/touch", marker}, "cwd": ".", "timeout_seconds": 1})
	done := make(chan reviewEffectOutcome, 1)
	go func() {
		output, err := f.runtime.executeTool(agent.WithToolCall(ctx, "review-controlled-command"), "run_command", raw)
		done <- reviewEffectOutcome{output, err}
	}()
	request := reviewEffectWaitRequest(t, f, ctx, nil, done)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command effect happened before its approval")
	}
	reviewEffectApprove(t, f, request)
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Errorf("approved controlled command failed: %v", outcome.err)
		}
	case <-ctx.Done():
		t.Fatal("approved controlled command did not finish")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("approved command did not produce its actual temporary marker: %v", err)
	}
	var settings w.Settings
	if err := f.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		settings, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if settings.ExecutionMode != w.ExecutionControlled || f.runtime.run.EffectiveExecutionMode != w.ExecutionControlled {
		t.Fatal("single operation approval changed the entire mode")
	}
}

// A03: a tool error after an approval is a failed/unknown admission, never a
// completed one. The nonexistent temporary file cannot accidentally be read.
func TestReviewWorkflowFailedApprovedReadIsNotAuditedAsCompleted(t *testing.T) {
	f := newReviewEffectFixture(t, w.ExecutionControlled)
	missing := filepath.Join(filepath.Dir(f.outside), "missing.txt")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"path": missing})
	done := make(chan reviewEffectOutcome, 1)
	go func() {
		output, err := f.runtime.executeTool(agent.WithToolCall(ctx, "review-failed-read"), "read_file", raw)
		done <- reviewEffectOutcome{output, err}
	}()
	request := reviewEffectWaitRequest(t, f, ctx, nil, done)
	reviewEffectApprove(t, f, request)
	select {
	case outcome := <-done:
		if outcome.err == nil {
			t.Fatal("missing file unexpectedly read successfully")
		}
	case <-ctx.Done():
		t.Fatal("approved failing read did not finish")
	}
	admission := reviewEffectAdmission(t, f, request, "review-failed-read")
	if admission.Status != w.AdmissionFailed && admission.Status != w.AdmissionUnknown {
		t.Fatalf("tool error was recorded as a successful effect: %+v", admission)
	}
}

// A04: request_tool_access approval must bind to the next exact real operation.
// A second durable prompt is a failure, even if the UI could answer it again.
func TestReviewWorkflowPreApprovalIsConsumedByTheRealCallWithoutAnotherPrompt(t *testing.T) {
	f := newReviewEffectFixture(t, w.ExecutionControlled)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	arguments, _ := json.Marshal(map[string]any{"path": f.outside})
	raw, _ := json.Marshal(map[string]any{
		"action": "read", "tool_name": "read_file", "scope": f.outside,
		"arguments": json.RawMessage(arguments), "reason": "Read the explicitly requested temporary external document",
	})
	preDone := make(chan reviewEffectOutcome, 1)
	go func() {
		output, err := f.runtime.executeTool(agent.WithToolCall(ctx, "review-preapproval"), "request_tool_access", raw)
		preDone <- reviewEffectOutcome{output, err}
	}()
	request := reviewEffectWaitRequest(t, f, ctx, nil, preDone)
	reviewEffectApprove(t, f, request)
	select {
	case outcome := <-preDone:
		if outcome.err != nil {
			t.Fatalf("pre-approval call failed: %v", outcome.err)
		}
	case <-ctx.Done():
		t.Fatal("pre-approval did not return its receipt")
	}
	done := make(chan reviewEffectOutcome, 1)
	go func() {
		output, err := f.runtime.executeTool(agent.WithToolCall(ctx, "review-real-read"), "read_file", arguments)
		done <- reviewEffectOutcome{output, err}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case outcome := <-done:
			if outcome.err != nil || !strings.Contains(outcome.output, "PUBLIC_EXACT_EXTERNAL_READ_PROOF") {
				t.Fatalf("approved real operation did not produce its exact bytes: output=%q err=%v", outcome.output, outcome.err)
			}
			return
		case <-ctx.Done():
			t.Fatal("approved real operation did not finish")
		case <-ticker.C:
			requests, err := f.service.ThreadPermissionRequests(ctx, f.runtime.run.ThreadID, 200)
			if err != nil {
				t.Fatal(err)
			}
			for _, pending := range requests {
				if pending.Status == w.PermissionPending {
					t.Fatalf("one user decision produced another approval prompt: original=%s new=%+v", request.ID, pending)
				}
			}
		}
	}
}

// A05: simultaneous calls on one member runtime keep their own admission
// identities and terminal outcomes. Both use read-only temporary fixtures.
func TestReviewWorkflowConcurrentApprovedReadsKeepIndependentOutcomes(t *testing.T) {
	f := newReviewEffectFixture(t, w.ExecutionControlled)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	missing := filepath.Join(filepath.Dir(f.outside), "missing-concurrent.txt")
	paths := []string{f.outside, missing}
	expectedDigests := map[string]string{}
	results := make(chan struct {
		id string
		reviewEffectOutcome
	}, 2)
	for index, path := range paths {
		id := fmt.Sprintf("review-concurrent-%d", index)
		raw, _ := json.Marshal(map[string]any{"path": path})
		expectedDigests[id] = project.DigestArguments(raw)
		go func() {
			output, err := f.runtime.executeTool(agent.WithToolCall(ctx, id), "read_file", raw)
			results <- struct {
				id string
				reviewEffectOutcome
			}{id, reviewEffectOutcome{output, err}}
		}()
	}
	requests := map[string]w.ToolPermissionRequest{}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for len(requests) < 2 {
		items, err := f.service.ThreadPermissionRequests(ctx, f.runtime.run.ThreadID, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, request := range items {
			if request.Status == w.PermissionPending {
				requests[request.ToolCallID] = request
			}
		}
		select {
		case result := <-results:
			t.Fatalf("call returned without independent approval: id=%s output=%q err=%v", result.id, result.output, result.err)
		case <-ctx.Done():
			t.Fatal("concurrent operations did not receive two independent prompts")
		case <-ticker.C:
		}
	}
	for _, id := range []string{"review-concurrent-0", "review-concurrent-1"} {
		request, exists := requests[id]
		if !exists {
			t.Fatalf("the requests were bound to shared call state: %+v", requests)
		}
		reviewEffectApprove(t, f, request)
	}
	for range 2 {
		select {
		case result := <-results:
			admission := reviewEffectAdmission(t, f, requests[result.id], result.id)
			if admission.ToolCallID != result.id || admission.ArgumentsDigest != expectedDigests[result.id] {
				t.Errorf("concurrent call used another operation's admission: result=%+v admission=%+v", result, admission)
			}
			if result.id == "review-concurrent-0" {
				if result.err != nil || !strings.Contains(result.output, "PUBLIC_EXACT_EXTERNAL_READ_PROOF") || admission.Status != w.AdmissionCompleted {
					t.Errorf("successful approved operation was lost or mis-audited: result=%+v admission=%+v", result, admission)
				}
			} else if result.err == nil || admission.Status == w.AdmissionCompleted {
				t.Errorf("failed concurrent operation was mis-audited: result=%+v admission=%+v", result, admission)
			}
		case <-ctx.Done():
			t.Fatal("approved concurrent reads did not finish")
		}
	}
}
