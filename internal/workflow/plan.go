// Package workflow owns finite collaboration plans and their durable execution.
package workflow

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
		// Role capability comes from the runtime catalog, not from a retired
		// member tool array.
		catalog := w.StandardToolCatalog()
		for _, tool := range w.RoleTools(in.Workflow, "", role) {
			if !slices.Contains(catalog, tool) {
				return fmt.Errorf("this role requires the %s host capability", tool)
			}
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
		}
		last := func() string { return out[len(out)-1].ID }
		// Independent views share one snapshot and depend on nothing, so no
		// sibling reply is in their handoff or readable scope.
		views := make([]string, 0, len(in.Targets))
		for _, id := range in.Targets {
			if err := add(id, "discussant", "Give an independent view on the original user task. Use the shared input and prior history only; do not read or assume other members' replies in this discussion. State assumptions, tradeoffs and uncertainties. Do not publish an artifact. Match the answer length to the task; no fixed report template.", nil); err != nil {
				return nil, err
			}
			views = append(views, last())
		}
		// Serial cross-critique: each critic sees every view and every earlier
		// critique, so later challenges can answer points already raised.
		critiques := make([]string, 0, len(in.Targets))
		for _, id := range in.Targets {
			deps := append(slices.Clone(views), critiques...)
			if err := add(id, "critic", "Read the other members' independent views and any completed critiques in the exact upstream handoff. Challenge or extend specific arguments by quoting or referencing them; do not restate the whole discussion. Do not publish an artifact. Match the answer length to the task.", deps); err != nil {
				return nil, err
			}
			critiques = append(critiques, last())
		}
		// The draft is deliberately not a coordinator: only the final step has
		// a publication obligation.
		draftDeps := append(slices.Clone(views), critiques...)
		if err := add(in.Assignments["coordinator"], "synthesizer", "Draft a concise synthesis of the independent views and completed critiques: agreements, real disagreements, corrections and open questions. Do not invent consensus or treat agreement as evidence. This is an intermediate draft, not the final deliverable; return it as your step reply and do not publish an artifact.", draftDeps); err != nil {
			return nil, err
		}
		draft := last()
		checks := make([]string, 0, len(in.Targets))
		for i, id := range in.Targets {
			// Your own critique carries your revised position, so the check
			// must not fall back to the version your critique corrected.
			if err := add(id, "checker", "Check the draft synthesis against your own independent view and your own critique in the handoff; where they differ, your critique states your revised position and prevails. Reply with only one JSON object, no prose or code fence: {\"status\":\"confirmed\",\"correction\":\"\"} when the draft accurately represents your revised position, or {\"status\":\"disputed\",\"correction\":\"the specific correction\"} when it misreads you or a real disagreement remains (at most 4096 UTF-8 bytes in total). Do not rewrite the synthesis or publish an artifact.", []string{draft, views[i], critiques[i]}); err != nil {
				return nil, err
			}
			checks = append(checks, last())
		}
		finalDeps := append([]string{draft}, checks...)
		if err := add(in.Assignments["coordinator"], "coordinator", "Deliver the final answer to the original user task from the draft synthesis and the contributor checks in the handoff. The check messages are JSON records with status and correction: apply every correction, keep every disputed item visible as a genuine disagreement, and never present agreement or confirmation as verified evidence. Publish exactly one discussion note with this final content; match its length to the task.", finalDeps); err != nil {
			return nil, err
		}
	case "research":
		r, c, co := in.Assignments["researcher"], in.Assignments["critic"], in.Assignments["coordinator"]
		if r == c {
			return nil, errors.New("researcher and critic must be different members")
		}
		last := func() string { return out[len(out)-1].ID }
		if err := add(r, "researcher", "Research the user question using allowed sources. Fetch originals before presenting them as read. State findings and uncertainties.", nil); err != nil {
			return nil, err
		}
		research := last()
		if err := add(c, "critic", "Independently read relevant sources. Challenge dates, evidence, assumptions, missing alternatives and unsupported conclusions. Reply as a plain step message with exact references; do not publish an artifact.", []string{research}); err != nil {
			return nil, err
		}
		critique := last()
		if err := add(r, "researcher", "Respond point by point to the critic's exact challenges using the initial research and critique in the handoff. Add or correct evidence where possible, concede unsupported claims, and keep unresolved disagreement visible. Reply as a plain step message; do not publish an artifact.", []string{research, critique}); err != nil {
			return nil, err
		}
		response := last()
		if err := add(c, "critic", "Re-check the researcher's point-by-point response against the initial research and your own critique in the exact upstream handoff. Confirm corrected points and keep every unresolved item explicit. Reply as a plain step message; do not publish an artifact.", []string{research, critique, response}); err != nil {
			return nil, err
		}
		review := last()
		if err := add(co, "coordinator", "Deliver the final research_report from the initial research, critique, point-by-point response and critic re-check in the handoff. Include scope, method, findings, disagreements, source IDs, limitations and next steps, and keep unresolved items visible. Publish exactly one research_report.", []string{research, critique, response, review}); err != nil {
			return nil, err
		}
	case "code_analysis":
		a, r := in.Assignments["analyst"], in.Assignments["reviewer"]
		if a == r {
			return nil, errors.New("analyst and reviewer must be different members")
		}
		last := func() string { return out[len(out)-1].ID }
		if err := add(a, "analyst", "Read relevant source files. Analyze the requested behavior and report concrete findings with file evidence. Do not execute code or claim runtime verification.", nil); err != nil {
			return nil, err
		}
		analysis := last()
		if err := add(r, "critic", "Read relevant files independently; confirm or dispute each finding with exact file source IDs and preserve disagreement. Reply as a plain step message; do not publish an artifact. No runtime verification is available.", []string{analysis}); err != nil {
			return nil, err
		}
		review := last()
		if err := add(a, "analyst", "Read the critic's exact step reply in the handoff and answer it point by point with file evidence. Concede unsupported findings and keep unresolved disagreement visible. Stay read-only: do not modify files, execute code, or claim runtime verification. Reply as a plain step message; do not publish an artifact.", []string{analysis, review}); err != nil {
			return nil, err
		}
		response := last()
		if err := add(r, "final_reviewer", "Independently read the relevant files plus the initial analysis, the committed review and the analyst response in the handoff. Check every original finding against the review and the response, then publish exactly one code_analysis with severity, impact, file source IDs and limitations; no runtime verification is available. Keep disputes and unresolved findings visible.", []string{analysis, review, response}); err != nil {
			return nil, err
		}
	case "engineering":
		engineer, reviewer := in.Assignments["engineer"], in.Assignments["reviewer"]
		if engineer == "" || reviewer == "" || engineer == reviewer {
			return nil, errors.New("engineering requires different engineer and reviewer members")
		}
		execute := "Read the relevant files and perform the user's requested engineering task using only your granted tools. If write and test tools are granted and the task requires changes, implement them and cite captured diff and passing test evidence. If the task is inspection only, stay read-only and say so. Never claim a test passed unless the host reports exit code 0, and never write outside the bound project."
		if w.EngineeringIsReadOnly(in) {
			execute = "Read the relevant files and perform the user's requested engineering analysis. This method selection is read-only: do not modify files, run project tests, or claim runtime verification. Report concrete findings with exact file evidence."
		}
		if err := add(engineer, "engineer", execute, nil); err != nil {
			return nil, err
		}
		initial := out[len(out)-1].ID
		if err := add(reviewer, "reviewer", "Independently read the relevant files plus the engineer's committed reply and any captured diff or test evidence. Confirm or dispute each finding with exact source IDs and preserve disagreement. Run the relevant test yourself when run_test is granted; otherwise keep the review read-only and say runtime verification was unavailable. Reply as a plain step message and do not publish an artifact.", []string{initial}); err != nil {
			return nil, err
		}
		review := out[len(out)-1].ID
		respond := "Read the reviewer's exact step reply in the handoff and answer it point by point. If write or test tools are granted and defects remain, fix them and rerun the relevant test; otherwise concede unsupported claims and keep unresolved disagreement visible. Stay within the bound project and do not publish an artifact."
		if w.EngineeringIsReadOnly(in) {
			respond = "Read the reviewer's exact step reply in the handoff and answer it point by point with file evidence. This method selection is read-only: do not modify files, run tests, or claim runtime verification. Concede unsupported claims and keep unresolved disagreement visible. Reply as a plain step message and do not publish an artifact."
		}
		if err := add(engineer, "engineer", respond, []string{initial, review}); err != nil {
			return nil, err
		}
		response := out[len(out)-1].ID
		if err := add(reviewer, "final_reviewer", "Independently inspect the relevant files, the initial reply, the review and the engineer response in the handoff. Publish exactly one engineering_report with confirmed, disputed or insufficient_evidence findings, severities, exact file/diff/test source IDs and limitations. Never modify files. If this run captured a write diff, cite the latest diff per changed path and a passing test captured after the last write; if it captured no write diff, report the result as read-only and do not claim runtime verification. Keep disputes and unresolved findings visible.", []string{initial, review, response}); err != nil {
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

// finalSteps selects the steps whose replies form the run delivery. A built-in
// has one fixed final role. A free run keeps every root responder and follows
// its continuation chain to the last return, because a delegated task's reply
// belongs to the delegating root and is never itself the final answer.
func finalSteps(in w.RunInput, steps []w.Step) []w.Step {
	out := []w.Step{}
	if in.Workflow == "" || in.Workflow == "free" {
		for _, root := range steps {
			if root.Kind != "" && root.Kind != "retry_result" {
				continue
			}
			final := root
			for range len(steps) {
				next := slices.IndexFunc(steps, func(s w.Step) bool {
					return s.Kind == "continuation" && s.FromStepID == final.ID
				})
				if next < 0 {
					break
				}
				final = steps[next]
			}
			out = append(out, final)
		}
		return out
	}
	for _, step := range steps {
		if RequiredArtifact(step, in) != "" {
			out = append(out, step)
		}
	}
	if len(out) == 0 {
		for _, step := range steps {
			if step.Kind == "retry_result" {
				out = append(out, step)
			}
		}
	}
	return out
}

// RequiredArtifact determines whether success requires a validated deliverable.
func RequiredArtifact(step w.Step, in w.RunInput) string {
	if step.Role == "task_intake" {
		return ""
	}
	if step.Kind != "" && (in.Workflow == "free" || in.Workflow == "") {
		return ""
	}
	if in.Workflow == "engineering" {
		// Only the final reviewer publishes. The intermediate reviewer returns
		// a plain step reply with source references, which the engineer consumes
		// directly from the handoff. Legacy engineering snapshots used analyst
		// or developer roles and published code_analysis.
		if step.Role == "final_reviewer" {
			if in.EngineeringReadOnly {
				return "engineering_report"
			}
			if _, ok := in.Assignments["engineer"]; ok {
				return "engineering_report"
			}
			return "code_analysis"
		}
		return ""
	}
	if step.Role == "coordinator" {
		if in.Workflow == "research" {
			return "research_report"
		}
		return "note"
	}
	// Legacy two-step plans still publish as reviewer. New read-only analysis
	// plans use critic for the intermediate challenge and final_reviewer for
	// the sole publication step, so an intermediate step never inherits this
	// publication obligation.
	if step.Role == "reviewer" || step.Role == "final_reviewer" {
		return "code_analysis"
	}
	return ""
}

// findingStatuses is the publication contract shared by the publish_artifact
// schema and ValidateArtifact so the visible enum cannot contradict validation.
var findingStatuses = map[string][]string{
	"note":               {"supported", "confirmed", "disputed", "insufficient_evidence"},
	"research_report":    {"supported", "disputed", "insufficient_evidence"},
	"code_analysis":      {"confirmed", "disputed", "insufficient_evidence"},
	"engineering_report": {"confirmed", "disputed", "insufficient_evidence"},
}

// ValidateArtifact verifies source custody and typed report findings.
func ValidateArtifact(a w.Artifact, required string, sources []w.Source) error {
	if required == "" && a.Type != "note" {
		return errors.New("intermediate steps may publish notes only; final reports require the designated coordinator or reviewer")
	}
	if !slices.Contains([]string{"note", "research_report", "code_analysis", "engineering_report"}, a.Type) || a.Type != required && required != "" {
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
			switch a.Type {
			case "research_report":
				kind = "research finding"
			case "code_analysis":
				kind = "code finding"
			case "engineering_report":
				kind = "engineering finding"
			}
			return fmt.Errorf("invalid %s status %q; allowed statuses: %s", kind, f.Status, strings.Join(statuses, ", "))
		}
		if (a.Type == "code_analysis" || a.Type == "engineering_report") && !slices.Contains([]string{"high", "medium", "low"}, f.Severity) {
			return errors.New("code or engineering finding requires severity")
		}
		if a.Type == "code_analysis" && len(f.SourceIDs) == 0 {
			return errors.New("every code finding requires file evidence")
		}
		if a.Type == "engineering_report" && len(f.SourceIDs) == 0 && f.Status != "insufficient_evidence" {
			return errors.New("confirmed or disputed engineering findings require evidence")
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
			if a.Type == "engineering_report" && !slices.Contains([]string{"file", "native_file", "diff", "test"}, s.Kind) {
				return fmt.Errorf("engineering finding requires file, diff or test evidence: %s", id)
			}
		}
		if (f.Status == "supported" || f.Status == "confirmed") && !hasOriginal {
			return errors.New("supported or confirmed finding requires a read original source; search snippets and search result pages are leads, not evidence. Fetch the linked original or mark the finding insufficient_evidence")
		}
	}
	return nil
}

// ValidateEngineeringEvidence requires a report that claims changed files to
// cite an actual captured diff per final changed path and a passing test
// captured after the last write. A run with no write diff is a valid read-only
// result and is checked by ValidateArtifact's file evidence instead.
func ValidateEngineeringEvidence(a w.Artifact, run w.Run, sources []w.Source) error {
	if a.Type != "engineering_report" {
		return nil
	}
	in := effectiveInput(run)
	if in.Workflow != "engineering" {
		return errors.New("engineering_report is only valid for the engineering workflow")
	}
	latestDiff := map[string]w.Source{}
	lastWrite := time.Time{}
	successfulTests := []w.Source{}
	for _, source := range sources {
		if source.RunID != run.ID {
			continue
		}
		switch source.Kind {
		case "diff":
			if source.Path == "" || source.SHA256 == "" {
				continue
			}
			moment := parseSourceTime(source.CapturedAt)
			if moment.After(lastWrite) {
				lastWrite = moment
			}
			if current, ok := latestDiff[source.Path]; !ok || !parseSourceTime(current.CapturedAt).After(moment) {
				latestDiff[source.Path] = source
			}
		case "test":
			if source.ExitCode == 0 && !source.TimedOut {
				successfulTests = append(successfulTests, source)
			}
		}
	}
	if len(latestDiff) == 0 {
		for _, id := range a.SourceIDs {
			if slices.ContainsFunc(sources, func(s w.Source) bool { return s.ID == id && s.Kind == "diff" }) {
				return errors.New("engineering_report cites a diff that does not belong to this run")
			}
		}
		if len(a.Findings) == 0 {
			return errors.New("engineering_report without write evidence must record read-only findings")
		}
		return nil
	}
	evidenceErr := evidenceGap(latestDiff, successfulTests, a, run, lastWrite)
	if evidenceErr == nil {
		return nil
	}
	// A report that explicitly records missing/failed evidence may be
	// delivered, but it cannot claim any confirmed or disputed finding.
	if len(a.Findings) > 0 {
		allInsufficient := true
		for _, finding := range a.Findings {
			if finding.Status != "insufficient_evidence" {
				allInsufficient = false
				break
			}
		}
		if allInsufficient {
			return nil
		}
	}
	return evidenceErr
}

// evidenceGap returns nil only when the report cites a current diff and a
// passing test captured after the last write.
func evidenceGap(latestDiff map[string]w.Source, successfulTests []w.Source, a w.Artifact, run w.Run, lastWrite time.Time) error {
	if len(latestDiff) == 0 {
		return errors.New("engineering_report requires actual captured write diff evidence from this run")
	}
	for path, source := range latestDiff {
		if !slices.Contains(a.SourceIDs, source.ID) {
			return fmt.Errorf("engineering_report must cite the latest diff for %s (source %s)", path, source.ID)
		}
		current, err := tools.CurrentSHA256(run.ProjectRoot(), path)
		if err != nil || current != source.SHA256 {
			return fmt.Errorf("diff evidence for %s no longer matches the current file; re-read and revalidate before delivery", path)
		}
	}
	// A passing test only verifies the tree it actually ran against. Its digest
	// exists only when the bounded digest taken immediately before and after the
	// test both completed and matched; compare that digest with the current tree
	// so implicit test writes or later edits cannot be cited as delivered state.
	currentDigest := ""
	digestChecked := false
	for _, source := range successfulTests {
		if !slices.Contains(a.SourceIDs, source.ID) || parseSourceTime(source.CapturedAt).Before(lastWrite) || source.SHA256 == "" {
			continue
		}
		if !digestChecked {
			currentDigest, _ = tools.WorkspaceDigest(run.ProjectRoot())
			digestChecked = true
		}
		if currentDigest != "" && currentDigest == source.SHA256 {
			return nil
		}
	}
	return errors.New("engineering_report requires a passing run_test captured after the last write whose tested workspace still matches the current project files")
}

func parseSourceTime(value string) time.Time {
	moment, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return moment
}
