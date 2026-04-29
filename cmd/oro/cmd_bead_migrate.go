package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

func newBeadMigrateFromDoltCmd(store beadstore.Store) *cobra.Command {
	var opts beadMigrateOptions

	cmd := &cobra.Command{
		Use:   "migrate-from-dolt",
		Short: "Plan or run a bd/dolt to native bead-store migration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBeadMigrateFromDoltCmd(cmd, store, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print a migration plan without mutating SQLite")
	cmd.Flags().BoolVar(&opts.reconcile, "reconcile", false, "reconcile a previous migration against current dolt state")
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "apply reconcile changes; without this, --reconcile is a dry-run")
	cmd.Flags().StringVar(&opts.fromJSONL, "from-jsonl", "", "read bd export JSONL from a file instead of invoking bd")
	cmd.Flags().StringVar(&opts.fromFixture, "from-fixture", "", "read a test fixture directory or JSONL file instead of invoking bd")
	cmd.Flags().BoolVar(&opts.ignoreVersionDrift, "ignore-version-drift", false, "acknowledge bd/dolt version drift during migration")
	cmd.Flags().BoolVar(&opts.allowRunningDispatcher, "allow-running-dispatcher", false, "allow migration while a dispatcher PID lock is active")
	cmd.Flags().BoolVar(&opts.forceRecover, "force-recover", false, "acknowledge partial Dolt corruption and migrate readable bd export rows")
	return cmd
}

func runBeadMigrateFromDoltCmd(cmd *cobra.Command, store beadstore.Store, opts beadMigrateOptions) error {
	unlockMigration, err := acquireBeadMigrationLocksForOptions(opts, cmd.ErrOrStderr())
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
	}
	if unlockMigration != nil {
		defer func() { _ = unlockMigration() }()
	}
	if opts.reconcile {
		return runBeadMigrateReconcileCmd(cmd, opts)
	}
	if opts.ignoreVersionDrift {
		return writeBeadCommandErrorIfJSON(cmd, "unsupported", errors.New("--ignore-version-drift is not implemented in this migration seam"))
	}
	_ = store
	return runBeadMigrateApplyOrPlanCmd(cmd, opts)
}

func acquireBeadMigrationLocksForOptions(opts beadMigrateOptions, warnings io.Writer) (func() error, error) {
	if opts.dryRun || (opts.reconcile && !opts.apply) {
		return nil, nil
	}
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve bead store paths: %w", err)
	}
	return acquireBeadMigrationLocks(paths.StateDBPath, opts.allowRunningDispatcher, warnings)
}

func runBeadMigrateReconcileCmd(cmd *cobra.Command, opts beadMigrateOptions) error {
	source, data, _, err := readBeadMigrationSource(cmd.Context(), opts, false, defaultBeadMigrationRunner)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "reconcile", err)
	}
	applyReconcile := opts.apply && !opts.dryRun
	backupPath, err := writePreReconcileBackupIfApplying(cmd.Context(), applyReconcile)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "reconcile", err)
	}
	report, err := runBeadReconcile(cmd.Context(), data, applyReconcile)
	report.BackupPath = backupPath
	if err := failUnexpectedReconcileError(err, backupPath); err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "reconcile", err)
	}
	writeBeadReconcileReport(cmd.OutOrStdout(), source, report, applyReconcile)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "reconcile", err)
	}
	return nil
}

func writePreReconcileBackupIfApplying(ctx context.Context, apply bool) (string, error) {
	if !apply {
		return "", nil
	}
	backupPath, err := writePreReconcileSQLiteBackup(ctx)
	if err != nil {
		return "", fmt.Errorf("write pre-reconcile backup: %w", err)
	}
	return backupPath, nil
}

func failUnexpectedReconcileError(err error, backupPath string) error {
	var validationErr beadMigrationValidationError
	if err == nil || errors.As(err, &validationErr) {
		return nil
	}
	if backupPath != "" {
		return fmt.Errorf("reconcile failed after backup %s: %w", backupPath, err)
	}
	return err
}

func runBeadMigrateApplyOrPlanCmd(cmd *cobra.Command, opts beadMigrateOptions) error {
	source, data, preflight, err := readBeadMigrationSource(cmd.Context(), opts, true, defaultBeadMigrationRunner)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
	}
	if err := reportBeadMigrationPreflight(cmd.OutOrStdout(), source, preflight, opts.forceRecover); err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
	}
	if opts.dryRun {
		return writeBeadMigrationPlanForCmd(cmd, source, data)
	}
	return runBeadMigrationApplyCmd(cmd, source, data)
}

func runBeadMigrationApplyCmd(cmd *cobra.Command, source beadMigrationSource, data []byte) error {
	backupPath, err := writeBeadMigrationBackup(data)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "migrate", fmt.Errorf("write migration backup: %w", err))
	}
	report, err := runBeadMigration(cmd.Context(), data)
	report.BackupPath = backupPath
	var validationErr beadMigrationValidationError
	if err != nil && !errors.As(err, &validationErr) {
		return writeBeadCommandErrorIfJSON(cmd, "migrate", fmt.Errorf("migration failed after backup %s: %w", backupPath, err))
	}
	writeBeadMigrationCompletion(cmd.OutOrStdout(), source, report, err)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
	}
	return nil
}

func writeBeadMigrationCompletion(w io.Writer, source beadMigrationSource, report beadMigrationValidationReport, err error) {
	if err != nil {
		fmt.Fprintf(w, "Migration complete with errors\nsource: %s", source.kind)
	} else {
		fmt.Fprintf(w, "Migration complete\nsource: %s", source.kind)
	}
	if source.path != "" {
		fmt.Fprintf(w, " (%s)", source.path)
	}
	fmt.Fprintln(w)
	writeBeadMigrationReport(w, report)
}

func writeBeadMigrationPlanForCmd(cmd *cobra.Command, source beadMigrationSource, data []byte) error {
	plan, err := planBeadMigration(data)
	var validationErr beadMigrationValidationError
	if err != nil && !errors.As(err, &validationErr) {
		return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
	}
	writeBeadMigrationPlan(cmd.OutOrStdout(), source, plan)
	if err != nil {
		return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
	}
	return nil
}

type beadMigrateOptions struct {
	dryRun                 bool
	reconcile              bool
	apply                  bool
	fromJSONL              string
	fromFixture            string
	ignoreVersionDrift     bool
	allowRunningDispatcher bool
	forceRecover           bool
}

type beadMigrationSource struct {
	kind string
	path string
}

type beadMigrationCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execBeadMigrationRunner struct{}

func (execBeadMigrationRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // command names are fixed by migration code
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

var defaultBeadMigrationRunner beadMigrationCommandRunner = execBeadMigrationRunner{} //nolint:gochecknoglobals // tests swap this command seam.

type beadMigrationPreflight struct {
	checked     bool
	exportCount int
	doltCount   int
	doltErr     error
}

type beadMigrationPlan struct {
	Beads           int
	Dependencies    int
	Tags            int
	Labels          int
	MetadataEntries int
	Notes           int
	UnknownFields   int
	Errors          []string
	Warnings        []string
	StatusCounts    map[string]int
}

type beadMigrationValidationReport struct {
	SourceRows    int
	ValidRows     int
	ImportedRows  int
	VerifiedRows  int
	Verification  string
	BackupPath    string
	importedIDs   []string
	UnknownFields int
	Errors        []string
	Warnings      []string
	SkippedIDs    []string
}

type beadMigrationValidationError struct {
	count int
}

func (err beadMigrationValidationError) Error() string {
	return fmt.Sprintf("migration validation failed: %d row error(s)", err.count)
}

type beadReconcileReport struct {
	SourceBeads      int
	SQLiteBeads      int
	BackupPath       string
	Inserts          int
	Updates          int
	Deletes          int
	Conflicts        int
	ConflictedIDs    []string
	ValidationReport beadMigrationValidationReport
}

type beadReconcileChanges struct {
	inserts map[string]bdExportBead
	updates map[string]bdExportBead
	deletes map[string]sqliteMigrationBead
}

type bdExportBead struct {
	ID                 string                `json:"id"`
	Title              string                `json:"title"`
	Description        string                `json:"description"`
	AcceptanceCriteria string                `json:"acceptance_criteria"`
	Status             string                `json:"status"`
	Priority           int                   `json:"priority"`
	Type               string                `json:"type"`
	IssueType          string                `json:"issue_type"`
	Parent             string                `json:"parent"`
	ParentID           string                `json:"parent_id"`
	Owner              string                `json:"owner"`
	Assignee           string                `json:"assignee"`
	EstimatedMinutes   int                   `json:"estimated_minutes"`
	Tier               string                `json:"tier"`
	Model              string                `json:"model"`
	CreatedAt          string                `json:"created_at"`
	UpdatedAt          string                `json:"updated_at"`
	ClosedAt           string                `json:"closed_at"`
	CloseReason        string                `json:"close_reason"`
	DeferredUntil      string                `json:"deferred_until"`
	DeferUntil         string                `json:"defer_until"`
	Dependencies       []protocol.Dependency `json:"dependencies"`
	Tags               []string              `json:"tags"`
	Labels             []string              `json:"labels"`
	Metadata           map[string]any        `json:"metadata"`
	Notes              json.RawMessage       `json:"notes"`
}

const beadMigrationPIDLockMaxAge = time.Hour

var beadMigrationBeforeRemoveInspectedPIDLockForTest = func(inspectedPIDLock) {} //nolint:gochecknoglobals // tests hook the narrow lock race point.

func runBeadMigration(ctx context.Context, data []byte) (beadMigrationValidationReport, error) {
	beads, report, err := validateBDExportForMigration(data)
	if err != nil {
		return report, err
	}
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return report, fmt.Errorf("resolve bead store paths: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.StateDBPath), 0o700); err != nil {
		return report, fmt.Errorf("create bead store dir: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return report, err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := setBeadParentTouchTriggers(ctx, tx, false); err != nil {
		return report, err
	}
	for _, bead := range beads {
		if err := insertMigratedBeadAtomically(ctx, tx, bead); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", migrationRowLabel(bead.ID), err))
			continue
		}
		report.ImportedRows++
		report.importedIDs = append(report.importedIDs, bead.ID)
	}
	if err := setBeadParentTouchTriggers(ctx, tx, true); err != nil {
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit migration transaction: %w", err)
	}
	verifiedRows, err := verifyMigratedBeadCount(ctx, db, report.importedIDs)
	report.VerifiedRows = verifiedRows
	if err != nil {
		report.Verification = "FAILED"
		report.Errors = append(report.Errors, fmt.Sprintf("verification: %v", err))
	} else {
		report.Verification = "OK"
	}
	return report, report.err()
}

func writeBeadMigrationBackup(data []byte) (string, error) {
	return writeBeadMigrationBackupFile(data, "pre-migration.jsonl")
}

func writeBeadMigrationBackupFile(data []byte, suffix string) (string, error) {
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return "", fmt.Errorf("resolve bead store paths: %w", err)
	}
	backupDir := filepath.Join(paths.OroHome, "migrations")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create migration backup dir %s: %w", backupDir, err)
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	var backupPath string
	for attempt := 0; attempt < 10; attempt++ {
		name := stamp + "-" + suffix
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d-%s", stamp, attempt, suffix)
		}
		backupPath = filepath.Join(backupDir, name)
		file, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // backupPath is under the resolved project migration directory.
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("write migration backup %s: %w", backupPath, err)
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return "", fmt.Errorf("write migration backup %s: %w", backupPath, err)
		}
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("close migration backup %s: %w", backupPath, err)
		}
		return backupPath, nil
	}
	return "", fmt.Errorf("write migration backup %s: backup path already exists after retries", backupPath)
}

func writePreReconcileSQLiteBackup(ctx context.Context) (string, error) {
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return "", fmt.Errorf("resolve bead store paths: %w", err)
	}
	db, err := openReconcileStateDB(paths.StateDBPath, false)
	if err != nil {
		return "", err
	}
	if db == nil {
		return writeBeadMigrationBackupFile(nil, "pre-reconcile-sqlite.jsonl")
	}
	defer db.Close()
	sqliteBeads, err := loadSQLiteMigrationBeads(ctx, db)
	if err != nil {
		return "", err
	}
	return writeBeadReconcileBackup(sqliteBeads)
}

func writeBeadReconcileBackup(sqliteBeads map[string]sqliteMigrationBead) (string, error) {
	ids := make([]string, 0, len(sqliteBeads))
	for id, bead := range sqliteBeads {
		if bead.Deleted {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var buf bytes.Buffer
	for _, id := range ids {
		row, err := json.Marshal(sqliteBeads[id].BDExportBead)
		if err != nil {
			return "", fmt.Errorf("encode sqlite bead %s: %w", id, err)
		}
		buf.Write(row)
		buf.WriteByte('\n')
	}
	return writeBeadMigrationBackupFile(buf.Bytes(), "pre-reconcile-sqlite.jsonl")
}

func verifyMigratedBeadCount(ctx context.Context, db *sql.DB, beadIDs []string) (int, error) {
	if len(beadIDs) == 0 {
		return 0, nil
	}
	seen := map[string]struct{}{}
	uniqueIDs := make([]string, 0, len(beadIDs))
	for _, id := range beadIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	count := 0
	for _, id := range uniqueIDs {
		var exists int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads WHERE deleted=0 AND id=?`, id).Scan(&exists); err != nil {
			return count, fmt.Errorf("count imported SQLite row %s: %w", id, err)
		}
		count += exists
	}
	if count != len(uniqueIDs) {
		return count, fmt.Errorf("sqlite row count %d does not match imported rows %d", count, len(uniqueIDs))
	}
	return count, nil
}

func insertMigratedBeadAtomically(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	return writeMigratedBeadAtomically(ctx, tx, bead, false)
}

func updateMigratedBeadAtomically(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	return writeMigratedBeadAtomically(ctx, tx, bead, true)
}

func writeMigratedBeadAtomically(ctx context.Context, tx *sql.Tx, bead bdExportBead, update bool) error {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT migrate_bead_row`); err != nil {
		return fmt.Errorf("create row savepoint for %s: %w", bead.ID, err)
	}
	if err := writeMigratedBead(ctx, tx, bead, update); err != nil {
		if rollbackErr := rollbackMigratedBeadSavepoint(ctx, tx); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT migrate_bead_row`); err != nil {
		return fmt.Errorf("release row savepoint for %s: %w", bead.ID, err)
	}
	return nil
}

func rollbackMigratedBeadSavepoint(ctx context.Context, tx *sql.Tx) error {
	_, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT migrate_bead_row`)
	_, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT migrate_bead_row`)
	return errors.Join(
		wrapIfErr(rollbackErr, "rollback row savepoint"),
		wrapIfErr(releaseErr, "release row savepoint"),
	)
}

func wrapIfErr(err error, message string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func acquireBeadMigrationLocks(stateDBPath string, allowRunningDispatcher bool, warnings io.Writer) (func() error, error) {
	canonicalStateDBPath, err := canonicalBeadMigrationStateDBPath(stateDBPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(canonicalStateDBPath), 0o700); err != nil {
		return nil, fmt.Errorf("create migration lock dir: %w", err)
	}
	var unlockDispatcher func() error
	if !allowRunningDispatcher {
		unlockDispatcher, err = acquireBeadMigrationDispatcherLock(canonicalStateDBPath+".lock", warnings)
		if err != nil {
			return nil, err
		}
	}
	unlockMigration, err := acquireBeadMigrationSelfLock(canonicalStateDBPath+".migrate.lock", warnings)
	if err != nil {
		if unlockDispatcher != nil {
			_ = unlockDispatcher()
		}
		return nil, err
	}
	return func() error {
		var unlockErr error
		if err := unlockMigration(); err != nil {
			unlockErr = err
		}
		if unlockDispatcher != nil {
			if err := unlockDispatcher(); err != nil && unlockErr == nil {
				unlockErr = err
			}
		}
		return unlockErr
	}, nil
}

func canonicalBeadMigrationStateDBPath(dbPath string) (string, error) {
	resolved, err := filepath.EvalSymlinks(dbPath)
	if err == nil {
		return resolved, nil
	}
	parent := filepath.Dir(dbPath)
	if resolvedParent, parentErr := filepath.EvalSymlinks(parent); parentErr == nil {
		return filepath.Join(resolvedParent, filepath.Base(dbPath)), nil
	}
	abs, absErr := filepath.Abs(dbPath)
	if absErr != nil {
		return "", fmt.Errorf("canonicalize state db path %s: %w", dbPath, absErr)
	}
	return abs, nil
}

func acquireBeadMigrationDispatcherLock(lockPath string, warnings io.Writer) (func() error, error) {
	pid := os.Getpid()
	lockChanged := false
	for attempt := 0; attempt < 2; attempt++ {
		unlock, acquired, err := createOwnedPIDLock(lockPath, pid, "dispatcher migration lock")
		if err != nil {
			return nil, err
		}
		if acquired {
			return unlock, nil
		}
		lock, err := inspectPIDLock(lockPath)
		if err != nil {
			return nil, err
		}
		if !lock.stale {
			return nil, fmt.Errorf("dispatcher is running (PID %d) against this state.db; stop it first with 'oro stop' before running migrate-from-dolt", lock.pid)
		}
		warnStalePIDLock(warnings, "dispatcher", lock)
		removed, err := removeInspectedPIDLock(lock)
		if err != nil {
			return nil, fmt.Errorf("remove stale dispatcher lock %s: %w", lockPath, err)
		}
		if !removed {
			lockChanged = true
			continue
		}
	}
	if lockChanged {
		return nil, fmt.Errorf("dispatcher lock %s changed while reclaiming stale lock", lockPath)
	}
	return nil, fmt.Errorf("dispatcher lock %s changed while reclaiming stale lock", lockPath)
}

func acquireBeadMigrationSelfLock(lockPath string, warnings io.Writer) (func() error, error) {
	unlockGuard, err := acquireBeadMigrationGuardLock(lockPath+".guard", warnings)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unlockGuard() }()

	pid := os.Getpid()
	lockChanged := false
	for attempt := 0; attempt < 2; attempt++ {
		unlock, acquired, err := createOwnedPIDLock(lockPath, pid, "migration lock")
		if err != nil {
			return nil, err
		}
		if acquired {
			return unlock, nil
		}
		lock, inspectErr := inspectPIDLock(lockPath)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if !lock.stale {
			return nil, fmt.Errorf("another migration is running (PID %d) for this state.db; lock file: %s", lock.pid, lockPath)
		}
		warnStalePIDLock(warnings, "migration", lock)
		removed, removeErr := removeInspectedPIDLock(lock)
		if removeErr != nil {
			return nil, fmt.Errorf("remove stale migration lock %s: %w", lockPath, removeErr)
		}
		if !removed {
			lockChanged = true
			continue
		}
	}
	if lockChanged {
		return nil, fmt.Errorf("create migration lock %s: lock changed while acquiring", lockPath)
	}
	return nil, fmt.Errorf("create migration lock %s: lock changed while acquiring", lockPath)
}

func acquireBeadMigrationGuardLock(lockPath string, warnings io.Writer) (func() error, error) {
	pid := os.Getpid()
	lockChanged := false
	for attempt := 0; attempt < 2; attempt++ {
		unlock, acquired, err := createOwnedPIDLock(lockPath, pid, "migration lock guard")
		if err != nil {
			return nil, err
		}
		if acquired {
			return unlock, nil
		}
		lock, inspectErr := inspectPIDLock(lockPath)
		if inspectErr != nil {
			return nil, inspectErr
		}
		if !lock.stale {
			return nil, fmt.Errorf("another migration is acquiring this state.db lock (PID %d); guard file: %s", lock.pid, lockPath)
		}
		warnStalePIDLock(warnings, "migration guard", lock)
		removed, removeErr := removeInspectedPIDLock(lock)
		if removeErr != nil {
			return nil, fmt.Errorf("remove stale migration lock guard %s: %w", lockPath, removeErr)
		}
		if !removed {
			lockChanged = true
			continue
		}
	}
	if lockChanged {
		return nil, fmt.Errorf("create migration lock guard %s: lock changed while acquiring", lockPath)
	}
	return nil, fmt.Errorf("create migration lock guard %s: lock changed while acquiring", lockPath)
}

func createOwnedPIDLock(lockPath string, pid int, kind string) (unlock func() error, acquired bool, err error) {
	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // lock path is derived from StateDBPath
	if err != nil {
		if os.IsExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("create %s %s: %w", kind, lockPath, err)
	}
	if _, writeErr := f.WriteString(strconv.Itoa(pid)); writeErr != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return nil, false, fmt.Errorf("write %s %s: %w", kind, lockPath, writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(lockPath)
		return nil, false, fmt.Errorf("close %s %s: %w", kind, lockPath, closeErr)
	}
	return func() error {
		return removeOwnedPIDLock(lockPath, pid)
	}, true, nil
}

type inspectedPIDLock struct {
	path    string
	pid     int
	exists  bool
	alive   bool
	old     bool
	stale   bool
	modTime time.Time
}

func inspectPIDLock(lockPath string) (inspectedPIDLock, error) {
	info, err := os.Stat(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return inspectedPIDLock{path: lockPath, stale: true}, nil
		}
		return inspectedPIDLock{}, fmt.Errorf("stat lock file %s: %w", lockPath, err)
	}
	data, err := os.ReadFile(lockPath) //nolint:gosec // lock path is derived from StateDBPath
	if err != nil {
		return inspectedPIDLock{}, fmt.Errorf("read lock file %s: %w", lockPath, err)
	}
	pid := parsePIDLockText(data)
	if pid <= 0 {
		return staleExistingPIDLock(lockPath, info.ModTime()), nil
	}
	alive := processAlive(pid)
	old := time.Since(info.ModTime()) > beadMigrationPIDLockMaxAge
	return inspectedPIDLock{
		path:    lockPath,
		pid:     pid,
		exists:  true,
		alive:   alive,
		old:     old,
		stale:   !alive || old,
		modTime: info.ModTime(),
	}, nil
}

func parsePIDLockText(data []byte) int {
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func staleExistingPIDLock(lockPath string, modTime time.Time) inspectedPIDLock {
	return inspectedPIDLock{path: lockPath, exists: true, old: true, stale: true, modTime: modTime}
}

func warnStalePIDLock(w io.Writer, kind string, lock inspectedPIDLock) {
	if w == nil || lock.pid <= 0 {
		return
	}
	reason := "dead process"
	if lock.alive && lock.old {
		reason = "older than 1h"
	}
	fmt.Fprintf(w, "warning: reclaiming stale %s lock %s (PID %d, %s)\n", kind, lock.path, lock.pid, reason)
}

func removeInspectedPIDLock(lock inspectedPIDLock) (bool, error) {
	current, err := inspectPIDLock(lock.path)
	if err != nil {
		return false, err
	}
	if !current.exists {
		return true, nil
	}
	if current.pid != lock.pid || !current.modTime.Equal(lock.modTime) || current.stale != lock.stale {
		return false, nil
	}
	beadMigrationBeforeRemoveInspectedPIDLockForTest(lock)
	stalePath := fmt.Sprintf("%s.stale.%d.%d", lock.path, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(lock.path, stalePath); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("rename stale lock %s: %w", lock.path, err)
	}
	moved, err := inspectPIDLock(stalePath)
	if err != nil {
		return false, err
	}
	if moved.pid != lock.pid || !moved.modTime.Equal(lock.modTime) || moved.stale != lock.stale {
		if restoreErr := os.Rename(stalePath, lock.path); restoreErr != nil {
			return false, fmt.Errorf("restore changed lock %s: %w", lock.path, restoreErr)
		}
		return false, nil
	}
	if err := os.Remove(stalePath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove stale lock %s: %w", stalePath, err)
	}
	return true, nil
}

func removeOwnedPIDLock(lockPath string, pid int) error {
	data, err := os.ReadFile(lockPath) //nolint:gosec // lock path is derived from StateDBPath
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read migration lock %s: %w", lockPath, err)
	}
	if strings.TrimSpace(string(data)) != strconv.Itoa(pid) {
		return nil
	}
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove migration lock %s: %w", lockPath, err)
	}
	return nil
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func writeMigratedBead(ctx context.Context, tx *sql.Tx, bead bdExportBead, update bool) error {
	bead, updatedAt, err := prepareMigratedBead(bead)
	if err != nil {
		return err
	}

	if update {
		if err := updateMigratedBeadRow(ctx, tx, bead); err != nil {
			return err
		}
		if err := clearMigratedBeadRelations(ctx, tx, bead.ID); err != nil {
			return err
		}
	} else if err := insertMigratedBeadRow(ctx, tx, bead); err != nil {
		return err
	}
	return writeMigratedBeadRelations(ctx, tx, bead, updatedAt)
}

func prepareMigratedBead(bead bdExportBead) (bdExportBead, string, error) {
	if strings.TrimSpace(bead.ID) == "" {
		return bdExportBead{}, "", fmt.Errorf("bd export bead is missing id")
	}
	if strings.TrimSpace(bead.Title) == "" {
		return bdExportBead{}, "", fmt.Errorf("bd export bead %s is missing title", bead.ID)
	}
	normalizedBead, err := normalizeBDExportBeadForMigration(bead)
	if err != nil {
		return bdExportBead{}, "", fmt.Errorf("normalize migrated bead %s: %w", bead.ID, err)
	}
	createdAt := firstNonEmpty(normalizedBead.CreatedAt, normalizedBead.UpdatedAt)
	updatedAt := firstNonEmpty(normalizedBead.UpdatedAt, normalizedBead.CreatedAt)
	if createdAt == "" || updatedAt == "" {
		return bdExportBead{}, "", fmt.Errorf("bd export bead %s is missing created_at or updated_at", bead.ID)
	}
	normalizedBead.CreatedAt = createdAt
	normalizedBead.UpdatedAt = updatedAt
	return normalizedBead, updatedAt, nil
}

func updateMigratedBeadRow(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	beadType := firstNonEmpty(bead.IssueType, bead.Type, "task")
	if _, err := tx.ExecContext(ctx, `
UPDATE beads SET
	title=?, description=?, acceptance_criteria=?, status=?, priority=?, type=?, parent_id=?,
	owner=?, estimated_minutes=?, tier=?, model=?, deferred_until=?, close_reason=?,
	created_at=?, updated_at=?, closed_at=?, deleted=0
WHERE id=?`,
		bead.Title,
		bead.Description,
		bead.AcceptanceCriteria,
		normalizeMigrationInsertStatus(bead.Status),
		bead.Priority,
		beadType,
		emptyStringToNil(firstNonEmpty(bead.ParentID, bead.Parent)),
		emptyStringToNil(firstNonEmpty(bead.Owner, bead.Assignee)),
		positiveIntToNil(bead.EstimatedMinutes),
		emptyStringToNil(bead.Tier),
		emptyStringToNil(bead.Model),
		emptyStringToNil(firstNonEmpty(bead.DeferredUntil, bead.DeferUntil)),
		emptyStringToNil(bead.CloseReason),
		bead.CreatedAt,
		bead.UpdatedAt,
		emptyStringToNil(bead.ClosedAt),
		bead.ID,
	); err != nil {
		return fmt.Errorf("update migrated bead %s: %w", bead.ID, err)
	}
	return nil
}

func clearMigratedBeadRelations(ctx context.Context, tx *sql.Tx, beadID string) error {
	deleteQueries := map[string]string{
		"bead_deps":     `DELETE FROM bead_deps WHERE bead_id=?`,
		"bead_tags":     `DELETE FROM bead_tags WHERE bead_id=?`,
		"bead_labels":   `DELETE FROM bead_labels WHERE bead_id=?`,
		"bead_metadata": `DELETE FROM bead_metadata WHERE bead_id=?`,
		"bead_notes":    `DELETE FROM bead_notes WHERE bead_id=?`,
	}
	for table, query := range deleteQueries {
		if _, err := tx.ExecContext(ctx, query, beadID); err != nil {
			return fmt.Errorf("clear migrated %s for %s: %w", table, beadID, err)
		}
	}
	return nil
}

func insertMigratedBeadRow(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	beadType := firstNonEmpty(bead.IssueType, bead.Type, "task")
	if _, err := tx.ExecContext(ctx, `
INSERT INTO beads (
	id, title, description, acceptance_criteria, status, priority, type, parent_id,
	owner, estimated_minutes, tier, model, deferred_until, close_reason,
	created_at, updated_at, closed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		bead.ID,
		bead.Title,
		bead.Description,
		bead.AcceptanceCriteria,
		normalizeMigrationInsertStatus(bead.Status),
		bead.Priority,
		beadType,
		emptyStringToNil(firstNonEmpty(bead.ParentID, bead.Parent)),
		emptyStringToNil(firstNonEmpty(bead.Owner, bead.Assignee)),
		positiveIntToNil(bead.EstimatedMinutes),
		emptyStringToNil(bead.Tier),
		emptyStringToNil(bead.Model),
		emptyStringToNil(firstNonEmpty(bead.DeferredUntil, bead.DeferUntil)),
		emptyStringToNil(bead.CloseReason),
		bead.CreatedAt,
		bead.UpdatedAt,
		emptyStringToNil(bead.ClosedAt),
	); err != nil {
		return fmt.Errorf("insert migrated bead %s: %w", bead.ID, err)
	}
	return nil
}

func writeMigratedBeadRelations(ctx context.Context, tx *sql.Tx, bead bdExportBead, updatedAt string) error {
	if err := writeMigratedBeadDependencies(ctx, tx, bead); err != nil {
		return err
	}
	if err := writeMigratedBeadTags(ctx, tx, bead); err != nil {
		return err
	}
	if err := writeMigratedBeadLabels(ctx, tx, bead); err != nil {
		return err
	}
	if err := writeMigratedBeadMetadata(ctx, tx, bead); err != nil {
		return err
	}
	return writeMigratedBeadNotes(ctx, tx, bead, updatedAt)
}

func writeMigratedBeadDependencies(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	for _, dep := range bead.Dependencies {
		depType := firstNonEmpty(dep.Type, "blocks")
		dependsOn := strings.TrimSpace(dep.DependsOnID)
		if dependsOn == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, bead.ID, dependsOn, depType); err != nil {
			return fmt.Errorf("insert migrated dependency for %s: %w", bead.ID, err)
		}
	}
	return nil
}

func writeMigratedBeadTags(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	for _, tag := range bead.Tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_tags (bead_id, tag) VALUES (?, ?)`, bead.ID, tag); err != nil {
			return fmt.Errorf("insert migrated tag for %s: %w", bead.ID, err)
		}
	}
	return nil
}

func writeMigratedBeadLabels(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	for _, label := range bead.Labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_labels (bead_id, label) VALUES (?, ?)`, bead.ID, label); err != nil {
			return fmt.Errorf("insert migrated label for %s: %w", bead.ID, err)
		}
	}
	return nil
}

func writeMigratedBeadMetadata(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	for key, value := range bead.Metadata {
		if strings.TrimSpace(key) == "" {
			continue
		}
		encoded, err := migrationMetadataValue(value)
		if err != nil {
			return fmt.Errorf("encode metadata %s for %s: %w", key, bead.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_metadata (bead_id, key, value) VALUES (?, ?, ?)`, bead.ID, key, encoded); err != nil {
			return fmt.Errorf("insert migrated metadata for %s: %w", bead.ID, err)
		}
	}
	return nil
}

func writeMigratedBeadNotes(ctx context.Context, tx *sql.Tx, bead bdExportBead, updatedAt string) error {
	notes, err := migrationNotes(bead.Notes)
	if err != nil {
		return fmt.Errorf("decode notes for %s: %w", bead.ID, err)
	}
	for _, note := range notes {
		if strings.TrimSpace(note) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_notes (bead_id, author, content, created_at) VALUES (?, 'bd', ?, ?)`, bead.ID, note, updatedAt); err != nil {
			return fmt.Errorf("insert migrated note for %s: %w", bead.ID, err)
		}
	}
	return nil
}

func runBeadReconcile(ctx context.Context, data []byte, apply bool) (beadReconcileReport, error) {
	beads, validationReport, err := validateBDExportForMigration(data)
	if err != nil {
		return beadReconcileReport{}, err
	}
	sourceBeads := make(map[string]bdExportBead, len(beads))
	for _, bead := range beads {
		sourceBeads[bead.ID] = bead
	}
	report := beadReconcileReport{
		SourceBeads:      len(sourceBeads),
		ValidationReport: validationReport,
	}
	validationErr := validationReport.err()

	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return report, fmt.Errorf("resolve bead store paths: %w", err)
	}
	db, err := openReconcileStateDB(paths.StateDBPath, apply)
	if err != nil {
		return report, err
	}
	if db == nil {
		report.Inserts = len(sourceBeads)
		return report, validationErr
	}
	defer db.Close()

	sqliteBeads, err := loadSQLiteMigrationBeads(ctx, db)
	if err != nil {
		return report, err
	}
	report.SQLiteBeads = len(sqliteBeads)
	changes := planBeadReconcileChanges(sourceBeads, sqliteBeads, validationReport.SkippedIDs, validationErr == nil, &report)
	if !apply {
		return report, validationErr
	}
	if err := applyBeadReconcileChanges(ctx, db, changes, &report); err != nil {
		return beadReconcileReport{}, err
	}
	return report, report.ValidationReport.err()
}

func planBeadReconcileChanges(
	sourceBeads map[string]bdExportBead,
	sqliteBeads map[string]sqliteMigrationBead,
	skippedIDs []string,
	includeDeletes bool,
	report *beadReconcileReport,
) beadReconcileChanges {
	changes := beadReconcileChanges{
		inserts: map[string]bdExportBead{},
		updates: map[string]bdExportBead{},
		deletes: map[string]sqliteMigrationBead{},
	}
	for id, source := range sourceBeads {
		recordBeadReconcileUpsert(id, source, sqliteBeads[id], changes, report)
	}
	if includeDeletes {
		recordBeadReconcileDeletes(sourceBeads, sqliteBeads, skippedIDs, changes, report)
	}
	return changes
}

func recordBeadReconcileUpsert(
	id string,
	source bdExportBead,
	current sqliteMigrationBead,
	changes beadReconcileChanges,
	report *beadReconcileReport,
) {
	if current.BDExportBead.ID == "" {
		report.Inserts++
		changes.inserts[id] = source
		return
	}
	if current.Deleted {
		report.Updates++
		changes.updates[id] = source
		return
	}
	cmp := compareMigrationTimestamps(source.UpdatedAt, current.UpdatedAt)
	switch {
	case cmp > 0:
		report.Updates++
		changes.updates[id] = source
	case cmp == 0 && !migrationBeadsEquivalent(source, current):
		report.Conflicts++
		report.ConflictedIDs = append(report.ConflictedIDs, id)
		changes.updates[id] = source
	}
}

func recordBeadReconcileDeletes(
	sourceBeads map[string]bdExportBead,
	sqliteBeads map[string]sqliteMigrationBead,
	skippedIDs []string,
	changes beadReconcileChanges,
	report *beadReconcileReport,
) {
	sourceIDs := make(map[string]struct{}, len(sourceBeads)+len(skippedIDs))
	for id := range sourceBeads {
		sourceIDs[id] = struct{}{}
	}
	for _, id := range skippedIDs {
		sourceIDs[id] = struct{}{}
	}
	for id, current := range sqliteBeads {
		if current.Deleted {
			continue
		}
		if _, ok := sourceIDs[id]; ok {
			continue
		}
		report.Deletes++
		changes.deletes[id] = current
	}
}

func applyBeadReconcileChanges(ctx context.Context, db *sql.DB, changes beadReconcileChanges, report *beadReconcileReport) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reconcile transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := setBeadParentTouchTriggers(ctx, tx, false); err != nil {
		return err
	}
	for _, source := range changes.inserts {
		if err := insertMigratedBeadAtomically(ctx, tx, source); err != nil {
			report.ValidationReport.Errors = append(report.ValidationReport.Errors, fmt.Sprintf("%s: %v", migrationRowLabel(source.ID), err))
			continue
		}
	}
	for _, source := range changes.updates {
		if err := updateMigratedBeadAtomically(ctx, tx, source); err != nil {
			report.ValidationReport.Errors = append(report.ValidationReport.Errors, fmt.Sprintf("%s: %v", migrationRowLabel(source.ID), err))
			continue
		}
	}
	for id := range changes.deletes {
		if _, err := tx.ExecContext(ctx, `UPDATE beads SET deleted=1 WHERE id=?`, id); err != nil {
			return fmt.Errorf("soft-delete bead %s: %w", id, err)
		}
	}
	if err := setBeadParentTouchTriggers(ctx, tx, true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reconcile transaction: %w", err)
	}
	return nil
}

type sqliteMigrationBead struct {
	BDExportBead bdExportBead
	UpdatedAt    string
	Deleted      bool
}

func loadSQLiteMigrationBeads(ctx context.Context, db *sql.DB) (map[string]sqliteMigrationBead, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, title, description, acceptance_criteria, status, priority, type, parent_id,
       owner, estimated_minutes, tier, model, deferred_until, close_reason,
       created_at, updated_at, closed_at, deleted
FROM beads`)
	if err != nil {
		return nil, fmt.Errorf("query sqlite migration beads: %w", err)
	}
	defer rows.Close()

	out := map[string]sqliteMigrationBead{}
	for rows.Next() {
		bead, err := scanSQLiteMigrationBead(rows)
		if err != nil {
			return nil, err
		}
		out[bead.BDExportBead.ID] = bead
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite migration beads: %w", err)
	}
	for id, bead := range out {
		if err := hydrateSQLiteMigrationBead(ctx, db, id, &bead); err != nil {
			return nil, err
		}
		out[id] = bead
	}
	return out, nil
}

func scanSQLiteMigrationBead(rows *sql.Rows) (sqliteMigrationBead, error) {
	var bead bdExportBead
	var parentID, owner, tier, model, deferredUntil, closeReason, closedAt sql.NullString
	var estimatedMinutes sql.NullInt64
	var deleted int
	if err := rows.Scan(
		&bead.ID,
		&bead.Title,
		&bead.Description,
		&bead.AcceptanceCriteria,
		&bead.Status,
		&bead.Priority,
		&bead.IssueType,
		&parentID,
		&owner,
		&estimatedMinutes,
		&tier,
		&model,
		&deferredUntil,
		&closeReason,
		&bead.CreatedAt,
		&bead.UpdatedAt,
		&closedAt,
		&deleted,
	); err != nil {
		return sqliteMigrationBead{}, fmt.Errorf("scan sqlite migration bead: %w", err)
	}
	bead.ParentID = nullStringValue(parentID)
	bead.Owner = nullStringValue(owner)
	bead.EstimatedMinutes = int(estimatedMinutes.Int64)
	bead.Tier = nullStringValue(tier)
	bead.Model = nullStringValue(model)
	bead.DeferredUntil = nullStringValue(deferredUntil)
	bead.CloseReason = nullStringValue(closeReason)
	bead.ClosedAt = nullStringValue(closedAt)
	return sqliteMigrationBead{BDExportBead: bead, UpdatedAt: bead.UpdatedAt, Deleted: deleted != 0}, nil
}

func hydrateSQLiteMigrationBead(ctx context.Context, db *sql.DB, id string, bead *sqliteMigrationBead) error {
	deps, err := loadSQLiteMigrationDeps(ctx, db, id)
	if err != nil {
		return err
	}
	tags, err := loadSQLiteStrings(ctx, db, "bead_tags", "tag", id)
	if err != nil {
		return err
	}
	labels, err := loadSQLiteStrings(ctx, db, "bead_labels", "label", id)
	if err != nil {
		return err
	}
	metadata, err := loadSQLiteMetadata(ctx, db, id)
	if err != nil {
		return err
	}
	notes, err := loadSQLiteStrings(ctx, db, "bead_notes", "content", id)
	if err != nil {
		return err
	}
	bead.BDExportBead.Dependencies = deps
	bead.BDExportBead.Tags = tags
	bead.BDExportBead.Labels = labels
	bead.BDExportBead.Metadata = metadata
	if len(notes) > 0 {
		encoded, err := json.Marshal(notes)
		if err != nil {
			return fmt.Errorf("encode sqlite notes for %s: %w", id, err)
		}
		bead.BDExportBead.Notes = encoded
	}
	return nil
}

func compareMigrationTimestamps(left, right string) int {
	return migrationTimestampSecond(left).Compare(migrationTimestampSecond(right))
}

func parseMigrationTimestamp(value string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}

func migrationTimestampSecond(value string) time.Time {
	return parseMigrationTimestamp(value).UTC().Truncate(time.Second)
}

func openReconcileStateDB(path string, apply bool) (*sql.DB, error) {
	if apply {
		return openStateDB(path)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat sqlite state db: %w", err)
	}
	dbURL := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dbURL.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite state db read-only: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite state db read-only: %w", err)
	}
	return db, nil
}

func migrationBeadsEquivalent(source bdExportBead, current sqliteMigrationBead) bool {
	normalizedSource := normalizeMigrationBeadForCompare(source)
	normalizedCurrent := normalizeMigrationBeadForCompare(current.BDExportBead)
	return normalizedSource == normalizedCurrent
}

type comparableMigrationBead struct {
	ID                 string
	Title              string
	Description        string
	AcceptanceCriteria string
	Status             string
	Priority           int
	Type               string
	ParentID           string
	Owner              string
	EstimatedMinutes   int
	Tier               string
	Model              string
	DeferredUntil      string
	CloseReason        string
	CreatedAt          string
	UpdatedAt          string
	ClosedAt           string
	Dependencies       []string
	Tags               []string
	Labels             []string
	Metadata           map[string]string
	Notes              []string
}

func normalizeMigrationBeadForCompare(bead bdExportBead) string {
	normalizedBead, err := normalizeBDExportBeadForMigration(bead)
	if err == nil {
		bead = normalizedBead
	}

	normalized := comparableMigrationBead{
		ID:                 bead.ID,
		Title:              bead.Title,
		Description:        bead.Description,
		AcceptanceCriteria: bead.AcceptanceCriteria,
		Status:             normalizeMigrationInsertStatus(bead.Status),
		Priority:           bead.Priority,
		Type:               firstNonEmpty(bead.IssueType, bead.Type, "task"),
		ParentID:           firstNonEmpty(bead.ParentID, bead.Parent),
		Owner:              firstNonEmpty(bead.Owner, bead.Assignee),
		EstimatedMinutes:   bead.EstimatedMinutes,
		Tier:               bead.Tier,
		Model:              bead.Model,
		DeferredUntil:      firstNonEmpty(bead.DeferredUntil, bead.DeferUntil),
		CloseReason:        bead.CloseReason,
		CreatedAt:          firstNonEmpty(bead.CreatedAt, bead.UpdatedAt),
		UpdatedAt:          migrationTimestampSecond(firstNonEmpty(bead.UpdatedAt, bead.CreatedAt)).Format(time.RFC3339),
		ClosedAt:           bead.ClosedAt,
		Dependencies:       comparableMigrationDependencies(bead.Dependencies),
		Tags:               sortedNonEmptyCopy(bead.Tags),
		Labels:             sortedNonEmptyCopy(bead.Labels),
		Metadata:           comparableMigrationMetadata(bead.Metadata),
		Notes:              comparableMigrationNotes(bead.Notes),
	}
	encoded, _ := json.Marshal(normalized)
	return string(encoded)
}

func comparableMigrationDependencies(dependencies []protocol.Dependency) []string {
	deps := make([]string, 0, len(dependencies))
	for _, dep := range dependencies {
		if strings.TrimSpace(dep.DependsOnID) == "" {
			continue
		}
		deps = append(deps, dep.DependsOnID+"\x00"+firstNonEmpty(dep.Type, "blocks"))
	}
	return sortedCopy(deps)
}

func comparableMigrationMetadata(source map[string]any) map[string]string {
	metadata := map[string]string{}
	for key, value := range source {
		if strings.TrimSpace(key) == "" {
			continue
		}
		encoded, err := migrationMetadataValue(value)
		if err == nil {
			metadata[key] = encoded
		}
	}
	return metadata
}

func comparableMigrationNotes(raw json.RawMessage) []string {
	notes, _ := migrationNotes(raw)
	return sortedNonEmptyCopy(notes)
}

func normalizeBDExportBeadForMigration(bead bdExportBead) (bdExportBead, error) {
	extractedAC, description, err := beadstore.ExtractAndStripAC(bead.Description)
	if err != nil {
		return bead, fmt.Errorf("extract acceptance criteria: %w", err)
	}
	bead.Description = description
	if strings.TrimSpace(bead.AcceptanceCriteria) == "" {
		bead.AcceptanceCriteria = extractedAC
	}
	return bead, nil
}

func loadSQLiteMigrationDeps(ctx context.Context, db *sql.DB, id string) ([]protocol.Dependency, error) {
	rows, err := db.QueryContext(ctx, `SELECT depends_on_id, type FROM bead_deps WHERE bead_id=? ORDER BY depends_on_id, type`, id)
	if err != nil {
		return nil, fmt.Errorf("query sqlite dependencies for %s: %w", id, err)
	}
	defer rows.Close()
	var deps []protocol.Dependency
	for rows.Next() {
		var dep protocol.Dependency
		if err := rows.Scan(&dep.DependsOnID, &dep.Type); err != nil {
			return nil, fmt.Errorf("scan sqlite dependency for %s: %w", id, err)
		}
		deps = append(deps, dep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite dependencies for %s: %w", id, err)
	}
	return deps, nil
}

func loadSQLiteStrings(ctx context.Context, db *sql.DB, table, column, id string) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE bead_id=? ORDER BY %s`, column, table, column), id)
	if err != nil {
		return nil, fmt.Errorf("query sqlite %s for %s: %w", table, id, err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan sqlite %s for %s: %w", table, id, err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite %s for %s: %w", table, id, err)
	}
	return values, nil
}

func loadSQLiteMetadata(ctx context.Context, db *sql.DB, id string) (map[string]any, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM bead_metadata WHERE bead_id=? ORDER BY key`, id)
	if err != nil {
		return nil, fmt.Errorf("query sqlite metadata for %s: %w", id, err)
	}
	defer rows.Close()
	metadata := map[string]any{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan sqlite metadata for %s: %w", id, err)
		}
		metadata[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite metadata for %s: %w", id, err)
	}
	return metadata, nil
}

func setBeadParentTouchTriggers(ctx context.Context, tx *sql.Tx, enabled bool) error {
	if enabled {
		_, err := tx.ExecContext(ctx, protocol.BeadParentTouchTriggerDDL)
		return wrapIfErr(err, "enable bead parent touch triggers")
	}
	for _, name := range protocol.BeadParentTouchTriggerNames {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
			return fmt.Errorf("disable bead parent touch trigger %s: %w", name, err)
		}
	}
	return nil
}

func readBeadMigrationSource(ctx context.Context, opts beadMigrateOptions, preflightDefaultSource bool, runner beadMigrationCommandRunner) (beadMigrationSource, []byte, beadMigrationPreflight, error) {
	if opts.fromFixture != "" && opts.fromJSONL != "" {
		return beadMigrationSource{}, nil, beadMigrationPreflight{}, fmt.Errorf("--from-fixture and --from-jsonl are mutually exclusive")
	}
	if opts.fromFixture != "" {
		path, err := resolveMigrationFixturePath(opts.fromFixture)
		if err != nil {
			return beadMigrationSource{}, nil, beadMigrationPreflight{}, err
		}
		data, err := os.ReadFile(path) //nolint:gosec // fixture paths are explicit CLI/test inputs.
		if err != nil {
			return beadMigrationSource{}, nil, beadMigrationPreflight{}, fmt.Errorf("read fixture export: %w", err)
		}
		return beadMigrationSource{kind: "fixture", path: path}, data, beadMigrationPreflight{}, nil
	}
	if opts.fromJSONL != "" {
		data, err := os.ReadFile(opts.fromJSONL) //nolint:gosec // --from-jsonl intentionally reads the caller-provided export path.
		if err != nil {
			return beadMigrationSource{}, nil, beadMigrationPreflight{}, fmt.Errorf("read JSONL export: %w", err)
		}
		return beadMigrationSource{kind: "jsonl", path: opts.fromJSONL}, data, beadMigrationPreflight{}, nil
	}

	if runner == nil {
		runner = defaultBeadMigrationRunner
	}
	out, err := runner.Run(ctx, "bd", "export")
	if err != nil {
		return beadMigrationSource{}, nil, beadMigrationPreflight{}, fmt.Errorf("run bd export: %w", err)
	}
	source := beadMigrationSource{kind: "bd export"}
	if cwd, err := os.Getwd(); err == nil {
		source.path = cwd
	}
	if !preflightDefaultSource {
		return source, out, beadMigrationPreflight{}, nil
	}
	preflight, err := runBeadMigrationCorruptionPreflight(ctx, out, runner)
	if err != nil {
		return beadMigrationSource{}, nil, beadMigrationPreflight{}, err
	}
	return source, out, preflight, nil
}

func runBeadMigrationCorruptionPreflight(ctx context.Context, export []byte, runner beadMigrationCommandRunner) (beadMigrationPreflight, error) {
	exportCount, err := countBDExportRowsForPreflight(export)
	if err != nil {
		return beadMigrationPreflight{}, fmt.Errorf("decode bd export for pre-flight: %w", err)
	}
	preflight := beadMigrationPreflight{
		checked:     true,
		exportCount: exportCount,
	}
	out, doltErr := runner.Run(ctx, "dolt", "sql", "--result-format", "json", "-q", "SELECT COUNT(*) AS count FROM beads;")
	if doltErr != nil {
		return beadMigrationPreflightWithDoltErr(preflight, doltErr), nil
	}
	count, parseErr := parseDoltBeadCount(out)
	if parseErr != nil {
		return beadMigrationPreflightWithDoltErr(preflight, parseErr), nil
	}
	preflight.doltCount = count
	return preflight, nil
}

func beadMigrationPreflightWithDoltErr(preflight beadMigrationPreflight, err error) beadMigrationPreflight {
	preflight.doltErr = err
	return preflight
}

func countBDExportRowsForPreflight(data []byte) (int, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return 0, fmt.Errorf("bd export is empty")
	}
	if trimmed[0] == '[' {
		var rows []json.RawMessage
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return 0, fmt.Errorf("decode bd export JSON array: %w", err)
		}
		return len(rows), nil
	}
	count := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count, nil
}

func reportBeadMigrationPreflight(w io.Writer, source beadMigrationSource, preflight beadMigrationPreflight, forceRecover bool) error {
	if !preflight.checked {
		return nil
	}
	failed := preflight.doltErr != nil || preflight.doltCount != preflight.exportCount
	if !failed {
		return nil
	}
	var b strings.Builder
	if forceRecover {
		b.WriteString("[oro] WARNING: --force-recover is enabled; data loss acknowledged.\n")
	} else {
		b.WriteString("[oro] Pre-flight detected possible partial dolt corruption.\n")
	}
	fmt.Fprintf(&b, "[oro] source: %s", source.kind)
	if source.path != "" {
		fmt.Fprintf(&b, " (%s)", source.path)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "[oro] bd export count: %d\n", preflight.exportCount)
	if preflight.doltErr != nil {
		fmt.Fprintf(&b, "[oro] dolt internal count error: %v\n", preflight.doltErr)
	} else {
		fmt.Fprintf(&b, "[oro] dolt internal count: %d\n", preflight.doltCount)
		if preflight.doltCount > preflight.exportCount {
			fmt.Fprintf(&b, "[oro] MISMATCH: dolt has %d more beads than bd export returned.\n", preflight.doltCount-preflight.exportCount)
		} else {
			fmt.Fprintf(&b, "[oro] MISMATCH: bd export returned %d more beads than dolt counted.\n", preflight.exportCount-preflight.doltCount)
		}
	}
	b.WriteString("[oro] This indicates partial dolt corruption or an unreadable Dolt source.\n")
	if !forceRecover {
		b.WriteString("[oro] Override with --force-recover to migrate the readable bd export rows.\n")
		b.WriteString("[oro] Aborting.\n")
		fmt.Fprint(w, b.String())
		return errors.New("pre-flight detected possible partial dolt corruption; rerun with --force-recover to acknowledge possible data loss")
	}
	fmt.Fprint(w, b.String())
	return nil
}

func parseDoltBeadCount(out []byte) (int, error) {
	var result struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		return 0, fmt.Errorf("parse dolt count JSON: %w", err)
	}
	if len(result.Rows) == 0 {
		return 0, fmt.Errorf("parse dolt count JSON: no rows")
	}
	for _, value := range result.Rows[0] {
		switch v := value.(type) {
		case float64:
			return int(v), nil
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return 0, fmt.Errorf("parse dolt count value %q: %w", v, err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("parse dolt count JSON: no numeric count field")
}

func resolveMigrationFixturePath(path string) (string, error) {
	info, err := os.Stat(path) //nolint:gosec // fixture path is an explicit CLI/test input.
	if err != nil {
		return "", fmt.Errorf("stat fixture: %w", err)
	}
	if !info.IsDir() {
		return path, nil
	}
	for _, name := range []string{"export.jsonl", "beads.jsonl"} {
		candidate := filepath.Join(path, name)
		if _, err := os.Stat(candidate); err == nil { //nolint:gosec // candidate stays within the explicit fixture directory.
			return candidate, nil
		}
	}
	return "", fmt.Errorf("fixture %s does not contain export.jsonl or beads.jsonl", path)
}

func planBeadMigration(data []byte) (beadMigrationPlan, error) {
	beads, report, err := validateBDExportForMigration(data)
	if err != nil {
		return beadMigrationPlan{}, err
	}

	plan := beadMigrationPlan{
		UnknownFields: report.UnknownFields,
		Errors:        report.Errors,
		Warnings:      report.Warnings,
		StatusCounts:  map[string]int{},
	}
	for _, bead := range beads {
		plan.Beads++
		plan.Dependencies += len(bead.Dependencies)
		plan.Tags += len(bead.Tags)
		plan.Labels += len(bead.Labels)
		plan.MetadataEntries += len(bead.Metadata)
		notes, err := countMigrationNotes(bead.Notes)
		if err != nil {
			return beadMigrationPlan{}, fmt.Errorf("count notes for %s: %w", bead.ID, err)
		}
		plan.Notes += notes
		plan.StatusCounts[normalizeMigrationStatus(bead.Status)]++
	}
	return plan, report.err()
}

func validateBDExportForMigration(data []byte) ([]bdExportBead, beadMigrationValidationReport, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, beadMigrationValidationReport{}, fmt.Errorf("bd export is empty")
	}
	if trimmed[0] == '[' {
		var rows []json.RawMessage
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, beadMigrationValidationReport{}, fmt.Errorf("decode bd export JSON array: %w", err)
		}
		beads := make([]bdExportBead, 0, len(rows))
		report := beadMigrationValidationReport{SourceRows: len(rows)}
		for i, raw := range rows {
			bead, ok := validateBDExportRow(raw, fmt.Sprintf("row %d", i+1), &report)
			if ok {
				beads = append(beads, bead)
			}
		}
		report.ValidRows = len(beads)
		return beads, report, nil
	}

	beads := []bdExportBead{}
	var report beadMigrationValidationReport
	for lineNumber, line := range bytes.Split(data, []byte("\n")) {
		trimmedLine := bytes.TrimSpace(line)
		if len(trimmedLine) == 0 {
			continue
		}
		report.SourceRows++
		var raw json.RawMessage
		if err := json.Unmarshal(trimmedLine, &raw); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("line %d: decode bd export JSONL: %v", lineNumber+1, err))
			continue
		}
		bead, ok := validateBDExportRow(raw, fmt.Sprintf("line %d", lineNumber+1), &report)
		if ok {
			beads = append(beads, bead)
		}
	}
	report.ValidRows = len(beads)
	return beads, report, nil
}

func validateBDExportRow(raw json.RawMessage, label string, report *beadMigrationValidationReport) (bdExportBead, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: decode bd export bead: %v", label, err))
		return bdExportBead{}, false
	}
	rawID := rawBDExportID(fields)
	var bead bdExportBead
	recordSkippedID := func() {
		if rawID != "" {
			report.SkippedIDs = append(report.SkippedIDs, rawID)
		}
	}
	if err := json.Unmarshal(raw, &bead); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: decode bd export bead: %v", label, err))
		recordSkippedID()
		return bdExportBead{}, false
	}
	report.UnknownFields += countUnknownBDExportFields(fields)
	bead.Priority = defaultBDExportPriority(fields, bead.Priority)
	status, err := normalizeMigrationStatusStrict(bead.Status)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", label, err))
		recordSkippedID()
		return bdExportBead{}, false
	}
	if status == "blocked" {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s: status %q will be stored as open because native blocked state is derived from dependencies", label, bead.Status))
	}
	bead.Status = status
	if strings.TrimSpace(bead.ID) == "" {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: bd export bead is missing id", label))
		return bdExportBead{}, false
	}
	if strings.TrimSpace(bead.Title) == "" {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: bd export bead %s is missing title", label, bead.ID))
		recordSkippedID()
		return bdExportBead{}, false
	}
	if firstNonEmpty(bead.CreatedAt, bead.UpdatedAt) == "" || firstNonEmpty(bead.UpdatedAt, bead.CreatedAt) == "" {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: bd export bead %s is missing created_at or updated_at", label, bead.ID))
		recordSkippedID()
		return bdExportBead{}, false
	}
	if _, err := countMigrationNotes(bead.Notes); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: decode notes for %s: %v", label, bead.ID, err))
		recordSkippedID()
		return bdExportBead{}, false
	}
	return bead, true
}

func countUnknownBDExportFields(fields map[string]json.RawMessage) int {
	unknown := 0
	for field := range fields {
		if !knownBDExportField(field) {
			unknown++
		}
	}
	return unknown
}

func defaultBDExportPriority(fields map[string]json.RawMessage, priority int) int {
	raw, ok := fields["priority"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 2
	}
	return priority
}

func rawBDExportID(fields map[string]json.RawMessage) string {
	raw, ok := fields["id"]
	if !ok {
		return ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

func decodeBDExport(data []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("bd export is empty")
	}
	if trimmed[0] == '[' {
		var rows []json.RawMessage
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("decode bd export JSON array: %w", err)
		}
		return rows, nil
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rows []json.RawMessage
	for {
		var row json.RawMessage
		if err := dec.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode bd export JSONL: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func countMigrationNotes(raw json.RawMessage) (int, error) {
	notes, err := migrationNotes(raw)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, note := range notes {
		if strings.TrimSpace(note) != "" {
			count++
		}
	}
	return count, nil
}

func normalizeMigrationStatus(status string) string {
	normalized, err := normalizeMigrationStatusStrict(status)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(status))
	}
	return normalized
}

func normalizeMigrationStatusStrict(status string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(status)); normalized {
	case "", "open", "pending", "to-do":
		return "open", nil
	case "in_progress", "blocked", "closed":
		return normalized, nil
	default:
		return "", fmt.Errorf("unknown status %q", status)
	}
}

func normalizeMigrationInsertStatus(status string) string {
	switch normalizeMigrationStatus(status) {
	case "in_progress", "closed":
		return normalizeMigrationStatus(status)
	default:
		return "open"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func emptyStringToNil(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func positiveIntToNil(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func sortedNonEmptyCopy(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func migrationMetadataValue(value any) (string, error) {
	if s, ok := value.(string); ok {
		return s, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode migration metadata value: %w", err)
	}
	return string(encoded), nil
}

func migrationNotes(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var noteString string
	if err := json.Unmarshal(trimmed, &noteString); err == nil {
		return []string{noteString}, nil
	}
	var noteStrings []string
	if err := json.Unmarshal(trimmed, &noteStrings); err == nil {
		return noteStrings, nil
	}
	var notes []struct {
		Content string `json:"content"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &notes); err != nil {
		return nil, fmt.Errorf("decode migration notes: %w", err)
	}
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		out = append(out, firstNonEmpty(note.Content, note.Text))
	}
	return out, nil
}

func knownBDExportField(field string) bool {
	switch field {
	case "id", "title", "description", "acceptance_criteria", "status", "priority",
		"type", "issue_type", "parent", "parent_id", "owner", "assignee", "estimated_minutes",
		"tier", "model", "created_at", "updated_at", "closed_at", "close_reason",
		"deferred_until", "defer_until", "dependencies", "tags", "labels",
		"metadata", "notes":
		return true
	default:
		return false
	}
}

func writeBeadMigrationPlan(w io.Writer, source beadMigrationSource, plan beadMigrationPlan) {
	fmt.Fprintln(w, "Migration plan")
	if source.path != "" {
		fmt.Fprintf(w, "source: %s (%s)\n", source.kind, source.path)
	} else {
		fmt.Fprintf(w, "source: %s\n", source.kind)
	}
	fmt.Fprintf(w, "beads: %d\n", plan.Beads)
	fmt.Fprintf(w, "dependencies: %d\n", plan.Dependencies)
	fmt.Fprintf(w, "tags: %d\n", plan.Tags)
	fmt.Fprintf(w, "labels: %d\n", plan.Labels)
	fmt.Fprintf(w, "metadata entries: %d\n", plan.MetadataEntries)
	fmt.Fprintf(w, "notes: %d\n", plan.Notes)
	writeBeadMigrationValidationReport(w, beadMigrationValidationReport{
		UnknownFields: plan.UnknownFields,
		Errors:        plan.Errors,
		Warnings:      plan.Warnings,
	})
	fmt.Fprintln(w, "DRY RUN -- no writes performed")
}

func writeBeadMigrationValidationReport(w io.Writer, report beadMigrationValidationReport) {
	if report.UnknownFields > 0 {
		fmt.Fprintf(w, "unknown fields: %d\n", report.UnknownFields)
	}
	if len(report.Errors) > 0 {
		fmt.Fprintf(w, "migration errors: %d\n", len(report.Errors))
		for _, err := range report.Errors {
			fmt.Fprintf(w, "migration error: %s\n", err)
		}
	}
	if len(report.Warnings) == 0 {
		return
	}
	fmt.Fprintf(w, "migration warnings: %d\n", len(report.Warnings))
	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "migration warning: %s\n", warning)
	}
}

func writeBeadMigrationReport(w io.Writer, report beadMigrationValidationReport) {
	if report.BackupPath != "" {
		fmt.Fprintf(w, "backup snapshot: %s\n", report.BackupPath)
	}
	fmt.Fprintf(w, "source rows: %d\n", report.SourceRows)
	fmt.Fprintf(w, "valid rows: %d\n", report.ValidRows)
	fmt.Fprintf(w, "imported rows: %d\n", report.ImportedRows)
	if report.Verification == "" {
		report.Verification = "SKIPPED"
	}
	fmt.Fprintf(w, "verification: %s", report.Verification)
	if report.Verification == "OK" {
		fmt.Fprintf(w, " (sqlite rows: %d)", report.VerifiedRows)
	}
	fmt.Fprintln(w)
	writeBeadMigrationValidationReport(w, report)
}

func (report beadMigrationValidationReport) err() error {
	if len(report.Errors) == 0 {
		return nil
	}
	return beadMigrationValidationError{count: len(report.Errors)}
}

func migrationRowLabel(id string) string {
	if strings.TrimSpace(id) == "" {
		return "row"
	}
	return fmt.Sprintf("row %s", id)
}

func writeBeadReconcileReport(w io.Writer, source beadMigrationSource, report beadReconcileReport, apply bool) {
	fmt.Fprintln(w, "Reconcile plan")
	if source.path != "" {
		fmt.Fprintf(w, "source: %s (%s)\n", source.kind, source.path)
	} else {
		fmt.Fprintf(w, "source: %s\n", source.kind)
	}
	fmt.Fprintf(w, "bd beads: %d\n", report.SourceBeads)
	fmt.Fprintf(w, "sqlite beads: %d\n", report.SQLiteBeads)
	if report.BackupPath != "" {
		fmt.Fprintf(w, "backup snapshot: %s\n", report.BackupPath)
	}
	fmt.Fprintf(w, "inserts: %d\n", report.Inserts)
	fmt.Fprintf(w, "updates: %d\n", report.Updates)
	fmt.Fprintf(w, "deletes: %d\n", report.Deletes)
	fmt.Fprintf(w, "conflicts: %d\n", report.Conflicts)
	for _, id := range sortedCopy(report.ConflictedIDs) {
		fmt.Fprintf(w, "conflict: %s\n", id)
	}
	writeBeadMigrationValidationReport(w, report.ValidationReport)
	if apply {
		fmt.Fprintln(w, "APPLIED")
	} else {
		fmt.Fprintln(w, "DRY RUN -- pass --apply to write changes")
	}
}
