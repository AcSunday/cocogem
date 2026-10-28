package nativecli

import (
	"context"
	"sync"
	"time"

	"github.com/AcSunday/cocogem/internal/agent"
)

// hostToolDeadline bounds one ordinary host tool call. run_test has its own
// bounded host timeout so a real test run is not cut off after 20 seconds.
const hostToolDeadline = 20 * time.Second

const testToolDeadline = 10 * time.Minute

func toolDeadline(name string) time.Duration {
	if name == "run_test" {
		return testToolDeadline
	}
	return hostToolDeadline
}

// liveness separates first-response waiting, tool activity and true silence.
// A parsed zero disables that individual check; defaults belong to config.
type liveness struct {
	ctx     context.Context
	cancel  context.CancelFunc
	observe agent.Observe
	first   time.Duration
	stall   time.Duration
	action  string
	started time.Time
	now     func() time.Time

	mu           sync.Mutex
	processLive  bool
	firstSeen    bool
	lastProgress time.Time
	lastType     string
	toolSince    time.Time
	toolLimit    time.Duration
	tools        int
	warned       map[string]bool
	err          error
	done         chan struct{}
	stopCh       chan struct{}
	stopOnce     sync.Once
}

func newLiveness(ctx context.Context, cancel context.CancelFunc, observe agent.Observe, first, stall time.Duration, action string) *liveness {
	if action != "stop" {
		action = "warn"
	}
	now := time.Now()
	return &liveness{ctx: ctx, cancel: cancel, observe: observe, first: first, stall: stall, action: action, started: now, lastProgress: now, warned: map[string]bool{}, done: make(chan struct{}), stopCh: make(chan struct{})}
}

func (l *liveness) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *liveness) start() {
	// Two disabled checks need no timer at all.
	if l.first <= 0 && l.stall <= 0 {
		close(l.done)
		return
	}
	go func() {
		defer close(l.done)
		var interval time.Duration
		for _, limit := range []time.Duration{l.first, l.stall} {
			if limit > 0 && (interval == 0 || limit < interval) {
				interval = limit
			}
		}
		interval /= 4
		if interval < 20*time.Millisecond {
			interval = 20 * time.Millisecond
		}
		if interval > time.Second {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-l.ctx.Done():
				return
			case <-l.stopCh:
				return
			case <-ticker.C:
				if !l.check() {
					return
				}
			}
		}
	}()
}

func (l *liveness) stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
	<-l.done
}

// dispatched starts the first-response clock at the real process launch, not
// while work directories and CLI arguments are still being prepared.
func (l *liveness) dispatched() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	l.processLive = true
	l.started = now
	l.lastProgress = now
	delete(l.warned, "waiting_first_response")
}

// valid records activity only after an adapter has validated the protocol item.
func (l *liveness) valid(kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.progressLocked(kind)
}

func (l *liveness) progressLocked(kind string) {
	l.firstSeen = true
	l.lastProgress = l.clock()
	l.lastType = kind
	delete(l.warned, "stalled")
	delete(l.warned, "waiting_tool")
}

// firstResponse is kept for tests and adapters whose valid activity has no
// finer kind. Production parsers call valid with the protocol item type.
func (l *liveness) firstResponse() { l.valid("response") }

// notice records a protocol extension without treating heartbeat noise as progress.
func (l *liveness) notice(eventType string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastType = eventType
}

func (l *liveness) toolStarted(limit ...time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if l.tools == 0 {
		l.toolSince = now
		l.toolLimit = hostToolDeadline
		if len(limit) > 0 && limit[0] > 0 {
			l.toolLimit = limit[0]
		}
	}
	l.tools++
	l.progressLocked("tool")
}

func (l *liveness) toolFinished() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tools > 0 {
		l.tools--
	}
	if l.tools == 0 {
		l.toolSince = time.Time{}
		l.toolLimit = 0
	}
	l.progressLocked("tool")
}

func (l *liveness) failure() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *liveness) check() bool {
	l.mu.Lock()
	if !l.processLive {
		l.mu.Unlock()
		return true
	}
	now := l.clock()
	if l.first > 0 && !l.firstSeen {
		elapsed := now.Sub(l.started)
		if elapsed >= l.first {
			l.err = newFailure(FailureFirstResponseTimeout, "native CLI produced no validated response within %s (last event: %s)", l.first, safeToken(l.lastType))
			l.mu.Unlock()
			l.cancel()
			return false
		}
		if elapsed >= l.first/2 && !l.warned["waiting_first_response"] {
			l.warned["waiting_first_response"] = true
			last := safeToken(l.lastType)
			l.mu.Unlock()
			_ = l.observe("liveness_warning", map[string]any{"state": "waiting_first_response", "silence_seconds": int(elapsed.Seconds()), "last_event_type": last, "process_alive": true, "tool_active": false})
			return true
		}
		l.mu.Unlock()
		return true
	}
	if l.stall <= 0 {
		l.mu.Unlock()
		return true
	}
	// An in-flight tool gets its own bounded window; after that the watchdog
	// is allowed to judge the silence normally.
	limit := l.toolLimit
	if limit <= 0 {
		limit = hostToolDeadline
	}
	if l.tools > 0 && now.Sub(l.toolSince) < limit {
		if now.Sub(l.lastProgress) >= l.stall/2 && !l.warned["waiting_tool"] {
			l.warned["waiting_tool"] = true
			silence, last := now.Sub(l.lastProgress), safeToken(l.lastType)
			l.mu.Unlock()
			_ = l.observe("liveness_warning", map[string]any{"state": "waiting_tool", "silence_seconds": int(silence.Seconds()), "last_event_type": last, "process_alive": true, "tool_active": true})
			return true
		}
		l.mu.Unlock()
		return true
	}
	if now.Sub(l.lastProgress) < l.stall {
		l.mu.Unlock()
		return true
	}
	if l.action == "stop" {
		l.err = newFailure(FailureStallTimeout, "native CLI made no validated progress for %s (last event: %s)", l.stall, safeToken(l.lastType))
		l.mu.Unlock()
		l.cancel()
		return false
	}
	if !l.warned["stalled"] {
		l.warned["stalled"] = true
		state, silence, last := "stalled", now.Sub(l.lastProgress), safeToken(l.lastType)
		l.mu.Unlock()
		_ = l.observe("liveness_warning", map[string]any{"state": state, "silence_seconds": int(silence.Seconds()), "last_event_type": last, "process_alive": true, "tool_active": false})
		return true
	}
	l.mu.Unlock()
	return true
}
