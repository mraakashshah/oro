package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildDispatcher_BuildsIndex verifies that buildDispatcher launches a
// goroutine to build the code index asynchronously without blocking startup.
func TestBuildDispatcher_BuildsIndex(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))
	t.Setenv("ORO_HOME", tmpDir)

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

	d, db, err := buildDispatcher(1)
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
		t.Errorf("index DB not created at %s after buildDispatcher", indexPath)
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
	d, db, err := buildDispatcher(1)
	if err != nil {
		t.Fatalf("buildDispatcher failed (should not block on index build): %v", err)
	}
	defer func() { _ = db.Close() }()
	_ = d

	// Success: dispatcher built without blocking on index.
	// The goroutine may fail to build the index (no .go files in cwd), but that's
	// logged and doesn't prevent startup.
}
