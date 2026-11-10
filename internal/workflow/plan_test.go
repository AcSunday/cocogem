package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
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
		{"discussion", w.RunInput{Workflow: "discussion", Targets: []string{"a", "b"}, Assignments: map[string]string{"coordinator": "c"}}, 8, false},
		{"research", w.RunInput{Workflow: "research", Assignments: map[string]string{"researcher": "a", "critic": "b", "coordinator": "c"}}, 5, false},
		{"self review", w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": "a", "reviewer": "a"}}, 0, true},
		{"unknown member", w.RunInput{Targets: []string{"x"}}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Plan("run", tc.in, agents)
			if (err != nil) != tc.bad || len(plan) != tc.n {
				t.Fatal(plan, err)
			}
		})
	}
}

func TestResearchPlanHandsExactContextToEachLaterStep(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "研究员", Tools: []string{"publish_artifact"}}, {ID: "b", DisplayName: "质疑者", Tools: []string{"publish_artifact"}}, {ID: "c", DisplayName: "协调者", Tools: []string{"publish_artifact"}}}
	steps, err := Plan("run", w.RunInput{Workflow: "research", Assignments: map[string]string{"researcher": "a", "critic": "b", "coordinator": "c"}}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 {
		t.Fatalf("research steps = %d", len(steps))
	}
	roles := []string{}
	for _, step := range steps {
		roles = append(roles, step.Role)
	}
	if !slices.Equal(roles, []string{"researcher", "critic", "researcher", "critic", "coordinator"}) {
		t.Fatalf("research roles = %v", roles)
	}
	if !slices.Equal(steps[1].DependsOn, []string{steps[0].ID}) {
		t.Fatalf("critique deps = %v", steps[1].DependsOn)
	}
	if !slices.Equal(steps[2].DependsOn, []string{steps[0].ID, steps[1].ID}) {
		t.Fatalf("researcher response deps = %v", steps[2].DependsOn)
	}
	if !slices.Equal(steps[3].DependsOn, []string{steps[0].ID, steps[1].ID, steps[2].ID}) {
		t.Fatalf("critic re-check deps = %v", steps[3].DependsOn)
	}
	if !slices.Equal(steps[4].DependsOn, []string{steps[0].ID, steps[1].ID, steps[2].ID, steps[3].ID}) {
		t.Fatalf("final report deps = %v", steps[4].DependsOn)
	}
	in := w.RunInput{Workflow: "research"}
	for i, step := range steps {
		want := ""
		if i == len(steps)-1 {
			want = "research_report"
		}
		if got := RequiredArtifact(step, in); got != want {
			t.Fatalf("step %d artifact = %q, want %q", i, got, want)
		}
	}
}

func TestPlansReuseTheSameMemberForPairedRoles(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "甲", Tools: []string{"publish_artifact"}}, {ID: "b", DisplayName: "乙", Tools: []string{"publish_artifact"}}}
	research, err := Plan("run", w.RunInput{Workflow: "research", Assignments: map[string]string{"researcher": "a", "critic": "b", "coordinator": "a"}}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(research) != 5 || research[0].AgentID != "a" || research[2].AgentID != "a" || research[4].AgentID != "a" || research[1].AgentID != "b" || research[3].AgentID != "b" {
		t.Fatalf("research reuse = %+v", research)
	}
	analysis, err := Plan("run", w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": "a", "reviewer": "b"}}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis) != 4 || analysis[0].AgentID != "a" || analysis[2].AgentID != "a" || analysis[1].AgentID != "b" || analysis[3].AgentID != "b" {
		t.Fatalf("analysis reuse = %+v", analysis)
	}
	if analysis[1].Role != "critic" || analysis[3].Role != "final_reviewer" || analysis[1].AgentID != analysis[3].AgentID {
		t.Fatalf("one reviewer member must serve critic and final_reviewer: %+v", analysis)
	}
}

func TestReadOnlyAnalysisUsesCriticThenFinalReviewerPublisher(t *testing.T) {
	agents := []w.Agent{{ID: "a", DisplayName: "分析", Tools: []string{"read_file", "publish_artifact"}}, {ID: "b", DisplayName: "复核", Tools: []string{"read_file", "publish_artifact"}}}
	for _, in := range []w.RunInput{
		{Workflow: "code_analysis", Assignments: map[string]string{"analyst": "a", "reviewer": "b"}},
	} {
		steps, err := Plan("run", in, agents)
		if err != nil {
			t.Fatal(err)
		}
		roles := []string{}
		for _, step := range steps {
			roles = append(roles, step.Role)
		}
		if !slices.Equal(roles, []string{"analyst", "critic", "analyst", "final_reviewer"}) {
			t.Fatalf("%s roles = %v", in.Workflow, roles)
		}
		if !slices.Equal(steps[3].DependsOn, []string{steps[0].ID, steps[1].ID, steps[2].ID}) {
			t.Fatalf("%s final deps = %v", in.Workflow, steps[3].DependsOn)
		}
		if !strings.Contains(steps[3].Instruction, "initial analysis") || !strings.Contains(steps[3].Instruction, "every original finding") {
			t.Fatalf("%s final reviewer does not reconcile the initial analysis: %q", in.Workflow, steps[3].Instruction)
		}
		if RequiredArtifact(steps[1], in) != "" {
			t.Fatalf("%s intermediate critic received a publication obligation", in.Workflow)
		}
		if RequiredArtifact(steps[3], in) != "code_analysis" {
			t.Fatalf("%s final reviewer does not publish code_analysis", in.Workflow)
		}
		if got := w.RoleTools("code_analysis", "", "critic"); len(got) != 0 {
			t.Fatalf("critic tools = %v", got)
		}
		if !slices.Contains(w.RoleTools("code_analysis", "", "final_reviewer"), "publish_artifact") {
			t.Fatal("final_reviewer is not guaranteed publication")
		}
	}
}

func TestLegacyTwoStepReviewerStillPublishes(t *testing.T) {
	legacy := w.Step{Role: "reviewer", Status: "pending"}
	if got := RequiredArtifact(legacy, w.RunInput{Workflow: "code_analysis"}); got != "code_analysis" {
		t.Fatalf("legacy code_analysis reviewer artifact = %q", got)
	}
	if got := RequiredArtifact(w.Step{Role: "final_reviewer", Status: "pending"}, w.RunInput{Workflow: "engineering", Assignments: map[string]string{"engineer": "a", "reviewer": "b"}}); got != "engineering_report" {
		t.Fatalf("engineering final reviewer artifact = %q", got)
	}
	if got := RequiredArtifact(w.Step{Role: "final_reviewer", Status: "pending"}, w.RunInput{Workflow: "engineering", Assignments: map[string]string{"analyst": "a", "reviewer": "b"}}); got != "code_analysis" {
		t.Fatalf("legacy engineering final reviewer artifact = %q", got)
	}
	if got := RequiredArtifact(w.Step{Role: "critic", Status: "pending"}, w.RunInput{Workflow: "code_analysis"}); got != "" {
		t.Fatalf("intermediate critic artifact = %q", got)
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

// TestLegacyToolArraysDoNotGatePublication keeps the retirement contract: the
// publish capability comes from the runtime catalog, so a legacy array that omits
// publish_artifact cannot exclude a member, and a legacy array that lists extra
// write tools cannot grant them either (the role/task checks still apply).
func TestLegacyToolArraysDoNotGatePublication(t *testing.T) {
	agents := []w.Agent{
		{ID: "a", Tools: []string{"read_file"}},
		{ID: "b", Tools: []string{"read_file", "write_file", "run_command"}},
	}
	plan, err := Plan("run", w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": "a", "reviewer": "b"}}, agents)
	if err != nil {
		t.Fatalf("legacy tool arrays gated a valid plan: %v", err)
	}
	if RequiredArtifact(plan[len(plan)-1], w.RunInput{Workflow: "code_analysis"}) == "" {
		t.Fatalf("final reviewer has no publication obligation: %+v", plan)
	}
	e := Engine{}
	readOnly := w.Run{Workspace: w.Workspace{Root: t.TempDir()}, Input: w.RunInput{Workflow: "code_analysis"}}
	for _, tool := range e.allowedTools(readOnly, agents[1], w.Step{Role: "analyst"}) {
		if tool == "write_file" || tool == "patch_file" || tool == "run_test" || tool == "run_command" {
			t.Fatalf("a legacy write grant leaked into a read-only role: %s", tool)
		}
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

func engineeringAgents() []w.Agent {
	return []w.Agent{
		{ID: "dev", DisplayName: "开发", Tools: []string{"read_file", "write_file", "patch_file", "run_test", "publish_artifact"}},
		{ID: "review", DisplayName: "复核", Tools: []string{"read_file", "run_test", "publish_artifact"}},
	}
}

func TestEngineeringPlanUsesEngineerAndReviewer(t *testing.T) {
	agents := engineeringAgents()
	in := w.RunInput{Workflow: "engineering", Assignments: map[string]string{"engineer": "dev", "reviewer": "review"}}
	steps, err := Plan("run", in, agents)
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	for _, step := range steps {
		roles = append(roles, step.Role)
	}
	if len(steps) != 4 || !slices.Equal(roles, []string{"engineer", "reviewer", "engineer", "final_reviewer"}) {
		t.Fatalf("engineering plan roles = %v", roles)
	}
	if len(steps[2].DependsOn) != 2 || steps[2].DependsOn[0] != steps[0].ID || steps[2].DependsOn[1] != steps[1].ID {
		t.Fatal("engineering response does not consume the initial work and review")
	}
	if !slices.Contains(steps[3].DependsOn, steps[0].ID) || !slices.Contains(steps[3].DependsOn, steps[1].ID) || !slices.Contains(steps[3].DependsOn, steps[2].ID) {
		t.Fatal("final review does not receive the initial work, review and response")
	}
	if RequiredArtifact(steps[1], in) != "" || RequiredArtifact(steps[3], in) != "engineering_report" || RequiredArtifact(steps[0], in) != "" {
		t.Fatal("engineering artifact roles are wrong")
	}
	_, err = Plan("run", w.RunInput{Workflow: "engineering", Assignments: map[string]string{"engineer": "dev", "reviewer": "dev"}}, agents)
	if err == nil {
		t.Fatal("engineer and reviewer may not be the same member")
	}
	if _, err := Plan("run", w.RunInput{Workflow: "engineering", Assignments: map[string]string{"engineer": "dev"}}, agents); err == nil || !strings.Contains(err.Error(), "missing required reviewer") {
		t.Fatalf("missing reviewer err = %v", err)
	}
	// The engineer uses the member's granted tools, so a read-only member can
	// still serve and simply reports without writing or running tests.
	limited := []w.Agent{{ID: "dev", DisplayName: "执行", Tools: []string{"read_file", "publish_artifact"}}, agents[1]}
	if _, err := Plan("run", w.RunInput{Workflow: "engineering", Assignments: map[string]string{"engineer": "dev", "reviewer": "review"}}, limited); err != nil {
		t.Fatalf("read-only engineer was rejected: %v", err)
	}
	// A coordinator-selected engineering method is writable under the member's
	// granted tools; analysis is a behavior inside the same plan.
	selectedSteps, err := Plan("run", in, agents)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(selectedSteps[0].Instruction, "reproduce") || !strings.Contains(selectedSteps[2].Instruction, "fix them") {
		t.Fatal("engineering implementation instructions were not applied")
	}
}

func TestEngineeringToolGatingIntersectsRole(t *testing.T) {
	e := Engine{}
	a := w.Agent{ID: "dev", Tools: []string{"read_file", "write_file", "patch_file", "run_test"}}
	develop := w.Run{Workspace: w.Workspace{Root: t.TempDir()}, Input: w.RunInput{Workflow: "engineering"}}
	engineer := e.allowedTools(develop, a, w.Step{Role: "engineer"})
	if !slices.Contains(engineer, "write_file") || !slices.Contains(engineer, "patch_file") || !slices.Contains(engineer, "run_test") {
		t.Fatalf("engineer tools = %v", engineer)
	}
	reviewer := e.allowedTools(develop, a, w.Step{Role: "reviewer"})
	if slices.Contains(reviewer, "write_file") || slices.Contains(reviewer, "patch_file") || !slices.Contains(reviewer, "run_test") || !slices.Contains(reviewer, "read_file") {
		t.Fatalf("reviewer tools = %v", reviewer)
	}
	final := e.allowedTools(develop, a, w.Step{Role: "final_reviewer"})
	if slices.Contains(final, "write_file") || slices.Contains(final, "patch_file") {
		t.Fatalf("final reviewer can write: %v", final)
	}
	legacy := w.Run{Workspace: develop.Workspace, Input: w.RunInput{Workflow: "code_analysis"}}
	for _, role := range []string{"analyst", "critic", "final_reviewer"} {
		gated := e.allowedTools(legacy, a, w.Step{Role: role})
		if slices.Contains(gated, "write_file") || slices.Contains(gated, "patch_file") || slices.Contains(gated, "run_test") {
			t.Fatalf("legacy code analysis role %s received engineering tools: %v", role, gated)
		}
	}
	// A legacy array without patch_file no longer narrows the runtime catalog; the
	// engineer role keeps the catalog's write/test tools.
	noPatch := w.Agent{ID: "dev", Tools: []string{"read_file", "write_file", "run_test"}}
	engineer = e.allowedTools(develop, noPatch, w.Step{Role: "engineer"})
	if !slices.Contains(engineer, "write_file") || !slices.Contains(engineer, "patch_file") || !slices.Contains(engineer, "run_test") {
		t.Fatalf("legacy array narrowed the engineer catalog: %v", engineer)
	}
}

func TestEngineeringReviewerCanPublishWithoutRunTest(t *testing.T) {
	engineer := w.Agent{ID: "dev", DisplayName: "执行", Tools: []string{"read_file", "write_file", "run_test"}}
	publisher := w.Agent{ID: "publisher", DisplayName: "仅发布", Tools: []string{"read_file", "publish_artifact"}}
	agents := []w.Agent{engineer, publisher}
	steps, err := Plan("run", w.RunInput{Workflow: "engineering", Assignments: map[string]string{"engineer": engineer.ID, "reviewer": publisher.ID}}, agents)
	if err != nil {
		t.Fatal(err)
	}
	final := steps[len(steps)-1]
	if final.Role != "final_reviewer" || final.AgentID != publisher.ID {
		t.Fatalf("engineering final reviewer = %+v", final)
	}
	// The legacy read-only workflow still accepts a publisher without run_test.
	if _, err := Plan("run", w.RunInput{Workflow: "code_analysis", Assignments: map[string]string{"analyst": engineer.ID, "reviewer": publisher.ID}}, agents); err != nil {
		t.Fatalf("code_analysis reviewer without run_test was refused: %v", err)
	}
}

func TestEngineeringReportRequiresCurrentDiffAndPassingTest(t *testing.T) {
	root := t.TempDir()
	content := "package calc\n\nfunc Add(a, b int) int { return a + b }\n"
	if err := os.WriteFile(filepath.Join(root, "calc.go"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])
	run := w.Run{ID: "run", Workspace: w.Workspace{Root: root}, Input: w.RunInput{Workflow: "engineering"}}
	workspace, err := tools.WorkspaceDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	diff := w.Source{ID: "diff", RunID: run.ID, Kind: "diff", Path: "calc.go", SHA256: digest, CapturedAt: "2026-10-28T10:00:00Z"}
	test := w.Source{ID: "test", RunID: run.ID, Kind: "test", Path: ".", SHA256: workspace, CapturedAt: "2026-10-28T10:00:01Z", ExitCode: 0}
	artifact := w.Artifact{Type: "engineering_report", Title: "交付", Body: "scope and result", SourceIDs: []string{"diff", "test"}}
	sources := []w.Source{diff, test}
	if err := ValidateArtifact(artifact, "engineering_report", sources); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEngineeringEvidence(artifact, run, sources); err != nil {
		t.Fatal(err)
	}
	missingTest := artifact
	missingTest.SourceIDs = []string{"diff"}
	if err := ValidateEngineeringEvidence(missingTest, run, sources); err == nil {
		t.Fatal("report without passing test evidence was accepted")
	}
	failing := append([]w.Source{}, sources...)
	failing[1].ExitCode = 1
	if err := ValidateEngineeringEvidence(artifact, run, failing); err == nil {
		t.Fatal("report with failing test evidence was accepted")
	}
	stale := append([]w.Source{}, sources...)
	stale[0].SHA256 = hex.EncodeToString(make([]byte, 32))
	if err := ValidateEngineeringEvidence(artifact, run, stale); err == nil {
		t.Fatal("report with stale diff hash was accepted")
	}
	changedTree := append([]w.Source{}, sources...)
	changedTree[1].SHA256 = hex.EncodeToString(make([]byte, 32))
	if err := ValidateEngineeringEvidence(artifact, run, changedTree); err == nil {
		t.Fatal("report citing a test whose tested workspace no longer matches was accepted")
	}
	noDigest := append([]w.Source{}, sources...)
	noDigest[1].SHA256 = ""
	if err := ValidateEngineeringEvidence(artifact, run, noDigest); err == nil {
		t.Fatal("test evidence without a workspace digest was accepted")
	}
	early := append([]w.Source{}, sources...)
	early[1].CapturedAt = "2026-10-28T09:59:59Z"
	if err := ValidateEngineeringEvidence(artifact, run, early); err == nil {
		t.Fatal("test captured before the last write was accepted")
	}
	otherRun := append([]w.Source{}, sources...)
	otherRun[0].RunID = "other"
	if err := ValidateEngineeringEvidence(artifact, run, otherRun); err == nil {
		t.Fatal("diff from another run was accepted")
	}
}

func TestEngineeringReportMayRecordEvidenceGapButNotConfirm(t *testing.T) {
	run := w.Run{ID: "run", Workspace: w.Workspace{Root: t.TempDir()}, Input: w.RunInput{Workflow: "engineering"}}
	gap := w.Artifact{Type: "engineering_report", Title: "未能验证", Body: "测试沙箱不可用", Findings: []w.Finding{{Title: "测试未运行", Detail: "run_test 返回 sandbox_unavailable", Status: "insufficient_evidence", Severity: "high"}}}
	if err := ValidateArtifact(gap, "engineering_report", nil); err != nil {
		t.Fatalf("evidence-gap finding was rejected: %v", err)
	}
	if err := ValidateEngineeringEvidence(gap, run, nil); err != nil {
		t.Fatalf("honest evidence-gap report was rejected: %v", err)
	}
	claim := gap
	claim.Findings = []w.Finding{{Title: "已修复", Detail: "模型自称通过", Status: "confirmed", Severity: "high"}}
	if err := ValidateArtifact(claim, "engineering_report", nil); err == nil {
		t.Fatal("confirmed finding without evidence was accepted")
	}
	emptyClaim := w.Artifact{Type: "engineering_report", Title: "完成", Body: "已交付"}
	if err := ValidateEngineeringEvidence(emptyClaim, run, nil); err == nil {
		t.Fatal("empty report without evidence was accepted as verified")
	}
}
