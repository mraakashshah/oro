// Package memoryboundary adapts legacy memory storage behind narrower callers.
package memoryboundary

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"oro/pkg/cards"
	"oro/pkg/dispatcher"
	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// Store wraps pkg/memory.Store behind command- and dispatcher-owned interfaces.
type Store struct {
	store *memory.Store
}

// NewStore creates a memory boundary store around an open database handle.
func NewStore(db *sql.DB) *Store {
	return &Store{store: memory.NewStore(db)}
}

// NewWorkerStore creates a memory boundary store with embedding enabled.
func NewWorkerStore(db *sql.DB) *Store {
	store := NewStore(db)
	store.store.SetEmbedder(memory.NewEmbedder())
	_ = store.store.LoadVocab(context.Background()) // non-fatal: empty vocab is valid
	return store
}

// NewExtractSpawner returns the production memory extraction spawner.
func NewExtractSpawner() *memory.CLISpawner {
	return &memory.CLISpawner{}
}

// Insert persists a memory.
func (s *Store) Insert(ctx context.Context, params protocol.MemoryInsertParams) (int64, error) {
	id, err := s.store.Insert(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("insert memory: %w", err)
	}
	return id, nil
}

// GetByID loads a memory by id.
func (s *Store) GetByID(ctx context.Context, id int64) (protocol.Memory, error) {
	mem, err := s.store.GetByID(ctx, id)
	if err != nil {
		return protocol.Memory{}, fmt.Errorf("get memory by id: %w", err)
	}
	return mem, nil
}

// Delete removes a memory by id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	if err := s.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	return nil
}

// Search returns scored memory search results.
func (s *Store) Search(ctx context.Context, query string, opts protocol.MemorySearchOpts) ([]protocol.ScoredMemory, error) {
	results, err := s.store.Search(ctx, query, opts)
	if err != nil {
		return nil, fmt.Errorf("search memories: %w", err)
	}
	return results, nil
}

// DumpAll returns every memory.
func (s *Store) DumpAll(ctx context.Context) ([]protocol.Memory, error) {
	memories, err := s.store.DumpAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("dump memories: %w", err)
	}
	return memories, nil
}

// HasEmbedder reports whether embeddings are enabled.
func (s *Store) HasEmbedder() bool {
	return s.store.HasEmbedder()
}

// SetProject scopes future memory operations to a project.
func (s *Store) SetProject(project string) {
	s.store.SetProject(project)
}

// SaveVocab persists the store vocabulary.
func (s *Store) SaveVocab(ctx context.Context) error {
	if err := s.store.SaveVocab(ctx); err != nil {
		return fmt.Errorf("save memory vocab: %w", err)
	}
	return nil
}

// ListMemories lists memories using command-facing options.
func (s *Store) ListMemories(ctx context.Context, opts protocol.MemoryListOpts) ([]protocol.Memory, error) {
	memories, err := s.store.List(ctx, memory.ListOpts{
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

// ConsolidateMemories consolidates memories using command-facing options.
func (s *Store) ConsolidateMemories(ctx context.Context, opts protocol.MemoryConsolidateOpts) (merged, pruned int, err error) {
	merged, pruned, err = memory.Consolidate(ctx, s.store, memory.ConsolidateOpts{
		SimilarityThreshold: opts.SimilarityThreshold,
		MinDecayedScore:     opts.MinDecayedScore,
		DryRun:              opts.DryRun,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("consolidate memories: %w", err)
	}
	return merged, pruned, nil
}

// ClearMemoryProjectScope removes project filtering for command list/consolidate operations.
func (s *Store) ClearMemoryProjectScope() {
	s.store.SetProject("")
}

// NewDispatcherMemoryServices creates dispatcher memory hooks around an open database handle.
func NewDispatcherMemoryServices(db *sql.DB) dispatcher.MemoryServices {
	store := NewWorkerStore(db)
	return dispatcher.MemoryServices{
		Store: store,
		InsertRejection: func(ctx context.Context, beadID, workerID, feedback string) error {
			return store.store.InsertRejection(ctx, beadID, workerID, feedback)
		},
		GetRejections: func(ctx context.Context, beadID string) ([]dispatcher.MemoryRejection, error) {
			rejections, err := store.store.GetRejections(ctx, beadID)
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
			return memory.Consolidate(ctx, store.store, memory.ConsolidateOpts{})
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
			return memory.ExecuteActions(ctx, memoryActions, store.store, logFn)
		},
		HandoffInserter: func(cardStore cards.Store) dispatcher.MemoryInserter {
			return memory.NewLegacyCardWriter(store.store, cardStore)
		},
	}
}
