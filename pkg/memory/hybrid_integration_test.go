//go:build integration

package memory_test

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/memory/testhelpers"
	"oro/pkg/protocol"
)

// TestHybridSearchFullStackSQLiteVec exercises HybridSearch end-to-end with a
// real on-disk SQLite database, the sqlite-vec extension loaded, a
// SQLiteVecIndex serving the ANN path, and two project partitions ("alpha",
// "beta"). It verifies:
//
//  1. HybridSearch in project alpha returns the 3 alpha-seeded memories that
//     match the query in its top-5 results, AND no beta-seeded memory leaks
//     into the alpha-scoped result set.
//  2. RRF-fused scores are well-formed (no NaN/Inf) and monotonically
//     decreasing.
//  3. Store.Delete removes a memory from both the FTS index (via trigger) and
//     the HybridSearch result set on subsequent queries (the ANN path still
//     surfaces the stale id, but vectorSearchViaIndex skips ids missing from
//     the memories table).
//
// Build-tagged so unit CI stays fast; runs via `go test -tags integration`.
// Skipped when the sqlite-vec extension is unavailable.
func TestHybridSearchFullStackSQLiteVec(t *testing.T) {
	if _, err := dbutil.ResolveSqliteVecLibPath(); err != nil {
		t.Skipf("sqlite-vec not available in this environment: %v", err)
	}

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "hybrid_integration.db")
	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, "SELECT vec_version()"); err != nil {
		t.Skipf("sqlite-vec extension did not load on this connection: %v", err)
	}

	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	idx, err := memory.NewSQLiteVecIndex(db)
	if err != nil {
		t.Fatalf("NewSQLiteVecIndex: %v", err)
	}

	// Dim must match SQLiteVecIndex's FLOAT[384] vec0 DDL — passing 0 here
	// would silently default to 128 and break Upsert at runtime.
	embedder := testhelpers.NewFakeEmbedder(384)

	storeAlpha := memory.NewStore(db)
	storeAlpha.SetEmbedder(embedder)
	storeAlpha.SetVectorIndex(idx)
	storeAlpha.SetProject("alpha")

	storeBeta := memory.NewStore(db)
	storeBeta.SetEmbedder(embedder)
	storeBeta.SetVectorIndex(idx)
	storeBeta.SetProject("beta")

	// Three alpha memories whose token sets overlap the query "retry failed
	// bead" but whose pairwise Jaccard stays below the 0.7 dedup threshold.
	alphaRelevant := []string{
		"retry failed bead via dispatcher manager escalation queue handoff",
		"retry failed bead investigation through trace timeline correlator service",
		"retry failed bead manual override admin diagnostic process complete",
	}
	// Twenty-two alpha distractors with no query-token overlap and unique
	// per-row marker tokens to evade write-time dedup.
	alphaDistractors := buildDistractors("alpha", 22)
	// Five beta memories that DO match the query — they exist solely to verify
	// project isolation: alpha-scoped HybridSearch must filter them out from
	// both the FTS phase (project column) and the ANN phase (vec0 partition).
	betaRelevant := []string{
		"retry failed bead alpha handler delegation outsourcing roster",
		"retry failed bead reroute via secondary worker pool resolver",
		"retry failed bead consolidation report monthly review subsystem",
		"retry failed bead audit trail forensic investigation team workflow",
		"retry failed bead emergency stop button manual lever interlock",
	}
	// Twenty beta distractors to round the seed set out to fifty memories.
	betaDistractors := buildDistractors("beta", 20)

	relevantIDs := seedMemories(t, ctx, storeAlpha, idx, embedder, "alpha", alphaRelevant)
	seedMemories(t, ctx, storeAlpha, idx, embedder, "alpha", alphaDistractors)
	betaIDs := seedMemories(t, ctx, storeBeta, idx, embedder, "beta", betaRelevant)
	seedMemories(t, ctx, storeBeta, idx, embedder, "beta", betaDistractors)

	const query = "retry failed bead"

	// --- Assertion 1: top-5 contains all relevant alpha IDs and no beta IDs. ---
	results, err := storeAlpha.HybridSearch(ctx, query, memory.SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("HybridSearch alpha: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("HybridSearch returned 0 results for query %q", query)
	}

	gotIDs := make(map[int64]bool, len(results))
	for _, r := range results {
		gotIDs[r.ID] = true
	}
	for _, id := range relevantIDs {
		if !gotIDs[id] {
			t.Errorf("expected relevant alpha id=%d in top-%d, got %v", id, len(results), idsOf(results))
		}
	}
	betaIDset := make(map[int64]bool, len(betaIDs))
	for _, id := range betaIDs {
		betaIDset[id] = true
	}
	for _, r := range results {
		if betaIDset[r.ID] {
			t.Errorf("project leak: beta memory id=%d returned in alpha-scoped HybridSearch", r.ID)
		}
	}

	// --- Assertion 2: scores are non-NaN and monotonically decreasing. ---
	for i, r := range results {
		if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) {
			t.Errorf("result[%d] score=%v is NaN or Inf", i, r.Score)
		}
		if i > 0 && r.Score > results[i-1].Score {
			t.Errorf("results not monotonic at i=%d: %v > previous %v", i, r.Score, results[i-1].Score)
		}
	}

	// --- Assertion 3: Delete removes the memory from HybridSearch results. ---
	deletedID := relevantIDs[0]
	if err := storeAlpha.Delete(ctx, deletedID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	postDelete, err := storeAlpha.HybridSearch(ctx, query, memory.SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("HybridSearch after delete: %v", err)
	}
	for _, r := range postDelete {
		if r.ID == deletedID {
			t.Errorf("HybridSearch still returned deleted id=%d", deletedID)
		}
	}
}

func TestHybridSearchEmptyScopeMatchesAllProjectContract(t *testing.T) {
	if _, err := dbutil.ResolveSqliteVecLibPath(); err != nil {
		t.Skipf("sqlite-vec not available in this environment: %v", err)
	}

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "hybrid_empty_scope.db")
	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, "SELECT vec_version()"); err != nil {
		t.Skipf("sqlite-vec extension did not load on this connection: %v", err)
	}
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}

	idx, err := memory.NewSQLiteVecIndex(db)
	if err != nil {
		t.Fatalf("NewSQLiteVecIndex: %v", err)
	}
	embedder := testhelpers.NewFakeEmbedder(384)

	storeAlpha := memory.NewStore(db)
	storeAlpha.SetEmbedder(embedder)
	storeAlpha.SetVectorIndex(idx)
	storeAlpha.SetProject("alpha")

	storeBeta := memory.NewStore(db)
	storeBeta.SetEmbedder(embedder)
	storeBeta.SetVectorIndex(idx)
	storeBeta.SetProject("beta")

	alphaIDs := seedMemories(t, ctx, storeAlpha, idx, embedder, "alpha", []string{
		"retry failed bead alpha integration path",
	})
	betaIDs := seedMemories(t, ctx, storeBeta, idx, embedder, "beta", []string{
		"retry failed bead beta integration path",
	})

	storeAll := memory.NewStore(db)
	storeAll.SetEmbedder(embedder)
	storeAll.SetVectorIndex(idx)
	storeAll.SetProject("")

	results, err := storeAll.HybridSearch(ctx, "retry failed bead", memory.SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("HybridSearch empty project: %v", err)
	}

	got := make(map[int64]bool, len(results))
	for _, r := range results {
		got[r.ID] = true
	}
	for _, id := range append(alphaIDs, betaIDs...) {
		if !got[id] {
			t.Fatalf("expected empty project scope to include id=%d, got %v", id, idsOf(results))
		}
	}
}

// seedMemories inserts each content string into store and mirrors the
// embedding into idx under project. The project parameter is used only for
// the vec-index Upsert and MUST match the project the store was already
// configured with via SetProject — the helper does not (and cannot, as
// store.project is unexported) re-apply SetProject, so the caller is the
// single source of truth for store scope. Returns the inserted IDs in
// insertion order.
func seedMemories(
	t *testing.T,
	ctx context.Context,
	store *memory.Store,
	idx *memory.SQLiteVecIndex,
	embedder *testhelpers.FakeEmbedder,
	project string,
	contents []string,
) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(contents))
	for i, content := range contents {
		id, err := store.Insert(ctx, memory.InsertParams{
			Content:    content,
			Type:       "lesson",
			Source:     "self_report",
			Confidence: 0.9,
		})
		if err != nil {
			t.Fatalf("seed[%d] insert (project=%s): %v", i, project, err)
		}
		if err := idx.Upsert(ctx, id, embedder.Embed(content), project); err != nil {
			t.Fatalf("seed[%d] vec upsert (project=%s, id=%d): %v", i, project, id, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// buildDistractors generates n unique distractor contents whose token sets
// share no terms with the test query "retry failed bead" and whose pairwise
// Jaccard stays well below 0.7 (each row carries a unique marker token).
func buildDistractors(prefix string, n int) []string {
	topics := []string{
		"docker container networking firewall ruleset",
		"postgres index btree gin gist comparison metrics",
		"javascript promise async await chaining patterns",
		"rust borrow checker lifetime annotations cookbook",
		"kubernetes pod scheduling node affinity strategy",
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("%s_marker_%d covers %s observations entry", prefix, i, topics[i%len(topics)])
	}
	return out
}

func idsOf(results []memory.ScoredMemory) []int64 {
	out := make([]int64, len(results))
	for i, r := range results {
		out[i] = r.ID
	}
	return out
}
