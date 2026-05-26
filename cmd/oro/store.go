package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"

	"oro/internal/memoryboundary"
	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
)

// defaultMemoryStore opens (or creates) the default SQLite memory store at
// ~/.oro/state.db (or ~/.oro/projects/<project>/state.db if a project is set)
// and ensures the schema is applied.
// Uses ResolveProjectDBPaths to respect project context, so that CLI commands
// read and write the same database as running workers in the current project.
func defaultMemoryStore() (*memoryboundary.Store, error) {
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	dbPath := paths.StateDBPath

	db, err := openStateDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}

	// Apply store-specific migrations not covered by migrateStateDB.
	_, _ = db.ExecContext(context.Background(), protocol.MigrateProjectColumn)
	// Backfill project column for existing rows
	_, _ = db.ExecContext(context.Background(), `UPDATE memories SET project = 'oro' WHERE project IS NULL OR project = ''`)

	store := memoryboundary.NewStore(db)

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

func defaultMemoriesStore() (memoriesStore, error) {
	store, err := defaultMemoryStore()
	if err != nil {
		return nil, err
	}
	return store, nil
}

func newWorkerMemoryExtractSpawner() workerMemoryExtractSpawner {
	return memoryboundary.NewExtractSpawner()
}

type workerMemoryExtractSpawner interface {
	Spawn(ctx context.Context, model, prompt string) (io.ReadCloser, error)
}

// openWorkerMemoryStore creates a memory boundary store from an open DB connection.
// It attaches a fresh Embedder and restores the accumulated vocabulary from
// the database so embeddings from prior sessions remain in the same vector
// space. LoadVocab failure is non-fatal: the embedder starts with an empty
// vocab and degrades gracefully (new memories embed correctly; cosine
// similarity against old embeddings may be noisy until vocab re-accumulates).
func openWorkerMemoryStore(db *sql.DB) *memoryboundary.Store {
	return memoryboundary.NewWorkerStore(db)
}

func newDispatcherMemoryServices(db *sql.DB) dispatcher.MemoryServices {
	return memoryboundary.NewDispatcherMemoryServices(db)
}
