package dispatcher

import (
	"context"
	"fmt"
)

// BeadTracker manages bead-to-worker mapping and per-bead counters
// (attempts, handoffs, rejections, pending handoffs, QG stuck history).
// It is embedded in Dispatcher so that field access (e.g. d.attemptCounts)
// is promoted, keeping existing call-sites and tests unchanged.
// Synchronisation is provided by the Dispatcher-level mu.
type BeadTracker struct {
	rejectionCounts map[string]int             // bead ID -> review rejection count
	handoffCounts   map[string]int             // bead ID -> ralph handoff count
	attemptCounts   map[string]int             // bead ID -> QG retry attempt count
	pendingHandoffs map[string]*pendingHandoff // bead ID -> pending handoff info
	qgStuckTracker  map[string]*qgHistory      // bead ID -> consecutive QG output hashes
	escalatedBeads  map[string]bool            // bead ID -> true if PRIORITY_CONTENTION escalated
}

// --- Bead tracking helpers ---

// clearBeadTracking removes all tracking-map entries for a bead in a
// single lock acquisition. Call this on every terminal path (success,
// escalation, heartbeat timeout) to prevent map entry leaks.
func (d *Dispatcher) clearBeadTracking(beadID string) {
	d.mu.Lock()
	delete(d.attemptCounts, beadID)
	delete(d.handoffCounts, beadID)
	delete(d.rejectionCounts, beadID)
	delete(d.pendingHandoffs, beadID)
	delete(d.qgStuckTracker, beadID)
	delete(d.escalatedBeads, beadID)
	d.mu.Unlock()
}

// clearRejectionCount removes the rejection counter for a bead (e.g., on approval or completion).
func (d *Dispatcher) clearRejectionCount(beadID string) {
	d.mu.Lock()
	delete(d.rejectionCounts, beadID)
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
// An entry is orphaned if its bead ID is not currently assigned to any worker.
// This runs periodically in heartbeatLoop to prevent unbounded map growth from
// worker crashes that occur before escalation.
func (d *Dispatcher) pruneStaleTracking(ctx context.Context) {
	d.mu.Lock()

	// Collect all active bead IDs from workers.
	activeBeads := make(map[string]bool)
	for _, w := range d.workers {
		if w.beadID != "" {
			activeBeads[w.beadID] = true
		}
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
		delete(d.handoffCounts, beadID)
		delete(d.rejectionCounts, beadID)
		delete(d.pendingHandoffs, beadID)
		delete(d.qgStuckTracker, beadID)
		delete(d.escalatedBeads, beadID)
	}
	return len(orphaned)
}

// allTrackingKeys returns all bead IDs referenced across tracking maps.
// Caller must hold d.mu.
func (d *Dispatcher) allTrackingKeys() []string {
	seen := make(map[string]bool)
	for id := range d.attemptCounts {
		seen[id] = true
	}
	for id := range d.handoffCounts {
		seen[id] = true
	}
	for id := range d.rejectionCounts {
		seen[id] = true
	}
	for id := range d.pendingHandoffs {
		seen[id] = true
	}
	for id := range d.qgStuckTracker {
		seen[id] = true
	}
	for id := range d.escalatedBeads {
		seen[id] = true
	}
	keys := make([]string, 0, len(seen))
	for id := range seen {
		keys = append(keys, id)
	}
	return keys
}
