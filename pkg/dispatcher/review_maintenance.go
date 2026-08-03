package dispatcher

import (
	"context"
	"errors"
	"os"
	"time"
)

const (
	defaultReviewArtifactRetention   = 7 * 24 * time.Hour
	defaultReviewMaintenanceInterval = time.Hour
)

// isReviewArtifactTerminal reports whether a checkpoint can release its
// artifacts after retention. Approval alone is intentionally not terminal:
// integration can still be pending or require operator action.
func isReviewArtifactTerminal(state ReviewCheckpointState) bool {
	return state == ReviewCheckpointStateIntegrated || state == ReviewCheckpointStateSuperseded
}

// reviewMaintenanceLoop periodically removes terminal review artifacts after
// their retention window. It runs once immediately so restarts do not defer a
// due cleanup until the next interval.
func (d *Dispatcher) reviewMaintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(d.reviewMaintenanceInterval)
	defer ticker.Stop()

	for {
		d.pruneReviewArtifacts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) pruneReviewArtifacts(ctx context.Context) {
	d.reviewArtifactPruneMu.Lock()
	defer d.reviewArtifactPruneMu.Unlock()
	reviewRecoveryArtifactLifecycleMu.Lock()
	defer reviewRecoveryArtifactLifecycleMu.Unlock()

	store := NewReviewCheckpointStore(d.db)
	before := d.nowFunc().Add(-d.reviewArtifactRetention)
	artifacts, err := store.ListPrunableArtifacts(ctx, before, d.reviewRecoveryArtifactDir)
	if err != nil {
		return
	}
	for _, artifact := range artifacts {
		if d.testReviewArtifactBeforeDelete != nil {
			d.testReviewArtifactBeforeDelete(artifact)
		}
		if err := os.Remove(artifact.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := store.ClearPrunedArtifact(ctx, artifact.Path); err != nil {
			continue
		}
	}
}
