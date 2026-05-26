package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"oro/pkg/cards"
	"oro/pkg/dispatcher"
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

	db, err := openStateDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}

	// Apply store-specific migrations not covered by migrateStateDB.
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

func defaultMemoriesStore() (memoriesStore, error) {
	store, err := defaultMemoryStore()
	if err != nil {
		return nil, err
	}
	return memoriesStoreAdapter{store: store}, nil
}

func newMemoriesStoreAdapter(store *memory.Store) memoriesStore {
	return memoriesStoreAdapter{store: store}
}

type memoriesStoreAdapter struct {
	store *memory.Store
}

func (a memoriesStoreAdapter) ListMemories(ctx context.Context, opts protocol.MemoryListOpts) ([]protocol.Memory, error) {
	memories, err := a.store.List(ctx, memory.ListOpts{
		Type:   opts.Type,
		Tag:    opts.Tag,
		Limit:  opts.Limit,
		Offset: opts.Offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	return memories, nil
}

func (a memoriesStoreAdapter) ConsolidateMemories(ctx context.Context, opts protocol.MemoryConsolidateOpts) (merged, pruned int, err error) {
	merged, pruned, err = memory.Consolidate(ctx, a.store, memory.ConsolidateOpts{
		SimilarityThreshold: opts.SimilarityThreshold,
		MinDecayedScore:     opts.MinDecayedScore,
		DryRun:              opts.DryRun,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("consolidate memories: %w", err)
	}
	return merged, pruned, nil
}

func (a memoriesStoreAdapter) ClearMemoryProjectScope() {
	a.store.SetProject("")
}

func newWorkerMemoryExtractSpawner() memory.Spawner {
	return &memory.CLISpawner{}
}

// openWorkerMemoryStore creates a memory.Store from an open DB connection.
// It attaches a fresh Embedder and restores the accumulated vocabulary from
// the database so embeddings from prior sessions remain in the same vector
// space. LoadVocab failure is non-fatal: the embedder starts with an empty
// vocab and degrades gracefully (new memories embed correctly; cosine
// similarity against old embeddings may be noisy until vocab re-accumulates).
func openWorkerMemoryStore(db *sql.DB) *memory.Store {
	store := memory.NewStore(db)
	// NewEmbedder returns the default *TFIDFEmbedder implementation of the Embedder interface.
	store.SetEmbedder(memory.NewEmbedder())
	_ = store.LoadVocab(context.Background()) // non-fatal: empty vocab is valid
	return store
}

func newDispatcherMemoryServices(db *sql.DB) dispatcher.MemoryServices {
	store := openWorkerMemoryStore(db)
	return dispatcher.MemoryServices{
		Store: store,
		InsertRejection: func(ctx context.Context, beadID, workerID, feedback string) error {
			return store.InsertRejection(ctx, beadID, workerID, feedback)
		},
		GetRejections: func(ctx context.Context, beadID string) ([]dispatcher.MemoryRejection, error) {
			rejections, err := store.GetRejections(ctx, beadID)
			if err != nil {
				return nil, fmt.Errorf("get memory rejections: %w", err)
			}
			out := make([]dispatcher.MemoryRejection, 0, len(rejections))
			for _, r := range rejections {
				out = append(out, dispatcher.MemoryRejection{
					ID:        r.ID,
					BeadID:    r.BeadID,
					WorkerID:  r.WorkerID,
					Feedback:  r.Feedback,
					CreatedAt: r.CreatedAt,
				})
			}
			return out, nil
		},
		Consolidate: func(ctx context.Context) (int, int, error) {
			return memory.Consolidate(ctx, store, memory.ConsolidateOpts{})
		},
		TrimSearchEvents: func(ctx context.Context, maxAge time.Duration) (int64, error) {
			return memory.TrimSearchEvents(ctx, db, maxAge)
		},
		ExecuteDream: func(ctx context.Context, actions []dispatcher.DreamAction, logFn func(string)) error {
			memoryActions := make([]memory.DreamAction, 0, len(actions))
			for _, a := range actions {
				memoryActions = append(memoryActions, memory.DreamAction{
					Kind:   a.Kind,
					ID:     a.ID,
					IDs:    a.IDs,
					Params: a.Params,
				})
			}
			return memory.ExecuteActions(ctx, memoryActions, store, logFn)
		},
		HandoffInserter: func(cardStore cards.Store) dispatcher.MemoryInserter {
			return memory.NewLegacyCardWriter(store, cardStore)
		},
	}
}
