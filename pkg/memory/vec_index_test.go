package memory_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/memory/testhelpers"
)

// openVecTestDB opens an in-memory SQLite DB and skips the test if the
// sqlite-vec extension is not loaded on that connection.
func openVecTestDB(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := dbutil.ResolveSqliteVecLibPath(); err != nil {
		t.Skipf("sqlite-vec extension not available: %v", err)
	}
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Skip if vec_version() is not available (extension not auto-loaded).
	if _, err := db.ExecContext(context.Background(), "SELECT vec_version()"); err != nil {
		t.Skipf("sqlite-vec not loaded in DB: %v", err)
	}
	return db
}

// det384 returns a deterministic 384-dim unit vector for the given text.
func det384(text string) []float32 {
	return testhelpers.NewFakeEmbedder(384).Embed(text)
}

func TestSQLiteVecIndexUpsertSearchDelete(t *testing.T) {
	db := openVecTestDB(t)
	ctx := context.Background()

	idx, err := memory.NewSQLiteVecIndex(db)
	if err != nil {
		t.Fatalf("NewSQLiteVecIndex: %v", err)
	}

	vec := det384("hello world search test")

	if err := idx.Upsert(ctx, 42, vec, "alpha"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// INSERT OR REPLACE: upsert same id again should not error.
	if err := idx.Upsert(ctx, 42, vec, "alpha"); err != nil {
		t.Fatalf("Upsert re-insert: %v", err)
	}

	results, err := idx.Search(ctx, vec, "alpha", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].MemoryID != 42 {
		t.Errorf("expected MemoryID=42, got %d", results[0].MemoryID)
	}
	if results[0].Score < 0.99 {
		t.Errorf("expected Score>0.99, got %f", results[0].Score)
	}

	if err := idx.Delete(ctx, 42, "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	results, err = idx.Search(ctx, vec, "alpha", 5)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after delete, got %d", len(results))
	}
}

func TestPerProjectPartitionIsolation(t *testing.T) {
	db := openVecTestDB(t)
	ctx := context.Background()

	idx, err := memory.NewSQLiteVecIndex(db)
	if err != nil {
		t.Fatalf("NewSQLiteVecIndex: %v", err)
	}

	vec := det384("isolation test vector")
	if err := idx.Upsert(ctx, 7, vec, "alpha"); err != nil {
		t.Fatalf("Upsert into alpha: %v", err)
	}

	// Search in "beta" — must find nothing.
	results, err := idx.Search(ctx, vec, "beta", 5)
	if err != nil {
		t.Fatalf("Search beta: %v", err)
	}
	for _, r := range results {
		if r.MemoryID == 7 {
			t.Errorf("id=7 from project 'alpha' leaked into project 'beta'")
		}
	}
}

func TestVecTableCreatedLazilyPerProject(t *testing.T) {
	db := openVecTestDB(t)
	ctx := context.Background()

	idx, err := memory.NewSQLiteVecIndex(db)
	if err != nil {
		t.Fatalf("NewSQLiteVecIndex: %v", err)
	}

	// Table must not exist before first Upsert.
	var count int
	err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='vec_memories_lazy'",
	).Scan(&count)
	if err != nil {
		t.Fatalf("check table before upsert: %v", err)
	}
	if count != 0 {
		t.Error("table should not exist before first Upsert")
	}

	vec := det384("lazy table creation")
	if err := idx.Upsert(ctx, 1, vec, "lazy"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Table must exist after first Upsert.
	err = db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='vec_memories_lazy'",
	).Scan(&count)
	if err != nil {
		t.Fatalf("check table after upsert: %v", err)
	}
	if count == 0 {
		t.Error("table should exist after first Upsert")
	}
}

func TestSQLiteVecIndex_InvalidProject(t *testing.T) {
	db := openVecTestDB(t)
	ctx := context.Background()

	idx, err := memory.NewSQLiteVecIndex(db)
	if err != nil {
		t.Fatalf("NewSQLiteVecIndex: %v", err)
	}

	vec := det384("invalid project test")
	for _, proj := range []string{"bad;name", "has space", "dot.name", "slash/name"} {
		if err := idx.Upsert(ctx, 1, vec, proj); !errors.Is(err, memory.ErrInvalidProject) {
			t.Errorf("Upsert(%q): expected ErrInvalidProject, got %v", proj, err)
		}
	}
}

func TestSQLiteVecIndex_EmptyProjectDefaultsToOro(t *testing.T) {
	db := openVecTestDB(t)
	ctx := context.Background()

	idx, err := memory.NewSQLiteVecIndex(db)
	if err != nil {
		t.Fatalf("NewSQLiteVecIndex: %v", err)
	}

	vec := det384("empty project defaults")
	if err := idx.Upsert(ctx, 10, vec, ""); err != nil {
		t.Fatalf("Upsert empty project: %v", err)
	}

	// Must appear under "oro" project.
	results, err := idx.Search(ctx, vec, "oro", 5)
	if err != nil {
		t.Fatalf("Search oro: %v", err)
	}
	found := false
	for _, r := range results {
		if r.MemoryID == 10 {
			found = true
		}
	}
	if !found {
		t.Error("id=10 inserted with empty project not found when searching 'oro'")
	}
}

func TestInMemoryVecIndex_UpsertSearchDelete(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()

	vec := []float32{1.0, 0.0, 0.0}
	if err := idx.Upsert(42, vec, "proj"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := idx.Search(vec, "proj", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].MemoryID != 42 {
		t.Errorf("expected MemoryID=42, got %d", results[0].MemoryID)
	}
	if results[0].Score < 0.99 {
		t.Errorf("expected Score>0.99, got %f", results[0].Score)
	}

	if err := idx.Delete(42); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	results, err = idx.Search(vec, "proj", 5)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after delete, got %d", len(results))
	}
}

func TestInMemoryVecIndex_ProjectIsolation(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()

	vec := []float32{1.0, 0.0, 0.0}
	if err := idx.Upsert(7, vec, "a"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := idx.Search(vec, "b", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		if r.MemoryID == 7 {
			t.Errorf("id=7 from project 'a' leaked into project 'b' results")
		}
	}
}

func TestInMemoryVecIndex_NilVecError(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()
	if err := idx.Upsert(1, nil, "proj"); err == nil {
		t.Error("expected error for nil vec")
	}
}

func TestInMemoryVecIndex_EmptyPartition(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()
	results, err := idx.Search([]float32{1.0}, "missing", 5)
	if err != nil {
		t.Fatalf("Search on empty partition: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestInMemoryVecIndex_DeleteNonExistent(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()
	if err := idx.Delete(999); err != nil {
		t.Errorf("Delete of non-existent id should be no-op, got: %v", err)
	}
}

func TestInMemoryVecIndex_KZeroReturnsEmpty(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()
	_ = idx.Upsert(1, []float32{1.0}, "proj")

	results, err := idx.Search([]float32{1.0}, "proj", 0)
	if err != nil {
		t.Fatalf("Search k=0: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty for k=0, got %d", len(results))
	}
}

func TestInMemoryVecIndex_TieBreakByMemoryID(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()
	vec := []float32{1.0, 0.0}
	_ = idx.Upsert(3, vec, "proj")
	_ = idx.Upsert(1, vec, "proj")
	_ = idx.Upsert(2, vec, "proj")

	results, err := idx.Search(vec, "proj", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].MemoryID != 1 || results[1].MemoryID != 2 || results[2].MemoryID != 3 {
		t.Errorf("tie-break order wrong: %v", results)
	}
}

func TestInMemoryVecIndex_DeleteFromAllProjects(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()
	vec := []float32{1.0, 0.0}
	_ = idx.Upsert(5, vec, "a")
	_ = idx.Upsert(5, vec, "b")

	if err := idx.Delete(5); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for _, proj := range []string{"a", "b"} {
		results, _ := idx.Search(vec, proj, 5)
		for _, r := range results {
			if r.MemoryID == 5 {
				t.Errorf("id=5 still present in project %q after Delete", proj)
			}
		}
	}
}

// TestInMemoryVecIndex_ConcurrentSearchUpsert exercises the race between
// Search iterating a partition and Upsert writing to the same inner map.
// Must be run with -race to be meaningful.
func TestInMemoryVecIndex_ConcurrentSearchUpsert(t *testing.T) {
	idx := memory.NewInMemoryVecIndex()
	vec := []float32{1.0, 0.0, 0.0}
	if err := idx.Upsert(1, vec, "proj"); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	const iters = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			if err := idx.Upsert(int64(i+2), vec, "proj"); err != nil {
				t.Errorf("Upsert: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			if _, err := idx.Search(vec, "proj", 10); err != nil {
				t.Errorf("Search: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// compile-time check: *InMemoryVecIndex must satisfy VectorIndex.
var _ memory.VectorIndex = (*memory.InMemoryVecIndex)(nil) //nolint:staticcheck
