package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// runPreflightChecks verifies that all required external tools are available
// and that the git repository is in a good state for oro to operate.
// Returns an error with an actionable message if any check fails.
func runPreflightChecks() error {
	requiredTools := []string{"tmux", "claude", "bd", "git"}

	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required tool '%s' not found in PATH - please install it before running oro start", tool)
		}
	}

	// Check git repo status - verify we can run git commands.
	// A more sophisticated check could verify the repo is clean,
	// but for now we just verify git works.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("not in a git repository or git is not functioning properly: %w", err)
	}

	return nil
}
