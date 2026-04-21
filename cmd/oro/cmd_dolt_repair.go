package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	repairExitNoOwner   = 2
	repairExitStillBad  = 3
	repairExitNoDB      = 4
	repairExitContended = 5

	repairKillGrace       = 5 * time.Second
	repairPortWaitTimeout = 10 * time.Second
	repairLockFilename    = "dolt-repair.lock"
)

// ErrFlockContended is returned by acquireSpawnLock when another repair
// process already holds the exclusive lock.
var ErrFlockContended = errors.New("repair lock already held by another process")

// doltRepairDeps holds injectable dependencies for runDoltRepair.
type doltRepairDeps struct {
	oroHome     string
	lockFn      func(path string) (func() error, error)
	probeFn     func() (int, string, error)          // returns (pid, dataDir, err)
	ownerFn     func(pid int) (int, error)           // returns UID of process owner
	currentUID  int                                  // os.Getuid() for production
	killFn      func(pid int) error                  // SIGTERM + grace period + SIGKILL
	kickstartFn func() bool                          // launchctl kickstart
	waitPortFn  func(port int, d time.Duration) bool // poll until port is up
	dbPresentFn func() bool                          // true if dolt DB dirs exist
	dryRun      bool
}

func newDoltRepairCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Detect and repair a rogue shared Dolt server",
		Long: `Detect a rogue shared Dolt server (one started with the wrong --data-dir)
and repair it: SIGTERM the rogue process, then trigger a launchctl kickstart
so the managed service takes over with the correct data directory.

Exit codes:
  0  server healthy (correct data-dir and database present)
  2  cannot identify process owner (no PID file, lsof unavailable, or UID mismatch)
  3  repair was attempted but the server is still unhealthy afterwards
  4  data-dir is correct but no database found (run 'oro dolt setup' to migrate)
  5  another repair is already running (flock contended)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			oroHome := paths.OroHome
			deps := doltRepairDeps{
				oroHome: oroHome,
				lockFn:  acquireSpawnLock,
				probeFn: func() (int, string, error) {
					return runProcessProbe(oroHome)
				},
				ownerFn:     getProcessUID,
				currentUID:  os.Getuid(),
				killFn:      func(pid int) error { return killProcessWithGrace(pid, repairKillGrace) },
				kickstartFn: tryLaunchctlKickstart,
				waitPortFn:  waitForPort,
				dbPresentFn: func() bool { return doltDatabasePresent(oroHome) },
				dryRun:      dryRun,
			}
			err = runDoltRepair(deps, cmd.OutOrStdout())
			var ee *exitError
			if errors.As(err, &ee) {
				os.Exit(ee.code) //nolint:gocritic // intentional early exit; deferred cleanup runs before this via defer in RunE
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report state without acting")
	return cmd
}

// acquireSpawnLock opens (or creates) the repair lock file and acquires an
// exclusive non-blocking flock. Returns ErrFlockContended if another process
// holds the lock. The returned function releases the lock and closes the file.
func acquireSpawnLock(lockPath string) (func() error, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // lockPath is caller-controlled from oroHome
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrFlockContended
		}
		return nil, fmt.Errorf("acquire flock: %w", err)
	}
	return func() error {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // fd from trusted f
		return f.Close()
	}, nil
}

// getProcessUID returns the UID of the process with the given PID using ps.
func getProcessUID(pid int) (int, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-p", strconv.Itoa(pid), "-o", "uid=").Output() //nolint:gosec,noctx // pid is int from trusted internal sources
	if err != nil {
		return 0, fmt.Errorf("ps -p %d -o uid=: %w", pid, err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse uid from ps output %q: %w", strings.TrimSpace(string(out)), err)
	}
	return uid, nil
}

// killProcessWithGrace sends SIGTERM to pid, waits up to grace for exit,
// then sends SIGKILL. Safe to call if the process is already gone.
func killProcessWithGrace(pid int, grace time.Duration) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil // process already gone
	}
	_ = proc.Signal(syscall.SIGTERM)
	// Reap in background so IsProcessAlive can detect exit via ESRCH.
	go func() { _, _ = proc.Wait() }()

	deadline := time.After(grace)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !IsProcessAlive(pid) {
				return nil
			}
		case <-deadline:
			_ = proc.Signal(syscall.SIGKILL)
			return nil
		}
	}
}

// doltDatabasePresent reports whether the dolt data directory under oroHome
// contains at least one database directory.
func doltDatabasePresent(oroHome string) bool {
	return len(listDatabases(filepath.Join(oroHome, "dolt"))) > 0
}

// runDoltRepair is the testable core of the dolt repair command.
func runDoltRepair(deps doltRepairDeps, w io.Writer) error {
	// Step 1: Acquire exclusive repair lock.
	lockPath := filepath.Join(deps.oroHome, repairLockFilename)
	unlock, err := deps.lockFn(lockPath)
	if err != nil {
		if errors.Is(err, ErrFlockContended) {
			return &exitError{code: repairExitContended, msg: "another dolt repair is already running"}
		}
		return fmt.Errorf("acquire repair lock: %w", err)
	}
	defer func() { _ = unlock() }()

	// Step 2: Run identity probe.
	pid, _, probeErr := deps.probeFn()

	switch {
	case probeErr == nil:
		// Server is running with correct data-dir; check database presence.
		if !deps.dbPresentFn() {
			fmt.Fprintln(w, "dolt server data-dir correct but no database found; run 'oro dolt setup' to migrate")
			return &exitError{code: repairExitNoDB, msg: "no database in dolt data directory"}
		}
		// Warn (non-blocking) if any project's registry port differs from its metadata port.
		warnPortMismatches(deps.oroHome, w)
		fmt.Fprintln(w, "dolt server identity probe passed; no repair needed")
		return nil

	case errors.Is(probeErr, ErrCannotIdentify):
		fmt.Fprintf(w, "cannot identify dolt server owner: %v\n", probeErr)
		return &exitError{code: repairExitNoOwner, msg: fmt.Sprintf("cannot identify dolt server: %v", probeErr)}

	case errors.Is(probeErr, ErrDataDirMismatch):
		if deps.dryRun {
			fmt.Fprintf(w, "dry-run: rogue dolt server PID %d using wrong data-dir; would SIGTERM and kickstart\n", pid)
			return nil
		}
		return repairRogueDolt(deps, pid, w)

	default:
		return fmt.Errorf("identity probe: %w", probeErr)
	}
}

// warnPortMismatches scans all registered projects and warns when a project's
// registry-allocated port does not match its metadata.json port. Non-blocking:
// errors reading the registry or metadata are silently skipped.
func warnPortMismatches(oroHome string, w io.Writer) {
	registryPath := filepath.Join(oroHome, "port-registry.json")
	reg, err := readRegistry(registryPath)
	if err != nil {
		return
	}
	for _, beadsDir := range discoverBreadsDirs(oroHome) {
		meta, metaErr := readDoltMeta(beadsDir)
		if metaErr != nil || meta == nil {
			continue
		}
		absBeadsDir, absErr := filepath.Abs(beadsDir)
		if absErr != nil {
			absBeadsDir = beadsDir
		}
		alloc, ok := reg.Allocations[absBeadsDir]
		if !ok {
			continue
		}
		if alloc.Port != meta.DoltServerPort {
			fmt.Fprintf(w, "warning: registry port %d != metadata port %d for %s\n",
				alloc.Port, meta.DoltServerPort, beadsDir)
		}
	}
}

// repairRogueDolt kills the rogue process (if we own it) and kickstarts the
// managed service, then re-probes to confirm health.
func repairRogueDolt(deps doltRepairDeps, roguePID int, w io.Writer) error {
	// Verify we own the rogue process before killing it.
	if roguePID > 0 {
		uid, err := deps.ownerFn(roguePID)
		if err != nil || uid != deps.currentUID {
			msg := fmt.Sprintf(
				"rogue dolt server PID %d is owned by UID %d (current UID %d); cannot kill — manual intervention required",
				roguePID, uid, deps.currentUID,
			)
			fmt.Fprintln(w, msg)
			return &exitError{code: repairExitNoOwner, msg: msg}
		}
		fmt.Fprintf(w, "stopping rogue dolt server (PID %d)...\n", roguePID)
		_ = deps.killFn(roguePID)
	}

	// Kickstart the managed launchd service.
	fmt.Fprintln(w, "restarting shared dolt server via launchctl...")
	deps.kickstartFn()

	// Wait up to repairPortWaitTimeout for the server to bind.
	if deps.waitPortFn != nil {
		deps.waitPortFn(SharedDoltPort, repairPortWaitTimeout)
	}

	// Re-probe to confirm health.
	_, _, reProbeErr := deps.probeFn()
	if reProbeErr != nil {
		fmt.Fprintf(w, "server still unhealthy after repair attempt: %v\n", reProbeErr)
		return &exitError{code: repairExitStillBad, msg: fmt.Sprintf("repair failed: server still unhealthy: %v", reProbeErr)}
	}

	// Check DB presence after successful re-probe.
	if !deps.dbPresentFn() {
		fmt.Fprintln(w, "server repaired but no database found; run 'oro dolt setup' to migrate")
		return &exitError{code: repairExitNoDB, msg: "no database in dolt data directory after repair"}
	}

	fmt.Fprintln(w, "dolt server repaired successfully")
	return nil
}
