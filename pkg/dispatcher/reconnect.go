package dispatcher

import (
	"context"
	"fmt"
	"oro/pkg/protocol"
)

func (d *Dispatcher) validateReconnectBead(ctx context.Context, beadID, workerID string) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil {
		_ = d.logEvent(ctx, "reconnect_closed_bead_rejected", workerID, beadID, workerID,
			"rejecting reconnect: bead lookup failed (not found or error)")
		return false
	}
	if detail.Status == "closed" {
		_ = d.logEvent(ctx, "reconnect_closed_bead_rejected", workerID, beadID, workerID,
			"rejecting reconnect: bead is closed")
		return false
	}
	return true
}

// processReconnectUnderLock handles reconnection logic while holding d.mu.
// Caller must hold d.mu.
// oro-ovpc: Prevents bead stealing by checking for existing assignments.
func (d *Dispatcher) processReconnectUnderLock(ctx context.Context, w *trackedWorker, workerID, beadID, state string) {
	if w.state == protocol.WorkerReserved {
		w.lastSeen = d.nowFunc()
		for _, pending := range w.pendingMsgs {
			_ = d.sendToWorker(w, pending)
		}
		w.pendingMsgs = nil
		return
	}

	// Check if another worker is already assigned to this bead.
	var beadStolenFrom string
	for otherID, other := range d.workers {
		if otherID != workerID && other.beadID == beadID && other.state == protocol.WorkerBusy {
			beadStolenFrom = otherID
			break
		}
	}

	if beadStolenFrom != "" {
		_ = d.logEvent(ctx, "reconnect_bead_conflict", workerID, beadID, beadStolenFrom,
			fmt.Sprintf("worker %s already assigned to %s", beadStolenFrom, beadID))
	} else {
		w.beadID = beadID
	}

	w.lastSeen = d.nowFunc()
	if state == "running" && w.beadID == beadID {
		w.state = protocol.WorkerBusy
		w.lastProgress = d.nowFunc()
	} else {
		w.state = protocol.WorkerIdle
	}

	// Replay pending messages
	for _, pending := range w.pendingMsgs {
		_ = d.sendToWorker(w, pending)
	}
	w.pendingMsgs = nil
}

func (d *Dispatcher) reactivateRequeuedAssignment(ctx context.Context, beadID, workerID string) int64 {
	if d.db == nil || beadID == "" {
		return 0
	}
	var assignmentID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE bead_id=? AND status='requeued' ORDER BY id DESC LIMIT 1`,
		beadID,
	).Scan(&assignmentID); err != nil {
		return d.activeAssignmentIDForBead(ctx, beadID)
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='active', completed_at=NULL, worker_id=? WHERE id=?`,
		workerID, assignmentID,
	); err != nil {
		_ = d.logEvent(ctx, "assignment_reactivate_failed", "dispatcher", beadID, workerID, err.Error())
		return 0
	}
	_ = d.logEvent(ctx, "assignment_reactivated", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"assignment_id":%d}`, assignmentID))
	return assignmentID
}

func (d *Dispatcher) handleReconnect(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Reconnect == nil {
		return
	}

	// Validate the reconnect payload to prevent unbounded buffered events
	if err := msg.Reconnect.Validate(); err != nil {
		_ = d.logEvent(ctx, "reconnect_rejected", workerID, msg.Reconnect.BeadID, workerID, err.Error())
		return
	}

	_ = d.logEvent(ctx, "reconnect", workerID, msg.Reconnect.BeadID, workerID, msg.Reconnect.State)

	beadID := msg.Reconnect.BeadID
	if d.shutdownReconnectIfSpawnForStopping(workerID) {
		return
	}

	// oro-sydf: If BeadID is empty, the worker was idle before the network glitch.
	// Skip bead validation entirely — there is no bead to look up — and
	// transition the worker directly to idle so tryAssign can pick it up.
	if beadID == "" {
		d.mu.Lock()
		if w, ok := d.workers[workerID]; ok {
			w.state = protocol.WorkerIdle
			w.beadID = ""
			w.lastSeen = d.nowFunc()
		}
		d.mu.Unlock()
		return
	}

	// oro-3xdf: Check if the bead is valid (open, not closed/missing).
	// Do this outside the lock to avoid I/O while holding mutex.
	if !d.validateReconnectBead(ctx, beadID, workerID) {
		// oro-xj37: Transition worker to Idle so tryAssign can pick it up.
		// Without this, the worker stays in its previous state permanently.
		d.mu.Lock()
		if w, ok := d.workers[workerID]; ok {
			w.state = protocol.WorkerIdle
			w.beadID = ""
			w.lastSeen = d.nowFunc()
		}
		d.mu.Unlock()
		return
	}

	assignmentID := d.reactivateRequeuedAssignment(ctx, beadID, workerID)

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return
	}

	d.processReconnectUnderLock(ctx, w, workerID, beadID, msg.Reconnect.State)
	if assignmentID > 0 && w.beadID == beadID {
		w.assignmentID = assignmentID
	}
	d.mu.Unlock()

	// Process any buffered events
	for _, buffered := range msg.Reconnect.BufferedEvents {
		d.handleMessage(ctx, workerID, buffered)
	}
}

func (d *Dispatcher) shutdownReconnectIfSpawnForStopping(workerID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || !w.spawnFor || w.state != protocol.WorkerShuttingDown {
		return false
	}
	w.markShuttingDownWithoutAssignment()
	sendShutdownWithoutBuffering(w)
	w.lastSeen = d.nowFunc()
	return true
}
