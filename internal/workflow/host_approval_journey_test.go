package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// hostApprovalJourney is the exact production path Codex flagged: a member calls
// request_tool_access with the intended operation, the user decides, and the
// follow-up real call must consume that approval exactly once and actually run.
type hostApprovalJourney struct {
	db      *store.Store
	service *project.Service
	engine  *Engine
	run     w.Run
	step    w.Step
	attempt w.Attempt
	root    string
	outside string
	runtime *stepRuntime

	mu        sync.Mutex
	queue     []string
	decisions []string
}

// newHostApprovalJourney seeds a controlled-mode task whose member may write only
// inside its project. The target used by the tests lives outside it, so a real
// decision is required.
func newHostApprovalJourney(t *testing.T) *hostApprovalJourney {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root, outsideDir := t.TempDir(), t.TempDir()
	// The stored workspace root is the resolved real path; ValidateSourceRoot
	// rejects a lexical path that differs from its resolved target.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(outsideDir); err == nil {
		outsideDir = resolved
	}
	outside := filepath.Join(outsideDir, "external.txt")
	ws := w.Workspace{ID: "ws", Name: "项目", Root: root, Revision: 1, CreatedAt: w.Now()}
	member := w.Agent{ID: "member", WorkspaceID: ws.ID, DisplayName: "成员", Handle: "member", Model: "test", Revision: 1,
		Tools: []string{"read_file", "write_file", "run_test", "publish_artifact", "request_tool_access"}}
	th := w.Thread{ID: "th", WorkspaceID: ws.ID, Title: "任务", CoordinatorID: member.ID, AgentIDs: []string{member.ID}, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveAgent(member); err != nil {
			return err
		}
		if err := tx.SaveMember(w.WorkspaceMember{WorkspaceID: ws.ID, AgentID: member.ID, Enabled: true, Tools: member.Tools, Revision: 1}); err != nil {
			return err
		}
		return tx.SaveThread(th)
	}); err != nil {
		t.Fatal(err)
	}
	service := project.New(db)
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: ws.ID, ThreadID: th.ID, OwnerMemberID: member.ID,
		Goal:            "Write the external report the user explicitly approved",
		AllowedActions:  []string{"read", "write", "run_test", "execute", "network", "collaborate", "publish_artifact"},
		SourceMessageID: "user-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "journey-run", WorkspaceID: ws.ID, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: created.Task.CurrentRevision, BudgetAccountID: created.Budget.ID,
		Input:  w.RunInput{Message: "Write the external report", Workflow: "free", Targets: []string{member.ID}},
		Agents: []w.Agent{member}, CoordinatorID: member.ID, Status: "running", CreatedAt: w.Now(),
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		live, err := tx.Workspace(ws.ID)
		if err != nil {
			return err
		}
		run.Workspace = live
		if err := service.EnsureRunAuthorization(tx, &run, "user-message"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		return tx.InsertRun(run, "journey-key", "journey-digest")
	}); err != nil {
		t.Fatal(err)
	}
	step := w.Step{ID: "journey-step", RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: "responder", Instruction: "write", Status: "running"}
	attempt := w.Attempt{ID: "journey-attempt", RunID: run.ID, StepID: step.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	j := &hostApprovalJourney{db: db, service: service, run: run, step: step, attempt: attempt, root: root, outside: outside}
	j.engine = &Engine{Store: db, AllowedRoots: []string{root}, AllowedModels: []string{"test"}}
	j.runtime = &stepRuntime{ctx: t.Context(), engine: j.engine, run: run, step: step, agent: member, attempt: attempt,
		allowed: []string{"read_file", "write_file", "run_test"}, nativeCaptured: map[string]bool{}}
	return j
}

// queueDecision registers the next click. A background loop answers pending
// requests in order, exactly like a user clicking through queued prompts.
func (j *hostApprovalJourney) queueDecision(decision string) {
	j.mu.Lock()
	j.queue = append(j.queue, decision)
	j.mu.Unlock()
}

// startClicker runs the browser-side loop: whenever a request is pending and a
// click is queued, it settles that one request with the queued decision.
func (j *hostApprovalJourney) startClicker(t *testing.T) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			j.mu.Lock()
			queued := len(j.queue)
			j.mu.Unlock()
			if queued == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			var pending []w.ToolPermissionRequest
			_ = j.db.Read(context.Background(), func(tx *store.Tx) error {
				var err error
				pending, err = tx.PendingPreApprovalsOrRequests(j.run.ThreadID)
				return err
			})
			if len(pending) == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			request := pending[0]
			j.mu.Lock()
			decision := j.queue[0]
			j.mu.Unlock()
			if _, err := j.service.DecidePermissionRequest(context.Background(), request.ID, request.Revision, project.DecisionInput{
				Decision: decision, RequestID: "journey-decision-" + request.ID[:8],
			}); err == nil {
				j.mu.Lock()
				j.queue = j.queue[1:]
				j.decisions = append(j.decisions, decision)
				j.mu.Unlock()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

func (j *hostApprovalJourney) requestArgs(t *testing.T, path string) json.RawMessage {
	t.Helper()
	arguments, err := json.Marshal(map[string]any{"path": path, "content": "approved payload"})
	if err != nil {
		t.Fatal(err)
	}
	return arguments
}

func (j *hostApprovalJourney) callRequest(t *testing.T, path string) string {
	t.Helper()
	return j.callRequestWithID(t, "call-request-"+w.NewID()[:8], path, map[string]any{"path": path, "content": "approved payload"})
}

func (j *hostApprovalJourney) callWrite(t *testing.T, callID, path string) (string, error) {
	t.Helper()
	return j.callWriteArgs(t, callID, path, map[string]any{"path": path, "content": "approved payload"})
}

func (j *hostApprovalJourney) callWriteArgs(t *testing.T, callID, path string, args map[string]any) (string, error) {
	t.Helper()
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return j.runtime.executeTool(agent.WithToolCall(t.Context(), callID), "write_file", arguments)
}

// callRequestArgs asks for an operation with arbitrary arguments.
func (j *hostApprovalJourney) callRequestArgs(t *testing.T, path string, args map[string]any) string {
	return j.callRequestWithID(t, "call-request-"+w.NewID()[:8], path, args)
}

// callRequestWithID asks for an operation under an explicit request call id.
func (j *hostApprovalJourney) callRequestWithID(t *testing.T, callID, path string, args map[string]any) string {
	t.Helper()
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	in, err := json.Marshal(map[string]any{
		"action": "write", "tool_name": "write_file", "scope": path,
		"arguments": json.RawMessage(arguments), "reason": "write the external report",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := j.runtime.executeTool(agent.WithToolCall(t.Context(), callID), "request_tool_access", in)
	if err != nil {
		t.Fatalf("request_tool_access failed: %v", err)
	}
	return out
}

// TestHostApprovalOnceProducesExactlyOneEffect is the end-to-end regression for
// the Codex finding: one approval, one effect, on the real follow-up call.
func TestHostApprovalOnceProducesExactlyOneEffect(t *testing.T) {
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	j.queueDecision(w.DecisionAllowOnce)
	result := j.callRequest(t, j.outside)
	if !strings.Contains(result, `"decision":"allowed_once"`) && !strings.Contains(result, `"decision":"allowed_conversation"`) {
		t.Fatalf("request was not approved: %s", result)
	}
	if _, err := j.callWrite(t, "call-real-1", j.outside); err != nil {
		t.Fatalf("approved write did not execute: %v", err)
	}
	if data, err := readJourneyFile(j.outside); err != nil || data != "approved payload" {
		t.Fatalf("approved effect missing: %q %v", data, err)
	}
	// The second call with the same arguments must request again, not reuse.
	j.queueDecision(w.DecisionDeny)
	if _, err := j.callWrite(t, "call-real-2", j.outside); err == nil {
		t.Fatal("a consumed one-time approval authorized a second effect")
	}
	if data, _ := readJourneyFile(j.outside); data != "approved payload" {
		t.Fatalf("denied second call changed the file: %q", data)
	}
}

// TestHostApprovalDenyProducesNoEffect proves a denial never executes.
func TestHostApprovalDenyProducesNoEffect(t *testing.T) {
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	j.queueDecision(w.DecisionDeny)
	result := j.callRequest(t, j.outside)
	if !strings.Contains(result, `"decision":"denied"`) {
		t.Fatalf("denial was not reported: %s", result)
	}
	// The real call is a new, distinct operation: it asks again and is denied.
	j.queueDecision(w.DecisionDeny)
	if _, err := j.callWrite(t, "call-real-1", j.outside); err == nil {
		t.Fatal("a denied request still executed the effect")
	}
	if _, err := readJourneyFile(j.outside); err == nil {
		t.Fatal("denied write created the file")
	}
}

// TestHostApprovalDoesNotCoverADifferentOperation proves the approval is bound to
// the exact arguments and target: a different path is a fresh request.
func TestHostApprovalDoesNotCoverADifferentOperation(t *testing.T) {
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	j.queueDecision(w.DecisionAllowOnce)
	j.callRequest(t, j.outside)
	other := filepath.Join(filepath.Dir(j.outside), "other.txt")
	j.queueDecision(w.DecisionDeny)
	if _, err := j.callWrite(t, "call-real-1", other); err == nil {
		t.Fatal("an approval for one path authorized a different path")
	}
	// The approved target itself was never written by the different call.
	if _, err := readJourneyFile(j.outside); err == nil {
		t.Fatal("the approved path was written by a different operation")
	}
}

// TestHostApprovalConversationGrantIsToolScoped proves a conversation grant is
// scoped to the approved tool and can admit a later call from the same thread.
func TestHostApprovalConversationGrantIsToolScoped(t *testing.T) {
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	j.queueDecision(w.DecisionAllowConversation)
	j.callRequest(t, j.outside)
	if _, err := j.callWrite(t, "call-real-1", j.outside); err != nil {
		t.Fatalf("conversation grant did not admit the real call: %v", err)
	}
	// Another path is still outside the approved scope.
	j.queueDecision(w.DecisionDeny)
	other := filepath.Join(filepath.Dir(j.outside), "second.txt")
	if _, err := j.callWrite(t, "call-real-2", other); err == nil {
		t.Fatal("conversation grant admitted an unapproved scope")
	}
}

// TestHostToolCallIdentityIsPerInvocation proves two concurrent calls do not
// share call identity or admission state.
func TestHostToolCallIdentityIsPerInvocation(t *testing.T) {
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	first := j.runtime.currentCallID(context.Background())
	second := j.runtime.currentCallID(context.Background())
	if first == second {
		t.Fatalf("two calls shared the identity %q", first)
	}
	j.queueDecision(w.DecisionAllowOnce)
	j.callRequest(t, j.outside)
	if _, err := j.callWrite(t, "call-real-1", j.outside); err != nil {
		t.Fatal(err)
	}
	// The single approval is spent: the next call must request again, and this
	// click denies it. Only the first effect may exist.
	j.queueDecision(w.DecisionDeny)
	if _, err := j.callWrite(t, "call-real-2", j.outside); err == nil {
		t.Fatal("a second call reused the same one-time approval")
	}
}

// TestHostAdmissionSettlementRecordsFailure proves a failed effect is not recorded
// as completed, so it cannot be presented as a successful execution.
func TestHostAdmissionSettlementRecordsFailure(t *testing.T) {
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	j.queueDecision(w.DecisionAllowOnce)
	j.callRequest(t, j.outside)
	if _, err := j.callWrite(t, "call-real-1", j.outside); err != nil {
		t.Fatal(err)
	}
	var admissions []w.ToolInvocationAdmission
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		admissions, err = tx.AdmissionsByRun(j.run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(admissions) != 1 || admissions[0].Status != w.AdmissionCompleted {
		t.Fatalf("successful effect settlement = %+v", admissions)
	}
	j.queueDecision(w.DecisionAllowOnce)
	j.callRequest(t, j.outside)
	// The same path now exists, so a different body without expected_sha256 is
	// rejected by write_file; the consumed allowance must settle as failed.
	j.queueDecision(w.DecisionAllowOnce)
	j.callRequestArgs(t, j.outside, map[string]any{"path": j.outside, "content": "different payload"})
	if _, err := j.callWriteArgs(t, "call-real-2", j.outside, map[string]any{"path": j.outside, "content": "different payload"}); err == nil {
		t.Fatal("a stale write unexpectedly succeeded")
	}
	var latest []w.ToolInvocationAdmission
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		latest, err = tx.AdmissionsByRun(j.run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("failed call did not record an admission: %+v", latest)
	}
	for _, admission := range latest {
		if admission.ID == admissions[0].ID {
			continue
		}
		if admission.Status == w.AdmissionCompleted {
			t.Fatalf("failed effect settled as completed: %+v", admission)
		}
	}
}

// TestHostRequestToolIsDiscoverable keeps the request path in the visible catalog
// so a denied effect is not an unexplained failure.
func TestHostRequestToolIsDiscoverable(t *testing.T) {
	j := newHostApprovalJourney(t)
	request, err := j.engine.buildRequest(t.Context(), j.run, j.step, j.run.Agents[0], []string{"read_file", "write_file"}, "", agent.ConnectionInfo{}, agent.AdapterInfo{}, w.Message{}, Handoff{}, nil, nil, nil, nil, nil, j.attempt)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range request.Tools {
		if tool.Name == "request_tool_access" {
			found = true
		}
	}
	if !found {
		t.Fatal("request_tool_access is not visible to the member")
	}
}

func readJourneyFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// TestIntakeChatToolsStayReadOnlyWithoutApproval is the intake edge: the first
// call in a new conversation runs under an intentionally read-only intake mode
// while the global policy is project_autonomous. Ordinary read/web tools must run
// without a task and without creating a permission request.
func TestIntakeChatToolsStayReadOnlyWithoutApproval(t *testing.T) {
	j := newHostApprovalJourney(t)
	// Turn the fixture run into an intake run: no task, requested mode is the live
	// global policy, effective mode is the deliberate read-only projection.
	run := j.run
	run.TaskID, run.TaskRevision, run.TaskIntake = "", 0, true
	run.RequestedExecutionMode = w.ExecutionProjectAutonomous
	run.EffectiveExecutionMode = w.ExecutionControlled
	// Intake's ceiling is the read-only catalog the runtime authorization
	// already froze for it.
	run.GrantActions = []string{"read", "network", "collaborate", "publish_artifact"}
	if err := j.db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveRun(run) }); err != nil {
		t.Fatal(err)
	}
	j.runtime.run = run
	in := json.RawMessage(`{"path":"notes.txt"}`)
	decision, err := j.service.CheckToolAccess(t.Context(), project.AccessRequest{
		Run: run, StepID: j.step.ID, Attempt: j.attempt.ID, CallID: "intake-read",
		Name: "read_file", Arguments: in, Scope: w.NormalizeScope(j.root, filepath.Join(j.root, "notes.txt")),
	})
	if err != nil {
		t.Fatalf("intake read was rejected with an error: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("intake read required an approval: %+v", decision)
	}
	var pending []w.ToolPermissionRequest
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		pending, err = tx.PendingPreApprovalsOrRequests(j.run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("intake read created a permission request: %+v", pending)
	}
	// A write in intake must not run: the intake projection is read-only.
	write, err := j.service.CheckToolAccess(t.Context(), project.AccessRequest{
		Run: run, StepID: j.step.ID, Attempt: j.attempt.ID, CallID: "intake-write",
		Name: "write_file", Arguments: json.RawMessage(`{"path":"x","content":"y"}`), Scope: w.NormalizeScope(j.root, j.root),
	})
	if err != nil {
		t.Fatalf("intake write returned an unexpected error: %v", err)
	}
	if write.Allowed {
		t.Fatal("intake write was admitted")
	}
}

// TestControlledApprovedCommandReallyRuns proves A06 for commands: an in-project
// command needs a decision in controlled mode, a denial produces no marker, and
// one allow_once runs exactly that command once under the sandbox.
func TestControlledApprovedCommandReallyRuns(t *testing.T) {
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	mode := w.ExecutionControlled
	var current w.Settings
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.service.UpdateSettings(t.Context(), current.Revision, project.SettingsPatch{ExecutionMode: &mode}); err != nil {
		t.Fatal(err)
	}
	// The live epoch advanced, so this run must be re-authorized for the test.
	if err := j.db.Write(t.Context(), func(tx *store.Tx) error {
		live, err := tx.Run(j.run.ID)
		if err != nil {
			return err
		}
		if err := j.service.EnsureRunAuthorization(tx, &live, "controlled-command"); err != nil {
			return err
		}
		j.run = live
		j.runtime.run = live
		return tx.SaveRun(live)
	}); err != nil {
		t.Fatal(err)
	}
	j.runtime.allowed = w.StandardToolCatalog()
	marker := filepath.Join(j.root, "command-marker.txt")
	command := func() json.RawMessage {
		argv := []any{"/usr/bin/touch", marker}
		raw, err := json.Marshal(map[string]any{"argv": argv, "cwd": ".", "timeout_seconds": 20})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// Denial: no marker, no effect.
	j.queueDecision(w.DecisionDeny)
	if _, err := j.runtime.executeTool(agent.WithToolCall(t.Context(), "cmd-deny"), "run_command", command()); err == nil {
		t.Fatal("a denied command executed")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("denied command produced its marker")
	}
	// Approval: exactly one real execution.
	j.queueDecision(w.DecisionAllowOnce)
	if _, err := j.runtime.executeTool(agent.WithToolCall(t.Context(), "cmd-allow"), "run_command", command()); err != nil {
		t.Fatalf("approved command failed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("approved command produced no marker: %v", err)
	}
	// The one-time approval is spent: a second identical command asks again and is
	// denied, leaving no duplicate effect.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	j.queueDecision(w.DecisionDeny)
	if _, err := j.runtime.executeTool(agent.WithToolCall(t.Context(), "cmd-replay"), "run_command", command()); err == nil {
		t.Fatal("a second command reused the one-time approval")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("replayed command produced a second effect")
	}
}

// TestApprovedOutsideReadReturnsRealBytesOnce proves the approved-scope executor
// path: the file tools really read the approved outside target, and a second read
// needs its own decision.
func TestApprovedOutsideReadReturnsRealBytesOnce(t *testing.T) {
	j := newHostApprovalJourney(t)
	j.startClicker(t)
	const content = "OUTSIDE_READ_APPROVED_BYTES"
	if err := os.WriteFile(j.outside, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	read := func(callID string) (string, error) {
		raw, err := json.Marshal(map[string]any{"path": j.outside})
		if err != nil {
			t.Fatal(err)
		}
		return j.runtime.executeTool(agent.WithToolCall(t.Context(), callID), "read_file", raw)
	}
	// Denial: the real bytes must not leak.
	j.queueDecision(w.DecisionDeny)
	if output, err := read("read-deny"); err == nil || strings.Contains(output, content) {
		t.Fatalf("denied outside read returned bytes: %q %v", output, err)
	}
	// Approval once: the actual content is returned exactly once.
	j.queueDecision(w.DecisionAllowOnce)
	output, err := read("read-allow")
	if err != nil {
		t.Fatalf("approved outside read failed: %v", err)
	}
	if !strings.Contains(output, content) {
		t.Fatalf("approved outside read did not return real bytes: %q", output)
	}
	// Second read needs a fresh decision.
	j.queueDecision(w.DecisionDeny)
	if second, err := read("read-replay"); err == nil || strings.Contains(second, content) {
		t.Fatalf("a consumed read approval was reused: %q %v", second, err)
	}
}
