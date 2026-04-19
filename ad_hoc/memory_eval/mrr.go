// ad_hoc/memory_eval/mrr.go
// MRR and hit-rate evaluation functions for the compare CLI.
package memoryeval

import (
	"context"
	"fmt"
	"os"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// MRRScore computes the reciprocal rank of the first relevant result in topKIDs.
// Returns 0 when no relevant result is found, or when relevant is nil/empty.
func MRRScore(topKIDs []int64, relevant map[int64]bool) float64 {
	for i, id := range topKIDs {
		if relevant[id] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// HitAt returns 1.0 if any of the top-k results is relevant, 0 otherwise.
func HitAt(topKIDs []int64, relevant map[int64]bool, k int) float64 {
	if k <= 0 || len(topKIDs) == 0 || len(relevant) == 0 {
		return 0
	}
	limit := min(k, len(topKIDs))
	for _, id := range topKIDs[:limit] {
		if relevant[id] {
			return 1
		}
	}
	return 0
}

// RunConfigMRR evaluates MRR, hit@10, and hit@1 for the named configuration.
// cfg must be one of: "tfidf", "dispatcher-warm", "solo-cli-cold".
// Seeds a fresh in-memory SQLite store with anchors and runs HybridSearch
// for each unique query in corpus.
func RunConfigMRR(corpus []CorpusEntry, anchors []CorpusAnchor, cfg string, k int) (mrr, hit10, hit1 float64, runtimeMS int64, err error) {
	if k <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("k must be > 0, got %d", k)
	}
	validCfgs := map[string]bool{"tfidf": true, "dispatcher-warm": true, "solo-cli-cold": true}
	if !validCfgs[cfg] {
		return 0, 0, 0, 0, fmt.Errorf("unknown config %q: must be one of tfidf, dispatcher-warm, solo-cli-cold", cfg)
	}

	start := time.Now()

	db, openErr := dbutil.OpenDB(":memory:")
	if openErr != nil {
		return 0, 0, 0, 0, fmt.Errorf("open db: %w", openErr)
	}
	defer func() { _ = db.Close() }()

	if _, execErr := db.Exec(protocol.SchemaDDL); execErr != nil {
		return 0, 0, 0, 0, fmt.Errorf("exec schema: %w", execErr)
	}
	if _, execErr := db.Exec(protocol.MigrateSemanticMemorySearchEvents); execErr != nil {
		return 0, 0, 0, 0, fmt.Errorf("exec telemetry migration: %w", execErr)
	}

	store := memory.NewStore(db)
	if emb := embedderForCfg(cfg); emb != nil {
		store.SetEmbedder(emb)
	}

	ctx := context.Background()

	// Seed store with anchors; track anchor.ID → store-assigned ID.
	anchorIDMap := make(map[int64]int64, len(anchors))
	for _, a := range anchors {
		id, insErr := store.Insert(ctx, memory.InsertParams{
			Content: a.Content,
			Type:    a.Type,
			Source:  "anchor",
		})
		if insErr != nil {
			return 0, 0, 0, 0, fmt.Errorf("insert anchor %d: %w", a.ID, insErr)
		}
		anchorIDMap[a.ID] = id
	}

	// Build per-query relevant sets, mapping corpus candidate IDs to store IDs.
	type relevantSet = map[int64]bool
	queryRelevant := make(map[string]relevantSet)
	for _, e := range corpus {
		if e.Relevant == nil || !*e.Relevant {
			continue
		}
		storeID, ok := anchorIDMap[e.CandidateMemoryID]
		if !ok {
			fmt.Fprintf(os.Stderr,
				"warning: candidate_memory_id %d not in anchors, skipping\n",
				e.CandidateMemoryID)
			continue
		}
		if queryRelevant[e.Query] == nil {
			queryRelevant[e.Query] = make(relevantSet)
		}
		queryRelevant[e.Query][storeID] = true
	}

	// Collect unique queries preserving first-seen order.
	seen := make(map[string]struct{}, len(corpus))
	queries := make([]string, 0, len(corpus))
	for _, e := range corpus {
		if _, ok := seen[e.Query]; !ok {
			seen[e.Query] = struct{}{}
			queries = append(queries, e.Query)
		}
	}
	if len(queries) == 0 {
		return 0, 0, 0, 0, fmt.Errorf("no queries in corpus")
	}

	var sumMRR, sumHit10, sumHit1 float64
	for _, q := range queries {
		rel := queryRelevant[q]
		results, searchErr := store.HybridSearch(ctx, q, memory.SearchOpts{Limit: k})
		if searchErr != nil {
			return 0, 0, 0, 0, fmt.Errorf("search %q: %w", q, searchErr)
		}
		ids := make([]int64, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		sumMRR += MRRScore(ids, rel)
		sumHit10 += HitAt(ids, rel, 10)
		sumHit1 += HitAt(ids, rel, 1)
	}

	n := float64(len(queries))
	runtimeMS = time.Since(start).Milliseconds()
	return sumMRR / n, sumHit10 / n, sumHit1 / n, runtimeMS, nil
}
