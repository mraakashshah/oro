package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"oro/pkg/dispatcher"
)

// recoveryAbandonConfig holds injectable dependencies for the offline
// abandon-stale recovery flow, mirroring stopConfig so the confirmation guard
// and daemon-liveness check are testable without a real store or TTY.
type recoveryAbandonConfig struct {
	stateDBPath string
	force       bool
	w           io.Writer
	stdin       io.Reader
	isTTY       func() bool
	// daemonLive reports whether a dispatcher is currently running and, if so,
	// its PID. Offline recovery must refuse while a dispatcher owns the DB.
	daemonLive func() (bool, int)
}

// newRecoveryAbandonStaleCmd builds `oro recovery abandon-stale`: an offline,
// guarded command that quarantines every status='active' assignment WITHOUT
// starting the dispatcher or running the v4 migration. It breaks the deadlock
// where stale active rows block ensureNoActiveAssignments (and thus `oro
// start`), so the dispatcher can never come up to clean them.
func newRecoveryAbandonStaleCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "abandon-stale",
		Short: "Offline: quarantine all active assignments to break a migration deadlock",
		Long: `Quarantines every status='active' assignment in the state DB WITHOUT starting
the dispatcher or running the v3->v4 migration.

Use this to break the deadlock where stale active assignment rows block the v4
migration (ensureNoActiveAssignments), so 'oro start' aborts before the
dispatcher's own recovery can quarantine them.

Refuses to run while a dispatcher is live. Backs up state.db (and any WAL/SHM
sidecars) to a timestamped copy before writing. After it runs, 'oro start' will
migrate cleanly and 'oro recovery list'/'resolve' can finish the quarantined
work.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			pidPath, sockPath := paths.PIDPath, paths.SocketPath
			cfg := recoveryAbandonConfig{
				stateDBPath: paths.StateDBPath,
				force:       force,
				w:           cmd.OutOrStdout(),
				stdin:       os.Stdin,
				isTTY:       isStdinTTY,
				daemonLive: func() (bool, int) {
					status, pid, statusErr := DaemonStatus(pidPath, sockPath)
					if statusErr != nil {
						return false, 0
					}
					return status == StatusRunning, pid
				},
			}
			return runRecoveryAbandonStale(cmd.Context(), cfg)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip interactive confirmation (requires ORO_HUMAN_CONFIRMED=1)")
	return cmd
}

// runRecoveryAbandonStale executes the guarded offline recovery: refuse if a
// dispatcher is live, confirm authorization, back up the DB, then quarantine
// every active assignment.
func runRecoveryAbandonStale(ctx context.Context, cfg recoveryAbandonConfig) error {
	if live, pid := cfg.daemonLive(); live {
		return fmt.Errorf("dispatcher is running (PID %d); stop it with 'oro stop' before offline recovery", pid)
	}
	if err := confirmRecoveryAbandon(cfg); err != nil {
		return err
	}

	backupPath, err := backupStateDBForRecovery(cfg.stateDBPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(cfg.w, "backed up state DB to %s\n", backupPath)

	db, err := openStateDB(cfg.stateDBPath)
	if err != nil {
		return fmt.Errorf("open state db: %w", err)
	}
	defer func() { _ = db.Close() }()

	result, err := dispatcher.AbandonAllActiveAssignments(ctx, db)
	if err != nil {
		return fmt.Errorf("abandon active assignments: %w", err)
	}

	writeRecoveryAbandonSummary(cfg.w, result)
	return nil
}

// confirmRecoveryAbandon mirrors confirmStop: --force requires
// ORO_HUMAN_CONFIRMED=1, otherwise an interactive TTY must type YES.
func confirmRecoveryAbandon(cfg recoveryAbandonConfig) error {
	if cfg.force {
		if os.Getenv("ORO_HUMAN_CONFIRMED") != "1" {
			return fmt.Errorf("--force requires ORO_HUMAN_CONFIRMED=1 environment variable")
		}
		return nil
	}
	if cfg.isTTY != nil && !cfg.isTTY() {
		return fmt.Errorf("oro recovery abandon-stale requires an interactive terminal (stdin is not a TTY)\n" +
			"Hint: use --force with ORO_HUMAN_CONFIRMED=1 for non-interactive use")
	}
	fmt.Fprint(cfg.w, "Type YES to quarantine all active assignments: ")
	scanner := bufio.NewScanner(cfg.stdin)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read confirmation from stdin")
	}
	if strings.TrimSpace(scanner.Text()) != "YES" {
		return fmt.Errorf("aborted (expected YES)")
	}
	return nil
}

// backupStateDBForRecovery copies state.db and any -wal/-shm sidecars to
// timestamped .bak-YYYYMMDD-HHMMSS files before any write. It returns the
// backup path of the primary DB file.
func backupStateDBForRecovery(stateDBPath string) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	suffix := ".bak-" + stamp
	primaryBackup := stateDBPath + suffix
	if err := copyFileForBackup(stateDBPath, primaryBackup); err != nil {
		return "", err
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		src := stateDBPath + sidecar
		if _, statErr := os.Stat(src); statErr != nil { // #nosec G304,G703 -- src derived from the configured local state DB path.
			continue // sidecar absent; nothing to back up
		}
		if err := copyFileForBackup(src, src+suffix); err != nil {
			return "", err
		}
	}
	return primaryBackup, nil
}

// copyFileForBackup copies src to dst, syncing to disk. dst must not exist.
func copyFileForBackup(src, dst string) error {
	in, err := os.Open(src) // #nosec G304,G703 -- src is the configured local state DB or its sidecar.
	if err != nil {
		return fmt.Errorf("open %s for backup: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600) // #nosec G304,G703 -- dst derived from the state DB path.
	if err != nil {
		return fmt.Errorf("create backup %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy backup %s: %w", dst, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync backup %s: %w", dst, err)
	}
	return nil
}

// writeRecoveryAbandonSummary prints the quarantine counts and the operator
// next-step hint.
func writeRecoveryAbandonSummary(w io.Writer, result dispatcher.AbandonResult) {
	fmt.Fprintf(w, "quarantined %d active assignment(s): %d with beads, %d orphaned\n",
		result.Total, result.WithBead, result.Orphaned)
	for _, q := range result.Quarantined {
		fmt.Fprintf(w, "  #%d %s %s\n", q.AssignmentID, q.BeadID, q.Reason)
	}
	if result.Total == 0 {
		fmt.Fprintln(w, "no active assignments found; nothing to quarantine")
		return
	}
	fmt.Fprintln(w, "next: 'oro start' will now migrate cleanly; use 'oro recovery list' and 'oro recovery resolve' to finish the quarantined work")
}
