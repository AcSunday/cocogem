// Package workflow owns finite collaboration plans and their durable execution.
package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Plan validates exact recipients and materializes one finite built-in workflow.
func Plan(runID string, in w.RunInput, agents []w.Agent) ([]w.Step, error) {
	byID := map[string]w.Agent{}
	for _, a := range agents {
		if !a.Disabled {
			byID[a.ID] = a
		}
	}
	out := []w.Step{}
	add := func(id, role, instruction string, deps []string) error {
		a, ok := byID[id]
		if !ok {
			return errors.New("selected member is unavailable")
		}
		if (role == "coordinator" || role == "reviewer") && !slices.Contains(a.Tools, "publish_artifact") {
			return errors.New("this role requires explicit publish_artifact permission")
		}
		out = append(out, w.Step{ID: w.NewID(), RunID: runID, AgentID: id, AgentName: a.DisplayName, Role: role, Instruction: instruction, DependsOn: deps, Status: "pending"})
		return nil
	}
	if in.Workflow == "" || in.Workflow == "free" {
		if len(in.Targets) < 1 || len(in.Targets) > 4 {
			return nil, errors.New("select 1–4 recipients")
		}
		seen := map[string]bool{}
		for _, id := range in.Targets {
			if seen[id] {
				return nil, errors.New("duplicate recipient")
			}
			seen[id] = true
			if err := add(id, "responder", "Answer the user's task; cite sources and distinguish limitations.", nil); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	switch in.Workflow {
	case "discussion":
		if len(in.Targets) < 2 || len(in.Targets) > 4 {
			return nil, errors.New("discussion requires 2–4 members")
		}
		seen := map[string]bool{}
		for _, id := range in.Targets {
			if seen[id] {
				return nil, errors.New("duplicate discussion member")
			}
			seen[id] = true
			if err := add(id, "discussant", "Give an independent view. State assumptions, tradeoffs and uncertainties.", nil); err != nil {
				return nil, err
			}
		}
		deps := []string{}
		for _, step := range out {
			deps = append(deps, step.ID)
		}
		if err := add(in.Assignments["coordinator"], "coordinator", "Summarize all views, preserve disagreements and publish a note.", deps); err != nil {
			return nil, err
		}
	case "research":
		r, c, co := in.Assignments["researcher"], in.Assignments["critic"], in.Assignments["coordinator"]
		if r == c {
			return nil, errors.New("researcher and critic must be different members")
		}
		if err := add(r, "researcher", "Research the user question using allowed sources. Fetch originals before presenting them as read. State findings and uncertainties.", nil); err != nil {
			return nil, err
		}
		if err := add(c, "critic", "Independently read relevant sources. Challenge dates, evidence, assumptions and missing alternatives. Preserve disputed conclusions.", []string{out[0].ID}); err != nil {
			return nil, err
		}
		if err := add(co, "coordinator", "Integrate research and critique; publish a research_report including scope, method, findings, disagreements, limitations and next steps.", []string{out[0].ID, out[1].ID}); err != nil {
			return nil, err
		}
	case "code_analysis":
		a, r := in.Assignments["analyst"], in.Assignments["reviewer"]
		if a == r {
			return nil, errors.New("analyst and reviewer must be different members")
		}
		if err := add(a, "analyst", "Read relevant source files. Analyze the requested behavior and report concrete findings with file evidence. Do not execute code or claim runtime verification.", nil); err != nil {
			return nil, err
		}
		if err := add(r, "reviewer", "Read relevant files independently; confirm or dispute each finding and publish code_analysis with severity, impact, file source IDs and limitations. No runtime verification is available.", []string{out[0].ID}); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unsupported workflow")
	}
	unique := map[string]bool{}
	for _, s := range out {
		unique[s.AgentID] = true
	}
	if len(unique) > 4 {
		return nil, errors.New("workflow exceeds four members")
	}
	return out, nil
}

// RequiredArtifact determines whether success requires a validated deliverable.
func RequiredArtifact(step w.Step, in w.RunInput) string {
	if step.Kind != "" && (in.Workflow == "free" || in.Workflow == "") {
		return ""
	}
	if step.Role == "coordinator" {
		if in.Workflow == "research" {
			return "research_report"
		}
		return "note"
	}
	if step.Role == "reviewer" {
		return "code_analysis"
	}
	return ""
}

// findingStatuses is the publication contract shared by the publish_artifact
// schema and ValidateArtifact so the visible enum cannot contradict validation.
var findingStatuses = map[string][]string{
	"note":            {"supported", "confirmed", "disputed", "insufficient_evidence"},
	"research_report": {"supported", "disputed", "insufficient_evidence"},
	"code_analysis":   {"confirmed", "disputed", "insufficient_evidence"},
}

// ValidateArtifact verifies source custody and typed report findings.
func ValidateArtifact(a w.Artifact, required string, sources []w.Source) error {
	if required == "" && a.Type != "note" {
		return errors.New("intermediate steps may publish notes only; final reports require the designated coordinator or reviewer")
	}
	if !slices.Contains([]string{"note", "research_report", "code_analysis"}, a.Type) || a.Type != required && required != "" {
		return errors.New("wrong artifact type")
	}
	if strings.TrimSpace(a.Title) == "" || len(a.Title) > 500 || strings.TrimSpace(a.Body) == "" || len(a.Body) > 48000 {
		return errors.New("invalid artifact title or body")
	}
	byID := map[string]w.Source{}
	for _, s := range sources {
		byID[s.ID] = s
	}
	for _, id := range a.SourceIDs {
		if _, ok := byID[id]; !ok {
			return errors.New("artifact cites an unavailable source")
		}
	}
	if len(a.Findings) > 50 || len(a.Limitations) > 30 {
		return errors.New("report exceeds item limits")
	}
	for _, f := range a.Findings {
		if f.Title == "" || f.Detail == "" {
			return errors.New("invalid finding: title and detail are required")
		}
		statuses := findingStatuses[a.Type]
		if !slices.Contains(statuses, f.Status) {
			kind := "finding"
			switch a.Type {
			case "research_report":
				kind = "research finding"
			case "code_analysis":
				kind = "code finding"
			}
			return fmt.Errorf("invalid %s status %q; allowed statuses: %s", kind, f.Status, strings.Join(statuses, ", "))
		}
		if a.Type == "code_analysis" && !slices.Contains([]string{"high", "medium", "low"}, f.Severity) {
			return errors.New("code finding requires severity")
		}
		if a.Type == "code_analysis" && len(f.SourceIDs) == 0 {
			return errors.New("every code finding requires file evidence")
		}
		if len(f.SourceIDs) == 0 && f.Status != "insufficient_evidence" {
			return errors.New("supported finding requires captured evidence")
		}
		hasOriginal := false
		for _, id := range f.SourceIDs {
			if !slices.Contains(a.SourceIDs, id) {
				return errors.New("artifact source_ids must include finding references")
			}
			s, ok := byID[id]
			if !ok {
				return errors.New("finding cites an unavailable source")
			}
			if evidenceStrength(s.Kind) != "lead" && !tools.IsSearchResultURL(s.URL) {
				hasOriginal = true
			}
			if a.Type == "code_analysis" && s.Kind != "file" && s.Kind != "native_file" {
				return fmt.Errorf("code finding requires file evidence: %s", id)
			}
		}
		if (f.Status == "supported" || f.Status == "confirmed") && !hasOriginal {
			return errors.New("supported or confirmed finding requires a read original source; search snippets and search result pages are leads, not evidence. Fetch the linked original or mark the finding insufficient_evidence")
		}
	}
	return nil
}
