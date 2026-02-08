package dispatcher

import (
	"context"
	"fmt"
	"path/filepath"
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
	path = filepath.Join(g.repoRoot, ".worktrees", beadID)
	branch = "agent/" + beadID

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
