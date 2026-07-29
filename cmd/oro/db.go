package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/beadstore/migrations"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
	"oro/pkg/storage"
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

// openStorageCatalog opens the host-global catalog associated with oroHome.
func openStorageCatalog(ctx context.Context, oroHome string) (*storage.Catalog, error) {
	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		return nil, fmt.Errorf("resolve storage paths: %w", err)
	}
	catalog, err := storage.OpenCatalog(ctx, paths.CatalogPath)
	if err != nil {
		return nil, fmt.Errorf("open storage catalog: %w", err)
	}
	return catalog, nil
}

// runStartupDevCacheSweep runs one due developer-tool cache sweep during
// `oro start`. Every failure is a warning, never a boot failure: cache
// maintenance is housekeeping and must not prevent the factory from starting.
// startupDevCacheSweepBudget bounds how long boot will spend on cache
// maintenance. The sweep runs before the dispatcher opens its socket, so an
// unbounded sweep is a boot failure waiting to happen: a 52 GB Go build cache
// made `go clean -cache` outlast oro start's socket-readiness wait on
// 2026-07-29 and the launch reported failure. Exceeding the budget cancels the
// cleaner mid-run, which is safe — a partially pruned cache is still a valid
// cache, and the size trigger simply resumes the sweep on the next start.
const startupDevCacheSweepBudget = 20 * time.Second

func runStartupDevCacheSweep(catalog *storage.Catalog, oroHome string) {
	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: resolve storage paths for dev-cache sweep: %v\n", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), startupDevCacheSweepBudget)
	defer cancel()
	if _, err := storage.RunWeeklyDevCacheSweep(ctx, storage.WeeklyDevCacheSweepRequest{
		Catalog:   catalog,
		LockPath:  paths.LockPath,
		Providers: storage.BuiltinProviders(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: run dev-cache sweep: %v\n", err)
	}
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
	if err := migrations.MigrateToV3(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply v3 schema on %s: %w", path, err)
	}
	if err := repairAppliedV4Schema(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("repair v4 schema on %s: %w", path, err)
	}
	if err := beadstore.BackfillJourneyEvents(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("backfill journey events on %s: %w", path, err)
	}

	migrateStateDB(db)

	return db, nil
}

func repairAppliedV4Schema(ctx context.Context, db *sql.DB) error {
	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return fmt.Errorf("inspect state db user_version: %w", err)
	}
	if userVersion < 4 {
		return nil
	}
	if err := migrations.EnsureV4BeadsFTSTriggers(ctx, db); err != nil {
		return fmt.Errorf("ensure v4 fts triggers: %w", err)
	}
	return nil
}

func openStateDBWithV4Migration(path string) (*sql.DB, error) {
	db, err := openStateDB(path)
	if err != nil {
		return nil, err
	}

	needsV4, err := stateDBNeedsV4Migration(context.Background(), db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !needsV4 {
		return db, nil
	}

	db, backupPath, err := reopenStateDBWithV4Backup(path, db)
	if err != nil {
		return nil, err
	}
	if err := migrations.MigrateToV4(context.Background(), db); err != nil {
		_ = db.Close()
		_ = os.Remove(backupPath)
		return nil, fmt.Errorf("apply v4 schema on %s: %w", path, err)
	}
	return db, nil
}

func reopenStateDBWithV4Backup(path string, db *sql.DB) (*sql.DB, string, error) {
	if _, err := db.ExecContext(context.Background(), `PRAGMA wal_checkpoint(FULL)`); err != nil {
		_ = db.Close()
		return nil, "", fmt.Errorf("checkpoint state db before v4 backup: %w", err)
	}
	_ = db.Close()

	backupPath, err := backupStateDBForV4(path)
	if err != nil {
		return nil, "", err
	}
	db, err = openStateDB(path)
	if err != nil {
		_ = os.Remove(backupPath)
		return nil, "", err
	}
	return db, backupPath, nil
}

func stateDBNeedsV4Migration(ctx context.Context, db *sql.DB) (bool, error) {
	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return false, fmt.Errorf("inspect state db user_version: %w", err)
	}
	if userVersion >= 4 {
		return false, nil
	}
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(beads)`)
	if err != nil {
		return false, fmt.Errorf("inspect beads columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, fmt.Errorf("scan beads column: %w", err)
		}
		if name == "gate_state" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate beads columns: %w", err)
	}
	return false, nil
}

func backupStateDBForV4(path string) (string, error) {
	src, err := os.Open(path) // #nosec G304,G703 -- path is the configured local SQLite state database.
	if err != nil {
		return "", fmt.Errorf("open state db for v4 backup: %w", err)
	}
	defer func() { _ = src.Close() }()

	backupPath := fmt.Sprintf("%s.pre-v4-%s", path, time.Now().UTC().Format("20060102T150405.000000000Z"))
	dst, err := os.OpenFile(backupPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600) // #nosec G304,G703 -- backupPath is derived from the configured local state DB path.
	if err != nil {
		return "", fmt.Errorf("create v4 backup %s: %w", backupPath, err)
	}
	cleanup := true
	defer func() {
		_ = dst.Close()
		if cleanup {
			_ = os.Remove(backupPath)
		}
	}()
	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("copy v4 backup %s: %w", backupPath, err)
	}
	if err := dst.Sync(); err != nil {
		return "", fmt.Errorf("sync v4 backup %s: %w", backupPath, err)
	}
	cleanup = false
	return backupPath, nil
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
	_, _ = db.ExecContext(ctx, protocol.MigrateSemanticMemoryReadEvents)
	_, _ = db.ExecContext(ctx, protocol.MigrateSemanticMemoryChunks)
}
