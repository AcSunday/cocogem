package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestPurgeRollbackReturnsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		ws := w.Workspace{ID: "ws", Revision: 1, Name: "test"}
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		for _, id := range []string{"t1", "t2"} {
			if err := tx.SaveThread(w.Thread{ID: id, WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now(), DeletedAt: "2026-09-01T08:00:00Z", PurgeAt: "2026-10-01T08:00:00Z"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER review_force_rollback BEFORE DELETE ON threads WHEN OLD.id='t2' BEGIN SELECT RAISE(ABORT,'forced rollback'); END`); err != nil {
		t.Fatal(err)
	}
	s := Server{store: db, proposals: map[string]proposal{}}
	count, err := s.PurgeExpiredThreads(t.Context())
	if err == nil {
		t.Fatal("expected injected transaction failure")
	}
	if e := db.Read(t.Context(), func(tx *store.Tx) error {
		for _, id := range []string{"t1", "t2"} {
			if _, e := tx.ThreadAny(id); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatalf("purge rollback lost thread: %v", e)
	}
	if count != 0 {
		t.Fatalf("purge returned %d deleted although transaction rolled back both threads: %v", count, err)
	}
}

func TestPurgeAppliesCurrentRetentionToExistingTrash(t *testing.T) {
	s, db, _, _ := pagingServer(t, workflow.DefaultLimits())
	thread, _ := seedThread(t, db)
	deleted := time.Now().UTC().AddDate(0, 0, -10)
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		thread.DeletedAt = deleted.Format(time.RFC3339Nano)
		// An older 30-day schedule that should follow the current 7-day default.
		thread.PurgeAt = deleted.AddDate(0, 0, 30).Format(time.RFC3339Nano)
		return tx.SaveThread(thread)
	}); err != nil {
		t.Fatal(err)
	}
	count, err := s.PurgeExpiredThreads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("purged %d threads under the current retention, want 1", count)
	}
}

func TestPurgeKeepsActiveRunsThenRemovesExpiredTrash(t *testing.T) {
	s, db, _, _ := pagingServer(t, workflow.DefaultLimits())
	thread, _ := seedThread(t, db)
	active := w.Run{ID: w.NewID(), WorkspaceID: thread.WorkspaceID, ThreadID: thread.ID, Status: "running", CreatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		thread.DeletedAt = "2026-09-01T08:00:00Z"
		thread.PurgeAt = "2026-10-01T08:00:00Z"
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		return tx.InsertRun(active, w.NewID(), "active")
	}); err != nil {
		t.Fatal(err)
	}
	count, err := s.PurgeExpiredThreads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("purged %d threads with an active run", count)
	}
	var kept bool
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		_, err := tx.ThreadAny(thread.ID)
		kept = err == nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !kept {
		t.Fatal("active-run thread was removed from trash")
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		run, err := tx.Run(active.ID)
		if err != nil {
			return err
		}
		run.Status = "succeeded"
		return tx.SaveRun(run)
	}); err != nil {
		t.Fatal(err)
	}
	count, err = s.PurgeExpiredThreads(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("purged %d expired threads, want 1", count)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		_, err := tx.ThreadAny(thread.ID)
		return err
	}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired trash was not removed: %v", err)
	}
}

// A cleanup candidate scanned before its thread is restored and deleted again
// must not use the stale expiry to purge the fresh trash entry.
func TestPurgeSkipsRestoredAndRedeletedThread(t *testing.T) {
	s, db, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	thread, run := seedThread(t, db)
	step := w.Step{ID: w.NewID(), RunID: run.ID, Status: "failed"}
	attempt := w.Attempt{ID: w.NewID(), RunID: run.ID, StepID: step.ID, Number: 1, Status: "failed", Process: &w.ProcessRef{PID: 2000000000, PGID: 2000000000, Start: "test-process"}}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		thread.DeletedAt = "2026-09-01T08:00:00Z"
		thread.PurgeAt = "2026-10-01T08:00:00Z"
		if err := tx.SaveThread(thread); err != nil {
			return err
		}
		if err := tx.SaveStep(step); err != nil {
			return err
		}
		return tx.SaveAttempt(attempt)
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	releasePath := filepath.Join(dir, "release")
	enteredPath := filepath.Join(dir, "entered")
	if err := syscall.Mkfifo(releasePath, 0600); err != nil {
		t.Fatal(err)
	}
	// Only the first ps (cleanup's owned-process check) blocks. Later ps calls
	// from the normal delete API report no surviving owned group immediately.
	script := fmt.Sprintf("#!/bin/sh\nif mkdir %q 2>/dev/null; then\n : > %q\n IFS= read -r ignored < %q\nfi\nexit 0\n", filepath.Join(dir, "once"), enteredPath, releasePath)
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	type result struct {
		count int
		err   error
	}
	done := make(chan result, 1)
	go func() {
		count, err := s.PurgeExpiredThreads(t.Context())
		done <- result{count: count, err: err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(enteredPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not reach blocked owned-process check")
		}
		time.Sleep(time.Millisecond)
	}
	unblock := func() {
		t.Helper()
		file, err := os.OpenFile(releasePath, os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(file, "continue"); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	restored := apiCall(s, "POST", "/api/v1/threads/"+thread.ID+"/restore", map[string]any{}, cookie, csrf, map[string]string{"If-Match": "1"})
	if restored.Code != 200 {
		unblock()
		t.Fatalf("restore failed: %d %s", restored.Code, restored.Body.String())
	}
	deleted := apiCall(s, "DELETE", "/api/v1/threads/"+thread.ID, nil, cookie, csrf, map[string]string{"If-Match": strconv.Itoa(thread.Revision + 1)})
	if deleted.Code != 200 {
		unblock()
		t.Fatalf("second delete failed: %d %s", deleted.Code, deleted.Body.String())
	}
	unblock()
	var got result
	select {
	case got = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup did not return")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	err := db.Read(t.Context(), func(tx *store.Tx) error {
		_, err := tx.ThreadAny(thread.ID)
		return err
	})
	if got.count != 0 || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cleanup used stale expiry and deleted newly trashed thread: count=%d err=%v", got.count, err)
	}
	if err != nil {
		t.Fatal(err)
	}
}
