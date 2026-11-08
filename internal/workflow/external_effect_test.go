package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/testutil"
	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// mockEgress records exactly which external operations were forwarded. It never
// touches the public network: the functional journey proves the approval gate,
// not an integration with a real service.
type mockEgress struct {
	mu      sync.Mutex
	flights []tools.ExternalRequest
	err     error
}

func (m *mockEgress) Forward(_ context.Context, request tools.ExternalRequest) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return 0, m.err
	}
	m.flights = append(m.flights, request)
	return 201, nil
}

func (m *mockEgress) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.flights)
}

// externalJourney seeds a controlled-mode free task (the strictest default) with
// one member, a running step, and the given egress integration.
func externalJourney(t *testing.T, egress tools.Egress) (*hostApprovalJourney, *stepRuntime, *mockEgress) {
	t.Helper()
	db, service, th, member, root := newFreeTaskFixture(t)
	mode := w.ExecutionControlled
	var current w.Settings
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		current, err = tx.Settings()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSettings(t.Context(), current.Revision, project.SettingsPatch{ExecutionMode: &mode}); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateTask(t.Context(), project.CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ThreadID: th.ID, OwnerMemberID: member.ID,
		Goal: "发布外部版本", SourceMessageID: "external-publish", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{
		ID: "external-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID,
		TaskRevision: created.Task.CurrentRevision, BudgetAccountID: created.Budget.ID,
		Input:  w.RunInput{Message: "发布外部版本", Workflow: "free", Targets: []string{member.ID}},
		Agents: []w.Agent{member}, CoordinatorID: member.ID, Status: "running", CreatedAt: w.Now(),
	}
	step := w.Step{ID: "external-step", RunID: run.ID, AgentID: member.ID, AgentName: member.DisplayName, Role: "responder", Status: "running"}
	attempt := w.Attempt{ID: "external-attempt", RunID: run.ID, StepID: step.ID, Number: 1, Status: "running", StartedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		live, err := tx.Workspace(th.WorkspaceID)
		if err != nil {
			return err
		}
		run.Workspace = live
		if err := service.EnsureRunAuthorization(tx, &run, "external-publish"); err != nil {
			return err
		}
		if err := tx.ReserveTaskRun(&run); err != nil {
			return err
		}
		if err := tx.TaskRunStarted(&run); err != nil {
			return err
		}
		if err := tx.InsertRun(run, "external-key", "external-digest"); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Store: db, AllowedRoots: []string{root}, AllowedModels: []string{"test"}, Web: testutil.Web{}, Egress: egress}
	// validateMember requires a runner for an executor profile. Use a runner that
	// never runs here: the journey calls executeTool directly.
	runner := &testRunner{run: func(context.Context, model.Request, int, agent.Execute, agent.Observe) error { return nil }}
	engine.Runners = map[string]agent.Runner{"claude": runner}
	engine.Fallback = runner
	runtime := &stepRuntime{ctx: t.Context(), engine: engine, run: run, step: step, agent: member, attempt: attempt,
		allowed: w.StandardToolCatalog(), nativeCaptured: map[string]bool{}}
	j := &hostApprovalJourney{db: db, service: service, run: run, step: step, attempt: attempt, root: root}
	return j, runtime, egress.(*mockEgress)
}

func externalPayload(t *testing.T, url, body string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"kind": "publish", "method": "POST", "url": url, "body": body})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestExternalPublishWaitsForApprovalAndRunsOnce is the P2 external-effect
// journey: the outbound request is held before forwarding, a denial forwards
// nothing, and one allow_once forwards exactly one identical request.
func TestExternalPublishWaitsForApprovalAndRunsOnce(t *testing.T) {
	egress := &mockEgress{}
	j, runtime, mock := externalJourney(t, egress)
	j.startClicker(t)
	payload := externalPayload(t, "https://deploy.example.test/v1/release", `{"version":"1.2.3"}`)

	// Deny: no external effect.
	j.queueDecision(w.DecisionDeny)
	if _, err := runtime.executeTool(agent.WithToolCall(t.Context(), "ext-deny"), "external_publish", payload); err == nil {
		t.Fatal("a denied external publish still forwarded")
	}
	if mock.count() != 0 {
		t.Fatalf("denied external publish forwarded %d requests", mock.count())
	}

	// Allow once: exactly one forwarded request, with the exact method/target/body.
	j.queueDecision(w.DecisionAllowOnce)
	output, err := runtime.executeTool(agent.WithToolCall(t.Context(), "ext-allow"), "external_publish", payload)
	if err != nil {
		t.Fatalf("approved external publish failed: %v", err)
	}
	if mock.count() != 1 {
		t.Fatalf("approved external publish forwarded %d requests, want 1", mock.count())
	}
	flight := mock.flights[0]
	if flight.Method != "POST" || flight.URL != "https://deploy.example.test/v1/release" || flight.Body != `{"version":"1.2.3"}` {
		t.Fatalf("forwarded request = %+v", flight)
	}
	wantDigest, _ := tools.DecodeExternalRequest(payload)
	if flight.Digest != wantDigest.Digest {
		t.Fatalf("forwarded digest = %q, want %q", flight.Digest, wantDigest.Digest)
	}
	if !json.Valid([]byte(output)) || !jsonContainsString(output, "forwarded") {
		t.Fatalf("receipt = %s", output)
	}
	// The one-time approval is spent: the same operation asks again and is denied.
	j.queueDecision(w.DecisionDeny)
	if _, err := runtime.executeTool(agent.WithToolCall(t.Context(), "ext-replay"), "external_publish", payload); err == nil {
		t.Fatal("a second external publish reused the one-time approval")
	}
	if mock.count() != 1 {
		t.Fatalf("second external publish forwarded again: %d", mock.count())
	}
}

// TestExternalPublishDifferentBodyNeedsNewApproval proves the approval binds the
// exact bytes, not only the destination.
func TestExternalPublishDifferentBodyNeedsNewApproval(t *testing.T) {
	egress := &mockEgress{}
	j, runtime, mock := externalJourney(t, egress)
	j.startClicker(t)
	first := externalPayload(t, "https://deploy.example.test/v1/release", `{"version":"1.0.0"}`)
	j.queueDecision(w.DecisionAllowOnce)
	if _, err := runtime.executeTool(agent.WithToolCall(t.Context(), "ext-a"), "external_publish", first); err != nil {
		t.Fatal(err)
	}
	second := externalPayload(t, "https://deploy.example.test/v1/release", `{"version":"2.0.0"}`)
	j.queueDecision(w.DecisionDeny)
	if _, err := runtime.executeTool(agent.WithToolCall(t.Context(), "ext-b"), "external_publish", second); err == nil {
		t.Fatal("a different body was forwarded under the earlier approval")
	}
	if mock.count() != 1 {
		t.Fatalf("different body forwarded: %d", mock.count())
	}
}

// TestExternalOperationSummaryIsReviewable proves the pending request itself
// carries a bounded command/destination description, so the UI can show what is
// about to happen without raw logs.
func TestExternalOperationSummaryIsReviewable(t *testing.T) {
	egress := &mockEgress{}
	j, runtime, _ := externalJourney(t, egress)
	payload := externalPayload(t, "https://deploy.example.test/v1/release", `{"version":"3.1.4"}`)
	// Deny after the request is created, then inspect the persisted request.
	j.startClicker(t)
	j.queueDecision(w.DecisionDeny)
	if _, err := runtime.executeTool(agent.WithToolCall(t.Context(), "ext-summary"), "external_publish", payload); err == nil {
		t.Fatal("denied publish returned no error")
	}
	var pending []w.ToolPermissionRequest
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		pending, err = tx.PendingPreApprovalsOrRequests(j.run.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Settled requests are not pending; read the settled row directly.
	var settled w.ToolPermissionRequest
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		items, err := tx.PermissionRequestsByRunCall(j.run.ID, "ext-summary")
		if err != nil {
			return err
		}
		if len(items) != 1 {
			return errors.New("expected exactly one request for the call")
		}
		settled = items[0]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if settled.Status != w.PermissionDenied {
		t.Fatalf("request status = %s", settled.Status)
	}
	if settled.OperationSummary == "" || !jsonContainsString(settled.OperationSummary, "publish") || !jsonContainsString(settled.OperationSummary, "deploy.example.test") {
		t.Fatalf("operation summary = %q", settled.OperationSummary)
	}
	if len(settled.OperationSummary) > 500 {
		t.Fatalf("operation summary is not bounded: %d bytes", len(settled.OperationSummary))
	}
	_ = pending
}

// TestPublicNetworkReadNeedsNoApproval keeps the distinction: ordinary public
// research reads stay autonomous and never create a request.
func TestPublicNetworkReadNeedsNoApproval(t *testing.T) {
	egress := &mockEgress{}
	j, runtime, _ := externalJourney(t, egress)
	_ = j
	// Use the permission service directly for the read decision.
	service := project.New(runtime.engine.Store)
	access, err := service.CheckToolAccess(t.Context(), project.AccessRequest{
		Run: runtime.run, StepID: runtime.step.ID, Attempt: runtime.attempt.ID,
		CallID: "read-call", Name: "fetch_url",
		Arguments: json.RawMessage(`{"url":"https://example.org/article"}`), Scope: "network:public",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !access.Allowed {
		t.Fatalf("public network read required approval: %+v", access)
	}
	// And no request was created for it.
	if err := j.db.Read(t.Context(), func(tx *store.Tx) error {
		items, err := tx.PermissionRequestsByRunCall(runtime.run.ID, "read-call")
		if err != nil {
			return err
		}
		if len(items) != 0 {
			t.Fatalf("public read created a request: %+v", items)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func jsonContainsString(value, needle string) bool {
	return len(value) >= len(needle) && stringContains(value, needle)
}

func stringContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
