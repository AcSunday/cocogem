package workflow

import (
	"strings"
	"testing"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func planAgents() []w.Agent {
	return []w.Agent{
		{ID: "coord", DisplayName: "协调者", Handle: "coord", Model: "default"},
		{ID: "one", DisplayName: "甲", Handle: "one", Model: "default"},
		{ID: "two", DisplayName: "乙", Handle: "two", Model: "default"},
		{ID: "off", DisplayName: "停用", Handle: "off", Model: "default", Disabled: true},
	}
}

// TestPlanCreatesOneIndependentStepPerRecipient is the A05 regression for the
// single dynamic kernel: a run materializes exactly one responder step per
// addressed member and never expands a preset template.
func TestPlanCreatesOneIndependentStepPerRecipient(t *testing.T) {
	steps, err := Plan("run", w.RunInput{Message: "看一下", Targets: []string{"one"}}, planAgents())
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].AgentID != "one" || steps[0].Role != "responder" {
		t.Fatalf("single recipient plan = %+v", steps)
	}
	if steps[0].ReadOnly {
		t.Fatal("a single addressed member may write")
	}
	if len(steps[0].DependsOn) != 0 {
		t.Fatalf("an addressed member must not depend on a sibling: %+v", steps[0].DependsOn)
	}

	parallel, err := Plan("run", w.RunInput{Message: "各自评估", Targets: []string{"one", "two"}}, planAgents())
	if err != nil {
		t.Fatal(err)
	}
	if len(parallel) != 2 {
		t.Fatalf("parallel plan = %+v", parallel)
	}
	for _, step := range parallel {
		if !step.ReadOnly {
			t.Fatalf("a parallel blind pass must be read-only: %+v", step)
		}
		if len(step.DependsOn) != 0 {
			t.Fatalf("independent views must not depend on each other: %+v", step.DependsOn)
		}
		if !strings.Contains(step.Instruction, "independent") {
			t.Fatalf("parallel instruction must state independence: %q", step.Instruction)
		}
	}
}

// TestPlanRejectsUnusableRecipientSets covers the boundary checks that replaced
// the preset workflow roster validation.
func TestPlanRejectsUnusableRecipientSets(t *testing.T) {
	cases := map[string]w.RunInput{
		"no recipient":    {Message: "x"},
		"too many":        {Message: "x", Targets: []string{"coord", "one", "two", "four", "five"}},
		"duplicate":       {Message: "x", Targets: []string{"one", "one"}},
		"unknown member":  {Message: "x", Targets: []string{"missing"}},
		"disabled member": {Message: "x", Targets: []string{"off"}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Plan("run", in, planAgents()); err == nil {
				t.Fatal("unusable plan accepted")
			}
		})
	}
}

// TestPlanDoesNotDuplicateTheCoordinatorRole proves the kernel has no fixed
// coordinator responsibility: the coordinator is only a member the user may
// address, and it produces exactly one responder step like everyone else.
func TestPlanDoesNotDuplicateTheCoordinatorRole(t *testing.T) {
	steps, err := Plan("run", w.RunInput{Message: "x", Targets: []string{"coord"}}, planAgents())
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].AgentID != "coord" || steps[0].Role != "responder" {
		t.Fatalf("coordinator plan = %+v", steps)
	}
}

// TestNoPresetArtifactObligation proves the kernel no longer requires a report
// template: success is the member's reply, not a forced engineering_report.
func TestNoPresetArtifactObligation(t *testing.T) {
	for _, step := range []w.Step{
		{Role: "responder"},
		{Role: "reviewer"},
		{Role: "coordinator"},
		{Role: "task_intake"},
		{Role: "final_reviewer"},
	} {
		if got := RequiredArtifact(step, w.RunInput{}); got != "" {
			t.Fatalf("role %s unexpectedly requires a preset artifact: %q", step.Role, got)
		}
	}
}

// TestArtifactSourceCustody keeps the first-hand evidence guarantee after the
// preset report types are removed.
func TestArtifactSourceCustody(t *testing.T) {
	sources := []w.Source{
		{ID: "file-1", Kind: "file"},
		{ID: "lead-1", Kind: "search_result", URL: "https://example.com/search?q=x"},
	}
	ok := w.Artifact{Type: "note", Title: "结论", Body: "正文", SourceIDs: []string{"file-1"},
		Findings: []w.Finding{{Title: "f", Detail: "d", Status: "supported", SourceIDs: []string{"file-1"}}}}
	if err := ValidateArtifact(ok, "", sources); err != nil {
		t.Fatalf("valid note rejected: %v", err)
	}
	if err := ValidateArtifact(w.Artifact{Type: "note", Title: "t", Body: "b", SourceIDs: []string{"missing"}}, "", sources); err == nil {
		t.Fatal("unavailable source accepted")
	}
	// A search result page is a lead, never evidence for a supported finding.
	lead := w.Artifact{Type: "note", Title: "t", Body: "b", SourceIDs: []string{"lead-1"},
		Findings: []w.Finding{{Title: "f", Detail: "d", Status: "supported", SourceIDs: []string{"lead-1"}}}}
	if err := ValidateArtifact(lead, "", sources); err == nil {
		t.Fatal("search-result lead accepted as supported evidence")
	}
	// The retired preset report types cannot be published any more.
	if err := ValidateArtifact(w.Artifact{Type: "engineering_report", Title: "t", Body: "b"}, "", sources); err == nil {
		t.Fatal("retired engineering_report is still publishable")
	}
	if err := ValidateArtifact(w.Artifact{Type: "code_analysis", Title: "t", Body: "b"}, "", sources); err == nil {
		t.Fatal("retired code_analysis is still publishable")
	}
}

// TestUnknownArtifactRequiredTypeRejected keeps the required-type contract
// strict for any caller that still passes an explicit requirement.
func TestUnknownArtifactRequiredTypeRejected(t *testing.T) {
	err := ValidateArtifact(w.Artifact{Type: "research_report", Title: "t", Body: "b"}, "note", nil)
	if err == nil || !strings.Contains(err.Error(), "wrong artifact type") {
		t.Fatalf("mismatched required type accepted: %v", err)
	}
}

// TestPlanOnlyTurnsTheSingleRecipientStepReadOnly is the A09 regression: an
// explicit plan-only turn inspects and proposes without acquiring the write
// candidate, and it never changes the global mode or the epoch.
func TestPlanOnlyTurnsTheSingleRecipientStepReadOnly(t *testing.T) {
	steps, err := Plan("run", w.RunInput{Message: "先给方案，别改代码", Targets: []string{"one"}, PlanOnly: true}, planAgents())
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || !steps[0].ReadOnly {
		t.Fatalf("plan-only plan = %+v", steps)
	}
	if !strings.Contains(steps[0].Instruction, "read-only") {
		t.Fatalf("plan-only instruction must state the read-only boundary: %q", steps[0].Instruction)
	}
	_, err = Plan("run", w.RunInput{Message: "实现它", Targets: []string{"one"}}, planAgents())
	if err != nil {
		t.Fatal(err)
	}
}
