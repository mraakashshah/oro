package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// WorkerPool manages the set of connected workers. It is embedded in
// Dispatcher so that field access (e.g. d.workers) is promoted, keeping
// existing call-sites and tests unchanged. Synchronisation is provided by
// the Dispatcher-level mu; WorkerPool does not carry its own mutex.
type WorkerPool struct {
	workers map[string]*trackedWorker
}

// --- Worker lifecycle ---

// registerWorker adds or updates a tracked worker. If a pending handoff exists,
// the worker is immediately assigned that bead+worktree (ralph respawn).
func (d *Dispatcher) registerWorker(id string, conn net.Conn) {
	d.mu.Lock()
	if _, exists := d.workers[id]; !exists {
		d.workers[id] = &trackedWorker{
			id:       id,
			conn:     conn,
			state:    protocol.WorkerIdle,
			lastSeen: d.nowFunc(),
			encoder:  json.NewEncoder(conn),
		}
	} else {
		d.workers[id].conn = conn
		d.workers[id].lastSeen = d.nowFunc()
		d.workers[id].encoder = json.NewEncoder(conn)
	}

	// Check for pending ralph handoffs — assign immediately if one exists.
	var h *pendingHandoff
	for beadID, ph := range d.pendingHandoffs {
		h = ph
		delete(d.pendingHandoffs, beadID)
		break
	}

	if h != nil {
		w := d.workers[id]
		// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
		w.state = protocol.WorkerReserved
		w.beadID = h.beadID
		w.worktree = h.worktree
		w.model = h.model

		// Retrieve relevant memories (best-effort, outside lock).
		// We need to unlock before calling memory.ForPrompt.
		d.mu.Unlock()
		if d.testUnlockHook != nil {
			d.testUnlockHook()
		}

		var memCtx string
		if d.memories != nil {
			memCtx, _ = memory.ForPrompt(context.Background(), d.memories, nil, h.beadID, 0)
		}

		d.mu.Lock()
		// Phase 2: Verify reservation still valid, then transition to Busy.
		w, ok := d.workers[id]
		if !ok || w.state != protocol.WorkerReserved {
			d.mu.Unlock()
			return
		}
		w.state = protocol.WorkerBusy
		_ = d.sendToWorker(w, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:        h.beadID,
				Worktree:      h.worktree,
				Model:         h.model,
				MemoryContext: memCtx,
			},
		})
	}
	d.mu.Unlock()
}

// ConnectedWorkers returns the number of currently connected workers.
func (d *Dispatcher) ConnectedWorkers() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.workers)
}

// TargetWorkers returns the target worker pool size set by a scale directive.
//
//oro:testonly
func (d *Dispatcher) TargetWorkers() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.targetWorkers
}

// WorkerInfo returns state info for a tracked worker (for testing).
//
//oro:testonly
func (d *Dispatcher) WorkerInfo(id string) (state protocol.WorkerState, beadID string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, exists := d.workers[id]
	if !exists {
		return "", "", false
	}
	return w.state, w.beadID, true
}

// WorkerModel returns the stored model for a tracked worker (for testing).
//
//oro:testonly
func (d *Dispatcher) WorkerModel(id string) (model string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, exists := d.workers[id]
	if !exists {
		return "", false
	}
	return w.model, true
}

// --- Heartbeat monitoring ---

// heartbeatLoop checks for workers that have exceeded the heartbeat timeout
// and periodically prunes stale tracking map entries (hourly).
func (d *Dispatcher) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.HeartbeatTimeout / 3)
	defer ticker.Stop()

	pruneTicker := time.NewTicker(1 * time.Hour)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkHeartbeats(ctx)
		case <-pruneTicker.C:
			d.pruneStaleTracking(ctx)
		}
	}
}

// checkHeartbeats finds workers that have timed out and marks them dead.
func (d *Dispatcher) checkHeartbeats(ctx context.Context) {
	now := d.nowFunc()
	d.mu.Lock()
	var dead []string
	for id, w := range d.workers {
		if w.state != protocol.WorkerIdle && w.state != protocol.WorkerReserved && now.Sub(w.lastSeen) > d.cfg.HeartbeatTimeout {
			dead = append(dead, id)
		}
	}
	// Remove dead workers and collect info for escalation after unlock.
	type deadInfo struct {
		workerID string
		beadID   string
	}
	deadWorkers := make([]deadInfo, 0, len(dead))
	for _, id := range dead {
		w := d.workers[id]
		deadWorkers = append(deadWorkers, deadInfo{workerID: id, beadID: w.beadID})
		_ = d.logEventLocked(ctx, "heartbeat_timeout", "dispatcher", w.beadID, id, "")
		_ = w.conn.Close()
		delete(d.workers, id)
	}
	d.mu.Unlock()

	// Escalate outside the lock and clear tracking maps for abandoned beads.
	for _, dw := range deadWorkers {
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscWorkerCrash, dw.beadID, "worker disconnected", "heartbeat timeout for worker "+dw.workerID), dw.beadID, dw.workerID)
		if dw.beadID != "" {
			d.clearBeadTracking(dw.beadID)
		}
	}
}

// --- UDS send helper ---

// maxPendingMessages is the maximum number of messages to buffer for a
// disconnected worker before treating it as dead.
const maxPendingMessages = 10

// sendToWorker sends a message to a tracked worker. If the worker is
// disconnected (write fails), the message is buffered up to maxPendingMessages.
// If the buffer exceeds maxPendingMessages, the worker is removed from tracking.
// Caller must hold d.mu.
func (d *Dispatcher) sendToWorker(w *trackedWorker, msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')
	_, err = w.conn.Write(data)
	if err != nil {
		// Connection is broken — buffer the message
		w.pendingMsgs = append(w.pendingMsgs, msg)

		// If buffer exceeds limit, treat worker as dead and remove it
		if len(w.pendingMsgs) > maxPendingMessages {
			_ = w.conn.Close()
			delete(d.workers, w.id)
			// Return typed WorkerUnreachableError for error discrimination
			return &protocol.WorkerUnreachableError{
				WorkerID: w.id,
				BeadID:   w.beadID,
				Reason:   fmt.Sprintf("exceeded pending message limit (%d), removed", maxPendingMessages),
			}
		}

		// Return typed WorkerUnreachableError for error discrimination
		return &protocol.WorkerUnreachableError{
			WorkerID: w.id,
			BeadID:   w.beadID,
			Reason:   fmt.Sprintf("write failed: %v (message buffered)", err),
		}
	}
	return nil
}

// --- Graceful shutdown ---

// GracefulShutdownWorker initiates a graceful shutdown for a specific worker.
// It sends PREPARE_SHUTDOWN with the given timeout, then waits for SHUTDOWN_APPROVED.
// If the worker does not respond within the timeout, it sends a hard SHUTDOWN.
// Duplicate shutdown calls for the same worker cancel the previous goroutine.
func (d *Dispatcher) GracefulShutdownWorker(workerID string, timeout time.Duration) {
	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return
	}

	// Cancel any previous shutdown goroutine for this worker
	if w.shutdownCancel != nil {
		w.shutdownCancel()
	}

	// Create a new context for this shutdown attempt
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	w.shutdownCancel = cancel

	_ = d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgPrepareShutdown,
		PrepareShutdown: &protocol.PrepareShutdownPayload{
			Timeout: timeout,
		},
	})
	d.mu.Unlock()

	// Spawn background goroutine to wait for approval or timeout
	d.wg.Add(1)
	go d.shutdownWaitLoop(ctx, cancel, workerID)
}

// shutdownWaitLoop polls for worker approval or timeout (extracted for complexity).
func (d *Dispatcher) shutdownWaitLoop(shutdownCtx context.Context, cancelFunc context.CancelFunc, workerID string) {
	defer d.wg.Done()
	defer cancelFunc() // Clean up context when goroutine exits

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-shutdownCtx.Done():
			// Context cancelled (either timeout or duplicate shutdown call)
			if shutdownCtx.Err() == context.DeadlineExceeded {
				d.handleShutdownTimeout(workerID)
			}
			// If err == context.Canceled, duplicate shutdown call cancelled us - exit silently
			return
		case <-ticker.C:
			if d.checkShutdownApproved(workerID) {
				return
			}
		}
	}
}

// handleShutdownTimeout sends hard SHUTDOWN after graceful shutdown timeout.
func (d *Dispatcher) handleShutdownTimeout(workerID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if ok {
		_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.shutdownCancel = nil
	}
}

// checkShutdownApproved returns true if the worker has been approved for shutdown (state == Idle).
func (d *Dispatcher) checkShutdownApproved(workerID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok {
		return false
	}
	approved := w.state == protocol.WorkerIdle
	if approved && w.shutdownCancel != nil {
		w.shutdownCancel = nil // Clear cancel func after successful shutdown
	}
	return approved
}

// shutdownWaitForWorkers waits up to ShutdownTimeout for all workers to drain,
// then force-closes any remaining connections.
func (d *Dispatcher) shutdownWaitForWorkers() {
	deadline := time.NewTimer(d.cfg.ShutdownTimeout)
	defer deadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline.C:
			// Timeout expired — force-close all remaining connections.
			d.mu.Lock()
			for id, w := range d.workers {
				_ = w.conn.Close()
				delete(d.workers, id)
			}
			d.mu.Unlock()
			return
		case <-ticker.C:
			if d.ConnectedWorkers() == 0 {
				return
			}
		}
	}
}
