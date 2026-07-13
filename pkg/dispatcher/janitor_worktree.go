package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const janitorDetectScriptPath = "scripts/janitor_detect.sh"

// withScanWorktree creates an isolated checkout of DefaultBranch for a janitor
// scan, materializes the project-owned detector script when present, invokes
// fn, and removes the worktree even if the detector reports an error.
func (d *Dispatcher) withScanWorktree(ctx context.Context, fn func(path string) error) error {
	worktreeID := d.epicQGWorktreeID("janitor-scan")
	path, _, err := d.worktrees.Create(ctx, worktreeID, d.cfg.DefaultBranch)
	if err != nil {
		_ = d.logEvent(ctx, "janitor_scan_worktree_failed", "dispatcher", "", "", err.Error())
		return fmt.Errorf("create janitor scan worktree: %w", err)
	}
	defer func() { _ = d.worktrees.Remove(context.Background(), path) }()

	if err := d.copyJanitorDetectSnapshot(path); err != nil {
		_ = d.logEvent(ctx, "janitor_scan_detect_snapshot_failed", "dispatcher", "", "", err.Error())
		return err
	}
	if err := fn(path); err != nil {
		return fmt.Errorf("run janitor scan: %w", err)
	}
	return nil
}

func (d *Dispatcher) copyJanitorDetectSnapshot(worktreePath string) error {
	source := filepath.Join(d.repoRoot, janitorDetectScriptPath)
	if _, err := os.Stat(source); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat janitor detector script: %w", err)
	}

	destination := filepath.Join(worktreePath, janitorDetectScriptPath)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create janitor detector directory: %w", err)
	}
	if err := copyQualityGateSnapshot(source, destination); err != nil {
		return fmt.Errorf("copy janitor detector snapshot: %w", err)
	}
	return nil
}
