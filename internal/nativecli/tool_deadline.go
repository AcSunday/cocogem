package nativecli

import (
	"context"
	"sync"
	"time"
)

// pausableDeadlineKey carries the pausable execution deadline for one host tool
// call. Permission waiting is not execution: while a human decision is pending
// the timer is stopped, and a fresh execution window starts when the wait ends.
type pausableDeadlineKey struct{}

type pausableDeadline struct {
	parent   context.Context
	duration time.Duration

	mu       sync.Mutex
	deadline time.Time
	timer    *time.Timer
	waiters  int
	stopped  bool
	err      error
	done     chan struct{}
	once     sync.Once
}

func newPausableDeadline(parent context.Context, duration time.Duration) *pausableDeadline {
	deadline := &pausableDeadline{parent: parent, duration: duration, done: make(chan struct{})}
	deadline.deadline = time.Now().Add(duration)
	deadline.timer = time.AfterFunc(duration, func() {
		deadline.mu.Lock()
		paused := deadline.waiters > 0
		deadline.mu.Unlock()
		if !paused {
			deadline.expire(context.DeadlineExceeded)
		}
	})
	go func() {
		select {
		case <-parent.Done():
			deadline.expire(parent.Err())
		case <-deadline.done:
		}
	}()
	return deadline
}

func (d *pausableDeadline) expire(err error) {
	d.mu.Lock()
	if d.err == nil {
		d.err = err
	}
	d.mu.Unlock()
	d.once.Do(func() { close(d.done) })
}

func (d *pausableDeadline) Deadline() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deadline.IsZero() {
		return time.Time{}, false
	}
	return d.deadline, true
}

func (d *pausableDeadline) Done() <-chan struct{} { return d.done }

func (d *pausableDeadline) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func (d *pausableDeadline) Value(key any) any { return d.parent.Value(key) }

func (d *pausableDeadline) pause() func() {
	d.mu.Lock()
	d.waiters++
	if d.timer != nil {
		d.timer.Stop()
	}
	d.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			d.mu.Lock()
			d.waiters--
			if d.waiters == 0 && !d.stopped && d.err == nil {
				d.deadline = time.Now().Add(d.duration)
				if d.timer != nil {
					d.timer.Reset(d.duration)
				}
			}
			d.mu.Unlock()
		})
	}
}

func (d *pausableDeadline) stop() {
	d.mu.Lock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
	d.mu.Unlock()
}

// PauseToolDeadline stops the current host tool's execution timeout while a
// human permission decision is pending. The returned function restarts the
// execution window; it must only be called by the executing host path.
func PauseToolDeadline(ctx context.Context) func() {
	deadline, _ := ctx.Value(pausableDeadlineKey{}).(*pausableDeadline)
	if deadline == nil {
		return func() {}
	}
	return deadline.pause()
}
