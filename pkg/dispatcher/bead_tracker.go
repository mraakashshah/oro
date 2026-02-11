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

	// Find orphaned bead IDs across all tracking maps.
	orphanedBeads := make(map[string]bool)
	for beadID := range d.attemptCounts {
		if !activeBeads[beadID] {
			orphanedBeads[beadID] = true
		}
	}
	for beadID := range d.handoffCounts {
		if !activeBeads[beadID] {
			orphanedBeads[beadID] = true
		}
	}
	for beadID := range d.rejectionCounts {
		if !activeBeads[beadID] {
			orphanedBeads[beadID] = true
		}
	}
	for beadID := range d.pendingHandoffs {
		if !activeBeads[beadID] {
			orphanedBeads[beadID] = true
		}
	}
	for beadID := range d.qgStuckTracker {
		if !activeBeads[beadID] {
			orphanedBeads[beadID] = true
		}
	}
	for beadID := range d.escalatedBeads {
		if !activeBeads[beadID] {
			orphanedBeads[beadID] = true
		}
	}

	// Delete all orphaned entries.
	for beadID := range orphanedBeads {
		delete(d.attemptCounts, beadID)
		delete(d.handoffCounts, beadID)
		delete(d.rejectionCounts, beadID)
		delete(d.pendingHandoffs, beadID)
		delete(d.qgStuckTracker, beadID)
		delete(d.escalatedBeads, beadID)
	}

	d.mu.Unlock()

	// Log the cleanup event if any orphans were found.
	if len(orphanedBeads) > 0 {
		_ = d.logEvent(ctx, "tracking_pruned", "dispatcher", "", "",
			fmt.Sprintf(`{"orphaned_count":%d}`, len(orphanedBeads)))
	}
}
