package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstall_RemovesOroHome(t *testing.T) {
	oroHome := t.TempDir()

	// Create some files to simulate a real ~/.oro/
	mkFile(t, oroHome, "hooks/enforce_skills.py", "# hook")
	mkFile(t, oroHome, ".claude/skills/tdd/SKILL.md", "# skill")
	mkFile(t, oroHome, ".asset-version", "0.1.0\n")

	var buf bytes.Buffer
	opts := uninstallOptions{
		oroHome: oroHome,
		force:   true,
		w:       &buf,
	}

	if err := runUninstall(opts); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	if _, err := os.Stat(oroHome); !os.IsNotExist(err) {
		t.Errorf("expected oroHome to be removed, got err=%v", err)
	}
}

func TestUninstall_KeepDataPreservesOroHome(t *testing.T) {
	oroHome := t.TempDir()

	mkFile(t, oroHome, ".asset-version", "0.1.0\n")

	var buf bytes.Buffer
	opts := uninstallOptions{
		oroHome:  oroHome,
		force:    true,
		keepData: true,
		w:        &buf,
	}

	if err := runUninstall(opts); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	if _, err := os.Stat(oroHome); os.IsNotExist(err) {
		t.Error("expected oroHome to be preserved with --keep-data")
	}
}

func TestUninstall_CleansGlobalGitignore(t *testing.T) {
	oroHome := t.TempDir()
	gitignorePath := filepath.Join(t.TempDir(), "gitignore_global")

	content := "# user entries\n*.log\n\n# Oro / Beads (managed by oro init)\n" + beadsDirName + "/\n" + beadsDirName + "\n.oro/\n.dolt/\n"
	if err := os.WriteFile(gitignorePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	opts := uninstallOptions{
		oroHome:             oroHome,
		force:               true,
		keepData:            true, // skip removal of oroHome to isolate this test
		globalGitignorePath: gitignorePath,
		w:                   &buf,
	}

	if err := runUninstall(opts); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	cleaned, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range oroGitignoreEntries() {
		if strings.Contains(string(cleaned), entry) {
			t.Errorf("expected entry %q to be removed from gitignore", entry)
		}
	}

	if !strings.Contains(string(cleaned), "*.log") {
		t.Error("expected user entries to be preserved in gitignore")
	}
}

func TestUninstall_CleansProjectArtifacts(t *testing.T) {
	oroHome := t.TempDir()
	projectRoot := t.TempDir()
	projectName := "test-project"

	// Set up project dir in oroHome
	projDir := filepath.Join(oroHome, "projects", projectName)
	mkFile(t, projDir, "project.root", projectRoot)

	// Create .oro/ anchor dir in project root
	oroAnchor := filepath.Join(projectRoot, ".oro")
	if err := os.MkdirAll(oroAnchor, 0o755); err != nil {
		t.Fatal(err)
	}
	mkFile(t, oroAnchor, "config.yaml", "project: test\n")

	// Create .worktrees/ dir in project root
	worktreesDir := filepath.Join(projectRoot, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a fake .git dir for hook cleanup
	gitDir := filepath.Join(projectRoot, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write an oro-managed pre-push hook
	hookContent := "#!/bin/sh\n# managed by oro — do not edit manually\nset -e\n"
	if err := os.WriteFile(filepath.Join(gitDir, "hooks", "pre-push"), []byte(hookContent), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	opts := uninstallOptions{
		oroHome:  oroHome,
		force:    true,
		keepData: true,
		w:        &buf,
	}

	if err := runUninstall(opts); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	// .oro/ anchor should be removed
	if _, err := os.Stat(oroAnchor); !os.IsNotExist(err) {
		t.Error("expected .oro/ anchor dir to be removed")
	}

	// .worktrees/ should be removed
	if _, err := os.Stat(worktreesDir); !os.IsNotExist(err) {
		t.Error("expected .worktrees/ dir to be removed")
	}

	// pre-push hook should be removed
	if _, err := os.Stat(filepath.Join(gitDir, "hooks", "pre-push")); !os.IsNotExist(err) {
		t.Error("expected pre-push hook to be removed")
	}
}

func TestUninstall_MissingOroHomeIsNotAnError(t *testing.T) {
	var buf bytes.Buffer
	opts := uninstallOptions{
		oroHome: filepath.Join(t.TempDir(), "nonexistent"),
		force:   true,
		w:       &buf,
	}

	if err := runUninstall(opts); err != nil {
		t.Fatalf("expected no error for missing oroHome, got: %v", err)
	}
}

func TestUninstall_ConfirmationPromptWorks(t *testing.T) {
	oroHome := t.TempDir()
	mkFile(t, oroHome, ".asset-version", "0.1.0\n")

	var buf bytes.Buffer
	opts := uninstallOptions{
		oroHome: oroHome,
		force:   false,
		stdin:   strings.NewReader("y\n"),
		w:       &buf,
	}

	if err := runUninstall(opts); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	// oroHome should be removed when user confirms with "y".
	if _, err := os.Stat(oroHome); !os.IsNotExist(err) {
		t.Error("expected oroHome to be removed after confirmation, but it still exists")
	}
}

func TestUninstall_ForceSkipsPrompt(t *testing.T) {
	oroHome := t.TempDir()
	mkFile(t, oroHome, ".asset-version", "0.1.0\n")

	var buf bytes.Buffer
	// stdin is empty — if a prompt were shown, ReadString would fail/hang.
	opts := uninstallOptions{
		oroHome: oroHome,
		force:   true,
		stdin:   strings.NewReader(""),
		w:       &buf,
	}

	if err := runUninstall(opts); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	// oroHome should be removed without any prompt.
	if _, err := os.Stat(oroHome); !os.IsNotExist(err) {
		t.Error("expected oroHome to be removed with --force, but it still exists")
	}

	out := buf.String()
	if strings.Contains(out, "[y/N]") {
		t.Error("expected no confirmation prompt when --force is set")
	}
}

func TestUninstall_ConfirmationAbortedPreservesOroHome(t *testing.T) {
	oroHome := t.TempDir()
	mkFile(t, oroHome, ".asset-version", "0.1.0\n")

	var buf bytes.Buffer
	opts := uninstallOptions{
		oroHome: oroHome,
		force:   false,
		stdin:   strings.NewReader("n\n"),
		w:       &buf,
	}

	if err := runUninstall(opts); err != nil {
		t.Fatalf("runUninstall should not error on abort, got: %v", err)
	}

	// oroHome should be preserved when user denies.
	if _, err := os.Stat(oroHome); os.IsNotExist(err) {
		t.Error("expected oroHome to be preserved when user declines confirmation")
	}
}

// mkFile creates a file with parent dirs in a temp dir.
func mkFile(t *testing.T, base, rel, content string) {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
