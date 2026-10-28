package nativecli

import (
	"context"
	"errors"
	"testing"
)

func TestReviewJoinedFailureIsNotDeduplicatedByCancellation(t *testing.T) {
	provider := &Failure{Kind: FailureAuthentication, Reason: "provider rejected request"}
	failures := appendFailure(nil, context.Canceled)
	failures = appendFailure(failures, errors.Join(context.Canceled, provider))
	result := errors.Join(failures...)
	var actual *Failure
	if !errors.Is(result, context.Canceled) || !errors.As(result, &actual) || actual != provider {
		t.Fatalf("failure aggregation discarded independent provider evidence: %v", result)
	}
}
