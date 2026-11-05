package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/project"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestManagedCaptureAppendsScopeAndWritesDeliveredPageReceipt(t *testing.T) {
	db, created, run := seedTaskRun(t)
	run.AutomaticContinuation = false
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		manifest, err := project.New(db).PrepareContextInTx(tx, created.Task.ID, 1, 10000)
		if err != nil {
			return err
		}
		run.ContextRef = manifest.ID
		return tx.InsertRun(run, "context-run-key", "context-run-hash")
	}); err != nil {
		t.Fatal(err)
	}
	source := w.Source{ID: "during-run", WorkspaceID: run.WorkspaceID, ThreadID: run.ThreadID, RunID: run.ID, Kind: "file", Content: "0123456789abcdef"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveSource(source) }); err != nil {
		t.Fatal(err)
	}
	engine := Engine{Store: db}
	page, err := engine.readSourcePage(t.Context(), run, source, "", 8)
	if err != nil || page.Content != "01234567" || !page.HasMore {
		t.Fatalf("page = %+v, %v", page, err)
	}
	brief, err := engine.runContextBrief(t.Context(), run)
	if err != nil || !strings.Contains(brief, created.Revision.Goal) {
		t.Fatalf("real request brief = %q, %v", brief, err)
	}
	if err := db.Read(t.Context(), func(tx *store.Tx) error {
		appended, err := tx.ContextScopeEntries(run.ContextRef)
		if err != nil {
			return err
		}
		if len(appended) != 1 || appended[0].Ref != source.ID {
			t.Fatalf("scope append = %+v", appended)
		}
		receipts, err := tx.ReadReceipts(run.ContextRef)
		if err != nil {
			return err
		}
		if len(receipts) != 1 || receipts[0].Bytes != 8 || receipts[0].Complete {
			t.Fatalf("read receipts = %+v", receipts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetryContextRebindsRecordsWithoutChangingObjective(t *testing.T) {
	db, created, run := seedTaskRun(t)
	run.AutomaticContinuation = true
	var first, second string
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := prepareRetryContext(tx, &run); err != nil {
			return err
		}
		first = run.ContextRef
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := project.New(db).SaveProjectRecord(t.Context(), w.ProjectRecord{ID: "new-decision", WorkspaceID: run.WorkspaceID, Content: "后续执行段的新有效决定", AcceptedBy: "user", Visibility: "project"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return prepareRetryContext(tx, &run) }); err != nil {
		t.Fatal(err)
	}
	second = run.ContextRef
	if first == second || run.TaskRevision != created.Task.CurrentRevision || run.TaskID != created.Task.ID {
		t.Fatalf("retry identity/context = %+v", run)
	}
	manifest, err := dbManifest(db, second)
	if err != nil || !strings.Contains(manifest.Brief, "后续执行段的新有效决定") {
		t.Fatalf("retry brief = %q, %v", manifest.Brief, err)
	}
}

func dbManifest(db *store.Store, id string) (w.ContextManifest, error) {
	var manifest w.ContextManifest
	err := db.Read(context.Background(), func(tx *store.Tx) error {
		var err error
		manifest, err = tx.ContextManifest(id)
		return err
	})
	return manifest, err
}
