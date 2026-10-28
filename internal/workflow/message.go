package workflow

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// messageWriter is the only writer of one streamed assistant message. It
// keeps a bounded preview and immutable 8 KiB chunks; the complete body never
// lives in memory or in the message row.
type messageWriter struct {
	store     *store.Store
	ctx       context.Context
	runID     string
	stepID    string
	agentID   string
	attemptID string
	interval  time.Duration
	onFailure func(error)
	failOnce  sync.Once
	closeOnce sync.Once
	closeErr  error

	mu            sync.Mutex
	message       w.Message
	seq           int
	pending       []byte
	progress      bool
	total         int
	preview       []byte
	previewClosed bool
	err           error
	stopping      bool
	stop          chan struct{}
	done          chan struct{}
}

func newMessageWriter(s *store.Store, ctx context.Context, interval time.Duration, runID, stepID, agentID, attemptID string, message w.Message, onFailure func(error)) *messageWriter {
	writer := &messageWriter{store: s, ctx: ctx, interval: interval, runID: runID, stepID: stepID, agentID: agentID, attemptID: attemptID, message: message, onFailure: onFailure, stop: make(chan struct{}), done: make(chan struct{})}
	go writer.tick()
	return writer
}

func (m *messageWriter) tick() {
	defer close(m.done)
	if m.interval <= 0 {
		<-m.stop
		return
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			if err := m.flushDue(); err != nil {
				return
			}
		}
	}
}

// Append records new visible text and flushes whole chunks as they fill.
func (m *messageWriter) Append(text string) error { return m.append(text, true) }

// AppendDuplicate records a frame the adapter reported as non-progress: the
// text is persisted exactly, but it does not refresh the attempt's liveness.
func (m *messageWriter) AppendDuplicate(text string) error { return m.append(text, false) }

func (m *messageWriter) append(text string, progress bool) error {
	if text == "" {
		return nil
	}
	if !utf8.ValidString(text) {
		return errors.New("message text must be valid UTF-8")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.stopping {
		return nil
	}
	if progress {
		m.progress = true
	}
	for len(text) > 0 {
		room := m.MaxChunk() - len(m.pending)
		if room <= 0 {
			if err := m.flushLocked(m.MaxChunk()); err != nil {
				return err
			}
			continue
		}
		piece := text
		if len(piece) > room {
			piece = trimBytes(text, room)
		}
		if piece == "" {
			// The next rune does not fit the remaining room; publish the
			// partial chunk first and continue on a fresh one.
			if err := m.flushLocked(0); err != nil {
				return err
			}
			if len(m.pending) > 0 {
				return errors.New("next UTF-8 character does not fit the chunk budget")
			}
			continue
		}
		text = text[len(piece):]
		m.pending = append(m.pending, piece...)
		if len(m.pending) >= m.MaxChunk() {
			if err := m.flushLocked(m.MaxChunk()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *messageWriter) MaxChunk() int { return w.MaxMessageChunkBytes }

func (m *messageWriter) MaxPreviewBytes() int { return w.MaxMessagePreviewBytes }

func trimBytes(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

// extendPreview appends only a true prefix of the committed body. Once a rune
// does not fit the remaining preview budget the preview is closed; later bytes
// can never skip the missing rune and pretend to be a prefix.
func extendPreview(preview []byte, closed bool, maxBytes int, chunk []byte) ([]byte, bool) {
	if closed {
		return preview, true
	}
	remaining := maxBytes - len(preview)
	if remaining <= 0 {
		return preview, true
	}
	if len(chunk) <= remaining {
		return append(preview, chunk...), false
	}
	piece := trimBytes(string(chunk), remaining)
	if piece == "" {
		return preview, true
	}
	return append(preview, piece...), true
}

// flushDue publishes the bounded tail on the 100ms timer.
func (m *messageWriter) flushDue() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil || len(m.pending) == 0 {
		return m.err
	}
	return m.flushLocked(0)
}

// flushLocked writes complete chunks (when maxChunk > 0) and the current
// bounded snapshot in one transaction, so row, chunks and event agree.
// Totals and preview advance only after that transaction commits.
func (m *messageWriter) flushLocked(maxChunk int) error {
	if m.err != nil {
		return m.err
	}
	pending := m.pending
	chunks := [][]byte{}
	if maxChunk > 0 {
		for len(pending) >= maxChunk {
			chunk := trimBytes(string(pending), maxChunk)
			if chunk == "" {
				return errors.New("next UTF-8 character does not fit the chunk budget")
			}
			chunks = append(chunks, []byte(chunk))
			pending = pending[len(chunk):]
		}
	} else if len(pending) > 0 {
		chunk := trimBytes(string(pending), m.MaxChunk())
		if len(chunk) > 0 {
			chunks = append(chunks, []byte(chunk))
			pending = pending[len(chunk):]
		}
	}
	total, preview, closed := m.total, m.preview, m.previewClosed
	for _, chunk := range chunks {
		total += len(chunk)
		preview, closed = extendPreview(preview, closed, m.MaxPreviewBytes(), chunk)
	}
	message := m.message
	message.Content = string(preview)
	message.ContentFormat = m.ContentFormat()
	message.ContentBytes = total
	message.ContentTruncated = total > len(preview)
	progress := m.progress
	ctx, cancel := context.WithTimeout(context.WithoutCancel(m.ctx), 5*time.Second)
	defer cancel()
	err := m.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		for _, chunk := range chunks {
			m.seq++
			if err := tx.AppendMessageChunk(message.ID, m.seq, string(chunk)); err != nil {
				return err
			}
		}
		if progress {
			if err := touchAttempt(tx, m.attemptID, m.now()); err != nil {
				return err
			}
		}
		return tx.AppendEvent(m.runID, m.stepID, m.agentID, "message_delta", map[string]any{"message_id": message.ID, "mode": "snapshot", "activity": progress, "message": message})
	})
	if err != nil {
		m.err = err
		m.failOnce.Do(func() {
			if m.onFailure != nil {
				m.onFailure(err)
			}
		})
		return err
	}
	m.pending = pending
	m.total, m.preview, m.previewClosed = total, preview, closed
	m.progress = false
	return nil
}

func (m *messageWriter) now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (m *messageWriter) snapshotLocked() w.Message {
	message := m.message
	message.Content = string(m.preview)
	message.ContentFormat = m.ContentFormat()
	message.ContentBytes = m.total
	message.ContentTruncated = m.total > len(m.preview)
	return message
}

func (m *messageWriter) ContentFormat() string { return w.ContentFormatChunksV1 }

// Snapshot returns the current bounded message projection of committed bytes.
func (m *messageWriter) Snapshot() w.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// SnapshotError reports a persistence failure that must stop the runner.
func (m *messageWriter) SnapshotError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Close stops the timer, flushes the partial tail and joins the timer
// goroutine, so no stale tick can write after the attempt is finalized.
// Concurrent and repeated calls observe the same terminal result.
func (m *messageWriter) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.stopping = true
		close(m.stop)
		m.mu.Unlock()
		<-m.done
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.err != nil {
			m.closeErr = m.err
			return
		}
		if len(m.pending) > 0 {
			m.closeErr = m.flushLocked(0)
		}
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeErr
}

// touchAttempt is the only liveness write on the execution record. A genuine
// activity signal clears the persisted silence warning.
func touchAttempt(tx *store.Tx, id, at string) error {
	if id == "" {
		return nil
	}
	attempt, err := tx.Attempt(id)
	if err != nil {
		return err
	}
	attempt.LastProgress = at
	attempt.LivenessWarning = nil
	return tx.SaveAttempt(attempt)
}
