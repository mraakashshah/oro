package dispatcher //nolint:testpackage // white-box tests need access to Dispatcher internals

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestExternalCloseRecoversWorktree verifies that when a bead is closed
// externally (e.g. a worker calls `oro task close` directly) while still
// holding an active worker assignment, the dispatcher attempts to ff-merge
// the agent branch to main rather than discarding the work.
//
// Background: oro-ohlro lost commit 099cc7a6 because the worker bypassed the
// dispatcher's merge path. The dispatcher detected the external close and
// shut down the worker, but the worktree branch was discarded with the
// uncommitted-from-main work. The fix is to call merger.Merge on external
// close — recovery on success, escalation on conflict — so work is never
// silently lost.
//
// This test uses the default mockGitRunner which returns a fake SHA for
// rev-parse/rev-list and succeeds on rebase, so merger.Merge succeeds. The
// dispatcher should log `external_close_recovered` with the SHA in payload.
func TestExternalCloseRecoversWorktree(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-recover"
	workerID := "w-recover"
	worktree := "/tmp/worktree-" + beadID

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

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		assignmentID: assignmentID,
		beadID:       beadID,
		state:        protocol.WorkerBusy,
		worktree:     worktree,
		targetBranch: "main",
		encoder:      json.NewEncoder(conn),
	}
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		Title:              beadID,
		Status:             "closed",
		AcceptanceCriteria: "done",
	}
	beadSrc.mu.Unlock()

	d.checkClosedBeadAssignments(ctx)

	waitFor(t, func() bool {
		return eventCount(t, d.db, "external_close_recovered") > 0
	}, 2*time.Second)
	waitFor(t, func() bool {
		wtMgr.mu.Lock()
		defer wtMgr.mu.Unlock()
		return len(wtMgr.removed) == 1 && wtMgr.removed[0] == worktree
	}, 2*time.Second)

	// Sanity: cleanup still ran so we don't leak the worktree.
	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 1 || removed[0] != worktree {
		t.Fatalf("expected worktree cleanup for %q, got removed=%v", worktree, removed)
	}
}

// TestExternalCloseEscalatesOnMergeConflict verifies that when the agent
// branch can't be ff-merged (rebase conflict), the dispatcher escalates with
// the branch SHA so the manager can recover the work manually instead of
// silently dropping it.
func TestExternalCloseEscalatesOnMergeConflict(t *testing.T) {
	d, beadSrc, wtMgr, esc, gitRunner, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// Force the rebase step inside merger.Merge to conflict.
	gitRunner.conflict = true

	beadID := "bead-conflict"
	workerID := "w-conflict"
	worktree := "/tmp/worktree-" + beadID

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

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		assignmentID: assignmentID,
		beadID:       beadID,
		state:        protocol.WorkerBusy,
		worktree:     worktree,
		targetBranch: "main",
		encoder:      json.NewEncoder(conn),
	}
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		Title:              beadID,
		Status:             "closed",
		AcceptanceCriteria: "done",
	}
	beadSrc.mu.Unlock()

	d.checkClosedBeadAssignments(ctx)

	waitFor(t, func() bool {
		return eventCount(t, d.db, "external_close_recovery_failed") > 0 && len(esc.Messages()) > 0
	}, 2*time.Second)

	if len(esc.Messages()) == 0 {
		t.Fatal("expected escalation on external_close merge conflict, got none")
	}

	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 0 {
		t.Fatalf("external close recovery failed; worktree should be preserved, removed=%v", removed)
	}
	waitFor(t, func() bool {
		return eventCount(t, d.db, "recovery_work_quarantined") > 0
	}, 2*time.Second)
}
