package tools

import (
	"strings"
	"testing"
)

// TestAnalyzeDiffHygieneFlagsStackedFallbacks is the A17 regression: three net
// added fallback branches produce a non-blocking hint, while a fix that removes
// branches never does.
func TestAnalyzeDiffHygieneFlagsStackedFallbacks(t *testing.T) {
	patch := `diff --git a/internal/state.go b/internal/state.go
--- a/internal/state.go
+++ b/internal/state.go
@@ -1,4 +1,10 @@
 func load(id string) (*Item, error) {
+	if id == "" {
+		return nil, nil
+	}
+	if item, ok := cache[id]; ok {
+		return item, nil
+	} else if fallback {
+		return nil, nil
+	} else if legacy {
+		return nil, nil
+	}
+	value := mapValue ?? ""
+	return nil, nil
 }
`
	hints := AnalyzeDiffHygiene(patch)
	if len(hints) != 1 {
		t.Fatalf("hints = %+v", hints)
	}
	if hints[0].Path != "internal/state.go" || hints[0].FallbackBranches < 3 {
		t.Fatalf("hint = %+v", hints[0])
	}
	if !strings.Contains(DiffHygieneSummary(hints), "不阻塞交付") {
		t.Fatalf("summary is not non-blocking: %q", DiffHygieneSummary(hints))
	}

	// A cleanup that removes more fallbacks than it adds never triggers the hint.
	cleanup := `diff --git a/internal/state.go b/internal/state.go
--- a/internal/state.go
+++ b/internal/state.go
@@ -1,8 +1,2 @@
-	if err != nil { return nil }
-	} else if fallback {
-		return nil, nil
-	}
+	return loadFromStore(id, nil)
`
	if hints := AnalyzeDiffHygiene(cleanup); len(hints) != 0 {
		t.Fatalf("a branch-removing fix was flagged: %+v", hints)
	}
}

// TestAnalyzeDiffHygieneIgnoresCommentsAndTwoBranches keeps the hint quiet for a
// normal two-branch guard and for comment text that merely mentions a marker.
func TestAnalyzeDiffHygieneIgnoresCommentsAndTwoBranches(t *testing.T) {
	patch := `diff --git a/internal/a.go b/internal/a.go
--- a/internal/a.go
+++ b/internal/a.go
@@ -1,3 +1,8 @@
+	// if err != nil { is only documentation
+	// else if fallback is not code
+	if value != nil {
+		return value, nil
+	}
+	return nil, nil
`
	if hints := AnalyzeDiffHygiene(patch); len(hints) != 0 {
		t.Fatalf("comments or a two-branch guard were flagged: %+v", hints)
	}
}
