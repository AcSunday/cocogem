package workflow

import (
	w "github.com/AcSunday/cocogem/internal/workspace"
	"testing"
)

func TestFinitePlansAndIndependentReview(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "布偶猫", Tools: []string{"publish_artifact"}}, {ID: "b", DisplayName: "缅因猫", Tools: []string{"publish_artifact"}}, {ID: "c", DisplayName: "橘猫", Tools: []string{"publish_artifact"}}}
	for _, tc := range []struct {
		name string
		in   w.RunInput
		n    int
		bad  bool
	}{
		{"free", w.RunInput{Workflow: "free", Targets: []string{"a"}}, 1, false},
		{"discussion", w.RunInput{Workflow: "discussion", Targets: []string{"a", "b"}, Assignments: map[string]string{"coordinator": "c"}}, 3, false},
		{"research", w.RunInput{Workflow: "research", Assignments: map[string]string{"researcher": "a", "critic": "b", "coordinator": "c"}}, 3, false},
		{"self review", w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": "a", "reviewer": "a"}}, 0, true},
		{"unknown member", w.RunInput{Targets: []string{"x"}}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Plan("run", tc.in, agents)
			if (err != nil) != tc.bad || len(plan) != tc.n {
				t.Fatal(plan, err)
			}
			if tc.name == "discussion" && (len(plan[0].DependsOn) != 0 || len(plan[2].DependsOn) != 2) {
				t.Fatal("parallel snapshot dependency lost")
			}
		})
	}
}

func TestArtifactSourceCustody(t *testing.T) {
	a := w.Artifact{Type: "code_analysis", Title: "report", Body: "scope", Findings: []w.Finding{{Title: "issue", Detail: "impact", Status: "confirmed", Severity: "high", SourceIDs: []string{"file"}}}, SourceIDs: []string{"file"}}
	sources := []w.Source{{ID: "file", Kind: "file"}}
	if err := ValidateArtifact(a, "code_analysis", sources); err != nil {
		t.Fatal(err)
	}
	if err := ValidateArtifact(a, "code_analysis", nil); err == nil {
		t.Fatal("invented source accepted")
	}
	sources[0].Kind = "search_snippet"
	if err := ValidateArtifact(a, "code_analysis", sources); err == nil {
		t.Fatal("snippet counted as code evidence")
	}
}

func TestSupportedFindingRequiresReadOriginal(t *testing.T) {
	sources := []w.Source{
		{ID: "serp", Kind: "search_results"},
		{ID: "snippet", Kind: "search_snippet"},
		{ID: "legacy", Kind: "web", URL: "https://www.so.com/s?q=DeBank"},
		{ID: "page", Kind: "web", URL: "https://example.org/article?q=DeBank"},
	}
	artifact := func(ids ...string) w.Artifact {
		return w.Artifact{Type: "research_report", Title: "report", Body: "body", SourceIDs: ids, Findings: []w.Finding{{Title: "claim", Detail: "detail", Status: "supported", SourceIDs: ids}}}
	}
	for _, ids := range [][]string{{"serp"}, {"snippet"}, {"legacy"}} {
		if err := ValidateArtifact(artifact(ids...), "research_report", sources); err == nil {
			t.Fatalf("search leads counted as read evidence: %v", ids)
		}
	}
	if err := ValidateArtifact(artifact("serp", "page"), "research_report", sources); err != nil {
		t.Fatalf("finding backed by a read original was refused: %v", err)
	}
	leadOnly := artifact("serp")
	leadOnly.Findings[0].Status = "insufficient_evidence"
	if err := ValidateArtifact(leadOnly, "research_report", sources); err != nil {
		t.Fatalf("insufficient evidence with a search lead was refused: %v", err)
	}
	note := artifact("serp")
	note.Type = "note"
	if err := ValidateArtifact(note, "", sources); err == nil {
		t.Fatal("note supported finding counted search leads as evidence")
	}
}

func TestRequiredPublicationCannotGrantRevokedPermission(t *testing.T) {
	agents := []w.Agent{{ID: "a", Tools: []string{"read_file"}}, {ID: "b", Tools: []string{"read_file"}}}
	_, err := Plan("run", w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": "a", "reviewer": "b"}}, agents)
	if err == nil {
		t.Fatal("workflow silently granted publication")
	}
}

func TestIntermediateStepsCannotPublishFormalReports(t *testing.T) {
	for _, kind := range []string{"note", "research_report", "code_analysis"} {
		t.Run(kind, func(t *testing.T) {
			err := ValidateArtifact(w.Artifact{Type: kind, Title: "result", Body: "bounded note"}, "", nil)
			if (err == nil) != (kind == "note") {
				t.Fatalf("kind=%s err=%v", kind, err)
			}
		})
	}
}
