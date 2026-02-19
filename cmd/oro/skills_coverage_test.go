package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillsAllListedInUsingSkillsIndex verifies that every skill directory
// under .claude/skills/ is listed by name in the using-skills index (SKILL.md).
//
// It walks up from the current working directory to find .claude/skills/,
// which handles both the main repo and worktree setups.
func TestSkillsAllListedInUsingSkillsIndex(t *testing.T) {
	skillsDir := findSkillsDir(t)

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("failed to read skills directory %s: %v", skillsDir, err)
	}

	// Collect direct subdirectory names, excluding "using-skills" (the index itself).
	var skillNames []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if name == "using-skills" {
			continue
		}
		skillNames = append(skillNames, name)
	}

	if len(skillNames) == 0 {
		t.Fatal("no skill directories found (excluding using-skills)")
	}

	// Read the using-skills index.
	indexPath := filepath.Join(skillsDir, "using-skills", "SKILL.md")
	data, err := os.ReadFile(indexPath) //nolint:gosec // reads fixed path under .claude/
	if err != nil {
		t.Fatalf("failed to read using-skills index at %s: %v", indexPath, err)
	}
	indexContent := string(data)

	// Check each skill name appears as a literal substring in SKILL.md.
	var missing []string
	for _, name := range skillNames {
		if !strings.Contains(indexContent, name) {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		t.Errorf("skills not listed in using-skills index: %v", missing)
		t.Logf("add the missing skill names to %s", indexPath)
		t.Logf("hint: check the Quick Reference section for each category")
	}
}

// findSkillsDir walks up from the current working directory to find .claude/skills/.
// This handles both the main repo and worktree setups.
func findSkillsDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	for {
		candidate := filepath.Join(dir, ".claude", "skills")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find .claude/skills/ by walking up from working directory")
		}
		dir = parent
	}
}
