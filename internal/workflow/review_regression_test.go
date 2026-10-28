package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AcSunday/cocogem/internal/nativecli"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestReviewIndependentFailureAfterSiblingCheckpoint(t *testing.T) {
	for _, err := range []error{
		&nativecli.Failure{Kind: nativecli.FailureAuthentication, Reason: "provider rejected the request"},
		&nativecli.Failure{Kind: nativecli.FailureNetwork, Reason: "connection failed independently"},
		errors.New("independent execution failure"),
	} {
		t.Run(err.Error(), func(t *testing.T) {
			got := classifyStepError(err, context.Canceled, true, false, false, false)
			if got.step != "failed" || got.attempt != "failed" || got.reason != err.Error() {
				t.Fatalf("sibling checkpoint hid a real failure: %+v", got)
			}
		})
	}
}

func TestReviewMultipleSameKindFailuresRetainAllDetails(t *testing.T) {
	steps := []w.Step{
		{ID: "first-step", Status: "failed", FailureKind: failureStepTimeout, Error: "first timeout"},
		{ID: "second-step", Status: "failed", FailureKind: failureStepTimeout, Error: "second timeout"},
	}
	status, reason, kind := aggregateSteps(steps, false)
	if status != "failed" || kind != failureMultipleSteps || !strings.Contains(reason, "first-step") || !strings.Contains(reason, "second-step") || !strings.Contains(reason, "first timeout") || !strings.Contains(reason, "second timeout") {
		t.Fatalf("multiple failures lost their individual evidence: status=%s kind=%s reason=%s", status, kind, reason)
	}
}
