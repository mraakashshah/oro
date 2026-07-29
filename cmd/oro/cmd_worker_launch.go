package main

import (
	"context"
	"encoding/json"
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

type workerLaunchReservation struct {
	WorkerIDs []string `json:"worker_ids"`
}

// ExecWorkerSpawner spawns a real worker subprocess running `oro worker --socket <path> --id <id>`.
// The child is placed in its own session (Setsid: true) so it survives parent exit.
type ExecWorkerSpawner struct{}

// SpawnWorker forks a child process running the current binary as a worker.
// stdout/stderr are redirected to logPath. The child is detached via Setsid.
func (e *ExecWorkerSpawner) SpawnWorker(socketPath, workerID, logPath string) error {
	self, err := trustedSelfExecutable()
	if err != nil {
		return err
	}
	child := exec.Command(self, "worker", "--socket", socketPath, "--id", workerID) //nolint:gosec,noctx // intentionally re-executing self; no context — worker must outlive parent

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
	if count < 1 {
		return fmt.Errorf("--count must be at least 1, got %d", count)
	}
	if _, err := ensureRuntimeProjectEnv(currentRepoRoot()); err != nil {
		return fmt.Errorf("resolve runtime identity: %w", err)
	}

	paths, err := ResolveDaemonPaths()
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
	ts := time.Now().UnixNano()
	ids := buildWorkerLaunchIDs(count, workerID, ts)
	if err := reserveWorkerLaunch(sockPath, ids); err != nil {
		return err
	}

	for i, id := range ids {
		logDir := filepath.Join(paths.OroHome, "workers", id)
		logPath := filepath.Join(logDir, "output.log")

		if err := spawner.SpawnWorker(sockPath, id, logPath); err != nil {
			releaseWorkerLaunchReservation(sockPath, ids[i:])
			return fmt.Errorf("spawn worker %s: %w", id, err)
		}
	}
	return nil
}

func buildWorkerLaunchIDs(count int, workerID string, ts int64) []string {
	ids := make([]string, 0, count)
	for i := range count {
		id := workerID
		if id == "" {
			id = fmt.Sprintf("ext-%d-%d", ts, i)
		} else if count > 1 {
			id = fmt.Sprintf("%s-%d", workerID, i)
		}
		ids = append(ids, id)
	}
	return ids
}

func reserveWorkerLaunch(sockPath string, ids []string) error {
	return sendWorkerLaunchDirective(sockPath, "launch-workers", ids)
}

func releaseWorkerLaunchReservation(sockPath string, ids []string) {
	_ = sendWorkerLaunchDirective(sockPath, "cancel-worker-launch", ids)
}

func sendWorkerLaunchDirective(sockPath, op string, ids []string) error {
	payload, err := json.Marshal(workerLaunchReservation{WorkerIDs: ids})
	if err != nil {
		return fmt.Errorf("marshal worker launch reservation: %w", err)
	}
	conn, err := dialDispatcher(context.Background(), sockPath)
	if err != nil {
		return fmt.Errorf("dial dispatcher: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := sendDirective(conn, op, string(payload)); err != nil {
		return fmt.Errorf("send %s directive: %w", op, err)
	}
	if _, err := readACK(conn); err != nil {
		return fmt.Errorf("%s ack: %w", op, err)
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
