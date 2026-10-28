package nativecli

import (
	"context"
	"errors"
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
