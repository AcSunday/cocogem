package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/AcSunday/cocogem/internal/nativecli"
)

func TestReviewOnlyRecoverableSignalStaysRecoverable(t *testing.T) {
	for _, kind := range []nativecli.FailureKind{nativecli.FailureStallTimeout, nativecli.FailureFirstResponseTimeout, nativecli.FailureContextOverflow} {
		recoverable := &nativecli.Failure{Kind: kind, Reason: "checkpoint requested"}
		got := classifyStepError(errors.Join(context.Canceled, recoverable), context.Canceled, false, false, false, false)
		if got.step != "interrupted" || got.kind != string(kind) {
			t.Fatalf("recoverable signal changed without independent evidence: %+v", got)
		}
	}
}

func TestReviewRealFailureOutranksRecoverableSignal(t *testing.T) {
	auth := &nativecli.Failure{Kind: nativecli.FailureAuthentication, Reason: "provider independently rejected the request"}
	unknown := errors.New("independent persistence failure")
	for _, kind := range []nativecli.FailureKind{nativecli.FailureStallTimeout, nativecli.FailureFirstResponseTimeout, nativecli.FailureContextOverflow} {
		t.Run(string(kind), func(t *testing.T) {
			for _, real := range []struct {
				err  error
				kind string
			}{{auth, string(nativecli.FailureAuthentication)}, {unknown, failureUnknown}} {
				recoverable := &nativecli.Failure{Kind: kind, Reason: "checkpoint requested"}
				got := classifyStepError(errors.Join(context.Canceled, recoverable, real.err), context.Canceled, false, false, false, false)
				if got.step != "failed" || got.attempt != "failed" || got.kind != real.kind {
					t.Fatalf("recoverable signal masked an independent real failure (%v): %+v", real.err, got)
				}
			}
		})
	}
}

func TestReviewIndependentFailureBesideProcessUncertainty(t *testing.T) {
	uncertain := &nativecli.Failure{Kind: nativecli.FailureProcessUncertain, Reason: "group cleanup unverified"}
	real := errors.New("independent usage persistence failure")
	got := classifyStepError(errors.Join(context.Canceled, uncertain, real), context.Canceled, false, false, false, false)
	if got.step != "failed" || got.attempt != "failed" || got.kind != failureUnknown || got.recovery != recoveryProcessUncertain {
		t.Fatalf("process uncertainty hid a real persistence failure: %+v", got)
	}
}
