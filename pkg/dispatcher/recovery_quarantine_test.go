package dispatcher //nolint:testpackage // white-box tests for recovery quarantine lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/factoryhealth"
	"oro/pkg/protocol"
)

func TestRecoveryQuarantineEmptySafeMatchesRecoveryCommandSemantics(t *testing.T) {
	tests := []struct {
		name         string
		dirtyFiles   int
		branchExists bool
		branchAhead  int
		want         bool
	}{
		{name: "no branch or dirty files", want: true},
		{name: "clean branch without unique commits", branchExists: true, want: true},
		{name: "dirty worktree", dirtyFiles: 1, want: false},
		{name: "branch with unique commits", branchExists: true, branchAhead: 1, want: false},
		{name: "missing branch ignores stale ahead count", branchAhead: 1, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RecoveryQuarantineEmptySafe(tt.dirtyFiles, tt.branchExists, tt.branchAhead); got != tt.want {
				t.Fatalf("RecoveryQuarantineEmptySafe(%d, %t, %d) = %t, want %t",
					tt.dirtyFiles, tt.branchExists, tt.branchAhead, got, tt.want)
			}
		})
	}
}

func TestResolvedPreservedAssignmentSuppressesStaleBranchRecovery(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	res, err := d.db.ExecContext(ctx, `INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-preserved-stale', 'worker', '/tmp/preserved-stale', 'requeued')`)
	if err != nil {
		t.Fatalf("insert requeued assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO recovery_quarantines (bead_id, assignment_id, reason, details, status, resolved_at) VALUES ('oro-preserved-stale', ?, 'unsafe_stale_branch', 'operator preserved and requeued dirty worktree', 'resolved', datetime('now'))`, assignmentID); err != nil {
		t.Fatalf("insert resolved stale quarantine: %v", err)
	}

	if !d.resolvedPreservedMismatchAssignment(ctx, assignmentID) {
		t.Fatal("resolved requeue-preserved stale assignment must be suppressed on restart")
	}
}

func TestCreateRecoveryQuarantineIdempotent(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-q', 'w1', '/tmp/wt-q', 'active')`)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}

	first, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       "oro-q",
		AssignmentID: assignmentID,
		WorkerID:     "w1",
		Worktree:     "/tmp/wt-q",
		Branch:       "agent/oro-q",
		Reason:       "missing_worktree_path",
		Details:      "first observation",
	})
	if err != nil {
		t.Fatalf("create first quarantine: %v", err)
	}
	second, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       "oro-q",
		AssignmentID: assignmentID,
		WorkerID:     "w1",
		Worktree:     "/tmp/wt-q",
		Branch:       "agent/oro-q",
		Reason:       "missing_worktree_path",
		Details:      "second observation",
	})
	if err != nil {
		t.Fatalf("create second quarantine: %v", err)
	}
	if first != second {
		t.Fatalf("quarantine IDs = %d and %d, want idempotent same row", first, second)
	}

	var openCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id='oro-q' AND reason='missing_worktree_path' AND status='open'`,
	).Scan(&openCount); err != nil {
		t.Fatalf("count open quarantines: %v", err)
	}
	if openCount != 1 {
		t.Fatalf("open quarantine count = %d, want 1", openCount)
	}

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignmentStatus != "quarantined" {
		t.Fatalf("assignment status = %q, want quarantined", assignmentStatus)
	}
}

func TestCreateRecoveryQuarantineCoalescesOpenRowsByAssignment(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-q', 'w1', '/tmp/wt-q', 'active')`)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}

	first, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       "oro-q",
		AssignmentID: assignmentID,
		WorkerID:     "w1",
		Worktree:     "/tmp/wt-q",
		Branch:       "agent/oro-q",
		Reason:       "stale_active_assignment",
		Details:      "startup saw a disconnected active assignment",
	})
	if err != nil {
		t.Fatalf("create stale active quarantine: %v", err)
	}
	second, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       "oro-q",
		AssignmentID: assignmentID,
		WorkerID:     "w2",
		Worktree:     "/tmp/wt-q2",
		Branch:       "agent/oro-q",
		Reason:       "progress_timeout_recovery_blocked",
		Details:      "latest recovery inspection details",
	})
	if err != nil {
		t.Fatalf("create progress timeout quarantine: %v", err)
	}
	if first != second {
		t.Fatalf("quarantine IDs = %d and %d, want same open row for assignment", first, second)
	}

	var openCount int
	var reason, details, workerID, worktree string
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MAX(reason), ''), COALESCE(MAX(details), ''), COALESCE(MAX(worker_id), ''), COALESCE(MAX(worktree), '')
FROM recovery_quarantines
WHERE assignment_id=? AND status='open'`, assignmentID).Scan(&openCount, &reason, &details, &workerID, &worktree); err != nil {
		t.Fatalf("query open quarantines: %v", err)
	}
	if openCount != 1 {
		t.Fatalf("open quarantines for assignment = %d, want 1", openCount)
	}
	if reason != "progress_timeout_recovery_blocked" || details != "latest recovery inspection details" {
		t.Fatalf("latest quarantine reason/details = %q/%q, want progress timeout latest details", reason, details)
	}
	if workerID != "w2" || worktree != "/tmp/wt-q2" {
		t.Fatalf("latest quarantine worker/worktree = %q/%q, want w2 /tmp/wt-q2", workerID, worktree)
	}

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignmentStatus != "quarantined" {
		t.Fatalf("assignment status = %q, want quarantined", assignmentStatus)
	}

	if err := d.resolveRecoveryQuarantine(ctx, first); err != nil {
		t.Fatalf("resolve recovery quarantine: %v", err)
	}
	reopened, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       "oro-q",
		AssignmentID: assignmentID,
		WorkerID:     "w3",
		Worktree:     "/tmp/wt-q3",
		Branch:       "agent/oro-q",
		Reason:       "stale_active_assignment",
		Details:      "new unresolved observation",
	})
	if err != nil {
		t.Fatalf("create quarantine after resolved row: %v", err)
	}
	if reopened == first {
		t.Fatalf("reopened quarantine id = %d, want new row after resolved id %d", reopened, first)
	}
}

func TestCompleteAssignmentDoesNotHideQuarantinedAssignment(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-q', 'w1', '/tmp/wt-q', 'active')`)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}

	if _, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       "oro-q",
		AssignmentID: assignmentID,
		WorkerID:     "w1",
		Worktree:     "/tmp/wt-q",
		Branch:       "agent/oro-q",
		Reason:       "stale_active_assignment",
		Details:      "active assignment belongs to a disconnected worker",
	}); err != nil {
		t.Fatalf("create recovery quarantine: %v", err)
	}
	if err := d.completeAssignment(ctx, assignmentID, "oro-q"); err != nil {
		t.Fatalf("complete quarantined assignment: %v", err)
	}

	var status string
	var completedAt any
	if err := d.db.QueryRowContext(ctx,
		`SELECT status, completed_at FROM assignments WHERE id=?`, assignmentID).Scan(&status, &completedAt); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "quarantined" {
		t.Fatalf("assignment status = %q, want quarantined", status)
	}
	if completedAt != nil {
		t.Fatalf("completed_at = %v, want NULL", completedAt)
	}
}

func TestSchemaApplyAddsRecoveryQuarantinesTable(t *testing.T) {
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE events (
    id INTEGER PRIMARY KEY,
    type TEXT NOT NULL,
    source TEXT NOT NULL
);
`); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO recovery_quarantines (bead_id, reason, details, status) VALUES ('oro-schema', 'unsafe_stale_branch', 'ok', 'open')`); err != nil {
		t.Fatalf("insert recovery quarantine after schema apply: %v", err)
	}
}

func TestListAndResolveRecoveryQuarantine(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	id, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:  "oro-resolve",
		Branch:  "agent/oro-resolve",
		Reason:  "unsafe_stale_branch",
		Details: "operator inspected branch",
	})
	if err != nil {
		t.Fatalf("create recovery quarantine: %v", err)
	}

	open, err := d.listOpenRecoveryQuarantines(ctx)
	if err != nil {
		t.Fatalf("list open recovery quarantines: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open quarantines = %+v, want one", open)
	}
	if open[0].ID != id || open[0].BeadID != "oro-resolve" || open[0].Reason != "unsafe_stale_branch" {
		t.Fatalf("open quarantine = %+v, want id/bead/reason populated", open[0])
	}

	if err := d.resolveRecoveryQuarantine(ctx, id); err != nil {
		t.Fatalf("resolve recovery quarantine: %v", err)
	}
	if err := d.resolveRecoveryQuarantine(ctx, id); err != nil {
		t.Fatalf("resolve recovery quarantine should be idempotent: %v", err)
	}

	open, err = d.listOpenRecoveryQuarantines(ctx)
	if err != nil {
		t.Fatalf("list after resolve: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open quarantines after resolve = %+v, want none", open)
	}

	nextID, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:  "oro-resolve",
		Branch:  "agent/oro-resolve",
		Reason:  "unsafe_stale_branch",
		Details: "regressed after resolution",
	})
	if err != nil {
		t.Fatalf("recreate recovery quarantine after resolve: %v", err)
	}
	if nextID == id {
		t.Fatalf("recreated open quarantine id = %d, want new row after resolved id %d", nextID, id)
	}
}

func TestRestoreStateQuarantinesMissingWorktreeDurably(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	beadSrc.inProgressBeads = []protocol.Bead{{ID: "oro-bad"}}
	d.shutdownRunner = &mockCommandRunner{}
	wtMgr.existsFn = func(_ context.Context, _ string) bool { return false }
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-bad", nil
	}

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-bad', 'w1', '/tmp/missing', 'active')`); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	cancel := startDispatcher(t, d)
	defer cancel()

	beadSrc.mu.Lock()
	updated := beadSrc.updated
	beadSrc.mu.Unlock()
	if _, ok := updated["oro-bad"]; ok {
		t.Fatalf("expected quarantined bead to remain untouched, got updates=%v", updated)
	}

	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE bead_id='oro-bad'`).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "quarantined" {
		t.Fatalf("quarantined assignment status = %q, want quarantined", status)
	}

	var reason string
	if err := d.db.QueryRowContext(ctx,
		`SELECT reason FROM recovery_quarantines WHERE bead_id='oro-bad' AND status='open'`,
	).Scan(&reason); err != nil {
		t.Fatalf("query recovery quarantine: %v", err)
	}
	if reason != "missing_worktree_path" {
		t.Fatalf("quarantine reason = %q, want missing_worktree_path", reason)
	}
}

func TestStartupRecoveryQuarantinesUnsafeRequeuedAssignmentAndReportsHealth(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-requeued-recovery"
		workerID = "worker-requeued-recovery"
		worktree = "/tmp/missing-oro-requeued-recovery"
		branch   = protocol.BranchPrefix + beadID
	)

	wtMgr.existsFn = func(_ context.Context, path string) bool { return path != worktree }
	wtMgr.branchExistsFn = func(_ context.Context, got string) (bool, error) { return got == branch, nil }

	res, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status, completed_at)
VALUES (?, ?, ?, 'requeued', datetime('now'))`, beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert requeued assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}

	if err := d.startupRecovery(ctx); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	var openQuarantines int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM recovery_quarantines
WHERE assignment_id=? AND bead_id=? AND reason='missing_worktree_path' AND status='open'`,
		assignmentID, beadID).Scan(&openQuarantines); err != nil {
		t.Fatalf("count open recovery quarantines: %v", err)
	}
	if openQuarantines != 1 {
		t.Fatalf("open recovery quarantines = %d, want 1", openQuarantines)
	}

	var assignmentStatus string
	var completedAt any
	if err := d.db.QueryRowContext(ctx,
		`SELECT status, completed_at FROM assignments WHERE id=?`, assignmentID,
	).Scan(&assignmentStatus, &completedAt); err != nil {
		t.Fatalf("query recovered assignment: %v", err)
	}
	if assignmentStatus != "quarantined" {
		t.Fatalf("assignment status = %q, want quarantined", assignmentStatus)
	}
	if completedAt != nil {
		t.Fatalf("assignment completed_at = %v, want NULL", completedAt)
	}

	var quarantinedEvents, failedEvents int
	if err := d.db.QueryRowContext(ctx, `
SELECT
    SUM(CASE WHEN type='startup_recovery_quarantined' THEN 1 ELSE 0 END),
    SUM(CASE WHEN type='startup_recovery_quarantine_failed' THEN 1 ELSE 0 END)
FROM events
WHERE bead_id=?`, beadID).Scan(&quarantinedEvents, &failedEvents); err != nil {
		t.Fatalf("count startup recovery events: %v", err)
	}
	if quarantinedEvents != 1 || failedEvents != 0 {
		t.Fatalf("startup recovery events = quarantined %d failed %d, want 1/0", quarantinedEvents, failedEvents)
	}

	if redeployable, blocked := d.recoveryQuarantineAssignmentScope(ctx); !blocked || len(redeployable) != 0 {
		t.Fatalf("assignment scope = redeployable %+v blocked %t, want globally blocked", redeployable, blocked)
	}

	healthJSON, err := d.applyHealth()
	if err != nil {
		t.Fatalf("applyHealth: %v", err)
	}
	var health SwarmHealth
	if err := json.Unmarshal([]byte(healthJSON), &health); err != nil {
		t.Fatalf("unmarshal health: %v", err)
	}
	if health.Metrics.OpenRecoveryQuarantines != 1 ||
		health.Metrics.BlockingRecoveryQuarantines != 1 ||
		!health.Metrics.AssignmentFrozenByQuarantine {
		t.Fatalf("recovery quarantine health metrics = %+v, want open=1 blocking=1 frozen=true", health.Metrics)
	}
	if !hasHealthFinding(health, factoryhealth.FindingRecoveryQuarantineOpen) ||
		!hasHealthFinding(health, factoryhealth.FindingAssignmentFrozenByQuarantine) {
		t.Fatalf("health missing recovery quarantine findings: %+v", health.Findings)
	}
}

func TestProcessQuarantinedRollsBackWhenEventInsertFails(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID              = "oro-recovery-event-failure"
		workerID            = "worker-recovery-event-failure"
		worktree            = "/tmp/missing-oro-recovery-event-failure"
		originalCompletedAt = "2026-08-02 20:00:00"
	)
	var successBroadcasts int
	d.sseBroadcaster = &callbackSSEBroadcaster{send: func(eventType, _, _ string) {
		if eventType == "startup_recovery_quarantined" {
			successBroadcasts++
		}
	}}

	res, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status, completed_at)
VALUES (?, ?, ?, 'requeued', ?)`, beadID, workerID, worktree, originalCompletedAt)
	if err != nil {
		t.Fatalf("insert requeued assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `
CREATE TRIGGER reject_recovery_event
BEFORE INSERT ON events
WHEN NEW.type='startup_recovery_quarantined'
BEGIN
	SELECT RAISE(ABORT, 'forced recovery event failure');
END;`); err != nil {
		t.Fatalf("install event failure trigger: %v", err)
	}

	d.processQuarantined(ctx, []quarantinedAssignment{{
		id:       assignmentID,
		beadID:   beadID,
		workerID: workerID,
		worktree: worktree,
		branch:   protocol.BranchPrefix + beadID,
		reason:   "missing_worktree_path",
	}})

	var status string
	var completedAt string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status, completed_at FROM assignments WHERE id=?`, assignmentID,
	).Scan(&status, &completedAt); err != nil {
		t.Fatalf("query assignment after event failure: %v", err)
	}
	if status != "requeued" {
		t.Fatalf("assignment status = %q, want requeued after rollback", status)
	}
	if completedAt != originalCompletedAt {
		t.Fatalf("assignment completed_at = %q, want original %q after rollback", completedAt, originalCompletedAt)
	}

	var quarantines, successEvents, failureEvents int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE assignment_id=?`, assignmentID,
	).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if err := d.db.QueryRowContext(ctx, `
SELECT
	SUM(CASE WHEN type='startup_recovery_quarantined' THEN 1 ELSE 0 END),
	SUM(CASE WHEN type='startup_recovery_quarantine_failed' THEN 1 ELSE 0 END)
FROM events
WHERE bead_id=?`, beadID).Scan(&successEvents, &failureEvents); err != nil {
		t.Fatalf("count recovery events: %v", err)
	}
	if quarantines != 0 || successEvents != 0 || failureEvents != 1 {
		t.Fatalf("recovery state after event failure = quarantines %d success events %d failure events %d, want 0/0/1",
			quarantines, successEvents, failureEvents)
	}
	if successBroadcasts != 0 {
		t.Fatalf("success broadcasts = %d, want 0", successBroadcasts)
	}
}

func TestStartupAutoResolvesEmptySafeQuarantines(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		emptyBeadID       = "oro-startup-empty"
		preservedBeadID   = "oro-startup-preserved"
		dirtyBeadID       = "oro-startup-dirty"
		missingWorktree   = "/tmp/oro-startup-empty-missing"
		preservedWorktree = "/tmp/oro-startup-preserved"
		dirtyWorktree     = "/tmp/oro-startup-dirty"
	)

	wtMgr.existsFn = func(_ context.Context, path string) bool {
		return path == preservedWorktree || path == dirtyWorktree
	}
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch != protocol.BranchPrefix+emptyBeadID, nil
	}
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) >= 4 && args[0] == "-C" && args[2] == "status" && args[3] == "--porcelain" {
			if args[1] == dirtyWorktree {
				return []byte(" M preserved.txt\n"), nil
			}
			return nil, nil
		}
		return nil, nil
	}}

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, 'worker-empty', ?, 'active')`,
		emptyBeadID, missingWorktree); err != nil {
		t.Fatalf("insert empty-safe assignment: %v", err)
	}
	for _, quarantine := range []struct {
		beadID   string
		worktree string
	}{
		{beadID: preservedBeadID, worktree: preservedWorktree},
		{beadID: dirtyBeadID, worktree: dirtyWorktree},
	} {
		if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, worktree, branch, reason, details, status)
VALUES (?, ?, ?, 'startup_fixture', 'preserved recovery state', 'open')`,
			quarantine.beadID, quarantine.worktree, protocol.BranchPrefix+quarantine.beadID); err != nil {
			t.Fatalf("insert %s quarantine: %v", quarantine.beadID, err)
		}
	}

	if err := d.startupRecovery(ctx); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	var emptyStatus string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status FROM recovery_quarantines WHERE bead_id=?`, emptyBeadID).Scan(&emptyStatus); err != nil {
		t.Fatalf("query empty-safe quarantine: %v", err)
	}
	if emptyStatus != "resolved" {
		t.Fatalf("empty-safe quarantine status = %q, want resolved", emptyStatus)
	}

	var payload string
	if err := d.db.QueryRowContext(ctx, `
SELECT payload
FROM events
WHERE type='startup_recovery_quarantine_auto_resolved' AND bead_id=?`, emptyBeadID).Scan(&payload); err != nil {
		t.Fatalf("query empty-safe resolution event: %v", err)
	}
	var event struct {
		Status string `json:"status"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("decode empty-safe resolution event: %v", err)
	}
	if event.Status != "closed" || event.Mode != "discard-empty-safe" {
		t.Fatalf("resolution event = %+v, want status closed and discard-empty-safe mode", event)
	}

	for _, beadID := range []string{preservedBeadID, dirtyBeadID} {
		var status string
		if err := d.db.QueryRowContext(ctx,
			`SELECT status FROM recovery_quarantines WHERE bead_id=?`, beadID).Scan(&status); err != nil {
			t.Fatalf("query preserved quarantine %s: %v", beadID, err)
		}
		if status != "open" {
			t.Fatalf("preserved quarantine %s status = %q, want open", beadID, status)
		}
	}

	openQuarantines, err := factoryhealth.LoadRecoveryQuarantineMetrics(ctx, d.db)
	if err != nil {
		t.Fatalf("load recovery quarantine metrics: %v", err)
	}
	if openQuarantines != 2 {
		t.Fatalf("open recovery quarantines = %d, want 2 preservable rows", openQuarantines)
	}
}

func TestRestoreStateQuarantinesBranchWorktreeMismatch(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	worktree := t.TempDir()

	beadSrc.inProgressBeads = []protocol.Bead{{ID: "oro-mismatch"}}
	d.shutdownRunner = &mockCommandRunner{}
	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-mismatch", nil
	}
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path == worktree {
			return "agent/other", nil
		}
		return "", nil
	}

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-mismatch', 'w1', ?, 'active')`,
		worktree); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	cancel := startDispatcher(t, d)
	defer cancel()

	var status, reason string
	if err := d.db.QueryRowContext(ctx, `
SELECT a.status, q.reason
FROM assignments a
JOIN recovery_quarantines q ON q.assignment_id=a.id
WHERE a.bead_id='oro-mismatch' AND q.status='open'`,
	).Scan(&status, &reason); err != nil {
		t.Fatalf("query recovery state: %v", err)
	}
	if status != "quarantined" {
		t.Fatalf("assignment status = %q, want quarantined", status)
	}
	if reason != "branch_worktree_mismatch" {
		t.Fatalf("quarantine reason = %q, want branch_worktree_mismatch", reason)
	}
}

func TestProcessQuarantinedContinuesAfterRowFailure(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-good', 'w1', '/tmp/good', 'active')`)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	goodID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	d.processQuarantined(ctx, []quarantinedAssignment{
		{id: 999999, beadID: "oro-missing-assignment", workerID: "w0", worktree: "/tmp/missing", branch: "agent/oro-missing-assignment", reason: "missing_worktree_path"},
		{id: goodID, beadID: "oro-good", workerID: "w1", worktree: "/tmp/good", branch: "agent/oro-good", reason: "missing_branch"},
	})

	var count int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id='oro-good' AND status='open'`).Scan(&count); err != nil {
		t.Fatalf("count successful quarantine: %v", err)
	}
	if count != 1 {
		t.Fatalf("successful quarantines = %d, want 1", count)
	}
	if eventCount(t, d.db, "startup_recovery_quarantine_failed") == 0 {
		t.Fatalf("expected startup_recovery_quarantine_failed event for bad row")
	}
}

func TestRestoreStateCompletesClosedMergedAssignmentWithoutQuarantine(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	beadSrc.shown["oro-closed-merged"] = &protocol.BeadDetail{
		ID:          "oro-closed-merged",
		Status:      "closed",
		CloseReason: "Merged: deadbeef",
		ClosedAt:    "2026-05-18T16:03:43Z",
	}
	wtMgr.existsFn = func(_ context.Context, path string) bool {
		if path != "/tmp/missing-closed" {
			t.Fatalf("inspected unexpected worktree %q", path)
		}
		return false
	}

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-closed-merged', 'w1', '/tmp/missing-closed', 'active')`)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}

	recoverable, stats, err := d.restoreState(ctx)
	if err != nil {
		t.Fatalf("restore state: %v", err)
	}
	if len(recoverable) != 0 {
		t.Fatalf("recoverable = %+v, want none", recoverable)
	}
	if stats.recoverable != 0 || stats.quarantined != 0 || stats.retiredClosed != 1 {
		t.Fatalf("stats = %+v, want one retired closed assignment only", stats)
	}

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if assignmentStatus != "completed" {
		t.Fatalf("assignment status = %q, want completed", assignmentStatus)
	}

	var openQuarantines int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE status='open'`).Scan(&openQuarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if openQuarantines != 0 {
		t.Fatalf("open recovery quarantines = %d, want 0", openQuarantines)
	}
}

func TestStartupRecoveryRetiresClosedAssignmentWithEmptyState(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const preservedWorktree = "/tmp/worktree-oro-closed-preserved"

	beadSrc.shown["oro-closed-active"] = &protocol.BeadDetail{
		ID:          "oro-closed-active",
		Status:      "closed",
		CloseReason: "Manually merged into epic/oro-recov-hardening by the coordinator.",
		ClosedAt:    "2026-05-19T12:00:00Z",
	}
	beadSrc.shown["oro-closed-requeued"] = &protocol.BeadDetail{
		ID:          "oro-closed-requeued",
		Status:      "closed",
		CloseReason: "Manually merged into epic/oro-recov-hardening by the coordinator.",
		ClosedAt:    "2026-05-19T12:01:00Z",
	}
	beadSrc.shown["oro-open-missing"] = &protocol.BeadDetail{
		ID:     "oro-open-missing",
		Status: "open",
	}
	beadSrc.shown["oro-closed-preserved"] = &protocol.BeadDetail{
		ID:          "oro-closed-preserved",
		Status:      "closed",
		CloseReason: "Manually merged into epic/oro-recov-hardening by the coordinator.",
		ClosedAt:    "2026-05-19T12:02:00Z",
	}
	wtMgr.existsFn = func(_ context.Context, path string) bool {
		return path == preservedWorktree
	}
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-closed-preserved", nil
	}
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path == preservedWorktree {
			return "agent/oro-closed-preserved", nil
		}
		return "", nil
	}

	for _, assignment := range []struct {
		beadID   string
		workerID string
		worktree string
		status   string
	}{
		{beadID: "oro-closed-active", workerID: "w-closed-active", worktree: "/tmp/missing-closed-active", status: "active"},
		{beadID: "oro-closed-requeued", workerID: "w-closed-requeued", worktree: "", status: "requeued"},
		{beadID: "oro-open-missing", workerID: "w-open-missing", worktree: "/tmp/missing-open", status: "active"},
		{beadID: "oro-closed-preserved", workerID: "w-closed-preserved", worktree: preservedWorktree, status: "active"},
	} {
		if _, err := d.db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, ?)`,
			assignment.beadID, assignment.workerID, assignment.worktree, assignment.status); err != nil {
			t.Fatalf("insert assignment %s: %v", assignment.beadID, err)
		}
	}

	recoverable, stats, err := d.restoreState(ctx)
	if err != nil {
		t.Fatalf("restore state: %v", err)
	}
	if !recoverable["oro-closed-preserved"] || len(recoverable) != 1 {
		t.Fatalf("recoverable = %+v, want only preserved closed assignment", recoverable)
	}
	if stats.recoverable != 1 || stats.quarantined != 1 || stats.retiredClosed != 2 {
		t.Fatalf("stats = %+v, want one recoverable, one quarantined, two retired closed", stats)
	}

	for _, beadID := range []string{"oro-closed-active", "oro-closed-requeued"} {
		var assignmentStatus string
		if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE bead_id=?`, beadID).Scan(&assignmentStatus); err != nil {
			t.Fatalf("query assignment status for %s: %v", beadID, err)
		}
		if assignmentStatus != "completed" {
			t.Fatalf("%s assignment status = %q, want completed", beadID, assignmentStatus)
		}
		var openQuarantines int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID).Scan(&openQuarantines); err != nil {
			t.Fatalf("count closed bead quarantines for %s: %v", beadID, err)
		}
		if openQuarantines != 0 {
			t.Fatalf("%s open recovery quarantines = %d, want 0", beadID, openQuarantines)
		}
	}

	var openReason string
	if err := d.db.QueryRowContext(ctx,
		`SELECT reason FROM recovery_quarantines WHERE bead_id='oro-open-missing' AND status='open'`).Scan(&openReason); err != nil {
		t.Fatalf("query open bead quarantine: %v", err)
	}
	if openReason != "missing_worktree_path" {
		t.Fatalf("open bead quarantine reason = %q, want missing_worktree_path", openReason)
	}

	var preservedStatus string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status FROM assignments WHERE bead_id='oro-closed-preserved'`).Scan(&preservedStatus); err != nil {
		t.Fatalf("query preserved assignment status: %v", err)
	}
	if preservedStatus != "active" {
		t.Fatalf("preserved closed assignment status = %q, want active", preservedStatus)
	}
}

func TestDeleteStaleAgentBranchQuarantinesUnmergedBranch(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "oro-unmerged"

	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/"+beadID, nil
	}
	wtMgr.deleteBranchFn = func(branch string) error {
		return assertSafeDeleteFailure(branch)
	}

	err := d.deleteStaleAgentBranch(ctx, beadID, "w1", d.cfg.DefaultBranch)
	if err == nil {
		t.Fatal("expected unsafe stale branch to block assignment")
	}
	if !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("error = %v, want quarantine context", err)
	}

	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 0 {
		t.Fatalf("unmerged stale branch removed worktrees: %v", removed)
	}

	var openCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=? AND reason='unsafe_stale_branch' AND status='open'`,
		beadID,
	).Scan(&openCount); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if openCount != 1 {
		t.Fatalf("open unsafe_stale_branch quarantines = %d, want 1", openCount)
	}
}

func TestFilterAssignableSkipsBeadWithOpenRecoveryQuarantine(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES ('oro-quarantined', 'agent/oro-quarantined', 'unsafe_stale_branch', 'unmerged branch', 'open');
`); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}

	got := d.filterAssignable(ctx, []protocol.Bead{
		{ID: "oro-quarantined", Status: "open", Priority: 1, Type: "task"},
		{ID: "oro-ready", Status: "open", Priority: 2, Type: "task"},
	})

	if len(got) != 1 || got[0].ID != "oro-ready" {
		t.Fatalf("filterAssignable = %+v, want only oro-ready", got)
	}
	if eventCount(t, d.db, "recovery_quarantined_bead_skipped") == 0 {
		t.Fatalf("expected recovery_quarantined_bead_skipped event")
	}
}

func TestFilterAssignableAllowsBeadAfterRequeuePreservedResolution(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	assignment, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES ('oro-requeued-preserved', 'offline-worker', '/tmp/oro-requeued-preserved', 'requeued')`)
	if err != nil {
		t.Fatalf("insert requeued preserved assignment: %v", err)
	}
	assignmentID, err := assignment.LastInsertId()
	if err != nil {
		t.Fatalf("requeued preserved assignment ID: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, assignment_id, reason, details, status, resolved_at)
VALUES ('oro-requeued-preserved', ?, 'unsafe_stale_branch', 'preserved and requeued', 'resolved', datetime('now'))`, assignmentID); err != nil {
		t.Fatalf("insert resolved recovery quarantine: %v", err)
	}

	got := d.filterAssignable(ctx, []protocol.Bead{
		{ID: "oro-requeued-preserved", Status: "open", Priority: 1, Type: "task"},
		{ID: "oro-ready", Status: "open", Priority: 2, Type: "task"},
	})

	if len(got) != 2 {
		t.Fatalf("filterAssignable = %+v, want both ready beads after requeue-preserved resolution", got)
	}
}

func TestTryAssignBlocksFreshWorkWhenRecoveryQuarantineOpen(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES ('oro-quarantined', 'agent/oro-quarantined', 'unsafe_stale_branch', 'unmerged branch', 'open');
`); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "oro-ready", Status: "open", Priority: 1, Type: "task"},
	})

	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := client.Read(buf); err != nil {
				return
			}
		}
	}()

	d.mu.Lock()
	d.state = StateRunning
	d.workers["w-idle"] = &trackedWorker{
		id:      "w-idle",
		conn:    server,
		state:   protocol.WorkerIdle,
		encoder: json.NewEncoder(server),
	}
	d.mu.Unlock()

	d.tryAssign(ctx)

	beadSrc.mu.Lock()
	status := beadSrc.updated["oro-ready"]
	beadSrc.mu.Unlock()
	if status == "in_progress" {
		t.Fatalf("ready bead was assigned while recovery quarantine is open")
	}

	d.mu.Lock()
	workerState := d.workers["w-idle"].state
	workerBead := d.workers["w-idle"].beadID
	d.mu.Unlock()
	if workerState != protocol.WorkerIdle || workerBead != "" {
		t.Fatalf("idle worker = state %s bead %q, want idle with no bead", workerState, workerBead)
	}

	if _, err := d.db.ExecContext(ctx, `
UPDATE recovery_quarantines SET status='resolved', resolved_at=datetime('now') WHERE status='open';
`); err != nil {
		t.Fatalf("resolve recovery quarantine: %v", err)
	}

	d.tryAssign(ctx)

	beadSrc.mu.Lock()
	status = beadSrc.updated["oro-ready"]
	beadSrc.mu.Unlock()
	if status != "in_progress" {
		t.Fatalf("ready bead status after resolving quarantines = %q, want in_progress", status)
	}
}

func TestUnsafeStaleBranchOnOpenBeadDoesNotFreezeAssignment(t *testing.T) {
	for _, tt := range []struct {
		name              string
		quarantinedStatus string
		wantFrozen        bool
		wantReadyAssigned bool
	}{
		{
			name:              "open bead leaves unrelated work assignable",
			quarantinedStatus: "open",
			wantReadyAssigned: true,
		},
		{
			name:              "closed bead retains assignment freeze",
			quarantinedStatus: "closed",
			wantFrozen:        true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, beadSrc, _, _, _, _ := newTestDispatcher(t)
			ctx := context.Background()

			beadSrc.shown["oro-quarantined"] = &protocol.BeadDetail{Status: tt.quarantinedStatus}
			beadSrc.SetBeads([]protocol.Bead{
				{ID: "oro-quarantined", Status: tt.quarantinedStatus, Priority: 0, Type: "task"},
				{ID: "oro-ready", Status: "open", Priority: 1, Type: "task"},
			})
			if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES ('oro-quarantined', 'agent/oro-quarantined', 'unsafe_stale_branch', 'unmerged branch', 'open');
`); err != nil {
				t.Fatalf("insert recovery quarantine: %v", err)
			}

			server, client := net.Pipe()
			t.Cleanup(func() {
				_ = server.Close()
				_ = client.Close()
			})
			go func() {
				buf := make([]byte, 4096)
				for {
					if _, err := client.Read(buf); err != nil {
						return
					}
				}
			}()

			d.mu.Lock()
			d.state = StateRunning
			d.workers["w-idle"] = &trackedWorker{
				id:      "w-idle",
				conn:    server,
				state:   protocol.WorkerIdle,
				encoder: json.NewEncoder(server),
			}
			d.mu.Unlock()

			if _, blocked := d.recoveryQuarantineAssignmentScope(ctx); blocked != tt.wantFrozen {
				t.Fatalf("assignment freeze = %t, want %t", blocked, tt.wantFrozen)
			}

			tryAssignAndWait(t, d, ctx)

			beadSrc.mu.Lock()
			readyStatus := beadSrc.updated["oro-ready"]
			beadSrc.mu.Unlock()
			if got := readyStatus == "in_progress"; got != tt.wantReadyAssigned {
				t.Fatalf("ready bead assigned = %t, want %t (status %q)", got, tt.wantReadyAssigned, readyStatus)
			}
			beadSrc.mu.Lock()
			quarantinedStatus := beadSrc.updated["oro-quarantined"]
			beadSrc.mu.Unlock()
			if quarantinedStatus == "in_progress" {
				t.Fatal("quarantined bead was assigned")
			}
			if !tt.wantFrozen {
				assertOpenBeadStaleBranchHealth(t, d)
			}
		})
	}
}

func assertOpenBeadStaleBranchHealth(t *testing.T, d *Dispatcher) {
	t.Helper()

	result, err := d.applyHealth()
	if err != nil {
		t.Fatalf("applyHealth: %v", err)
	}
	var health SwarmHealth
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatalf("unmarshal health: %v", err)
	}
	if health.Metrics.AssignmentFrozenByQuarantine {
		t.Fatal("open-bead stale-branch quarantine froze assignment health")
	}
	if !hasHealthFinding(health, factoryhealth.FindingRecoveryQuarantineOpen) {
		t.Fatalf("health missing open recovery quarantine finding: %+v", health.Findings)
	}
}

func TestTryAssignAllowsFreshWorkWhenRecoveryQuarantineIsHumanOwned(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status, resolved_at)
VALUES ('oro-human-owned', 'agent/oro-human-owned', 'unsafe_stale_branch', 'operator owns branch', 'human_owned', datetime('now'));
`); err != nil {
		t.Fatalf("insert human-owned recovery quarantine: %v", err)
	}
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "oro-human-owned", Status: "open", Priority: 0, Type: "task"},
		{ID: "oro-ready", Status: "open", Priority: 1, Type: "task"},
	})

	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := client.Read(buf); err != nil {
				return
			}
		}
	}()

	d.mu.Lock()
	d.state = StateRunning
	d.workers["w-idle"] = &trackedWorker{
		id:      "w-idle",
		conn:    server,
		state:   protocol.WorkerIdle,
		encoder: json.NewEncoder(server),
	}
	d.mu.Unlock()

	d.tryAssign(ctx)

	beadSrc.mu.Lock()
	readyStatus := beadSrc.updated["oro-ready"]
	humanOwnedStatus := beadSrc.updated["oro-human-owned"]
	beadSrc.mu.Unlock()
	if readyStatus != "in_progress" {
		t.Fatalf("fresh ready bead status = %q, want in_progress", readyStatus)
	}
	if humanOwnedStatus == "in_progress" {
		t.Fatal("human-owned bead was assigned")
	}
}

func TestPreservedWorktreeAutoRedeploysFreshWorker(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "oro-preserved-redeploy"
	const worktree = "/tmp/worktree-oro-preserved-redeploy"

	beadSrc.SetBeads([]protocol.Bead{{
		ID:       beadID,
		Title:    "resume preserved worker attempt",
		Status:   "open",
		Priority: 1,
		Type:     "task",
	}})
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "resume preserved worker attempt",
		Status:             "open",
		Type:               "task",
		AcceptanceCriteria: "Test: preserved worker attempt | Cmd: true | Assert: pass",
	}
	beadSrc.mu.Unlock()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, worker_id, worktree, branch, reason, details, status)
VALUES (?, 'disconnected-worker', ?, ?, 'stale_active_assignment', 'preserved clean attempt', 'open')`,
		beadID, worktree, protocol.BranchPrefix+beadID); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}

	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path != worktree {
			return "", fmt.Errorf("unexpected worktree %q", path)
		}
		return protocol.BranchPrefix + beadID, nil
	}
	wtMgr.prepareReuseFn = func(_ context.Context, path, branch, base string) (bool, error) {
		if path != worktree || branch != protocol.BranchPrefix+beadID || base != "main" {
			t.Fatalf("reuse preparation = path %q branch %q base %q", path, branch, base)
		}
		return false, nil // clean worktree: no fast-forward or rebase is required.
	}
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && strings.Join(args, " ") == "-C "+worktree+" status --porcelain" {
			return nil, nil // dirty=0
		}
		return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}}

	conn := newMockConn()
	w := &trackedWorker{id: "fresh-worker", conn: conn, state: protocol.WorkerIdle, encoder: json.NewEncoder(conn)}
	d.mu.Lock()
	d.state = StateRunning
	d.workers[w.id] = w
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	redeployable, err := d.autoRedeployablePreservedWorktrees(ctx)
	if err != nil {
		t.Fatalf("inspect preserved worktree: %v", err)
	}
	if !redeployable[beadID] {
		t.Fatalf("clean preserved worktree %q was not eligible for auto-redeploy", beadID)
	}

	tryAssignAndWait(t, d, ctx)

	state, assigned, ok := d.WorkerInfo(w.id)
	if !ok || state != protocol.WorkerBusy || assigned != beadID {
		t.Fatalf("worker assignment = exists %t state %q bead %q, want busy on %q", ok, state, assigned, beadID)
	}
	d.mu.Lock()
	gotWorktree := d.workers[w.id].worktree
	d.mu.Unlock()
	if gotWorktree != worktree {
		t.Fatalf("worker worktree = %q, want preserved %q", gotWorktree, worktree)
	}
	wtMgr.mu.Lock()
	created := len(wtMgr.created)
	wtMgr.mu.Unlock()
	if created != 0 {
		t.Fatalf("created %d fresh worktrees, want preserved worktree reuse", created)
	}
}

func TestPreservedWorktreeAutoRedeploysWithManagedQualityGateCache(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "oro-preserved-cache-redeploy"
	const worktree = "/tmp/worktree-oro-preserved-cache-redeploy"

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, worker_id, worktree, branch, reason, details, status)
VALUES (?, 'disconnected-worker', ?, ?, 'stale_active_assignment', 'managed cache only', 'open')`,
		beadID, worktree, protocol.BranchPrefix+beadID); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}
	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		return protocol.BranchPrefix + beadID, nil
	}
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && strings.Join(args, " ") == "-C "+worktree+" status --porcelain" {
			return []byte("?? .tmp-gocache/trim.txt\n?? .gocache-task/trim.txt\n?? .golangci-cache/trim.txt\n"), nil
		}
		return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	redeployable, err := d.autoRedeployablePreservedWorktrees(ctx)
	if err != nil {
		t.Fatalf("inspect preserved worktree: %v", err)
	}
	if !redeployable[beadID] {
		t.Fatalf("managed-cache preserved worktree %q was not eligible for auto-redeploy", beadID)
	}
}

func TestOfflineRequeuePreservedRedeploy(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "oro-offline-requeue"
	const worktree = "/tmp/worktree-oro-offline-requeue"

	bead := protocol.Bead{
		ID:       beadID,
		Title:    "redeploy preserved offline work",
		Status:   "open",
		Priority: 1,
		Type:     "task",
	}
	beadSrc.SetBeads([]protocol.Bead{bead})
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              bead.Title,
		Status:             "open",
		Type:               "task",
		AcceptanceCriteria: "Test: preserved work | Cmd: true | Assert: pass",
	}
	beadSrc.mu.Unlock()

	assignment, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES (?, 'offline-worker', ?, 'requeued')`, beadID, worktree)
	if err != nil {
		t.Fatalf("insert requeued preserved assignment: %v", err)
	}
	assignmentID, err := assignment.LastInsertId()
	if err != nil {
		t.Fatalf("preserved assignment ID: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, assignment_id, worker_id, worktree, branch, reason, details, status, resolved_at)
VALUES (?, ?, 'offline-worker', ?, ?, 'stale_active_assignment', 'requeued preserved attempt', 'resolved', datetime('now'))`,
		beadID, assignmentID, worktree, protocol.BranchPrefix+beadID); err != nil {
		t.Fatalf("insert resolved preserved quarantine: %v", err)
	}

	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path != worktree {
			return "", fmt.Errorf("unexpected worktree %q", path)
		}
		return protocol.BranchPrefix + beadID, nil
	}
	wtMgr.prepareReuseFn = func(_ context.Context, path, branch, base string) (bool, error) {
		if path != worktree || branch != protocol.BranchPrefix+beadID || base != "main" {
			t.Fatalf("reuse preparation = path %q branch %q base %q", path, branch, base)
		}
		return false, nil
	}
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && strings.Join(args, " ") == "-C "+worktree+" status --porcelain" {
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}}

	conn := newMockConn()
	w := &trackedWorker{id: "fresh-worker", conn: conn, state: protocol.WorkerIdle, encoder: json.NewEncoder(conn)}
	d.mu.Lock()
	d.state = StateRunning
	d.workers[w.id] = w
	// Simulate a new dispatcher process: durable recovery state survives while
	// the in-memory worktree mapping does not.
	d.worktreeByBead = make(map[string]string)
	d.mu.Unlock()

	tryAssignAndWait(t, d, ctx)

	state, assigned, ok := d.WorkerInfo(w.id)
	if !ok || state != protocol.WorkerBusy || assigned != beadID {
		t.Fatalf("worker assignment = exists %t state %q bead %q, want busy on %q", ok, state, assigned, beadID)
	}
	d.mu.Lock()
	gotWorktree := d.workers[w.id].worktree
	d.mu.Unlock()
	if gotWorktree != worktree {
		t.Fatalf("worker worktree = %q, want preserved %q", gotWorktree, worktree)
	}
	wtMgr.mu.Lock()
	created := len(wtMgr.created)
	deleted := len(wtMgr.deletedBranches)
	wtMgr.mu.Unlock()
	if created != 0 {
		t.Fatalf("created %d fresh worktrees, want preserved worktree reuse", created)
	}
	if deleted != 0 {
		t.Fatalf("deleted %d stale agent branches, want no stale cleanup", deleted)
	}
	var quarantines int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=?`, beadID).Scan(&quarantines); err != nil {
		t.Fatalf("count recovery quarantines: %v", err)
	}
	if quarantines != 1 {
		t.Fatalf("recovery quarantine rows = %d, want original resolved row only", quarantines)
	}
}

func TestRestartDoesNotRequarantinePreviouslyRequeuedPreservedWorktree(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-preserved-startup"
		worktree = "/tmp/worktree-oro-preserved-startup"
		branch   = protocol.BranchPrefix + beadID
	)

	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.branchExistsFn = func(_ context.Context, got string) (bool, error) { return got == branch, nil }
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path != worktree {
			return "", fmt.Errorf("unexpected worktree %q", path)
		}
		return "", nil // Dirty preserved worktree remains detached.
	}
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && strings.Join(args, " ") == "-C "+worktree+" status --porcelain" {
			return []byte(" M pkg/protocol/contract_schema_test.go\n"), nil
		}
		return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}}
	branchDeleteAttempts := 0
	wtMgr.deleteBranchFn = func(got string) error {
		branchDeleteAttempts++
		return fmt.Errorf("force branch delete %s: branch is checked out at %s", got, worktree)
	}

	assignment, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES (?, 'offline-worker', ?, 'active')`, beadID, worktree)
	if err != nil {
		t.Fatalf("insert preserved assignment: %v", err)
	}
	assignmentID, err := assignment.LastInsertId()
	if err != nil {
		t.Fatalf("preserved assignment ID: %v", err)
	}

	_, initialStats, err := d.restoreState(ctx)
	if err != nil {
		t.Fatalf("initial startup recovery: %v", err)
	}
	if initialStats.quarantined != 1 {
		t.Fatalf("initial startup stats = %+v, want one mismatch quarantine", initialStats)
	}
	var initialQuarantines int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recovery_quarantines
WHERE assignment_id=? AND reason='branch_worktree_mismatch' AND status='open'`, assignmentID).Scan(&initialQuarantines); err != nil {
		t.Fatalf("count initial mismatch quarantine: %v", err)
	}
	if initialQuarantines != 1 {
		t.Fatalf("initial mismatch quarantines = %d, want 1", initialQuarantines)
	}

	if _, err := d.db.ExecContext(ctx, `
UPDATE assignments SET status='requeued', completed_at=datetime('now') WHERE id=?`, assignmentID); err != nil {
		t.Fatalf("requeue preserved assignment: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `
UPDATE recovery_quarantines SET status='resolved', resolved_at=datetime('now')
WHERE assignment_id=? AND reason='branch_worktree_mismatch'`, assignmentID); err != nil {
		t.Fatalf("resolve preserved mismatch quarantine: %v", err)
	}

	for restart := 1; restart <= 2; restart++ {
		// Assignment-failure cooldown is process-local. A real dispatcher
		// restart begins without the previous process's retry throttle.
		d.mu.Lock()
		delete(d.worktreeFailures, beadID)
		d.mu.Unlock()

		recoverable, stats, err := d.restoreState(ctx)
		if err != nil {
			t.Fatalf("restart %d restore state: %v", restart, err)
		}
		if len(recoverable) != 0 || stats.recoverable != 0 || stats.quarantined != 0 {
			t.Fatalf("restart %d recovery = %+v, stats = %+v; want preserved mismatch ignored", restart, recoverable, stats)
		}

		var openQuarantines int
		if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID).Scan(&openQuarantines); err != nil {
			t.Fatalf("restart %d count open recovery quarantines: %v", restart, err)
		}
		if openQuarantines != 0 {
			t.Fatalf("restart %d open recovery quarantines = %d, want 0", restart, openQuarantines)
		}

		d.mu.Lock()
		_, tracked := d.worktreeByBead[beadID]
		d.mu.Unlock()
		if tracked {
			t.Fatalf("restart %d tracked dirty detached preserved worktree", restart)
		}

		assignable := d.filterAssignable(ctx, []protocol.Bead{{ID: beadID, Status: "open", Priority: 1, Type: "task"}})
		if len(assignable) != 1 || assignable[0].ID != beadID {
			t.Fatalf("restart %d assignable beads = %+v, want fresh retry for %s", restart, assignable, beadID)
		}
		if d.createFreshAssignmentWorktreeAllowed(ctx, beadID, "fresh-worker", d.cfg.DefaultBranch) {
			t.Fatalf("restart %d allowed worktree creation over preserved evidence", restart)
		}
	}

	if branchDeleteAttempts != 0 {
		t.Fatalf("preserved branch delete attempts = %d, want 0", branchDeleteAttempts)
	}
	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 0 {
		t.Fatalf("preserved worktree removals = %v, want none", removed)
	}

	wtMgr.mu.Lock()
	created := len(wtMgr.created)
	wtMgr.mu.Unlock()
	if created != 0 {
		t.Fatalf("fresh worktrees created over preserved evidence = %d, want 0", created)
	}

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES (?, 'distinct-worker', '/tmp/missing-distinct', 'active')`, beadID); err != nil {
		t.Fatalf("insert distinct assignment: %v", err)
	}
	if suppressedID, suppressed, err := d.resolvedPreservedMismatchForRequeuedBead(ctx, beadID); err != nil {
		t.Fatalf("check distinct assignment recovery scope: %v", err)
	} else if suppressed {
		t.Fatalf("distinct assignment hidden by preserved assignment %d", suppressedID)
	}
	_, distinctStats, err := d.restoreState(ctx)
	if err != nil {
		t.Fatalf("restore repeated startup state: %v", err)
	}
	if distinctStats.quarantined != 1 {
		t.Fatalf("distinct assignment stats = %+v, want one independently quarantined assignment", distinctStats)
	}
}

func TestFilterAssignableSkipsHumanOwnedRecoveryWork(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status, resolved_at)
VALUES ('oro-human-owned', 'agent/oro-human-owned', 'unsafe_stale_branch', 'operator owns branch', 'human_owned', datetime('now'));
`); err != nil {
		t.Fatalf("insert human-owned recovery quarantine: %v", err)
	}

	got := d.filterAssignable(ctx, []protocol.Bead{
		{ID: "oro-human-owned", Status: "open", Priority: 1, Type: "task"},
		{ID: "oro-ready", Status: "open", Priority: 2, Type: "task"},
	})

	if len(got) != 1 || got[0].ID != "oro-ready" {
		t.Fatalf("filterAssignable = %+v, want only oro-ready", got)
	}
	if eventCount(t, d.db, "recovery_quarantined_bead_skipped") == 0 {
		t.Fatalf("expected recovery_quarantined_bead_skipped event")
	}
}

func TestFilterRecoveryQuarantinedBeadsFailsClosedOnQueryError(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	if err := d.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	got := d.filterRecoveryQuarantinedBeads(ctx, []protocol.Bead{
		{ID: "oro-a", Status: "open", Priority: 1, Type: "task"},
	})
	if len(got) != 0 {
		t.Fatalf("filterRecoveryQuarantinedBeads on DB error = %+v, want fail-closed empty list", got)
	}
}

func TestGCClosedWorktreesSkipsBeadsWithOpenRecoveryQuarantine(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		"oro-quarantined": {ID: "oro-quarantined", Status: "closed"},
		"oro-clean":       {ID: "oro-clean", Status: "closed"},
	}
	beadSrc.mu.Unlock()

	if _, err := d.db.ExecContext(ctx, `
INSERT INTO recovery_quarantines (bead_id, branch, worktree, reason, details, status)
VALUES ('oro-quarantined', 'agent/oro-quarantined', '/tmp/oro-quarantined', 'unsafe_stale_branch', 'preserve me', 'open');
`); err != nil {
		t.Fatalf("insert recovery quarantine: %v", err)
	}

	var gcAllowed []string
	wtMgr.gcClosedFn = func(_ context.Context, isBeadClosed func(string) bool) error {
		for _, beadID := range []string{"oro-quarantined", "oro-clean"} {
			if isBeadClosed(beadID) {
				gcAllowed = append(gcAllowed, beadID)
			}
		}
		return nil
	}

	d.gcWorktrees(ctx)

	if len(gcAllowed) != 1 || gcAllowed[0] != "oro-clean" {
		t.Fatalf("GC candidates = %v, want only oro-clean", gcAllowed)
	}
	if eventCount(t, d.db, "gc_skipped_recovery_quarantined") == 0 {
		t.Fatalf("expected gc_skipped_recovery_quarantined event")
	}
}

func TestAssignBeadReusesWorktreeOnlyIfBranchMatches(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "oro-reuse-mismatch"
	const worktree = "/tmp/worktree-oro-reuse-mismatch"

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "reuse mismatch",
		Status:             "open",
		Type:               "task",
		AcceptanceCriteria: "Test: reuse mismatch | Cmd: true | Assert: pass",
	}
	beadSrc.mu.Unlock()

	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.currentBranchFn = func(_ context.Context, path string) (string, error) {
		if path == worktree {
			return "agent/other-bead", nil
		}
		return "", nil
	}

	conn := newMockConn()
	w := &trackedWorker{id: "w-reuse", state: protocol.WorkerIdle, conn: conn, encoder: json.NewEncoder(conn)}
	d.mu.Lock()
	d.workers[w.id] = w
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	if err := d.assignBead(ctx, w, protocol.Bead{ID: beadID, Title: "reuse mismatch", Status: "open", Type: "task"}); err != nil {
		t.Fatalf("assignBead: %v", err)
	}

	wtMgr.mu.Lock()
	created := len(wtMgr.created)
	wtMgr.mu.Unlock()
	if created != 0 {
		t.Fatalf("assignBead created a new worktree despite branch mismatch")
	}
	var reason string
	if err := d.db.QueryRowContext(ctx,
		`SELECT reason FROM recovery_quarantines WHERE bead_id=? AND status='open'`,
		beadID).Scan(&reason); err != nil {
		t.Fatalf("query recovery quarantine: %v", err)
	}
	if reason != "branch_worktree_mismatch" {
		t.Fatalf("quarantine reason = %q, want branch_worktree_mismatch", reason)
	}
}

func assertSafeDeleteFailure(branch string) error {
	return &safeDeleteError{branch: branch}
}

type safeDeleteError struct {
	branch string
}

func (e *safeDeleteError) Error() string {
	return "branch delete " + e.branch + ": error: The branch is not fully merged"
}
