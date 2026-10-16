package store

import (
	"context"
	"errors"
	w "github.com/AcSunday/cocogem/internal/workspace"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistenceRollbackAndConstraints(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ws := w.Workspace{ID: w.NewID(), Name: "研究", Revision: 1}
	th := w.Thread{ID: w.NewID(), WorkspaceID: ws.ID, Revision: 1, UpdatedAt: w.Now()}
	run := w.Run{ID: w.NewID(), WorkspaceID: ws.ID, ThreadID: th.ID, Status: "queued", CreatedAt: w.Now()}
	err = s.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveWorkspace(ws); err != nil {
			return err
		}
		if err := tx.SaveThread(th); err != nil {
			return err
		}
		return tx.InsertRun(run, "key", "hash")
	})
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	err = s.Write(ctx, func(tx *Tx) error {
		if err := tx.AppendEvent(run.ID, "", "", "test", map[string]string{"x": "y"}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(tx *Tx) error {
		events, err := tx.Events(run.ID, 0)
		if len(events) != 0 {
			t.Fatal("rolled-back event survived")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *Tx) error { other := run; other.ID = w.NewID(); return tx.InsertRun(other, "other", "hash") })
	if err == nil {
		t.Fatal("two active runs accepted")
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		run.Status = "succeeded"
		if err := tx.SaveRun(run); err != nil {
			return err
		}
		return tx.AppendEvent(run.ID, "", "", "run_finished", run)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *Tx) error { return tx.SaveRun(run) }); !errors.Is(err, w.ErrConflict) {
		t.Fatal("terminal state rewritten", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Read(ctx, func(tx *Tx) error {
		got, hash, err := tx.Idempotent(th.ID, "key")
		if got.Status != "succeeded" || hash != "hash" {
			t.Fatal(got, hash)
		}
		es, _ := tx.Events(run.ID, 0)
		if len(es) != 1 || es[0].Seq != 1 {
			t.Fatal(es)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %o", info.Mode().Perm())
	}
}
