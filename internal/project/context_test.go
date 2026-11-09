package project

import (
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/contextplan"
	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestPrepareContextUsesOnlyCurrentRequirementsAndExactSources(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	revised, err := service.ReviseTask(t.Context(), created.Task.ID, 1, ReviseTaskInput{
		Goal:           "实现幂等创建；当前决定：不得新增 Redis",
		AllowedActions: created.Revision.AllowedActions, SourceMessageID: "message-20", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveProjectRecord(t.Context(), w.ProjectRecord{ID: "runtime", WorkspaceID: th.WorkspaceID, Kind: "decision", Content: "不得新增 Redis", AcceptedBy: "user", Visibility: "project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveProjectRecord(t.Context(), w.ProjectRecord{ID: "private", WorkspaceID: th.WorkspaceID, Kind: "note", Content: "私人草稿密钥", AcceptedBy: "user", Visibility: "private"}); err != nil {
		t.Fatal(err)
	}
	sourceRun := w.Run{ID: "source-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, TaskRevision: 2, Status: "succeeded", CreatedAt: w.Now(), FinishedAt: w.Now()}
	source := w.Source{ID: "source-v1", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: sourceRun.ID, Kind: "file", Content: "完整原文不在启动输入中"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(sourceRun, "source-run-key", "source-run-hash"); err != nil {
			return err
		}
		return tx.SaveSource(source)
	}); err != nil {
		t.Fatal(err)
	}
	var manifest w.ContextManifest
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		var prepareErr error
		manifest, prepareErr = service.PrepareContextInTx(tx, created.Task.ID, revised.Task.CurrentRevision, 10000)
		return prepareErr
	}); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"不得新增 Redis", "source-v1", "work:"} {
		if !strings.Contains(manifest.Brief, wanted) {
			t.Fatalf("brief omitted %q: %s", wanted, manifest.Brief)
		}
	}
	for _, forbidden := range []string{"实现并验证幂等创建", "私人草稿密钥", "完整原文不在启动输入中"} {
		if strings.Contains(manifest.Brief, forbidden) {
			t.Fatalf("brief exposed stale/private/full content %q: %s", forbidden, manifest.Brief)
		}
	}
	page, receipt, err := contextplan.ReadPage(manifest, source.ID, source.Content, contextplan.Digest(source.Content), "", 8)
	if err != nil || page.Content != "完整" || receipt.Bytes == 0 {
		t.Fatalf("authorized exact page = %+v %+v %v", page, receipt, err)
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		_, err := service.PrepareContextInTx(tx, created.Task.ID, 1, 10000)
		return err
	}); !IsCode(err, "revision_conflict") {
		t.Fatalf("retry silently moved to another goal: %v", err)
	}
}
