package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// worktreeFailureCooldown is the minimum time before retrying worktree
// creation for a bead that previously failed. Prevents infinite retry loops
// when a stale branch or locked worktree cannot be cleaned up.
const worktreeFailureCooldown = 60 * time.Second

// BeadTracker manages bead-to-worker mapping and per-bead counters
// (attempts, handoffs, rejections, pending handoffs, QG stuck history).
// It is embedded in Dispatcher so that field access (e.g. d.attemptCounts)
// is promoted, keeping existing call-sites and tests unchanged.
// Synchronisation is provided by the Dispatcher-level mu.
type BeadTracker struct {
	rejectionCounts        map[string]int             // bead ID -> review rejection count
	reviewBlockedCounts    map[string]int             // bead ID -> consecutive blocked review count
	handoffCounts          map[string]int             // bead ID -> ralph handoff count
	attemptCounts          map[string]int             // bead ID -> QG retry attempt count (deterministic failures)
	transientCounts        map[string]int             // bead ID -> transient/flaky QG retry count (does not burn worker-fix budget)
	checkpointCounts       map[string]int             // bead ID -> checkpoint respawn count (§9.3 step 9)
	pendingHandoffs        map[string]*pendingHandoff // bead ID -> pending handoff info
	qgStuckTracker         map[string]*qgHistory      // bead ID -> consecutive QG output hashes
	escalatedBeads         map[string]bool            // bead ID -> true if PRIORITY_CONTENTION escalated
	worktreeFailures       map[string]time.Time       // bead ID -> last worktree creation failure time
	exhaustedBeads         map[string]bool            // bead ID -> true if QG retries exhausted (blocks re-assignment)
	assigningBeads         map[string]bool            // bead ID -> true if assignment in progress (oro-ptp2: prevents concurrent assignment)
	mergingBeads           map[string]bool            // bead ID -> true if mergeAndComplete is in-flight (oro-x4x8: prevents duplicate merge on external close)
	worktreeByBead         map[string]string          // bead ID -> worktree path (preserved on timeout/kill for respawn reuse, oro-1eo8)
	epicMergeFailed        map[string]bool            // epic ID -> true if FF-merge failed (blocks auto-close until a rebase fix child merges)
	epicCloseInFlight      map[string]bool            // epic ID -> true while tryCloseEpic is running (prevents duplicate acceptance and close side effects)
	processedExternalClose map[string]bool            // bead ID -> true once handleClosedAssignment has processed an external close (FM2: prevents re-entry)
	epicSkipLogged         map[string]bool            // epic ID -> true once non_executable_issue_type has been logged (dedup; oro-cn6a)
}

// --- Bead tracking helpers ---

// clearBeadTracking removes all tracking-map entries for a bead in a
// single lock acquisition. Call this on every terminal path (success,
// escalation, heartbeat timeout) to prevent map entry leaks.
func (d *Dispatcher) clearBeadTracking(beadID string) {
	d.mu.Lock()
	delete(d.attemptCounts, beadID)
	delete(d.transientCounts, beadID)
	delete(d.handoffCounts, beadID)
	delete(d.rejectionCounts, beadID)
	delete(d.reviewBlockedCounts, beadID)
	delete(d.checkpointCounts, beadID)
	delete(d.pendingHandoffs, beadID)
	delete(d.qgStuckTracker, beadID)
	delete(d.escalatedBeads, beadID)
	delete(d.worktreeFailures, beadID)
	delete(d.exhaustedBeads, beadID)
	delete(d.assigningBeads, beadID)
	delete(d.mergingBeads, beadID)
	delete(d.processedExternalClose, beadID)
	d.mu.Unlock()
}

// clearBeadTrackingPreservingBlockedReviewCount releases a retryable review
// assignment while retaining its consecutive blocked-review count.
func (d *Dispatcher) clearBeadTrackingPreservingBlockedReviewCount(beadID string) {
	d.mu.Lock()
	delete(d.attemptCounts, beadID)
	delete(d.transientCounts, beadID)
	delete(d.handoffCounts, beadID)
	delete(d.rejectionCounts, beadID)
	delete(d.checkpointCounts, beadID)
	delete(d.pendingHandoffs, beadID)
	delete(d.qgStuckTracker, beadID)
	delete(d.escalatedBeads, beadID)
	delete(d.worktreeFailures, beadID)
	delete(d.exhaustedBeads, beadID)
	delete(d.assigningBeads, beadID)
	delete(d.mergingBeads, beadID)
	delete(d.processedExternalClose, beadID)
	d.mu.Unlock()
}

// clearRejectionCount removes all review retry counters for a bead.
func (d *Dispatcher) clearRejectionCount(beadID string) {
	d.mu.Lock()
	delete(d.rejectionCounts, beadID)
	delete(d.reviewBlockedCounts, beadID)
	d.mu.Unlock()
}

// clearHandoffCount removes the handoff counter for a bead (e.g., on completion).
func (d *Dispatcher) clearHandoffCount(beadID string) {
	d.mu.Lock()
	delete(d.handoffCounts, beadID)
	d.mu.Unlock()
}

// consumePendingHandoff returns and removes a single pending handoff, or nil
// if none exist. Used when a new worker connects to immediately assign a
// ralph-handoff bead+worktree.
func (d *Dispatcher) consumePendingHandoff() *pendingHandoff {
	d.mu.Lock()
	defer d.mu.Unlock()
	for beadID, h := range d.pendingHandoffs {
		delete(d.pendingHandoffs, beadID)
		return h
	}
	return nil
}

// pruneStaleTracking removes orphaned entries from all tracking maps.
// An entry is orphaned if its bead ID is not currently assigned to any worker
// AND is not in the ready queue. This runs periodically in heartbeatLoop to
// prevent unbounded map growth from worker crashes and closed beads.
func (d *Dispatcher) pruneStaleTracking(ctx context.Context) {
	// Fetch ready beads from bead source (outside lock to avoid blocking).
	readyBeads, err := d.beads.Ready(ctx)
	if err != nil {
		// If we can't fetch ready beads, skip pruning this cycle to avoid
		// incorrectly deleting entries for queued beads.
		return
	}

	d.mu.Lock()

	// Collect all active bead IDs (assigned to workers OR in ready queue).
	activeBeads := make(map[string]bool)
	for _, w := range d.workers {
		if w.beadID != "" {
			activeBeads[w.beadID] = true
		}
	}
	for _, bead := range readyBeads {
		activeBeads[bead.ID] = true
	}

	// Find and delete orphaned bead IDs across all tracking maps.
	orphanCount := d.deleteOrphanedTracking(activeBeads)

	d.mu.Unlock()

	if orphanCount > 0 {
		_ = d.logEvent(ctx, "tracking_pruned", "dispatcher", "", "",
			fmt.Sprintf(`{"orphaned_count":%d}`, orphanCount))
	}
}

// deleteOrphanedTracking finds bead IDs present in tracking maps but not in
// activeBeads, deletes them, and returns the count. Caller must hold d.mu.
func (d *Dispatcher) deleteOrphanedTracking(activeBeads map[string]bool) int {
	orphaned := make(map[string]bool)
	for _, m := range d.allTrackingKeys() {
		if !activeBeads[m] {
			orphaned[m] = true
		}
	}
	for beadID := range orphaned {
		delete(d.attemptCounts, beadID)
		delete(d.transientCounts, beadID)
		delete(d.handoffCounts, beadID)
		delete(d.rejectionCounts, beadID)
		delete(d.reviewBlockedCounts, beadID)
		delete(d.checkpointCounts, beadID)
		delete(d.pendingHandoffs, beadID)
		delete(d.qgStuckTracker, beadID)
		delete(d.escalatedBeads, beadID)
		delete(d.worktreeFailures, beadID)
		delete(d.exhaustedBeads, beadID)
		delete(d.assigningBeads, beadID)
		delete(d.mergingBeads, beadID)
		delete(d.worktreeByBead, beadID)
		delete(d.epicMergeFailed, beadID)
		delete(d.processedExternalClose, beadID)
	}
	return len(orphaned)
}

// gcWorktrees removes filesystem worktrees and branches for closed beads.
// It calls GCClosedWorktrees with an isBeadClosed callback that uses
// beads.Show to check status; Show failures return false (conservative).
func (d *Dispatcher) gcWorktrees(ctx context.Context) {
	if d.beads == nil {
		return
	}

	quarantined := map[string]bool{}
	if d.db != nil {
		var err error
		quarantined, err = d.openRecoveryQuarantineBeads(ctx)
		if err != nil {
			_ = d.logEvent(ctx, "gc_worktrees_quarantine_filter_failed", "dispatcher", "", "", err.Error())
			return
		}
	}
	isBeadClosed := func(beadID string) bool {
		if quarantined[beadID] {
			_ = d.logEvent(ctx, "gc_skipped_recovery_quarantined", "dispatcher", beadID, "",
				`{"reason":"open_recovery_quarantine"}`)
			return false
		}
		detail, err := d.beads.Show(ctx, beadID)
		if err != nil {
			return false
		}
		if detail == nil {
			return false
		}
		return detail.Status == "closed"
	}
	if err := d.worktrees.GCClosedWorktrees(ctx, isBeadClosed); err != nil {
		slog.WarnContext(ctx, "gc_worktrees_failed", "error", err.Error())
	}
}

// addIntMapKeys marks every key in m as present in seen.
func addIntMapKeys(seen map[string]bool, m map[string]int) {
	for id := range m {
		seen[id] = true
	}
}

// addBoolMapKeys marks every key in m as present in seen.
func addBoolMapKeys(seen, m map[string]bool) {
	for id := range m {
		seen[id] = true
	}
}

// allTrackingKeys returns all bead IDs referenced across tracking maps.
// Caller must hold d.mu.
func (d *Dispatcher) allTrackingKeys() []string {
	seen := make(map[string]bool)
	addIntMapKeys(seen, d.attemptCounts)
	addIntMapKeys(seen, d.transientCounts)
	addIntMapKeys(seen, d.handoffCounts)
	addIntMapKeys(seen, d.rejectionCounts)
	addIntMapKeys(seen, d.reviewBlockedCounts)
	addIntMapKeys(seen, d.checkpointCounts)
	for id := range d.pendingHandoffs {
		seen[id] = true
	}
	for id := range d.qgStuckTracker {
		seen[id] = true
	}
	for id := range d.worktreeFailures {
		seen[id] = true
	}
	for id := range d.worktreeByBead {
		seen[id] = true
	}
	addBoolMapKeys(seen, d.escalatedBeads)
	addBoolMapKeys(seen, d.exhaustedBeads)
	addBoolMapKeys(seen, d.assigningBeads)
	addBoolMapKeys(seen, d.mergingBeads)
	addBoolMapKeys(seen, d.epicMergeFailed)
	addBoolMapKeys(seen, d.processedExternalClose)
	keys := make([]string, 0, len(seen))
	for id := range seen {
		keys = append(keys, id)
	}
	return keys
}
