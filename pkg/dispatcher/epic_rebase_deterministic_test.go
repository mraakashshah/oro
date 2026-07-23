package dispatcher //nolint:testpackage // white-box tests for deterministic epic-rebase recovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// gitOut runs a git command in repo and returns trimmed stdout, failing the test
// on error. Used to capture OIDs and prove ancestry independently of the code
// under test.
func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func isAncestorGit(t *testing.T, repo, older, newer string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", older, newer)
	return cmd.Run() == nil
}

// newPreserveRepo builds a repo where main and branch diverge. When
// conflicting is true both edit the same file (merge conflict); otherwise they
// touch disjoint files (clean merge). Returns the repo path and a manager
// rooted at it.
func newPreserveRepo(t *testing.T, branch string, conflicting bool) (string, *GitWorktreeManager) {
	t.Helper()
	repo := t.TempDir()
	runAssignmentTestGit(t, repo, "init", "-b", "main")
	runAssignmentTestGit(t, repo, "config", "user.email", "test@example.com")
	runAssignmentTestGit(t, repo, "config", "user.name", "Oro Test")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runAssignmentTestGit(t, repo, "add", "shared.txt")
	runAssignmentTestGit(t, repo, "commit", "-m", "base commit")

	runAssignmentTestGit(t, repo, "checkout", "-b", branch)
	epicFile, epicContent := "epic.txt", "epic\n"
	if conflicting {
		epicFile, epicContent = "shared.txt", "epic-change\n"
	}
	if err := os.WriteFile(filepath.Join(repo, epicFile), []byte(epicContent), 0o644); err != nil {
		t.Fatalf("write epic: %v", err)
	}
	runAssignmentTestGit(t, repo, "add", epicFile)
	runAssignmentTestGit(t, repo, "commit", "-m", "epic commit")

	runAssignmentTestGit(t, repo, "checkout", "main")
	mainFile, mainContent := "main.txt", "main\n"
	if conflicting {
		mainFile, mainContent = "shared.txt", "main-change\n"
	}
	if err := os.WriteFile(filepath.Join(repo, mainFile), []byte(mainContent), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}
	runAssignmentTestGit(t, repo, "add", mainFile)
	runAssignmentTestGit(t, repo, "commit", "-m", "main commit")

	return repo, NewGitWorktreeManager(repo, "", "", &ExecCommandRunner{})
}

func TestPreserveEpicAncestryCleanMergeCreatesBothAncestors(t *testing.T) {
	ctx := context.Background()
	repo, mgr := newPreserveRepo(t, "epic/clean", false)
	oldEpicOID := gitOut(t, repo, "rev-parse", "epic/clean")
	mainOID := gitOut(t, repo, "rev-parse", "main")

	outcome, sha, err := mgr.preserveEpicAncestry(ctx, "epic/clean", "main", "preserve")
	if err != nil {
		t.Fatalf("PreserveEpicAncestry: %v", err)
	}
	if outcome != epicPreserveMerged {
		t.Fatalf("outcome = %v, want epicPreserveMerged", outcome)
	}
	newEpicOID := gitOut(t, repo, "rev-parse", "epic/clean")
	if newEpicOID != sha {
		t.Fatalf("returned sha %s != epic branch head %s", sha, newEpicOID)
	}
	if newEpicOID == oldEpicOID {
		t.Fatal("epic branch was not advanced")
	}
	if !isAncestorGit(t, repo, mainOID, newEpicOID) {
		t.Error("main is not an ancestor of the new epic tip")
	}
	if !isAncestorGit(t, repo, oldEpicOID, newEpicOID) {
		t.Error("old epic tip is not an ancestor of the new epic tip")
	}
}

func TestPreserveEpicAncestryConflictLeavesRefUnchanged(t *testing.T) {
	ctx := context.Background()
	repo, mgr := newPreserveRepo(t, "epic/conflict", true)
	oldEpicOID := gitOut(t, repo, "rev-parse", "epic/conflict")

	outcome, sha, err := mgr.preserveEpicAncestry(ctx, "epic/conflict", "main", "preserve")
	if err != nil {
		t.Fatalf("PreserveEpicAncestry returned error, want conflict outcome: %v", err)
	}
	if outcome != epicPreserveConflict {
		t.Fatalf("outcome = %v, want epicPreserveConflict", outcome)
	}
	if sha != "" {
		t.Errorf("conflict returned sha %q, want empty", sha)
	}
	if got := gitOut(t, repo, "rev-parse", "epic/conflict"); got != oldEpicOID {
		t.Errorf("epic ref changed on conflict: %s != %s", got, oldEpicOID)
	}
}

func TestPreserveEpicAncestryIdempotentNoop(t *testing.T) {
	ctx := context.Background()
	// Ahead: epic already contains main's tip (main is at base).
	repo := t.TempDir()
	runAssignmentTestGit(t, repo, "init", "-b", "main")
	runAssignmentTestGit(t, repo, "config", "user.email", "test@example.com")
	runAssignmentTestGit(t, repo, "config", "user.name", "Oro Test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runAssignmentTestGit(t, repo, "add", "base.txt")
	runAssignmentTestGit(t, repo, "commit", "-m", "base commit")
	runAssignmentTestGit(t, repo, "checkout", "-b", "epic/ahead")
	if err := os.WriteFile(filepath.Join(repo, "epic.txt"), []byte("epic\n"), 0o644); err != nil {
		t.Fatalf("write epic: %v", err)
	}
	runAssignmentTestGit(t, repo, "add", "epic.txt")
	runAssignmentTestGit(t, repo, "commit", "-m", "epic commit")
	runAssignmentTestGit(t, repo, "checkout", "main")
	mgr := NewGitWorktreeManager(repo, "", "", &ExecCommandRunner{})
	oldEpicOID := gitOut(t, repo, "rev-parse", "epic/ahead")

	outcome, sha, err := mgr.preserveEpicAncestry(ctx, "epic/ahead", "main", "preserve")
	if err != nil {
		t.Fatalf("PreserveEpicAncestry: %v", err)
	}
	if outcome != epicPreserveNoop {
		t.Fatalf("outcome = %v, want epicPreserveNoop", outcome)
	}
	if sha != oldEpicOID {
		t.Errorf("noop returned sha %s, want unchanged %s", sha, oldEpicOID)
	}
	if got := gitOut(t, repo, "rev-parse", "epic/ahead"); got != oldEpicOID {
		t.Errorf("noop advanced the epic ref: %s != %s", got, oldEpicOID)
	}
}

func TestPreserveEpicAncestryBadRefIsError(t *testing.T) {
	ctx := context.Background()
	repo, mgr := newPreserveRepo(t, "epic/clean", false)
	oldEpicOID := gitOut(t, repo, "rev-parse", "epic/clean")

	_, _, err := mgr.preserveEpicAncestry(ctx, "epic/clean", "does-not-exist", "preserve")
	if err == nil {
		t.Fatal("PreserveEpicAncestry with a bad target ref returned nil error")
	}
	if got := gitOut(t, repo, "rev-parse", "epic/clean"); got != oldEpicOID {
		t.Errorf("epic ref changed on error path: %s != %s", got, oldEpicOID)
	}
}

// TestFFMergeUsesDeterministicRecovery proves the close-time ff failure path
// recovers the divergence deterministically (no LLM rebase child) when the
// worktree manager supports it.
func TestFFMergeUsesDeterministicRecovery(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	const epicID = "oro-detrecov"
	epicBranch := protocol.EpicBranchPrefix + epicID
	const targetBranch = "epic/detrecov-parent" // non-default => UpdateBranchRef path

	// Real repo: epicBranch and targetBranch diverge on disjoint files.
	repo := t.TempDir()
	runAssignmentTestGit(t, repo, "init", "-b", "main")
	runAssignmentTestGit(t, repo, "config", "user.email", "test@example.com")
	runAssignmentTestGit(t, repo, "config", "user.name", "Oro Test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runAssignmentTestGit(t, repo, "add", "base.txt")
	runAssignmentTestGit(t, repo, "commit", "-m", "base commit")
	// targetBranch adds parent.txt.
	runAssignmentTestGit(t, repo, "checkout", "-b", targetBranch)
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent\n"), 0o644); err != nil {
		t.Fatalf("write parent: %v", err)
	}
	runAssignmentTestGit(t, repo, "add", "parent.txt")
	runAssignmentTestGit(t, repo, "commit", "-m", "parent commit")
	targetOID := gitOut(t, repo, "rev-parse", targetBranch)
	// epicBranch (from base) adds epic.txt.
	runAssignmentTestGit(t, repo, "checkout", "-b", epicBranch, "main")
	if err := os.WriteFile(filepath.Join(repo, "epic.txt"), []byte("epic\n"), 0o644); err != nil {
		t.Fatalf("write epic: %v", err)
	}
	runAssignmentTestGit(t, repo, "add", "epic.txt")
	runAssignmentTestGit(t, repo, "commit", "-m", "epic commit")
	oldEpicOID := gitOut(t, repo, "rev-parse", epicBranch)
	runAssignmentTestGit(t, repo, "checkout", "main")

	d.worktrees = NewGitWorktreeManager(repo, "", "", &ExecCommandRunner{})
	d.repoRoot = repo
	d.cfg.DefaultBranch = "main"

	beadSrc.mu.Lock()
	beadSrc.shown[epicID] = &protocol.BeadDetail{ID: epicID, Title: "Recovery Epic"}
	beadSrc.mu.Unlock()

	if err := d.ffMergeEpicBranch(ctx, epicID, "worker-detrecov", targetBranch); err != nil {
		t.Fatalf("ffMergeEpicBranch after deterministic recovery = %v, want nil", err)
	}

	// No rebase child should have been created.
	beadSrc.mu.Lock()
	for _, call := range beadSrc.created {
		if strings.HasPrefix(call.title, "Rebase ") {
			beadSrc.mu.Unlock()
			t.Fatalf("deterministic recovery still created an LLM rebase child: %q", call.title)
		}
	}
	beadSrc.mu.Unlock()

	// targetBranch must now be advanced and contain both original tips.
	newTargetOID := gitOut(t, repo, "rev-parse", targetBranch)
	if newTargetOID == targetOID {
		t.Fatal("target branch was not advanced by the recovery")
	}
	if !isAncestorGit(t, repo, targetOID, newTargetOID) {
		t.Error("original target tip is not an ancestor after recovery")
	}
	if !isAncestorGit(t, repo, oldEpicOID, newTargetOID) {
		t.Error("original epic tip is not an ancestor after recovery")
	}
}
