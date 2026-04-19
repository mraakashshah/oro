//go:build cgo && darwin

// Package memoryeval: harness.go — setupConfig, seedStoreWithVectors, and
// RunConfigWithEmbedder. Requires cgo+darwin for BGE model loading.
package memoryeval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// setupConfig opens an in-memory SQLite DB, runs all required migrations,
// and wires the memory.Store with the embedder, vector index, and reranker
// appropriate for cfg.
//
// Returned cleanup MUST be called (defer) by the caller to close the DB.
//
// Config matrix:
//
//	tfidf           → TFIDFEmbedder,  idx=nil, reranker=nil
//	dispatcher-warm → BGEEmbedder,    idx=nil (sqlite-vec not yet wired), BGEReranker
//	solo-cli-cold   → BGEEmbedder,    idx=nil, reranker=nil
func setupConfig(cfg string) (store *memory.Store, emb memory.Embedder, idx memory.VectorIndex, cleanup func(), err error) {
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open in-memory db: %w", err)
	}
	cleanup = func() { _ = db.Close() }

	for _, ddl := range []string{
		protocol.SchemaDDL,
		protocol.MigrateSemanticMemorySearchEvents,
		protocol.MigrateSemanticMemoryChunks,
		protocol.MigrateSemanticMemoryDense,
		protocol.MigrateSemanticMemoryBackfillState,
	} {
		if _, execErr := db.Exec(ddl); execErr != nil {
			cleanup()
			return nil, nil, nil, nil, fmt.Errorf("run migration: %w", execErr)
		}
	}

	store = memory.NewStore(db)
	store.SetProject("oro_eval")

	switch cfg {
	case "tfidf":
		emb = memory.NewEmbedder()
		store.SetEmbedder(emb)

	case "dispatcher-warm":
		bgeEmb, bgeErr := memory.NewBGEEmbedder(resolveModelPath("bge-small-en-v1.5"))
		if bgeErr != nil {
			cleanup()
			return nil, nil, nil, nil, fmt.Errorf("dispatcher-warm embedder: %w", bgeErr)
		}
		emb = bgeEmb
		store.SetEmbedder(emb)

		reranker, rrErr := memory.NewBGEReranker(resolveModelPath("bge-reranker-base"))
		if rrErr != nil {
			cleanup()
			return nil, nil, nil, nil, fmt.Errorf("dispatcher-warm reranker: %w", rrErr)
		}
		store.SetReranker(reranker)
		rerank := true
		store.SetSemanticConfig(memory.SemanticConfig{Rerank: &rerank, ANNTopK: 20, FinalTopK: 10})

	case "solo-cli-cold":
		bgeEmb, bgeErr := memory.NewBGEEmbedder(resolveModelPath("bge-small-en-v1.5"))
		if bgeErr != nil {
			cleanup()
			return nil, nil, nil, nil, fmt.Errorf("solo-cli-cold embedder: %w", bgeErr)
		}
		emb = bgeEmb
		store.SetEmbedder(emb)

	default:
		cleanup()
		return nil, nil, nil, nil, fmt.Errorf("unknown config %q: must be one of tfidf, dispatcher-warm, solo-cli-cold", cfg)
	}

	return store, emb, idx, cleanup, nil
}

// seedStoreWithVectors inserts anchors into store and, when both idx and emb
// are non-nil, upserts their embeddings into the vector index under project.
// The caller MUST call store.SetProject(project) before invoking this helper.
// Returns a map from anchor.ID (corpus ID) to the store-assigned row ID.
func seedStoreWithVectors(
	ctx context.Context,
	store *memory.Store,
	idx memory.VectorIndex,
	emb memory.Embedder,
	anchors []CorpusAnchor,
	project string,
) (map[int64]int64, error) {
	anchorMap := make(map[int64]int64, len(anchors))
	for _, a := range anchors {
		id, err := store.Insert(ctx, memory.InsertParams{
			Content: a.Content,
			Type:    a.Type,
			Source:  "eval_fixture",
		})
		if err != nil {
			return nil, fmt.Errorf("insert anchor %d: %w", a.ID, err)
		}
		anchorMap[a.ID] = id
		if idx != nil && emb != nil {
			vec := emb.Embed(a.Content)
			if upsertErr := idx.Upsert(ctx, id, vec, project); upsertErr != nil {
				return nil, fmt.Errorf("upsert anchor %d vec: %w", a.ID, upsertErr)
			}
		}
	}
	return anchorMap, nil
}

// RunConfigWithEmbedder evaluates MRR, Hit@10, and Hit@1 for the named
// configuration using the provided anchors as the seeded store and corpus
// entries as the query/relevance ground truth.
//
// cfg must be one of: "tfidf", "dispatcher-warm", "solo-cli-cold".
// Empty anchors returns an error "corpus has no anchors".
// Corpus entries referencing unknown anchor IDs are skipped with a stderr warning.
func RunConfigWithEmbedder(entries []CorpusEntry, anchors []CorpusAnchor, cfg string, k int) (mrr, hit10, hit1 float64, err error) {
	if len(anchors) == 0 {
		return 0, 0, 0, fmt.Errorf("corpus has no anchors")
	}
	if k <= 0 {
		return 0, 0, 0, fmt.Errorf("k must be > 0, got %d", k)
	}

	store, emb, idx, cleanup, err := setupConfig(cfg)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("setup config %q: %w", cfg, err)
	}
	defer cleanup()

	ctx := context.Background()

	anchorMap, err := seedStoreWithVectors(ctx, store, idx, emb, anchors, "oro_eval")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("seed store: %w", err)
	}

	// Build per-query anchor map: query → store-assigned anchor ID.
	// Each corpus entry with Relevant=true names the anchor for its query.
	queryAnchor := make(map[string]int64)
	for _, e := range entries {
		if e.Relevant == nil || !*e.Relevant {
			continue
		}
		storeID, ok := anchorMap[e.CandidateMemoryID]
		if !ok {
			fmt.Fprintf(os.Stderr,
				"warning: candidate_memory_id %d not in seeded store, skipping\n",
				e.CandidateMemoryID)
			continue
		}
		queryAnchor[e.Query] = storeID
	}

	// Collect unique queries, preserving first-seen order.
	seen := make(map[string]struct{}, len(entries))
	queries := make([]string, 0, len(entries))
	for _, e := range entries {
		if _, ok := seen[e.Query]; !ok {
			seen[e.Query] = struct{}{}
			queries = append(queries, e.Query)
		}
	}
	if len(queries) == 0 {
		return 0, 0, 0, fmt.Errorf("no queries in corpus")
	}

	if len(entries) < 100 {
		fmt.Fprintf(os.Stderr, "warning: corpus has only %d entries (< 100)\n", len(entries))
	}

	// Compute MRR, Hit@k, Hit@1. Denominator = total query count (queries with
	// no labeled anchor contribute 0).
	var sumMRR, sumHit10, sumHit1 float64
	for _, q := range queries {
		anchorStoreID, hasAnchor := queryAnchor[q]
		if !hasAnchor {
			continue
		}
		results, searchErr := store.HybridSearch(ctx, q, memory.SearchOpts{Limit: k})
		if searchErr != nil {
			return 0, 0, 0, fmt.Errorf("search %q: %w", q, searchErr)
		}
		ids := make([]int64, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		sumMRR += MRR(ids, anchorStoreID, k)
		sumHit10 += HitAtK(ids, anchorStoreID, k)
		sumHit1 += HitAtK(ids, anchorStoreID, 1)
	}

	n := float64(len(queries))
	return sumMRR / n, sumHit10 / n, sumHit1 / n, nil
}

// resolveModelPath returns the path to a model subdirectory under the model
// base dir. Uses ORO_MODEL_DIR env override, else ~/.oro/models.
func resolveModelPath(name string) string {
	if d := os.Getenv("ORO_MODEL_DIR"); d != "" {
		return filepath.Join(d, name)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".oro", "models", name)
}
