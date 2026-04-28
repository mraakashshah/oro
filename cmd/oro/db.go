package main

import (
	"context"
	"database/sql"
	"fmt"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

// openDB opens a SQLite database at path and enforces production-safe
// defaults: WAL journal mode and a 5-second busy timeout. It also calls
// db.PingContext to verify the connection is usable before returning.
func openDB(path string) (*sql.DB, error) {
	db, err := dbutil.OpenDB(path)
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", path, err)
	}
	return db, nil
}

// openStateDB opens the dispatcher state database and ensures the full schema
// (tables, indexes) exists. It wraps openDB with SchemaDDL + migrations so
// that any consumer (oro logs, oro status, buildDispatcher) gets a usable DB
// even if the file was just created. Safe to call on existing DBs — all DDL
// uses CREATE TABLE IF NOT EXISTS.
func openStateDB(path string) (*sql.DB, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}

	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply state schema on %s: %w", path, err)
	}
	if err := protocol.MigrateBeadSchema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply bead schema on %s: %w", path, err)
	}

	migrateStateDB(db)

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
	_, _ = db.ExecContext(ctx, protocol.MigrateKVStore)
	_, _ = db.ExecContext(ctx, protocol.MigrateRejectionHistory)
	_, _ = db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_rejection_bead ON rejection_history(bead_id)")
	// Semantic memory migrations: the overhaul added these tables/columns but
	// they were never wired into the startup path, so HybridSearch telemetry
	// was writing to /dev/null on production state.db. Same try/ignore pattern;
	// the bare ALTER TABLE in MigrateSemanticMemoryDense errors when columns
	// already exist and MigrateSemanticMemoryBackfillState depends on kv_store
	// created by the earlier MigrateKVStore call.
	_, _ = db.ExecContext(ctx, protocol.MigrateSemanticMemoryDense)
	_, _ = db.ExecContext(ctx, protocol.MigrateSemanticMemoryBackfillState)
	_, _ = db.ExecContext(ctx, protocol.MigrateSemanticMemorySearchEvents)
	_, _ = db.ExecContext(ctx, protocol.MigrateSemanticMemoryChunks)
}
