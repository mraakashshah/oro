//go:build cgo && darwin

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	memoryeval "oro/ad_hoc/memory_eval"
)

// buildTestCache pre-populates paraphrase_cache.jsonl with entries for every
// anchor that SelectAnchors selects for the given db and seed.
func buildTestCache(t *testing.T, seed int64, cachePath string) {
	t.Helper()
	db := openTestDB(t)
	insertDiverseMemories(t, db)
	anchors, err := SelectAnchors(db, seed, 50)
	if err != nil {
		t.Fatalf("SelectAnchors for cache bootstrap: %v", err)
	}
	cacheEntries := make(map[string]memoryeval.CacheEntry, len(anchors))
	for _, a := range anchors {
		sha := anchorSHA(a.Content)
		key := memoryeval.CacheKey(sha, memoryeval.ParaphrasePromptVersion)
		cacheEntries[key] = memoryeval.CacheEntry{
			AnchorSHA:     sha,
			PromptVersion: memoryeval.ParaphrasePromptVersion,
			Queries: []string{
				"how does this feature work in the system",
				"what is the best approach for handling this",
				"can you describe the underlying mechanism here",
			},
		}
	}
	if err := memoryeval.WriteCache(cachePath, cacheEntries); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
}

func TestRebuildCorpusDeterministic(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t)
	insertDiverseMemories(t, db)

	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")
	buildTestCache(t, 42, cachePath)

	fixedNow := func() time.Time {
		return time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC)
	}

	doRun := func(tag string) (corpus, anchors []byte) {
		outPath := filepath.Join(dir, "corpus_"+tag+".jsonl")
		anchorsPath := filepath.Join(dir, "anchors_"+tag+".jsonl")
		code := run(runConfig{
			db:          db,
			dbPath:      ":memory:",
			outPath:     outPath,
			anchorsPath: anchorsPath,
			cachePath:   cachePath,
			seed:        42,
			noAPI:       true,
			now:         fixedNow,
		})
		if code != 0 {
			t.Fatalf("run(%s) exit %d", tag, code)
		}
		c, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("read corpus %s: %v", tag, err)
		}
		a, err := os.ReadFile(anchorsPath)
		if err != nil {
			t.Fatalf("read anchors %s: %v", tag, err)
		}
		return c, a
	}

	corpus1, anchors1 := doRun("run1")
	corpus2, anchors2 := doRun("run2")

	if !bytes.Equal(corpus1, corpus2) {
		t.Error("corpus.jsonl not byte-identical across two runs with same seed and cache")
	}
	if !bytes.Equal(anchors1, anchors2) {
		t.Error("corpus_anchors.jsonl not byte-identical across two runs with same seed and cache")
	}
}

func TestGroundTruthIntegrity(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t)
	insertDiverseMemories(t, db)

	cachePath := filepath.Join(dir, "paraphrase_cache.jsonl")
	buildTestCache(t, 42, cachePath)

	outPath := filepath.Join(dir, "corpus.jsonl")
	anchorsPath := filepath.Join(dir, "anchors.jsonl")

	code := run(runConfig{
		db:          db,
		dbPath:      ":memory:",
		outPath:     outPath,
		anchorsPath: anchorsPath,
		cachePath:   cachePath,
		seed:        42,
		noAPI:       true,
		now:         time.Now,
	})
	if code != 0 {
		t.Fatalf("run exit %d", code)
	}

	entries, err := memoryeval.LoadCorpus(outPath)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("corpus is empty")
	}

	// Build anchor ID set from corpus_anchors.jsonl.
	anchorIDSet := make(map[int64]bool)
	af, err := os.Open(anchorsPath) //nolint:gosec
	if err != nil {
		t.Fatalf("open anchors: %v", err)
	}
	defer func() { _ = af.Close() }()
	sc := bufio.NewScanner(af)
	for sc.Scan() {
		var a CorpusAnchor
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			t.Fatalf("unmarshal anchor line: %v", err)
		}
		anchorIDSet[a.ID] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan anchors: %v", err)
	}

	// Check 1: every candidate_memory_id in corpus has a row in corpus_anchors.jsonl.
	for i, e := range entries {
		if !anchorIDSet[e.CandidateMemoryID] {
			t.Errorf("entry[%d]: candidate_memory_id %d missing from corpus_anchors.jsonl", i, e.CandidateMemoryID)
		}
	}

	// Check 2: no null relevant values.
	for i, e := range entries {
		if e.Relevant == nil {
			t.Errorf("entry[%d]: null relevant for query %q candidate %d", i, e.Query, e.CandidateMemoryID)
		}
	}

	// Check 3: every anchor appears as relevant:true for >=1 query.
	trueByID := make(map[int64]bool)
	for _, e := range entries {
		if e.Relevant != nil && *e.Relevant {
			trueByID[e.CandidateMemoryID] = true
		}
	}
	for id := range anchorIDSet {
		if !trueByID[id] {
			t.Errorf("anchor id=%d never appears as relevant:true in corpus", id)
		}
	}
}

func TestFallbackRateAbort(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t)
	insertDiverseMemories(t, db)

	highFallbackFn := func(anchors []CorpusAnchor, _ string, _ bool) (map[int64][]string, float64, error) {
		queries := make(map[int64][]string, len(anchors))
		for _, a := range anchors {
			queries[a.ID] = []string{
				"how does this feature work in the system",
				"what is the best approach for handling this",
				"can you describe the underlying mechanism here",
			}
		}
		return queries, 0.25, nil // 25% > 20% threshold
	}

	code := run(runConfig{
		db:           db,
		dbPath:       ":memory:",
		outPath:      filepath.Join(dir, "corpus.jsonl"),
		anchorsPath:  filepath.Join(dir, "anchors.jsonl"),
		cachePath:    filepath.Join(dir, "cache.jsonl"),
		seed:         42,
		noAPI:        true,
		now:          time.Now,
		paraphraseFn: highFallbackFn,
	})

	if code != 2 {
		t.Errorf("want exit code 2 for fallback_rate > 0.20, got %d", code)
	}
}
