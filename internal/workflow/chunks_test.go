package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AcSunday/cocogem/internal/agent"
	"github.com/AcSunday/cocogem/internal/model"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func readWholeMessage(t *testing.T, s *store.Store, id string) (string, store.MessageContentPage) {
	t.Helper()
	var built strings.Builder
	cursor := ""
	var first store.MessageContentPage
	for {
		var page store.MessageContentPage
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			page, err = tx.MessageContent(id, cursor, store.MaxContentChunks)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if cursor == "" {
			first = page
		}
		built.WriteString(page.Content)
		if !page.HasMore {
			return built.String(), first
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatal("content paging did not advance")
		}
		cursor = page.NextCursor
	}
}

func TestChunkedStreamPreservesLargeUTF8AndSnapshots(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "stream"}})
	payload := strings.Repeat("猫", 90000) // 270000 UTF-8 bytes
	var stepID string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil {
			stepID = steps[0].ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		piece := 900 // bytes per frame
		for offset := 0; offset < len(payload); offset += piece {
			end := min(offset+piece, len(payload))
			if err := observe("text", map[string]any{"text": payload[offset:end]}); err != nil {
				return err
			}
		}
		// The 100ms timer must publish a bounded snapshot before completion.
		if err := observe("text", map[string]any{"text": "tail-before-wait"}); err != nil {
			return err
		}
		time.Sleep(250 * time.Millisecond)
		if err := s.Read(ctx, func(tx *store.Tx) error {
			messages, err := tx.MessagesMetadata(run.ThreadID)
			if err != nil {
				return err
			}
			for _, message := range messages {
				if message.StepID == stepID && message.ContentBytes > 0 {
					return nil
				}
			}
			return errors.New("100ms flush did not persist visible progress")
		}); err != nil {
			return err
		}
		return nil
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("streamed run failed: %+v", got)
	}
	full := payload + "tail-before-wait"
	var stored w.Message
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		messages, err := tx.Messages(run.ThreadID)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.StepID == stepID && message.Role == "assistant" {
				stored = message
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if stored.ID == "" || stored.Status != "completed" || stored.Format() != w.ContentFormatChunksV1 {
		t.Fatalf("message was not a completed chunked message: %+v", stored)
	}
	if stored.ContentBytes != len(full) || !stored.ContentTruncated {
		t.Fatalf("content metadata lost byte length/truncation: bytes=%d want=%d truncated=%v", stored.ContentBytes, len(full), stored.ContentTruncated)
	}
	if len(stored.Content) > w.MaxMessagePreviewBytes || !utf8.ValidString(stored.Content) {
		t.Fatalf("preview is unbounded or invalid: %d", len(stored.Content))
	}
	if !strings.HasPrefix(full, stored.Content) {
		limit := min(len(full), len(stored.Content))
		at := 0
		for at < limit && full[at] == stored.Content[at] {
			at++
		}
		t.Fatalf("preview is not a prefix: preview=%d full=%d first-diff=%d full-around=%q preview-around=%q", len(stored.Content), len(full), at, full[max(0, at-8):min(len(full), at+16)], stored.Content[max(0, at-8):min(len(stored.Content), at+16)])
	}
	reconstructed, page := readWholeMessage(t, s, stored.ID)
	if reconstructed != full {
		t.Fatalf("chunked body did not round-trip: got=%d want=%d", len(reconstructed), len(full))
	}
	if page.ContentFormat != w.ContentFormatChunksV1 || page.ContentBytes != len(full) {
		t.Fatalf("paged metadata disagrees: %+v", page)
	}
	var deltas int
	lastSeq := int64(0)
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		events, err := tx.Events(run.ID, 0)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Seq <= lastSeq {
				return errors.New("event replay sequence is not strictly increasing")
			}
			lastSeq = event.Seq
			if event.Type != "message_delta" {
				continue
			}
			deltas++
			var data struct {
				MessageID string    `json:"message_id"`
				Mode      string    `json:"mode"`
				Message   w.Message `json:"message"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return err
			}
			if data.MessageID != stored.ID || data.Mode != "snapshot" {
				return errors.New("message_delta is not a bounded snapshot")
			}
			if len(data.Message.Content) > w.MaxMessagePreviewBytes || data.Message.ContentBytes > len(full) {
				return errors.New("snapshot carried an unbounded body")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if deltas == 0 {
		t.Fatal("streaming produced no snapshot events")
	}
}

func TestChunkWriterFlushesPartialBodyOnFailure(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "fail"}})
	partial := "部分输出-" + strings.Repeat("尾", 5000)
	runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		if err := observe("text", map[string]any{"text": partial}); err != nil {
			return err
		}
		return errors.New("synthetic terminal failure")
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "failed" {
		t.Fatalf("runner error was hidden: %+v", got)
	}
	var stored w.Message
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		messages, err := tx.Messages(run.ThreadID)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.Role == "assistant" {
				stored = message
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" {
		t.Fatalf("partial message did not settle: %+v", stored)
	}
	reconstructed, _ := readWholeMessage(t, s, stored.ID)
	if reconstructed != partial {
		t.Fatalf("partial terminal flush was lost: got=%d want=%d", len(reconstructed), len(partial))
	}
}

func TestChunkStorageFailureStopsRunnerWithoutSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "disk"}})
	fault, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close()
	if _, err := fault.Exec(`CREATE TRIGGER fail_chunk_insert BEFORE INSERT ON message_chunks BEGIN SELECT RAISE(FAIL,'synthetic chunk failure'); END`); err != nil {
		t.Fatal(err)
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		for range 4 {
			if err := observe("text", map[string]any{"text": strings.Repeat("x", 4096)}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("storage failure did not stop the runner")
		}
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}, Errors: make(chan error, 8)}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "failed" {
		t.Fatalf("chunk write failure was reported as %+v", got)
	}
	select {
	case <-e.Errors:
	default:
		t.Fatal("storage failure was not reported")
	}
}

func TestLastProgressIgnoresProtocolNoiseAndDuplicateFrames(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "liveness"}})
	var stepID string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil {
			stepID = steps[0].ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	readProgress := func(ctx context.Context) string {
		var value string
		if err := s.Read(ctx, func(tx *store.Tx) error {
			attempts, err := tx.StepAttempts(stepID)
			if err != nil {
				return err
			}
			value = attempts[len(attempts)-1].LastProgress
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return value
	}
	runner := nativeRunnerFunc(func(ctx context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
		base := readProgress(ctx)
		time.Sleep(20 * time.Millisecond)
		for _, noise := range []struct {
			kind string
			data map[string]any
		}{
			{"native_adapter", map[string]any{"version": "1", "program": "p", "identity": "i", "adapter": "a"}},
			{"native_session", map[string]any{"session_id": "noise-session", "mode": "fresh"}},
			{"native_process", map[string]any{"pid": 1, "pgid": 1, "start": "x"}},
			{"usage", map[string]any{"known": false, "input_tokens": 0, "output_tokens": 0}},
			{"liveness_warning", map[string]any{"state": "stalled"}},
			{"error", map[string]any{"message": "noise"}},
		} {
			if err := observe(noise.kind, noise.data); err != nil {
				return err
			}
		}
		if err := observe("text", map[string]any{"text": ""}); err != nil {
			return err
		}
		if got := readProgress(ctx); got != base {
			return errors.New("protocol noise refreshed last_progress")
		}
		if err := observe("text", map[string]any{"text": "first"}); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
		first := readProgress(ctx)
		if first == base {
			return errors.New("real text did not refresh last_progress")
		}
		// A repeated frame is still persisted, but the adapter explicitly marks
		// it non-progress; core no longer guesses duplicates from text equality.
		if err := observe("text", map[string]any{"text": "first", "activity": false}); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
		if got := readProgress(ctx); got != first {
			return errors.New("non-progress frame refreshed last_progress")
		}
		if err := observe("text", map[string]any{"text": "second"}); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
		second := readProgress(ctx)
		if second == first {
			return errors.New("new text did not refresh last_progress")
		}
		if err := observe("tool_started", map[string]any{"tool_call_id": "t1", "name": "read_source"}); err != nil {
			return err
		}
		tool := readProgress(ctx)
		if tool == second {
			return errors.New("tool activity did not refresh last_progress")
		}
		return nil
	})
	e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
	cancel, wait := startEngine(t, &e)
	defer wait()
	got := awaitRun(t, s, run.ID)
	_ = cancel
	if got.Status != "succeeded" {
		t.Fatalf("liveness probe failed: %+v", got)
	}
}

func TestUsageKnownSurvivesFailureAndUnknownStaysUnknown(t *testing.T) {
	for _, tc := range []struct {
		name          string
		known         bool
		input, output int
	}{{"known failure", true, 123, 45}, {"unknown failure", false, 0, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "usage"}})
			runner := nativeRunnerFunc(func(_ context.Context, _ model.Request, _ agent.Execute, observe agent.Observe) error {
				if err := observe("usage", map[string]any{"known": tc.known, "input_tokens": tc.input, "output_tokens": tc.output}); err != nil {
					return err
				}
				return errors.New("synthetic failure after usage")
			})
			e := Engine{Store: s, Runners: map[string]agent.Runner{"claude": runner}}
			cancel, wait := startEngine(t, &e)
			defer wait()
			got := awaitRun(t, s, run.ID)
			_ = cancel
			if got.Status != "failed" {
				t.Fatalf("run=%+v", got)
			}
			attempts := loadAttempts(t, s, run.ID)
			if len(attempts) != 1 {
				t.Fatalf("attempts=%+v", attempts)
			}
			attempt := attempts[0]
			if attempt.UsageKnown != tc.known {
				t.Fatalf("usage known flag changed: %+v", attempt)
			}
			if tc.known && (attempt.InputTokens != 123 || attempt.OutputTokens != 45) {
				t.Fatalf("known usage was lost on cleanup: %+v", attempt)
			}
			if !tc.known && (attempt.InputTokens != 0 || attempt.OutputTokens != 0) {
				t.Fatalf("unknown usage was replaced by fake zeros: %+v", attempt)
			}
		})
	}
}

func TestMessageWindowPagesLargeChunks(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := seedCustomRun(t, s, []customStepSpec{{Agent: 0, Role: "responder", Instruction: "window"}})
	var stepID string
	if err := s.Read(t.Context(), func(tx *store.Tx) error {
		steps, err := tx.Steps(run.ID)
		if err == nil {
			stepID = steps[0].ID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("窗口", 70000) // 420000 bytes
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, StepID: stepID, Role: "assistant", Status: "completed", ContentFormat: w.ContentFormatChunksV1, ContentBytes: len(text), CreatedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		seq := 0
		for offset := 0; offset < len(text); {
			end := min(offset+w.MaxMessageChunkBytes, len(text))
			for end > offset && end < len(text) && !utf8.RuneStart(text[end]) {
				end--
			}
			seq++
			if err := tx.AppendMessageChunk(message.ID, seq, text[offset:end]); err != nil {
				return err
			}
			offset = end
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var built strings.Builder
	seq, offset := 1, 0
	for page := 0; page < 200; page++ {
		var content string
		var nextSeq, nextOffset, absolute int
		var more bool
		if err := s.Read(t.Context(), func(tx *store.Tx) error {
			var err error
			content, nextSeq, nextOffset, absolute, more, err = tx.MessageWindow(message.ID, seq, offset, w.MaxTextPageBytes)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(content) > w.MaxTextPageBytes || absolute != built.Len() {
			t.Fatalf("window is not bounded or not contiguous: len=%d offset=%d want=%d", len(content), absolute, built.Len())
		}
		built.WriteString(content)
		if !more {
			if built.String() != text {
				t.Fatalf("window paging did not round-trip: got=%d want=%d", built.Len(), len(text))
			}
			return
		}
		seq, offset = nextSeq, nextOffset
	}
	t.Fatal("window paging did not terminate")
}
