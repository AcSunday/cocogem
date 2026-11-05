package contextplan

import (
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestManifestReuseKeyAndMeasurementsBindExactPolicyScopeAndBytes(t *testing.T) {
	first, err := Build(scope(), w.ContextBudget{HostInput: 1000})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := Build(scope(), w.ContextBudget{HostInput: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if first.ReuseKey == "" || first.ReuseKey != replay.ReuseKey {
		t.Fatalf("identical policy/scope/content did not produce a stable reuse key: %+v %+v", first, replay)
	}
	if first.HostInputBytes != len([]byte(first.Brief)) || first.HostInputEstimate <= 0 || first.InlineItems != 1 || first.ReferenceItems != 2 || first.InlineItems+first.ReferenceItems != len(first.Items) {
		t.Fatalf("manifest measurements are inaccurate: %+v", first)
	}
	changedSource := scope()
	changedSource.Entries[1].Digest = "new-source-digest"
	changed, err := Build(changedSource, w.ContextBudget{HostInput: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ReuseKey == first.ReuseKey || changed.ScopeDigest == first.ScopeDigest {
		t.Fatal("source snapshot change reused the previous context key")
	}
	if changed.HostInputBytes != len([]byte(changed.Brief)) || changed.HostInputBytes == 0 {
		t.Fatal("changed rendering did not retain exact byte measurement")
	}
}
