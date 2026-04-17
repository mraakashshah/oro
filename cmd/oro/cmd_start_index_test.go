package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/dbutil"
)

// TestBuildDispatcher_BuildsIndex verifies that buildDispatcher launches a
// goroutine to build the code index asynchronously without blocking startup.
func TestBuildDispatcher_BuildsIndex(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))
	t.Setenv("ORO_HOME", tmpDir)
	// Clear ORO_PROJECT so ResolveProjectDBPaths does not route to a
	// project-scoped path whose parent directory does not exist.
	t.Setenv("ORO_PROJECT", "")

	// Create a test repo structure (minimal Go file to index).
	repoRoot := filepath.Join(tmpDir, "test-repo")
	if err := os.MkdirAll(repoRoot, 0o750); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	testFile := filepath.Join(repoRoot, "main.go")
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Change to repo root so buildDispatcher picks it up.
	origWd, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()

	d, db, err := buildDispatcher(1, 1, 0, 0, "", false, "")
	if err != nil {
		t.Fatalf("buildDispatcher: %v", err)
	}
	defer func() { _ = db.Close() }()
	_ = d

	// The index goroutine should have launched. Give it time to start.
	// We verify by checking that the index DB was created.
	indexPath := filepath.Join(tmpDir, "code_index.db")
	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(indexPath); statErr == nil {
			found = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !found {
		t.Fatalf("index DB not created at %s after buildDispatcher", indexPath)
	}

	// Verify the index contains >0 chunks (acceptance: "index contains >0 chunks").
	// The build goroutine may still be running; poll until chunks appear.
	var chunkCount int
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		idxDB, err := dbutil.OpenDB(indexPath)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		err = idxDB.QueryRow("SELECT count(*) FROM chunks").Scan(&chunkCount)
		_ = idxDB.Close()
		if err == nil && chunkCount > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if chunkCount == 0 {
		t.Errorf("index DB at %s has 0 chunks; expected >0 after buildCodeIndex", indexPath)
	}
}

// TestBuildCodeIndex_DirectCall verifies buildCodeIndex creates a DB with >0 chunks
// when given a directory containing Go source files.
func TestBuildCodeIndex_DirectCall(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "code_index.db")

	// Create a minimal Go source file.
	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "hello.go"), []byte("package hello\n\nfunc Hello() string { return \"hi\" }\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := buildCodeIndex(context.Background(), srcDir, dbPath); err != nil {
		t.Fatalf("buildCodeIndex: %v", err)
	}

	// Verify DB exists and contains chunks.
	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT count(*) FROM chunks").Scan(&count); err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	if count == 0 {
		t.Errorf("expected >0 chunks, got 0")
	}
}

// TestBuildCodeIndex_CancelledContext verifies buildCodeIndex returns nil
// and does NOT create the index DB when the context is already cancelled.
func TestBuildCodeIndex_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	dbPath := filepath.Join(t.TempDir(), "index.db")
	err := buildCodeIndex(ctx, t.TempDir(), dbPath)
	if err != nil {
		t.Errorf("expected nil on cancelled context, got: %v", err)
	}

	// Verify the DB was NOT created (early return before NewCodeIndex).
	if _, statErr := os.Stat(dbPath); statErr == nil {
		t.Errorf("index DB should not be created when context is cancelled")
	}
}

// TestBuildCodeIndex_OpenFailure verifies buildCodeIndex returns nil (never fatal)
// when the index DB cannot be opened (parent path blocked by a regular file).
func TestBuildCodeIndex_OpenFailure(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a regular file that blocks MkdirAll from creating the DB's parent dir.
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	dbPath := filepath.Join(blocker, "subdir", "index.db")

	err := buildCodeIndex(context.Background(), tmpDir, dbPath)
	if err != nil {
		t.Errorf("expected nil on open failure, got: %v", err)
	}
}

// TestBuildDispatcher_IndexBuildDoesNotBlockStartup verifies that index build
// errors are logged but never prevent the dispatcher from starting.
func TestBuildDispatcher_IndexBuildDoesNotBlockStartup(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

	// Use separate dir for index to avoid cleanup issues.
	indexDir := t.TempDir()
	t.Setenv("ORO_HOME", indexDir)

	// buildDispatcher should succeed immediately regardless of index build status.
	// The index builds asynchronously in a goroutine.
	d, db, err := buildDispatcher(1, 1, 0, 0, "", false, "")
	if err != nil {
		t.Fatalf("buildDispatcher failed (should not block on index build): %v", err)
	}
	defer func() { _ = db.Close() }()
	_ = d

	// Success: dispatcher built without blocking on index.
	// The goroutine may fail to build the index (no .go files in cwd), but that's
	// logged and doesn't prevent startup.
}
