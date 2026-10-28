package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func pagingStore(t *testing.T) (*Store, w.Workspace, w.Thread, w.Run) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ws := w.Workspace{ID: w.NewID(), Name: "paging", Revision: 1}
	thread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "paging", Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: thread.ID, Status: "succeeded", CreatedAt: w.Now()}
	err = s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
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
	return s, ws, thread, run
}

func savePagingMessages(t *testing.T, s *Store, thread w.Thread, run w.Run, contents ...string) []w.Message {
	t.Helper()
	messages := make([]w.Message, 0, len(contents))
	err := s.Write(context.Background(), func(tx *Tx) error {
		for _, content := range contents {
			m := w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Role: "assistant", Content: content, Status: "completed", CreatedAt: w.Now()}
			if err := tx.SaveMessage(m); err != nil {
				return err
			}
			messages = append(messages, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func pageContents(items []w.Message) []string {
	out := make([]string, len(items))
	for i, m := range items {
		out[i] = m.Content
	}
	return out
}

func TestPagedMessagesKeepsAttemptAndModelSnapshot(t *testing.T) {
	s, _, thread, run := pagingStore(t)
	message := w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, StepID: w.NewID(), AttemptID: w.NewID(), AgentID: w.NewID(), AgentName: "研究成员", Role: "assistant", Model: "requested", ResolvedModel: "resolved", Content: "snapshot", Status: "completed", CreatedAt: w.Now()}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.SaveMessage(message) }); err != nil {
		t.Fatal(err)
	}
	var page MessagePage
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.PagedMessages(thread.ID, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %+v", page.Items)
	}
	got := page.Items[0]
	if got.AttemptID != message.AttemptID || got.Model != "requested" || got.ResolvedModel != "resolved" {
		t.Fatalf("model snapshot was not persisted: %+v", got)
	}
}

func TestPagedMessagesReturnsLatestPageInOrder(t *testing.T) {
	s, _, thread, run := pagingStore(t)
	contents := make([]string, 35)
	for i := range contents {
		contents[i] = fmt.Sprintf("message-%02d", i)
	}
	savePagingMessages(t, s, thread, run, contents...)

	var first MessagePage
	err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		first, err = tx.PagedMessages(thread.ID, "", 0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := pageContents(first.Items)
	want := contents[5:]
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("latest page = %v, want %v", got, want)
	}
	if first.NextCursor == "" {
		t.Fatal("missing cursor for older messages")
	}

	var older MessagePage
	err = s.Read(context.Background(), func(tx *Tx) error {
		var err error
		older, err = tx.PagedMessages(thread.ID, first.NextCursor, 0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := pageContents(older.Items); strings.Join(got, ",") != strings.Join(contents[:5], ",") {
		t.Fatalf("older page = %v", got)
	}
	if older.NextCursor != "" {
		t.Fatal("cursor returned without an older page")
	}
}

func TestPagedMessagesCursorStaysStableWhenMessagesArrive(t *testing.T) {
	s, _, thread, run := pagingStore(t)
	contents := make([]string, 40)
	for i := range contents {
		contents[i] = fmt.Sprintf("message-%02d", i)
	}
	savePagingMessages(t, s, thread, run, contents...)

	var first MessagePage
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		first, err = tx.PagedMessages(thread.ID, "", 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	savePagingMessages(t, s, thread, run, "inserted-a", "inserted-b")

	var older MessagePage
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		older, err = tx.PagedMessages(thread.ID, first.NextCursor, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := contents[20:30]
	if got := pageContents(older.Items); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("older page shifted after inserts: %v", got)
	}
	seen := map[string]bool{}
	for _, m := range append(append([]w.Message{}, first.Items...), older.Items...) {
		if seen[m.ID] {
			t.Fatal("duplicate message across pages")
		}
		seen[m.ID] = true
	}
}

func TestPagedMessagesRejectsForeignOrBrokenCursors(t *testing.T) {
	s, ws, thread, run := pagingStore(t)
	savePagingMessages(t, s, thread, run, "a", "b")
	otherThread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "other", Revision: 1, UpdatedAt: w.Now()}
	otherRun := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: otherThread.ID, Status: "succeeded", CreatedAt: w.Now()}
	if err := s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.SaveThread(otherThread); err != nil {
			return err
		}
		if err := tx.InsertRun(otherRun, w.NewID(), "hash"); err != nil {
			return err
		}
		for _, content := range []string{"x", "y", "z", "w", "v"} {
			if err := tx.SaveMessage(w.Message{ID: w.NewID(), ThreadID: otherThread.ID, RunID: otherRun.ID, Role: "assistant", Content: content, Status: "completed", CreatedAt: w.Now()}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var foreignPosition int64
	if err := s.db.QueryRow("SELECT position FROM messages WHERE thread_id=? ORDER BY position DESC LIMIT 1", otherThread.ID).Scan(&foreignPosition); err != nil {
		t.Fatal(err)
	}
	foreign := base64.RawURLEncoding.EncodeToString([]byte(`{"thread_id":"other","position":1}`))
	encode := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
	crossScope := encode(fmt.Sprintf(`{"thread_id":%q,"position":%d}`, thread.ID, foreignPosition))
	for name, cursor := range map[string]string{
		"foreign thread":    foreign,
		"foreign position":  crossScope,
		"garbage":           "%%%",
		"zero position":     encode(`{"thread_id":"` + thread.ID + `","position":0}`),
		"negative position": encode(`{"thread_id":"` + thread.ID + `","position":-1}`),
		"missing position":  encode(`{"thread_id":"` + thread.ID + `","position":999}`),
		"huge position":     encode(`{"thread_id":"` + thread.ID + `","position":4611686018427387904}`),
		"unknown field":     encode(`{"thread_id":"` + thread.ID + `","position":1,"extra":true}`),
		"trailing json":     encode(`{"thread_id":"` + thread.ID + `","position":1}{}`),
		"oversized token":   strings.Repeat("A", maxPageCursorBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			err := s.Read(context.Background(), func(tx *Tx) error {
				_, err := tx.PagedMessages(thread.ID, cursor, 10)
				return err
			})
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for name, limit := range map[string]int{"negative message limit": -1, "oversized message limit": MaxMessagePage + 1} {
		err := s.Read(context.Background(), func(tx *Tx) error {
			_, err := tx.PagedMessages(thread.ID, "", limit)
			return err
		})
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	var page MessagePage
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.PagedMessages(thread.ID, encode(`{"thread_id":"`+thread.ID+`","position":2}`), 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Content != "a" || page.NextCursor != "" {
		t.Fatalf("exclusive-before cursor = %+v", page)
	}
}

func TestPagedMessagesProjectsLegacyInlinePreviewWithoutRewriting(t *testing.T) {
	s, _, thread, run := pagingStore(t)
	body := strings.Repeat("中文证据", 12000)
	messages := savePagingMessages(t, s, thread, run, "短消息", body)
	var page MessagePage
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.PagedMessages(thread.ID, "", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d", len(page.Items))
	}
	short := page.Items[0]
	if short.Content != "短消息" || short.ContentBytes != len("短消息") || short.ContentTruncated || short.Format() != w.ContentFormatInline {
		t.Fatalf("short inline projection = %+v", short)
	}
	large := page.Items[1]
	if large.Format() != w.ContentFormatInline || large.ContentBytes != len(body) || !large.ContentTruncated {
		t.Fatalf("large inline projection = %+v", large)
	}
	if len(large.Content) > w.MaxMessagePreviewBytes || !utf8.ValidString(large.Content) || !strings.HasPrefix(body, large.Content) {
		t.Fatalf("preview is not a bounded UTF-8 prefix: %d bytes", len(large.Content))
	}
	var stored w.Message
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		stored, err = tx.Message(messages[1].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if stored.Content != body || stored.ContentBytes != 0 || stored.ContentTruncated {
		t.Fatal("paging projection rewrote the stored inline body")
	}

	chunked := w.Message{ID: w.NewID(), ThreadID: thread.ID, RunID: run.ID, Role: "assistant", Content: "块预览", ContentFormat: w.ContentFormatChunksV1, ContentBytes: 50000, ContentTruncated: true, Status: "completed", CreatedAt: w.Now()}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.SaveMessage(chunked) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.PagedMessages(thread.ID, "", 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("latest items = %d", len(page.Items))
	}
	got := page.Items[0]
	if got.Content != "块预览" || got.ContentBytes != 50000 || !got.ContentTruncated || got.Format() != w.ContentFormatChunksV1 {
		t.Fatalf("chunked projection = %+v", got)
	}
}

func TestEventPageReturnsBoundedPagesAndNextSequence(t *testing.T) {
	s, ws, _, run := pagingStore(t)
	otherThread := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Title: "other", Revision: 1, UpdatedAt: w.Now()}
	otherRun := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: otherThread.ID, Status: "succeeded", CreatedAt: w.Now()}
	if err := s.Write(context.Background(), func(tx *Tx) error {
		if err := tx.SaveThread(otherThread); err != nil {
			return err
		}
		if err := tx.InsertRun(otherRun, w.NewID(), "hash"); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, "", "", "one", map[string]int{"n": 1}); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, "", "", "two", map[string]int{"n": 2}); err != nil {
			return err
		}
		if err := tx.AppendEvent(run.ID, "", "", "three", map[string]int{"n": 3}); err != nil {
			return err
		}
		return tx.AppendEvent(otherRun.ID, "", "", "foreign", map[string]int{"n": 9})
	}); err != nil {
		t.Fatal(err)
	}

	var page EventPage
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.EventPage(run.ID, 0, 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Type != "one" || page.Items[1].Type != "two" || page.NextCursor != "2" {
		t.Fatalf("first page = %+v", page)
	}
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.EventPage(run.ID, 2, 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Type != "three" || page.NextCursor != "" {
		t.Fatalf("last page = %+v", page)
	}
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.EventPage(run.ID, 3, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("empty page = %+v", page)
	}
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		page, err = tx.EventPage(otherRun.ID, 0, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Type != "foreign" {
		t.Fatalf("events crossed runs: %+v", page)
	}
	for name, call := range map[string]func() error{
		"negative cursor": func() error {
			return s.Read(context.Background(), func(tx *Tx) error { _, err := tx.EventPage(run.ID, -1, 2); return err })
		},
		"oversized limit": func() error {
			return s.Read(context.Background(), func(tx *Tx) error { _, err := tx.EventPage(run.ID, 0, MaxEventLimit+1); return err })
		},
		"negative limit": func() error {
			return s.Read(context.Background(), func(tx *Tx) error { _, err := tx.EventPage(run.ID, 0, -1); return err })
		},
	} {
		if err := call(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestSourceMetadataOmitsBodyButDetailKeepsIt(t *testing.T) {
	s, _, thread, run := pagingStore(t)
	source := w.Source{ID: w.NewID(), WorkspaceID: run.WorkspaceID, ThreadID: thread.ID, RunID: run.ID, Kind: "web", Title: "captured", URL: "https://example.test/a", Content: strings.Repeat("证据正文", 4000), CapturedAt: w.Now()}
	if err := s.Write(context.Background(), func(tx *Tx) error { return tx.SaveSource(source) }); err != nil {
		t.Fatal(err)
	}
	var listed []w.Source
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		listed, err = tx.SourceMetadata(thread.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Content != "" || listed[0].Title != source.Title || listed[0].URL != source.URL {
		t.Fatalf("metadata = %+v", listed)
	}
	var detail w.Source
	if err := s.Read(context.Background(), func(tx *Tx) error {
		var err error
		detail, err = tx.Source(source.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if detail.Content != source.Content || detail.ID != source.ID {
		t.Fatal("detail lost the captured body")
	}
}
