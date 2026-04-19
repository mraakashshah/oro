package memoryeval

import (
	"strings"
	"testing"
)

func TestCheckGateZeroBaselineFails(t *testing.T) {
	result := CheckGate(0, 1, 1)
	if result.Pass {
		t.Errorf("CheckGate(0, 1, 1).Pass = true; want false")
	}
	if !strings.Contains(result.Reason, "baseline MRR is 0") {
		t.Errorf("CheckGate(0, 1, 1).Reason = %q; want to contain 'baseline MRR is 0'", result.Reason)
	}
}

func TestCheckGatePassesWhenRatiosMet(t *testing.T) {
	result := CheckGate(0.5, 0.65, 0.60)
	if !result.Pass {
		t.Errorf("CheckGate(0.5, 0.65, 0.60).Pass = false; want true. Reason: %s", result.Reason)
	}
}

func TestCheckGateWarmBelowThreshold(t *testing.T) {
	result := CheckGate(0.5, 0.60, 0.60)
	if result.Pass {
		t.Errorf("CheckGate(0.5, 0.60, 0.60).Pass = true; want false")
	}
	if !strings.Contains(result.Reason, "warm MRR") {
		t.Errorf("CheckGate(0.5, 0.60, 0.60).Reason = %q; want to contain 'warm MRR'", result.Reason)
	}
}

func TestCheckGateColdBelowThreshold(t *testing.T) {
	result := CheckGate(0.5, 0.70, 0.55)
	if result.Pass {
		t.Errorf("CheckGate(0.5, 0.70, 0.55).Pass = true; want false")
	}
	if !strings.Contains(result.Reason, "cold MRR") {
		t.Errorf("CheckGate(0.5, 0.70, 0.55).Reason = %q; want to contain 'cold MRR'", result.Reason)
	}
}

func TestMRRSingleRelevant(t *testing.T) {
	const relevant = int64(42)

	ids := func(ranks ...int64) []int64 { return ranks }

	tests := []struct {
		name       string
		topKIDs    []int64
		relevantID int64
		k          int
		want       float64
	}{
		{
			name:       "rank_1_returns_1.0",
			topKIDs:    ids(42, 1, 2, 3, 4),
			relevantID: relevant,
			k:          10,
			want:       1.0,
		},
		{
			name:       "rank_5_returns_0.2",
			topKIDs:    ids(1, 2, 3, 4, 42, 5, 6, 7, 8, 9),
			relevantID: relevant,
			k:          10,
			want:       0.2,
		},
		{
			name:       "absent_returns_0",
			topKIDs:    ids(1, 2, 3, 4, 5),
			relevantID: relevant,
			k:          10,
			want:       0.0,
		},
		{
			name:       "absent_within_k_contributes_0_to_sum",
			topKIDs:    ids(1, 2, 3),
			relevantID: relevant,
			k:          5,
			want:       0.0,
		},
		{
			name:       "k_le_0_returns_0",
			topKIDs:    ids(42, 1, 2),
			relevantID: relevant,
			k:          0,
			want:       0.0,
		},
		{
			name:       "negative_k_returns_0",
			topKIDs:    ids(42, 1, 2),
			relevantID: relevant,
			k:          -1,
			want:       0.0,
		},
		{
			name:       "empty_topKIDs_returns_0",
			topKIDs:    []int64{},
			relevantID: relevant,
			k:          10,
			want:       0.0,
		},
		{
			name:       "nil_topKIDs_returns_0",
			topKIDs:    nil,
			relevantID: relevant,
			k:          10,
			want:       0.0,
		},
		{
			name:       "relevant_beyond_k_treated_as_absent",
			topKIDs:    ids(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 42),
			relevantID: relevant,
			k:          10,
			want:       0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MRR(tc.topKIDs, tc.relevantID, tc.k)
			if got != tc.want {
				t.Errorf("MRR(%v, %d, %d) = %v; want %v", tc.topKIDs, tc.relevantID, tc.k, got, tc.want)
			}
		})
	}
}

func TestHitAtKSingleRelevant(t *testing.T) {
	const relevant = int64(42)

	ids := func(ranks ...int64) []int64 { return ranks }

	tests := []struct {
		name       string
		topKIDs    []int64
		relevantID int64
		k          int
		want       float64
	}{
		{
			name:       "Hit@10_rank_5_returns_1",
			topKIDs:    ids(1, 2, 3, 4, 42, 5, 6, 7, 8, 9),
			relevantID: relevant,
			k:          10,
			want:       1.0,
		},
		{
			name:       "Hit@10_rank_11_returns_0",
			topKIDs:    ids(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 42),
			relevantID: relevant,
			k:          10,
			want:       0.0,
		},
		{
			name:       "Hit@1_rank_1_returns_1",
			topKIDs:    ids(42, 1, 2, 3),
			relevantID: relevant,
			k:          1,
			want:       1.0,
		},
		{
			name:       "Hit@1_rank_2_returns_0",
			topKIDs:    ids(1, 42, 2, 3),
			relevantID: relevant,
			k:          1,
			want:       0.0,
		},
		{
			name:       "k_le_0_returns_0",
			topKIDs:    ids(42, 1, 2),
			relevantID: relevant,
			k:          0,
			want:       0.0,
		},
		{
			name:       "negative_k_returns_0",
			topKIDs:    ids(42, 1, 2),
			relevantID: relevant,
			k:          -1,
			want:       0.0,
		},
		{
			name:       "empty_topKIDs_returns_0",
			topKIDs:    []int64{},
			relevantID: relevant,
			k:          10,
			want:       0.0,
		},
		{
			name:       "nil_topKIDs_returns_0",
			topKIDs:    nil,
			relevantID: relevant,
			k:          10,
			want:       0.0,
		},
		{
			name:       "absent_returns_0",
			topKIDs:    ids(1, 2, 3, 4, 5),
			relevantID: relevant,
			k:          10,
			want:       0.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HitAtK(tc.topKIDs, tc.relevantID, tc.k)
			if got != tc.want {
				t.Errorf("HitAtK(%v, %d, %d) = %v; want %v", tc.topKIDs, tc.relevantID, tc.k, got, tc.want)
			}
		})
	}
}
