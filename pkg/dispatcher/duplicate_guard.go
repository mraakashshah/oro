package dispatcher

import (
	"context"
	"fmt"

	"oro/pkg/protocol"
)

// beadCandidate is a busy worker competing for ownership of a single bead.
type beadCandidate struct {
	workerID     string
	assignmentID int64
}

// duplicateLoser records a worker that lost the duplicate-bead election.
type duplicateLoser struct {
	workerID     string
	assignmentID int64
	beadID       string
	encoder      interface{ Encode(v any) error }
}

// detectAndResolveDuplicateActiveAssignments scans the in-memory worker map for
// cases where two or more WorkerBusy workers hold the same non-empty beadID.
// Such duplicates should never occur, but can arise from bugs in the handoff /
// reconnect paths.  For each duplicate group the function:
//  1. Keeps the worker with the highest assignmentID (the most-recently-created
//     assignment wins).
//  2. Clears the loser's in-memory bead state (beadID, epicID, assignmentID).
//  3. Sends a SHUTDOWN message to every loser's connection (outside the lock).
//  4. Marks each loser's active DB assignment record as completed.
//  5. Logs a "duplicate_worker_bead" event for each resolved duplicate.
func (d *Dispatcher) detectAndResolveDuplicateActiveAssignments(ctx context.Context) {
	losers := d.collectDuplicateLosers()
	for _, loser := range losers {
		d.evictDuplicateLoser(ctx, loser)
	}
}

// collectDuplicateLosers identifies workers that lost the bead-ownership election
// and clears their in-memory assignment state while holding d.mu. Returns the
// list of losers so that Phase 2 (I/O) can run outside the lock.
func (d *Dispatcher) collectDuplicateLosers() []duplicateLoser {
	d.mu.Lock()
	defer d.mu.Unlock()

	byBead := make(map[string][]beadCandidate)
	for id, w := range d.workers {
		if w.beadID != "" && w.state == protocol.WorkerBusy {
			byBead[w.beadID] = append(byBead[w.beadID], beadCandidate{id, w.assignmentID})
		}
	}

	var losers []duplicateLoser
	for beadID, candidates := range byBead {
		if len(candidates) < 2 {
			continue
		}
		losers = append(losers, d.electAndEvictLocked(beadID, candidates)...)
	}
	return losers
}

// electAndEvictLocked picks the winner (highest assignmentID) for a bead that
// has multiple busy workers, clears the losers' assignment state, and returns
// their records for downstream I/O. Caller must hold d.mu.
func (d *Dispatcher) electAndEvictLocked(beadID string, candidates []beadCandidate) []duplicateLoser {
	winner := candidates[0]
	for _, c := range candidates[1:] {
		if c.assignmentID > winner.assignmentID {
			winner = c
		}
	}

	var losers []duplicateLoser
	for _, c := range candidates {
		if c.workerID == winner.workerID {
			continue
		}
		w := d.workers[c.workerID]
		if w == nil {
			continue
		}
		losers = append(losers, duplicateLoser{
			workerID:     c.workerID,
			assignmentID: c.assignmentID,
			beadID:       beadID,
			encoder:      w.encoder,
		})
		w.beadID = ""
		w.state = protocol.WorkerIdle
		w.assignmentID = 0
		w.epicID = ""
	}
	return losers
}

// evictDuplicateLoser sends SHUTDOWN to a loser, marks its DB assignment
// completed, and logs the resolution event. Runs outside d.mu.
func (d *Dispatcher) evictDuplicateLoser(ctx context.Context, loser duplicateLoser) {
	if loser.encoder != nil {
		_ = loser.encoder.Encode(protocol.Message{Type: protocol.MsgShutdown})
	}

	if loser.assignmentID > 0 {
		_ = d.completeAssignment(ctx, loser.assignmentID, loser.beadID)
	} else {
		_, _ = d.db.ExecContext(ctx,
			`UPDATE assignments SET status='completed', completed_at=datetime('now')
			 WHERE bead_id=? AND worker_id=? AND status='active'`,
			loser.beadID, loser.workerID)
	}

	_ = d.logEvent(ctx, "duplicate_worker_bead", "dispatcher", loser.beadID, loser.workerID,
		fmt.Sprintf(`{"evicted_worker":%q,"assignment_id":%d}`, loser.workerID, loser.assignmentID))
}
