//nolint:testpackage // The reconciliation regression verifies dispatcher-owned state directly.
package dispatcher

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

// TestReopenedUnownedTaskStaysOpen prevents reconciliation from reactivating
// a bead after its prior assignment has completed and ownership is gone.
func TestReopenedUnownedTaskStaysOpen(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "oro-reopened-unowned"

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "open"}
	beadSrc.mu.Unlock()

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, '', '', 'completed')`,
		beadID,
	); err != nil {
		t.Fatalf("insert completed assignment: %v", err)
	}

	if recoverable, _, err := d.restoreState(ctx); err != nil {
		t.Fatalf("restore state: %v", err)
	} else if len(recoverable) != 0 {
		t.Fatalf("recoverable assignments = %v, want none", recoverable)
	}

	d.checkClosedBeadAssignments(ctx)
	if got := d.abandonStaleActiveAssignments(ctx); got != 0 {
		t.Fatalf("abandoned stale assignments = %d, want 0", got)
	}

	beadSrc.mu.Lock()
	updated, statusChanged := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if statusChanged {
		t.Fatalf("unowned reopened bead status changed to %q, want open", updated)
	}

	d.mu.Lock()
	workers := len(d.workers)
	d.mu.Unlock()
	if workers != 0 {
		t.Fatalf("claimed workers = %d, want 0", workers)
	}

	var activeAssignments, quarantines, assignments int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`, beadID,
	).Scan(&activeAssignments); err != nil {
		t.Fatalf("count active assignments: %v", err)
	}
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID,
	).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE bead_id=?`, beadID,
	).Scan(&assignments); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if activeAssignments != 0 || quarantines != 0 || assignments != 1 {
		t.Fatalf("active=%d quarantines=%d assignments=%d, want 0, 0, 1", activeAssignments, quarantines, assignments)
	}
}
