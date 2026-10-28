package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func seedWorkflowRows(t *testing.T, status string) (*Store, w.Run) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ws := w.Workspace{ID: w.NewID(), Name: "store", Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: thread.ID, Status: status, CreatedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		return tx.InsertRun(run, w.NewID(), "store-test")
	}); err != nil {
		t.Fatal(err)
	}
	return s, run
}

func TestMessageMetaAndDigestStreamInlineBody(t *testing.T) {
	s, run := seedWorkflowRows(t, "interrupted")
	text := strings.Repeat("旧格式正文", 5000)
	message := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, Status: "completed", Content: text, CreatedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveMessage(message) }); err != nil {
		t.Fatal(err)
	}
	var meta w.Message
	var digest string
	var total int
	var format string
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var err error
		meta, err = tx.MessageMeta(message.ID)
		if err != nil {
			return err
		}
		digest, total, format, err = tx.MessageDigest(message.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if meta.Content != "" || meta.ContentBytes != len(text) || meta.Format() != w.ContentFormatInline {
		t.Fatalf("metadata loaded the body or lost the length: %+v", meta)
	}
	want := sha256.Sum256([]byte(text))
	if digest != hex.EncodeToString(want[:]) || total != len(text) || format != w.ContentFormatInline {
		t.Fatalf("legacy digest is not the content digest: %s %d %s", digest, total, format)
	}
}

func TestMessageWindowRejectsOutsidePositions(t *testing.T) {
	s, run := seedWorkflowRows(t, "interrupted")
	chunked := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, Status: "completed", ContentFormat: w.ContentFormatChunksV1, Content: "abcdef", ContentBytes: 10, CreatedAt: w.Now()}
	legacy := w.Message{ID: w.NewID(), ThreadID: run.ThreadID, RunID: run.ID, Status: "completed", Content: strings.Repeat("x", 100), CreatedAt: w.Now()}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		if err := tx.SaveMessage(chunked); err != nil {
			return err
		}
		if err := tx.SaveMessage(legacy); err != nil {
			return err
		}
		if err := tx.AppendMessageChunk(chunked.ID, 1, "abcdef"); err != nil {
			return err
		}
		return tx.AppendMessageChunk(chunked.ID, 2, "ghij")
	}); err != nil {
		t.Fatal(err)
	}
	var first, second string
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var nextSeq, nextOffset, absolute int
		var more bool
		var err error
		first, nextSeq, nextOffset, absolute, more, err = tx.MessageWindow(chunked.ID, 1, 0, 4)
		if err != nil {
			return err
		}
		if first != "abcd" || nextSeq != 1 || nextOffset != 4 || absolute != 0 || !more {
			t.Fatalf("first chunked page is wrong: %q %d %d %d %v", first, nextSeq, nextOffset, absolute, more)
		}
		second, _, _, _, more, err = tx.MessageWindow(chunked.ID, nextSeq, nextOffset, 8)
		if err != nil {
			return err
		}
		if second != "efghij" || more {
			t.Fatalf("second chunked page is wrong: %q %v", second, more)
		}
		if _, _, _, _, _, err := tx.MessageWindow(chunked.ID, 9, 0, 8); err == nil {
			return errors.New("a nonexistent chunk sequence returned success")
		}
		if _, _, _, _, _, err := tx.MessageWindow(chunked.ID, 2, 999, 8); err == nil {
			return errors.New("an oversized chunk offset returned success")
		}
		if _, _, _, _, _, err := tx.MessageWindow(legacy.ID, 1, 500, 8); err == nil {
			return errors.New("an oversized inline offset returned success")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionRunIsNarrowInterruptedCAS(t *testing.T) {
	s, run := seedWorkflowRows(t, "interrupted")
	run.Status, run.FailureKind, run.Error = "failed", "run_timeout", "expired"
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.TransitionRun("interrupted", run) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.TransitionRun("interrupted", run) }); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("second transition was not a conflict: %v", err)
	}
	var got w.Run
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var err error
		got, err = tx.Run(run.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.FailureKind != "run_timeout" {
		t.Fatalf("narrow transition lost the failed outcome: %+v", got)
	}
	got.Status = "running"
	if err := s.Write(t.Context(), func(tx *Tx) error { return tx.SaveRun(got) }); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("SaveRun no longer protects terminal states: %v", err)
	}
}
