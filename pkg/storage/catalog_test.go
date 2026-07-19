//nolint:testpackage // rollback coverage injects a failing package-private migrator.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/dbutil"
)

func TestOpenCatalogMigratesSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "catalog.db")
	catalog, err := OpenCatalog(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenCatalog() error = %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	for _, table := range []string{
		"providers",
		"namespaces",
		"leases",
		"controllers",
		"refs",
		"sweeps",
		"evidence",
	} {
		var name string
		err := catalog.DB().QueryRowContext(
			context.Background(),
			`SELECT name FROM sqlite_schema WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("catalog missing %q table: %v", table, err)
		}
	}

	var version int
	if err := catalog.DB().QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version == 0 {
		t.Fatal("schema version = 0, want migrated version")
	}
}

func TestOpenCatalogRejectsCorruptDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "catalog.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write corrupt catalog: %v", err)
	}

	_, err := OpenCatalog(context.Background(), path)
	if !errors.Is(err, ErrCatalogCorrupt) {
		t.Fatalf("OpenCatalog() error = %v, want ErrCatalogCorrupt", err)
	}
}

func TestOpenCatalogRollbackPreservesOriginalVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 7`); err != nil {
		t.Fatalf("set fixture version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}

	_, err = openCatalog(context.Background(), path, func(ctx context.Context, tx catalogTx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE rollback_probe (id INTEGER PRIMARY KEY)`); err != nil {
			return err
		}
		return errors.New("abort migration")
	})
	if err == nil {
		t.Fatal("openCatalog() error = nil, want migration failure")
	}

	db, err = dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("reopen fixture db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read original version: %v", err)
	}
	if version != 7 {
		t.Fatalf("schema version = %d, want 7 after rollback", version)
	}
	var probe string
	err = db.QueryRow(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name = 'rollback_probe'`).Scan(&probe)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rollback_probe lookup error = %v, want sql.ErrNoRows", err)
	}
}

func TestOpenCatalogHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenCatalog() error = %v, want context.Canceled", err)
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
