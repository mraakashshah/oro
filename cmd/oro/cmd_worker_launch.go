package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// WorkerSpawner abstracts spawning a worker subprocess for testability.
type WorkerSpawner interface {
	SpawnWorker(socketPath, workerID, logPath string) error
}

// ExecWorkerSpawner spawns a real worker subprocess running `oro worker --socket <path> --id <id>`.
// The child is placed in its own session (Setsid: true) so it survives parent exit.
type ExecWorkerSpawner struct{}

// SpawnWorker forks a child process running the current binary as a worker.
// stdout/stderr are redirected to logPath. The child is detached via Setsid.
func (e *ExecWorkerSpawner) SpawnWorker(socketPath, workerID, logPath string) error {
	child := exec.Command(os.Args[0], "worker", "--socket", socketPath, "--id", workerID) //nolint:gosec,noctx // intentionally re-executing self; no context — worker must outlive parent

	// Ensure log directory exists.
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("create worker log dir: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // log path is deterministic
	if err != nil {
		return fmt.Errorf("open worker log %s: %w", logPath, err)
	}
	child.Stdout = logFile
	child.Stderr = logFile

	child.Env = cleanEnvForDaemon(os.Environ())
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("spawn worker %s: %w", workerID, err)
	}
	// logFile fd is inherited by the child; parent can close its copy.
	_ = logFile.Close()
	return nil
}

// generateWorkerID produces a unique external worker ID in the form ext-<unix-ts>-<i>.
// The index i is used to differentiate workers spawned in the same batch.
func generateWorkerID(i int) string {
	return fmt.Sprintf("ext-%d-%d", time.Now().UnixNano(), i)
}

// newWorkerLaunchCmd creates the "oro worker launch" subcommand.
func newWorkerLaunchCmd() *cobra.Command {
	var (
		count    int
		workerID string
		beadID   string
	)

	cmd := &cobra.Command{
		Use:   "launch",
		Short: "Launch one or more external worker processes",
		Long: `Spawns oro worker processes that connect to the running dispatcher.

The dispatcher must be started first with 'oro dispatcher start'.
Workers are spawned as detached background processes with logs written to
$ORO_HOME/workers/<id>/output.log.

With --bead, sends a spawn-for directive to the dispatcher, which handles
spawning a worker targeted at that specific bead.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorkerLaunch(&ExecWorkerSpawner{}, count, workerID, beadID)
		},
	}

	cmd.Flags().IntVarP(&count, "count", "n", 1, "number of workers to spawn")
	cmd.Flags().StringVar(&workerID, "id", "", "worker ID (optional; auto-generated if not set)")
	cmd.Flags().StringVar(&beadID, "bead", "", "bead ID; sends spawn-for directive instead of direct spawn")

	return cmd
}

// runWorkerLaunch implements the core logic for "oro worker launch".
// It is extracted for testability (spawner is injected).
func runWorkerLaunch(spawner WorkerSpawner, count int, workerID, beadID string) error {
	paths, err := ResolvePaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	sockPath := paths.SocketPath

	// Verify dispatcher is running by checking socket file existence.
	if _, err := os.Stat(sockPath); err != nil {
		return fmt.Errorf("dispatcher not running (socket %s not found); start it with: oro dispatcher start", sockPath)
	}

	// --bead flag: delegate to dispatcher via spawn-for directive.
	if beadID != "" {
		return sendSpawnForDirective(sockPath, beadID)
	}

	// Spawn count workers directly.
	ts := time.Now().UnixNano()
	for i := range count {
		id := workerID
		if id == "" {
			id = fmt.Sprintf("ext-%d-%d", ts, i)
		} else if count > 1 {
			// Multiple workers with explicit base ID: append index.
			id = fmt.Sprintf("%s-%d", workerID, i)
		}

		logDir := filepath.Join(paths.OroHome, "workers", id)
		logPath := filepath.Join(logDir, "output.log")

		if err := spawner.SpawnWorker(sockPath, id, logPath); err != nil {
			return fmt.Errorf("spawn worker %s: %w", id, err)
		}
	}
	return nil
}

// sendSpawnForDirective sends a "spawn-for" directive to the dispatcher, asking
// it to spawn a worker targeted at a specific bead.
func sendSpawnForDirective(sockPath, beadID string) error {
	conn, err := dialDispatcher(context.Background(), sockPath)
	if err != nil {
		return fmt.Errorf("dial dispatcher: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := sendDirective(conn, string(protocol.DirectiveSpawnFor), beadID); err != nil {
		return fmt.Errorf("send spawn-for directive: %w", err)
	}

	if _, err := readACK(conn); err != nil {
		return fmt.Errorf("spawn-for ack: %w", err)
	}
	return nil
}
