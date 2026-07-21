package dispatcher //nolint:testpackage // white-box integration test for worktree spawn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreeCreatedFromCurrentMain verifies that Create fetches the base
// branch from origin before branching, so an ahead remote HEAD wins over a
// stale local ref.
//
// Repro scenario: local main is at commit A; another developer pushes commit B
// (e.g. a go.mod version bump) to origin/main. Without a fetch, a new agent
// worktree would still branch from A, causing the worker to operate on stale
// content (the govulncheck loop root cause). With the fix the worktree HEAD
// must match B.
func TestWorktreeCreatedFromCurrentMain(t *testing.T) {
	ctx := context.Background()

	// Isolate subprocesses from the parent oro worktree's git context.
	tmpBase := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(tmpBase))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmpBase, ".gitconfig-empty"))
	for _, k := range []string{"GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE", "GIT_PREFIX"} {
		t.Setenv(k, "")
		os.Unsetenv(k) //nolint:errcheck
	}

	runner := &ExecCommandRunner{}
	run := func(args ...string) {
		t.Helper()
		if _, err := runner.Run(ctx, "git", args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	runOut := func(args ...string) string {
		t.Helper()
		out, err := runner.Run(ctx, "git", args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	writeGoMod := func(dir, goVersion string) {
		t.Helper()
		content := "module example.com/app\n\n" + goVersion + "\n"
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o600); err != nil {
			t.Fatalf("write go.mod in %s: %v", dir, err)
		}
	}

	localDir := filepath.Join(tmpBase, "local")
	remoteDir := filepath.Join(tmpBase, "remote.git")
	secondDir := filepath.Join(tmpBase, "second")
	worktreesDir := filepath.Join(tmpBase, "worktrees")

	// ── Set up local repo at commit A ──────────────────────────────────────
	run("init", localDir)
	run("-C", localDir, "config", "user.name", "Test")
	run("-C", localDir, "config", "user.email", "test@test.test")

	writeGoMod(localDir, "go 1.21")
	run("-C", localDir, "add", "go.mod")
	run("-C", localDir, "commit", "--no-verify", "-m", "initial: commit A")
	run("-C", localDir, "branch", "-m", "main") // normalize to "main" regardless of git default
	commitA := runOut("-C", localDir, "rev-parse", "HEAD")

	// ── Create bare remote; push local main ────────────────────────────────
	run("init", "--bare", remoteDir)
	run("-C", remoteDir, "symbolic-ref", "HEAD", "refs/heads/main")
	run("-C", localDir, "remote", "add", "origin", remoteDir)
	run("-C", localDir, "push", "-u", "origin", "main")

	// ── Advance remote to commit B via a second clone ──────────────────────
	run("clone", remoteDir, secondDir)
	run("-C", secondDir, "config", "user.name", "Second")
	run("-C", secondDir, "config", "user.email", "second@test.test")

	writeGoMod(secondDir, "go 1.22")
	run("-C", secondDir, "add", "go.mod")
	run("-C", secondDir, "commit", "--no-verify", "-m", "advance: commit B (go.mod bump)")
	run("-C", secondDir, "push", "origin", "main")
	commitB := runOut("-C", secondDir, "rev-parse", "HEAD")

	// ── Sanity checks ───────────────────────────────────────────────────────
	if localMain := runOut("-C", localDir, "rev-parse", "main"); localMain != commitA {
		t.Fatalf("setup: local main should be at commitA=%s, got %s", commitA, localMain)
	}
	if commitA == commitB {
		t.Fatal("setup: commitA and commitB must differ")
	}

	// ── Call Create — should fetch from origin and branch from commitB ──────
	mgr := NewGitWorktreeManager(localDir, worktreesDir, "", runner)
	worktreePath, _, err := mgr.Create(ctx, "bead-1", "main")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// The agent worktree HEAD must match commitB (current remote main), not the
	// stale commitA that local main still points to.
	wtHEAD := runOut("-C", worktreePath, "rev-parse", "HEAD")
	if wtHEAD != commitB {
		t.Fatalf("worktree HEAD = %s\n  want commitB = %s (current remote main)\n  stale commitA = %s\n  Create did not fetch from origin before branching",
			wtHEAD, commitB, commitA)
	}

	// Verify go.mod content reflects the post-advance state.
	content, err := os.ReadFile(filepath.Join(worktreePath, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod in worktree: %v", err)
	}
	if !strings.Contains(string(content), "go 1.22") {
		t.Fatalf("worktree go.mod should contain 'go 1.22' (commit B), got:\n%s", content)
	}
}
