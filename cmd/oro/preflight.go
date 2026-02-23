package main

import (
	"context"
	"errors"
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

	// Remove stale binary before rebuilding; go build refuses to overwrite a
	// file that isn't a valid object/executable (Go 1.21+).
	_ = os.Remove(binPath)

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

// warnIfQualityGateUntracked writes a warning if quality_gate.sh exists in the
// given directory but is untracked in git. The directory is typically the repo root.
func warnIfQualityGateUntracked(w io.Writer, dir string) {
	qualityGatePath := filepath.Join(dir, "quality_gate.sh")

	// Check if the file exists
	if _, err := os.Stat(qualityGatePath); err != nil {
		// File doesn't exist — other functions handle this case
		return
	}

	// File exists. Check if it's tracked in git.
	isTracked, err := isFileTrackedInGit(dir, "quality_gate.sh")
	if err != nil {
		// We're either not in a git repo or git command failed — skip warning
		return
	}

	if !isTracked {
		fmt.Fprintf(w, "warning: quality_gate.sh exists but is untracked in git — commit it with: git add quality_gate.sh && git commit\n")
	}
}

// warnIfQualityGateMissing writes a warning if quality_gate.sh does not exist
// in the given directory. The directory is typically the repo root.
func warnIfQualityGateMissing(w io.Writer, dir string) {
	qualityGatePath := filepath.Join(dir, "quality_gate.sh")

	if _, err := os.Stat(qualityGatePath); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(w, "warning: quality_gate.sh is missing — run oro init to generate it\n")
		}
	}
}

// isFileTrackedInGit returns true if the given filename is tracked in git within
// the specified directory. Returns false if the file is untracked. Returns error if
// we're not in a git repository or if there's an unexpected failure.
func isFileTrackedInGit(dir, filename string) (bool, error) {
	// Run git ls-files to check if the file is tracked
	// If the file is tracked, it will be in the output; if not, output will be empty
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", filename) //nolint:gosec // filename is a constant or user-provided safe value
	cmd.Dir = dir

	// Suppress stderr to avoid "fatal" messages for untracked files
	cmd.Stderr = io.Discard

	err := cmd.Run()
	if err == nil {
		// git ls-files succeeded — file is tracked
		return true, nil
	}

	// Check if it's an exit error to distinguish between different error codes
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode := exitErr.ExitCode()
		if exitCode == 1 {
			// Exit code 1 means file is untracked (we are in a git repo)
			return false, nil
		}
		// Exit code 128 means not in a git repo — return error so caller knows to skip
		return false, fmt.Errorf("not in a git repository")
	}

	// Unexpected error (e.g., git not found)
	return false, fmt.Errorf("git check failed: %w", err)
}

// warnIfEpicCNotDeployed writes a warning if prompt.go has hardcoded coding rules
// instead of reading from config. This indicates Epic C (config-driven worker prompts)
// is not yet deployed.
func warnIfEpicCNotDeployed(w io.Writer, dir string) {
	promptPath := filepath.Join(dir, "prompt.go")

	data, err := os.ReadFile(promptPath) //nolint:gosec // dir is trusted (repo root)
	if err != nil {
		// File doesn't exist or can't be read — skip warning
		return
	}

	content := string(data)

	// Check for indicators of hardcoded rules vs config-driven rules
	// Hardcoded: contains constant declarations with tool names like "gofumpt", "golangci-lint"
	// Config-driven: reads from cfg.CodingRules or similar
	hasHardcodedRules := strings.Contains(content, "gofumpt") && strings.Contains(content, "const")
	hasConfigDriven := strings.Contains(content, "cfg.CodingRules") || strings.Contains(content, "config.CodingRules")

	if hasHardcodedRules && !hasConfigDriven {
		fmt.Fprintf(w, "warning: prompt.go has hardcoded coding rules — Epic C (config-driven worker prompts) is not deployed yet — external projects may receive incorrect linting instructions\n")
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
	if err := extractAssets(oroHome, subAssets, true); err != nil {
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
