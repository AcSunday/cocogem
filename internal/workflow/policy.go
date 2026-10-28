package workflow

import (
	"time"

	w "github.com/AcSunday/cocogem/internal/workspace"
)

// policyFromLimits freezes the parsed execution policy at the queue/dispatch
// boundary. Zero keeps its explicit "disabled" meaning; defaults were already
// applied when configuration was parsed.
func policyFromLimits(limits Limits) w.ExecutionPolicy {
	return w.ExecutionPolicy{
		RunTimeout:    limits.RunTimeout,
		StepTimeout:   limits.StepTimeout,
		FirstResponse: limits.FirstResponse,
		Stall:         limits.Stall,
		StallAction:   limits.StallAction,
	}
}

// adoptPolicy persists one policy for a run that predates policy snapshots.
// The absolute run deadline is measured from the adoption, never from a
// historical attempt: the old policy was unknown, so the current configuration
// applies from the moment it is first adopted. StartedAt remains a historical
// fact (earliest reliable dispatch, blocked preflight excluded). It returns the
// persisted adoption time and whether adoption happened in this call.
func adoptPolicy(run *w.Run, fallback Limits, attempts []w.Attempt, now time.Time) (time.Time, bool) {
	adopted := false
	if run.ExecutionPolicy == nil {
		policy := policyFromLimits(fallback)
		run.ExecutionPolicy = &policy
		run.PolicyAdoptedAt = now.UTC().Format(time.RFC3339Nano)
		adopted = true
	}
	started := parseMoment(run.StartedAt)
	if started.IsZero() {
		started = earliestAttemptStart(attempts)
	}
	if started.IsZero() {
		started = now
	}
	if run.StartedAt == "" {
		run.StartedAt = started.UTC().Format(time.RFC3339Nano)
	}
	// A persisted deadline is never extended, even when the policy was missing.
	if run.DeadlineAt == "" && run.ExecutionPolicy.RunTimeout > 0 {
		origin := parseMoment(run.PolicyAdoptedAt)
		if origin.IsZero() {
			origin = started
		}
		run.DeadlineAt = origin.Add(run.ExecutionPolicy.RunTimeout).UTC().Format(time.RFC3339Nano)
	}
	return parseMoment(run.PolicyAdoptedAt), adopted
}

// adoptStepClocks gives already-dispatched legacy steps one absolute deadline
// measured from the policy adoption. A step that never dispatched keeps its
// clock unset until its first actual dispatch. It reports whether any step
// changed.
func adoptStepClocks(steps []w.Step, attempts []w.Attempt, policy w.ExecutionPolicy, adoptedAt time.Time) bool {
	if adoptedAt.IsZero() {
		return false
	}
	changed := false
	for i := range steps {
		step := &steps[i]
		if step.Status == "succeeded" || step.Status == "failed" || step.Status == "skipped" {
			continue
		}
		if step.StartedAt == "" {
			if earliest := earliestAttemptStart(attemptsOf(attempts, step.ID)); !earliest.IsZero() {
				step.StartedAt = earliest.UTC().Format(time.RFC3339Nano)
				changed = true
			}
		}
		if step.StartedAt == "" {
			continue
		}
		if step.DeadlineAt == "" && policy.StepTimeout > 0 {
			step.DeadlineAt = adoptedAt.Add(policy.StepTimeout).UTC().Format(time.RFC3339Nano)
			changed = true
		}
	}
	return changed
}

func attemptsOf(attempts []w.Attempt, stepID string) []w.Attempt {
	var result []w.Attempt
	for _, attempt := range attempts {
		if attempt.StepID == stepID {
			result = append(result, attempt)
		}
	}
	return result
}

// earliestAttemptStart finds the earliest reliable dispatch time recorded for
// a logical step. Blocked preflight attempts never launched a runner, so they
// must not shorten or extend the step's clock.
func earliestAttemptStart(attempts []w.Attempt) time.Time {
	var earliest time.Time
	for _, attempt := range attempts {
		if attempt.Status == "blocked" {
			continue
		}
		moment := parseMoment(attempt.StartedAt)
		if moment.IsZero() {
			continue
		}
		if earliest.IsZero() || moment.Before(earliest) {
			earliest = moment
		}
	}
	return earliest
}

// applyStepDeadline adopts a legacy step's clock from its earliest existing
// attempt, then evaluates the frozen absolute deadline. A step that has never
// dispatched keeps an empty start; the caller starts the clock only when it is
// about to hand work to a runner.
func applyStepDeadline(step *w.Step, attempts []w.Attempt, policy w.ExecutionPolicy, now time.Time) (time.Time, bool) {
	if step.StartedAt == "" {
		if earliest := earliestAttemptStart(attempts); !earliest.IsZero() {
			step.StartedAt = earliest.UTC().Format(time.RFC3339Nano)
		}
	}
	if step.StartedAt == "" {
		return time.Time{}, false
	}
	if step.DeadlineAt == "" && policy.StepTimeout > 0 {
		if started := parseMoment(step.StartedAt); !started.IsZero() {
			step.DeadlineAt = started.Add(policy.StepTimeout).UTC().Format(time.RFC3339Nano)
		}
	}
	deadline := parseMoment(step.DeadlineAt)
	if deadline.IsZero() {
		return time.Time{}, false
	}
	return deadline, !now.Before(deadline)
}

// startStepClock starts a logical step exactly once, immediately before its
// first actual dispatch.
func startStepClock(step *w.Step, policy w.ExecutionPolicy, now time.Time) {
	if step.StartedAt == "" {
		step.StartedAt = now.UTC().Format(time.RFC3339Nano)
	}
	if step.DeadlineAt == "" && policy.StepTimeout > 0 {
		if started := parseMoment(step.StartedAt); !started.IsZero() {
			step.DeadlineAt = started.Add(policy.StepTimeout).UTC().Format(time.RFC3339Nano)
		}
	}
}

// runDeadline is the frozen absolute end of the run's dispatch window.
func runDeadline(run w.Run) time.Time { return parseMoment(run.DeadlineAt) }

func parseMoment(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	moment, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return moment
}

// expired reports whether a persisted absolute deadline has already passed.
func expired(deadline time.Time, now time.Time) bool {
	return !deadline.IsZero() && !now.Before(deadline)
}
