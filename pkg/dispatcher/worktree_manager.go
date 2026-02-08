package dispatcher

import (
	"context"
	"fmt"
	"path/filepath"
)

// CommandRunner abstracts command execution for testability.
// The same pattern used by merge.GitRunner, scoped to this package.
type CommandRunner interface {
	Run(ctx context.Context, dir string, args ...string) (stdout string, stderr string, err error)
}

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

	_, _, err = g.runner.Run(ctx, g.repoRoot,
		"worktree", "add", path, "-b", branch, "main",
	)
	if err != nil {
		return "", "", fmt.Errorf("worktree add %s: %w", beadID, err)
	}

	return path, branch, nil
}

// Remove runs `git worktree remove <path> --force`.
func (g *GitWorktreeManager) Remove(ctx context.Context, path string) error {
	_, _, err := g.runner.Run(ctx, g.repoRoot,
		"worktree", "remove", path, "--force",
	)
	if err != nil {
		return fmt.Errorf("worktree remove %s: %w", path, err)
	}
	return nil
}
