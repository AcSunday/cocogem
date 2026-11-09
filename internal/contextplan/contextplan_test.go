package contextplan

import (
	"strings"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func scope() w.EligibleScope {
	return w.EligibleScope{WorkspaceID: "ws", TaskID: "task", TaskRevision: 20, CollaborationScopeID: "scope", Entries: []w.ContextEntry{
		{Ref: "task@20", Kind: "task", Digest: "current", Required: true, Content: "当前约束：不得新增 Redis"},
		{Ref: "source@1", Kind: "source", Digest: "source-digest"},
		{Ref: "task@1", Kind: "task", Digest: "old", Content: "旧约束：可以使用 Redis"},
	}}
}

func TestBuildKeepsCurrentRequirementsAndExactOriginalEntry(t *testing.T) {
	manifest, err := Build(scope(), w.ContextBudget{HostInput: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manifest.Brief, "不得新增 Redis") || strings.Contains(manifest.Brief, "可以使用 Redis") {
		t.Fatalf("brief revived a replaced constraint: %s", manifest.Brief)
	}
	if !strings.Contains(manifest.Brief, "source@1") || manifest.RenderedDigest == manifest.ScopeDigest || manifest.WorkDigest == manifest.RenderedDigest {
		t.Fatalf("manifest digests or exact entry are invalid: %+v", manifest)
	}
	if manifest.Items[0].Mode == "inline" && !manifest.Items[0].Required {
		t.Fatal("optional old material displaced required state")
	}
}

func TestMinimumStateAndKnownWindowFailClosed(t *testing.T) {
	tooSmall := scope()
	tooSmall.Entries[0].Content = strings.Repeat("必须完整保留", 100)
	if _, err := Build(tooSmall, w.ContextBudget{HostInput: 20}); !IsCode(err, "context_budget_exhausted") {
		t.Fatalf("required state was silently truncated: %v", err)
	}
	if _, err := Build(scope(), w.ContextBudget{HostInput: 1000, KnownModelWindow: 1100, OutputReserve: 200}); !IsCode(err, "context_budget_exhausted") {
		t.Fatalf("known native window was ignored: %v", err)
	}
}

func TestReadPageBindsScopeDigestAndRecordsOnlyDeliveredBytes(t *testing.T) {
	manifest, err := Build(scope(), w.ContextBudget{})
	if err != nil {
		t.Fatal(err)
	}
	manifest.ID = "manifest"
	content := "0123456789abcdef"
	for index := range manifest.Items {
		if manifest.Items[index].Ref == "source@1" {
			manifest.Items[index].Digest = Digest(content)
		}
	}
	page, receipt, err := ReadPage(manifest, "source@1", content, Digest(content), "", 8)
	if err != nil {
		t.Fatal(err)
	}
	if page.Content != "01234567" || !page.HasMore || receipt.Bytes != 8 || receipt.Complete {
		t.Fatalf("page/receipt = %+v / %+v", page, receipt)
	}
	if _, _, err := ReadPage(manifest, "source@1", "changed", Digest(content), "", 8); !IsCode(err, "disclosure_denied") {
		t.Fatalf("changed original was accepted: %v", err)
	}
	if _, _, err := ReadPage(manifest, "task@1", "旧约束：可以使用 Redis", Digest("旧约束：可以使用 Redis"), "", 8); !IsCode(err, "disclosure_denied") {
		t.Fatalf("excluded old content became readable: %v", err)
	}
}
