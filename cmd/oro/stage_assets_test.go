package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// Verify the shared runtime-agnostic instructions were copied.
	sharedInstructionsFile := filepath.Join(assetsDir, "ORO_AGENT.md")
	if _, err := os.Stat(sharedInstructionsFile); os.IsNotExist(err) {
		t.Error("ORO_AGENT.md was not copied to _assets/")
	}

	// CRITICAL TEST: Verify .test-marker was copied from repo's assets/ directory.
	// This marker file exists in assets/ but NOT in ~/.oro/, proving we're
	// copying from the correct source.
	markerFile := filepath.Join(assetsDir, ".test-marker")
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		t.Fatal("_assets/.test-marker not found - stage-assets is still copying from ~/.oro/ instead of repo assets/")
	}
	// Note: _assets/ is intentionally NOT removed here. The go:embed directive in
	// embed.go requires _assets/ to exist for subsequent go build steps in CI.
	// CI cleans up via 'make clean-assets' after all build/test steps complete.
}

func TestStageAssetsBuildsTempDirBeforeSwap(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	text := string(makefile)
	for _, want := range []string{
		`tmp="cmd/oro/.assets-stage-$$$$"`,
		`old="cmd/oro/.assets-old-$$$$"`,
		`trap 'cleanup $$?' EXIT`,
		`trap 'cleanup 130' INT`,
		`trap 'cleanup 143' TERM`,
		`mkdir -p "$$tmp/skills"`,
		`mv cmd/oro/_assets "$$old"`,
		`mv "$$tmp" cmd/oro/_assets`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("stage-assets should build in a temp dir and swap atomically; missing %q", want)
		}
	}
	stageTarget := text[strings.Index(text, "stage-assets:"):strings.Index(text, "clean-assets:")]
	if strings.Contains(stageTarget, "|| true") {
		t.Fatal("stage-assets should not mask copy failures with || true")
	}
}

func TestStageAssetsRestoresOldAssetsWhenSwapFails(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "Makefile"), makefile, 0o600); err != nil {
		t.Fatalf("write temp Makefile: %v", err)
	}
	for _, dir := range []string{
		"assets/skills",
		"assets/hooks",
		"assets/beacons",
		"assets/commands",
		"cmd/oro/_assets",
		"bin",
	} {
		if err := os.MkdirAll(filepath.Join(tmp, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "assets", "CLAUDE.md"), []byte("# staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "cmd", "oro", "_assets", "marker"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fakeMV := `#!/bin/sh
case "$1:$2" in
  *".assets-stage-"*":cmd/oro/_assets") exit 42 ;;
esac
exec /bin/mv "$@"
`
	if err := os.WriteFile(filepath.Join(tmp, "bin", "mv"), []byte(fakeMV), 0o700); err != nil {
		t.Fatalf("write fake mv: %v", err)
	}

	cmd := exec.Command("make", "stage-assets", "VERSION=test")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(tmp, "bin")+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected stage-assets swap failure, got success\nOutput: %s", output)
	}
	marker, err := os.ReadFile(filepath.Join(tmp, "cmd", "oro", "_assets", "marker"))
	if err != nil {
		t.Fatalf("old _assets marker was not restored after failed swap; output: %s; err: %v", output, err)
	}
	if string(marker) != "old\n" {
		t.Fatalf("old _assets marker content changed: %q", marker)
	}
	leftovers, err := filepath.Glob(filepath.Join(tmp, "cmd", "oro", ".assets-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("stage-assets left temp swap directories: %v", leftovers)
	}
}

func TestStageAssetsRestoresOldAssetsWhenCopyFails(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "Makefile"), makefile, 0o600); err != nil {
		t.Fatalf("write temp Makefile: %v", err)
	}
	for _, dir := range []string{
		"assets/skills/sample",
		"assets/hooks",
		"assets/beacons",
		"assets/commands",
		"cmd/oro/_assets",
		"bin",
	} {
		if err := os.MkdirAll(filepath.Join(tmp, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "assets", "skills", "sample", "SKILL.md"), []byte("# skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "cmd", "oro", "_assets", "marker"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeCP := "#!/bin/sh\nexit 42\n"
	if err := os.WriteFile(filepath.Join(tmp, "bin", "cp"), []byte(fakeCP), 0o700); err != nil {
		t.Fatalf("write fake cp: %v", err)
	}

	cmd := exec.Command("make", "stage-assets", "VERSION=test")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(tmp, "bin")+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected stage-assets copy failure, got success\nOutput: %s", output)
	}
	marker, err := os.ReadFile(filepath.Join(tmp, "cmd", "oro", "_assets", "marker"))
	if err != nil {
		t.Fatalf("old _assets marker was not restored after copy failure; output: %s; err: %v", output, err)
	}
	if string(marker) != "old\n" {
		t.Fatalf("old _assets marker content changed: %q", marker)
	}
}

func TestDevSyncRemovesStaleInstalledSkillAssets(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	oroHome := t.TempDir()
	staleSkill := filepath.Join(oroHome, ".claude", "skills", "beads", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(staleSkill), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleSkill, []byte("legacy bd ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("make", "dev-sync", "ORO_HOME="+oroHome)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make dev-sync failed: %v\nOutput: %s", err, output)
	}

	if _, err := os.Stat(staleSkill); !os.IsNotExist(err) {
		t.Fatalf("dev-sync should remove stale deleted skill asset %s; stat err=%v", staleSkill, err)
	}
	if _, err := os.Stat(filepath.Join(oroHome, ".claude", "skills", "test-driven-development", "SKILL.md")); err != nil {
		t.Fatalf("dev-sync should install current skills: %v", err)
	}
}

func TestBuildInstallRestagesAssetsBetweenGoals(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	tmp := t.TempDir()
	for _, dir := range []string{
		"assets/skills/test-driven-development",
		"assets/hooks",
		"assets/beacons",
		"assets/commands",
		"cmd/oro",
		"cmd/oro-search-hook",
		"bin",
		"oro-home",
	} {
		if err := os.MkdirAll(filepath.Join(tmp, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "Makefile"), makefile, 0o600); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "assets", "CLAUDE.md"), []byte("# claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "assets", "ORO_AGENT.md"), []byte("# oro\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "assets", "hooks", "enforce_skills.py"), []byte("# hook\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "assets", "skills", "test-driven-development", "SKILL.md"), []byte("# skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fakeGo := `#!/bin/sh
set -eu
case "$1" in
  build|install)
    if [ ! -d cmd/oro/_assets ]; then
      echo "missing staged assets for go $1" >&2
      exit 42
    fi
    out=""
    prev=""
    for arg in "$@"; do
      if [ "$prev" = "-o" ]; then out="$arg"; break; fi
      prev="$arg"
    done
    if [ -n "$out" ]; then
      mkdir -p "$(dirname "$out")"
      : >"$out"
    fi
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(tmp, "bin", "go"), []byte(fakeGo), 0o700); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	cmd := exec.Command("make", "build", "install", "ORO_HOME="+filepath.Join(tmp, "oro-home"))
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(tmp, "bin")+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make build install should restage assets before each goal: %v\nOutput: %s", err, output)
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
