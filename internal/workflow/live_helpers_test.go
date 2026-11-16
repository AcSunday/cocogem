package workflow

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

var (
	liveBearerPattern = regexp.MustCompile(`(?i)bearer\s+[^\s"']+`)
	liveSecretPattern = regexp.MustCompile(`(?i)(?:sk-[a-z0-9_-]{8,}|(?:api[_-]?key|auth[_-]?token|token|secret)\s*[=:]\s*[^\s"']*)`)
)

// liveSafeError keeps concrete step reasons while redacting credential-shaped
// values and bounded output. It never writes prompts, tool input or tool output.
func liveSafeError(value string) string {
	value = liveBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = liveSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, "=:"); index >= 0 {
			return match[:index+1] + "[redacted]"
		}
		return "[redacted]"
	})
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= 600 {
		return value
	}
	end := 600
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…"
}

// liveReviewedText applies the same credential redaction to synthetic step and
// report bodies that a live probe may log.
func liveReviewedText(value string) string {
	value = liveBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = liveSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, "=:"); index >= 0 {
			return match[:index+1] + "[redacted]"
		}
		return "[redacted]"
	})
	if len(value) > 6000 {
		end := 6000
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		return value[:end] + "\n…[logged body truncated]"
	}
	return value
}

// logVisibleSteps prints the synthetic step replies a live probe observed.
func logVisibleSteps(t *testing.T, steps []w.Step, messages []w.Message) {
	t.Helper()
	for _, step := range steps {
		for _, message := range messages {
			if message.RunID != step.RunID || message.StepID != step.ID || message.Role != "assistant" || message.Status != "completed" {
				continue
			}
			t.Logf("visible step role=%s status=%s message_id=%s:\n%s", step.Role, step.Status, liveSafeError(message.ID), liveReviewedText(message.Content))
		}
	}
}

// logFinalArtifact prints the exact synthetic deliverable that was published.
func logFinalArtifact(t *testing.T, artifact w.Artifact) {
	t.Helper()
	t.Logf("final artifact type=%s title=%q id=%s version=%d source_ids=%d findings=%d limitations=%d body:\n%s", artifact.Type, liveSafeError(artifact.Title), liveSafeError(artifact.ID), artifact.Version, len(artifact.SourceIDs), len(artifact.Findings), len(artifact.Limitations), liveReviewedText(artifact.Body))
	for _, finding := range artifact.Findings {
		t.Logf("artifact finding status=%s severity=%s title=%q detail=%s sources=%v", finding.Status, finding.Severity, liveSafeError(finding.Title), liveReviewedText(finding.Detail), finding.SourceIDs)
	}
	for _, limitation := range artifact.Limitations {
		t.Logf("artifact limitation: %s", liveReviewedText(limitation))
	}
}
