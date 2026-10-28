package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AcSunday/cocogem/internal/nativecli"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

// Machine-readable run/step failure kinds. Human text stays in Error.
const (
	failureRunTimeout          = "run_timeout"
	failureStepTimeout         = "step_timeout"
	failureMultipleSteps       = "multiple_step_failures"
	failureCheckpointInterrupt = "checkpoint_interrupted"
	failureArtifactContract    = "artifact_contract"
	failureUnknown             = "unknown"
)

// failureKind maps a native failure wrapper to its machine kind; generic
// errors remain explicitly unknown rather than guessed from their text.
func failureKind(err error) string {
	if err == nil {
		return ""
	}
	if kind := nativecli.FailureKindOf(err); kind != "" {
		return string(kind)
	}
	return failureUnknown
}

// independentFailure returns the first wrapped non-context error, or nil when
// the whole chain is only context cancellation/deadline. errors.Join keeps a
// real provider error next to the cancellation; that real error must win.
func independentFailure(err error) error {
	if err == nil || err == context.Canceled || err == context.DeadlineExceeded {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range joined.Unwrap() {
			if found := independentFailure(inner); found != nil {
				return found
			}
		}
		return nil
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return independentFailure(wrapped)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}

// recoveryProcessUncertain marks an attempt whose owned process group could
// not be proven stopped. Resume must verify a trusted ProcessRef before any
// new dispatch; a true sibling failure still fails the run and keeps the
// uncertainty as recovery evidence.
const recoveryProcessUncertain = "process_uncertain"

// nativeFailureEvidence walks the whole tree. errors.As stops at the first
// typed failure, which is often the process-convergence wrapper even when a
// confirmed authentication or protocol failure is joined next to it.
func nativeFailureEvidence(err error) []*nativecli.Failure {
	var failures []*nativecli.Failure
	var walk func(error)
	walk = func(current error) {
		if current == nil {
			return
		}
		if failure, ok := current.(*nativecli.Failure); ok {
			failures = append(failures, failure)
			return
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				walk(inner)
			}
			return
		}
		walk(errors.Unwrap(current))
	}
	walk(err)
	return failures
}

// processUncertainEvidence separates the process ownership verdict from real
// terminal failures and from recoverable interruptions. Only typed native
// failures count: a cancellation-truncated EOF or wait error must not turn
// into independent evidence.
func processUncertainEvidence(err error) (uncertain bool, finalKind, recoverableKind nativecli.FailureKind) {
	for _, failure := range nativeFailureEvidence(err) {
		switch failure.Kind {
		case nativecli.FailureProcessUncertain:
			uncertain = true
		case nativecli.FailureContextOverflow, nativecli.FailureFirstResponseTimeout, nativecli.FailureStallTimeout:
			if recoverableKind == "" {
				recoverableKind = failure.Kind
			}
		default:
			if finalKind == "" {
				finalKind = failure.Kind
			}
		}
	}
	return uncertain, finalKind, recoverableKind
}

// independentRealFailure finds a real failure next to a recoverable signal.
// Typed failures are always trusted. A plain error is trusted too: native
// cancellation-derived stdio/wait noise is filtered at the native boundary, so
// an unknown error reaching the workflow is independent evidence, not a
// truncation artifact inferred from the presence of a cancellation.
func independentRealFailure(err error) error {
	if err == nil {
		return nil
	}
	var walk func(error) error
	walk = func(current error) error {
		if current == nil {
			return nil
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				if found := walk(inner); found != nil {
					return found
				}
			}
			return nil
		}
		if wrapped := errors.Unwrap(current); wrapped != nil {
			return walk(wrapped)
		}
		if errors.Is(current, context.Canceled) || errors.Is(current, context.DeadlineExceeded) {
			return nil
		}
		if failure, ok := current.(*nativecli.Failure); ok {
			switch failure.Kind {
			case nativecli.FailureContextOverflow, nativecli.FailureFirstResponseTimeout, nativecli.FailureStallTimeout, nativecli.FailureProcessUncertain:
				return nil
			}
			return current
		}
		return current
	}
	return walk(err)
}

// stepOutcome decides the logical Step and Attempt statuses from the final
// failure kind. A failed Attempt is not automatically a failed Step.
type stepOutcome struct {
	step     string
	attempt  string
	kind     string
	reason   string
	recovery string
}

func classifyStepError(err error, ctxErr error, checkpointed, internal bool, runExpired, stepExpired bool) stepOutcome {
	if runExpired {
		return stepOutcome{step: "failed", attempt: "failed", kind: failureRunTimeout, reason: "run_timeout: the frozen run deadline elapsed before this step committed a result"}
	}
	if stepExpired {
		return stepOutcome{step: "failed", attempt: "failed", kind: failureStepTimeout, reason: "step_timeout: the frozen step deadline elapsed without a committed result"}
	}
	if err == nil {
		return stepOutcome{step: "succeeded", attempt: "succeeded"}
	}
	if internal {
		return stepOutcome{step: "failed", attempt: "failed", kind: failureKind(err), reason: err.Error()}
	}
	uncertain, finalKind, recoverableKind := processUncertainEvidence(err)
	if recoverableKind != "" {
		// A recoverable interrupt signal (stall, first response, overflow)
		// joined with an independent real failure must not hide that failure.
		if finalKind != "" {
			outcome := stepOutcome{step: "failed", attempt: "failed", kind: string(finalKind), reason: err.Error()}
			if uncertain {
				outcome.recovery = recoveryProcessUncertain
			}
			return outcome
		}
		if real := independentRealFailure(err); real != nil {
			outcome := stepOutcome{step: "failed", attempt: "failed", kind: failureKind(real), reason: err.Error()}
			if uncertain {
				outcome.recovery = recoveryProcessUncertain
			}
			return outcome
		}
	}
	if uncertain {
		reason := err.Error()
		if real := independentRealFailure(err); real != nil {
			return stepOutcome{step: "failed", attempt: "failed", kind: failureKind(real), reason: reason, recovery: recoveryProcessUncertain}
		}
		switch {
		case recoverableKind != "":
			// Keep the existing recovery mode (for example context_fresh) so
			// overflow replay detection still applies; attempt=uncertain is
			// itself the durable process-ownership marker.
			return stepOutcome{step: "interrupted", attempt: "uncertain", kind: string(recoverableKind), reason: reason}
		default:
			return stepOutcome{step: "interrupted", attempt: "uncertain", kind: string(nativecli.FailureProcessUncertain), reason: reason, recovery: recoveryProcessUncertain}
		}
	}
	kind := failureKind(err)
	switch kind {
	case string(nativecli.FailureContextOverflow):
		return stepOutcome{step: "interrupted", attempt: "failed", kind: kind, reason: err.Error()}
	case string(nativecli.FailureFirstResponseTimeout), string(nativecli.FailureStallTimeout):
		return stepOutcome{step: "interrupted", attempt: "interrupted", kind: kind, reason: err.Error()}
	}
	if isBlockedError(err) {
		attemptStatus := "blocked"
		kind := "capability_blocked"
		if strings.Contains(err.Error(), connectionBlockedPrefix) || strings.Contains(err.Error(), connectionChangedPrefix) {
			kind = "connection_blocked"
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			attemptStatus = "canceled"
		}
		return stepOutcome{step: "interrupted", attempt: attemptStatus, kind: kind, reason: err.Error()}
	}
	if ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			// A deadline is still a true failure; keep an independent typed
			// cause instead of flattening it to unknown.
			return stepOutcome{step: "failed", attempt: "failed", kind: failureKind(err), reason: err.Error()}
		}
		if checkpointed {
			if independent := independentFailure(err); independent != nil {
				// A real provider failure that arrived after a sibling checkpoint
				// is still a true failure; it must not be hidden as interruption.
				return stepOutcome{step: "failed", attempt: "failed", kind: failureKind(independent), reason: independent.Error()}
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Only the cancellation caused by this checkpoint may become an
				// interrupted checkpoint; a late sibling failure won the branch above.
				return stepOutcome{step: "interrupted", attempt: "interrupted", kind: failureCheckpointInterrupt, reason: "checkpoint_interrupted: " + err.Error()}
			}
			return stepOutcome{step: "failed", attempt: "failed", kind: failureUnknown, reason: err.Error()}
		}
		return stepOutcome{step: "canceled", attempt: "canceled", reason: err.Error()}
	}
	return stepOutcome{step: "failed", attempt: "failed", kind: kind, reason: err.Error()}
}

// aggregateSteps reports the deterministic run outcome. A real failed Step
// outranks recoverable interruptions; a run deadline outranks both.
func aggregateSteps(steps []w.Step, runTimedOut bool) (string, string, string) {
	if runTimedOut {
		return "failed", "run_timeout: the frozen run deadline elapsed before the run completed", failureRunTimeout
	}
	failed := []w.Step{}
	interrupted := []w.Step{}
	for _, step := range steps {
		switch step.Status {
		case "failed":
			failed = append(failed, step)
		case "interrupted":
			interrupted = append(interrupted, step)
		}
	}
	if len(failed) == 1 {
		kind := failed[0].FailureKind
		if kind == "" {
			kind = failureUnknown
		}
		reason := failed[0].Error
		if reason == "" {
			reason = "step failed"
		}
		return "failed", reason, kind
	}
	if len(failed) > 1 {
		parts := make([]string, 0, len(failed))
		for _, step := range failed {
			kind := step.FailureKind
			if kind == "" {
				kind = failureUnknown
			}
			parts = append(parts, fmt.Sprintf("step %s [%s]: %s", step.ID, kind, step.Error))
		}
		return "failed", failureMultipleSteps + ": " + strings.Join(parts, "; "), failureMultipleSteps
	}
	if len(interrupted) > 0 {
		chosen := interrupted[0]
		for _, step := range interrupted {
			if step.FailureKind != failureCheckpointInterrupt {
				chosen = step
				break
			}
		}
		kind := chosen.FailureKind
		reason := chosen.Error
		if reason == "" {
			reason = "checkpoint preserved for explicit resume"
		}
		return "interrupted", reason, kind
	}
	for _, step := range steps {
		if step.Status == "skipped" {
			reason := step.Error
			if reason == "" {
				reason = "a required step was skipped before it could run"
			}
			kind := step.FailureKind
			if kind == "" {
				kind = failureUnknown
			}
			return "failed", reason, kind
		}
	}
	for _, step := range steps {
		if step.Status == "pending" || step.Status == "running" {
			return "interrupted", "checkpoint preserved for explicit resume", ""
		}
	}
	return "succeeded", "", ""
}
