package dispatcher //nolint:testpackage // white-box tests for recovery quarantine lifecycle

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

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

	err := d.deleteStaleAgentBranch(ctx, beadID, "w1")
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
