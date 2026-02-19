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
