package dispatcher //nolint:testpackage // white-box tests for checkPreMergeQG lifecycle

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// TestPreMergeQGFailureClassifiedBeforeReopen verifies that handlePreMergeQGFailure
// classifies QG output before deciding how to record and reopen:
//  1. Deterministic failure: records a qg_failure_occurrence and reopens the original bead.
//  2. Systemic failure: creates or reuses an infra incident (qg_infra_incident_reused event)
//     without creating a direct P0 child of the original bead.
//  3. Closed bead: classification still runs but the bead is not reopened.
func TestPreMergeQGFailureClassifiedBeforeReopen(t *testing.T) {
	t.Run("deterministic records occurrence and reopens bead", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		const (
			beadID   = "bead-prm-det"
			workerID = "w-prm-det"
			worktree = "/tmp/wt-prm-det"
			qgOutput = "golangci-lint: error in pkg/bar/baz.go: unused variable"
		)
		beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}

		res, err := d.db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
			beadID, workerID, worktree)
		if err != nil {
			t.Fatalf("insert assignment: %v", err)
		}
		assignmentID, _ := res.LastInsertId()
		d.mu.Lock()
		d.worktreeByBead[beadID] = worktree
		d.mu.Unlock()

		got := d.handlePreMergeQGFailure(ctx, beadID, workerID, worktree, assignmentID, qgOutput)

		if got {
			t.Error("handlePreMergeQGFailure returned true, want false (QG failed)")
		}

		// occurrence recorded in DB
		var occurrences int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_occurrences WHERE bead_id=?`, beadID).Scan(&occurrences); err != nil {
			t.Fatalf("count qg_failure_occurrences: %v", err)
		}
		if occurrences != 1 {
			t.Errorf("qg_failure_occurrences count = %d, want 1", occurrences)
		}

		// bead reopened to "open"
		beadSrc.mu.Lock()
		status, wasUpdated := beadSrc.updated[beadID]
		beadSrc.mu.Unlock()
		if !wasUpdated || status != "open" {
			t.Errorf("bead status after deterministic pre-merge QG fail = %q (updated=%v), want \"open\"", status, wasUpdated)
		}

		// no direct P0 child bead created
		beadSrc.mu.Lock()
		created := beadSrc.created
		beadSrc.mu.Unlock()
		for _, c := range created {
			if c.parent == beadID {
				t.Errorf("unexpected direct P0 child of %s: %+v", beadID, c)
			}
		}
	})

	t.Run("systemic reuses infra incident without direct P0 child", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		const (
			beadID   = "bead-prm-sys"
			workerID = "w-prm-sys"
			worktree = "/tmp/wt-prm-sys"
			qgOutput = "out of memory: killed by OOM killer"
		)
		beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}

		res, err := d.db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
			beadID, workerID, worktree)
		if err != nil {
			t.Fatalf("insert assignment: %v", err)
		}
		assignmentID, _ := res.LastInsertId()
		d.mu.Lock()
		d.worktreeByBead[beadID] = worktree
		d.mu.Unlock()

		got := d.handlePreMergeQGFailure(ctx, beadID, workerID, worktree, assignmentID, qgOutput)

		if got {
			t.Error("handlePreMergeQGFailure returned true, want false (QG failed)")
		}

		// infra incident recorded
		var incidents int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_incidents`).Scan(&incidents); err != nil {
			t.Fatalf("count qg_failure_incidents: %v", err)
		}
		if incidents == 0 {
			t.Error("no qg_failure_incidents row created for systemic pre-merge QG failure")
		}

		// qg_infra_incident_reused event logged
		var eventCount int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM events WHERE type='qg_infra_incident_reused' AND bead_id=?`, beadID).Scan(&eventCount); err != nil {
			t.Fatalf("count qg_infra_incident_reused events: %v", err)
		}
		if eventCount == 0 {
			t.Error("qg_infra_incident_reused event not logged for systemic pre-merge QG failure")
		}

		// no direct P0 child of the original bead
		beadSrc.mu.Lock()
		created := beadSrc.created
		beadSrc.mu.Unlock()
		for _, c := range created {
			if c.parent == beadID {
				t.Errorf("unexpected direct P0 child of %s created: title=%q", beadID, c.title)
			}
		}
	})

	t.Run("closed bead not reopened", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		const (
			beadID   = "bead-prm-closed"
			workerID = "w-prm-closed"
			worktree = "/tmp/wt-prm-closed"
			qgOutput = "golangci-lint: error in closed bead pkg/foo"
		)
		beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "closed"}

		res, err := d.db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
			beadID, workerID, worktree)
		if err != nil {
			t.Fatalf("insert assignment: %v", err)
		}
		assignmentID, _ := res.LastInsertId()
		d.mu.Lock()
		d.worktreeByBead[beadID] = worktree
		d.mu.Unlock()

		d.handlePreMergeQGFailure(ctx, beadID, workerID, worktree, assignmentID, qgOutput)

		beadSrc.mu.Lock()
		updatedStatus, wasUpdated := beadSrc.updated[beadID]
		beadSrc.mu.Unlock()
		if wasUpdated {
			t.Errorf("closed bead was reopened to %q, want no status update", updatedStatus)
		}
	})
}

// TestPreMergeQGFailureDoesNotOrphan verifies that when the pre-merge mutation
// QG fails the dispatcher:
//  1. Records a "qg_failed" event with the actionable QG output.
//  2. Requeues the bead to "open" (visible retryable state).
//  3. Marks the assignment requeued so no active bead is left with an idle
//     worker, while the failed work remains resumable.
func TestPreMergeQGFailureDoesNotOrphan(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "bead-qg-orphan"
	const workerID = "w-qg-orphan"
	const worktree = "/tmp/worktree-qg-orphan"
	const qgOutput = "mutation testing failed: 3 surviving mutants in pkg/foo"

	// Insert an active assignment so we can verify it is completed.
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

	d.qgRunner = &mockQGRunner{passed: false, output: qgOutput}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	d.checkPreMergeQG(ctx, beadID, workerID, worktree, assignmentID)

	// (1) Event "qg_failed" logged with actionable output.
	var payload string
	err = d.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='qg_failed' AND bead_id=?`, beadID).Scan(&payload)
	if err != nil {
		t.Fatalf("expected 'qg_failed' event to be logged, got: %v", err)
	}
	if !strings.Contains(payload, qgOutput) {
		t.Errorf("qg_failed event payload = %q, want to contain QG output %q", payload, qgOutput)
	}

	// (2) Bead requeued to visible retryable state.
	beadSrc.mu.Lock()
	status, hasUpdate := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if !hasUpdate || status != "open" {
		t.Errorf("bead status after QG fail = %q (updated=%v), want \"open\"", status, hasUpdate)
	}

	// (3) Assignment requeued — no active in-progress assignment left.
	var assignStatus string
	err = d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignStatus)
	if err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignStatus != "requeued" {
		t.Errorf("assignment status = %q after QG fail, want \"requeued\" (orphan guard)", assignStatus)
	}
}

// TestClosedBeadNotReopenedByStaleAssignment verifies that when a bead is
// closed on main (e.g. merged externally) after its assignment was created but
// before the pre-merge QG result arrives, the dispatcher does NOT reopen the
// bead to "open". Cleanup (assignment completion, worktree removal) still runs.
func TestClosedBeadNotReopenedByStaleAssignment(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "bead-closed-stale"
	const workerID = "w-closed-stale"
	const worktree = "/tmp/worktree-closed-stale"

	// Insert an active assignment (created before bead was closed externally).
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

	// Bead has since been closed on main (before QG failure arrives).
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "closed"}
	beadSrc.mu.Unlock()

	d.qgRunner = &mockQGRunner{passed: false, output: "mutation testing failed"}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	d.checkPreMergeQG(ctx, beadID, workerID, worktree, assignmentID)

	// Bead must NOT be reopened — it was already closed on main.
	beadSrc.mu.Lock()
	updatedStatus, wasUpdated := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if wasUpdated {
		t.Errorf("closed bead was reopened to %q via stale QG failure, want no update", updatedStatus)
	}

	// Cleanup still ran: assignment must be completed (no orphan).
	var assignStatus string
	err = d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignStatus)
	if err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignStatus != "completed" {
		t.Errorf("assignment status = %q after closed-bead QG fail, want \"completed\"", assignStatus)
	}

	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 0 {
		t.Fatalf("closed-bead QG failure has no merge proof; worktree should be preserved, removed=%v", removed)
	}
	if eventCount(t, d.db, "pre_merge_qg_work_preserved") == 0 {
		t.Fatalf("expected pre_merge_qg_work_preserved event for closed-bead QG failure")
	}
}

// TestPreMergeQGErrorClassifiedBeforePreservation verifies that when checkPreMergeQG
// encounters a qgErr (infrastructure failure, not just a test failure), the dispatcher
// records a classified occurrence before logging that it preserved the work.
func TestPreMergeQGErrorClassifiedBeforePreservation(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "bead-qg-classify"
	const workerID = "w-qg-classify"
	const worktree = "/tmp/worktree-qg-classify"

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

	// Simulate a systemic infrastructure error: quality_gate.sh not found.
	d.qgRunner = &mockQGRunner{err: fmt.Errorf("run quality gate: %w", os.ErrNotExist)}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	d.checkPreMergeQG(ctx, beadID, workerID, worktree, assignmentID)

	// (1) Classification event must be recorded.
	var payload string
	err = d.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='pre_merge_qg_error_classified' AND bead_id=?`,
		beadID).Scan(&payload)
	if err != nil {
		t.Fatalf("expected 'pre_merge_qg_error_classified' event: %v", err)
	}
	if !strings.Contains(payload, `"class"`) {
		t.Errorf("classification payload = %q, want 'class' field", payload)
	}
	// Missing QG script is a systemic incident.
	if !strings.Contains(payload, `"systemic"`) {
		t.Errorf("classification payload = %q, want class='systemic' for missing QG script", payload)
	}

	// (2) Classification must have been recorded before preservation.
	var classID, preserveID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM events WHERE type='pre_merge_qg_error_classified' AND bead_id=?`,
		beadID).Scan(&classID); err != nil {
		t.Fatalf("query classification event id: %v", err)
	}
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM events WHERE type='pre_merge_qg_work_preserved' AND bead_id=?`,
		beadID).Scan(&preserveID); err != nil {
		t.Fatalf("query preservation event id: %v", err)
	}
	if classID >= preserveID {
		t.Errorf("classification event id %d must precede preservation event id %d", classID, preserveID)
	}
}

// TestPreMergeQGFailurePreservesRejectedWork verifies that a systemic pre-merge QG
// error (e.g. missing quality_gate.sh) preserves the agent branch for discovery rather
// than deleting it, and logs a preservation event before cleanup.
func TestPreMergeQGFailurePreservesRejectedWork(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const beadID = "bead-qg-preserve"
	const workerID = "w-qg-preserve"
	const worktree = "/tmp/worktree-qg-preserve"

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

	// Systemic error: missing quality_gate.sh.
	d.qgRunner = &mockQGRunner{err: fmt.Errorf("run quality gate: %w", os.ErrNotExist)}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	d.checkPreMergeQG(ctx, beadID, workerID, worktree, assignmentID)

	// (1) Preservation event must be logged.
	var preservePayload string
	err = d.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='pre_merge_qg_work_preserved' AND bead_id=?`,
		beadID).Scan(&preservePayload)
	if err != nil {
		t.Fatalf("expected 'pre_merge_qg_work_preserved' event: %v", err)
	}
	branch := protocol.BranchPrefix + beadID
	if !strings.Contains(preservePayload, branch) {
		t.Errorf("preservation payload = %q, want branch reference %q", preservePayload, branch)
	}

	// (2) Agent branch must NOT be deleted — leave it discoverable.
	wtMgr.mu.Lock()
	deletedBranches := append([]string(nil), wtMgr.deletedBranches...)
	wtMgr.mu.Unlock()
	for _, b := range deletedBranches {
		if b == branch {
			t.Errorf("branch %q was deleted for systemic error; want preserved for discovery", branch)
		}
	}

	wtMgr.mu.Lock()
	removedWorktrees := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	for _, removed := range removedWorktrees {
		if removed == worktree {
			t.Errorf("worktree %q was removed for systemic error; want preserved for retry/inspection", worktree)
		}
	}

	var status, storedWorktree string
	if err := d.db.QueryRowContext(ctx, `SELECT status, worktree FROM assignments WHERE id=?`, assignmentID).Scan(&status, &storedWorktree); err != nil {
		t.Fatalf("query assignment after QG error: %v", err)
	}
	if status != "requeued" || storedWorktree != worktree {
		t.Fatalf("assignment status/worktree = %s/%s, want requeued/%s", status, storedWorktree, worktree)
	}
}
