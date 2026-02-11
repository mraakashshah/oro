package dispatcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"oro/pkg/protocol"
)

// CommandRunner is defined in beadsource.go.

// GitWorktreeManager is the production WorktreeManager that shells out
// to git to create and remove worktrees.
type GitWorktreeManager struct {
	repoRoot string
	runner   CommandRunner
}

// NewGitWorktreeManager returns a WorktreeManager backed by real git commands.
func NewGitWorktreeManager(repoRoot string, runner CommandRunner) *GitWorktreeManager {
	return &GitWorktreeManager{
		repoRoot: repoRoot,
		runner:   runner,
	}
}

// Create runs `git worktree add <path> -b agent/<beadID> main` and returns
// the worktree path and branch name.
func (g *GitWorktreeManager) Create(ctx context.Context, beadID string) (path, branch string, err error) {
	// Validate bead ID before using it in filepath operations to prevent
	// directory traversal attacks.
	if err := protocol.ValidateBeadID(beadID); err != nil {
		return "", "", fmt.Errorf("invalid bead ID: %w", err)
	}

	path = filepath.Join(g.repoRoot, protocol.WorktreesDir, beadID)
	branch = protocol.BranchPrefix + beadID

	_, err = g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "add", path, "-b", branch, "main",
	)
	if err != nil {
		return "", "", fmt.Errorf("worktree add %s: %w", beadID, err)
	}

	return path, branch, nil
}

// Remove runs `git worktree remove <path> --force`.
func (g *GitWorktreeManager) Remove(ctx context.Context, path string) error {
	_, err := g.runner.Run(ctx, "git", "-C", g.repoRoot,
		"worktree", "remove", path, "--force",
	)
	if err != nil {
		return fmt.Errorf("worktree remove %s: %w", path, err)
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

	// Step 2: Remove all directories under .worktrees/.
	worktreesDir := filepath.Join(g.repoRoot, protocol.WorktreesDir)
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		// Directory doesn't exist or is unreadable — nothing to clean.
		return nil //nolint:nilerr // missing dir is expected, not an error
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_ = os.RemoveAll(filepath.Join(worktreesDir, entry.Name()))
	}

	return nil
}
