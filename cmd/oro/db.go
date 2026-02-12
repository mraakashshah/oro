package main

import (
	"context"
	"database/sql"
	"fmt"

	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// openDB opens a SQLite database at path and enforces production-safe
// defaults: WAL journal mode and a 5-second busy timeout. It also calls
// db.PingContext to verify the connection is usable before returning.
func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	ctx := context.Background()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode on %s: %w", path, err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout on %s: %w", path, err)
	}

	return db, nil
}

// migrateStateDB applies schema migrations to the dispatcher state database.
// Each migration uses ALTER TABLE which errors if the column already exists;
// errors are intentionally ignored (try/ignore pattern).
func migrateStateDB(db *sql.DB) {
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, protocol.MigrateAssignmentCounts)
	_, _ = db.ExecContext(ctx, protocol.MigrateFileTracking)
	_, _ = db.ExecContext(ctx, protocol.MigratePinnedMemories)
}
