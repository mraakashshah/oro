package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// runbookRefPattern matches a runbook reference like "docs/runbooks/beadstore-recovery.md"
// or "~/.oro/runbooks/beadstore-recovery.md" and captures the basename.
var runbookRefPattern = regexp.MustCompile(`runbooks/([A-Za-z0-9._-]+\.md)`)

// skillReferencedRunbooks scans every shipped skill markdown file under the given
// skills directory and returns the sorted, deduplicated set of runbook basenames
// they reference (e.g. "beadstore-recovery.md").
func skillReferencedRunbooks(t *testing.T, skillsDir string) []string {
	t.Helper()
	seen := map[string]struct{}{}
	err := filepath.WalkDir(skillsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, readErr := os.ReadFile(path) // #nosec G304 -- test walks a known skills tree
		if readErr != nil {
			return readErr
		}
		for _, m := range runbookRefPattern.FindAllSubmatch(data, -1) {
			seen[string(m[1])] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan skills for runbook refs: %v", err)
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestShippedSkillRunbookRefsResolveInRepo guards against a shipped skill pointing
// at a runbook that does not exist in docs/runbooks/ (the source of truth).
func TestShippedSkillRunbookRefsResolveInRepo(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	refs := skillReferencedRunbooks(t, filepath.Join(repoRoot, "assets", "skills"))
	if len(refs) == 0 {
		t.Skip("no shipped skills reference a runbook")
	}
	for _, name := range refs {
		src := filepath.Join(repoRoot, "docs", "runbooks", name)
		if _, err := os.Stat(src); err != nil {
			t.Errorf("shipped skill references runbooks/%s but docs/runbooks/%s is missing: %v", name, name, err)
		}
	}
}

// TestStageAssetsShipsSkillReferencedRunbooks verifies that 'make stage-assets'
// embeds every runbook that a shipped skill references into _assets/runbooks/,
// so the reference resolves from the binary in any project.
func TestStageAssetsShipsSkillReferencedRunbooks(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	tmp := t.TempDir()
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "Makefile"), makefile, 0o600); err != nil {
		t.Fatalf("write temp Makefile: %v", err)
	}
	if err := os.CopyFS(filepath.Join(tmp, "assets"), os.DirFS(filepath.Join(repoRoot, "assets"))); err != nil {
		t.Fatalf("copy assets fixture: %v", err)
	}
	if err := os.CopyFS(filepath.Join(tmp, "docs", "runbooks"), os.DirFS(filepath.Join(repoRoot, "docs", "runbooks"))); err != nil {
		t.Fatalf("copy runbooks fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "cmd", "oro"), 0o750); err != nil {
		t.Fatalf("mkdir cmd/oro: %v", err)
	}

	cmd := exec.Command("make", "stage-assets", "VERSION=test")
	cmd.Dir = tmp
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make stage-assets failed: %v\nOutput: %s", err, output)
	}

	refs := skillReferencedRunbooks(t, filepath.Join(tmp, "cmd", "oro", "_assets", "skills"))
	if len(refs) == 0 {
		t.Skip("no shipped skills reference a runbook")
	}
	for _, name := range refs {
		staged := filepath.Join(tmp, "cmd", "oro", "_assets", "runbooks", name)
		if _, err := os.Stat(staged); err != nil {
			t.Errorf("stage-assets did not embed runbook referenced by a skill: _assets/runbooks/%s missing: %v", name, err)
		}
	}
}

// TestDevSyncShipsSkillReferencedRunbooks verifies that 'make dev-sync' installs
// every runbook that a shipped skill references into $(ORO_HOME)/runbooks/, so the
// reference resolves against ~/.oro from any project.
func TestDevSyncShipsSkillReferencedRunbooks(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	oroHome := t.TempDir()

	cmd := exec.Command("make", "dev-sync", "ORO_HOME="+oroHome)
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make dev-sync failed: %v\nOutput: %s", err, output)
	}

	refs := skillReferencedRunbooks(t, filepath.Join(repoRoot, "assets", "skills"))
	if len(refs) == 0 {
		t.Skip("no shipped skills reference a runbook")
	}
	for _, name := range refs {
		installed := filepath.Join(oroHome, "runbooks", name)
		if _, err := os.Stat(installed); err != nil {
			t.Errorf("dev-sync did not install runbook referenced by a skill: %s/runbooks/%s missing: %v", oroHome, name, err)
		}
	}
}
