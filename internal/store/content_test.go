package store

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func contentStore(t *testing.T) (*Store, w.Thread, w.Run) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	thread := w.Thread{ID: w.NewID(), WorkspaceID: w.NewID(), Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: thread.WorkspaceID, ThreadID: thread.ID, Status: "queued", CreatedAt: w.Now()}
	err = s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.SaveWorkspace(w.Workspace{ID: thread.WorkspaceID, Name: "test", Revision: 1}); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		return tx.InsertRun(run, w.NewID(), "hash")
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, thread, run
}

func saveChunkedMessage(t *testing.T, s *Store, thread w.Thread, run w.Run, id string, chunks []string, preview string) w.Message {
	t.Helper()
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	message := w.Message{ID: id, ThreadID: thread.ID, RunID: run.ID, Content: preview, ContentFormat: w.ContentFormatChunksV1, ContentBytes: total, ContentTruncated: total > len(preview), Status: "processing", CreatedAt: w.Now()}
	err := s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		for i, chunk := range chunks {
			if err := tx.AppendMessageChunk(id, i+1, chunk); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func readMessageContent(t *testing.T, s *Store, id string, limit int) string {
	t.Helper()
	var body strings.Builder
	err := s.Read(context.Background(), func(tx *Tx) error {
		cursor := ""
		for pages := 0; pages < 100; pages++ {
			page, err := tx.MessageContent(id, cursor, limit)
			if err != nil {
				return err
			}
			if !utf8.ValidString(page.Content) {
				t.Fatal("page split a UTF-8 character")
			}
			body.WriteString(page.Content)
			if !page.HasMore {
				return nil
			}
			if page.NextCursor == "" {
				t.Fatal("missing next cursor")
			}
			cursor = page.NextCursor
		}
		t.Fatal("pagination did not terminate")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return body.String()
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func contentCursorToken(t *testing.T, c contentCursor) string {
	t.Helper()
	token, err := encodeContentCursor(c)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func rawContentCursor(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestMessageChunksPageWithoutLoadingWholeBody(t *testing.T) {
	s, thread, run := contentStore(t)
	chunks := []string{}
	for i := 0; i < 10; i++ {
		chunks = append(chunks, strings.Repeat("猫", 2730))
	}
	message := saveChunkedMessage(t, s, thread, run, w.NewID(), chunks, strings.Repeat("猫", 10000))
	if got := readMessageContent(t, s, message.ID, 0); got != strings.Join(chunks, "") {
		t.Fatalf("default pagination lost content: got %d bytes", len(got))
	}
	if got := readMessageContent(t, s, message.ID, 1); got != strings.Join(chunks, "") {
		t.Fatalf("single-chunk pagination lost content: got %d bytes", len(got))
	}
	var first MessageContentPage
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		first, err = tx.MessageContent(message.ID, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if first.ContentFormat != w.ContentFormatChunksV1 || first.ContentBytes != 81900 || !first.ContentTruncated || !first.HasMore {
		t.Fatalf("unexpected first page: %+v", first)
	}
	if len(first.Content) != DefaultContentChunks*len(chunks[0]) {
		t.Fatalf("default page did not contain eight chunks: %d", len(first.Content))
	}
	loaded, err := func() (w.Message, error) {
		var got w.Message
		err := s.Read(context.Background(), func(tx *Tx) error { var err error; got, err = tx.Message(message.ID); return err })
		return got, err
	}()
	if err != nil || loaded.Content != message.Content || loaded.Format() != w.ContentFormatChunksV1 {
		t.Fatalf("message metadata changed: %+v %v", loaded, err)
	}
	if _, err := readContentPage(s, message.ID, "", -1); err == nil {
		t.Fatal("negative limit accepted")
	}
	if _, err := readContentPage(s, message.ID, "", MaxContentChunks+1); err == nil {
		t.Fatal("oversized limit accepted")
	}
}

func readContentPage(s *Store, id, cursor string, limit int) (MessageContentPage, error) {
	var page MessageContentPage
	err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.MessageContent(id, cursor, limit)
		return err
	})
	return page, err
}

func TestMessageChunkValidationRetryAndRollback(t *testing.T) {
	s, thread, run := contentStore(t)
	message := saveChunkedMessage(t, s, thread, run, w.NewID(), nil, "")
	for _, tc := range []struct {
		name    string
		content string
		seq     int
	}{
		{"empty", "", 1},
		{"oversized", strings.Repeat("x", w.MaxMessageChunkBytes+1), 1},
		{"invalid utf8", string([]byte{0xff}), 1},
		{"zero seq", "body", 0},
	} {
		if err := s.Write(context.Background(), func(tx *Tx) error { return tx.AppendMessageChunk(message.ID, tc.seq, tc.content) }); err == nil {
			t.Fatalf("%s chunk accepted", tc.name)
		}
	}
	if err := s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.AppendMessageChunk(message.ID, 1, "first"); err != nil {
			return err
		}
		return tx.AppendMessageChunk(message.ID, 1, "first")
	}); err != nil {
		t.Fatal(err)
	}
	if got := readMessageContent(t, s, message.ID, 0); got != "first" {
		t.Fatalf("retry duplicated a chunk: %q", got)
	}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.AppendMessageChunk(message.ID, 1, "different") }); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("different chunk overwrote the same position: %v", err)
	}
	rollback := errors.New("rollback")
	if err := s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.AppendMessageChunk(message.ID, 2, "rolled back"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if got := readMessageContent(t, s, message.ID, 0); got != "first" {
		t.Fatalf("rolled-back chunk survived: %q", got)
	}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.AppendMessageChunk(message.ID, 4, "gap") }); err == nil {
		t.Fatal("chunk sequence gap accepted")
	}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.AppendMessageChunk(message.ID, 2, "second") }); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.AppendMessageChunk(message.ID, 2, "second") }); err != nil {
		t.Fatalf("repeating the next chunk was not idempotent: %v", err)
	}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.AppendMessageChunk(message.ID, 2, "changed") }); !errors.Is(err, w.ErrConflict) {
		t.Fatalf("existing chunk was rewritten: %v", err)
	}
	if got := readMessageContent(t, s, message.ID, 0); got != "firstsecond" {
		t.Fatalf("chunk snapshots are not contiguous: %q", got)
	}
}

func TestChunkAndMessageMetadataCommitTogether(t *testing.T) {
	s, thread, run := contentStore(t)
	id := w.NewID()
	err := s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.AppendMessageChunk(id, 1, "chunk-first"); err != nil {
			return err
		}
		return tx.SaveMessage(w.Message{ID: id, ThreadID: thread.ID, RunID: run.ID, Content: "chunk-first", ContentFormat: w.ContentFormatChunksV1, ContentBytes: 11, Status: "processing", CreatedAt: w.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readMessageContent(t, s, id, 0); got != "chunk-first" {
		t.Fatalf("combined transaction lost the chunk: %q", got)
	}
}

func TestChunkedPreviewAndSnapshotCursor(t *testing.T) {
	s, thread, run := contentStore(t)
	id := w.NewID()
	err := s.Write(context.Background(), func(tx *Tx) error {
		return tx.SaveMessage(w.Message{ID: id, ThreadID: thread.ID, RunID: run.ID, Content: strings.Repeat("x", w.MaxMessagePreviewBytes), ContentFormat: w.ContentFormatChunksV1, ContentBytes: w.MaxMessagePreviewBytes, Status: "processing", CreatedAt: w.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), func(tx *Tx) error {
		return tx.SaveMessage(w.Message{ID: id, ThreadID: thread.ID, RunID: run.ID, Content: strings.Repeat("x", w.MaxMessagePreviewBytes+1), ContentFormat: w.ContentFormatChunksV1, ContentBytes: w.MaxMessagePreviewBytes + 1, Status: "processing", CreatedAt: w.Now()})
	}); err == nil {
		t.Fatal("oversized chunked preview accepted")
	}
	if err := s.Write(context.Background(), func(tx *Tx) error {
		return tx.SaveMessage(w.Message{ID: id, ThreadID: thread.ID, RunID: run.ID, Content: "x", ContentFormat: "unexpected", ContentBytes: 1, Status: "processing", CreatedAt: w.Now()})
	}); err == nil {
		t.Fatal("unknown content format accepted")
	}
	saveChunkedMessage(t, s, thread, run, id, []string{"first", "second"}, "")
	first, err := readContentPage(s, id, "", 1)
	if err != nil || first.NextCursor == "" || first.Content != "first" || first.ContentBytes != 11 {
		t.Fatal(err, first)
	}
	tail, err := readContentPage(s, id, first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := readContentPage(s, id, first.NextCursor, 1)
	if err != nil || !reflect.DeepEqual(tail, retry) {
		t.Fatalf("retrying a cursor changed its page: %v %+v %+v", err, tail, retry)
	}
	if tail.Content != "second" || tail.HasMore {
		t.Fatalf("unexpected snapshot tail: %+v", tail)
	}
	err = s.Write(context.Background(), func(tx *Tx) error {
		message, err := tx.Message(id)
		if err != nil {
			return err
		}
		message.ContentBytes += len("third")
		if err := tx.SaveMessage(message); err != nil {
			return err
		}
		return tx.AppendMessageChunk(id, 3, "third")
	})
	if err != nil {
		t.Fatal(err)
	}
	stable, err := readContentPage(s, id, first.NextCursor, 1)
	if err != nil {
		t.Fatalf("append invalidated a snapshot cursor: %v", err)
	}
	if stable.Content != tail.Content || stable.ContentBytes != 11 || stable.ContentTruncated != tail.ContentTruncated {
		t.Fatalf("snapshot page drifted after append: %+v", stable)
	}
	fresh, err := readContentPage(s, id, "", 0)
	if err != nil || fresh.Content != "firstsecondthird" || fresh.ContentBytes != 16 || fresh.HasMore {
		t.Fatalf("fresh first page missed the new body: %+v %v", fresh, err)
	}
}

func TestChunkSnapshotSurvivesConcurrentAppends(t *testing.T) {
	s, thread, run := contentStore(t)
	id := saveChunkedMessage(t, s, thread, run, w.NewID(), []string{"one", "two", "three", "four"}, "").ID
	first, err := readContentPage(s, id, "", 2)
	if err != nil || !first.HasMore || first.Content != "onetwo" || first.ContentBytes != 15 {
		t.Fatal(err, first)
	}
	appended := make(chan error, 1)
	go func() {
		appended <- s.Write(context.Background(), func(tx *Tx) error {
			message, err := tx.Message(id)
			if err != nil {
				return err
			}
			message.ContentBytes += len("five")
			if err := tx.SaveMessage(message); err != nil {
				return err
			}
			return tx.AppendMessageChunk(id, 5, "five")
		})
	}()
	tail, tailErr := readContentPage(s, id, first.NextCursor, 0)
	appendErr := <-appended
	if appendErr != nil {
		t.Fatal(appendErr)
	}
	if tailErr != nil {
		t.Fatal(tailErr)
	}
	if tail.Content != "threefour" || tail.HasMore || tail.NextCursor != "" || tail.ContentBytes != 15 || !tail.ContentTruncated {
		t.Fatalf("old snapshot drifted after a concurrent append: %+v", tail)
	}
	fresh, err := readContentPage(s, id, "", 0)
	if err != nil || fresh.Content != "onetwothreefourfive" || fresh.ContentBytes != 19 || fresh.HasMore {
		t.Fatalf("fresh page missed the appended body: %+v %v", fresh, err)
	}
	if got := readMessageContent(t, s, id, 1); got != "onetwothreefourfive" {
		t.Fatalf("paging across the new snapshot lost content: %q", got)
	}
}

func TestChunkedCursorRejectsMalformedAndOutOfRange(t *testing.T) {
	s, thread, run := contentStore(t)
	id := saveChunkedMessage(t, s, thread, run, w.NewID(), []string{"甲", "乙", "丙"}, "").ID
	valid := contentCursor{MessageID: id, Format: w.ContentFormatChunksV1, Snapshot: 3, Bytes: 9, Truncated: true, After: 2}
	forged := func(mutate func(*contentCursor)) string {
		c := valid
		mutate(&c)
		return contentCursorToken(t, c)
	}
	for _, tc := range []struct {
		name   string
		cursor string
	}{
		{"oversized", strings.Repeat("A", maxContentCursorBytes+1)},
		{"not base64", "%%%"},
		{"unknown field", rawContentCursor(`{"message_id":"` + id + `","format":"chunks_v1","snapshot":3,"bytes":9,"truncated":false,"after":2,"extra":1}`)},
		{"trailing value", rawContentCursor(`{"message_id":"` + id + `","format":"chunks_v1","snapshot":3,"bytes":9,"truncated":false,"after":2}{}`)},
		{"negative after", forged(func(c *contentCursor) { c.After = -1 })},
		{"zero after", forged(func(c *contentCursor) { c.After = 0 })},
		{"after at snapshot", forged(func(c *contentCursor) { c.After = 3 })},
		{"after beyond snapshot", forged(func(c *contentCursor) { c.After = 4 })},
		{"huge after", forged(func(c *contentCursor) { c.After = 1 << 30 })},
		{"snapshot beyond highwater", forged(func(c *contentCursor) { c.Snapshot = 4 })},
		{"cross message", forged(func(c *contentCursor) { c.MessageID = "other-message" })},
		{"format mismatch", forged(func(c *contentCursor) { c.Format = w.ContentFormatInline })},
	} {
		if _, err := readContentPage(s, id, tc.cursor, 1); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("%s cursor was accepted: %v", tc.name, err)
		}
	}
}

func TestChunkedCursorRejectsMissingSequence(t *testing.T) {
	s, thread, run := contentStore(t)
	id := saveChunkedMessage(t, s, thread, run, w.NewID(), []string{"甲", "乙"}, "").ID
	if _, err := s.db.Exec("INSERT INTO message_chunks(message_id,seq,content) VALUES(?,?,?)", id, 4, "缺"); err != nil {
		t.Fatal(err)
	}
	c := contentCursor{MessageID: id, Format: w.ContentFormatChunksV1, Snapshot: 4, Bytes: len("甲乙"), After: 3}
	if _, err := readContentPage(s, id, contentCursorToken(t, c), 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor past a missing sequence was accepted: %v", err)
	}
}

func TestInlineCursorBindsExactBody(t *testing.T) {
	s, thread, run := contentStore(t)
	id := w.NewID()
	text := strings.Repeat("猫", 3000)
	message := w.Message{ID: id, ThreadID: thread.ID, RunID: run.ID, Content: text, Status: "completed", CreatedAt: w.Now()}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.SaveMessage(message) }); err != nil {
		t.Fatal(err)
	}
	first, err := readContentPage(s, id, "", 1)
	if err != nil || !first.HasMore || first.NextCursor == "" {
		t.Fatal(err, first)
	}
	tail, err := readContentPage(s, id, first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := readContentPage(s, id, first.NextCursor, 1)
	if err != nil || !reflect.DeepEqual(tail, retry) {
		t.Fatalf("retrying an inline cursor changed its page: %v %+v %+v", err, tail, retry)
	}
	midrune := contentCursor{MessageID: id, Format: w.ContentFormatInline, Snapshot: len(text), Bytes: len(text), Digest: contentDigest(text), After: 1}
	if _, err := readContentPage(s, id, contentCursorToken(t, midrune), 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("mid-rune inline cursor was accepted: %v", err)
	}
	atEnd := midrune
	atEnd.After = len(text)
	if _, err := readContentPage(s, id, contentCursorToken(t, atEnd), 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("inline cursor at the body end was accepted: %v", err)
	}
	message.Content = strings.Repeat("犬", 3000)
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.SaveMessage(message) }); err != nil {
		t.Fatal(err)
	}
	if _, err := readContentPage(s, id, first.NextCursor, 1); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("same-length body change kept the cursor: %v", err)
	}
}

func TestEmptyChunkedMessagePagesEmpty(t *testing.T) {
	s, thread, run := contentStore(t)
	message := saveChunkedMessage(t, s, thread, run, w.NewID(), nil, "")
	page, err := readContentPage(s, message.ID, "", 1)
	if err != nil || page.Content != "" || page.HasMore || page.NextCursor != "" || page.ContentBytes != 0 || page.ContentTruncated || page.ContentFormat != w.ContentFormatChunksV1 {
		t.Fatalf("empty chunked page = %+v %v", page, err)
	}
	if got := readMessageContent(t, s, message.ID, 1); got != "" {
		t.Fatalf("empty chunked message paged to %q", got)
	}
}

func TestLegacyInlineFallbackSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: w.NewID(), Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: thread.WorkspaceID, ThreadID: thread.ID, Status: "queued", CreatedAt: w.Now()}
	text := strings.Repeat("证", 30000)
	message := w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Content: text, Status: "completed", CreatedAt: w.Now()}
	err = s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.SaveWorkspace(w.Workspace{ID: thread.WorkspaceID, Name: "test", Revision: 1}); err != nil {
			return err
		}
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.InsertRun(run, w.NewID(), "hash"); err != nil {
			return err
		}
		return tx.SaveMessage(message)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := func() (w.Message, error) {
		var got w.Message
		err := s.Read(context.Background(), func(tx *Tx) error { var err error; got, err = tx.Message(message.ID); return err })
		return got, err
	}()
	if err != nil || loaded.Content != text || loaded.Format() != w.ContentFormatInline {
		t.Fatalf("legacy inline message changed: %d bytes, %v", len(loaded.Content), err)
	}
	if got := readMessageContent(t, s, message.ID, 2); got != text {
		t.Fatalf("inline fallback lost content: got %d bytes", len(got))
	}
	if count := countRows(t, s, "SELECT COUNT(*) FROM schema_migrations WHERE version=3"); count != 1 {
		t.Fatal("chunk migration was not appended")
	}
	if count := countRows(t, s, "SELECT COUNT(*) FROM message_chunks WHERE message_id=?", message.ID); count != 0 {
		t.Fatal("legacy message gained a chunk copy")
	}
}

func TestDeleteThreadAndWorkspaceRemoveChunks(t *testing.T) {
	for _, workspaceWide := range []bool{false, true} {
		name := "thread"
		if workspaceWide {
			name = "workspace"
		}
		t.Run(name, func(t *testing.T) {
			s, thread, run := contentStore(t)
			message := saveChunkedMessage(t, s, thread, run, w.NewID(), []string{"one", "two"}, "one")
			err := s.Write(context.Background(), func(tx *Tx) error {
				if workspaceWide {
					return tx.DeleteWorkspace(thread.WorkspaceID)
				}
				return tx.DeleteThread(thread.ID)
			})
			if err != nil {
				t.Fatal(err)
			}
			if count := countRows(t, s, "SELECT COUNT(*) FROM messages WHERE id=?", message.ID); count != 0 {
				t.Fatal("message survived deletion")
			}
			if count := countRows(t, s, "SELECT COUNT(*) FROM message_chunks WHERE message_id=?", message.ID); count != 0 {
				t.Fatal("chunks survived deletion")
			}
		})
	}
}
