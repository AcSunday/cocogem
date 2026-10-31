package model

import "testing"

func TestInputBudgetOnlyAppliesExplicitMemberLimits(t *testing.T) {
	for _, size := range []int{0, -1} {
		if got := InputBudget(size); got != 0 {
			t.Fatalf("context_size=%d produced budget %d, want CLI default (no host budget)", size, got)
		}
	}
	for _, size := range []int{1024, 64000, 1 << 30} {
		if got := InputBudget(size); got != size {
			t.Fatalf("explicit budget=%d, want %d", got, size)
		}
	}
}
