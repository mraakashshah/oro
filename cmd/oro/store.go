package main

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
)

var errLegacyMemoryRetired = errors.New("legacy memory has been retired; use cards instead")

type retiredMemoryStore struct{}

// defaultMemoryStore opens (or creates) the default SQLite memory store at
// ~/.oro/state.db (or ~/.oro/projects/<project>/state.db if a project is set)
// and ensures the schema is applied.
// Uses ResolveProjectDBPaths to respect project context, so that CLI commands
// read and write the same database as running workers in the current project.
func defaultMemoryStore() (*retiredMemoryStore, error) {
	return &retiredMemoryStore{}, nil
}

func defaultMemoriesStore() (memoriesStore, error) {
	store, err := defaultMemoryStore()
	if err != nil {
		return nil, err
	}
	return store, nil
}

func newWorkerMemoryExtractSpawner() workerMemoryExtractSpawner {
	return nil
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
func openWorkerMemoryStore(_ *sql.DB) workMemoryStore {
	return nil
}

func newDispatcherMemoryServices(db *sql.DB) dispatcher.MemoryServices {
	return dispatcher.MemoryServices{}
}

func (retiredMemoryStore) Insert(context.Context, protocol.MemoryInsertParams) (int64, error) {
	return 0, errLegacyMemoryRetired
}

func (retiredMemoryStore) GetByID(context.Context, int64) (protocol.Memory, error) {
	return protocol.Memory{}, errLegacyMemoryRetired
}

func (retiredMemoryStore) Delete(context.Context, int64) error {
	return errLegacyMemoryRetired
}

func (retiredMemoryStore) Search(context.Context, string, protocol.MemorySearchOpts) ([]protocol.ScoredMemory, error) {
	return nil, errLegacyMemoryRetired
}

func (retiredMemoryStore) SetProject(string) {}

func (retiredMemoryStore) SaveVocab(context.Context) error {
	return nil
}

func (retiredMemoryStore) ListMemories(context.Context, protocol.MemoryListOpts) ([]protocol.Memory, error) {
	return nil, errLegacyMemoryRetired
}

func (retiredMemoryStore) ConsolidateMemories(context.Context, protocol.MemoryConsolidateOpts) (merged, pruned int, err error) {
	return 0, 0, errLegacyMemoryRetired
}

func (retiredMemoryStore) ClearMemoryProjectScope() {}
