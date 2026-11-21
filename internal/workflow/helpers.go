package workflow

import (
	"fmt"
	"strings"
	"unicode/utf8"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// shortTaskResult bounds a committed body for an inline preview. It keeps both
// the head and the tail: a conclusion, acceptance condition or counterexample
// usually sits at the end, so a head-only cut would silently lose it. The
// omitted middle names the exact tool that pages the full bytes.
func shortTaskResult(value string) string {
	const limit = 6000
	if len(value) <= limit {
		return value
	}
	head := limit * 2 / 5
	tail := limit - head
	for head > 0 && !utf8.RuneStart(value[head]) {
		head--
	}
	start := len(value) - tail
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	omitted := len(value) - head - (len(value) - start)
	return value[:head] +
		fmt.Sprintf("\n[...省略 %d 字节，中间部分不是已读证据；请用 read_context（消息/产物版本）或 read_source（捕获来源）翻阅原件...]\n", omitted) +
		value[start:]
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

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
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
