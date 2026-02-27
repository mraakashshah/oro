package main

import (
	"context"
	"fmt"
	"os"

	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// defaultMemoryStore opens (or creates) the default SQLite memory store at
// ~/.oro/state.db (or ~/.oro/projects/<project>/state.db if a project is set)
// and ensures the schema is applied.
// Uses ResolveProjectDBPaths to respect project context, so that CLI commands
// read and write the same database as running workers in the current project.
func defaultMemoryStore() (*memory.Store, error) {
	paths, err := ResolveProjectDBPaths()
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
	// If not set, fallback to reading project name from .oro/config.yaml.
	// If neither exists, no project filtering is applied (all memories accessible).
	project := os.Getenv("ORO_PROJECT")
	if project == "" {
		// Fallback to .oro/config.yaml in current directory
		configProject, err := readProjectConfig(".")
		if err == nil && configProject != "" {
			project = configProject
		}
	}
	if project != "" {
		store.SetProject(project)
	}

	return store, nil
}
