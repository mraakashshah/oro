// ad_hoc/memory_eval/compare_test.go
//
//nolint:testpackage // tests cover unexported helpers in this ad hoc eval package
package memoryeval

import (
	"os"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestPrecisionAtKAndGates(t *testing.T) {
	t.Run("precisionAtK_basic", func(t *testing.T) {
		ids := []int64{1, 2, 3, 4, 5}
		rel := map[int64]bool{1: true, 3: true}
		got := PrecisionAtK(ids, rel, 5)
		if got != 0.4 {
			t.Errorf("got %v, want 0.4", got)
		}
	})

	t.Run("precisionAtK_kLargerThanResults", func(t *testing.T) {
		// 1 result, 1 relevant, k=5 → 1 hit / 5 = 0.2
		ids := []int64{1}
		rel := map[int64]bool{1: true}
		got := PrecisionAtK(ids, rel, 5)
		if got != 0.2 {
			t.Errorf("got %v, want 0.2 (1 hit / k=5)", got)
		}
	})

	t.Run("precisionAtK_noRelevant", func(t *testing.T) {
		ids := []int64{1, 2, 3}
		got := PrecisionAtK(ids, nil, 5)
		if got != 0 {
			t.Errorf("got %v, want 0 for nil relevant set", got)
		}
	})

	t.Run("precisionAtK_zeroK", func(t *testing.T) {
		ids := []int64{1, 2, 3}
		rel := map[int64]bool{1: true}
		got := PrecisionAtK(ids, rel, 0)
		if got != 0 {
			t.Errorf("got %v, want 0 for k=0", got)
		}
	})

	t.Run("precisionAtK_allRelevant", func(t *testing.T) {
		ids := []int64{1, 2, 3}
		rel := map[int64]bool{1: true, 2: true, 3: true}
		got := PrecisionAtK(ids, rel, 3)
		if got != 1.0 {
			t.Errorf("got %v, want 1.0", got)
		}
	})

	t.Run("checkGate_passes", func(t *testing.T) {
		if !CheckGate(0.2, 0.26, 0.24).Pass {
			t.Error("expected gate to pass")
		}
	})

	t.Run("checkGate_fails_warm", func(t *testing.T) {
		if CheckGate(0.2, 0.25, 0.24).Pass {
			t.Error("expected gate to fail (warm below threshold)")
		}
	})

	t.Run("checkGate_fails_cold", func(t *testing.T) {
		if CheckGate(0.2, 0.26, 0.23).Pass {
			t.Error("expected gate to fail (cold below threshold)")
		}
	})

	t.Run("checkGate_degenerate_zeros", func(t *testing.T) {
		// zero-baseline guard: baseMRR == 0 always fails regardless of warm/cold
		if CheckGate(0, 0, 0).Pass {
			t.Error("expected gate to fail when baseline is 0")
		}
	})

	t.Run("hasApprovalMarker_absent", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "corpus*.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("# source: fixture\n")
		_ = f.Close()

		ok, err := HasApprovalMarker(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected no approval marker")
		}
	})

	t.Run("hasApprovalMarker_present", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "corpus*.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("# source: fixture\n# APPROVED\n{\"query\":\"test\",\"candidate_memory_id\":1}\n")
		_ = f.Close()

		ok, err := HasApprovalMarker(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("expected approval marker to be present")
		}
	})
}
