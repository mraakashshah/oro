package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"

	"oro/pkg/cards"
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

func (d *Dispatcher) registerWorker(id string, conn net.Conn) {
	d.mu.Lock()
	// Consume the pending managed ID if present (delete is no-op if absent).
	managed := d.pendingManagedIDs[id]
	spawnFor := d.pendingSpawnForWorkers[id]
	pendingTargetBeadID := d.pendingWorkerTargets[id]
	delete(d.pendingManagedIDs, id)
	delete(d.pendingManagedSince, id)
	delete(d.pendingWorkerTargets, id)
	delete(d.pendingSpawnForWorkers, id)
	delete(d.pendingExternalIDs, id)
	delete(d.pendingExternalSince, id)
	d.upsertWorker(id, conn, managed)
	w, ok := d.workers[id]
	if !ok || w == nil {
		d.mu.Unlock()
		return
	}
	applyPendingWorkerRegistration(w, spawnFor, pendingTargetBeadID)
	if d.cfg.MaxWorkers > 0 && d.liveWorkerCountLocked() > d.cfg.MaxWorkers {
		w.markShuttingDownWithoutAssignment()
		sendShutdownWithoutBuffering(w)
		d.mu.Unlock()
		return
	}
	if w.spawnFor && w.state == protocol.WorkerShuttingDown {
		w.markShuttingDownWithoutAssignment()
		sendShutdownWithoutBuffering(w)
		d.mu.Unlock()
		return
	}
	targetBeadID := w.targetBeadID

	// Check for pending ralph handoffs. Spawn-for workers may only consume a
	// handoff for their target bead; unrelated handoffs must wait for a general
	// worker or their own handoff respawn.
	var h *pendingHandoff
	var handoffBeadID string
	if targetBeadID != "" {
		if ph, ok := d.pendingHandoffs[targetBeadID]; ok {
			h = ph
			handoffBeadID = targetBeadID
		}
	} else {
		for beadID, ph := range d.pendingHandoffs {
			h = ph
			handoffBeadID = beadID
			break
		}
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

func applyPendingWorkerRegistration(w *trackedWorker, spawnFor bool, pendingTargetBeadID string) {
	if spawnFor {
		w.spawnFor = true
	}
	if pendingTargetBeadID != "" {
		w.targetBeadID = pendingTargetBeadID
	}
}

// assignHandoffToWorker assigns pending handoff h to the just-registered worker id.
// Caller must hold d.mu; on return d.mu is unlocked. The function temporarily
// releases d.mu during card retrieval. handoffBeadID is the key for h in
// d.pendingHandoffs.
func (d *Dispatcher) assignHandoffToWorker(id, handoffBeadID string, h *pendingHandoff) {
	w := d.workers[id]
	if w == nil {
		if _, exists := d.pendingHandoffs[handoffBeadID]; !exists {
			d.pendingHandoffs[handoffBeadID] = h
		}
		d.mu.Unlock()
		return
	}
	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	w.state = protocol.WorkerReserved
	w.assignmentID = h.assignmentID
	w.execution = h.execution
	w.beadID = h.beadID
	w.worktree = h.worktree
	w.runtime = h.runtime
	w.model = h.model
	w.reasoning = h.reasoning
	w.epicID = h.epicID
	w.baseBranch = h.baseBranch
	w.targetBranch = h.targetBranch
	w.lastProgress = d.nowFunc()

	cardsCtx := d.buildHandoffCardContext(h)
	defer d.mu.Unlock()

	// Phase 2: Verify reservation still valid, then transition to Busy.
	w, ok := d.workers[id]
	if !ok || w == nil || w.state != protocol.WorkerReserved {
		if _, exists := d.pendingHandoffs[handoffBeadID]; !exists {
			d.pendingHandoffs[handoffBeadID] = h
		}
		return
	}
	w.state = protocol.WorkerBusy
	w.targetBeadID = ""
	if err := d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:       h.beadID,
			Worktree:     h.worktree,
			AssignmentID: h.execution.AssignmentID,
			Generation:   h.execution.Generation,
			ActorRole:    h.execution.ActorRole,
			Project:      h.execution.Project,
			Capability:   h.execution.Capability,
			Runtime:      h.runtime,
			Model:        h.model,
			Reasoning:    h.reasoning,
			Cards:        cardsCtx,
			TargetBranch: h.targetBranch,
			Feedback:     h.nextAction,
			Attempt:      h.checkpointTurn,
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

func (d *Dispatcher) buildHandoffCardContext(h *pendingHandoff) cards.RelevantCards {
	d.mu.Unlock()
	if d.testUnlockHook != nil {
		d.testUnlockHook()
	}
	cardsCtx := d.buildCardContext(context.Background(), protocol.Bead{
		ID:     h.beadID,
		Title:  h.title,
		Labels: h.labels,
	})
	d.mu.Lock()
	return cardsCtx
}

func (d *Dispatcher) assignPendingHandoffsToIdleWorkers() {
	for {
		d.mu.Lock()
		var workerID, handoffBeadID string
		var h *pendingHandoff
		for id, w := range d.workers {
			if w.state != protocol.WorkerIdle || w.spawnFor || w.targetBeadID != "" {
				continue
			}
			workerID = id
			break
		}
		if workerID == "" {
			d.mu.Unlock()
			return
		}
		for beadID, pending := range d.pendingHandoffs {
			handoffBeadID = beadID
			h = pending
			break
		}
		if h == nil {
			d.mu.Unlock()
			return
		}
		d.assignHandoffToWorker(workerID, handoffBeadID, h)
	}
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
	worktree     string
	baseBranch   string
	assignmentID int64
	prevSession  bool // worker is from a previous dispatcher session
	managed      bool // worker was spawned by the dispatcher (procMgr)
	reviewing    bool // worker was in an active ops review
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
		if dw.prevSession {
			if dw.beadID != "" {
				d.clearBeadTracking(dw.beadID)
			}
			continue
		}

		d.escalate(ctx, protocol.FormatEscalation(protocol.EscWorkerCrash, dw.beadID, "worker disconnected", "heartbeat timeout for worker "+dw.workerID), dw.beadID, dw.workerID)
		if dw.beadID == "" {
			continue
		}
		if d.quarantineDisconnectedPreservedAssignment(ctx, dw.workerID, dw.beadID, dw.assignmentID, dw.worktree, dw.baseBranch, "heartbeat timeout for worker "+dw.workerID) {
			d.clearBeadTracking(dw.beadID)
			continue
		}
		if err := d.updateBeadStatus(ctx, dw.beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "heartbeat_bead_reset_failed", "dispatcher", dw.beadID, dw.workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		_ = d.completeAssignment(ctx, dw.assignmentID, dw.beadID)
		d.clearBeadTracking(dw.beadID)
	}
	for _, sw := range stuck {
		d.handleStuckTimedOutWorker(ctx, sw)
	}
}

func (d *Dispatcher) handleStuckTimedOutWorker(ctx context.Context, sw workerExitInfo) {
	escalation := protocol.FormatEscalation(protocol.EscStuckWorker, sw.beadID,
		"worker stalled with no progress", "progress timeout for worker "+sw.workerID)
	if sw.reviewing && d.ops != nil {
		if _, err := d.ops.CancelReviewsForBead(sw.beadID); err != nil {
			_ = d.logEvent(ctx, "review_timeout_cancel_failed", "dispatcher", sw.beadID, sw.workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		// A review timeout owns the existing ops process. Do not immediately
		// replace it with a same-bead escalation process: callers need the
		// cancellation boundary to leave no active review for this bead.
		d.escalateWithoutOneShot(ctx, escalation, sw.beadID, sw.workerID)
	} else {
		d.escalate(ctx, escalation, sw.beadID, sw.workerID)
	}
	if sw.beadID == "" {
		return
	}

	if sw.assignmentID <= 0 {
		d.reopenTimedOutWorkerBead(ctx, sw)
		return
	}

	blocked, details, err := d.recoveryWorkBlocked(ctx, sw.beadID, sw.worktree, sw.baseBranch)
	if blocked || err != nil {
		d.quarantineProgressTimeoutRecovery(ctx, sw, details, err)
		return
	}

	d.reopenTimedOutWorkerBead(ctx, sw)
}

func (d *Dispatcher) quarantineProgressTimeoutRecovery(ctx context.Context, sw workerExitInfo, details string, err error) {
	if err != nil {
		details = appendRecoveryDetail(details, "error: "+err.Error())
	}
	if details == "" {
		details = "progress timeout could not prove worker recovery state safe"
	}
	d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
		BeadID:       sw.beadID,
		AssignmentID: sw.assignmentID,
		WorkerID:     sw.workerID,
		Worktree:     sw.worktree,
		Branch:       protocol.BranchPrefix + sw.beadID,
		Reason:       "progress_timeout_recovery_blocked",
		Details:      details,
	})
	d.clearBeadTracking(sw.beadID)
}

func (d *Dispatcher) reopenTimedOutWorkerBead(ctx context.Context, sw workerExitInfo) {
	// Reset the bead to "open" so it can be reassigned, mirroring the
	// graceful-disconnect path in dispatcher.go.
	if err := d.updateBeadStatus(ctx, sw.beadID, "open"); err != nil {
		_ = d.logEvent(ctx, "progress_timeout_bead_reset_failed", "dispatcher", sw.beadID, sw.workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	_ = d.completeAssignment(ctx, sw.assignmentID, sw.beadID)
	d.clearBeadTracking(sw.beadID)
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
				_ = d.refreshExpiringCapabilities(ctx, d.nowFunc())
			case <-pruneTicker.C:
				d.pruneStaleTracking(ctx)
				d.detectAndResolveDuplicateActiveAssignments(ctx)
			case <-gcTicker.C:
				d.gcWorktrees(ctx)
			}
			return false
		}()
		if exit {
			return
		}
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
	dead, stuck, stoppedSpawnFor := d.collectTimedOutWorkersLocked(now)
	deadWorkers, deadManagedExits := d.removeDeadWorkersLocked(ctx, dead)
	d.removeStoppedSpawnForWorkersLocked(ctx, stoppedSpawnFor)
	stuckWorkers, stuckManagedExits := d.removeStuckWorkersLocked(ctx, stuck, now)
	newManagedExits := deadManagedExits + stuckManagedExits
	d.unexpectedManagedExits += newManagedExits
	d.clampManagedExitCapAfterPoolDrainLocked(ctx, newManagedExits)
	hasManagedIdle := d.hasManagedIdleWorkersLocked()
	d.mu.Unlock()

	// Wake the assign loop so reconcileScale can spawn replacements immediately,
	// and so idle managed workers pick up newly-ready beads without waiting for
	// the next poll tick (oro-ntr3).
	if len(deadWorkers)+len(stuckWorkers) > 0 || hasManagedIdle {
		d.notifyAssignLoop()
	}

	// Escalate outside the lock and clear tracking maps for abandoned beads.
	d.escalateTimedOutWorkers(ctx, deadWorkers, stuckWorkers)

	// Kill OS processes for timed-out managed workers (best-effort, outside lock).
	d.killManagedWorkers(managedWorkerIDs(deadWorkers, stuckWorkers))
}

func (d *Dispatcher) clampManagedExitCapAfterPoolDrainLocked(ctx context.Context, newManagedExits int) {
	target := d.targetWorkers
	if newManagedExits == 0 || target <= 0 {
		return
	}
	if d.managedWorkerCountLocked() != 0 {
		return
	}
	capAt := 2 * target
	if d.unexpectedManagedExits < capAt {
		return
	}
	d.unexpectedManagedExits = target
	_ = d.logEventLocked(ctx, "managed_exit_cap_clamped", "dispatcher", "", "",
		fmt.Sprintf(`{"target":%d,"cap":%d}`, target, capAt))
}

func (d *Dispatcher) hasManagedIdleWorkersLocked() bool {
	for _, w := range d.workers {
		if w.managed && !w.spawnFor && w.state == protocol.WorkerIdle {
			return true
		}
	}
	return false
}

// reviewDeadStateLocked tracks whether a reviewing worker's ops subprocess is
// absent and whether its ReviewDeadGrace window has expired. It resets the timer
// when the review is active and starts it on first absence. Must be called with
// d.mu held.
func (d *Dispatcher) reviewDeadStateLocked(w *trackedWorker, now time.Time) (expired, graceActive bool) {
	if !w.managed || w.state != protocol.WorkerReviewing {
		return false, false
	}
	if d.ops == nil || d.ops.HasActiveForBead(w.beadID) {
		w.reviewDeadSince = time.Time{}
		return false, false
	}
	if w.reviewDeadSince.IsZero() {
		w.reviewDeadSince = now
		return false, true
	}
	if now.Sub(w.reviewDeadSince) > d.cfg.ReviewDeadGrace {
		return true, false
	}
	return false, true
}

func (d *Dispatcher) collectTimedOutWorkersLocked(now time.Time) (dead, stuck, stoppedSpawnFor []string) {
	for id, w := range d.workers {
		if w.state == protocol.WorkerReserved {
			continue
		}
		if stoppedSpawnForHeartbeatTimedOut(w, now, d.cfg.HeartbeatTimeout) {
			stoppedSpawnFor = append(stoppedSpawnFor, id)
			continue
		}
		// Liveness check: heartbeat timeout (applies to all non-reserved workers,
		// including idle — an idle worker with a stale heartbeat is disconnected).
		if heartbeatTimedOut(w, now, d.cfg.HeartbeatTimeout) {
			dead = append(dead, id)
			continue
		}
		// Dead process check: managed reviewing worker whose OS process has exited.
		// Reviewing workers may keep heartbeating long after their process dies
		// (the review timeout is 15m), so we detect exit via signal(0) instead.
		if w.managed && w.state == protocol.WorkerReviewing && d.procMgr != nil && !d.procMgr.IsAlive(id) {
			dead = append(dead, id)
			continue
		}
		// Dead ops review check: reviewing worker whose ops subprocess has exited
		// while the OS process is still alive. After ReviewDeadGrace, remove worker.
		reviewDead, reviewGraceActive := d.reviewDeadStateLocked(w, now)
		if reviewDead {
			dead = append(dead, id)
			continue
		}
		// A missing ops review gets its full grace period before any progress
		// or review timeout can reap the owning worker.
		if reviewGraceActive {
			continue
		}
		// Progress check: a busy coding worker has not made meaningful progress.
		// Reviewing workers use the separate ReviewTimeout below, allowing an
		// active ops review to outlive the shorter coding-progress deadline.
		if workerProgressTimedOut(w, now, d.cfg.ProgressTimeout) {
			stuck = append(stuck, id)
			continue
		}
		// Review timeout: reviewing worker has stalled without progress.
		if workerReviewTimedOut(w, now, d.cfg.ReviewTimeout) {
			stuck = append(stuck, id)
		}
	}
	return dead, stuck, stoppedSpawnFor
}

func (d *Dispatcher) removeDeadWorkersLocked(ctx context.Context, dead []string) (deadWorkers []workerExitInfo, managedExits int) {
	deadWorkers = make([]workerExitInfo, 0, len(dead))
	for _, id := range dead {
		w := d.workers[id]
		if w == nil {
			continue
		}
		deadWorkers = append(deadWorkers, workerExitInfo{workerID: id, beadID: w.beadID, worktree: w.worktree, baseBranch: w.baseBranch, assignmentID: w.assignmentID, prevSession: w.prevSession, managed: w.managed})
		if w.managed && !w.spawnFor {
			managedExits++
		}
		_ = d.logEventLocked(ctx, "heartbeat_timeout", "dispatcher", w.beadID, id, "")
		_ = w.conn.Close()
		delete(d.workers, id)
	}
	return deadWorkers, managedExits
}

func (d *Dispatcher) removeStoppedSpawnForWorkersLocked(ctx context.Context, stoppedSpawnFor []string) {
	for _, id := range stoppedSpawnFor {
		w := d.workers[id]
		if w == nil {
			continue
		}
		_ = d.logEventLocked(ctx, "spawn_for_shutdown_timeout", "dispatcher", "", id, "")
		_ = w.conn.Close()
		delete(d.workers, id)
	}
}

func (d *Dispatcher) removeStuckWorkersLocked(ctx context.Context, stuck []string, now time.Time) (stuckWorkers []workerExitInfo, managedExits int) {
	stuckWorkers = make([]workerExitInfo, 0, len(stuck))
	for _, id := range stuck {
		w := d.workers[id]
		if w == nil {
			continue
		}
		stuckWorkers = append(stuckWorkers, workerExitInfo{workerID: id, beadID: w.beadID, worktree: w.worktree, baseBranch: w.baseBranch, assignmentID: w.assignmentID, managed: w.managed, reviewing: w.state == protocol.WorkerReviewing})
		if w.managed && !w.spawnFor {
			managedExits++
		}
		_ = d.logEventLocked(ctx, "progress_timeout", "dispatcher", w.beadID, id,
			fmt.Sprintf(`{"last_progress_ago":%q}`, now.Sub(w.lastProgress).Round(time.Second)))
		_ = w.conn.Close()
		delete(d.workers, id)
	}
	return stuckWorkers, managedExits
}

func stoppedSpawnForHeartbeatTimedOut(w *trackedWorker, now time.Time, timeout time.Duration) bool {
	return w.spawnFor && w.state == protocol.WorkerShuttingDown && w.beadID == "" && heartbeatTimedOut(w, now, timeout)
}

func heartbeatTimedOut(w *trackedWorker, now time.Time, timeout time.Duration) bool {
	return now.Sub(w.lastSeen) > timeout
}

func workerProgressTimedOut(w *trackedWorker, now time.Time, timeout time.Duration) bool {
	return !w.spawnFor && w.state == protocol.WorkerBusy &&
		!w.lastProgress.IsZero() && now.Sub(w.lastProgress) > timeout
}

func workerReviewTimedOut(w *trackedWorker, now time.Time, timeout time.Duration) bool {
	return w.state == protocol.WorkerReviewing && !w.lastProgress.IsZero() && now.Sub(w.lastProgress) > timeout
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

	if err := w.conn.SetWriteDeadline(time.Now().Add(directWorkerWriteTimeout)); err == nil {
		defer func() { _ = w.conn.SetWriteDeadline(time.Time{}) }()
	}
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
	d.gracefulShutdownWorker(workerID, timeout, "")
}

func (d *Dispatcher) gracefulShutdownWorker(workerID string, timeout time.Duration, reason string) {
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
	w.shutdownReason = reason
	if reason == shutdownReasonScaleDown || w.spawnFor {
		w.state = protocol.WorkerShuttingDown
	}

	if w.spawnFor {
		sendPrepareShutdownWithoutBuffering(w, timeout)
	} else {
		_ = d.sendToWorker(w, protocol.Message{
			Type: protocol.MsgPrepareShutdown,
			PrepareShutdown: &protocol.PrepareShutdownPayload{
				Timeout: timeout,
			},
		})
	}
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
	dispatcherStopping := false
	w, ok := d.workers[workerID]
	if ok {
		beadID = w.beadID // capture before clearing
		assignmentID = w.assignmentID
		dispatcherStopping = d.state == StateStopping
		if w.shutdownReason == shutdownReasonScaleDown || w.spawnFor {
			sendShutdownWithoutBuffering(w)
			w.markShuttingDownWithoutAssignment()
		} else {
			_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
			w.state = protocol.WorkerIdle
			w.shutdownReason = ""
			w.assignmentID = 0
			w.beadID = ""
		}
		w.shutdownCancel = nil
	}
	d.mu.Unlock()

	// Requeue any in-flight bead so it can be reassigned.
	if beadID != "" {
		ctx := context.Background()
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "scale_down_bead_reset_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
		}
		if dispatcherStopping {
			_ = d.logEvent(ctx, "bead_requeued_shutdown_timeout", "dispatcher", beadID, workerID,
				`{"reason":"shutdown_timeout"}`)
			return
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
