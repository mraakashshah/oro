package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"oro/pkg/dispatcher"
	"oro/pkg/merge"
	"oro/pkg/ops"

	"github.com/spf13/cobra"
)

// DaemonSpawner abstracts spawning the daemon subprocess for testability.
type DaemonSpawner interface {
	SpawnDaemon(pidPath string, workers int) (pid int, err error)
}

// ExecDaemonSpawner spawns a real child process running `oro start --daemon-only`.
type ExecDaemonSpawner struct{}

// SpawnDaemon forks a child process running the current binary with --daemon-only.
func (e *ExecDaemonSpawner) SpawnDaemon(pidPath string, workers int) (int, error) {
	child := exec.CommandContext(context.Background(), os.Args[0], "start", "--daemon-only", "--workers", strconv.Itoa(workers)) //nolint:gosec // intentionally re-executing self
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("spawn daemon: %w", err)
	}
	return child.Process.Pid, nil
}

// socketPollTimeout is the maximum time to wait for the dispatcher socket.
const socketPollTimeout = 5 * time.Second

// socketPollInterval is how often to check for the socket file.
const socketPollInterval = 50 * time.Millisecond

// runFullStart implements the non-daemon start flow:
// 1. Spawn daemon subprocess
// 2. Wait for socket file to appear
// 3. Create tmux session with both beacons
// 4. Print status
func runFullStart(w io.Writer, workers int, model string, spawner DaemonSpawner, tmuxRunner CmdRunner, socketTimeout time.Duration, sleeper func(time.Duration), beaconTimeout time.Duration) error {
	pidPath, err := oroPath("ORO_PID_PATH", "oro.pid")
	if err != nil {
		return err
	}
	sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
	if err != nil {
		return err
	}

	// 1. Spawn the daemon subprocess.
	pid, err := spawner.SpawnDaemon(pidPath, workers)
	if err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}

	// 2. Wait for the dispatcher socket to appear.
	deadline := time.Now().Add(socketTimeout)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(sockPath); statErr == nil {
			break
		}
		time.Sleep(socketPollInterval)
	}
	if _, err := os.Stat(sockPath); err != nil {
		return fmt.Errorf("dispatcher socket not ready at %s: %w", sockPath, err)
	}

	// 2b. Send start directive so dispatcher transitions from Inert to Running.
	if err := sendStartDirective(sockPath); err != nil {
		return fmt.Errorf("send start directive: %w", err)
	}

	// 3. Create tmux session with both beacons (architect + manager).
	sess := &TmuxSession{Name: "oro", Runner: tmuxRunner, Sleeper: sleeper, BeaconTimeout: beaconTimeout}
	if err := sess.Create(ArchitectBeacon(), ManagerBeacon()); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}

	// 4. Print status.
	fmt.Fprintf(w, "oro swarm started (PID %d, workers=%d, model=%s)\n", pid, workers, model)
	return nil
}

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
			// Run preflight checks before attempting to start.
			if err := runPreflightChecks(); err != nil {
				return err
			}

			// Bootstrap ~/.oro/ directory if it doesn't exist.
			oroDir, err := defaultOroDir()
			if err != nil {
				return err
			}
			if err := bootstrapOroDir(oroDir); err != nil {
				return fmt.Errorf("bootstrap oro dir: %w", err)
			}

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

			return runFullStart(cmd.OutOrStdout(), workers, model, &ExecDaemonSpawner{}, &ExecRunner{}, socketPollTimeout, nil, 0)
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

// defaultOroDir returns the default ~/.oro directory path.
func defaultOroDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".oro"), nil
}

// bootstrapOroDir creates the oro state directory with 0700 permissions.
// It is idempotent — calling it on an existing directory is a no-op.
func bootstrapOroDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create oro dir %s: %w", dir, err)
	}
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

	db, err := openDB(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state db: %w", err)
	}

	// Get repo root for worktree manager.
	repoRoot, err := os.Getwd()
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("get working dir: %w", err)
	}

	runner := &dispatcher.ExecCommandRunner{}
	beadSrc := dispatcher.NewCLIBeadSource(runner)
	wtMgr := dispatcher.NewGitWorktreeManager(repoRoot, runner)
	esc := dispatcher.NewTmuxEscalator("", "", runner) // defaults: session "oro", pane "oro:0.1"

	merger := merge.NewCoordinator(&merge.ExecGitRunner{})
	opsSpawner := ops.NewSpawner(&ops.ClaudeOpsSpawner{})

	cfg := dispatcher.Config{
		SocketPath: sockPath,
		MaxWorkers: maxWorkers,
		DBPath:     dbPath,
	}

	d := dispatcher.New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc)
	d.SetProcessManager(dispatcher.NewOroProcessManager(sockPath))
	return d, db, nil
}

// sendStartDirective connects to the dispatcher UDS and sends a "start"
// directive so it transitions from StateInert to StateRunning.
func sendStartDirective(sockPath string) error {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", sockPath)
	if err != nil {
		return fmt.Errorf("connect to dispatcher: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := sendDirective(conn, "start", ""); err != nil {
		return err
	}
	if _, err := readACK(conn); err != nil {
		return err
	}
	return nil
}
