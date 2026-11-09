package project

import (
	"testing"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestResearchEvidenceLocatorsAndDerivedVisibility(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	run := w.Run{ID: "research-source-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, Status: "succeeded", CreatedAt: w.Now()}
	source := w.Source{
		ID: "private-csv", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: run.ID,
		Kind: "csv", Path: "private.csv", Content: "1: \"name\" | \"amount\"\n2: \"alpha\" | \"2\"\n",
		SHA256: "dataset-sha", Visibility: "private", ParserVersion: "csv-v1", SourceLocator: "row:N; column:M",
	}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, "research-source-key", "research-source-hash"); err != nil {
			return err
		}
		if err := tx.SaveSource(source); err != nil {
			return err
		}
		return SaveDatasetForSource(tx, run, source)
	}); err != nil {
		t.Fatal(err)
	}
	datasets, err := service.Datasets(t.Context(), created.Task.ID)
	if err != nil || len(datasets) != 1 || datasets[0].ContentDigest != "dataset-sha" || datasets[0].Visibility != "private" {
		t.Fatalf("dataset manifests = %+v, %v", datasets, err)
	}
	claim, err := service.SaveClaim(t.Context(), w.Claim{
		ID: "claim-v1", WorkspaceID: th.WorkspaceID, TaskID: created.Task.ID, Revision: 1,
		Statement: "alpha has amount two", Status: "supported",
		Evidence: []w.ClaimEvidence{{SourceID: source.ID, Relation: "supports", Locator: "row:2", Completeness: "complete", Reviewer: "reviewer"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Visibility != "private" || len(claim.SupportingRefs) != 1 {
		t.Fatalf("claim disclosure = %+v", claim)
	}
	if _, err := service.SaveClaim(t.Context(), w.Claim{
		ID: "claim-bad-locator", WorkspaceID: th.WorkspaceID, TaskID: created.Task.ID, Revision: 1,
		Statement: "unsupported locator", Status: "proposed",
		Evidence: []w.ClaimEvidence{{SourceID: source.ID, Relation: "supports", Locator: "row:99", Completeness: "complete", Reviewer: "reviewer"}},
	}); !IsCode(err, "evidence_stale") {
		t.Fatalf("bad evidence locator = %v", err)
	}
	memory, err := service.SaveMemory(t.Context(), w.MemoryRecord{
		ID: "memory-v1", WorkspaceID: th.WorkspaceID, Revision: 1, Scope: created.Task.CollaborationScopeID,
		Kind: "research", Content: "derived alpha conclusion", SourceRefs: []string{source.ID},
		ClaimStatus: "accepted", AcceptedBy: "reviewer", Visibility: "project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if memory.Visibility != "private" || memory.ExposureDigest == "" {
		t.Fatalf("private source was widened by memory publication: %+v", memory)
	}
	if _, err := service.SaveDataset(t.Context(), w.DatasetManifest{
		ID: "wrong-digest", WorkspaceID: th.WorkspaceID, TaskID: created.Task.ID, Revision: 1,
		SourceRefs: []string{source.ID}, ContentDigest: "not-the-source-digest",
	}); !IsCode(err, "evidence_stale") {
		t.Fatalf("dataset accepted mismatched digest: %v", err)
	}
}

func TestDerivedVisibilityFiltersCrossTaskContext(t *testing.T) {
	db, service, th := setup(t)
	created := create(t, service, th)
	childThread := w.Thread{ID: "research-child-thread", WorkspaceID: th.WorkspaceID, Internal: true, Revision: 1, UpdatedAt: w.Now()}
	if err := db.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveThread(childThread) }); err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTask(t.Context(), CreateTaskInput{
		WorkspaceID: th.WorkspaceID, ParentID: created.Task.ID, ThreadID: childThread.ID,
		Goal: "child task", SourceMessageID: "child-message", AcceptedBy: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := w.Run{ID: "visibility-run", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, TaskID: created.Task.ID, Status: "succeeded", CreatedAt: w.Now()}
	privateSource := w.Source{ID: "private-source", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: run.ID, Kind: "file", Content: "private body", Visibility: "private"}
	publicSource := w.Source{ID: "public-source", WorkspaceID: th.WorkspaceID, ThreadID: th.ID, RunID: run.ID, Kind: "file", Content: "public body", Visibility: "project"}
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertRun(run, "visibility-key", "visibility-hash"); err != nil {
			return err
		}
		for _, source := range []w.Source{privateSource, publicSource} {
			if err := tx.SaveSource(source); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveMemory(t.Context(), w.MemoryRecord{
		ID: "private-memory", WorkspaceID: th.WorkspaceID, Revision: 1, Scope: created.Task.ID,
		Kind: "research", Content: "private derived body", SourceRefs: []string{privateSource.ID},
		ClaimStatus: "accepted", AcceptedBy: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}
	var rootManifest, childManifest w.ContextManifest
	if err := db.Write(t.Context(), func(tx *store.Tx) error {
		var err error
		rootManifest, err = service.PrepareContextInTx(tx, created.Task.ID, 1, 0)
		if err != nil {
			return err
		}
		childManifest, err = service.PrepareContextInTx(tx, child.Task.ID, 1, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	contains := func(manifest w.ContextManifest, ref string) bool {
		for _, item := range manifest.Items {
			if item.Ref == ref {
				return true
			}
		}
		return false
	}
	if !contains(rootManifest, privateSource.ID) || !contains(rootManifest, "memory:private-memory@1") {
		t.Fatalf("source task lost its authorized private inputs: %+v", rootManifest.Items)
	}
	if contains(childManifest, privateSource.ID) || contains(childManifest, "memory:private-memory@1") {
		t.Fatalf("private derived data leaked into child task: %+v", childManifest.Items)
	}
	if !contains(childManifest, publicSource.ID) {
		t.Fatalf("explicit project source was not shared: %+v", childManifest.Items)
	}
}
