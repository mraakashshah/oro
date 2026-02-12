package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// runPreflightChecks verifies that all required external tools are available
// and that the git repository is in a good state for oro to operate.
// Returns an error with an actionable message if any check fails.
func runPreflightChecks() error {
	requiredTools := []string{"tmux", "claude", "bd", "git"}

	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required tool '%s' not found in PATH — run 'oro init' to bootstrap all dependencies", tool)
		}
	}

	// Check git repo status - verify we can run git commands.
	// A more sophisticated check could verify the repo is clean,
	// but for now we just verify git works.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("not in a git repository or git is not functioning properly: %w", err)
	}

	return nil
}

// ensureSearchHook builds the oro-search-hook binary if it is missing or stale
// (older than any source file in srcDir). Returns an error only if srcDir is
// missing — build failures are fail-open (logged, not fatal).
func ensureSearchHook(binPath, srcDir string) error {
	// Verify source directory exists.
	if _, err := os.Stat(srcDir); err != nil {
		return fmt.Errorf("search hook source dir: %w", err)
	}

	if !isStale(binPath, srcDir) {
		return nil
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(binPath), 0o750); err != nil {
		return fmt.Errorf("create hook dir: %w", err)
	}

	// Derive repo root (two levels up from srcDir which is cmd/oro-search-hook).
	repoRoot := filepath.Dir(filepath.Dir(srcDir))

	// Compute relative package path from repo root.
	relPkg, err := filepath.Rel(repoRoot, srcDir)
	if err != nil {
		return fmt.Errorf("compute relative path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./"+relPkg) //nolint:gosec // args constructed internally from known paths
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fail-open: log warning but don't block startup.
		fmt.Fprintf(os.Stderr, "warning: failed to build search hook: %v\n%s\n", err, out)
		return nil
	}

	return nil
}

// isStale returns true if binPath doesn't exist or is older than any file in srcDir.
func isStale(binPath, srcDir string) bool {
	binInfo, err := os.Stat(binPath)
	if err != nil {
		return true // binary doesn't exist
	}
	binMod := binInfo.ModTime()

	stale := false
	_ = filepath.WalkDir(srcDir, func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr //nolint:nilerr // propagate or skip dirs
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil //nolint:nilerr // skip unreadable files
		}
		if info.ModTime().After(binMod) {
			stale = true
			return filepath.SkipAll
		}
		return nil
	})
	return stale
}
