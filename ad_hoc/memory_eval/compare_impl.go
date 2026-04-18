// ad_hoc/memory_eval/compare_impl.go
// Core evaluation logic for the precision@k CLI.
package memoryeval

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/protocol"
)

const (
	approvalMarker = "# APPROVED"
	warmThreshold  = 1.30
	coldThreshold  = 1.20
)

// HasApprovalMarker reports whether path contains a line equal to "# APPROVED".
// Returns false (not true) when the line is absent; only returns an error on I/O failure.
func HasApprovalMarker(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open corpus: %w", err)
	}
	defer func() { _ = f.Close() }()

	s := bufio.NewScanner(f)
	for s.Scan() {
		if strings.TrimSpace(s.Text()) == approvalMarker {
			return true, nil
		}
	}
	return false, s.Err()
}

// PrecisionAtK computes precision@k: the fraction of the top-k results that are
// relevant. Returns 0 when k ≤ 0, topKIDs is empty, or relevant is nil/empty.
func PrecisionAtK(topKIDs []int64, relevant map[int64]bool, k int) float64 {
	if k <= 0 || len(topKIDs) == 0 || len(relevant) == 0 {
		return 0
	}
	limit := min(k, len(topKIDs))
	hits := 0
	for _, id := range topKIDs[:limit] {
		if relevant[id] {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// CheckGate returns true iff:
//
//	warmP10 >= 1.30 * baseP10  AND  coldP10 >= 1.20 * baseP10
func CheckGate(baseP10, warmP10, coldP10 float64) bool {
	return warmP10 >= warmThreshold*baseP10 && coldP10 >= coldThreshold*baseP10
}

// RunConfig evaluates precision@5 and precision@10 for the named configuration.
// cfg must be one of: "tfidf", "dispatcher-warm", "solo-cli-cold".
// Seeds a fresh in-memory SQLite store with builtinFixtures and runs HybridSearch
// for each unique query in corpus against labeled relevant_memory_ids.
func RunConfig(corpus []CorpusEntry, cfg string, k int) (p5, p10 float64, err error) {
	if k <= 0 {
		return 0, 0, fmt.Errorf("k must be > 0, got %d", k)
	}
	validCfgs := map[string]bool{"tfidf": true, "dispatcher-warm": true, "solo-cli-cold": true}
	if !validCfgs[cfg] {
		return 0, 0, fmt.Errorf("unknown config %q: must be one of tfidf, dispatcher-warm, solo-cli-cold", cfg)
	}
	return RunConfigWithEmbedder(corpus, embedderForCfg(cfg), k)
}

// embedderForCfg returns the Embedder for the named config.
// "solo-cli-cold" returns nil (FTS5-only search with no vector component).
func embedderForCfg(cfg string) memory.Embedder {
	switch cfg {
	case "tfidf":
		return memory.NewEmbedder()
	case "dispatcher-warm":
		// Production path would use BGE reranker (ONNX); eval uses TF-IDF proxy.
		return memory.NewEmbedder()
	default: // "solo-cli-cold"
		return nil
	}
}

// RunConfigWithEmbedder is the testable core of RunConfig. It creates a fresh
// in-memory SQLite store, seeds it with builtinFixtures, attaches emb (nil =
// FTS5-only), and runs HybridSearch for each unique query in corpus.
// Tests inject testhelpers.NewFakeEmbedder here to stay ONNX-free.
func RunConfigWithEmbedder(corpus []CorpusEntry, emb memory.Embedder, k int) (p5, p10 float64, err error) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		return 0, 0, fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		return 0, 0, fmt.Errorf("exec schema: %w", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemorySearchEvents); err != nil {
		return 0, 0, fmt.Errorf("exec telemetry migration: %w", err)
	}

	store := memory.NewStore(db)
	if emb != nil {
		store.SetEmbedder(emb)
	}

	ctx := context.Background()

	// Seed store with builtin fixtures; track fixture.id → store-assigned id.
	fixtures := builtinFixtures()
	fixtureIDMap := make(map[int64]int64, len(fixtures))
	for _, f := range fixtures {
		id, insErr := store.Insert(ctx, memory.InsertParams{
			Content: f.content,
			Type:    f.memType,
			Source:  "fixture",
		})
		if insErr != nil {
			return 0, 0, fmt.Errorf("insert fixture %d: %w", f.id, insErr)
		}
		fixtureIDMap[f.id] = id
	}

	// Build per-query relevant-set, mapping corpus candidate IDs to store IDs.
	type relevantSet = map[int64]bool
	queryRelevant := make(map[string]relevantSet)
	for _, e := range corpus {
		if e.Relevant == nil || !*e.Relevant {
			continue
		}
		storeID, ok := fixtureIDMap[e.CandidateMemoryID]
		if !ok {
			fmt.Fprintf(os.Stderr,
				"warning: candidate_memory_id %d not in seeded store, skipping\n",
				e.CandidateMemoryID)
			continue
		}
		if queryRelevant[e.Query] == nil {
			queryRelevant[e.Query] = make(relevantSet)
		}
		queryRelevant[e.Query][storeID] = true
	}

	// Collect unique queries, preserving first-seen order.
	seen := make(map[string]struct{}, len(corpus))
	queries := make([]string, 0, len(corpus))
	for _, e := range corpus {
		if _, ok := seen[e.Query]; !ok {
			seen[e.Query] = struct{}{}
			queries = append(queries, e.Query)
		}
	}
	if len(queries) == 0 {
		return 0, 0, fmt.Errorf("no queries in corpus")
	}

	if len(corpus) < 100 {
		fmt.Fprintf(os.Stderr, "warning: corpus has only %d entries (< 100)\n", len(corpus))
	}

	var sum5, sum10 float64
	for _, q := range queries {
		rel := queryRelevant[q] // nil → empty relevant set
		results, searchErr := store.HybridSearch(ctx, q, memory.SearchOpts{Limit: k})
		if searchErr != nil {
			return 0, 0, fmt.Errorf("search %q: %w", q, searchErr)
		}
		ids := make([]int64, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		sum5 += PrecisionAtK(ids, rel, 5)
		sum10 += PrecisionAtK(ids, rel, k)
	}

	n := float64(len(queries))
	return sum5 / n, sum10 / n, nil
}
