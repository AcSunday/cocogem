package model

import "testing"

func TestInputTokenBudgetOnlyAppliesExplicitMemberLimits(t *testing.T) {
	for _, size := range []int{0, -1} {
		if got := InputTokenBudget(size); got != 0 {
			t.Fatalf("context_size=%d produced budget %d, want CLI default (no host budget)", size, got)
		}
	}
	for _, size := range []int{1024, 64000, 1 << 30} {
		if got := InputTokenBudget(size); got != size {
			t.Fatalf("explicit budget=%d, want %d", size, got)
		}
	}
}

func TestEstimateTokensIsConservativeAcrossScripts(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcde", 2},
		{"hello world", 3},
		{"中文", 2},
		{"a中b文", 3},
	} {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Fatalf("EstimateTokens(%q)=%d, want %d", tc.text, got, tc.want)
		}
	}
}
