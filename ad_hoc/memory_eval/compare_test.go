// ad_hoc/memory_eval/compare_test.go
package memoryeval

import (
	"os"
	"testing"

	"oro/pkg/memory"
	"oro/pkg/memory/testhelpers"
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
		// warmP10 = 0.26 >= 1.30 * 0.2 = 0.26 → pass
		// coldP10 = 0.24 >= 1.20 * 0.2 = 0.24 → pass
		if !CheckGate(0.2, 0.26, 0.24).Pass {
			t.Error("expected gate to pass")
		}
	})

	t.Run("checkGate_fails_warm", func(t *testing.T) {
		// warmP10 = 0.25 < 1.30 * 0.2 = 0.26 → fail
		if CheckGate(0.2, 0.25, 0.24).Pass {
			t.Error("expected gate to fail (warm below threshold)")
		}
	})

	t.Run("checkGate_fails_cold", func(t *testing.T) {
		// coldP10 = 0.23 < 1.20 * 0.2 = 0.24 → fail
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

	t.Run("runConfig_tfidf_noError", func(t *testing.T) {
		corpus := makeMinimalCorpus()
		p5, p10, err := RunConfig(corpus, "tfidf", 10)
		if err != nil {
			t.Fatalf("RunConfig tfidf: %v", err)
		}
		if p5 < 0 || p5 > 1 {
			t.Errorf("p5 out of [0,1]: %v", p5)
		}
		if p10 < 0 || p10 > 1 {
			t.Errorf("p10 out of [0,1]: %v", p10)
		}
	})

	t.Run("runConfig_soloCliCold_noError", func(t *testing.T) {
		corpus := makeMinimalCorpus()
		p5, p10, err := RunConfig(corpus, "solo-cli-cold", 10)
		if err != nil {
			t.Fatalf("RunConfig solo-cli-cold: %v", err)
		}
		if p5 < 0 || p5 > 1 {
			t.Errorf("p5 out of [0,1]: %v", p5)
		}
		if p10 < 0 || p10 > 1 {
			t.Errorf("p10 out of [0,1]: %v", p10)
		}
	})

	t.Run("runConfigWithEmbedder_fakeJaccard_noError", func(t *testing.T) {
		corpus := makeMinimalCorpus()
		emb := testhelpers.NewFakeEmbedder(0)
		p5, p10, err := RunConfigWithEmbedder(corpus, emb, 10)
		if err != nil {
			t.Fatalf("RunConfigWithEmbedder: %v", err)
		}
		if p5 < 0 || p5 > 1 {
			t.Errorf("p5 out of [0,1]: %v", p5)
		}
		if p10 < 0 || p10 > 1 {
			t.Errorf("p10 out of [0,1]: %v", p10)
		}
	})

	t.Run("runConfigWithEmbedder_nilEmbedder_noError", func(t *testing.T) {
		corpus := makeMinimalCorpus()
		p5, p10, err := RunConfigWithEmbedder(corpus, nil, 10)
		if err != nil {
			t.Fatalf("RunConfigWithEmbedder nil: %v", err)
		}
		if p5 < 0 || p5 > 1 {
			t.Errorf("p5 out of [0,1]: %v", p5)
		}
		if p10 < 0 || p10 > 1 {
			t.Errorf("p10 out of [0,1]: %v", p10)
		}
	})

	t.Run("gateFailsWithUnlabeledCorpus", func(t *testing.T) {
		// No labeled relevant items → precision = 0 for all configs.
		// Zero-baseline guard triggers: base==0 → gate fails.
		corpus := makeUnlabeledCorpus()

		_, baseP10, err := RunConfigWithEmbedder(corpus, memory.NewEmbedder(), 10)
		if err != nil {
			t.Fatal(err)
		}
		_, warmP10, err := RunConfigWithEmbedder(corpus, testhelpers.NewFakeEmbedder(0), 10)
		if err != nil {
			t.Fatal(err)
		}
		_, coldP10, err := RunConfigWithEmbedder(corpus, nil, 10)
		if err != nil {
			t.Fatal(err)
		}

		if CheckGate(baseP10, warmP10, coldP10).Pass {
			t.Errorf("gate should fail when baseline is 0: base=%v warm=%v cold=%v",
				baseP10, warmP10, coldP10)
		}
	})

	t.Run("unknownCandidateIDSkippedWithWarning", func(t *testing.T) {
		// candidate_memory_id 999 doesn't exist in the fixture store; it should
		// be skipped gracefully (no error).
		corpus := []CorpusEntry{
			{Query: "ruff linting", CandidateMemoryID: 999, Relevant: boolPtr(true), Source: "fixture"},
			{Query: "ruff linting", CandidateMemoryID: 1, Relevant: boolPtr(false), Source: "fixture"},
		}
		_, _, err := RunConfigWithEmbedder(corpus, nil, 10)
		if err != nil {
			t.Fatalf("expected no error for unknown candidate ID: %v", err)
		}
	})
}

// makeMinimalCorpus returns a small corpus with two queries, each having
// one labeled-relevant fixture memory (IDs 1 and 2 — from builtinFixtures).
func makeMinimalCorpus() []CorpusEntry {
	return []CorpusEntry{
		{Query: "ruff pyright linting", CandidateMemoryID: 1, Relevant: boolPtr(true), Source: "fixture"},
		{Query: "ruff pyright linting", CandidateMemoryID: 2, Relevant: boolPtr(false), Source: "fixture"},
		{Query: "sqlite wal consistency", CandidateMemoryID: 2, Relevant: boolPtr(true), Source: "fixture"},
		{Query: "sqlite wal consistency", CandidateMemoryID: 1, Relevant: boolPtr(false), Source: "fixture"},
	}
}

// makeUnlabeledCorpus returns a corpus with all relevant fields nil.
func makeUnlabeledCorpus() []CorpusEntry {
	return []CorpusEntry{
		{Query: "golang testing patterns", CandidateMemoryID: 1, Source: "fixture"},
		{Query: "golang testing patterns", CandidateMemoryID: 2, Source: "fixture"},
	}
}
