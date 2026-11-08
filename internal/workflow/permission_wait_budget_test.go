package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// shortToolBudget is the test counterpart of the native bridge's pausable
// per-call budget: the timer cancels the tool-call context, a human wait stops
// it, and resuming starts a fresh execution window. It is deliberately faithful
// so a missing pause in the host path makes the wait expire exactly as the real
// 20-second budget would.
type shortToolBudget struct {
	ctx      context.Context
	cancel   context.CancelFunc
	duration time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	waiters int
	stopped bool
}

func newShortToolBudget(t *testing.T, duration time.Duration) *shortToolBudget {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	budget := &shortToolBudget{ctx: ctx, cancel: cancel, duration: duration}
	budget.timer = time.AfterFunc(duration, budget.fire)
	t.Cleanup(budget.stop)
	return budget
}

func (b *shortToolBudget) fire() {
	b.mu.Lock()
	paused := b.waiters > 0
	b.mu.Unlock()
	if !paused {
		b.cancel()
	}
}

// pause stops the execution window and returns the matching resume. This is the
// exact contract of nativecli.PauseToolDeadline.
func (b *shortToolBudget) pause(context.Context) func() {
	b.mu.Lock()
	b.waiters++
	b.timer.Stop()
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.waiters--
			if b.waiters == 0 && !b.stopped {
				b.timer.Reset(b.duration)
			}
		})
	}
}

func (b *shortToolBudget) stop() {
	b.mu.Lock()
	b.stopped = true
	b.timer.Stop()
	b.mu.Unlock()
	b.cancel()
}

// decideOldestAfter answers the first pending request only after delay. That
// guarantees the human wait is longer than the short execution budget.
func decideOldestAfter(j *hostApprovalJourney, delay time.Duration) <-chan error {
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			var pending []w.ToolPermissionRequest
			_ = j.db.Read(context.Background(), func(tx *store.Tx) error {
				var err error
				pending, err = tx.PendingPreApprovalsOrRequests(j.run.ThreadID)
				return err
			})
			if len(pending) == 0 {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			request := pending[0]
			time.Sleep(delay)
			_, err := j.service.DecidePermissionRequest(context.Background(), request.ID, request.Revision, project.DecisionInput{
				Decision: w.DecisionAllowOnce, RequestID: "budget-decision-" + request.ID[:8],
			})
			done <- err
			return
		}
		done <- errors.New("no pending permission request appeared")
	}()
	return done
}

// TestHumanPermissionWaitOutlivesTheHostToolDeadline proves the contract at the
// real effect boundary: the human decision takes far longer than the executing
// tool's budget, the wait is paused, and the approved write still completes in
// the restarted window. Removing the pause makes this call fail with a canceled
// context and no file (see the control below).
func TestHumanPermissionWaitOutlivesTheHostToolDeadline(t *testing.T) {
	j := newHostApprovalJourney(t)
	budget := newShortToolBudget(t, 400*time.Millisecond)
	j.runtime.pauseBudget = budget.pause
	decided := decideOldestAfter(j, time.Second)

	arguments, err := json.Marshal(map[string]any{"path": j.outside, "content": "approved payload"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.runtime.executeTool(agent.WithToolCall(budget.ctx, "deadline-write"), "write_file", arguments); err != nil {
		t.Fatalf("a human wait longer than the tool budget ended the call: %v", err)
	}
	if err := <-decided; err != nil {
		t.Fatalf("decision after the tool budget: %v", err)
	}
	if data, err := os.ReadFile(j.outside); err != nil || string(data) != "approved payload" {
		t.Fatalf("approved effect missing after the wait: %q %v", data, err)
	}
}

// TestRequestToolAccessWaitOutlivesTheHostToolDeadline covers the discoverable
// request_tool_access wait path: the receipt is returned after a decision that
// exceeds the request call's own budget.
func TestRequestToolAccessWaitOutlivesTheHostToolDeadline(t *testing.T) {
	j := newHostApprovalJourney(t)
	budget := newShortToolBudget(t, 400*time.Millisecond)
	j.runtime.pauseBudget = budget.pause
	decided := decideOldestAfter(j, time.Second)

	arguments, err := json.Marshal(map[string]any{"path": j.outside, "content": "approved payload"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := json.Marshal(map[string]any{
		"action": "write", "tool_name": "write_file", "scope": j.outside,
		"arguments": json.RawMessage(arguments), "reason": "write the external report",
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := j.runtime.executeTool(agent.WithToolCall(budget.ctx, "deadline-request"), "request_tool_access", in)
	if err != nil {
		t.Fatalf("a human wait longer than the request budget ended the call: %v", err)
	}
	if !strings.Contains(output, `"decision":"allowed_once"`) {
		t.Fatalf("request was not approved after the wait: %s", output)
	}
	if err := <-decided; err != nil {
		t.Fatalf("decision after the tool budget: %v", err)
	}
}

// TestNativePermissionWaitOutlivesTheHostToolDeadline covers the native
// authorization callback: an outside Write waits beyond the executing call's
// budget and is still approved with the normalized resource scope.
func TestNativePermissionWaitOutlivesTheHostToolDeadline(t *testing.T) {
	j := newHostApprovalJourney(t)
	budget := newShortToolBudget(t, 400*time.Millisecond)
	j.runtime.pauseBudget = budget.pause
	decided := decideOldestAfter(j, time.Second)

	input, err := json.Marshal(map[string]any{"file_path": j.outside, "content": "native payload"})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := j.runtime.nativeAuthorizer()(budget.ctx, model.NativePermissionRequest{
		CallID: "deadline-native", Name: "Write", Input: input, Resource: j.outside,
		Origin: w.OriginNative, Adapter: "fixture", Action: "write",
	})
	if err != nil {
		t.Fatalf("a human wait longer than the native call budget ended the callback: %v", err)
	}
	if !decision.Allow || decision.Scope != model.NativeScopeOnce || decision.ResourceScope == "" {
		t.Fatalf("native call was not approved after the wait: %+v", decision)
	}
	if err := <-decided; err != nil {
		t.Fatalf("decision after the tool budget: %v", err)
	}
}

// TestHumanPermissionWaitWithoutThePauseFailsClosed is the negative control: the
// same short budget without the host pause expires during the human wait, so the
// approved effect never happens. It proves the positive tests depend on the
// pause, not on a budget that never expires.
func TestHumanPermissionWaitWithoutThePauseFailsClosed(t *testing.T) {
	j := newHostApprovalJourney(t)
	budget := newShortToolBudget(t, 400*time.Millisecond)
	j.runtime.pauseBudget = func(context.Context) func() { return func() {} }
	decided := decideOldestAfter(j, time.Second)

	arguments, err := json.Marshal(map[string]any{"path": j.outside, "content": "approved payload"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.runtime.executeTool(agent.WithToolCall(budget.ctx, "deadline-unpaused"), "write_file", arguments); err == nil {
		t.Fatal("the call survived a budget that expired during the human wait")
	}
	<-decided
	if _, err := os.Stat(j.outside); err == nil {
		t.Fatal("an expired wait still produced the effect")
	}
}

// TestHumanPermissionWaitStillStopsOnCancellation proves the pause only stops
// the execution timer: the parent context still cancels the wait promptly and
// no effect is produced. A pause that detached the wait would hang here.
func TestHumanPermissionWaitStillStopsOnCancellation(t *testing.T) {
	j := newHostApprovalJourney(t)
	budget := newShortToolBudget(t, 5*time.Second)
	j.runtime.pauseBudget = budget.pause
	go func() {
		time.Sleep(100 * time.Millisecond)
		budget.cancel()
	}()

	arguments, err := json.Marshal(map[string]any{"path": j.outside, "content": "approved payload"})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := j.runtime.executeTool(agent.WithToolCall(budget.ctx, "deadline-cancel"), "write_file", arguments); err == nil {
		t.Fatal("a canceled wait admitted the effect")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %s while paused", elapsed)
	}
	if _, err := os.Stat(j.outside); err == nil {
		t.Fatal("a canceled wait produced the effect")
	}
}
