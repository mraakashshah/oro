package storage_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/storage"
)

type runtimeLeaseCatalog interface {
	AcquireLease(context.Context, storage.LeaseRequest) (storage.Lease, error)
	ReleaseLease(context.Context, storage.LeaseID) error
}

var _ runtimeLeaseCatalog = (*storage.Catalog)(nil)

func TestCatalogRuntimeLeaseLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE runtime_leases (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seed stale schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close stale catalog fixture: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, path)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	db = catalog.DB()

	var foundationTable string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='providers'`).Scan(&foundationTable); err != nil {
		t.Fatalf("foundational catalog schema was not preserved: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	lease, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	})
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if lease.ID != "lease-1" || lease.ReleasedAt != nil {
		t.Fatalf("unexpected acquired lease: %+v", lease)
	}
	if err := catalog.UpsertController(ctx, storage.Controller{
		ID: "controller-1", OwnerID: "owner-1", PID: 101, ProcessStart: now.Add(-time.Minute), ObservedEpoch: 7, HeartbeatAt: now,
		Identity: storage.ProcessIdentity{PID: 101, StartMarker: "start-controller-1", Executable: "oro", ProcessGroup: 101},
	}); err != nil {
		t.Fatalf("upsert controller: %v", err)
	}
	if err := catalog.RecordPauseEpoch(ctx, storage.PauseEpoch{Epoch: 7, State: storage.PauseRequested, CreatedAt: now}); err != nil {
		t.Fatalf("record pause epoch: %v", err)
	}
	if err := catalog.AcknowledgePauseEpoch(ctx, storage.PauseAcknowledgement{Epoch: 7, ControllerID: "controller-1", State: storage.Paused, AcknowledgedAt: now}); err != nil {
		t.Fatalf("acknowledge pause epoch: %v", err)
	}
	if err := catalog.UpsertTombstone(ctx, storage.Tombstone{ID: "tombstone-1", Namespace: "repo-a/worktree-a", Reason: "merged", State: "pending", RetiredAt: now}); err != nil {
		t.Fatalf("upsert tombstone: %v", err)
	}
	if err := catalog.SaveReconciliationCursor(ctx, storage.ReconciliationCursor{Name: "legacy-temp", Cursor: "token-99", Proof: "safe", UpdatedAt: now}); err != nil {
		t.Fatalf("save reconciliation cursor: %v", err)
	}
	if err := catalog.ReleaseLease(ctx, lease.ID); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	gotLease, err := catalog.Lease(ctx, lease.ID)
	if err != nil {
		t.Fatalf("load lease: %v", err)
	}
	if gotLease.ReleasedAt == nil || gotLease.Namespace != lease.Namespace || gotLease.OwnerID != lease.OwnerID {
		t.Fatalf("lease did not round-trip: %+v", gotLease)
	}
	if got, err := catalog.Controller(ctx, "controller-1"); err != nil || got.ObservedEpoch != 7 {
		t.Fatalf("controller did not round-trip: %+v, %v", got, err)
	}
	if got, err := catalog.PauseEpoch(ctx, 7); err != nil || got.State != storage.PauseRequested {
		t.Fatalf("pause epoch did not round-trip: %+v, %v", got, err)
	}
	if got, err := catalog.PauseAcknowledgement(ctx, 7, "controller-1"); err != nil || got.State != storage.Paused {
		t.Fatalf("pause acknowledgement did not round-trip: %+v, %v", got, err)
	}
	if got, err := catalog.Tombstone(ctx, "tombstone-1"); err != nil || got.Reason != "merged" {
		t.Fatalf("tombstone did not round-trip: %+v, %v", got, err)
	}
	if got, err := catalog.ReconciliationCursor(ctx, "legacy-temp"); err != nil || got.Cursor != "token-99" || got.Proof != "safe" {
		t.Fatalf("reconciliation cursor did not round-trip: %+v, %v", got, err)
	}

	var columns int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('runtime_leases')`).Scan(&columns); err != nil {
		t.Fatalf("inspect migrated lease schema: %v", err)
	}
	if columns < 9 {
		t.Fatalf("stale runtime_leases schema was not rebuilt, got %d columns", columns)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close incompatible catalog fixture: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, path)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, catalog.DB()); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, db); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, db); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, db); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, db); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, db); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, db); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, db); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, db)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, db); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}
