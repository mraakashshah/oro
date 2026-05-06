package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// candidateRecord returns a well-formed candidate inbox record as written by
// appendReviewPatternCandidates in the dispatcher.
func candidateRecord(bead, worker, pattern string) string {
	return "---\nbead: " + bead + "\nworker: " + worker + "\ncaptured_at: 2026-01-01T00:00:00Z\n\n" + pattern + "\n\n"
}

func TestReviewPatternsPromote_DedupAppend(t *testing.T) {
	dir := t.TempDir()
	candidatePath := filepath.Join(dir, "candidates.md")
	curatedPath := filepath.Join(dir, "curated.md")

	// Three candidates: two new, one already in curated.
	existingPattern := "security: log passwords -> use encrypted storage"
	newPatternA := "testing: skip assertions -> always assert return values"
	newPatternB := "errors: swallow silently -> wrap with context"

	candidateContent := candidateRecord("oro-1", "w1", newPatternA) +
		candidateRecord("oro-2", "w1", newPatternB) +
		candidateRecord("oro-3", "w1", existingPattern)

	if err := os.WriteFile(candidatePath, []byte(candidateContent), 0o600); err != nil { //nolint:gosec // test file
		t.Fatalf("write candidates: %v", err)
	}

	// Curated file already has the existing pattern.
	if err := os.WriteFile(curatedPath, []byte(existingPattern+"\n"), 0o600); err != nil { //nolint:gosec // test file
		t.Fatalf("write curated: %v", err)
	}

	n, err := promoteReviewPatternCandidates(candidatePath, curatedPath)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if n != 2 {
		t.Errorf("promoted = %d, want 2", n)
	}

	data, err := os.ReadFile(curatedPath) //nolint:gosec // test reads own temp file
	if err != nil {
		t.Fatalf("read curated: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, newPatternA) {
		t.Errorf("curated missing %q; got:\n%s", newPatternA, content)
	}
	if !strings.Contains(content, newPatternB) {
		t.Errorf("curated missing %q; got:\n%s", newPatternB, content)
	}
	// Existing pattern must appear exactly once (not duplicated).
	if got := strings.Count(content, existingPattern); got != 1 {
		t.Errorf("existing pattern count = %d, want 1; content:\n%s", got, content)
	}
}

func TestReviewPatternsPromote_CreatesMissingCuratedFile(t *testing.T) {
	dir := t.TempDir()
	candidatePath := filepath.Join(dir, "candidates.md")
	curatedPath := filepath.Join(dir, "subdir", "curated.md") // subdir does not exist yet

	pattern := "style: long functions -> extract helpers"
	if err := os.WriteFile(candidatePath, []byte(candidateRecord("oro-1", "w1", pattern)), 0o600); err != nil { //nolint:gosec // test file
		t.Fatalf("write candidates: %v", err)
	}

	// Curated file must not exist before promote.
	if _, err := os.Stat(curatedPath); !os.IsNotExist(err) {
		t.Fatal("curated file should not exist before promote")
	}

	n, err := promoteReviewPatternCandidates(candidatePath, curatedPath)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if n != 1 {
		t.Errorf("promoted = %d, want 1", n)
	}

	data, err := os.ReadFile(curatedPath) //nolint:gosec // test reads own temp file
	if err != nil {
		t.Fatalf("curated file not created: %v", err)
	}
	if !strings.Contains(string(data), pattern) {
		t.Errorf("curated file missing pattern %q; got:\n%s", pattern, data)
	}
}

// TestReviewPatternsPromote_LeavesGitStatusClean verifies that promotion bookkeeping
// does not produce untracked or modified files in git status.
//
// Setup: git repo with gitignore covering the candidate inbox; curated file
// pre-committed with the same pattern that is in the candidate inbox.
// Outcome: dedup skips all candidates → curated file unchanged → git status empty.
func TestReviewPatternsPromote_LeavesGitStatusClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoDir := t.TempDir()

	// Prevent git from walking up into the surrounding repository.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(repoDir))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repoDir, ".gitconfig-empty"))

	runGitCmd := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", repoDir}, args...) //nolint:gocritic // simple append
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Initialize repo and configure identity.
	runGitCmd("init")
	runGitCmd("config", "user.email", "test@test.com")
	runGitCmd("config", "user.name", "Test")

	// Gitignore covering the candidate inbox and its promoted sibling.
	gitignore := "/.oro/review-pattern-candidates.md\n/.oro/review-pattern-candidates.promoted.md\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(gitignore), 0o600); err != nil { //nolint:gosec // test file
		t.Fatalf("write .gitignore: %v", err)
	}

	// Create and commit curated file with the pattern already present.
	pattern := "review: missing nil check -> always check errors"
	assetsDir := filepath.Join(repoDir, "assets")
	if err := os.MkdirAll(assetsDir, 0o750); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	curatedPath := filepath.Join(assetsDir, "review-patterns.md")
	if err := os.WriteFile(curatedPath, []byte(pattern+"\n"), 0o600); err != nil { //nolint:gosec // test file
		t.Fatalf("write curated: %v", err)
	}

	runGitCmd("add", ".gitignore", "assets/review-patterns.md")
	runGitCmd("commit", "--no-verify", "-m", "init")

	// Create candidate inbox (gitignored).
	oroDir := filepath.Join(repoDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil {
		t.Fatalf("mkdir .oro: %v", err)
	}
	candidatePath := filepath.Join(oroDir, "review-pattern-candidates.md")
	if err := os.WriteFile(candidatePath, []byte(candidateRecord("oro-1", "w1", pattern)), 0o600); err != nil { //nolint:gosec // test file
		t.Fatalf("write candidates: %v", err)
	}

	// Promote: pattern already in curated → dedup → nothing written.
	n, err := promoteReviewPatternCandidates(candidatePath, curatedPath)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 promoted (all deduped), got %d", n)
	}

	// Git status must be empty: candidate file is gitignored, curated unchanged.
	out, err := exec.Command("git", "-C", repoDir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "" {
		t.Errorf("git status not clean after promote:\n%s", got)
	}
}
