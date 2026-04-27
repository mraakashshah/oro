package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// workerEpochRe matches the first 15-19 digit number in a worker ID, which
// encodes the nanosecond Unix timestamp at worker creation time.
// Worker ID formats: "worker-<nano>-<i>", "ext-<nano>-<i>",
// "worker-spawnfor-<nano>", "worker-handoff-<nano>".
var workerEpochRe = regexp.MustCompile(`\b(\d{15,19})\b`)

// parseWorkerEpoch extracts the creation timestamp embedded in a worker ID.
// Returns (time, true) when a nanosecond timestamp is found, otherwise zero
// time and false (e.g. for hand-crafted test IDs like "w-1").
func parseWorkerEpoch(id string) (time.Time, bool) {
	m := workerEpochRe.FindString(id)
	if m == "" {
		return time.Time{}, false
	}
	ns, err := strconv.ParseInt(m, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

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
// upsertWorker adds a new trackedWorker for id or refreshes its connection
// fields on reconnect. managed is true when the worker was spawned by the
// dispatcher (consumed from pendingManagedIDs by the caller). Must be called
// with d.mu held.
func (d *Dispatcher) upsertWorker(id string, conn net.Conn, managed bool) {
	if _, exists := d.workers[id]; !exists {
		prev := false
		if epoch, ok := parseWorkerEpoch(id); ok && epoch.Before(d.startTime) {
			prev = true
		}
		d.workers[id] = &trackedWorker{
			id:          id,
			conn:        conn,
			state:       protocol.WorkerIdle,
			lastSeen:    d.nowFunc(),
			encoder:     json.NewEncoder(conn),
			managed:     managed,
			prevSession: prev,
		}
	} else {
		d.workers[id].conn = conn
		d.workers[id].lastSeen = d.nowFunc()
		d.workers[id].encoder = json.NewEncoder(conn)
		// Preserve managed flag if already set (e.g. reconnect of a spawned worker).
		if managed {
			d.workers[id].managed = true
		}
	}
}

// buildHandoffMemoryContext retrieves memory context for a handoff using the bead title and labels.
// Falls back to beadID if title is empty.
func (d *Dispatcher) buildHandoffMemoryContext(h *pendingHandoff) string {
	if d.memories == nil {
		return ""
	}
	searchQuery := buildSearchQuery(h.title, h.labels)
	if searchQuery == "" {
		searchQuery = h.beadID
	}
	memCtx, _ := memory.ForPrompt(context.Background(), d.memories, nil, searchQuery, 0)
	return memCtx
}

func (d *Dispatcher) registerWorker(id string, conn net.Conn) {
	d.mu.Lock()
	// Consume the pending managed ID if present (delete is no-op if absent).
	managed := d.pendingManagedIDs[id]
	delete(d.pendingManagedIDs, id)
	d.upsertWorker(id, conn, managed)

	// Check for pending ralph handoffs — assign immediately if one exists.
	var h *pendingHandoff
	var handoffBeadID string
	for beadID, ph := range d.pendingHandoffs {
		h = ph
		handoffBeadID = beadID
		break
	}

	if h != nil {
		d.assignHandoffToWorker(id, handoffBeadID, h)
		return
	}
	d.mu.Unlock()

	// Worker is idle — wake the assign loop so it can call tryAssign immediately
	// instead of waiting for the next poll tick. Non-blocking send: if the channel
	// is already full, a tryAssign is already queued and this signal is redundant.
	select {
	case d.workerReadyCh <- struct{}{}:
	default:
	}
}

// assignHandoffToWorker assigns pending handoff h to the just-registered worker id.
// Caller must hold d.mu; on return d.mu is unlocked. The function temporarily
// releases d.mu during memory retrieval. handoffBeadID is the key for h in
// d.pendingHandoffs.
func (d *Dispatcher) assignHandoffToWorker(id, handoffBeadID string, h *pendingHandoff) {
	w := d.workers[id]
	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	w.state = protocol.WorkerReserved
	w.assignmentID = h.assignmentID
	w.beadID = h.beadID
	w.worktree = h.worktree
	w.model = h.model
	w.epicID = h.epicID
	w.baseBranch = h.baseBranch
	w.targetBranch = h.targetBranch
	w.lastProgress = d.nowFunc()

	// Retrieve relevant memories (best-effort, outside lock).
	d.mu.Unlock()
	if d.testUnlockHook != nil {
		d.testUnlockHook()
	}
	memCtx := d.buildHandoffMemoryContext(h)
	d.mu.Lock()
	defer d.mu.Unlock()

	// Phase 2: Verify reservation still valid, then transition to Busy.
	w, ok := d.workers[id]
	if !ok || w.state != protocol.WorkerReserved {
		if _, exists := d.pendingHandoffs[handoffBeadID]; !exists {
			d.pendingHandoffs[handoffBeadID] = h
		}
		return
	}
	w.state = protocol.WorkerBusy
	if err := d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:        h.beadID,
			Worktree:      h.worktree,
			Model:         h.model,
			MemoryContext: memCtx,
			TargetBranch:  h.targetBranch,
		},
	}); err != nil {
		_ = w.conn.Close()
		delete(d.workers, id)
		if _, exists := d.pendingHandoffs[handoffBeadID]; !exists {
			d.pendingHandoffs[handoffBeadID] = h
		}
		return
	}
	delete(d.pendingHandoffs, handoffBeadID)
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

// touchProgress updates lastProgress for a worker to the current time.
// Called on meaningful events: DONE, READY_FOR_REVIEW, QG result, STATUS.
func (d *Dispatcher) touchProgress(workerID string) {
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok {
		w.lastProgress = d.nowFunc()
	}
	d.mu.Unlock()
}

// --- Heartbeat monitoring ---

// workerExitInfo holds the minimal details needed to escalate a timed-out
// worker exit after releasing d.mu.
type workerExitInfo struct {
	workerID     string
	beadID       string
	assignmentID int64
	prevSession  bool // worker is from a previous dispatcher session
	managed      bool // worker was spawned by the dispatcher (procMgr)
}

// escalateTimedOutWorkers dispatches escalation messages and clears bead
// tracking for workers that were removed by checkHeartbeats. Called outside
// d.mu so that escalate and clearBeadTracking can acquire their own locks.
func (d *Dispatcher) escalateTimedOutWorkers(ctx context.Context, dead, stuck []workerExitInfo) {
	for _, dw := range dead {
		// Skip WORKER_CRASH alert for workers from a previous dispatcher session —
		// they are already dead from the operator's perspective and re-alerting
		// on them after a restart is noisy and misleading (oro-ny8h).
		// Prev-session workers also must NOT reset their bead to "open": their
		// bead assignments are stale and the bead may already be closed (oro-p2ey).
		if !dw.prevSession {
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscWorkerCrash, dw.beadID, "worker disconnected", "heartbeat timeout for worker "+dw.workerID), dw.beadID, dw.workerID)
			if dw.beadID != "" {
				if err := d.beads.Update(ctx, dw.beadID, "open"); err != nil {
					_ = d.logEvent(ctx, "heartbeat_bead_reset_failed", "dispatcher", dw.beadID, dw.workerID,
						fmt.Sprintf(`{"error":%q}`, err.Error()))
				}
				_ = d.completeAssignment(ctx, dw.assignmentID, dw.beadID)
			}
		}
		// Always clear internal tracking for timed-out workers, regardless of
		// session — prev-session workers must not hold stale dispatcher references.
		if dw.beadID != "" {
			d.clearBeadTracking(dw.beadID)
		}
	}
	for _, sw := range stuck {
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuckWorker, sw.beadID,
			"worker stalled with no progress", "progress timeout for worker "+sw.workerID), sw.beadID, sw.workerID)
		if sw.beadID != "" {
			// Reset the bead to "open" so it can be reassigned, mirroring the
			// graceful-disconnect path in dispatcher.go.
			if err := d.beads.Update(ctx, sw.beadID, "open"); err != nil {
				_ = d.logEvent(ctx, "progress_timeout_bead_reset_failed", "dispatcher", sw.beadID, sw.workerID,
					fmt.Sprintf(`{"error":%q}`, err.Error()))
			}
			_ = d.completeAssignment(ctx, sw.assignmentID, sw.beadID)
			d.clearBeadTracking(sw.beadID)
		}
	}
}

// heartbeatLoop checks for workers that have exceeded the heartbeat timeout
// and periodically prunes stale tracking map entries and GCs closed worktrees (hourly).
// heartbeatLoop checks for workers that have exceeded the heartbeat timeout
// and periodically prunes stale tracking map entries and GCs closed worktrees (hourly).
// Each iteration is wrapped in a defer/recover so a panic inside the body
// logs a goroutine_panic event and restarts the loop after exponential backoff.
func (d *Dispatcher) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.HeartbeatTimeout / 3)
	defer ticker.Stop()

	pruneTicker := time.NewTicker(1 * time.Hour)
	defer pruneTicker.Stop()

	gcTicker := time.NewTicker(1 * time.Hour)
	defer gcTicker.Stop()

	backupTicker := time.NewTicker(d.cfg.BackupInterval)
	defer backupTicker.Stop()

	doltHealthTicker := time.NewTicker(d.cfg.DoltHealthInterval)
	defer doltHealthTicker.Stop()

	var restartCount int
	var lastPanicTime time.Time

	for {
		exit := func() (shouldExit bool) {
			defer func() {
				if r := recover(); r != nil {
					if d.handleLoopPanic(ctx, r, &restartCount, &lastPanicTime) {
						shouldExit = true
					}
				}
			}()
			select {
			case <-ctx.Done():
				return true
			case <-d.shutdownCh:
				return true
			case <-ticker.C:
				d.callCheckHeartbeats(ctx)
				d.maybeChangeDetectionBackup(ctx)
			case <-pruneTicker.C:
				d.pruneStaleTracking(ctx)
				d.detectAndResolveDuplicateActiveAssignments(ctx)
			case <-gcTicker.C:
				d.gcWorktrees(ctx)
			case <-backupTicker.C:
				d.backupFullState(ctx)
			case <-doltHealthTicker.C:
				if !d.doltRecovering.Load() && !d.checkDoltHealth(ctx) {
					d.recoverDolt(ctx)
				}
			}
			return false
		}()
		if exit {
			return
		}
	}
}

// backupFullState runs bd export and writes all issues (open + closed) to
// .beads/backup/full-state.jsonl. Failures are logged as warnings and skipped
// (non-fatal). An empty export is silently skipped.
func (d *Dispatcher) backupFullState(ctx context.Context) {
	data, err := d.beads.Export(ctx)
	if err != nil {
		slog.WarnContext(ctx, "full_state_backup_export_failed", "error", err.Error())
		return
	}
	if len(data) == 0 {
		return
	}
	backupDir := filepath.Join(d.beadsDir, "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil { //nolint:gosec // backupDir derives from trusted beadsDir
		slog.WarnContext(ctx, "full_state_backup_mkdir_failed", "error", err.Error())
		return
	}
	backupPath := filepath.Join(backupDir, "full-state.jsonl")
	if err := os.WriteFile(backupPath, data, 0o644); err != nil { //nolint:gosec // backupPath derives from trusted beadsDir
		slog.WarnContext(ctx, "full_state_backup_write_failed", "error", err.Error())
	}
}

// maybeChangeDetectionBackup triggers a full backup when the bead count changes by >=5
// since the last backup. This provides a faster detection mechanism for large queue
// changes compared to the fixed-interval backup. The current bead count is computed as
// cachedQueueDepth + (beads.InProgress count or 0 on error). If abs(delta) >= 5,
// backupFullState is called and lastBackupBeadCount is updated.
func (d *Dispatcher) maybeChangeDetectionBackup(ctx context.Context) {
	d.mu.Lock()
	cachedDepth := d.cachedQueueDepth
	lastCount := d.lastBackupBeadCount
	d.mu.Unlock()

	// Get in-progress count; if it fails, use cachedDepth alone as a best-effort estimate.
	inProgressBeads, err := d.beads.InProgress(ctx)
	if err != nil {
		inProgressBeads = []protocol.Bead{}
	}

	currentCount := cachedDepth + len(inProgressBeads)
	delta := currentCount - lastCount

	// Trigger backup if absolute delta >= 5
	if delta >= 5 || delta <= -5 {
		d.backupFullState(ctx)
		d.mu.Lock()
		d.lastBackupBeadCount = currentCount
		d.mu.Unlock()
	}
}

// managedWorkerIDs returns the worker IDs of all managed workers across the
// provided slices. Used to collect IDs for procMgr.Kill after timeout removal.
func managedWorkerIDs(slices ...[]workerExitInfo) []string {
	var ids []string
	for _, s := range slices {
		for _, w := range s {
			if w.managed {
				ids = append(ids, w.workerID)
			}
		}
	}
	return ids
}

// checkHeartbeats finds workers that have timed out (liveness) or stalled
// (no progress within ProgressTimeout) and handles them appropriately.
// Dead workers (heartbeat timeout) are removed and escalated as WORKER_CRASH.
// Stuck workers (progress timeout) are killed and escalated as STUCK_WORKER.
//
//nolint:gocyclo // heartbeat dispatch combines liveness + progress + review timeout checks
func (d *Dispatcher) checkHeartbeats(ctx context.Context) {
	now := d.nowFunc()
	d.mu.Lock()
	var dead []string
	var stuck []string
	for id, w := range d.workers {
		if w.state == protocol.WorkerReserved {
			continue
		}
		// Liveness check: heartbeat timeout (applies to all non-reserved workers,
		// including idle — an idle worker with a stale heartbeat is disconnected).
		if now.Sub(w.lastSeen) > d.cfg.HeartbeatTimeout {
			dead = append(dead, id)
			continue
		}
		// Progress check: worker is busy or reviewing but has not made meaningful progress.
		if (w.state == protocol.WorkerBusy || w.state == protocol.WorkerReviewing) && !w.lastProgress.IsZero() && now.Sub(w.lastProgress) > d.cfg.ProgressTimeout {
			stuck = append(stuck, id)
			continue
		}
		// Review timeout: reviewing worker has stalled without progress.
		if w.state == protocol.WorkerReviewing && !w.lastProgress.IsZero() && now.Sub(w.lastProgress) > d.cfg.ReviewTimeout {
			stuck = append(stuck, id)
		}
	}
	// Remove dead workers and collect info for escalation after unlock.
	// Count managed exits inline to feed the reconcileScale cap (oro-kdne).
	var newManagedExits int
	deadWorkers := make([]workerExitInfo, 0, len(dead))
	for _, id := range dead {
		w := d.workers[id]
		deadWorkers = append(deadWorkers, workerExitInfo{workerID: id, beadID: w.beadID, assignmentID: w.assignmentID, prevSession: w.prevSession, managed: w.managed})
		if w.managed {
			newManagedExits++
		}
		_ = d.logEventLocked(ctx, "heartbeat_timeout", "dispatcher", w.beadID, id, "")
		_ = w.conn.Close()
		delete(d.workers, id)
	}
	// Kill stuck workers and collect info for escalation after unlock.
	stuckWorkers := make([]workerExitInfo, 0, len(stuck))
	for _, id := range stuck {
		w := d.workers[id]
		stuckWorkers = append(stuckWorkers, workerExitInfo{workerID: id, beadID: w.beadID, assignmentID: w.assignmentID, managed: w.managed})
		if w.managed {
			newManagedExits++
		}
		_ = d.logEventLocked(ctx, "progress_timeout", "dispatcher", w.beadID, id,
			fmt.Sprintf(`{"last_progress_ago":%q}`, now.Sub(w.lastProgress).Round(time.Second)))
		_ = w.conn.Close()
		delete(d.workers, id)
	}
	d.unexpectedManagedExits += newManagedExits
	d.mu.Unlock()

	// Wake the assign loop so reconcileScale can spawn replacements immediately.
	if len(deadWorkers)+len(stuckWorkers) > 0 {
		d.notifyAssignLoop()
	}

	// Escalate outside the lock and clear tracking maps for abandoned beads.
	d.escalateTimedOutWorkers(ctx, deadWorkers, stuckWorkers)

	// Kill OS processes for timed-out managed workers (best-effort, outside lock).
	d.killManagedWorkers(managedWorkerIDs(deadWorkers, stuckWorkers))
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
	d.safeGo(func() { d.shutdownWaitLoop(ctx, cancel, workerID) })
}

// shutdownWaitLoop polls for worker approval or timeout (extracted for complexity).
func (d *Dispatcher) shutdownWaitLoop(shutdownCtx context.Context, cancelFunc context.CancelFunc, workerID string) {
	defer cancelFunc() // Clean up context when goroutine exits

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-shutdownCtx.Done():
			if shutdownCtx.Err() == context.DeadlineExceeded {
				d.handleShutdownTimeout(workerID)
			}
			return
		case <-ticker.C:
			approved := d.checkShutdownApproved(workerID)
			if approved {
				return
			}
		}
	}
}

// handleShutdownTimeout sends hard SHUTDOWN after graceful shutdown timeout.
func (d *Dispatcher) handleShutdownTimeout(workerID string) {
	d.mu.Lock()
	var beadID string
	var assignmentID int64
	w, ok := d.workers[workerID]
	if ok {
		_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		beadID = w.beadID // capture before clearing
		assignmentID = w.assignmentID
		w.state = protocol.WorkerIdle
		w.assignmentID = 0
		w.beadID = ""
		w.shutdownCancel = nil
	}
	d.mu.Unlock()

	// Requeue any in-flight bead so it can be reassigned.
	if beadID != "" {
		ctx := context.Background()
		if err := d.beads.Update(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "scale_down_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		_ = d.completeAssignment(ctx, assignmentID, beadID)
		d.clearBeadTracking(beadID)
		_ = d.logEvent(ctx, "bead_requeued_scale_down", "dispatcher", beadID, workerID,
			`{"reason":"shutdown_timeout"}`)
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
	if w.shutdownApproved {
		w.shutdownCancel = nil
		return true
	}
	return false
}

// shutdownWaitForWorkers waits up to ShutdownTimeout for all workers to drain,
// then force-closes any remaining connections. After waiting, it kills any
// managed worker OS processes via procMgr (best-effort, errors ignored).
func (d *Dispatcher) shutdownWaitForWorkers() {
	deadline := time.NewTimer(d.cfg.ShutdownTimeout)
	defer deadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	// Capture managed IDs before the wait — workers may drain off the map.
	d.mu.Lock()
	managedIDs := make([]string, 0, len(d.workers))
	for id, w := range d.workers {
		if w.managed {
			managedIDs = append(managedIDs, id)
		}
	}
	d.mu.Unlock()

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
			d.killManagedWorkers(managedIDs)
			return
		case <-ticker.C:
			if d.ConnectedWorkers() == 0 {
				d.killManagedWorkers(managedIDs)
				return
			}
		}
	}
}

// killManagedWorkers sends Kill to each ID via procMgr.
// No-op when procMgr is nil. Errors are ignored (best-effort).
func (d *Dispatcher) killManagedWorkers(ids []string) {
	if d.procMgr == nil || len(ids) == 0 {
		return
	}
	for _, id := range ids {
		_ = d.procMgr.Kill(id)
	}
}
