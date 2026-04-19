//go:build cgo && darwin

package memoryeval

import (
	"testing"
)

// TestRunConfigUsesSidecarSeed verifies that RunConfigWithEmbedder seeds the
// store from the provided anchors, not from builtinFixtures. Anchor ID 99 is
// not in builtinFixtures (IDs 1-12); if the old path were used, the anchor
// would never be found and mrr would be 0.
func TestRunConfigUsesSidecarSeed(t *testing.T) {
	anchors := []CorpusAnchor{
		{ID: 99, Type: "lesson", Content: "sidecar anchor for eval harness seeding verification test"},
	}
	rel := true
	entries := []CorpusEntry{
		{Query: "sidecar anchor eval harness seeding", CandidateMemoryID: 99, Relevant: &rel, Source: "fixture"},
	}
	mrr, _, _, err := RunConfigWithEmbedder(entries, anchors, "tfidf", 10)
	if err != nil {
		t.Fatalf("RunConfigWithEmbedder: %v", err)
	}
	if mrr == 0 {
		t.Error("expected MRR > 0 when anchor seeded from sidecar; got 0 (implies builtinFixtures used instead)")
	}
}

// TestRunConfigMRRNotZero verifies that the tfidf config returns MRR > 0 when
// the anchor content lexically matches the query (proves FTS5+TFIDF search works).
func TestRunConfigMRRNotZero(t *testing.T) {
	anchors := []CorpusAnchor{
		{ID: 1, Type: "lesson", Content: "ruff must run before pyright in Python linting pipelines"},
	}
	rel := true
	entries := []CorpusEntry{
		{Query: "ruff pyright linting pipelines", CandidateMemoryID: 1, Relevant: &rel, Source: "fixture"},
	}
	mrr, _, _, err := RunConfigWithEmbedder(entries, anchors, "tfidf", 10)
	if err != nil {
		t.Fatalf("RunConfigWithEmbedder: %v", err)
	}
	if mrr == 0 {
		t.Errorf("expected tfidf MRR > 0 for lexical anchor query; got 0")
	}
}

// TestRunConfigEmptyAnchorsError verifies that empty anchors return an error.
func TestRunConfigEmptyAnchorsError(t *testing.T) {
	entries := []CorpusEntry{
		{Query: "test query", CandidateMemoryID: 1, Source: "fixture"},
	}
	_, _, _, err := RunConfigWithEmbedder(entries, nil, "tfidf", 10)
	if err == nil {
		t.Error("expected error for empty anchors; got nil")
	}
}

// TestRunConfigUnknownCandidateSkipped verifies that a corpus entry referencing
// an anchor ID not in the provided anchors is skipped gracefully (no error).
func TestRunConfigUnknownCandidateSkipped(t *testing.T) {
	anchors := []CorpusAnchor{
		{ID: 1, Type: "lesson", Content: "real anchor with known content for testing purposes"},
	}
	rel := true
	entries := []CorpusEntry{
		{Query: "real anchor known content", CandidateMemoryID: 999, Relevant: &rel, Source: "fixture"},
		{Query: "real anchor known content", CandidateMemoryID: 1, Relevant: boolPtr(false), Source: "fixture"},
	}
	_, _, _, err := RunConfigWithEmbedder(entries, anchors, "tfidf", 10)
	if err != nil {
		t.Fatalf("expected no error for unknown candidate ID; got: %v", err)
	}
}
