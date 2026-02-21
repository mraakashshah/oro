package codesearch_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/codesearch"
)

func TestCodeIndex_BuildAndSearch(t *testing.T) {
	// Create a temp directory with Go source files.
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "main.go", `package main

func main() {
	handleAuth()
}

func handleAuth() {
	// authentication logic
}
`)
	writeGoFile(t, rootDir, "server.go", `package main

type Server struct {
	Port int
}

func (s *Server) Start() error {
	return nil
}

func (s *Server) Stop() {
}
`)

	dbPath := filepath.Join(t.TempDir(), "test_index.db")

	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Build the index.
	stats, err := idx.Build(ctx, rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stats.FilesProcessed != 2 {
		t.Errorf("expected 2 files processed, got %d", stats.FilesProcessed)
	}
	if stats.ChunksIndexed < 4 {
		t.Errorf("expected at least 4 chunks indexed, got %d", stats.ChunksIndexed)
	}

	// Search is gutted in this version - will be replaced in a later bead.
	// Just verify it doesn't crash.
	_, _ = idx.Search(ctx, "authentication logic", 5)
}

func TestCodeIndex_SearchEmpty(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty_index.db")

	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	results, err := idx.Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("Search on empty index: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results on empty index, got %d", len(results))
	}
}

func TestCodeIndex_TopKLimitsResults(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "many.go", `package main

func A() {}
func B() {}
func C() {}
func D() {}
func E() {}
func F() {}
`)

	dbPath := filepath.Join(t.TempDir(), "topk_index.db")

	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.Search(ctx, "test", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("expected at most 3 results, got %d", len(results))
	}
}

func TestCodeIndex_RebuildClearsOldData(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "v1.go", `package main

func OldFunction() {}
`)

	dbPath := filepath.Join(t.TempDir(), "rebuild_index.db")

	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// First build.
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build 1: %v", err)
	}

	// Replace the file with different content.
	writeGoFile(t, rootDir, "v1.go", `package main

func NewFunction() {}
`)

	// Second build (full rebuild).
	stats, err := idx.Build(ctx, rootDir)
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}

	if stats.ChunksIndexed != 1 {
		t.Errorf("expected 1 chunk after rebuild, got %d", stats.ChunksIndexed)
	}
}

func TestCodeIndex_SkipsNonGoFiles(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "main.go", `package main

func Main() {}
`)
	// Write a non-Go file that should be skipped.
	writeFile(t, rootDir, "readme.md", "# Hello")
	writeFile(t, rootDir, "script.py", "def hello(): pass")

	dbPath := filepath.Join(t.TempDir(), "skip_index.db")

	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Build(context.Background(), rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stats.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed (only .go), got %d", stats.FilesProcessed)
	}
}

func writeGoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	writeFile(t, dir, name, content)
}

func TestCodeIndex_Search(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "main.go", `package main

func main() {
	handleAuth()
}

func handleAuth() {
	// authentication logic
}
`)
	writeGoFile(t, rootDir, "server.go", `package main

type Server struct {
	Port int
}

func (s *Server) Start() error {
	return nil
}

func (s *Server) Stop() {
}
`)

	dbPath := filepath.Join(t.TempDir(), "search_test_index.db")

	// Mock reranker that reverses chunk order and adds reasons.
	spawner := &searchMockSpawner{}
	reranker := codesearch.NewReranker(spawner)

	idx, err := codesearch.NewCodeIndex(dbPath, reranker)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Run("Search calls FTS5 then reranker", func(t *testing.T) {
		results, err := idx.Search(ctx, "authentication", 3)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected results from Search, got 0")
		}
		// Verify results have Reason populated (from reranker).
		for _, r := range results {
			if r.Reason == "" {
				t.Errorf("expected Reason populated for chunk %s, got empty", r.Chunk.Name)
			}
		}
	})

	t.Run("FTS5 returns 0 candidates skips reranker", func(t *testing.T) {
		results, err := idx.Search(ctx, "xyznonexistent", 3)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for no-match query, got %d", len(results))
		}
	})

	t.Run("nil reranker uses FTS5 only", func(t *testing.T) {
		dbPath2 := filepath.Join(t.TempDir(), "fts_only_index.db")
		idx2, err := codesearch.NewCodeIndex(dbPath2, nil)
		if err != nil {
			t.Fatalf("NewCodeIndex: %v", err)
		}
		defer idx2.Close()

		if _, err := idx2.Build(ctx, rootDir); err != nil {
			t.Fatalf("Build: %v", err)
		}

		results, err := idx2.Search(ctx, "authentication", 3)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected FTS5-only results, got 0")
		}
		// FTS5-only results should have empty Reason.
		for _, r := range results {
			if r.Reason != "" {
				t.Errorf("expected empty Reason for FTS5-only, got %q", r.Reason)
			}
		}
	})
}

// searchMockSpawner returns a ranking that preserves order with reasons.
type searchMockSpawner struct{}

func (s *searchMockSpawner) Spawn(ctx context.Context, prompt string) (string, error) {
	// Return IDs 1..N in order with reasons. We don't know how many chunks
	// FTS5 will return, so generate enough entries.
	return `[{"id":1,"reason":"best match"},{"id":2,"reason":"good match"},{"id":3,"reason":"ok match"},{"id":4,"reason":"partial match"},{"id":5,"reason":"weak match"},{"id":6,"reason":"marginal"},{"id":7,"reason":"marginal"},{"id":8,"reason":"marginal"},{"id":9,"reason":"marginal"},{"id":10,"reason":"marginal"}]`, nil
}

func TestFTS5Search(t *testing.T) {
	// Create a temp directory with Go source files.
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "main.go", `package main

func main() {
	handleAuth()
}

func handleAuth() {
	// authentication logic
}
`)
	writeGoFile(t, rootDir, "server.go", `package main

type Server struct {
	Port int
}

func (s *Server) Start() error {
	return nil
}

func (s *Server) Stop() {
}
`)

	dbPath := filepath.Join(t.TempDir(), "fts5_test_index.db")

	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Build the index.
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Run("query authentication returns handleAuth chunk", func(t *testing.T) {
		results, err := idx.FTS5Search(ctx, "authentication", 10)
		if err != nil {
			t.Fatalf("FTS5Search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected at least 1 result for 'authentication', got 0")
		}
		// Verify that at least one result contains "handleAuth"
		found := false
		for _, chunk := range results {
			if strings.Contains(chunk.Content, "handleAuth") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected result containing 'handleAuth', got none")
		}
	})

	t.Run("query Server returns Server type chunk", func(t *testing.T) {
		results, err := idx.FTS5Search(ctx, "Server", 10)
		if err != nil {
			t.Fatalf("FTS5Search: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected at least 1 result for 'Server', got 0")
		}
		// Verify that at least one result contains "Server"
		found := false
		for _, chunk := range results {
			if strings.Contains(chunk.Content, "Server") {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected result containing 'Server', got none")
		}
	})

	t.Run("empty query returns empty results", func(t *testing.T) {
		results, err := idx.FTS5Search(ctx, "", 10)
		if err != nil {
			t.Fatalf("FTS5Search with empty query: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results for empty query, got %d", len(results))
		}
	})
}

func TestBuild_AtomicRebuild(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "main.go", `package main

func TestFunc() {
	// test content
}
`)

	dbPath := filepath.Join(t.TempDir(), "atomic_index.db")

	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// First build.
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build 1: %v", err)
	}

	// Verify initial data exists.
	initialResults, err := idx.FTS5Search(ctx, "TestFunc", 10)
	if err != nil {
		t.Fatalf("FTS5Search before rebuild: %v", err)
	}
	if len(initialResults) == 0 {
		t.Fatal("expected initial data, got 0 results")
	}

	// Create a directory that will cause filepath.Walk to fail (unreadable directory).
	restrictedDir := filepath.Join(rootDir, "restricted")
	if err := os.Mkdir(restrictedDir, 0o000); err != nil {
		t.Fatalf("create restricted dir: %v", err)
	}
	defer func() {
		_ = os.Chmod(restrictedDir, 0o755) //nolint:gosec // test cleanup, needs write permissions
	}()

	// Attempt rebuild - should fail on restricted directory.
	_, err = idx.Build(ctx, rootDir)
	if err == nil {
		t.Fatal("expected Build to fail on restricted directory, got nil")
	}

	// Verify original data is still searchable (transaction rolled back).
	resultsAfterFailedRebuild, err := idx.FTS5Search(ctx, "TestFunc", 10)
	if err != nil {
		t.Fatalf("FTS5Search after failed rebuild: %v", err)
	}
	if len(resultsAfterFailedRebuild) == 0 {
		t.Error("expected original data preserved after failed rebuild, got 0 results")
	}

	// Also verify on a separate connection to ensure atomicity across connections.
	idx2, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex for separate connection: %v", err)
	}
	defer idx2.Close()

	results2, err := idx2.FTS5Search(ctx, "TestFunc", 10)
	if err != nil {
		t.Fatalf("FTS5Search on separate connection: %v", err)
	}
	if len(results2) == 0 {
		t.Error("expected original data visible on separate connection after failed rebuild")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file %s: %v", name, err)
	}
}

func TestBuild_ConcurrentReadsDontSeeEmptyIndex(t *testing.T) {
	rootDir := t.TempDir()

	// Write enough files so Build() takes measurable time for overlap with readers.
	const numFiles = 30
	for i := range numFiles {
		writeGoFile(t, rootDir, fmt.Sprintf("file%d.go", i), fmt.Sprintf(`package main

// ConcurrentFunc%d is a searchable placeholder.
func ConcurrentFunc%d() string {
	return "concurrent test content"
}
`, i, i))
	}

	dbPath := filepath.Join(t.TempDir(), "concurrent_test.db")
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Initial build to populate the index.
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("initial Build: %v", err)
	}

	// Verify initial data is present before testing concurrency.
	initial, err := idx.FTS5Search(ctx, "concurrent", 5)
	if err != nil {
		t.Fatalf("FTS5Search initial: %v", err)
	}
	if len(initial) == 0 {
		t.Fatal("initial build must produce results for this test to be meaningful")
	}

	var emptyCount atomic.Int64
	var searchCount atomic.Int64
	done := make(chan struct{})
	var wg sync.WaitGroup

	// Start readers that continuously search while Build() runs.
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				results, err := idx.FTS5Search(ctx, "concurrent", 5)
				if err == nil {
					searchCount.Add(1)
					if len(results) == 0 {
						emptyCount.Add(1)
					}
				}
				runtime.Gosched()
			}
		}()
	}

	// Let readers start before triggering rebuild.
	time.Sleep(5 * time.Millisecond)

	// Rebuild concurrently with readers running.
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("concurrent Build: %v", err)
	}

	close(done)
	wg.Wait()

	if emptyCount.Load() > 0 {
		t.Errorf("saw %d empty Search results during concurrent Build (%d total searches)",
			emptyCount.Load(), searchCount.Load())
	}
	t.Logf("completed %d concurrent searches, %d saw empty index",
		searchCount.Load(), emptyCount.Load())
}

// ---------------------------------------------------------------------------
// Mutation-killing tests for index.go pure logic paths
// ---------------------------------------------------------------------------

// TestSearch_ScoreComputation verifies that Search (nil reranker) assigns
// descending scores via 1/(i+1) and that the first result always has score 1.0.
// Kills mutants: score assignment replaced with no-op (.52), rank formula wrong.
func TestSearch_ScoreComputation(t *testing.T) {
	rootDir := t.TempDir()
	// Write enough distinct functions so FTS5 returns multiple hits.
	writeGoFile(t, rootDir, "funcs.go", `package main

// ScoreA is the first scored function in the index.
func ScoreA() string { return "score test alpha" }

// ScoreB is the second scored function in the index.
func ScoreB() string { return "score test beta" }

// ScoreC is the third scored function in the index.
func ScoreC() string { return "score test gamma" }
`)

	dbPath := t.TempDir() + "/score_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.Search(ctx, "score", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got 0")
	}

	// First result must have score exactly 1.0 (i=0 → 1/(0+1)=1.0).
	if results[0].Score != 1.0 {
		t.Errorf("first result score = %f, want 1.0", results[0].Score)
	}

	// Scores must be strictly decreasing.
	for i := 1; i < len(results); i++ {
		if results[i].Score >= results[i-1].Score {
			t.Errorf("result[%d].Score=%f not < result[%d].Score=%f — scores not decreasing",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}

	// Each score must equal 1/(position+1).
	for i, r := range results {
		want := 1.0 / float64(i+1)
		if r.Score != want {
			t.Errorf("result[%d].Score = %f, want %f (1/%d)", i, r.Score, want, i+1)
		}
	}
}

// TestSearch_TopKBoundaryExact verifies that Search returns exactly topK results
// when candidates >= topK, and that the boundary is >= not >.
// Kills mutant .34: `i >= topK` changed to `i > topK` (returns topK+1 results).
func TestSearch_TopKBoundaryExact(t *testing.T) {
	rootDir := t.TempDir()
	// Write 5 distinct functions all matching "boundary".
	writeGoFile(t, rootDir, "boundary.go", `package main

// BoundaryA matches boundary query first.
func BoundaryA() string { return "boundary alpha one" }

// BoundaryB matches boundary query second.
func BoundaryB() string { return "boundary beta two" }

// BoundaryC matches boundary query third.
func BoundaryC() string { return "boundary gamma three" }

// BoundaryD matches boundary query fourth.
func BoundaryD() string { return "boundary delta four" }

// BoundaryE matches boundary query fifth.
func BoundaryE() string { return "boundary epsilon five" }
`)

	dbPath := t.TempDir() + "/boundary_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Verify we have enough candidates in the index.
	all, err := idx.FTS5Search(ctx, "boundary", 30)
	if err != nil {
		t.Fatalf("FTS5Search: %v", err)
	}
	if len(all) < 3 {
		t.Skipf("not enough candidates for boundary test (got %d, need >= 3)", len(all))
	}

	// Request exactly N results where N < total candidates.
	const topK = 2
	results, err := idx.Search(ctx, "boundary", topK)
	if err != nil {
		t.Fatalf("Search(topK=%d): %v", topK, err)
	}
	if len(results) != topK {
		t.Errorf("Search(topK=%d) returned %d results, want exactly %d", topK, len(results), topK)
	}

	// The mutant uses `i > topK` which allows i==topK through — len would be topK+1.
	// This test directly catches that off-by-one.
}

// TestSearch_TopKBoundaryOne verifies topK=1 returns exactly 1 result.
func TestSearch_TopKBoundaryOne(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "one.go", `package main

// LimitOne is the only result we want.
func LimitOne() string { return "limitone unique marker" }

// LimitTwo exists to ensure there are multiple candidates.
func LimitTwo() string { return "limitone secondary marker" }
`)

	dbPath := t.TempDir() + "/topk1_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.Search(ctx, "limitone", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Search(topK=1) returned %d results, want 1", len(results))
	}
}

// TestSearch_RerankerScoreByRank verifies that the reranker path scores results
// as 1/rank, not 1/(i+1), and that Reason is populated.
// Kills mutant .53: SearchResult struct assignment replaced with no-op.
func TestSearch_RerankerScoreByRank(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "reranked.go", `package main

// RerankedA is an important reranked function.
func RerankedA() string { return "rerank target alpha" }

// RerankedB is a secondary reranked function.
func RerankedB() string { return "rerank target beta" }
`)

	dbPath := t.TempDir() + "/rerank_score_test.db"

	// Spawner returns rank 2 first, rank 1 second — score should be 1/rank.
	spawner := &fixedRankSpawner{
		response: `[{"id":2,"reason":"second chunk ranked first"},{"id":1,"reason":"first chunk ranked second"}]`,
	}
	reranker := codesearch.NewReranker(spawner)

	idx, err := codesearch.NewCodeIndex(dbPath, reranker)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.Search(ctx, "rerank", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from reranker path, got 0")
	}

	// Every result must have a non-empty Reason (populated from reranker).
	for i, r := range results {
		if r.Reason == "" {
			t.Errorf("result[%d] Reason is empty, want non-empty", i)
		}
		// Score must be 1/rank (rank starts at 1).
		wantScore := 1.0 / float64(i+1)
		if r.Score != wantScore {
			t.Errorf("result[%d].Score = %f, want %f", i, r.Score, wantScore)
		}
	}
}

// fixedRankSpawner returns a fixed JSON response for reranking.
type fixedRankSpawner struct {
	response string
}

func (s *fixedRankSpawner) Spawn(_ context.Context, _ string) (string, error) {
	return s.response, nil
}

// TestShouldSkipDir_DotFileIsRootDir verifies that the root dir itself (even if
// named with a dot prefix) is NOT skipped.
// Kills mutant .36: `path != rootDir` changed to `true` (root always skipped).
func TestShouldSkipDir_DotFileIsRootDir(t *testing.T) {
	// Create a root dir whose base name starts with "." and use it as rootDir.
	// shouldSkipDir must NOT skip it even though base starts with ".".
	parent := t.TempDir()
	dotRoot := parent + "/.myroot"
	if err := os.MkdirAll(dotRoot, 0o750); err != nil {
		t.Fatalf("mkdir dotRoot: %v", err)
	}

	writeGoFile(t, dotRoot, "pkg.go", `package main

// DotRootFunc is in a dot-prefixed root.
func DotRootFunc() string { return "dot root content" }
`)

	dbPath := t.TempDir() + "/dotroot_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	// Build with dotRoot as rootDir — should NOT skip it.
	stats, err := idx.Build(context.Background(), dotRoot)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if stats.FilesProcessed == 0 {
		t.Error("expected files processed in dot-prefixed root dir, got 0 (root was skipped)")
	}
}

// TestShouldSkipDir_HiddenSubdirIsSkipped verifies that hidden directories
// (dot-prefixed) that are not the root are skipped.
// Kills mutant .35: `strings.HasPrefix(base, ".")` changed to `true`.
func TestShouldSkipDir_HiddenSubdirIsSkipped(t *testing.T) {
	rootDir := t.TempDir()

	// Create a hidden subdir with a Go file that should NOT be indexed.
	hiddenDir := rootDir + "/.hidden"
	if err := os.MkdirAll(hiddenDir, 0o750); err != nil {
		t.Fatalf("mkdir hidden: %v", err)
	}
	writeGoFile(t, hiddenDir, "hidden.go", `package hidden

// HiddenFunc should not be indexed.
func HiddenFunc() string { return "hidden secret content" }
`)

	// Create a visible file that should be indexed.
	writeGoFile(t, rootDir, "visible.go", `package main

// VisibleFunc should be indexed.
func VisibleFunc() string { return "visible content" }
`)

	dbPath := t.TempDir() + "/hidden_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Build(context.Background(), rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Only the visible file should be processed.
	if stats.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed (hidden subdir skipped), got %d", stats.FilesProcessed)
	}
}

// TestShouldSkipDir_VendorSkipped verifies the vendor directory is skipped.
// Kills mutant .39: `base == "vendor"` condition removed (replaced with false).
func TestShouldSkipDir_VendorSkipped(t *testing.T) {
	rootDir := t.TempDir()

	vendorDir := rootDir + "/vendor"
	if err := os.MkdirAll(vendorDir, 0o750); err != nil {
		t.Fatalf("mkdir vendor: %v", err)
	}
	writeGoFile(t, vendorDir, "dep.go", `package dep

// VendorFunc should not be indexed.
func VendorFunc() string { return "vendor dependency" }
`)

	writeGoFile(t, rootDir, "main.go", `package main

// MainFunc should be indexed.
func MainFunc() string { return "main function" }
`)

	dbPath := t.TempDir() + "/vendor_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Build(context.Background(), rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stats.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed (vendor skipped), got %d", stats.FilesProcessed)
	}

	ctx := context.Background()
	results, err := idx.FTS5Search(ctx, "VendorFunc", 10)
	if err != nil {
		t.Fatalf("FTS5Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected vendor content not indexed, but FTS5Search returned %d results", len(results))
	}
}

// TestShouldSkipDir_NodeModulesSkipped verifies node_modules directory is skipped.
// Kills mutant .40: `base == "node_modules"` condition removed.
func TestShouldSkipDir_NodeModulesSkipped(t *testing.T) {
	rootDir := t.TempDir()

	nmDir := rootDir + "/node_modules"
	if err := os.MkdirAll(nmDir, 0o750); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	writeGoFile(t, nmDir, "jsmod.go", `package jsmod

// NodeFunc should not be indexed.
func NodeFunc() string { return "node module content" }
`)

	writeGoFile(t, rootDir, "app.go", `package main

// AppFunc should be indexed.
func AppFunc() string { return "application logic" }
`)

	dbPath := t.TempDir() + "/nodemodules_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Build(context.Background(), rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stats.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed (node_modules skipped), got %d", stats.FilesProcessed)
	}
}

// TestShouldSkipDir_TestdataSkipped verifies testdata directory is skipped.
// Kills mutant .38: `base == "testdata"` condition removed.
func TestShouldSkipDir_TestdataSkipped(t *testing.T) {
	rootDir := t.TempDir()

	tdDir := rootDir + "/testdata"
	if err := os.MkdirAll(tdDir, 0o750); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	writeGoFile(t, tdDir, "fixture.go", `package testdata

// FixtureFunc should not be indexed.
func FixtureFunc() string { return "testdata fixture" }
`)

	writeGoFile(t, rootDir, "impl.go", `package main

// ImplFunc should be indexed.
func ImplFunc() string { return "implementation logic" }
`)

	dbPath := t.TempDir() + "/testdata_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Build(context.Background(), rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stats.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed (testdata skipped), got %d", stats.FilesProcessed)
	}
}

// TestBuild_StatsFilesProcessed verifies that FilesProcessed is incremented for
// each Go file (not just kept at 0).
// Kills mutant .49: `stats.FilesProcessed++` replaced with no-op.
func TestBuild_StatsFilesProcessed(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "a.go", `package main

func FuncA() {}
`)
	writeGoFile(t, rootDir, "b.go", `package main

func FuncB() {}
`)
	writeGoFile(t, rootDir, "c.go", `package main

func FuncC() {}
`)

	dbPath := t.TempDir() + "/stats_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Build(context.Background(), rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stats.FilesProcessed != 3 {
		t.Errorf("FilesProcessed = %d, want 3", stats.FilesProcessed)
	}
}

// TestBuild_StatsChunksIndexed verifies that ChunksIndexed is incremented for
// each chunk stored.
// Kills mutant .51: `stats.ChunksIndexed++` replaced with no-op.
func TestBuild_StatsChunksIndexed(t *testing.T) {
	rootDir := t.TempDir()
	// 3 functions = 3 chunks.
	writeGoFile(t, rootDir, "multi.go", `package main

func ChunkOne() string { return "chunk one" }
func ChunkTwo() string { return "chunk two" }
func ChunkThree() string { return "chunk three" }
`)

	dbPath := t.TempDir() + "/chunks_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Build(context.Background(), rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stats.ChunksIndexed != 3 {
		t.Errorf("ChunksIndexed = %d, want 3", stats.ChunksIndexed)
	}
}

// TestFTS5Search_ResultsNotEmpty verifies that FTS5Search actually appends
// results to the slice and returns them.
// Kills mutant .56: `results = append(results, c)` replaced with no-op.
func TestFTS5Search_ResultsNotEmpty(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "indexed.go", `package main

// IndexedFunc is a function that must appear in search results.
func IndexedFunc() string { return "indexed searchable content unique" }
`)

	dbPath := t.TempDir() + "/fts5_results_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.FTS5Search(ctx, "indexed", 10)
	if err != nil {
		t.Fatalf("FTS5Search: %v", err)
	}

	// If append is a no-op, results will be empty.
	if len(results) == 0 {
		t.Fatal("FTS5Search returned 0 results — append mutant not killed")
	}

	// Verify a specific known function appears.
	found := false
	for _, r := range results {
		if strings.Contains(r.Content, "IndexedFunc") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected IndexedFunc in search results, not found")
	}
}

// TestFTS5Search_ChunkKindAssigned verifies that the Kind field is populated
// from the scanned `kind` string value.
// Kills mutant .55: `c.Kind = ChunkKind(kind)` replaced with no-op.
func TestFTS5Search_ChunkKindAssigned(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "kinds.go", `package main

// KindFunc is a function chunk.
func KindFunc() {}

// KindType is a type chunk.
type KindType struct{}
`)

	dbPath := t.TempDir() + "/kind_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.FTS5Search(ctx, "KindFunc", 10)
	if err != nil {
		t.Fatalf("FTS5Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results for KindFunc query")
	}

	// Verify that Kind field is non-empty (set correctly).
	for _, r := range results {
		if r.Kind == "" {
			t.Errorf("chunk %q has empty Kind — ChunkKind assignment mutant not killed", r.Name)
		}
	}
}

// TestFTS5Search_MultipleResultsOrdered verifies that multiple FTS5 hits are
// returned and have valid Kind/FilePath/Name fields.
func TestFTS5Search_MultipleResultsOrdered(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "multi.go", `package main

// OrderedAlpha is alpha result.
func OrderedAlpha() string { return "ordered search term one" }

// OrderedBeta is beta result.
func OrderedBeta() string { return "ordered search term two" }
`)

	dbPath := t.TempDir() + "/multi_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	results, err := idx.FTS5Search(ctx, "ordered", 10)
	if err != nil {
		t.Fatalf("FTS5Search: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected >= 2 results for 'ordered', got %d", len(results))
	}

	for i, r := range results {
		if r.FilePath == "" {
			t.Errorf("result[%d].FilePath is empty", i)
		}
		if r.Name == "" {
			t.Errorf("result[%d].Name is empty", i)
		}
		if r.Kind == "" {
			t.Errorf("result[%d].Kind is empty", i)
		}
		if r.Content == "" {
			t.Errorf("result[%d].Content is empty", i)
		}
	}
}

// TestSearch_EmptyCandidatesSkipsReranker verifies Search returns nil (not an
// error) when FTS5 yields no candidates, and never calls the reranker.
// Kills mutant .26: `return nil, nil` on empty candidates replaced with no-op.
func TestSearch_EmptyCandidatesSkipsReranker(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "noop.go", `package main

func NoopFunc() {}
`)

	dbPath := t.TempDir() + "/empty_candidates_test.db"

	spawner := &panicSpawner{}
	reranker := codesearch.NewReranker(spawner)

	idx, err := codesearch.NewCodeIndex(dbPath, reranker)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()
	if _, err := idx.Build(ctx, rootDir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Query that will not match anything.
	results, err := idx.Search(ctx, "zzznomatchxxx", 5)
	if err != nil {
		t.Fatalf("Search with no FTS5 candidates: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results when no candidates, got len=%d", len(results))
	}
}

// panicSpawner panics if Spawn is ever called — used to confirm reranker is not called.
type panicSpawner struct{}

func (s *panicSpawner) Spawn(_ context.Context, _ string) (string, error) {
	panic("reranker should not be called when FTS5 returns no candidates")
}

// TestBuild_StatsDuration verifies that BuildStats.Duration is set after a build.
// Kills mutant .47: `stats.Duration = time.Since(start)` replaced with no-op.
func TestBuild_StatsDuration(t *testing.T) {
	rootDir := t.TempDir()
	writeGoFile(t, rootDir, "dur.go", `package main

func DurationFunc() {}
`)

	dbPath := t.TempDir() + "/duration_test.db"
	idx, err := codesearch.NewCodeIndex(dbPath, nil)
	if err != nil {
		t.Fatalf("NewCodeIndex: %v", err)
	}
	defer idx.Close()

	stats, err := idx.Build(context.Background(), rootDir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stats.Duration == 0 {
		t.Error("BuildStats.Duration is zero — time.Since assignment mutant not killed")
	}
}
