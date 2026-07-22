// Package storage persists Oro runtime lifecycle state in a SQLite catalog.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"oro/pkg/dbutil"
)

// CatalogSchemaVersion is the newest storage catalog schema understood by this binary.
const CatalogSchemaVersion = 5

var (
	// ErrCatalogCorrupt reports a catalog SQLite database that cannot be read safely.
	ErrCatalogCorrupt = errors.New("storage catalog corrupt")
	// ErrCatalogUnsupportedVersion reports a catalog newer than this binary understands.
	ErrCatalogUnsupportedVersion = errors.New("storage catalog schema version unsupported")
)

// PauseState is a global admission state recorded for a pause epoch.
type PauseState string

const (
	// Open allows controllers to admit new work.
	Open PauseState = "open"
	// PauseRequested stops new admissions while controllers drain active work.
	PauseRequested PauseState = "pause_requested"
	// Paused confirms a controller has drained the requested epoch.
	Paused PauseState = "paused"
	// Resuming keeps admission closed while controllers verify stable headroom.
	Resuming PauseState = "resuming"
)

// LeaseID uniquely identifies one persisted runtime lease.
type LeaseID string

// LeaseRequest identifies a runtime namespace and the process allowed to use it.
type LeaseRequest struct {
	ID                                    LeaseID
	Namespace, ScratchPath                string
	ControllerID, OwnerID                 string
	PID                                   int
	ProcessStart, AcquiredAt, HeartbeatAt time.Time
}

// Lease is a persisted runtime lease.
type Lease struct {
	LeaseRequest
	ReleasedAt *time.Time
}

// Controller is a live dispatcher or standalone work controller.
type Controller struct {
	ID, OwnerID   string
	PID           int
	ProcessStart  time.Time
	Identity      ProcessIdentity
	ObservedEpoch int64
	HeartbeatAt   time.Time
	runtime       *controllerRuntime
}

// PauseEpoch records a host-wide admission transition.
type PauseEpoch struct {
	Epoch     int64
	State     PauseState
	CreatedAt time.Time
}

// PauseAcknowledgement records a controller's terminal drain state for an epoch.
type PauseAcknowledgement struct {
	Epoch          int64
	ControllerID   string
	State          PauseState
	AcknowledgedAt time.Time
}

// Tombstone is a retryable retirement record for a namespace.
type Tombstone struct {
	ID, Namespace, Reason, State string
	RetiredAt                    time.Time
	RetryAt                      *time.Time
	Attempts                     int
	BeforeBytes, AfterBytes      int64
}

// ReconciliationCursor bounds a restartable legacy scan.
type ReconciliationCursor struct {
	Name, Cursor, Proof string
	UpdatedAt           time.Time
}

// Catalog owns the host-global durable storage lifecycle database.
type Catalog struct {
	db *sql.DB
}

// DB returns the catalog's underlying SQLite connection pool.
//
//oro:testonly — production wiring lands when the catalog foundation is integrated.
func (c *Catalog) DB() *sql.DB {
	return c.db
}

// Close releases the catalog database resources.
//
//oro:testonly — production wiring lands when the catalog foundation is integrated.
func (c *Catalog) Close() error {
	if err := c.db.Close(); err != nil {
		return fmt.Errorf("close storage catalog: %w", err)
	}
	return nil
}

type catalogTx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type catalogMigrator func(context.Context, catalogTx) error

// OpenCatalog opens the host-global storage catalog and atomically upgrades its schema.
//
//oro:testonly — production wiring lands when the catalog foundation is integrated.
func OpenCatalog(ctx context.Context, path string) (*Catalog, error) {
	return openCatalog(ctx, path, migrateCatalog)
}

func openCatalog(ctx context.Context, path string, migrate catalogMigrator) (*Catalog, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open storage catalog context: %w", err)
	}
	db, err := dbutil.OpenDB(path)
	if err != nil {
		return nil, catalogOpenError(path, err)
	}
	db.SetMaxOpenConns(1)

	if err := migrateCatalogSchema(ctx, db, migrate); err != nil {
		_ = db.Close()
		return nil, catalogOpenError(path, err)
	}
	return &Catalog{db: db}, nil
}

// MigrateCatalog makes the runtime catalog schema canonical and repeatable.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func MigrateCatalog(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("nil catalog db")
	}
	if err := migrateCatalogSchema(ctx, db, migrateCatalog); err != nil {
		return fmt.Errorf("migrate runtime catalog: %w", err)
	}
	return nil
}

func migrateCatalogSchema(ctx context.Context, db *sql.DB, migrate catalogMigrator) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := migrate(ctx, tx); err != nil {
		return fmt.Errorf("migrate catalog schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog migration: %w", err)
	}
	return nil
}

func migrateCatalog(ctx context.Context, tx catalogTx) error {
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read catalog schema version: %w", err)
	}
	if version > CatalogSchemaVersion {
		return fmt.Errorf("%w: %d", ErrCatalogUnsupportedVersion, version)
	}
	if err := applyCatalogSchema(ctx, tx); err != nil {
		return err
	}
	for _, table := range catalogTables() {
		if err := ensureCatalogTable(ctx, tx, table); err != nil {
			return err
		}
	}
	if version != CatalogSchemaVersion {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, CatalogSchemaVersion)); err != nil {
			return fmt.Errorf("set catalog schema version: %w", err)
		}
	}
	return nil
}

func applyCatalogSchema(ctx context.Context, tx catalogTx) error {
	for _, statement := range catalogSchemaStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply catalog schema: %w", err)
		}
	}
	return nil
}

func catalogOpenError(path string, err error) error {
	if isCatalogCorruption(err) {
		return fmt.Errorf("open storage catalog %s: %w", path, errors.Join(ErrCatalogCorrupt, err))
	}
	return fmt.Errorf("open storage catalog %s: %w", path, err)
}

func isCatalogCorruption(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not a database") ||
		strings.Contains(message, "database disk image is malformed") ||
		strings.Contains(message, "database malformed")
}

func catalogSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS providers (
		id TEXT PRIMARY KEY,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
		`CREATE TABLE IF NOT EXISTS namespaces (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
		path TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(provider_id, path)
	)`,
		`CREATE TABLE IF NOT EXISTS leases (
		id TEXT PRIMARY KEY,
		namespace_id TEXT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
		owner_id TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
		`CREATE TABLE IF NOT EXISTS controllers (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
		last_seen_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
		`CREATE TABLE IF NOT EXISTS refs (
		id TEXT PRIMARY KEY,
		namespace_id TEXT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
		ref TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(namespace_id, ref)
	)`,
		`CREATE TABLE IF NOT EXISTS sweeps (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
		started_at TEXT NOT NULL,
		finished_at TEXT,
		status TEXT NOT NULL
	)`,
		`CREATE TABLE IF NOT EXISTS evidence (
		id TEXT PRIMARY KEY,
		sweep_id TEXT REFERENCES sweeps(id) ON DELETE SET NULL,
		kind TEXT NOT NULL,
		payload TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	}
}

// AcquireLease atomically records an active lease for a runtime namespace.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) AcquireLease(ctx context.Context, request LeaseRequest) (Lease, error) {
	if err := validateLeaseRequest(request); err != nil {
		return Lease{}, err
	}
	canonicalScratch, err := canonicalCachePath(request.ScratchPath)
	if err != nil {
		return Lease{}, fmt.Errorf("resolve lease scratch path: %w", err)
	}
	if filepath.Base(canonicalScratch) != request.Namespace {
		return Lease{}, fmt.Errorf("lease namespace does not match scratch path")
	}
	request.ScratchPath = canonicalScratch
	_, err = c.db.ExecContext(ctx, `
INSERT INTO runtime_leases (id, namespace, scratch_path, controller_id, owner_id, pid, process_start, acquired_at, heartbeat_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET namespace=excluded.namespace, scratch_path=excluded.scratch_path, controller_id=excluded.controller_id,
 owner_id=excluded.owner_id, pid=excluded.pid, process_start=excluded.process_start,
 acquired_at=excluded.acquired_at, heartbeat_at=excluded.heartbeat_at, released_at=NULL`,
		request.ID, request.Namespace, request.ScratchPath, request.ControllerID, request.OwnerID, request.PID,
		formatTime(request.ProcessStart), formatTime(request.AcquiredAt), formatTime(request.HeartbeatAt))
	if err != nil {
		return Lease{}, fmt.Errorf("acquire lease %s: %w", request.ID, err)
	}
	return Lease{LeaseRequest: request}, nil
}

// ReleaseLease marks an active lease as released without deleting its audit record.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) ReleaseLease(ctx context.Context, leaseID LeaseID) error {
	if leaseID == "" {
		return fmt.Errorf("empty lease id")
	}
	result, err := c.db.ExecContext(ctx, `UPDATE runtime_leases SET released_at=? WHERE id=? AND released_at IS NULL`, formatTime(time.Now().UTC()), leaseID)
	if err != nil {
		return fmt.Errorf("release lease %s: %w", leaseID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("release lease %s rows affected: %w", leaseID, err)
	}
	if changed == 0 {
		return fmt.Errorf("release lease %s: not active", leaseID)
	}
	return nil
}

// Lease loads one persisted lease.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) Lease(ctx context.Context, id LeaseID) (Lease, error) {
	row := c.db.QueryRowContext(ctx, `SELECT id, namespace, scratch_path, controller_id, owner_id, pid, process_start, acquired_at, heartbeat_at, released_at FROM runtime_leases WHERE id=?`, id)
	return scanLease(row)
}

// UpsertController persists a live controller heartbeat and observed epoch.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) UpsertController(ctx context.Context, controller Controller) error {
	if controller.ID == "" || controller.OwnerID == "" || controller.PID <= 0 || controller.ProcessStart.IsZero() || controller.HeartbeatAt.IsZero() {
		return fmt.Errorf("invalid controller")
	}
	if !controller.Identity.Matches(controller.Identity) || controller.Identity.PID != controller.PID {
		return fmt.Errorf("invalid controller identity")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO runtime_controllers (id, owner_id, pid, process_start, identity_start, executable, process_group, observed_epoch, heartbeat_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET owner_id=excluded.owner_id, pid=excluded.pid, process_start=excluded.process_start, identity_start=excluded.identity_start, executable=excluded.executable, process_group=excluded.process_group, observed_epoch=excluded.observed_epoch, heartbeat_at=excluded.heartbeat_at`, controller.ID, controller.OwnerID, controller.PID, formatTime(controller.ProcessStart), controller.Identity.StartMarker, controller.Identity.Executable, controller.Identity.ProcessGroup, controller.ObservedEpoch, formatTime(controller.HeartbeatAt))
	if err != nil {
		return fmt.Errorf("upsert controller %s: %w", controller.ID, err)
	}
	return nil
}

// Controller loads one persisted controller.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) Controller(ctx context.Context, id string) (Controller, error) {
	var value Controller
	var processStart, heartbeat string
	err := c.db.QueryRowContext(ctx, `SELECT id, owner_id, pid, process_start, identity_start, executable, process_group, observed_epoch, heartbeat_at FROM runtime_controllers WHERE id=?`, id).Scan(&value.ID, &value.OwnerID, &value.PID, &processStart, &value.Identity.StartMarker, &value.Identity.Executable, &value.Identity.ProcessGroup, &value.ObservedEpoch, &heartbeat)
	if err != nil {
		return Controller{}, fmt.Errorf("load controller %s: %w", id, err)
	}
	var parseErr error
	value.ProcessStart, parseErr = parseTime(processStart)
	if parseErr != nil {
		return Controller{}, fmt.Errorf("parse controller %s process start: %w", id, parseErr)
	}
	value.HeartbeatAt, parseErr = parseTime(heartbeat)
	if parseErr != nil {
		return Controller{}, fmt.Errorf("parse controller %s heartbeat: %w", id, parseErr)
	}
	value.Identity.PID = value.PID
	return value, nil
}

// RecordPauseEpoch atomically inserts or updates a global pause epoch.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) RecordPauseEpoch(ctx context.Context, epoch PauseEpoch) error {
	if epoch.Epoch < 0 || epoch.State == "" || epoch.CreatedAt.IsZero() {
		return fmt.Errorf("invalid pause epoch")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO runtime_pause_epochs (epoch, state, created_at) VALUES (?, ?, ?) ON CONFLICT(epoch) DO UPDATE SET state=excluded.state, created_at=excluded.created_at`, epoch.Epoch, epoch.State, formatTime(epoch.CreatedAt))
	if err != nil {
		return fmt.Errorf("record pause epoch %d: %w", epoch.Epoch, err)
	}
	return nil
}

// PauseEpoch loads one pause epoch.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) PauseEpoch(ctx context.Context, number int64) (PauseEpoch, error) {
	var value PauseEpoch
	var createdAt string
	err := c.db.QueryRowContext(ctx, `SELECT epoch, state, created_at FROM runtime_pause_epochs WHERE epoch=?`, number).Scan(&value.Epoch, &value.State, &createdAt)
	if err != nil {
		return PauseEpoch{}, fmt.Errorf("load pause epoch %d: %w", number, err)
	}
	parsed, err := parseTime(createdAt)
	if err != nil {
		return PauseEpoch{}, fmt.Errorf("parse pause epoch %d created at: %w", number, err)
	}
	value.CreatedAt = parsed
	return value, nil
}

// AcknowledgePauseEpoch records a controller acknowledgement for a pause epoch.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) AcknowledgePauseEpoch(ctx context.Context, acknowledgement PauseAcknowledgement) error {
	if acknowledgement.Epoch < 0 || acknowledgement.ControllerID == "" || acknowledgement.State == "" || acknowledgement.AcknowledgedAt.IsZero() {
		return fmt.Errorf("invalid pause acknowledgement")
	}
	result, err := c.db.ExecContext(ctx, `INSERT INTO runtime_pause_acknowledgements (epoch, controller_id, state, acknowledged_at) VALUES (?, ?, ?, ?) ON CONFLICT(epoch, controller_id) DO NOTHING`, acknowledgement.Epoch, acknowledgement.ControllerID, acknowledgement.State, formatTime(acknowledgement.AcknowledgedAt))
	if err != nil {
		return fmt.Errorf("acknowledge pause epoch %d: %w", acknowledgement.Epoch, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("acknowledge pause epoch %d rows affected: %w", acknowledgement.Epoch, err)
	}
	if changed == 0 {
		return fmt.Errorf("acknowledge pause epoch %d: %w", acknowledgement.Epoch, ErrPauseEpochAlreadyAcknowledged)
	}
	return nil
}

// PauseAcknowledgement loads one controller acknowledgement.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) PauseAcknowledgement(ctx context.Context, epoch int64, controllerID string) (PauseAcknowledgement, error) {
	var value PauseAcknowledgement
	var acknowledgedAt string
	err := c.db.QueryRowContext(ctx, `SELECT epoch, controller_id, state, acknowledged_at FROM runtime_pause_acknowledgements WHERE epoch=? AND controller_id=?`, epoch, controllerID).Scan(&value.Epoch, &value.ControllerID, &value.State, &acknowledgedAt)
	if err != nil {
		return PauseAcknowledgement{}, fmt.Errorf("load pause acknowledgement %d/%s: %w", epoch, controllerID, err)
	}
	parsed, err := parseTime(acknowledgedAt)
	if err != nil {
		return PauseAcknowledgement{}, fmt.Errorf("parse pause acknowledgement %d/%s: %w", epoch, controllerID, err)
	}
	value.AcknowledgedAt = parsed
	return value, nil
}

// UpsertTombstone persists retryable retirement state.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) UpsertTombstone(ctx context.Context, tombstone Tombstone) error {
	if tombstone.ID == "" || tombstone.Namespace == "" || tombstone.Reason == "" || tombstone.State == "" || tombstone.RetiredAt.IsZero() {
		return fmt.Errorf("invalid tombstone")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO runtime_tombstones (id, namespace, reason, state, retired_at, retry_at, attempts) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET namespace=excluded.namespace, reason=excluded.reason, state=excluded.state, retired_at=excluded.retired_at, retry_at=excluded.retry_at, attempts=excluded.attempts`, tombstone.ID, tombstone.Namespace, tombstone.Reason, tombstone.State, formatTime(tombstone.RetiredAt), formatOptionalTime(tombstone.RetryAt), tombstone.Attempts)
	if err != nil {
		return fmt.Errorf("upsert tombstone %s: %w", tombstone.ID, err)
	}
	return nil
}

// Tombstone loads one retirement record.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) Tombstone(ctx context.Context, id string) (Tombstone, error) {
	var value Tombstone
	var retiredAt string
	var retryAt sql.NullString
	err := c.db.QueryRowContext(ctx, `SELECT t.id, t.namespace, t.reason, t.state, t.retired_at, t.retry_at, t.attempts, COALESCE(e.before_bytes, 0), COALESCE(e.after_bytes, 0) FROM runtime_tombstones AS t LEFT JOIN runtime_tombstone_deletion_evidence AS e ON e.tombstone_id=t.id WHERE t.id=?`, id).Scan(&value.ID, &value.Namespace, &value.Reason, &value.State, &retiredAt, &retryAt, &value.Attempts, &value.BeforeBytes, &value.AfterBytes)
	if err != nil {
		return Tombstone{}, fmt.Errorf("load tombstone %s: %w", id, err)
	}
	parsed, err := parseTime(retiredAt)
	if err != nil {
		return Tombstone{}, fmt.Errorf("parse tombstone %s retired at: %w", id, err)
	}
	value.RetiredAt = parsed
	if retryAt.Valid {
		parsed, err = parseTime(retryAt.String)
		if err != nil {
			return Tombstone{}, fmt.Errorf("parse tombstone %s retry at: %w", id, err)
		}
		value.RetryAt = &parsed
	}
	return value, nil
}

func (c *Catalog) recordTombstoneDeletionProgress(ctx context.Context, tombstoneID string, beforeBytes, afterBytes int64) error {
	if tombstoneID == "" || beforeBytes < 0 || afterBytes < 0 {
		return fmt.Errorf("invalid tombstone deletion evidence")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO runtime_tombstone_deletion_evidence (tombstone_id, before_bytes, after_bytes, recorded_at) VALUES (?, ?, ?, ?) ON CONFLICT(tombstone_id) DO UPDATE SET after_bytes=excluded.after_bytes, recorded_at=excluded.recorded_at`, tombstoneID, beforeBytes, afterBytes, formatTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("record tombstone deletion evidence %s: %w", tombstoneID, err)
	}
	return nil
}

// SaveReconciliationCursor persists the bounded scan checkpoint and proof.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) SaveReconciliationCursor(ctx context.Context, cursor ReconciliationCursor) error {
	if cursor.Name == "" || cursor.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid reconciliation cursor")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO runtime_reconciliation_cursors (name, cursor, proof, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET cursor=excluded.cursor, proof=excluded.proof, updated_at=excluded.updated_at`, cursor.Name, cursor.Cursor, cursor.Proof, formatTime(cursor.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save reconciliation cursor %s: %w", cursor.Name, err)
	}
	return nil
}

// ReconciliationCursor loads one bounded scan checkpoint.
//
//oro:testonly — production wiring lands in dependent runtime lifecycle tasks.
func (c *Catalog) ReconciliationCursor(ctx context.Context, name string) (ReconciliationCursor, error) {
	var value ReconciliationCursor
	var updatedAt string
	err := c.db.QueryRowContext(ctx, `SELECT name, cursor, proof, updated_at FROM runtime_reconciliation_cursors WHERE name=?`, name).Scan(&value.Name, &value.Cursor, &value.Proof, &updatedAt)
	if err != nil {
		return ReconciliationCursor{}, fmt.Errorf("load reconciliation cursor %s: %w", name, err)
	}
	parsed, err := parseTime(updatedAt)
	if err != nil {
		return ReconciliationCursor{}, fmt.Errorf("parse reconciliation cursor %s updated at: %w", name, err)
	}
	value.UpdatedAt = parsed
	return value, nil
}

type catalogTable struct {
	name, ddl string
}

func catalogTables() []catalogTable {
	return []catalogTable{
		{"runtime_leases", `CREATE TABLE runtime_leases (id TEXT PRIMARY KEY, namespace TEXT NOT NULL, scratch_path TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL, pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL, heartbeat_at TEXT NOT NULL, released_at TEXT)`},
		{"runtime_controllers", `CREATE TABLE runtime_controllers (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, pid INTEGER NOT NULL, process_start TEXT NOT NULL, identity_start TEXT NOT NULL DEFAULT '', executable TEXT NOT NULL DEFAULT '', process_group INTEGER NOT NULL DEFAULT 0, observed_epoch INTEGER NOT NULL, heartbeat_at TEXT NOT NULL)`},
		{"runtime_pause_epochs", `CREATE TABLE runtime_pause_epochs (epoch INTEGER PRIMARY KEY, state TEXT NOT NULL, created_at TEXT NOT NULL)`},
		{"runtime_pause_acknowledgements", `CREATE TABLE runtime_pause_acknowledgements (epoch INTEGER NOT NULL, controller_id TEXT NOT NULL, state TEXT NOT NULL, acknowledged_at TEXT NOT NULL, PRIMARY KEY (epoch, controller_id))`},
		{"runtime_tombstones", `CREATE TABLE runtime_tombstones (id TEXT PRIMARY KEY, namespace TEXT NOT NULL, reason TEXT NOT NULL, state TEXT NOT NULL, retired_at TEXT NOT NULL, retry_at TEXT, attempts INTEGER NOT NULL DEFAULT 0)`},
		{"runtime_tombstone_deletion_evidence", `CREATE TABLE runtime_tombstone_deletion_evidence (tombstone_id TEXT PRIMARY KEY REFERENCES runtime_tombstones(id) ON DELETE CASCADE, before_bytes INTEGER NOT NULL, after_bytes INTEGER NOT NULL, recorded_at TEXT NOT NULL)`},
		{"runtime_reconciliation_cursors", `CREATE TABLE runtime_reconciliation_cursors (name TEXT PRIMARY KEY, cursor TEXT NOT NULL, proof TEXT NOT NULL, updated_at TEXT NOT NULL)`},
	}
}

func ensureCatalogTable(ctx context.Context, tx catalogTx, table catalogTable) error {
	var schema string
	err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table.name).Scan(&schema)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, table.ddl); err != nil {
			return fmt.Errorf("create catalog table %s: %w", table.name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect catalog table %s: %w", table.name, err)
	}
	if schema == table.ddl {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE `+table.name); err != nil {
		return fmt.Errorf("drop stale catalog table %s: %w", table.name, err)
	}
	if _, err := tx.ExecContext(ctx, table.ddl); err != nil {
		return fmt.Errorf("create catalog table %s: %w", table.name, err)
	}
	return nil
}

func validateLeaseRequest(request LeaseRequest) error {
	if request.ID == "" || request.Namespace == "" || request.ScratchPath == "" || request.ControllerID == "" || request.OwnerID == "" || request.PID <= 0 || request.ProcessStart.IsZero() || request.AcquiredAt.IsZero() || request.HeartbeatAt.IsZero() {
		return fmt.Errorf("invalid lease request")
	}
	return nil
}
func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func formatOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed, nil
}

func scanLease(row *sql.Row) (Lease, error) {
	var value Lease
	var processStart, acquiredAt, heartbeatAt string
	var releasedAt sql.NullString
	err := row.Scan(&value.ID, &value.Namespace, &value.ScratchPath, &value.ControllerID, &value.OwnerID, &value.PID, &processStart, &acquiredAt, &heartbeatAt, &releasedAt)
	if err != nil {
		return Lease{}, fmt.Errorf("load lease: %w", err)
	}
	var parseErr error
	value.ProcessStart, parseErr = parseTime(processStart)
	if parseErr != nil {
		return Lease{}, fmt.Errorf("parse lease process start: %w", parseErr)
	}
	value.AcquiredAt, parseErr = parseTime(acquiredAt)
	if parseErr != nil {
		return Lease{}, fmt.Errorf("parse lease acquired at: %w", parseErr)
	}
	value.HeartbeatAt, parseErr = parseTime(heartbeatAt)
	if parseErr != nil {
		return Lease{}, fmt.Errorf("parse lease heartbeat: %w", parseErr)
	}
	if releasedAt.Valid {
		parsed, err := parseTime(releasedAt.String)
		if err != nil {
			return Lease{}, fmt.Errorf("parse lease released at: %w", err)
		}
		value.ReleasedAt = &parsed
	}
	return value, nil
}
