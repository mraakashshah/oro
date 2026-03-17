package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// doltCmdConfig holds injectable dependencies for the oro dolt subcommands.
type doltCmdConfig struct {
	oroHome         string
	aliveFn         func(int) bool
	isPortUp        func(int) bool
	startFn         func(string) (int, error) // startSharedDoltServer
	stopFn          func(string) error        // stopDoltServer for oroHome
	force           bool
	dispatcherPIDFn func() int // returns dispatcher PID (0 = not running)
}

// newDoltCmd creates the "oro dolt" parent command with status/start/stop subcommands.
func newDoltCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dolt",
		Short: "Manage the shared Dolt server",
		Long: `Manage the machine-wide shared Dolt server used by beads.

Subcommands:
  status   Show shared server status, PID, port, and databases
  start    Start the shared server (idempotent)
  stop     Stop the shared server (requires --force if dispatcher is running)`,
	}

	cmd.AddCommand(newDoltStatusCmd())
	cmd.AddCommand(newDoltStartCmd())
	cmd.AddCommand(newDoltStopCmd())

	return cmd
}

// ---------- oro dolt status ----------

func newDoltStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show shared Dolt server status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolvePaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			cfg := &doltCmdConfig{
				oroHome:  paths.OroHome,
				aliveFn:  IsProcessAlive,
				isPortUp: isDoltServerRunning,
			}
			return runDoltStatus(cfg, cmd.OutOrStdout())
		},
	}
}

// runDoltStatus prints the shared server status to w.
func runDoltStatus(cfg *doltCmdConfig, w io.Writer) error {
	pidPath := filepath.Join(cfg.oroHome, "dolt-server.pid")
	portPath := filepath.Join(cfg.oroHome, "dolt-server.port")

	pid, port, running := readSharedServerState(cfg, pidPath, portPath)

	if !running {
		fmt.Fprintln(w, "shared dolt server: stopped")
		return nil
	}

	fmt.Fprintf(w, "shared dolt server: running (PID %d, port %d)\n", pid, port)

	// List databases in the data directory.
	doltDir := filepath.Join(cfg.oroHome, "dolt")
	dbs := listDatabases(doltDir)
	if len(dbs) > 0 {
		fmt.Fprintln(w, "databases:")
		for _, db := range dbs {
			fmt.Fprintf(w, "  - %s\n", db)
		}
	}

	return nil
}

// readSharedServerState reads PID and port files, then checks liveness.
// Returns (pid, port, running).
func readSharedServerState(cfg *doltCmdConfig, pidPath, portPath string) (pid, port int, running bool) {
	pidData, err := os.ReadFile(pidPath) //nolint:gosec // oroHome is caller-controlled
	if err != nil {
		return 0, 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return 0, 0, false
	}

	portData, err := os.ReadFile(portPath) //nolint:gosec // oroHome is caller-controlled
	if err != nil {
		return pid, SharedDoltPort, cfg.aliveFn(pid)
	}
	port, err = strconv.Atoi(strings.TrimSpace(string(portData)))
	if err != nil {
		port = SharedDoltPort
	}

	running = cfg.aliveFn(pid)
	return pid, port, running
}

// listDatabases returns directory names under the dolt data directory.
func listDatabases(doltDir string) []string {
	entries, err := os.ReadDir(doltDir)
	if err != nil {
		return nil
	}
	var dbs []string
	for _, e := range entries {
		if e.IsDir() {
			dbs = append(dbs, e.Name())
		}
	}
	return dbs
}

// ---------- oro dolt start ----------

func newDoltStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the shared Dolt server (idempotent)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolvePaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			cfg := &doltCmdConfig{
				oroHome:  paths.OroHome,
				aliveFn:  IsProcessAlive,
				isPortUp: isDoltServerRunning,
				startFn:  startSharedDoltServer,
			}
			return runDoltStart(cfg, cmd.OutOrStdout())
		},
	}
}

// runDoltStart starts the shared server or reports it's already running.
func runDoltStart(cfg *doltCmdConfig, w io.Writer) error {
	pidPath := filepath.Join(cfg.oroHome, "dolt-server.pid")
	portPath := filepath.Join(cfg.oroHome, "dolt-server.port")

	_, _, running := readSharedServerState(cfg, pidPath, portPath)
	if running {
		fmt.Fprintln(w, "shared dolt server already running")
		return nil
	}

	pid, err := cfg.startFn(cfg.oroHome)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("dolt not found in PATH: %w", err)
		}
		return fmt.Errorf("start shared dolt server: %w", err)
	}

	if pid == 0 {
		// Adopted existing server.
		fmt.Fprintln(w, "shared dolt server already running")
		return nil
	}

	fmt.Fprintf(w, "shared dolt server started (PID %d, port %d)\n", pid, SharedDoltPort)
	return nil
}

// ---------- oro dolt stop ----------

func newDoltStopCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the shared Dolt server",
		Long: `Stop the shared Dolt server.

Refuses to stop when the dispatcher is running unless --force is specified,
because running workers depend on the server for beads persistence.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolvePaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			cfg := &doltCmdConfig{
				oroHome:  paths.OroHome,
				aliveFn:  IsProcessAlive,
				isPortUp: isDoltServerRunning,
				force:    force,
				stopFn:   stopDoltServer,
				dispatcherPIDFn: func() int {
					pid, err := ReadPIDFile(paths.PIDPath)
					if err != nil {
						return 0
					}
					if !IsProcessAlive(pid) {
						return 0
					}
					return pid
				},
			}
			return runDoltStop(cfg, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "stop even if the dispatcher is running")
	return cmd
}

// runDoltStop stops the shared server with dispatcher guard.
func runDoltStop(cfg *doltCmdConfig, w io.Writer) error {
	pidPath := filepath.Join(cfg.oroHome, "dolt-server.pid")
	portPath := filepath.Join(cfg.oroHome, "dolt-server.port")

	_, _, running := readSharedServerState(cfg, pidPath, portPath)
	if !running {
		// Also check port directly in case PID file is missing.
		if !cfg.isPortUp(SharedDoltPort) {
			fmt.Fprintln(w, "shared dolt server is not running")
			return nil
		}
	}

	// Guard: refuse if dispatcher is running unless --force.
	if cfg.dispatcherPIDFn != nil {
		dispPID := cfg.dispatcherPIDFn()
		if dispPID > 0 && !cfg.force {
			return fmt.Errorf("dispatcher is running (PID %d); use --force to stop the dolt server anyway", dispPID)
		}
		if dispPID > 0 {
			fmt.Fprintf(w, "warning: dispatcher is running (PID %d), stopping dolt server anyway (--force)\n", dispPID)
		}
	}

	if err := cfg.stopFn(cfg.oroHome); err != nil {
		return fmt.Errorf("stop shared dolt server: %w", err)
	}

	fmt.Fprintln(w, "shared dolt server stopped")
	return nil
}
