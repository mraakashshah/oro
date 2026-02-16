package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestStageAssetsUsesRepoAssetsDir verifies that 'make stage-assets' copies
// from the repo's assets/ directory, not from $(ORO_HOME).
func TestStageAssetsUsesRepoAssetsDir(t *testing.T) {
	// Find repo root (two levels up from cmd/oro/)
	repoRoot := filepath.Join("..", "..")
	assetsDir := filepath.Join(repoRoot, "cmd", "oro", "_assets")

	// Clean up any existing _assets from previous runs
	if err := os.RemoveAll(assetsDir); err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to clean _assets: %v", err)
	}

	// Run make stage-assets from repo root
	cmd := exec.Command("make", "stage-assets")
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make stage-assets failed: %v\nOutput: %s", err, output)
	}

	// Verify _assets/ was created
	if _, err := os.Stat(assetsDir); os.IsNotExist(err) {
		t.Fatal("_assets/ directory was not created by stage-assets")
	}

	// Verify expected directories exist
	expectedDirs := []string{
		filepath.Join(assetsDir, "skills"),
		filepath.Join(assetsDir, "hooks"),
		filepath.Join(assetsDir, "beacons"),
		filepath.Join(assetsDir, "commands"),
	}

	for _, dir := range expectedDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Errorf("expected directory %s was not created", dir)
		}
	}

	// Verify CLAUDE.md was copied
	claudeFile := filepath.Join(assetsDir, "CLAUDE.md")
	if _, err := os.Stat(claudeFile); os.IsNotExist(err) {
		t.Error("CLAUDE.md was not copied to _assets/")
	}

	// CRITICAL TEST: Verify .test-marker was copied from repo's assets/ directory.
	// This marker file exists in assets/ but NOT in ~/.oro/, proving we're
	// copying from the correct source.
	markerFile := filepath.Join(assetsDir, ".test-marker")
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Fatal("_assets/.test-marker not found - stage-assets is still copying from ~/.oro/ instead of repo assets/")
	}

	// Clean up
	if err := os.RemoveAll(assetsDir); err != nil {
		t.Logf("warning: failed to clean up _assets: %v", err)
	}
}

// TestStageAssetsFailsWhenAssetsDirMissing verifies that stage-assets
// produces a clear error when assets/ directory is missing.
func TestStageAssetsFailsWhenAssetsDirMissing(t *testing.T) {
	// This test requires temporarily renaming assets/ to simulate it missing.
	// Check if assets/ exists first.
	repoRoot := filepath.Join("..", "..")
	assetsDir := filepath.Join(repoRoot, "assets")

	if _, err := os.Stat(assetsDir); os.IsNotExist(err) {
		// Perfect - assets/ doesn't exist, we can test the error case
		cmd := exec.Command("make", "stage-assets")
		cmd.Dir = repoRoot
		output, err := cmd.CombinedOutput()

		if err == nil {
			t.Fatal("expected make stage-assets to fail when assets/ is missing, but it succeeded")
		}

		// Verify we get a clear error message
		outputStr := string(output)
		if len(outputStr) == 0 {
			t.Error("expected clear error message when assets/ missing, got silent failure")
		}
		t.Logf("Error output: %s", outputStr)
	} else {
		t.Skip("assets/ exists - cannot test missing assets/ error case without destructive rename")
	}
}
