package dispatcher //nolint:testpackage // white-box: asserts abandonStaleActiveAssignments touches DB rows directly

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestAbandonStaleActiveAssignments_ResetsOpenBeads regression-tests oro-tczh.
//
// Background: dispatcher PID 48471 silently died on 2026-05-05; the new
// dispatcher came up with 9 'active' assignment rows still pointing at dead
// workers. Because beads_ready excludes any bead with an active assignment,
// those 9 beads vanished from the queue (1 ready vs 55 open) and the factory
// stalled until manually unstuck.
//
// The fix: after startup, the dispatcher walks every status='active'
// assignment and abandons any whose worker is not in the connected pool. For
// in_progress beads, it also resets the bead to 'open' so beads_ready sees
// them again. Tested directly without the grace-window timer so the test is
// fast and deterministic.
func TestAbandonStaleActiveAssignments_ResetsOpenBeads(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// Seed: one in_progress bead with stale assignment, one open bead with
	// stale assignment, one bead whose worker IS connected (must NOT be
	// touched).
	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		"bead-stale-inprog": {ID: "bead-stale-inprog", Status: "in_progress"},
		"bead-stale-open":   {ID: "bead-stale-open", Status: "open"},
		"bead-live":         {ID: "bead-live", Status: "in_progress"},
	}
	beadSrc.mu.Unlock()

	mustExec(t, d, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?,?,?,?)`,
		"bead-stale-inprog", "dead-worker-1", "/tmp/wt-stale-inprog", "active")
	mustExec(t, d, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?,?,?,?)`,
		"bead-stale-open", "dead-worker-2", "/tmp/wt-stale-open", "active")
	mustExec(t, d, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?,?,?,?)`,
		"bead-live", "live-worker", "/tmp/wt-live", "active")

	// Mark the live worker as connected.
	d.mu.Lock()
	d.workers["live-worker"] = &trackedWorker{id: "live-worker", state: protocol.WorkerBusy}
	d.mu.Unlock()

	abandoned := d.abandonStaleActiveAssignments(ctx)

	if abandoned != 2 {
		t.Errorf("expected 2 abandoned assignments (the two with dead workers), got %d", abandoned)
	}

	// Stale assignments are now status='abandoned'.
	for _, beadID := range []string{"bead-stale-inprog", "bead-stale-open"} {
		var status string
		if err := d.db.QueryRowContext(ctx,
			`SELECT status FROM assignments WHERE bead_id=?`, beadID).Scan(&status); err != nil {
			t.Fatalf("query %s assignment: %v", beadID, err)
		}
		if status != "abandoned" {
			t.Errorf("assignment for %s: expected status='abandoned', got %q", beadID, status)
		}
	}

	// Live assignment is still active.
	var liveStatus string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status FROM assignments WHERE bead_id='bead-live'`).Scan(&liveStatus); err != nil {
		t.Fatalf("query live assignment: %v", err)
	}
	if liveStatus != "active" {
		t.Errorf("live assignment: expected status='active', got %q", liveStatus)
	}

	// in_progress bead with stale assignment should have been Update()d to
	// status='open' so beads_ready picks it up again.
	beadSrc.mu.Lock()
	got := beadSrc.updated["bead-stale-inprog"]
	openOpen := beadSrc.updated["bead-stale-open"]
	live := beadSrc.updated["bead-live"]
	beadSrc.mu.Unlock()
	if got != "open" {
		t.Errorf("bead-stale-inprog: expected Update(status='open') after stale-assignment cleanup, got %q", got)
	}
	// open bead doesn't need a status flip — the fix should not Update it.
	if openOpen != "" {
		t.Errorf("bead-stale-open: expected no Update (already open), got %q", openOpen)
	}
	// Live in_progress bead should be untouched.
	if live != "" {
		t.Errorf("bead-live: expected no Update (live worker still connected), got %q", live)
	}
}

func TestStaleAssignmentSweepRepeatsAfterStartupGrace(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	d.cfg.HeartbeatTimeout = 20 * time.Millisecond

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		"bead-late-open": {ID: "bead-late-open", Status: "open"},
	}
	beadSrc.mu.Unlock()

	cancel := startDispatcher(t, d)
	defer cancel()

	// Let the startup grace and one-shot sweep pass before seeding the stale
	// row. This reproduces a worker that dies later in a long-lived dispatcher.
	time.Sleep(4 * d.cfg.HeartbeatTimeout)

	mustExec(t, d, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?,?,?,?)`,
		"bead-late-open", "dead-worker-late", "/tmp/wt-late-open", "active")

	deadline := time.Now().Add(10 * d.cfg.HeartbeatTimeout)
	for time.Now().Before(deadline) {
		var status string
		if err := d.db.QueryRowContext(ctx,
			`SELECT status FROM assignments WHERE bead_id='bead-late-open'`).Scan(&status); err != nil {
			t.Fatalf("query late assignment: %v", err)
		}
		if status == "abandoned" {
			return
		}
		time.Sleep(d.cfg.HeartbeatTimeout / 2)
	}

	var status string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status FROM assignments WHERE bead_id='bead-late-open'`).Scan(&status); err != nil {
		t.Fatalf("query late assignment after deadline: %v", err)
	}
	t.Fatalf("late stale assignment status = %q, want abandoned by recurring sweep", status)
}

func mustExec(t *testing.T, d *Dispatcher, query string, args ...any) {
	t.Helper()
	if _, err := d.db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
