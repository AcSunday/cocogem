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
