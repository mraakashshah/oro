package dispatcher

import (
	"context"

	"oro/pkg/protocol"
)

// retryMissingAC returns true if the bead still has no acceptance criteria.
// Resolved if AC is now populated or bead is closed.
func (d *Dispatcher) retryMissingAC(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	if detail.Status == "closed" {
		return false
	}
	return detail.AcceptanceCriteria == ""
}

// retryStuckWorker returns true if the worker for this bead still exists.
func (d *Dispatcher) retryStuckWorker(beadID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, w := range d.workers {
		if w.beadID == beadID {
			return true
		}
	}
	return false
}

// retryBeadStillAssigned returns true if the bead still has a worker assigned.
// Used for WORKER_CRASH and STUCK — resolved when bead is re-queued or closed.
func (d *Dispatcher) retryBeadStillAssigned(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	return detail.WorkerID != ""
}

// retryMergeConflict returns true if the bead still has a worker (not yet merged/closed).
func (d *Dispatcher) retryMergeConflict(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	return detail.WorkerID != ""
}

// retryPriorityContention returns true if the bead is still unassigned.
func (d *Dispatcher) retryPriorityContention(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	return detail.WorkerID == ""
}

// retryOversizedBead returns true if the bead is still oversized.
// Resolved if the bead has been promoted to an epic (children handle the work)
// or if the module count has dropped to <=2.
func (d *Dispatcher) retryOversizedBead(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	if detail.Type == "epic" {
		return false
	}
	return protocol.CountDistinctModules(detail.AcceptanceCriteria) > 2
}
