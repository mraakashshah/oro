package memoryeval_test

import (
	"fmt"
	"path/filepath"
	"testing"

	memoryeval "oro/ad_hoc/memory_eval"
)

func TestCorpusCandidateExtraction(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "corpus.jsonl")

	// Use nonexistent DB path to trigger fixture fallback (source="fixture").
	if err := memoryeval.ExtractCorpus("nonexistent.db", outputPath); err != nil {
		t.Fatalf("ExtractCorpus: %v", err)
	}

	entries, err := memoryeval.LoadCorpus(outputPath)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	if len(entries) != 100 {
		t.Fatalf("expected 100 entries, got %d", len(entries))
	}

	seen := make(map[string]bool)
	for i, e := range entries {
		if e.Query == "" {
			t.Errorf("entry %d: empty query", i)
		}
		if e.CandidateMemoryID <= 0 {
			t.Errorf("entry %d: non-positive candidate_memory_id %d", i, e.CandidateMemoryID)
		}
		if e.Relevant != nil {
			t.Errorf("entry %d: expected null relevant, got non-nil", i)
		}
		if e.Source != "history" && e.Source != "fixture" {
			t.Errorf("entry %d: invalid source %q", i, e.Source)
		}

		key := fmt.Sprintf("%s\x00%d", e.Query, e.CandidateMemoryID)
		if seen[key] {
			t.Errorf("entry %d: duplicate (query, candidate_memory_id) pair", i)
		}
		seen[key] = true
	}
}
