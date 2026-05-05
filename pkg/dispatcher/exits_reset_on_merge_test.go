package dispatcher //nolint:testpackage // white-box: asserts unexpectedManagedExits is reset by finalizeSuccessfulMerge

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

// TestUnexpectedManagedExitsResetsOnSuccessfulMerge regression-tests oro-1dbr.
//
// Background: dispatcher PID 85593 ran for ~10h with --workers 2 and merged 38+
// beads. After ~3 natural worker exits (one bead bumped twice past QG retry
// limit, worker process exited, plus normal turnover), unexpectedManagedExits
// reached the cap (managedCount + exits >= 2*target = 4). reconcileScale
// permanently refused to spawn a replacement, leaving managed=1 indefinitely.
//
// The cap exists to stop rapid crash-respawn loops (oro-135n / oro-kdne), not
// to penalize long-uptime dispatchers that have produced healthy work. A
// successful bead merge is a strong signal that the system is producing and
// not in a crash loop, so finalizeSuccessfulMerge resets the counter.
//
// This test pre-loads exits=3 (which would trip the cap with target=2),
// invokes finalizeSuccessfulMerge with the minimum scaffolding needed for it
// not to panic, and asserts exits is back to 0.
func TestUnexpectedManagedExitsResetsOnSuccessfulMerge(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-merged"
	workerID := "w-merged"
	worktree := "/tmp/worktree-" + beadID

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		beadID: {ID: beadID, Status: "in_progress"},
	}
	beadSrc.mu.Unlock()

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	d.mu.Lock()
	d.unexpectedManagedExits = 3
	d.mu.Unlock()

	d.finalizeSuccessfulMerge(ctx, beadID, workerID, worktree, "", "main", assignmentID, "deadbeef")

	d.mu.Lock()
	exits := d.unexpectedManagedExits
	d.mu.Unlock()

	if exits != 0 {
		t.Fatalf("expected unexpectedManagedExits=0 after successful merge (would unblock reconcileScale), got %d", exits)
	}
}
