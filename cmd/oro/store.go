package main

import (
	"context"
	"fmt"
	"os"

	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// defaultMemoryStore opens (or creates) the default SQLite memory store at
// ~/.oro/state.db and ensures the schema is applied.
// Uses StateDBPath (same as the dispatcher and workers) so that CLI commands
// read and write the same database as running workers.
func defaultMemoryStore() (*memory.Store, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	dbPath := paths.StateDBPath

	db, err := openDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	// Apply migrations for existing databases (columns may already exist).
	_, _ = db.ExecContext(context.Background(), protocol.MigrateFileTracking)
	_, _ = db.ExecContext(context.Background(), protocol.MigratePinnedMemories)
	_, _ = db.ExecContext(context.Background(), protocol.MigrateKVStore)
	_, _ = db.ExecContext(context.Background(), protocol.MigrateProjectColumn)

	// Backfill project column for existing rows
	_, _ = db.ExecContext(context.Background(), `UPDATE memories SET project = 'oro' WHERE project IS NULL OR project = ''`)

	store := memory.NewStore(db)

	// Set project scope from environment if ORO_PROJECT is set.
	// This scopes all memory operations to the current project.
	if project := os.Getenv("ORO_PROJECT"); project != "" {
		store.SetProject(project)
	}

	return store, nil
}
