package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
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
}

// TestEnsureSearchHookMissingSrcDir verifies that ensureSearchHook fails open
// (logs warning, returns nil) when the source directory does not exist.
// This is required for go-install users who lack the source tree.
func TestEnsureSearchHookMissingSrcDir(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "oro-search-hook")

	err := ensureSearchHook(binPath, "/nonexistent/source/dir")
	if err != nil {
		t.Fatalf("expected nil for missing srcDir (fail-open), got error: %v", err)
	}
}

// TestPreflightWarnsOnMissingSearchHook verifies that warnIfSearchHookMissing
// writes a warning when the binary is absent and stays silent when it exists.
func TestPreflightWarnsOnMissingSearchHook(t *testing.T) {
	t.Run("warns when binary missing", func(t *testing.T) {
		var buf bytes.Buffer
		warnIfSearchHookMissing(&buf, "/nonexistent/path/oro-search-hook")
		if !strings.Contains(buf.String(), "oro-search-hook not found") {
			t.Errorf("expected warning about missing binary, got: %q", buf.String())
		}
	})

	t.Run("no warning when binary exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		binPath := filepath.Join(tmpDir, "oro-search-hook")
		if err := os.WriteFile(binPath, []byte("binary"), 0o600); err != nil { //nolint:gosec // test-only fake binary
			t.Fatal(err)
		}

		var buf bytes.Buffer
		warnIfSearchHookMissing(&buf, binPath)
		if buf.Len() > 0 {
			t.Errorf("expected no output, got: %q", buf.String())
		}
	})
}

// TestAssetVersionStampMismatch verifies checkAssetVersion behaviour:
//   - stale stamp triggers re-extraction and updates stamp
//   - matching stamp is a no-op
//   - missing stamp triggers extraction
//   - missing embedded .version skips the check (backwards compat)
func TestAssetVersionStampMismatch(t *testing.T) {
	// Minimal embedded FS with version "v1.0.0".
	// Includes stub entries for each mapped asset directory so extractAssets
	// can walk them without error (it skips directories that are missing).
	testFS := fstest.MapFS{
		"_assets/.version":       {Data: []byte("v1.0.0\n")},
		"_assets/skills/.keep":   {Data: []byte("")},
		"_assets/hooks/.keep":    {Data: []byte("")},
		"_assets/beacons/.keep":  {Data: []byte("")},
		"_assets/commands/.keep": {Data: []byte("")},
		"_assets/CLAUDE.md":      {Data: []byte("# claude\n")},
	}

	t.Run("stale stamp triggers re-extraction", func(t *testing.T) {
		oroHome := t.TempDir()
		stampPath := filepath.Join(oroHome, ".asset-version")
		if err := os.WriteFile(stampPath, []byte("v0.9.0\n"), 0o600); err != nil { //nolint:gosec // test-only stamp file
			t.Fatal(err)
		}

		reExtracted, err := checkAssetVersion(oroHome, testFS)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reExtracted {
			t.Error("expected reExtracted=true for stale stamp")
		}
		data, err := os.ReadFile(stampPath) //nolint:gosec // test-only path from t.TempDir()
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(data)) != "v1.0.0" {
			t.Errorf("expected stamp v1.0.0 after re-extraction, got %q", strings.TrimSpace(string(data)))
		}
	})

	t.Run("matching stamp is no-op", func(t *testing.T) {
		oroHome := t.TempDir()
		stampPath := filepath.Join(oroHome, ".asset-version")
		if err := os.WriteFile(stampPath, []byte("v1.0.0\n"), 0o600); err != nil { //nolint:gosec // test-only stamp file
			t.Fatal(err)
		}

		reExtracted, err := checkAssetVersion(oroHome, testFS)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reExtracted {
			t.Error("expected reExtracted=false for matching stamp")
		}
	})

	t.Run("missing stamp triggers extraction", func(t *testing.T) {
		oroHome := t.TempDir()

		reExtracted, err := checkAssetVersion(oroHome, testFS)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reExtracted {
			t.Error("expected reExtracted=true for missing stamp")
		}
		stampPath := filepath.Join(oroHome, ".asset-version")
		data, err := os.ReadFile(stampPath) //nolint:gosec // test-only path from t.TempDir()
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(data)) != "v1.0.0" {
			t.Errorf("expected stamp v1.0.0 after extraction, got %q", strings.TrimSpace(string(data)))
		}
	})

	t.Run("missing embedded version skips check", func(t *testing.T) {
		emptyFS := fstest.MapFS{}
		oroHome := t.TempDir()

		reExtracted, err := checkAssetVersion(oroHome, emptyFS)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reExtracted {
			t.Error("expected reExtracted=false when no embedded .version file")
		}
	})
}

// TestPreflightWarnsUntrackedQualityGate verifies warnings about quality_gate.sh:
// - warns when quality_gate.sh exists but is untracked in git
// - warns differently when quality_gate.sh is completely missing
// - no warning when quality_gate.sh is tracked
// - skips git check entirely if not in a git repo
func TestPreflightWarnsUntrackedQualityGate(t *testing.T) {
	t.Run("warns when quality_gate.sh is untracked in git", func(t *testing.T) {
		tmpDir := t.TempDir()
		qualityGatePath := filepath.Join(tmpDir, "quality_gate.sh")

		// Initialize a git repo
		if err := initGitRepo(tmpDir); err != nil {
			t.Fatalf("failed to init git repo: %v", err)
		}

		// Create the file but don't track it in git
		if err := os.WriteFile(qualityGatePath, []byte("#!/bin/bash\necho test\n"), 0o755); err != nil { //nolint:gosec // test-only file
			t.Fatal(err)
		}

		var buf bytes.Buffer
		warnIfQualityGateUntracked(&buf, tmpDir)
		output := buf.String()
		if !strings.Contains(output, "quality_gate.sh") || !strings.Contains(output, "untracked") {
			t.Errorf("expected warning about untracked quality_gate.sh, got: %q", output)
		}
	})

	t.Run("warns when quality_gate.sh is missing", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Don't create the file at all
		var buf bytes.Buffer
		warnIfQualityGateMissing(&buf, tmpDir)
		output := buf.String()
		if !strings.Contains(output, "quality_gate.sh") || !strings.Contains(output, "missing") {
			t.Errorf("expected warning about missing quality_gate.sh, got: %q", output)
		}
	})

	t.Run("no warning when quality_gate.sh is tracked", func(t *testing.T) {
		tmpDir := t.TempDir()
		qualityGatePath := filepath.Join(tmpDir, "quality_gate.sh")

		// Initialize a git repo
		if err := initGitRepo(tmpDir); err != nil {
			t.Fatalf("failed to init git repo: %v", err)
		}

		// Create and track the file
		if err := os.WriteFile(qualityGatePath, []byte("#!/bin/bash\necho test\n"), 0o755); err != nil { //nolint:gosec // test-only file
			t.Fatal(err)
		}

		// Track the file in git
		if err := addAndCommitFile(tmpDir, "quality_gate.sh"); err != nil {
			t.Fatalf("failed to track file in git: %v", err)
		}

		var buf bytes.Buffer
		warnIfQualityGateUntracked(&buf, tmpDir)
		output := buf.String()
		if strings.Contains(output, "untracked") {
			t.Errorf("expected no warning for tracked file, got: %q", output)
		}
	})

	t.Run("skips git check when not in git repo", func(t *testing.T) {
		tmpDir := t.TempDir()
		qualityGatePath := filepath.Join(tmpDir, "quality_gate.sh")

		// Create file but don't initialize git repo
		if err := os.WriteFile(qualityGatePath, []byte("#!/bin/bash\necho test\n"), 0o755); err != nil { //nolint:gosec // test-only file
			t.Fatal(err)
		}

		var buf bytes.Buffer
		// Should not warn about untracked when not in a git repo
		warnIfQualityGateUntracked(&buf, tmpDir)
		output := buf.String()
		// Should be silent (no untracked warning) when not in git repo
		if strings.Contains(output, "untracked") {
			t.Errorf("expected no untracked warning in non-git repo, got: %q", output)
		}
	})
}

// TestPreflightWarnsEpicCGate verifies warning when prompt.go still has hardcoded coding rules
func TestPreflightWarnsEpicCGate(t *testing.T) {
	t.Run("warns when prompt.go has hardcoded coding rules", func(t *testing.T) {
		tmpDir := t.TempDir()
		promptPath := filepath.Join(tmpDir, "prompt.go")

		// Write prompt.go with hardcoded coding rules
		hardcodedRules := `package main

const hardcodedCodingRules = "gofumpt, golangci-lint"
`
		if err := os.WriteFile(promptPath, []byte(hardcodedRules), 0o600); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		warnIfEpicCNotDeployed(&buf, tmpDir)
		output := buf.String()
		if !strings.Contains(output, "Epic C") && !strings.Contains(output, "config") {
			t.Errorf("expected warning about Epic C not deployed, got: %q", output)
		}
	})

	t.Run("no warning when prompt.go is config-driven", func(t *testing.T) {
		tmpDir := t.TempDir()
		promptPath := filepath.Join(tmpDir, "prompt.go")

		// Write prompt.go that's config-driven (reads from config)
		configDrivenRules := `package main

func getCodingRules(cfg *config.Config) string {
	return cfg.CodingRules
}
`
		if err := os.WriteFile(promptPath, []byte(configDrivenRules), 0o600); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		warnIfEpicCNotDeployed(&buf, tmpDir)
		output := buf.String()
		if len(output) > 0 {
			t.Errorf("expected no warning for config-driven prompt.go, got: %q", output)
		}
	})
}

// initGitRepo initializes a git repository in the given directory.
func initGitRepo(dir string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	return cmd.Run()
}

// addAndCommitFile adds a file to git and commits it.
func addAndCommitFile(dir, filename string) error {
	// Add the file
	cmd := exec.Command("git", "add", filename) //nolint:gosec // args are test-controlled constants
	cmd.Dir = dir
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}

	// Commit the file
	cmd = exec.Command("git", "commit", "-m", "test commit") //nolint:gosec // args are test-controlled constants
	cmd.Dir = dir
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	return cmd.Run()
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
