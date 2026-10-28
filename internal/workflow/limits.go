package workflow

import "time"

// Limits separates wall-clock limits from liveness and dependency waiting.
// After parsing, a zero duration disables that individual check; negative
// values are rejected at the configuration boundary.
type Limits struct {
	RunTimeout    time.Duration
	StepTimeout   time.Duration
	FirstResponse time.Duration
	Stall         time.Duration
	StallAction   string
}

// DefaultLimits is applied once when the server parses configuration. It must
// not be re-applied during execution, where zero means "disabled".
func DefaultLimits() Limits {
	return Limits{FirstResponse: 10 * time.Minute, Stall: 15 * time.Minute, StallAction: "warn"}
}

// WithDefaults is retained for older callers. Defaults belong to the
// configuration entry; a parsed zero already means disabled and is preserved.
func (l Limits) WithDefaults() Limits { return l }
