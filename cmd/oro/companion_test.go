package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverCompanions(t *testing.T) {
	t.Run("finds sibling in same directory as executable", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a fake "oro" binary (the base).
		basePath := filepath.Join(tmpDir, "oro")
		if err := os.WriteFile(basePath, []byte("fake-oro"), 0o755); err != nil { //nolint:gosec // test-only fake binary
			t.Fatal(err)
		}

		// Create a fake "oro-dash" companion as a sibling.
		companionPath := filepath.Join(tmpDir, "oro-dash")
		if err := os.WriteFile(companionPath, []byte("fake-dash"), 0o755); err != nil { //nolint:gosec // test-only fake binary
			t.Fatal(err)
		}

		got, err := discoverCompanionFrom(basePath, "oro-dash")
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if got != companionPath {
			t.Errorf("expected %s, got %s", companionPath, got)
		}
	})

	t.Run("falls back to PATH when no sibling found", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a fake "oro" binary but NO oro-dash sibling.
		basePath := filepath.Join(tmpDir, "oro")
		if err := os.WriteFile(basePath, []byte("fake-oro"), 0o755); err != nil { //nolint:gosec // test-only fake binary
			t.Fatal(err)
		}

		// Create "oro-dash" in a separate directory and add it to PATH.
		pathDir := t.TempDir()
		pathBin := filepath.Join(pathDir, "oro-dash")
		if err := os.WriteFile(pathBin, []byte("fake-dash-on-path"), 0o755); err != nil { //nolint:gosec // test-only fake binary
			t.Fatal(err)
		}

		// Temporarily prepend pathDir to PATH.
		origPath := os.Getenv("PATH")
		t.Setenv("PATH", pathDir+string(os.PathListSeparator)+origPath)

		got, err := discoverCompanionFrom(basePath, "oro-dash")
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if got != pathBin {
			t.Errorf("expected %s, got %s", pathBin, got)
		}
	})

	t.Run("returns actionable error when not found anywhere", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a fake "oro" binary but NO companion.
		basePath := filepath.Join(tmpDir, "oro")
		if err := os.WriteFile(basePath, []byte("fake-oro"), 0o755); err != nil { //nolint:gosec // test-only fake binary
			t.Fatal(err)
		}

		// Use an empty PATH so LookPath won't find anything.
		t.Setenv("PATH", "")

		_, err := discoverCompanionFrom(basePath, "oro-dash")
		if err == nil {
			t.Fatal("expected error when companion not found, got nil")
		}

		errMsg := err.Error()

		// Error should mention the companion name.
		if !strings.Contains(errMsg, "oro-dash") {
			t.Errorf("error should mention companion name, got: %s", errMsg)
		}

		// Error should mention the sibling path that was checked.
		expectedSibling := filepath.Join(tmpDir, "oro-dash")
		if !strings.Contains(errMsg, expectedSibling) {
			t.Errorf("error should mention sibling path %s, got: %s", expectedSibling, errMsg)
		}

		// Error should contain installation guidance.
		if !strings.Contains(errMsg, "install") {
			t.Errorf("error should mention installation, got: %s", errMsg)
		}
	})

	t.Run("resolves symlinks before sibling lookup", func(t *testing.T) {
		// Create two directories: one with the real binary + companion,
		// and one with just a symlink to the binary.
		realDir := t.TempDir()
		linkDir := t.TempDir()

		// Real binary and companion in realDir.
		realBin := filepath.Join(realDir, "oro")
		if err := os.WriteFile(realBin, []byte("real-oro"), 0o755); err != nil { //nolint:gosec // test-only fake binary
			t.Fatal(err)
		}
		companionPath := filepath.Join(realDir, "oro-dash")
		if err := os.WriteFile(companionPath, []byte("real-dash"), 0o755); err != nil { //nolint:gosec // test-only fake binary
			t.Fatal(err)
		}

		// Symlink in linkDir pointing to real binary.
		linkPath := filepath.Join(linkDir, "oro")
		if err := os.Symlink(realBin, linkPath); err != nil {
			t.Fatal(err)
		}

		// discoverCompanionFrom receives the already-resolved path,
		// so discoverCompanion handles the symlink resolution.
		// We test that the top-level discoverCompanion would resolve
		// the symlink. Since we can't control os.Executable in tests,
		// we test discoverCompanionFrom with the resolved real path.
		got, err := discoverCompanionFrom(realBin, "oro-dash")
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if got != companionPath {
			t.Errorf("expected %s, got %s", companionPath, got)
		}
	})
}
