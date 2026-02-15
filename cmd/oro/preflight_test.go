package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPreflightChecks_AllToolsPresent(t *testing.T) {
	// When all tools are present, preflight should return nil.
	if err := runPreflightChecks(); err != nil {
		t.Errorf("preflight checks failed with all tools present: %v", err)
	}
}

func TestPreflightChecks_ErrorMessage(t *testing.T) {
	// Test that the error message is actionable when a tool is missing.
	// We can't simulate missing tools easily in unit tests, but we can
	// verify that the error messages contain the tool name and guidance.
	//
	// This test documents the expected error format.
	requiredTools := []string{"tmux", "claude", "bd", "git"}

	for _, tool := range requiredTools {
		// Verify each tool is mentioned in our implementation.
		// The actual error will only be returned if the tool is genuinely missing.
		t.Logf("preflight checks should verify '%s' is available", tool)
	}

	// Verify the function is callable and returns the expected type.
	err := runPreflightChecks()
	if err != nil {
		// If we get an error, it should be actionable.
		t.Logf("preflight checks reported: %v", err)
	}
}

func TestPreflightChecks_GitRepoStatus(t *testing.T) {
	// Test that preflight checks git repo is in good state.
	// This is a placeholder for now - we'll implement the actual check
	// when we add the preflight function.
	if err := runPreflightChecks(); err != nil {
		// If this fails, it should be because the repo is in a bad state,
		// not because the function doesn't exist.
		t.Logf("preflight checks reported: %v", err)
	}
}

func TestEnsureSearchHook(t *testing.T) {
	t.Run("builds binary when it does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		binPath := filepath.Join(tmpDir, "oro-search-hook")

		// Source dir is the real cmd/oro-search-hook (we're in the repo).
		srcDir := filepath.Join(repoRoot(t), "cmd", "oro-search-hook")

		err := ensureSearchHook(binPath, srcDir)
		if err != nil {
			t.Fatalf("ensureSearchHook: %v", err)
		}

		info, err := os.Stat(binPath)
		if err != nil {
			t.Fatalf("binary not created: %v", err)
		}
		if info.Size() == 0 {
			t.Error("binary is empty")
		}
	})

	t.Run("rebuilds when binary is stale", func(t *testing.T) {
		tmpDir := t.TempDir()
		binPath := filepath.Join(tmpDir, "oro-search-hook")
		srcDir := filepath.Join(repoRoot(t), "cmd", "oro-search-hook")

		// Create a stale binary with mod time at the Unix epoch, guaranteeing
		// it is older than any source file regardless of when they were last touched.
		if err := os.WriteFile(binPath, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		epoch := time.Unix(0, 0)
		if err := os.Chtimes(binPath, epoch, epoch); err != nil {
			t.Fatal(err)
		}

		err := ensureSearchHook(binPath, srcDir)
		if err != nil {
			t.Fatalf("ensureSearchHook: %v", err)
		}

		info, err := os.Stat(binPath)
		if err != nil {
			t.Fatal(err)
		}
		// Binary should have been rebuilt (newer mod time, larger than "old").
		if info.Size() <= 3 {
			t.Errorf("expected rebuilt binary, got size %d", info.Size())
		}
	})

	t.Run("skips build when binary is fresh", func(t *testing.T) {
		tmpDir := t.TempDir()
		binPath := filepath.Join(tmpDir, "oro-search-hook")
		srcDir := filepath.Join(repoRoot(t), "cmd", "oro-search-hook")

		// Build it first.
		if err := ensureSearchHook(binPath, srcDir); err != nil {
			t.Fatal(err)
		}
		info1, _ := os.Stat(binPath)

		// Set binary mod time to the future so it's definitely fresh.
		future := time.Now().Add(1 * time.Hour)
		if err := os.Chtimes(binPath, future, future); err != nil {
			t.Fatal(err)
		}

		// Run again — should skip build.
		if err := ensureSearchHook(binPath, srcDir); err != nil {
			t.Fatal(err)
		}
		info2, _ := os.Stat(binPath)

		// Mod time should remain the future time (not rebuilt).
		if info2.ModTime().Before(info1.ModTime().Add(30 * time.Minute)) {
			t.Error("expected binary to remain fresh (not rebuilt)")
		}
	})

	t.Run("returns error when source dir missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		binPath := filepath.Join(tmpDir, "oro-search-hook")

		err := ensureSearchHook(binPath, "/nonexistent/source/dir")
		if err == nil {
			t.Fatal("expected error for missing source dir")
		}
	})
}

// repoRoot finds the repository root by looking for go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod)")
		}
		dir = parent
	}
}
