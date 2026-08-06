package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestAssignmentReassignmentLeavesSingleActiveRow(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	firstID, err := d.createAssignment(ctx, "bead-reassign", "w1", "/tmp/w1")
	if err != nil {
		t.Fatalf("create first assignment: %v", err)
	}

	if _, err := d.createAssignment(ctx, "bead-reassign", "w2", "/tmp/w2"); err == nil {
		t.Fatal("expected second active assignment for same bead to fail")
	}

	if err := d.completeAssignment(ctx, firstID, "bead-reassign"); err != nil {
		t.Fatalf("complete first assignment: %v", err)
	}

	secondID, err := d.createAssignment(ctx, "bead-reassign", "w2", "/tmp/w2")
	if err != nil {
		t.Fatalf("create second assignment after completion: %v", err)
	}

	var activeCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`,
		"bead-reassign",
	).Scan(&activeCount); err != nil {
		t.Fatalf("count active assignments: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("expected exactly 1 active assignment, got %d", activeCount)
	}

	var activeID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM assignments WHERE bead_id=? AND status='active'`,
		"bead-reassign",
	).Scan(&activeID); err != nil {
		t.Fatalf("query active assignment id: %v", err)
	}
	if activeID != secondID {
		t.Fatalf("active assignment id: got %d, want %d", activeID, secondID)
	}
}

func TestReleaseWorkerAfterDoneReservesUntilTerminalCleanup(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	const (
		beadID   = "oro-approved"
		workerID = "worker-approved"
		worktree = "/tmp/oro-approved"
	)

	assignmentID, err := d.createAssignment(ctx, beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     worktree,
	}
	release := d.releaseWorkerAfterDoneLocked(workerID, beadID)
	w := d.workers[workerID]
	reservedState := w.state
	reservedBead := w.beadID
	reservedAssignment := w.assignmentID
	d.mu.Unlock()

	if !release.ok {
		t.Fatal("releaseWorkerAfterDoneLocked returned ok=false")
	}
	if reservedState != protocol.WorkerReserved {
		t.Fatalf("worker state after DONE = %s, want %s", reservedState, protocol.WorkerReserved)
	}
	if reservedBead != beadID || reservedAssignment != assignmentID {
		t.Fatalf("worker assignment cleared before terminal cleanup: bead=%q assignment=%d", reservedBead, reservedAssignment)
	}

	var activeBefore int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE worker_id=? AND status='active'`,
		workerID,
	).Scan(&activeBefore); err != nil {
		t.Fatalf("count active assignments before terminal cleanup: %v", err)
	}
	if activeBefore != 1 {
		t.Fatalf("active assignments before terminal cleanup = %d, want 1", activeBefore)
	}

	if err := d.completeAssignment(ctx, assignmentID, beadID); err != nil {
		t.Fatalf("complete assignment: %v", err)
	}
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, assignmentID)

	d.mu.Lock()
	w = d.workers[workerID]
	finalState := w.state
	finalBead := w.beadID
	finalAssignment := w.assignmentID
	d.mu.Unlock()

	if finalState != protocol.WorkerIdle {
		t.Fatalf("worker state after terminal cleanup = %s, want %s", finalState, protocol.WorkerIdle)
	}
	if finalBead != "" || finalAssignment != 0 {
		t.Fatalf("worker assignment after terminal cleanup: bead=%q assignment=%d, want cleared", finalBead, finalAssignment)
	}

	var activeAfter int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE worker_id=? AND status='active'`,
		workerID,
	).Scan(&activeAfter); err != nil {
		t.Fatalf("count active assignments after terminal cleanup: %v", err)
	}
	if activeAfter != 0 {
		t.Fatalf("active assignments after terminal cleanup = %d, want 0", activeAfter)
	}
}

func TestCompleteAssignmentTargetsSpecificAttempt(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	firstID, err := d.createAssignment(ctx, "bead-a", "w1", "/tmp/a1")
	if err != nil {
		t.Fatalf("create first assignment: %v", err)
	}
	if err := d.completeAssignment(ctx, firstID, "bead-a"); err != nil {
		t.Fatalf("complete first assignment: %v", err)
	}

	secondID, err := d.createAssignment(ctx, "bead-a", "w2", "/tmp/a2")
	if err != nil {
		t.Fatalf("create second assignment: %v", err)
	}

	if err := d.completeAssignment(ctx, firstID, "bead-a"); err != nil {
		t.Fatalf("re-complete first assignment by id: %v", err)
	}

	var firstStatus, secondStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, firstID).Scan(&firstStatus); err != nil {
		t.Fatalf("query first status: %v", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, secondID).Scan(&secondStatus); err != nil {
		t.Fatalf("query second status: %v", err)
	}
	if firstStatus != "completed" {
		t.Fatalf("first assignment status: got %q, want completed", firstStatus)
	}
	if secondStatus != "active" {
		t.Fatalf("second assignment status: got %q, want active", secondStatus)
	}
}

func TestCompleteAssignmentRetriesTransientSQLiteBusy(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/dispatcher.sqlite"

	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("init bead schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=1`); err != nil {
		t.Fatalf("set dispatcher busy timeout: %v", err)
	}

	d := &Dispatcher{db: db}
	assignmentID, err := d.createAssignment(ctx, "bead-busy-complete", "worker-busy", "/tmp/busy")
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	lockDB, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open lock db: %v", err)
	}
	t.Cleanup(func() { _ = lockDB.Close() })
	if _, err := lockDB.ExecContext(ctx, `PRAGMA busy_timeout=1`); err != nil {
		t.Fatalf("set lock busy timeout: %v", err)
	}

	lockConn, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatalf("lock conn: %v", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("begin immediate lock: %v", err)
	}

	releaseLock := make(chan struct{})
	lockReleased := make(chan struct{})
	go func() {
		defer close(lockReleased)
		<-releaseLock
		_, _ = lockConn.ExecContext(context.Background(), `COMMIT`)
	}()
	time.AfterFunc(25*time.Millisecond, func() { close(releaseLock) })

	if err := d.completeAssignment(ctx, assignmentID, "bead-busy-complete"); err != nil {
		t.Fatalf("complete assignment should retry through transient SQLite busy: %v", err)
	}
	<-lockReleased

	var status string
	var completedAt sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT status, completed_at FROM assignments WHERE id=?`,
		assignmentID,
	).Scan(&status, &completedAt); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "completed" {
		t.Fatalf("status = %q, want completed", status)
	}
	if !completedAt.Valid || completedAt.String == "" {
		t.Fatal("completed_at was not set")
	}
}

func TestPersistBeadCountTargetsSpecificAttempt(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	firstID, err := d.createAssignment(ctx, "bead-b", "w1", "/tmp/b1")
	if err != nil {
		t.Fatalf("create first assignment: %v", err)
	}
	if err := d.completeAssignment(ctx, firstID, "bead-b"); err != nil {
		t.Fatalf("complete first assignment: %v", err)
	}

	secondID, err := d.createAssignment(ctx, "bead-b", "w2", "/tmp/b2")
	if err != nil {
		t.Fatalf("create second assignment: %v", err)
	}

	d.persistBeadCount(ctx, secondID, "bead-b", "attempt_count", 7)
	d.persistBeadCount(ctx, firstID, "bead-b", "attempt_count", 3)

	var firstCount, secondCount int
	if err := d.db.QueryRowContext(ctx, `SELECT attempt_count FROM assignments WHERE id=?`, firstID).Scan(&firstCount); err != nil {
		t.Fatalf("query first attempt_count: %v", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT attempt_count FROM assignments WHERE id=?`, secondID).Scan(&secondCount); err != nil {
		t.Fatalf("query second attempt_count: %v", err)
	}
	if firstCount != 3 {
		t.Fatalf("first attempt_count: got %d, want 3", firstCount)
	}
	if secondCount != 7 {
		t.Fatalf("second attempt_count: got %d, want 7", secondCount)
	}
}
