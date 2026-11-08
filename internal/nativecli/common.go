package nativecli

import "strings"

// shellQuote is shared by every adapter that builds a private shell command.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// appendUnique keeps ordered scope lists deterministic.
func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}
