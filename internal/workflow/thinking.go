package workflow

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/store"
)

const (
	maxThinkingBytesPerAttempt = 64 << 10
	maxThinkingChunkBytes      = 4096
)

// thinkingWriter coalesces streamed chain-of-thought deltas into bounded
// thinking_delta events while allowing synchronous flushes right before tool
// calls and visible text so event.seq preserves exact chronological order.
type thinkingWriter struct {
	store     *store.Store
	ctx       context.Context
	runID     string
	stepID    string
	agentID   string
	attemptID string
	interval  time.Duration
	closeOnce sync.Once

	mu            sync.Mutex
	blockID       string
	pending       []byte
	progress      bool
	total         int
	truncated     bool
	truncatedSent bool
	stopping      bool
	stop          chan struct{}
	done          chan struct{}
}

func newThinkingWriter(s *store.Store, ctx context.Context, interval time.Duration, runID, stepID, agentID, attemptID string) *thinkingWriter {
	w := &thinkingWriter{
		store:     s,
		ctx:       ctx,
		interval:  interval,
		runID:     runID,
		stepID:    stepID,
		agentID:   agentID,
		attemptID: attemptID,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go w.tick()
	return w
}

func (tw *thinkingWriter) tick() {
	defer close(tw.done)
	if tw.interval <= 0 {
		<-tw.stop
		return
	}
	ticker := time.NewTicker(tw.interval)
	defer ticker.Stop()
	for {
		select {
		case <-tw.stop:
			return
		case <-ticker.C:
			_ = tw.Flush()
		}
	}
}

// Append buffers one thinking text delta. Distinct block IDs flush the prior
// block first so separate reasoning phases stay cleanly ordered.
func (tw *thinkingWriter) Append(blockID, text string) error {
	if text == "" {
		return nil
	}
	if !utf8.ValidString(text) {
		text = string([]rune(text))
	}
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.stopping || tw.truncated {
		return nil
	}
	if blockID != "" && tw.blockID != "" && blockID != tw.blockID && len(tw.pending) > 0 {
		if err := tw.flushLocked(); err != nil {
			return err
		}
	}
	if blockID != "" {
		tw.blockID = blockID
	}
	remaining := maxThinkingBytesPerAttempt - tw.total - len(tw.pending)
	if remaining <= 0 {
		tw.truncated = true
		if len(tw.pending) > 0 || !tw.truncatedSent {
			return tw.flushLocked()
		}
		return nil
	}
	if len(text) > remaining {
		text = trimBytes(text, remaining)
		tw.truncated = true
	}
	if text != "" {
		tw.pending = append(tw.pending, text...)
		tw.progress = true
	}
	if len(tw.pending) >= maxThinkingChunkBytes || (tw.truncated && !tw.truncatedSent) {
		return tw.flushLocked()
	}
	return nil
}

// Flush immediately persists any buffered thinking text so subsequent tool or
// message events always observe a strictly larger event.seq.
func (tw *thinkingWriter) Flush() error {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.pending) == 0 && !(tw.truncated && !tw.truncatedSent) {
		return nil
	}
	return tw.flushLocked()
}

func (tw *thinkingWriter) flushLocked() error {
	if len(tw.pending) == 0 && !(tw.truncated && !tw.truncatedSent) {
		return nil
	}
	chunk := string(tw.pending)
	blockID := tw.blockID
	truncated := tw.truncated
	progress := tw.progress
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tw.ctx), 5*time.Second)
	defer cancel()
	payload := map[string]any{
		"attempt_id": tw.attemptID,
		"text":       chunk,
	}
	if blockID != "" {
		payload["block_id"] = blockID
	}
	if truncated {
		payload["truncated"] = true
	}
	err := tw.store.Write(ctx, func(tx *store.Tx) error {
		if progress {
			if err := touchAttempt(tx, tw.attemptID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		return tx.AppendEvent(tw.runID, tw.stepID, tw.agentID, "thinking_delta", payload)
	})
	if err != nil {
		return err
	}
	tw.total += len(tw.pending)
	tw.pending = tw.pending[:0]
	tw.progress = false
	if truncated {
		tw.truncatedSent = true
	}
	return nil
}

// Close stops the ticker and flushes any trailing thinking bytes.
func (tw *thinkingWriter) Close() error {
	var closeErr error
	tw.closeOnce.Do(func() {
		tw.mu.Lock()
		tw.stopping = true
		close(tw.stop)
		tw.mu.Unlock()
		<-tw.done
		tw.mu.Lock()
		defer tw.mu.Unlock()
		if len(tw.pending) > 0 || (tw.truncated && !tw.truncatedSent) {
			closeErr = tw.flushLocked()
		}
	})
	return closeErr
}
