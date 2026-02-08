package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"oro/internal/runners"
	"oro/pkg/dispatcher"
	"oro/pkg/merge"
	"oro/pkg/ops"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

// newStartCmd creates the "oro start" subcommand.
func newStartCmd() *cobra.Command {
	var (
		workers    int
		daemonOnly bool
		model      string
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Launch the Oro swarm (tmux session + dispatcher)",
		Long:  "Creates a tmux session with the full Oro layout and begins autonomous execution.\nStarts the dispatcher daemon and worker pool in the background.",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath, err := oroPath("ORO_PID_PATH", "oro.pid")
			if err != nil {
				return err
			}

			// Check if already running.
			status, pid, err := DaemonStatus(pidPath)
			if err != nil {
				return err
			}

			switch status {
			case StatusRunning:
				fmt.Fprintf(cmd.OutOrStdout(), "dispatcher already running (PID %d)\n", pid)
				return nil
			case StatusStale:
				// Clean up stale PID file before starting fresh.
				_ = RemovePIDFile(pidPath)
			case StatusStopped:
				// Good to go.
			}

			if daemonOnly {
				return runDaemonOnly(cmd, pidPath, workers)
			}

			// TODO: full start — spawn daemon subprocess, create tmux session.
			_ = model
			fmt.Fprintf(cmd.OutOrStdout(), "starting oro swarm (workers=%d, model=%s)\n", workers, model)
			return nil
		},
	}

	cmd.Flags().IntVarP(&workers, "workers", "w", 2, "number of workers to spawn")
	cmd.Flags().BoolVarP(&daemonOnly, "daemon-only", "d", false, "start dispatcher without tmux/sessions (for CI or testing)")
	cmd.Flags().StringVar(&model, "model", "sonnet", "model for manager session")

	return cmd
}

// runDaemonOnly runs the dispatcher in the foreground (used for testing/CI).
func runDaemonOnly(cmd *cobra.Command, pidPath string, workers int) error {
	fmt.Fprintf(cmd.OutOrStdout(), "starting dispatcher (PID %d, workers=%d)\n", os.Getpid(), workers)
	if err := WritePIDFile(pidPath, os.Getpid()); err != nil {
		return err
	}

	ctx := cmd.Context()
	shutdownCtx, cleanup := SetupSignalHandler(ctx, pidPath)
	defer cleanup()

	d, db, err := buildDispatcher(workers)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := d.Run(shutdownCtx); err != nil {
		return fmt.Errorf("dispatcher: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "dispatcher stopped")
	return nil
}

// oroDir returns the resolved path, respecting ORO_*_PATH env overrides.
func oroPath(envKey, defaultSuffix string) (string, error) {
	if v := os.Getenv(envKey); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".oro", defaultSuffix), nil
}

// buildDispatcher constructs a Dispatcher with all production dependencies.
// The caller owns the returned *sql.DB and must close it.
func buildDispatcher(maxWorkers int) (*dispatcher.Dispatcher, *sql.DB, error) {
	sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
	if err != nil {
		return nil, nil, err
	}
	dbPath, err := oroPath("ORO_DB_PATH", "state.db")
	if err != nil {
		return nil, nil, err
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
	}

	// Get repo root for worktree manager.
	repoRoot, err := os.Getwd()
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("get working dir: %w", err)
	}

	runner := &runners.ExecCommandRunner{}
	beadSrc := dispatcher.NewCLIBeadSource(runner)
	wtMgr := dispatcher.NewGitWorktreeManager(repoRoot, runner)
	esc := dispatcher.NewTmuxEscalator("", "", runner) // defaults: session "oro", pane "oro:0.1"

	merger := merge.NewCoordinator(&runners.ExecGitRunner{})
	opsSpawner := ops.NewSpawner(&runners.ClaudeOpsSpawner{})

	cfg := dispatcher.Config{
		SocketPath: sockPath,
		MaxWorkers: maxWorkers,
		DBPath:     dbPath,
	}

	d := dispatcher.New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc)
	return d, db, nil
}
