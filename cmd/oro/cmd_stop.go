package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// stopConfig holds injectable dependencies for the graceful shutdown sequence.
type stopConfig struct {
	pidPath  string
	sockPath string
	tmuxName string
	runner   CmdRunner
	w        io.Writer
	stdin    io.Reader       // stdin for interactive confirmation
	signalFn func(int) error // sends SIGINT; injectable for testing
	aliveFn  func(int) bool  // checks process liveness; injectable for testing
	killFn   func(int) error // sends SIGKILL; injectable for testing
	isTTY    func() bool     // returns true if stdin is a TTY; injectable for testing
	force    bool            // --force flag: skip interactive confirmation
	oroHome  string          // base directory for daemon discovery
	beadsDir string          // directory containing .beads; used to flush dolt working set on stop
}

// projectDaemon describes a running daemon discovered in a project directory.
type projectDaemon struct {
	Project string // project name or "(global)" for legacy
	PID     int
	PIDPath string
}

// discoverProjectDaemons scans oroHome/projects/*/oro.pid for running daemons.
// Also checks the legacy global oroHome/oro.pid.
func discoverProjectDaemons(oroHome string) []projectDaemon {
	var daemons []projectDaemon

	// Check legacy global PID file.
	globalPID := filepath.Join(oroHome, "oro.pid")
	if pid, err := ReadPIDFile(globalPID); err == nil && IsProcessAlive(pid) {
		daemons = append(daemons, projectDaemon{
			Project: "(global)",
			PID:     pid,
			PIDPath: globalPID,
		})
	}

	// Scan per-project PID files.
	projectsDir := filepath.Join(oroHome, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return daemons // projects dir doesn't exist yet
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pidPath := filepath.Join(projectsDir, e.Name(), "oro.pid")
		pid, err := ReadPIDFile(pidPath)
		if err != nil {
			continue
		}
		if !IsProcessAlive(pid) {
			continue
		}
		daemons = append(daemons, projectDaemon{
			Project: e.Name(),
			PID:     pid,
			PIDPath: pidPath,
		})
	}

	return daemons
}

// drainTimeout is how long to wait for the dispatcher to exit after SIGTERM.
const drainTimeout = 30 * time.Second

// drainPollInterval is how often to check if the dispatcher has exited.
const drainPollInterval = 200 * time.Millisecond

// isStdinTTY returns true if os.Stdin is connected to a terminal.
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// newStopCmd creates the "oro stop" subcommand.
func newStopCmd() *cobra.Command {
	var (
		force bool
		all   bool
	)
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Graceful shutdown of the Oro swarm",
		Long: `Sends a stop directive to the dispatcher, waits for workers to finish,
and kills the tmux session.

Use --all to stop daemons in all projects simultaneously.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			daemonPaths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve daemon paths: %w", err)
			}

			if all {
				return runStopAll(cmd.Context(), daemonPaths.OroHome, force, cmd.OutOrStdout())
			}

			repoRoot, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working dir: %w", err)
			}
			projPaths, err := ResolvePaths(repoRoot)
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}

			cfg := &stopConfig{
				pidPath:  daemonPaths.PIDPath,
				sockPath: daemonPaths.SocketPath,
				tmuxName: TmuxSessionName(readProjectNameCWD()),
				runner:   &ExecRunner{},
				w:        cmd.OutOrStdout(),
				stdin:    os.Stdin,
				signalFn: defaultSignalINT,
				aliveFn:  IsProcessAlive,
				killFn:   defaultKill,
				isTTY:    isStdinTTY,
				force:    force,
				oroHome:  daemonPaths.OroHome,
				beadsDir: projPaths.BeadsDir,
			}

			return runStopSequence(cmd.Context(), cfg)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip interactive confirmation (requires ORO_HUMAN_CONFIRMED=1)")
	cmd.Flags().BoolVar(&all, "all", false, "stop daemons in all projects")
	return cmd
}

// suggestStopAll prints a hint about other running daemons when the current
// project's daemon is not running.
func suggestStopAll(w io.Writer, oroHome string) {
	if oroHome == "" {
		return
	}
	others := discoverProjectDaemons(oroHome)
	if len(others) == 0 {
		return
	}
	fmt.Fprintf(w, "\nFound %d running daemon(s) in other projects:\n", len(others))
	for _, d := range others {
		fmt.Fprintf(w, "  - %s (PID %d)\n", d.Project, d.PID)
	}
	fmt.Fprintln(w, "\nUse 'oro stop --all' to stop all daemons.")
}

// runStopAll discovers and stops all running project daemons.
// Dolt server is intentionally NOT stopped — it persists across sessions so
// standalone bd commands continue to work.
func runStopAll(ctx context.Context, oroHome string, force bool, w io.Writer) error {
	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) == 0 {
		fmt.Fprintln(w, "no running daemons found")
		return nil
	}

	fmt.Fprintf(w, "found %d running daemon(s):\n", len(daemons))
	for _, d := range daemons {
		fmt.Fprintf(w, "  - %s (PID %d)\n", d.Project, d.PID)
	}

	for _, d := range daemons {
		sockPath := strings.TrimSuffix(d.PIDPath, "oro.pid") + "oro.sock"

		// Read project.root from the project dir to derive beadsDir.
		// Graceful degradation: if project.root is missing, skip dolt cleanup.
		var beadsDir string
		projectRootFile := filepath.Join(filepath.Dir(d.PIDPath), "project.root")
		rootBytes, readErr := os.ReadFile(projectRootFile) //nolint:gosec // path derived from trusted oroHome
		if readErr != nil {
			fmt.Fprintf(w, "warning: cannot read project.root for %s, skipping dolt cleanup\n", d.Project)
		} else {
			rootPath := strings.TrimSpace(string(rootBytes))
			projPaths, pathErr := ResolvePaths(rootPath)
			if pathErr != nil {
				beadsDir = "" // skip dolt cleanup if paths can't be resolved
			} else {
				beadsDir = projPaths.BeadsDir
			}
		}

		cfg := &stopConfig{
			pidPath:  d.PIDPath,
			sockPath: sockPath,
			tmuxName: TmuxSessionName(d.Project),
			runner:   &ExecRunner{},
			w:        w,
			stdin:    os.Stdin,
			signalFn: defaultSignalINT,
			aliveFn:  IsProcessAlive,
			killFn:   defaultKill,
			isTTY:    isStdinTTY,
			force:    force,
			beadsDir: beadsDir,
		}

		fmt.Fprintf(w, "\nstopping %s (PID %d)...\n", d.Project, d.PID)
		if err := runStopSequence(ctx, cfg); err != nil {
			fmt.Fprintf(w, "warning: failed to stop %s: %v\n", d.Project, err)
		}
	}
	return nil
}

// defaultSignalINT sends SIGINT to the given PID.
// SIGINT is always honored by the daemon (like Ctrl+C), unlike SIGTERM which
// requires prior authorization via shutdown directive. This avoids the UDS
// directive path which agents can abuse.
func defaultSignalINT(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil {
		return fmt.Errorf("send SIGINT to PID %d: %w", pid, err)
	}
	return nil
}

// defaultKill sends SIGKILL to the given PID.
func defaultKill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("send SIGKILL to PID %d: %w", pid, err)
	}
	return nil
}

// confirmStop checks that the caller is authorized to stop the dispatcher.
// In interactive mode, it prompts for "YES" on stdin.
// With --force, it requires ORO_HUMAN_CONFIRMED=1.
// Returns an error if confirmation fails.
func confirmStop(cfg *stopConfig) error {
	if cfg.force {
		if os.Getenv("ORO_HUMAN_CONFIRMED") != "1" {
			return fmt.Errorf("--force requires ORO_HUMAN_CONFIRMED=1 environment variable")
		}
		return nil
	}

	if cfg.isTTY != nil && !cfg.isTTY() {
		return fmt.Errorf("oro stop requires an interactive terminal (stdin is not a TTY)\n" +
			"Hint: use --force with ORO_HUMAN_CONFIRMED=1 for non-interactive use")
	}

	fmt.Fprint(cfg.w, "Type YES to confirm shutdown: ")
	scanner := bufio.NewScanner(cfg.stdin)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read confirmation from stdin")
	}
	if strings.TrimSpace(scanner.Text()) != "YES" {
		return fmt.Errorf("shutdown aborted (expected YES)")
	}
	return nil
}

// runStopSequence performs the full graceful shutdown:
//  0. Confirm the caller is authorized (interactive TTY or --force)
//  1. Send SIGINT to the dispatcher (always honored, triggers graceful drain)
//  2. Wait for the dispatcher process to exit
//  3. If process won't exit: SIGKILL as emergency fallback
//  4. Flush dolt working set (bd dolt commit; non-fatal)
//  5. Clean up pane-died hooks
//  6. Kill the tmux session
//  7. Remove PID file
func runStopSequence(ctx context.Context, cfg *stopConfig) error {
	status, pid, err := DaemonStatus(cfg.pidPath, cfg.sockPath)
	if err != nil {
		return fmt.Errorf("get daemon status: %w", err)
	}

	switch status {
	case StatusStopped:
		fmt.Fprintln(cfg.w, "dispatcher is not running")
		suggestStopAll(cfg.w, cfg.oroHome)
		return nil
	case StatusStale:
		fmt.Fprintln(cfg.w, "removing stale PID file (process already dead)")
		_ = os.Remove(cfg.sockPath)
		return RemovePIDFile(cfg.pidPath)
	}

	// 0. Confirm authorization before proceeding.
	if err := confirmStop(cfg); err != nil {
		return err
	}

	// 1. Send SIGINT (always honored by daemon, like Ctrl+C).
	fmt.Fprintf(cfg.w, "sending SIGINT to dispatcher (PID %d)\n", pid)
	if err := cfg.signalFn(pid); err != nil {
		fmt.Fprintf(cfg.w, "warning: SIGINT failed: %v\n", err)
	}

	// 2. Wait for the dispatcher to exit.
	fmt.Fprintln(cfg.w, "waiting for dispatcher to drain and exit...")
	if err := waitForExit(ctx, pid, cfg.aliveFn); err != nil {
		fmt.Fprintf(cfg.w, "warning: %v\n", err)
		// 3. Emergency fallback: SIGKILL if process won't exit.
		if cfg.killFn != nil {
			fmt.Fprintf(cfg.w, "sending SIGKILL to dispatcher (PID %d)\n", pid)
			if killErr := cfg.killFn(pid); killErr != nil {
				fmt.Fprintf(cfg.w, "warning: SIGKILL failed: %v\n", killErr)
			}
		}
	}

	// 4. Flush dolt working set (non-fatal: log warning and continue on failure).
	if cfg.beadsDir != "" {
		if _, err := cfg.runner.Run("bd", "dolt", "commit"); err != nil {
			fmt.Fprintf(cfg.w, "warning: dolt flush: %v\n", err)
		} else {
			fmt.Fprintln(cfg.w, "dolt: working set committed")
		}
	}

	// 5. Clean up pane-died hooks before killing the tmux session.
	tmux := &TmuxSession{Name: cfg.tmuxName, Runner: cfg.runner}
	_ = tmux.CleanupPaneDiedHooks() // Best effort; non-fatal if hooks weren't registered

	// 6. Kill the tmux session.
	if err := tmux.Kill(); err != nil {
		fmt.Fprintf(cfg.w, "warning: tmux kill: %v\n", err)
	}

	// 7. Remove PID file (belt and suspenders — signal handler may have already done it).
	_ = RemovePIDFile(cfg.pidPath)

	// Note: dolt server is intentionally NOT stopped here. Dolt persists
	// across sessions so standalone bd commands continue to work.

	fmt.Fprintln(cfg.w, "shutdown complete")
	return nil
}

// waitForExit polls until the process is no longer alive or timeout.
func waitForExit(ctx context.Context, pid int, aliveFn func(int) bool) error {
	if !aliveFn(pid) {
		return nil
	}

	deadline := time.After(drainTimeout)
	ticker := time.NewTicker(drainPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !aliveFn(pid) {
				return nil
			}
		case <-deadline:
			return fmt.Errorf("timeout waiting for dispatcher (PID %d) to exit", pid)
		case <-ctx.Done():
			return fmt.Errorf("wait for dispatcher exit: %w", ctx.Err())
		}
	}
}
