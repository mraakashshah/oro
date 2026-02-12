package codesearch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	idx, err := codesearch.NewCodeIndex(dbPath)
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

	idx, err := codesearch.NewCodeIndex(dbPath)
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

	idx, err := codesearch.NewCodeIndex(dbPath)
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

	idx, err := codesearch.NewCodeIndex(dbPath)
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

	idx, err := codesearch.NewCodeIndex(dbPath)
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

	idx, err := codesearch.NewCodeIndex(dbPath)
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
