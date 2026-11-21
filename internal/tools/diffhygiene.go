package tools

import (
	"strings"
)

// DiffHygiene is a non-blocking static hint about fallback control-flow growth in
// one candidate diff. It never blocks a delivery: it exists so a reviewer sees
// when a fix stacked fallback branches instead of addressing the root cause.
type DiffHygiene struct {
	// Path is the affected workspace-relative file.
	Path string
	// FallbackBranches is the number of added else-if / null-fallback / swallowed
	// error branches in this file.
	FallbackBranches int
	// Hint is the one-line, non-blocking recommendation.
	Hint string
}

// fallbackBranchMarkers count added control-flow shapes that typically signal
// "stacked fallback" fixes without flagging idiomatic Go error checks
// (`if err != nil {` or `return nil`).
var fallbackBranchMarkers = []string{
	"else if", "elif ", "?? ", "??\"", "??'", "?: ",
	"catch (", "catch(", "catch {", "except ", "except:",
	"|| []", "|| {}", "|| \"\"", "|| ''", "|| 0", "|| null", "|| false",
	"unwrap_or", "if err ==", "recover()",
}

// AnalyzeDiffHygiene inspects added lines per file and returns a hint for any file
// with three or more added fallback branches. A removal or a refactor that
// replaces branches never triggers the hint.
func AnalyzeDiffHygiene(patch string) []DiffHygiene {
	type counter struct{ added, removed int }
	files := map[string]*counter{}
	current := ""
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			current = strings.TrimPrefix(line, "+++ b/")
			if files[current] == nil {
				files[current] = &counter{}
			}
		case strings.HasPrefix(line, "+++ "):
			current = ""
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "diff --git "), strings.HasPrefix(line, "index "):
			continue
		case strings.HasPrefix(line, "+") && current != "":
			body := strings.TrimSpace(strings.TrimPrefix(line, "+"))
			if containsFallbackMarker(body) {
				files[current].added++
			}
		case strings.HasPrefix(line, "-") && current != "":
			body := strings.TrimSpace(strings.TrimPrefix(line, "-"))
			if containsFallbackMarker(body) {
				files[current].removed++
			}
		}
	}
	hints := []DiffHygiene{}
	for path, counts := range files {
		net := counts.added - counts.removed
		if net < 3 {
			continue
		}
		hints = append(hints, DiffHygiene{
			Path:             path,
			FallbackBranches: net,
			Hint:             "this diff adds " + itoa(net) + " fallback control-flow branches; check whether the root cause and state contract are fixed instead of stacking fallbacks",
		})
	}
	// Deterministic order: the hint must not depend on map iteration.
	stableHints := make([]DiffHygiene, 0, len(hints))
	for _, hint := range hints {
		index := len(stableHints)
		for i, existing := range stableHints {
			if existing.Path > hint.Path {
				index = i
				break
			}
		}
		stableHints = append(stableHints, DiffHygiene{})
		copy(stableHints[index+1:], stableHints[index:])
		stableHints[index] = hint
	}
	return stableHints
}

func containsFallbackMarker(line string) bool {
	if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") {
		return false
	}
	for _, marker := range fallbackBranchMarkers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// DiffHygieneSummary renders the hint as one bounded, non-blocking line for the
// observation or review summary. Empty means no hint.
func DiffHygieneSummary(hints []DiffHygiene) string {
	if len(hints) == 0 {
		return ""
	}
	parts := []string{}
	for _, hint := range hints {
		parts = append(parts, hint.Path+" ("+itoa(hint.FallbackBranches)+" 层兜底分支): "+hint.Hint)
	}
	return "Diff 坏味道提示（不阻塞交付）：" + strings.Join(parts, "；")
}
