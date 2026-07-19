package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"oro/pkg/dbutil"
)

const catalogSchemaVersion = 1

var (
	// ErrCatalogCorrupt reports a catalog SQLite database that cannot be read safely.
	ErrCatalogCorrupt = errors.New("storage catalog corrupt")
	// ErrCatalogUnsupportedVersion reports a catalog newer than this binary understands.
	ErrCatalogUnsupportedVersion = errors.New("storage catalog schema version unsupported")
)

// Catalog is the host-global storage metadata database.
type Catalog struct {
	db *sql.DB
}

// DB returns the catalog's underlying SQLite connection pool.
func (c *Catalog) DB() *sql.DB {
	return c.db
}

// Close releases the catalog database resources.
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
	if version > catalogSchemaVersion {
		return fmt.Errorf("%w: %d", ErrCatalogUnsupportedVersion, version)
	}
	if version == catalogSchemaVersion {
		return nil
	}
	if err := applyCatalogSchema(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		return fmt.Errorf("set catalog schema version: %w", err)
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
