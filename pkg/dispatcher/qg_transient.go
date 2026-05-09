package dispatcher

import (
	"context"
	"fmt"
	"time"

	"oro/pkg/protocol"
)

// maxTransientRetries is the number of transient/flaky QG retries before the
// failure is escalated as systemic (CreateOrReuseInfra).
const maxTransientRetries = 3

// transientBackoff returns the backoff duration for the n-th transient retry
// (1-based). The default is exponential: 5s, 10s, 20s, capped at 60s.
// Tests override this via Dispatcher.transientBackoffFn.
func (d *Dispatcher) transientBackoff(count int) time.Duration {
	if d.transientBackoffFn != nil {
		return d.transientBackoffFn(count)
	}
	backoff := time.Duration(5<<uint(count-1)) * time.Second
	if backoff > 60*time.Second {
		backoff = 60 * time.Second
	}
	return backoff
}

// handleTransientQGFailure handles transient and flaky QG failures with a
// backoff retry. Unlike the deterministic retry path, it does NOT increment
// d.attemptCounts, so the worker-fix retry budget is preserved.
//
// Returns true if the worker was successfully re-assigned after the backoff,
// false if the context was cancelled, the worker disconnected, or the transient
// recurrence threshold was exceeded (in which case CreateOrReuseInfra runs).
func (d *Dispatcher) handleTransientQGFailure(ctx context.Context, workerID, beadID string, rec QGFailureRecord, cls QGFailureClassification) bool {
	_ = d.logEvent(ctx, "qg_transient_retry", workerID, beadID, workerID,
		fmt.Sprintf(`{"class":%q,"reason":%q}`, cls.Class, cls.Reason))

	d.mu.Lock()
	d.transientCounts[beadID]++
	transientCount := d.transientCounts[beadID]
	assignmentID := d.assignmentIDLocked(workerID, beadID)

	if transientCount >= maxTransientRetries {
		d.mu.Unlock()
		rec.ID = fmt.Sprintf("%s:%s:%d:transient", beadID, workerID, assignmentID)
		rec.AssignmentID = assignmentID
		d.handleSystemicQGExhaustion(ctx, workerID, beadID, assignmentID, rec, cls)
		return false
	}

	// Reserve the worker so the heartbeat checker skips it during the backoff.
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerReserved
	}
	attempt := d.attemptCounts[beadID] // read without incrementing
	d.mu.Unlock()

	// Backoff: interruptible via context cancellation.
	timer := time.NewTimer(d.transientBackoff(transientCount))
	select {
	case <-ctx.Done():
		timer.Stop()
		d.mu.Lock()
		if w, ok := d.workers[workerID]; ok && w.state == protocol.WorkerReserved {
			w.state = protocol.WorkerIdle
			w.beadID = ""
		}
		d.mu.Unlock()
		return false
	case <-timer.C:
	}

	// Capture snapshot for buildAssignPayload (I/O runs outside lock).
	d.mu.Lock()
	var snap trackedWorker
	if w, ok := d.workers[workerID]; ok {
		snap = *w
	}
	snap.model = protocol.ModelOpus
	d.mu.Unlock()

	var payload *protocol.AssignPayload
	sendFailed := false

	assigned := d.withReservation(workerID,
		func() string {
			memCtx := d.fetchBeadMemories(ctx, beadID)
			payload = d.buildAssignPayload(ctx, &snap, attempt, rec.Output, memCtx)
			return memCtx
		},
		func(w *trackedWorker, memCtx string) bool {
			if w.model != protocol.ModelOpus {
				w.model = protocol.ModelOpus
			}
			payload.Model = w.model
			if err := d.sendToWorker(w, protocol.Message{
				Type:   protocol.MsgAssign,
				Assign: payload,
			}); err != nil {
				sendFailed = true
				w.state = protocol.WorkerIdle
				w.beadID = ""
				w.epicID = ""
				w.isEpicDecomp = false
				_ = d.logEvent(ctx, "qg_transient_retry_send_failed", workerID, beadID, workerID,
					fmt.Sprintf(`{"error":%q,"transient_count":%d}`, err.Error(), transientCount))
				_ = d.completeAssignment(ctx, w.assignmentID, beadID)
				return false
			}
			_ = d.logEventLocked(ctx, "qg_transient_retry_sent", workerID, beadID, workerID,
				fmt.Sprintf(`{"attempt":%d,"transient_count":%d}`, attempt, transientCount))
			w.state = protocol.WorkerBusy
			w.beadID = beadID
			return true
		},
	)

	// If withReservation returned false because the worker was already gone
	// (not a send failure), complete the assignment and reopen the bead.
	if !assigned && !sendFailed {
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		if d.shouldReopenQGOriginal(ctx, beadID) {
			_ = d.updateBeadStatus(ctx, beadID, "open")
		}
	}
	return assigned
}
