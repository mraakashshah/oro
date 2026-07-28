package dispatcher

import (
	"context"
	"strings"
)

// retryMissingAC returns true if the bead still has no acceptance criteria.
// Resolved if AC is now populated or bead is closed.
func (d *Dispatcher) retryMissingAC(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	if detail == nil {
		return false // bead gone — escalation is stale
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
	if detail == nil {
		return false // bead gone — escalation is stale
	}
	return detail.WorkerID != ""
}

// retryMergeConflict returns true if the bead still has a worker (not yet merged/closed).
func (d *Dispatcher) retryMergeConflict(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	if detail == nil {
		return false // bead gone — escalation is stale
	}
	return detail.WorkerID != ""
}

// retryPriorityContention returns true if the bead is still unassigned.
func (d *Dispatcher) retryPriorityContention(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	if detail == nil {
		return false // bead gone — escalation is stale
	}
	return detail.WorkerID == ""
}

// retryNonTDDAC returns true if the bead still has Cmd/Assert without a Test: prefix.
// Resolved if: closed, in_progress (being handled), epic type, or AC now has Test:.
func (d *Dispatcher) retryNonTDDAC(ctx context.Context, beadID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		return true
	}
	if detail == nil {
		return false // bead gone — escalation is stale
	}
	if detail.Status == "closed" || detail.Status == "in_progress" {
		return false
	}
	if strings.EqualFold(detail.Type, "epic") {
		return false
	}
	hasTest := strings.Contains(detail.AcceptanceCriteria, "Test:")
	hasOperationalMarker := strings.Contains(detail.AcceptanceCriteria, "Cmd:") ||
		strings.Contains(detail.AcceptanceCriteria, "Assert:")
	return !hasTest && hasOperationalMarker
}
