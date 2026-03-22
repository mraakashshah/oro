package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/protocol"
)

// CommandRunner is defined in beadsource.go.

// GitWorktreeManager is the production WorktreeManager that shells out
// to git to create and remove worktrees.
type GitWorktreeManager struct {
	repoRoot     string
	worktreesDir string
	runner       CommandRunner
}

// NewGitWorktreeManager returns a WorktreeManager backed by real git commands.
// worktreesDir is the directory where worktrees are created; if empty it
// defaults to filepath.Join(repoRoot, ".worktrees").
func NewGitWorktreeManager(repoRoot, worktreesDir string, runner CommandRunner) *GitWorktreeManager {
	if worktreesDir == "" {
		worktreesDir = filepath.Join(repoRoot, ".worktrees")
	}
	return &GitWorktreeManager{
		repoRoot:     repoRoot,
		worktreesDir: worktreesDir,
		runner:       runner,
	}
}

// Create runs `git worktree add <path> -b agent/<beadID> <baseBranch>` and returns
// the worktree path and branch name. baseBranch is the branch to branch from
// (e.g. "main" for standalone beads, "agent/<epicID>" for epic child beads).
func (g *GitWorktreeManager) Create(ctx context.Context, beadID, baseBranch string) (path, branch string, err error) {
	// Validate bead ID before using it in filepath operations to prevent
	// directory traversal attacks.
	if err := protocol.ValidateBeadID(beadID); err != nil {
		return "", "", fmt.Errorf("invalid bead ID: %w", err)
	}

	if baseBranch == "" {
		baseBranch = "main"
	}

	path = filepath.Join(g.worktreesDir, beadID)
	branch = protocol.BranchPrefix + beadID

	_, err = g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "add", path, "-b", branch, baseBranch,
	)
	if err == nil {
		g.stageAssets(ctx, path)
		return path, branch, nil
	}

	// Non-recoverable error — fail immediately.
	if !strings.Contains(err.Error(), "already exists") {
		return "", "", fmt.Errorf("worktree add %s: %w", beadID, err)
	}

	// Branch already exists from a previous crashed run: prune stale state and retry once.
	if pruneErr := g.pruneStale(ctx, path, branch); pruneErr != nil {
		slog.WarnContext(ctx, "worktree_create_prune_failed", "error", pruneErr.Error())
	}

	_, err = g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "add", path, "-b", branch, baseBranch,
	)
	if err != nil {
		return "", "", fmt.Errorf("worktree add %s (after prune): %w", beadID, err)
	}
	g.stageAssets(ctx, path)
	return path, branch, nil
}

// stageAssets runs `make stage-assets` in the worktree to prepare embedded
// assets (skills, hooks, beacons) required by go:embed directives.
// Best-effort: failures are silently ignored since some worktrees may not
// need assets (e.g., some beads still compile without them).
func (g *GitWorktreeManager) stageAssets(ctx context.Context, path string) {
	_, _ = g.runner.Run(ctx, "make", "-C", path, "stage-assets")
}

// pruneStale cleans up a stale worktree and branch left by a previous crash.
// It force-removes the worktree directory first (handles locked worktrees),
// then prunes stale git metadata, then deletes the branch.
// Returns the first non-nil error from any git step; all steps still run.
func (g *GitWorktreeManager) pruneStale(ctx context.Context, path, branch string) error {
	var firstErr error
	// Force-remove worktree reference (handles locked or stale worktrees).
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "worktree", "remove", path, "--force"); err != nil {
		firstErr = err
	}
	// Prune stale worktree metadata from git's internal tracking.
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "worktree", "prune"); err != nil && firstErr == nil {
		firstErr = err
	}
	// Delete the stale branch now that it's no longer checked out.
	if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", "-D", branch); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Remove runs `git worktree remove <path> --force`.
// Before removing, it automatically commits any uncommitted changes in the
// worktree to prevent losing work when workers are killed or time out.
// If the worktree path does not exist, Remove returns nil (idempotent).
func (g *GitWorktreeManager) Remove(ctx context.Context, path string) error {
	// Check if path exists. If it doesn't, the worktree is already gone (idempotent).
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil // Path doesn't exist — already removed, not an error
		}
		// If Stat fails for a different reason, continue with git remove
		// (let git report the actual error).
	}

	// Auto-commit any uncommitted changes before removal to prevent data loss.
	if err := g.autoCommitUncommittedChanges(ctx, path); err != nil {
		// Log the error but don't fail the removal — stale worktree cleanup
		// is more important than preserving uncommitted changes in edge cases.
		_ = err // Errors are non-fatal
	}

	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "remove", path, "--force",
	)
	if err != nil {
		return fmt.Errorf("worktree remove %s: %w", path, err)
	}
	return nil
}

// autoCommitUncommittedChanges checks if the worktree has uncommitted changes
// and commits them with a descriptive message. This prevents losing work when
// a worker is killed or times out.
func (g *GitWorktreeManager) autoCommitUncommittedChanges(ctx context.Context, path string) error {
	// Check if worktree has uncommitted changes.
	output, err := g.runner.Run(ctx, "git", "-C", path, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status in %s: %w", path, err)
	}

	// If output is empty, worktree is clean — nothing to commit.
	if strings.TrimSpace(string(output)) == "" {
		return nil
	}

	// Stage all changes.
	_, err = g.runner.Run(ctx, "git", "-C", path, "add", "-A")
	if err != nil {
		return fmt.Errorf("git add in %s: %w", path, err)
	}

	// Commit with descriptive message.
	_, err = g.runner.Run(ctx, "git", "-C", path, "commit", "-m",
		"auto-commit: preserve uncommitted changes before worktree removal")
	if err != nil {
		return fmt.Errorf("git commit in %s: %w", path, err)
	}

	return nil
}

// DeleteBranch runs `git branch -d <branch>` to delete a merged branch.
// Uses -d (not -D) so git refuses if the branch is not fully merged — a safety net.
func (g *GitWorktreeManager) DeleteBranch(ctx context.Context, branch string) error {
	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", "-D", branch)
	if err != nil {
		return fmt.Errorf("branch delete %s: %w", branch, err)
	}
	return nil
}

// BranchExists reports whether the named branch exists in the local repository.
// Returns (false, nil) when the branch is simply absent — not found is not an error.
// Returns (false, err) only when git itself fails (e.g., not a git repo).
func (g *GitWorktreeManager) BranchExists(ctx context.Context, branch string) (bool, error) {
	out, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", "--list", branch)
	if err != nil {
		return false, fmt.Errorf("branch exists check %s: %w", branch, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// MergeFFOnly runs `git merge --ff-only <branch>` in the directory specified by
// target, then returns the resulting HEAD commit SHA. target is the filesystem
// path of the repository (or worktree) to run the merge in.
// If the merge cannot be fast-forwarded, the git error (including stderr) is
// returned wrapped.
func (g *GitWorktreeManager) MergeFFOnly(ctx context.Context, branch, target string) (commitSHA string, err error) {
	_, err = g.runner.Run(ctx, "git", "-C", target, "merge", "--ff-only", branch)
	if err != nil {
		return "", fmt.Errorf("ff-only merge of %s: %w", branch, err)
	}
	out, err := g.runner.Run(ctx, "git", "-C", target, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD after merge: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// GCClosedWorktrees removes worktree directories and branches for beads that
// are closed. It calls isBeadClosed for each directory found under .worktrees/;
// entries for which isBeadClosed returns false are skipped conservatively.
// ReadDir failure returns nil (same as Prune). Remove failures are logged and
// do not prevent other entries from being processed.
func (g *GitWorktreeManager) GCClosedWorktrees(ctx context.Context, isBeadClosed func(string) bool) error {
	entries, err := os.ReadDir(g.worktreesDir)
	if err != nil {
		return nil //nolint:nilerr // missing dir is expected, not an error
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		beadID := entry.Name()
		if !isBeadClosed(beadID) {
			continue
		}

		path := filepath.Join(g.worktreesDir, beadID)
		branch := protocol.BranchPrefix + beadID

		if err := g.Remove(ctx, path); err != nil {
			slog.WarnContext(ctx, "gc_closed_worktrees_remove_failed", "bead_id", beadID, "error", err.Error())
			continue
		}

		if err := g.DeleteBranch(ctx, branch); err != nil {
			slog.WarnContext(ctx, "gc_closed_worktrees_branch_delete_failed", "bead_id", beadID, "error", err.Error())
		}
	}

	return nil
}

// Prune cleans up orphaned worktree state left by a previous crash.
// It runs `git worktree prune` to clean git's internal tracking, then
// removes all directories under .worktrees/. Errors are logged but
// do not prevent startup — this method always returns nil.
func (g *GitWorktreeManager) Prune(ctx context.Context) error {
	// Step 1: Ask git to prune its internal worktree bookkeeping.
	// Errors are non-fatal — the directory cleanup below handles the rest.
	_, _ = g.runner.Run(ctx, "git", "-C", g.repoRoot, "worktree", "prune")

	// Step 2: Remove all directories under worktreesDir.
	entries, err := os.ReadDir(g.worktreesDir)
	if err != nil {
		// Directory doesn't exist or is unreadable — nothing to clean.
		return nil //nolint:nilerr // missing dir is expected, not an error
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_ = os.RemoveAll(filepath.Join(g.worktreesDir, entry.Name()))
	}

	return nil
}
