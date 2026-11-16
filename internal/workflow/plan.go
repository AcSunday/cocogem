// Package workflow owns the single dynamic collaboration kernel and its
// durable execution. There are no preset workflow templates: every run starts
// from the user's explicit recipients and grows through bounded handoffs.
package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/AcSunday/cocogem/internal/tools"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// maxTargets bounds one parallel fan-out of independently addressed members.
const maxTargets = 4

// Plan materializes the initial steps of one run: exactly one independent
// responder step per addressed member. Multiple addressed members run in
// parallel and are read-only (a blind, independent first pass); a single
// addressed member may write and verify inside the project.
func Plan(runID string, in w.RunInput, agents []w.Agent) ([]w.Step, error) {
	if len(in.Targets) < 1 || len(in.Targets) > maxTargets {
		return nil, fmt.Errorf("select 1–%d recipients", maxTargets)
	}
	byID := map[string]w.Agent{}
	for _, a := range agents {
		if !a.Disabled {
			byID[a.ID] = a
		}
	}
	seen := map[string]bool{}
	out := make([]w.Step, 0, len(in.Targets))
	readOnly := len(in.Targets) > 1
	for _, id := range in.Targets {
		if seen[id] {
			return nil, errors.New("duplicate recipient")
		}
		seen[id] = true
		a, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("selected member %s is unavailable", id)
		}
		instruction := "Answer the user's task directly. Use the tools enabled for this step, cite captured sources, and state limitations honestly."
		if readOnly {
			instruction = "Give your own independent answer to the original user task. Other addressed members are answering in parallel: you cannot see their replies and must not assume or restate them. This pass is read-only; do not modify files or run side-effecting commands. Use read-only evidence and state assumptions, tradeoffs and uncertainties."
		}
		out = append(out, w.Step{ID: w.NewID(), RunID: runID, AgentID: id, AgentName: a.DisplayName, Role: "responder", Instruction: instruction, Status: "pending", ReadOnly: readOnly})
	}
	return out, nil
}

// Step kinds a member retry gives the steps it replans. A retry_result is a
// repaired member whose reply is delivered on its own; a retry_root also heads
// the retry's delivery chain, so it keeps what the step it replaces could do:
// publish, and organise further work.
const (
	kindRetryResult = "retry_result"
	kindRetryRoot   = "retry_root"
)

// freeRoot reports whether a step starts a delivery chain.
func freeRoot(step w.Step) bool {
	return step.Kind == "" || step.Kind == kindRetryResult || step.Kind == kindRetryRoot
}

// deliveryChain follows one root through its continuation or relay successors
// to the terminal member.
func deliveryChain(steps []w.Step, root w.Step) []w.Step {
	chain := []w.Step{root}
	for range len(steps) {
		last := chain[len(chain)-1]
		next := slices.IndexFunc(steps, func(s w.Step) bool {
			return (s.Kind == "continuation" || s.Kind == "relay") && s.FromStepID == last.ID && !s.Converged
		})
		if next < 0 {
			break
		}
		chain = append(chain, steps[next])
	}
	return chain
}

// finalSteps selects the steps whose replies form the run delivery. Every root
// responder is delivered through its continuation or relay chain to the
// terminal member.
func finalSteps(in w.RunInput, steps []w.Step) []w.Step {
	out := []w.Step{}
	for _, root := range steps {
		if !freeRoot(root) {
			continue
		}
		chain := deliveryChain(steps, root)
		out = append(out, chain[len(chain)-1])
	}
	return out
}

// RequiredArtifact reports whether success requires a validated deliverable.
// The dynamic kernel never forces a report template; a member may publish one
// note or research report when durable structured output adds value.
func RequiredArtifact(step w.Step, in w.RunInput) string {
	return ""
}

// findingStatuses is the publication contract shared by the publish_artifact
// schema and ValidateArtifact so the visible enum cannot contradict validation.
var findingStatuses = map[string][]string{
	"note":            {"supported", "confirmed", "disputed", "insufficient_evidence"},
	"research_report": {"supported", "disputed", "insufficient_evidence"},
}

// ValidateArtifact verifies source custody and typed report findings.
func ValidateArtifact(a w.Artifact, required string, sources []w.Source) error {
	if required == "" && a.Type != "note" && a.Type != "research_report" {
		return errors.New("intermediate steps may publish notes or research reports only")
	}
	if !slices.Contains([]string{"note", "research_report"}, a.Type) || a.Type != required && required != "" {
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
	if len(a.Files) > 10 {
		return errors.New("too many output files")
	}
	for _, file := range a.Files {
		source, ok := byID[file.SourceID]
		if !ok || source.Kind != "file_output" || source.OriginalDigest == "" || source.OriginalDigest != file.SHA256 || !slices.Contains(a.SourceIDs, file.SourceID) || file.Name != source.Title || file.Size != source.FileSize || file.Size < 0 {
			return errors.New("output file does not match a captured immutable file")
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
			if a.Type == "research_report" {
				kind = "research finding"
			}
			return fmt.Errorf("invalid %s status %q; allowed statuses: %s", kind, f.Status, strings.Join(statuses, ", "))
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
		}
		if (f.Status == "supported" || f.Status == "confirmed") && !hasOriginal {
			return errors.New("supported or confirmed finding requires a read original source; search snippets and search result pages are leads, not evidence. Fetch the linked original or mark the finding insufficient_evidence")
		}
	}
	return nil
}
