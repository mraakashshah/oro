package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// newDoctorCmd creates the "oro doctor" parent command.
//
// With no arguments it runs a read-only DIAGNOSIS of the state DB. The
// "migrate" subcommand runs the offline v3->v4 state-DB migration without
// starting the dispatcher or any workers.
func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose oro installation issues",
		Long: `Diagnose common oro installation issues.

With no arguments, 'oro doctor' runs a read-only diagnosis of the project state
DB (schema version, active assignments, integrity) and prints a verdict.

Use 'oro doctor migrate' to run the offline v3->v4 state-DB migration on an idle
store without launching the dispatcher or any workers.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			pidPath, sockPath := paths.PIDPath, paths.SocketPath
			cfg := doctorDiagnoseConfig{
				stateDBPath: paths.StateDBPath,
				w:           cmd.OutOrStdout(),
				daemonLive:  daemonLiveFunc(pidPath, sockPath),
			}
			return runDoctorDiagnose(cmd.Context(), cfg)
		},
	}
	cmd.AddCommand(newDoctorMigrateCmd())
	return cmd
}

// daemonLiveFunc returns a closure reporting whether a dispatcher is running
// and, if so, its PID — the same liveness check used by offline recovery.
func daemonLiveFunc(pidPath, sockPath string) func() (bool, int) {
	return func() (bool, int) {
		status, pid, statusErr := DaemonStatus(pidPath, sockPath)
		if statusErr != nil {
			return false, 0
		}
		return status == StatusRunning, pid
	}
}

// doctorDiagnoseConfig holds injectable dependencies for the read-only doctor
// diagnosis so tests can drive it without a real store or daemon.
type doctorDiagnoseConfig struct {
	stateDBPath string
	w           io.Writer
	daemonLive  func() (bool, int)
}

// runDoctorDiagnose inspects the state DB read-only and prints a report:
// path, PRAGMA user_version, active-assignment count, integrity_check, and a
// one-line verdict. It has no side effects beyond what openStateDB performs.
func runDoctorDiagnose(ctx context.Context, cfg doctorDiagnoseConfig) error {
	db, err := openStateDB(cfg.stateDBPath)
	if err != nil {
		return fmt.Errorf("open state db: %w", err)
	}
	defer func() { _ = db.Close() }()

	userVersion, err := scanIntQuery(ctx, db, `PRAGMA user_version`)
	if err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	activeCount, err := scanIntQuery(ctx, db, `SELECT COUNT(*) FROM assignments WHERE status='active'`)
	if err != nil {
		return fmt.Errorf("count active assignments: %w", err)
	}
	integrity, err := scanStringQuery(ctx, db, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}

	fmt.Fprintf(cfg.w, "state DB:       %s\n", cfg.stateDBPath)
	fmt.Fprintf(cfg.w, "user_version:   %d\n", userVersion)
	fmt.Fprintf(cfg.w, "active assignments: %d\n", activeCount)
	fmt.Fprintf(cfg.w, "integrity_check:    %s\n", integrity)
	fmt.Fprintf(cfg.w, "verdict:        %s\n", doctorVerdict(userVersion, activeCount))
	return nil
}

// doctorVerdict returns the one-line diagnosis verdict.
func doctorVerdict(userVersion, activeCount int) string {
	if userVersion >= 4 {
		return "already migrated (v4)"
	}
	if activeCount > 0 {
		return fmt.Sprintf("needs v4 migration but BLOCKED by %d active assignments (run: oro recovery abandon-stale first)", activeCount)
	}
	return "needs v4 migration (run: oro doctor migrate)"
}

// doctorMigrateConfig holds injectable dependencies for the offline v4
// migration, mirroring recoveryAbandonConfig so the daemon-liveness check is
// testable without a real store or daemon.
type doctorMigrateConfig struct {
	stateDBPath string
	w           io.Writer
	// daemonLive reports whether a dispatcher is currently running and, if so,
	// its PID. Offline migration must refuse while a dispatcher owns the DB.
	daemonLive func() (bool, int)
}

// newDoctorMigrateCmd builds `oro doctor migrate`: the offline v3->v4 state-DB
// migration. It refuses while a dispatcher is live and reuses the exact
// production migration path (openStateDBWithV4Migration), which makes its own
// timestamped backup before transforming.
func newDoctorMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Offline: migrate the state DB to schema v4 without starting the dispatcher",
		Long: `Migrates the project state DB from v3 to v4 WITHOUT starting the dispatcher or
any workers.

Today the v3->v4 migration only runs as a side effect of 'oro start' or 'oro
work', both of which spawn real work. Use this to migrate an idle or parked
store offline — for example after 'oro recovery abandon-stale' clears a
deadlock.

Refuses to run while a dispatcher is live. Reuses the production migration path,
which backs up the state DB to a timestamped copy before transforming. If active
assignments still block the migration, run 'oro recovery abandon-stale' first.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := ResolveDaemonPaths()
			if err != nil {
				return fmt.Errorf("resolve paths: %w", err)
			}
			pidPath, sockPath := paths.PIDPath, paths.SocketPath
			cfg := doctorMigrateConfig{
				stateDBPath: paths.StateDBPath,
				w:           cmd.OutOrStdout(),
				daemonLive:  daemonLiveFunc(pidPath, sockPath),
			}
			return runDoctorMigrate(cmd.Context(), cfg)
		},
	}
}

// runDoctorMigrate executes the guarded offline v4 migration: refuse if a
// dispatcher is live, report the before user_version, run the production
// migration path, then report the after user_version. Active-assignment guard
// errors are translated into an operator-friendly hint.
func runDoctorMigrate(ctx context.Context, cfg doctorMigrateConfig) error {
	if live, pid := cfg.daemonLive(); live {
		return fmt.Errorf("dispatcher is running (PID %d); stop it with 'oro stop' before offline migration", pid)
	}

	before, err := readStateDBUserVersion(ctx, cfg.stateDBPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(cfg.w, "state DB:       %s\n", cfg.stateDBPath)
	fmt.Fprintf(cfg.w, "user_version before: %d\n", before)

	if before >= 4 {
		fmt.Fprintln(cfg.w, "already migrated (v4); nothing to do")
		return nil
	}

	db, err := openStateDBWithV4Migration(cfg.stateDBPath)
	if err != nil {
		if strings.Contains(err.Error(), "cannot migrate while") {
			return fmt.Errorf("cannot migrate: active assignments block the v4 migration; run 'oro recovery abandon-stale' first: %w", err)
		}
		return fmt.Errorf("migrate state db to v4: %w", err)
	}
	defer func() { _ = db.Close() }()

	after, err := scanIntQuery(ctx, db, `PRAGMA user_version`)
	if err != nil {
		return fmt.Errorf("read user_version after migration: %w", err)
	}
	fmt.Fprintf(cfg.w, "user_version after:  %d\n", after)
	if after >= 4 {
		fmt.Fprintln(cfg.w, "migrated to schema v4")
	}
	return nil
}

// readStateDBUserVersion opens the state DB read-only-ish (via openStateDB) and
// returns its PRAGMA user_version, closing the DB before returning.
func readStateDBUserVersion(ctx context.Context, path string) (int, error) {
	db, err := openStateDB(path)
	if err != nil {
		return 0, fmt.Errorf("open state db: %w", err)
	}
	defer func() { _ = db.Close() }()
	v, err := scanIntQuery(ctx, db, `PRAGMA user_version`)
	if err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}

// scanIntQuery runs a single-row, single-int query and returns the value.
func scanIntQuery(ctx context.Context, db *sql.DB, query string) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, query).Scan(&v); err != nil {
		return 0, fmt.Errorf("scan int query %q: %w", query, err)
	}
	return v, nil
}

// scanStringQuery runs a single-row, single-string query and returns the value.
func scanStringQuery(ctx context.Context, db *sql.DB, query string) (string, error) {
	var v string
	if err := db.QueryRowContext(ctx, query).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("scan string query %q: %w", query, err)
	}
	return v, nil
}
