package main

import (
	"context"
	"fmt"

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

	return memory.NewStore(db), nil
}
