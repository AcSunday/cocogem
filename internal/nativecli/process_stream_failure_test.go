package nativecli

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestStreamFailureFiltersCancellationNoise(t *testing.T) {
	process := newFailure(FailureProcessUncertain, "owned process group cleanup could not be verified")
	stdioNoise := errors.New("read |0: file already closed")
	waitNoise := errors.New("wait: signal: terminated")

	got := streamFailure(context.Canceled, process, stdioNoise, waitNoise)
	if !errors.Is(got, context.Canceled) || !errors.Is(got, process) {
		t.Fatalf("cancellation or ownership evidence lost: %v", got)
	}
	if errors.Is(got, stdioNoise) || errors.Is(got, waitNoise) {
		t.Fatalf("cancellation-derived stdio/wait noise reached the workflow: %v", got)
	}

	// A failure read from explicit protocol fields is not cancellation noise.
	protocol := confirmedFailure(FailureProtocol, "native CLI emitted a malformed event")
	got = streamFailure(context.Canceled, process, protocol, waitNoise)
	if !errors.Is(got, protocol) || !errors.Is(got, process) || errors.Is(got, waitNoise) {
		t.Fatalf("confirmed protocol failure was lost beside cancellation: %v", got)
	}
}

func TestStreamFailureTypesRealWaitWithoutLeakingText(t *testing.T) {
	process := newFailure(FailureProcessUncertain, "owned process group cleanup could not be verified")
	rawWait := errors.New("raw wait stderr must not leak")
	got := streamFailure(nil, process, nil, rawWait)
	if errors.Is(got, rawWait) || strings.Contains(got.Error(), "raw wait stderr") {
		t.Fatalf("raw wait error leaked into the workflow: %v", got)
	}
	kinds := failureKindsInTest(got)
	if !slices.Contains(kinds, FailureProcessUncertain) || !slices.Contains(kinds, FailureProcess) {
		t.Fatalf("real wait was not a typed process failure: %v kinds=%v", got, kinds)
	}
	if err := streamFailure(nil, nil, nil, nil); err != nil {
		t.Fatalf("empty stream failure: %v", err)
	}
}

func TestGeminiStartupAuthFailureIsClassifiedWithoutLeakingStderr(t *testing.T) {
	missing := newFailure(FailureProtocol, "native CLI ended without a confirmed terminal result")
	for _, stderr := range []string{
		"Please set an Auth method in your /private/secret/settings.json or specify GEMINI_API_KEY",
		"Invalid auth method selected. secret-value",
	} {
		got := geminiStartupFailure(missing, stderr, 0)
		if FailureKindOf(got) != FailureAuthentication || strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "/private/") {
			t.Fatalf("authentication failure was not safely classified: %v", got)
		}
		if confirmedStreamFailure(got) {
			t.Fatal("stderr was treated as a confirmed terminal result")
		}
	}
	other := newFailure(FailureProtocol, "native CLI emitted a malformed event")
	if got := geminiStartupFailure(other, "Invalid auth method selected.", 0); got != other {
		t.Fatal("a confirmed protocol failure was replaced by stderr")
	}
	if got := geminiStartupFailure(missing, "unrelated stderr", 0); got != missing {
		t.Fatal("unrecognized stderr changed the failure")
	}
	if got := geminiStartupFailure(missing, "Invalid auth method selected.", 1); got != missing {
		t.Fatal("stderr replaced the result of a process that emitted protocol output")
	}
}

func TestGeminiProcessReportsMissingScopedAuth(t *testing.T) {
	gate := newTestGate(t, nil, discardObserve)
	cmd := exec.Command("/bin/sh", "-c", `printf '%s\n' 'Invalid auth method selected. synthetic-private' >&2; sleep 0.1; exit 1`)
	err := streamProcess(t.Context(), "gemini", cmd, gate, nil)
	if FailureKindOf(err) != FailureAuthentication || strings.Contains(err.Error(), "synthetic-private") || confirmedStreamFailure(err) {
		t.Fatalf("startup failure was not safely reported: %v", err)
	}
}

func failureKindsInTest(err error) []FailureKind {
	var kinds []FailureKind
	var walk func(error)
	walk = func(current error) {
		if current == nil {
			return
		}
		if failure, ok := current.(*Failure); ok {
			kinds = append(kinds, failure.Kind)
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
	return kinds
}
