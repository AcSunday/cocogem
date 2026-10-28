package workflow

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func newTestWriter(t *testing.T, s *store.Store, run w.Run) *messageWriter {
	t.Helper()
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, AgentID: run.Agents[0].ID, AgentName: run.Agents[0].DisplayName, Role: "assistant", Status: "processing", ContentFormat: w.ContentFormatChunksV1, CreatedAt: w.Now()}
	return newMessageWriter(s, t.Context(), time.Hour, run.ID, steps[0].ID, run.Agents[0].ID, "", message, nil)
}

func storedChunkBytes(t *testing.T, s *store.Store, id string) (w.Message, int) {
	t.Helper()
	var message w.Message
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		var err error
		message, err = tx.Message(id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := readWholeMessage(t, s, id)
	return message, len(body)
}

func TestMessageWriterMetadataTracksCommittedChunksOnly(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "stream"}})
	writer := newTestWriter(t, s, run)
	// One byte more than a chunk: the whole-chunk flush commits 8192 bytes,
	// while the last byte stays pending and must not be claimed yet.
	if err := writer.Append(strings.Repeat("x", w.MaxMessageChunkBytes+1)); err != nil {
		t.Fatal(err)
	}
	snapshot := writer.Snapshot()
	if snapshot.ContentBytes != w.MaxMessageChunkBytes || len(snapshot.Content) != w.MaxMessageChunkBytes {
		t.Fatalf("snapshot claimed an uncommitted tail: bytes=%d preview=%d", snapshot.ContentBytes, len(snapshot.Content))
	}
	message, chunks := storedChunkBytes(t, s, snapshot.ID)
	if message.ContentBytes != snapshot.ContentBytes || message.ContentBytes != chunks {
		t.Fatalf("metadata and chunks disagree before the timer: metadata=%d chunks=%d snapshot=%d", message.ContentBytes, chunks, snapshot.ContentBytes)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	closed := writer.Snapshot()
	if closed.ContentBytes != w.MaxMessageChunkBytes+1 {
		t.Fatalf("terminal snapshot lost the committed tail: %d", closed.ContentBytes)
	}
	message, chunks = storedChunkBytes(t, s, snapshot.ID)
	if message.ContentBytes != w.MaxMessageChunkBytes+1 || chunks != w.MaxMessageChunkBytes+1 {
		t.Fatalf("terminal flush left gaps: metadata=%d chunks=%d", message.ContentBytes, chunks)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close was not idempotent: %v", err)
	}
}

func TestMessageWriterRejectsInvalidUTF8AndStopsPreviewAtRuneBoundary(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "stream"}})
	writer := newTestWriter(t, s, run)
	if err := writer.Append(string([]byte{0xff, 0xfe})); err == nil {
		t.Fatal("invalid UTF-8 input was accepted")
	}
	if err := writer.Append("valid"); err != nil {
		t.Fatalf("writer was poisoned by a rejected frame: %v", err)
	}
	text := strings.Repeat("证", 30000) + "abc" // 90003 bytes
	if err := writer.Append(text); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- writer.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for closeErr := range errs {
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	message, chunks := storedChunkBytes(t, s, writer.Snapshot().ID)
	if chunks != len("valid")+len(text) {
		t.Fatalf("chunked body lost bytes: %d", chunks)
	}
	if message.ContentBytes != len("valid")+len(text) {
		t.Fatalf("content bytes lost bytes: %d", message.ContentBytes)
	}
	if !utf8.ValidString(message.Content) || len(message.Content) > w.MaxMessagePreviewBytes {
		t.Fatalf("preview is invalid or unbounded: %d", len(message.Content))
	}
	if !strings.HasPrefix("valid"+text, message.Content) {
		t.Fatal("preview is not a prefix after a rune crossed the preview budget")
	}
	if len(message.Content) < w.MaxMessagePreviewBytes-utf8.UTFMax {
		t.Fatalf("preview stopped too early at %d of %d", len(message.Content), w.MaxMessagePreviewBytes)
	}
}

func TestMessageWriterSnapshotActivityMatchesPersistedProgress(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "stream"}})
	steps, err := loadSteps(t, s, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := "2000-01-01T00:00:00Z"
	attempt := w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: steps[0].ID, Number: 1, Status: "running", StartedAt: old, LastProgress: old}
	if err := s.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveAttempt(attempt) }); err != nil {
		t.Fatal(err)
	}
	write := func(duplicate, real bool) {
		message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: steps[0].ID, AgentID: run.Agents[0].ID, Role: "assistant", Status: "processing", ContentFormat: w.ContentFormatChunksV1, CreatedAt: w.Now()}
		writer := newMessageWriter(s, t.Context(), time.Hour, run.ID, steps[0].ID, run.Agents[0].ID, attempt.ID, message, nil)
		if duplicate {
			if err := writer.AppendDuplicate("quiet text"); err != nil {
				t.Fatal(err)
			}
		}
		if real {
			if err := writer.Append("real text"); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	lastActivity := func() (bool, bool) {
		var activity *bool
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			events, err := tx.Events(run.ID, 0)
			if err != nil {
				return err
			}
			for _, event := range events {
				if event.Type != "message_delta" {
					continue
				}
				var data struct {
					Activity *bool `json:"activity"`
				}
				if err := json.Unmarshal(event.Data, &data); err != nil {
					return err
				}
				activity = data.Activity
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if activity == nil {
			return false, false
		}
		return *activity, true
	}
	progress := func() string {
		var stored w.Attempt
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			stored, err = tx.Attempt(attempt.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return stored.LastProgress
	}
	write(true, false)
	if activity, ok := lastActivity(); !ok || activity {
		t.Fatalf("non-progress snapshot activity=%v present=%v", activity, ok)
	}
	if got := progress(); got != old {
		t.Fatalf("non-progress snapshot refreshed LastProgress: %q", got)
	}
	// One blank and one real text in the same flush must count as progress.
	write(true, true)
	if activity, ok := lastActivity(); !ok || !activity {
		t.Fatalf("mixed snapshot activity=%v present=%v", activity, ok)
	}
	if got := progress(); got == old {
		t.Fatal("progress snapshot did not persist LastProgress")
	}
}

func TestMessageWriterDoesNotCountNonProgressText(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "stream"}})
	writer := newTestWriter(t, s, run)
	if err := writer.AppendDuplicate("quiet text"); err != nil {
		t.Fatal(err)
	}
	if progress := writer.progress; progress {
		t.Fatal("non-progress text claimed liveness")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, chunks := storedChunkBytes(t, s, writer.Snapshot().ID)
	if chunks != len("quiet text") {
		t.Fatalf("non-progress text was not persisted: %d", chunks)
	}
}
