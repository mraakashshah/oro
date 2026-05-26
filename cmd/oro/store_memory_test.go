package main

import (
	"context"
	"fmt"

	"oro/pkg/memory"
	"oro/pkg/protocol"
)

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
