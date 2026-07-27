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

func TestOpenCatalogMigratesFoundationAndRuntimeSchema(t *testing.T) {
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
		"weekly_dev_cache_schedule",
		"runtime_leases",
		"runtime_controllers",
		"runtime_pause_epochs",
		"runtime_pause_acknowledgements",
		"runtime_tombstones",
		"runtime_reconciliation_cursors",
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
	if version != CatalogSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, CatalogSchemaVersion)
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
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
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
	if version != 1 {
		t.Fatalf("schema version = %d, want 1 after rollback", version)
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
