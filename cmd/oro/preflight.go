package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// (older than any source file in srcDir). Fail-open on all errors: missing
// srcDir logs a warning and returns nil (safe for go-install users who lack
// the source tree), and build failures are logged but not fatal.
func ensureSearchHook(binPath, srcDir string) error {
	// Verify source directory exists — fail-open for go-install users.
	if _, err := os.Stat(srcDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: oro-search-hook source dir not found (%s) — skipping build\n", srcDir)
		return nil
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

// warnIfSearchHookMissing writes a warning to w if the oro-search-hook binary
// is not found at binPath. Called during oro start — does not attempt to build
// (use oro init for that).
func warnIfSearchHookMissing(w io.Writer, binPath string) {
	if _, err := os.Stat(binPath); err != nil {
		fmt.Fprintf(w, "warning: oro-search-hook not found — run oro init to build it\n")
	}
}

// checkAssetVersion compares the version embedded in the binary (_assets/.version)
// against the stamp written to oroHome/.asset-version by the last extraction.
// If they differ (or the stamp is absent), assets are re-extracted and the stamp updated.
// Returns reExtracted=true when extraction was performed.
// If the embedded FS has no .version file, returns false, nil (backwards compat with old builds).
// If re-extraction fails, returns a hard error directing the user to run oro init.
func checkAssetVersion(oroHome string, embedded fs.FS) (bool, error) {
	// Read embedded version — skip check if absent (old builds lack .version).
	versionData, err := fs.ReadFile(embedded, "_assets/.version")
	if err != nil {
		return false, nil //nolint:nilerr // intentional: missing .version means skip check (backwards compat)
	}
	embeddedVersion := strings.TrimSpace(string(versionData))

	// Read on-disk stamp; missing file is treated as stale (triggers re-extraction).
	stampPath := filepath.Join(oroHome, ".asset-version")
	diskData, err := os.ReadFile(stampPath) //nolint:gosec // stampPath constructed from trusted oroHome
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read asset stamp: %w", err)
	}
	diskVersion := strings.TrimSpace(string(diskData))

	if diskVersion == embeddedVersion {
		return false, nil
	}

	// Versions differ — re-extract assets (extractAssets also writes the stamp).
	subAssets, err := fs.Sub(embedded, "_assets")
	if err != nil {
		return false, fmt.Errorf("re-extract assets: %w — run oro init to update", err)
	}
	if err := extractAssets(oroHome, subAssets); err != nil {
		return false, fmt.Errorf("re-extract assets: %w — run oro init to update", err)
	}

	fmt.Fprintf(os.Stderr, "assets updated from %s to %s\n", diskVersion, embeddedVersion)
	return true, nil
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
