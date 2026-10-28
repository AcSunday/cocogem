package nativecli

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type observedEvents struct {
	mu     sync.Mutex
	events []string
	states []string
}

func (o *observedEvents) observe(kind string, data map[string]any) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, kind)
	if kind == "liveness_warning" {
		state, _ := data["state"].(string)
		o.states = append(o.states, state)
	}
	return nil
}

func (o *observedEvents) snapshot() ([]string, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string{}, o.events...), append([]string{}, o.states...)
}

func TestLivenessFirstResponseTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedEvents{}
	live := newLiveness(ctx, cancel, observed.observe, 40*time.Millisecond, time.Second, "warn")
	live.dispatched()
	live.start()
	defer live.stop()
	deadline := time.Now().Add(time.Second)
	for live.failure() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if live.failure() == nil || !strings.Contains(live.failure().Error(), "first_response_timeout") {
		t.Fatalf("first-response timeout missing: %v", live.failure())
	}
}

func TestLivenessStallWarnsWithoutKilling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedEvents{}
	live := newLiveness(ctx, cancel, observed.observe, time.Second, 40*time.Millisecond, "warn")
	live.firstResponse()
	live.dispatched()
	live.start()
	defer live.stop()
	deadline := time.Now().Add(time.Second)
	for len(observed.snapshotStates()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if live.failure() != nil {
		t.Fatalf("warn mode stopped the step: %v", live.failure())
	}
	_, states := observed.snapshot()
	if len(states) == 0 || states[0] != "stalled" {
		t.Fatalf("stall warning missing: %v", states)
	}
}

func TestLivenessToolActivityIsProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedEvents{}
	live := newLiveness(ctx, cancel, observed.observe, time.Second, 40*time.Millisecond, "warn")
	live.firstResponse()
	live.toolStarted()
	live.dispatched()
	live.start()
	defer live.stop()
	time.Sleep(120 * time.Millisecond)
	if live.failure() != nil {
		t.Fatalf("tool wait was treated as a stall: %v", live.failure())
	}
	_, states := observed.snapshot()
	foundNotice := false
	for _, state := range states {
		if state == "waiting_tool" {
			foundNotice = true
		}
		if state == "stalled" {
			t.Fatalf("tool activity produced a stall warning: %v", states)
		}
	}
	if !foundNotice {
		t.Fatalf("tool wait was not distinguished: %v", states)
	}
	live.toolFinished()
}

func TestLivenessStallStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedEvents{}
	live := newLiveness(ctx, cancel, observed.observe, time.Second, 40*time.Millisecond, "stop")
	live.firstResponse()
	live.dispatched()
	live.start()
	defer live.stop()
	deadline := time.Now().Add(time.Second)
	for live.failure() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if live.failure() == nil || !strings.Contains(live.failure().Error(), "stall_timeout") {
		t.Fatalf("stall stop missing: %v", live.failure())
	}
}

func TestLivenessZeroDisablesChecks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedEvents{}
	live := newLiveness(ctx, cancel, observed.observe, 0, 0, "stop")
	live.dispatched()
	live.start()
	time.Sleep(60 * time.Millisecond)
	live.stop()
	if live.failure() != nil {
		t.Fatalf("disabled liveness stopped the step: %v", live.failure())
	}
	if events, _ := observed.snapshot(); len(events) != 0 {
		t.Fatalf("disabled liveness emitted warnings: %v", events)
	}
}

func TestLivenessStallStillRunsWithFirstResponseDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &observedEvents{}
	live := newLiveness(ctx, cancel, observed.observe, 0, 40*time.Millisecond, "warn")
	live.dispatched()
	live.start()
	defer live.stop()
	deadline := time.Now().Add(time.Second)
	for len(observed.snapshotStates()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if states := observed.snapshotStates(); len(states) == 0 || states[0] != "stalled" {
		t.Fatalf("stall warning missing: %v", states)
	}
}

func (o *observedEvents) snapshotStates() []string {
	_, states := o.snapshot()
	return states
}

func TestRunTestHasBoundedLongHostDeadline(t *testing.T) {
	if toolDeadline("read_file") != hostToolDeadline {
		t.Fatal("ordinary host tool deadline changed")
	}
	if toolDeadline("run_test") <= hostToolDeadline || toolDeadline("run_test") > 10*time.Minute {
		t.Fatalf("run_test deadline = %s", toolDeadline("run_test"))
	}
}
