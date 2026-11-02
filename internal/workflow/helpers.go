package workflow

import (
	"strings"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

func shortTaskResult(value string) string {
	if len(value) > 6000 {
		end := 6000
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		return value[:end] + "\n[truncated; reference IDs and exact sources remain available]"
	}
	return value
}

func shortPointer(value string) string {
	r := []rune(value)
	if len(r) > 180 {
		return string(r[:180]) + "…"
	}
	return value
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// sourceIDs snapshots the exact source authority an attempt was dispatched
// with; later sibling captures cannot enter its plan or checkpoint.
func sourceIDs(sources []w.Source) []string {
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	return ids
}

const (
	capabilityBlockedPrefix = "capability_blocked:"
	connectionBlockedPrefix = "connection_blocked:"
	connectionChangedPrefix = "connection_changed:"
)

func isBlockedError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), capabilityBlockedPrefix) || strings.Contains(err.Error(), connectionBlockedPrefix) || strings.Contains(err.Error(), connectionChangedPrefix) || strings.Contains(err.Error(), "required_tools_unavailable:")
}
