package workspace

import "testing"

func TestDeliveredArtifactsMatchExactVersion(t *testing.T) {
	run := Run{ID: "run", Status: "succeeded", Delivery: &Delivery{ArtifactRefs: []ArtifactRef{{ID: "artifact", Version: 2}}}}
	first := Artifact{ID: "artifact", Version: 1, RunID: run.ID}
	second := Artifact{ID: "artifact", Version: 2, RunID: run.ID}
	foreign := Artifact{ID: "artifact", Version: 2, RunID: "other"}

	if IsDeliveredArtifact(run, first) {
		t.Fatal("an undelivered version was accepted")
	}
	if !IsDeliveredArtifact(run, second) {
		t.Fatal("the exact delivered version was rejected")
	}
	if IsDeliveredArtifact(run, foreign) {
		t.Fatal("an artifact from another run was accepted")
	}
	if got := FinalArtifacts(run, []Artifact{first, second, foreign}); len(got) != 1 || got[0].Version != 2 {
		t.Fatalf("final artifacts = %+v", got)
	}
}

func TestLegacyTerminalArtifactsStayVisibleWithoutDelivery(t *testing.T) {
	artifact := Artifact{ID: "artifact", Version: 1, RunID: "legacy"}
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{"queued", false},
		{"running", false},
		{"canceling", false},
		{"succeeded", true},
		{"failed", true},
		{"canceled", true},
		{"interrupted", true},
	} {
		run := Run{ID: "legacy", Status: tc.status}
		if got := IsDeliveredArtifact(run, artifact); got != tc.want {
			t.Fatalf("status %s: delivered = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestRecordedDeliveryRequiresSucceededRun(t *testing.T) {
	artifact := Artifact{ID: "artifact", Version: 1, RunID: "run"}
	delivery := &Delivery{ArtifactRefs: []ArtifactRef{{ID: "artifact", Version: 1}}}
	for _, status := range []string{"queued", "running", "canceling", "failed", "canceled", "interrupted"} {
		run := Run{ID: "run", Status: status, Delivery: delivery}
		if IsDeliveredArtifact(run, artifact) {
			t.Fatalf("status %s: a non-succeeded run exposed a draft artifact", status)
		}
	}
	if !IsDeliveredArtifact(Run{ID: "run", Status: "succeeded", Delivery: delivery}, artifact) {
		t.Fatal("the succeeded run's exact delivered version was rejected")
	}
}
