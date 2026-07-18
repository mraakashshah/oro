package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRecoveryListAndResolve(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	res, err := db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, branch, worktree, reason, details, status)
VALUES ('oro-recover-cli', 'agent/oro-recover-cli', '/tmp/oro-recover-cli', 'unsafe_stale_branch', 'operator needed', 'open')`)
	if err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	db.Close()

	root := newRootCmd()
	var listOut bytes.Buffer
	root.SetOut(&listOut)
	root.SetArgs([]string{"recovery", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery list: %v", err)
	}
	if got := listOut.String(); !strings.Contains(got, "oro-recover-cli") || !strings.Contains(got, "unsafe_stale_branch") {
		t.Fatalf("recovery list output missing quarantine: %q", got)
	}

	root = newRootCmd()
	var jsonOut bytes.Buffer
	root.SetOut(&jsonOut)
	root.SetArgs([]string{"recovery", "list", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery list --json: %v", err)
	}
	var records []recoveryQuarantineCLIRecord
	if err := json.Unmarshal(jsonOut.Bytes(), &records); err != nil {
		t.Fatalf("recovery list --json invalid JSON: %v\nraw: %s", err, jsonOut.String())
	}
	if len(records) != 1 || records[0].ID != id {
		t.Fatalf("records = %+v, want id %d", records, id)
	}

	root = newRootCmd()
	var resolveOut bytes.Buffer
	root.SetOut(&resolveOut)
	root.SetArgs([]string{"recovery", "resolve", strconv.FormatInt(id, 10)})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery resolve: %v", err)
	}
	if !strings.Contains(resolveOut.String(), "resolved") {
		t.Fatalf("resolve output = %q, want resolved message", resolveOut.String())
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatalf("query resolved status: %v", err)
	}
	if status != "resolved" {
		t.Fatalf("status = %q, want resolved", status)
	}
}

func TestRecoveryInspectReportsPreservedWork(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	worktree := filepath.Join(tmpDir, "repo")
	initRecoveryTestRepo(t, worktree, "agent/oro-inspect")
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO beads (id, title, status, type)
VALUES ('oro-inspect', 'Inspect preserved recovery work', 'open', 'task')`); err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	res, err := db.ExecContext(context.Background(), `
INSERT INTO assignments (bead_id, worker_id, worktree, status, attempt_count)
VALUES ('oro-inspect', 'worker-1', ?, 'quarantined', 2)`, worktree)
	if err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}
	res, err = db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, assignment_id, worker_id, worktree, branch, reason, details, status)
VALUES ('oro-inspect', ?, 'worker-1', ?, 'agent/oro-inspect', 'stale_active_assignment', 'left active after restart', 'open')`,
		assignmentID, worktree)
	if err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quarantine id: %v", err)
	}
	db.Close()

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"recovery", "inspect", strconv.FormatInt(id, 10), "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery inspect: %v", err)
	}

	var got recoveryInspection
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("inspect output invalid JSON: %v\nraw: %s", err, out.String())
	}
	if got.Quarantine.ID != id {
		t.Fatalf("quarantine id = %d, want %d", got.Quarantine.ID, id)
	}
	if got.Assignment == nil || got.Assignment.Status != "quarantined" || got.Assignment.AttemptCount != 2 {
		t.Fatalf("assignment = %+v, want quarantined attempt 2", got.Assignment)
	}
	if got.Bead == nil || got.Bead.Title != "Inspect preserved recovery work" {
		t.Fatalf("bead = %+v, want seeded bead", got.Bead)
	}
	if !got.Worktree.Exists || got.Worktree.CheckedOutBranch != "agent/oro-inspect" {
		t.Fatalf("worktree = %+v, want existing agent branch", got.Worktree)
	}
	if !got.Branch.Exists {
		t.Fatalf("branch = %+v, want existing branch", got.Branch)
	}
	if got.Dirty.Total == 0 || got.Dirty.Modified == 0 {
		t.Fatalf("dirty = %+v, want modified tracked file", got.Dirty)
	}
	if got.RecommendedAction == "" {
		t.Fatalf("recommended action is empty: %+v", got)
	}
}

func TestRecoveryInspectionFormattingAndDiscardSafety(t *testing.T) {
	inspection := recoveryInspection{
		Quarantine: recoveryQuarantineCLIRecord{
			ID:      17,
			BeadID:  "oro-recover-format",
			Reason:  "external_close_recovery_failed",
			Details: "merge conflict preserved work",
			Status:  "open",
		},
		Bead: &recoveryBeadInspection{
			Title:  "Recover formatting",
			Status: "open",
			Type:   "task",
		},
		Assignment: &recoveryAssignmentInspection{
			ID:           99,
			Status:       "quarantined",
			WorkerID:     "worker-1",
			AttemptCount: 2,
		},
		Branch: recoveryBranchInspection{
			Name:   "agent/oro-recover-format",
			Exists: true,
			Ahead:  3,
			Behind: 1,
			Error:  "branch warning",
		},
		Worktree: recoveryWorktreeInspection{
			Path:             "/tmp/oro-recover-format",
			Exists:           true,
			CheckedOutBranch: "agent/oro-recover-format",
			Error:            "worktree warning",
		},
		Dirty: recoveryDirtyInspection{
			Total:     2,
			Staged:    1,
			Modified:  1,
			Untracked: 1,
			Sample:    []string{"M tracked.txt", "?? new.txt"},
		},
		RecommendedAction: "preserve and requeue",
	}
	if discardEmptySafe(inspection) {
		t.Fatal("dirty recovery inspection should not be discard-empty-safe")
	}

	var out bytes.Buffer
	writeRecoveryInspection(&out, inspection)
	got := out.String()
	for _, want := range []string{
		"#17 oro-recover-format external_close_recovery_failed status=open",
		"details: merge conflict preserved work",
		"bead: Recover formatting (open, task)",
		"assignment: #99 quarantined worker=worker-1 attempts=2",
		"branch: agent/oro-recover-format exists=true ahead=3 behind=1 error=\"branch warning\"",
		"worktree: /tmp/oro-recover-format exists=true branch=agent/oro-recover-format error=\"worktree warning\"",
		"dirty: total=2 staged=1 modified=1 deleted=0 untracked=1",
		"M tracked.txt",
		"?? new.txt",
		"action: preserve and requeue",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("recovery inspection output missing %q:\n%s", want, got)
		}
	}

	clean := recoveryInspection{
		Branch: recoveryBranchInspection{Exists: true},
	}
	if !discardEmptySafe(clean) {
		t.Fatal("clean branch with no ahead commits should be discard-empty-safe")
	}
	clean.Branch.Ahead = 1
	if discardEmptySafe(clean) {
		t.Fatal("branch with ahead commits should not be discard-empty-safe")
	}
}

func TestRecoveryResolveRequeuePreservedRequeuesAssignment(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	res, err := db.ExecContext(context.Background(), `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES ('oro-requeue', 'worker-1', '/tmp/oro-requeue', 'quarantined')`)
	if err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}
	res, err = db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, assignment_id, worker_id, worktree, branch, reason, details, status)
VALUES ('oro-requeue', ?, 'worker-1', '/tmp/oro-requeue', 'agent/oro-requeue', 'stale_active_assignment', 'preserved', 'open')`,
		assignmentID)
	if err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quarantine id: %v", err)
	}
	db.Close()

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"recovery", "resolve", strconv.FormatInt(id, 10), "--mode", "requeue-preserved"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery resolve --mode requeue-preserved: %v", err)
	}
	if !strings.Contains(out.String(), "requeue-preserved") {
		t.Fatalf("resolve output = %q, want mode", out.String())
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var quarantineStatus, assignmentStatus string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&quarantineStatus); err != nil {
		t.Fatalf("query quarantine status: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if quarantineStatus != "resolved" || assignmentStatus != "requeued" {
		t.Fatalf("quarantine=%q assignment=%q, want resolved/requeued", quarantineStatus, assignmentStatus)
	}
}

func TestRecoveryResolveRequeuePreservedFallsBackToLatestAssignment(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	res, err := db.ExecContext(context.Background(), `
INSERT INTO assignments (bead_id, worker_id, worktree, status, completed_at)
VALUES ('oro-requeue-fallback', 'worker-1', '/tmp/oro-requeue-fallback', 'completed', datetime('now'))`)
	if err != nil {
		t.Fatalf("seed completed assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}
	res, err = db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, worker_id, worktree, branch, reason, details, status)
VALUES ('oro-requeue-fallback', 'worker-2', '/tmp/oro-requeue-fallback', 'agent/oro-requeue-fallback', 'unsafe_stale_branch', 'preserved', 'open')`)
	if err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quarantine id: %v", err)
	}
	db.Close()

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"recovery", "resolve", strconv.FormatInt(id, 10), "--mode", "requeue-preserved"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery resolve --mode requeue-preserved: %v", err)
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var quarantineStatus, assignmentStatus string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&quarantineStatus); err != nil {
		t.Fatalf("query quarantine status: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if quarantineStatus != "resolved" || assignmentStatus != "requeued" {
		t.Fatalf("quarantine=%q assignment=%q, want resolved/requeued", quarantineStatus, assignmentStatus)
	}
}

func TestRecoveryResolveRequeuePreservedRefusesOlderEligibleAssignment(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO assignments (bead_id, worker_id, worktree, status, completed_at)
VALUES ('oro-requeue-latest-active', 'worker-1', '/tmp/oro-requeue-latest-active', 'completed', datetime('now'))`); err != nil {
		t.Fatalf("seed older completed assignment: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES ('oro-requeue-latest-active', 'worker-2', '/tmp/oro-requeue-latest-active', 'active')`); err != nil {
		t.Fatalf("seed latest active assignment: %v", err)
	}
	res, err := db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, worker_id, worktree, branch, reason, details, status)
VALUES ('oro-requeue-latest-active', 'worker-3', '/tmp/oro-requeue-latest-active', 'agent/oro-requeue-latest-active', 'unsafe_stale_branch', 'preserved', 'open')`)
	if err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quarantine id: %v", err)
	}
	db.Close()

	root := newRootCmd()
	root.SetArgs([]string{"recovery", "resolve", strconv.FormatInt(id, 10), "--mode", "requeue-preserved"})
	if err := root.Execute(); err == nil {
		t.Fatal("recovery resolve unexpectedly requeued an older completed assignment")
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var quarantineStatus string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&quarantineStatus); err != nil {
		t.Fatalf("query quarantine status: %v", err)
	}
	if quarantineStatus != "open" {
		t.Fatalf("quarantine status = %q, want open", quarantineStatus)
	}
	rows, err := db.QueryContext(context.Background(), `SELECT status FROM assignments WHERE bead_id='oro-requeue-latest-active' ORDER BY id`)
	if err != nil {
		t.Fatalf("query assignment statuses: %v", err)
	}
	defer rows.Close()
	var statuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan assignment status: %v", err)
		}
		statuses = append(statuses, status)
	}
	if got := strings.Join(statuses, ","); got != "completed,active" {
		t.Fatalf("assignment statuses = %q, want completed,active", got)
	}
}

func TestRecoveryAssignmentIDForRequeueRefusesStaleQuarantineLink(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	res, err := db.ExecContext(context.Background(), `
INSERT INTO assignments (bead_id, worker_id, worktree, status, completed_at)
VALUES ('oro-requeue-stale-link', 'worker-1', '/tmp/oro-requeue-stale-link', 'completed', datetime('now'))`)
	if err != nil {
		t.Fatalf("seed fallback assignment: %v", err)
	}
	fallbackID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("fallback assignment id: %v", err)
	}
	res, err = db.ExecContext(context.Background(), `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES ('oro-other-bead', 'worker-2', '/tmp/oro-other-bead', 'quarantined')`)
	if err != nil {
		t.Fatalf("seed linked assignment: %v", err)
	}
	linkedID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("linked assignment id: %v", err)
	}
	res, err = db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, worker_id, worktree, branch, reason, details, status)
VALUES ('oro-requeue-stale-link', 'worker-1', '/tmp/oro-requeue-stale-link', 'agent/oro-requeue-stale-link', 'unsafe_stale_branch', 'preserved', 'open')`)
	if err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}
	quarantineID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quarantine id: %v", err)
	}

	staleQuarantine, err := getRecoveryQuarantine(context.Background(), db, quarantineID)
	if err != nil {
		t.Fatalf("read stale quarantine: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE recovery_quarantines SET assignment_id=? WHERE id=?`, linkedID, quarantineID); err != nil {
		t.Fatalf("link quarantine concurrently: %v", err)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err := recoveryAssignmentIDForRequeue(context.Background(), tx, quarantineID, staleQuarantine); err == nil {
		t.Fatal("recoveryAssignmentIDForRequeue accepted a stale NULL assignment link")
	}

	var gotLinkedID int64
	if err := db.QueryRowContext(context.Background(), `SELECT assignment_id FROM recovery_quarantines WHERE id=?`, quarantineID).Scan(&gotLinkedID); err != nil {
		t.Fatalf("query linked assignment: %v", err)
	}
	if gotLinkedID != linkedID || gotLinkedID == fallbackID {
		t.Fatalf("quarantine assignment_id = %d, want preserved concurrent link %d", gotLinkedID, linkedID)
	}
}

func TestRecoveryResolveRequeuePreservedReleasesHumanOwned(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	res, err := db.ExecContext(context.Background(), `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES ('oro-requeue-human', 'worker-1', '/tmp/oro-requeue-human', 'quarantined')`)
	if err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("assignment id: %v", err)
	}
	res, err = db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, assignment_id, worker_id, worktree, branch, reason, details, status, resolved_at)
VALUES ('oro-requeue-human', ?, 'worker-1', '/tmp/oro-requeue-human', 'agent/oro-requeue-human', 'branch_worktree_mismatch', 'operator owns branch', 'human_owned', datetime('now'))`,
		assignmentID)
	if err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quarantine id: %v", err)
	}
	db.Close()

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"recovery", "resolve", strconv.FormatInt(id, 10), "--mode", "requeue-preserved"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery resolve --mode requeue-preserved: %v", err)
	}
	if !strings.Contains(out.String(), "requeue-preserved") {
		t.Fatalf("resolve output = %q, want mode", out.String())
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var quarantineStatus, assignmentStatus string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&quarantineStatus); err != nil {
		t.Fatalf("query quarantine status: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if quarantineStatus != "resolved" || assignmentStatus != "requeued" {
		t.Fatalf("quarantine=%q assignment=%q, want resolved/requeued", quarantineStatus, assignmentStatus)
	}
}

func TestRecoveryResolveHumanOwnedSetsDurableDisposition(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	res, err := db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES ('oro-human-owned', 'agent/oro-human-owned', 'unsafe_stale_branch', 'operator took branch', 'open')`)
	if err != nil {
		t.Fatalf("seed recovery quarantine: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quarantine id: %v", err)
	}
	db.Close()

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"recovery", "resolve", strconv.FormatInt(id, 10), "--mode", "human-owned"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery resolve --mode human-owned: %v", err)
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "human_owned" {
		t.Fatalf("status = %q, want human_owned", status)
	}
}

func TestRecoveryListIncludesHumanOwnedQuarantines(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status, resolved_at)
VALUES ('oro-human-owned', 'agent/oro-human-owned', 'unsafe_stale_branch', 'operator owns branch', 'human_owned', datetime('now'))`); err != nil {
		t.Fatalf("seed human-owned recovery quarantine: %v", err)
	}
	db.Close()

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"recovery", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery list: %v", err)
	}
	if !strings.Contains(out.String(), "oro-human-owned") {
		t.Fatalf("recovery list output missing human-owned row:\n%s", out.String())
	}
}

func TestRecoveryListJSONIncludesStatus(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()
	for _, row := range []struct {
		beadID string
		status string
	}{
		{beadID: "oro-json-open", status: "open"},
		{beadID: "oro-json-human-owned", status: "human_owned"},
		{beadID: "oro-json-resolved", status: "resolved"},
	} {
		if _, err := db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status)
VALUES (?, 'agent/recovery-json', 'unsafe_stale_branch', 'operator needed', ?)`, row.beadID, row.status); err != nil {
			t.Fatalf("seed %s recovery quarantine: %v", row.status, err)
		}
	}
	db.Close()

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"recovery", "list", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery list --json: %v", err)
	}

	var records []map[string]any
	if err := json.Unmarshal(out.Bytes(), &records); err != nil {
		t.Fatalf("recovery list --json invalid JSON: %v\nraw: %s", err, out.String())
	}
	got := make(map[string]string, len(records))
	for _, record := range records {
		beadID, _ := record["bead_id"].(string)
		status, ok := record["status"].(string)
		if !ok || status == "" {
			t.Fatalf("record for %q omits status: %#v", beadID, record)
		}
		got[beadID] = status
	}
	if got["oro-json-open"] != "open" || got["oro-json-human-owned"] != "human_owned" {
		t.Fatalf("returned statuses = %#v, want open and human_owned", got)
	}
	if _, ok := got["oro-json-resolved"]; ok {
		t.Fatalf("returned statuses = %#v, resolved row must be omitted", got)
	}
}

func TestRecoveryResolveAfterMergeCanReleaseHumanOwnedQuarantine(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	res, err := db.ExecContext(context.Background(), `
INSERT INTO recovery_quarantines (bead_id, branch, reason, details, status, resolved_at)
VALUES ('oro-human-owned', 'agent/oro-human-owned', 'unsafe_stale_branch', 'operator took branch', 'human_owned', datetime('now'))`)
	if err != nil {
		t.Fatalf("seed human-owned recovery quarantine: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("quarantine id: %v", err)
	}
	db.Close()

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"recovery", "resolve", strconv.FormatInt(id, 10), "--mode", "resolved-after-merge"})
	if err := root.Execute(); err != nil {
		t.Fatalf("recovery resolve --mode resolved-after-merge: %v", err)
	}

	db, err = openStateDB(dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRowContext(context.Background(), `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "resolved" {
		t.Fatalf("status = %q, want resolved", status)
	}
}

func initRecoveryTestRepo(t *testing.T, dir, branch string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runRecoveryGit(t, dir, "init", "-b", "main")
	runRecoveryGit(t, dir, "config", "user.email", "test@example.com")
	runRecoveryGit(t, dir, "config", "user.name", "Oro Test")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runRecoveryGit(t, dir, "add", "tracked.txt")
	runRecoveryGit(t, dir, "commit", "-m", "initial")
	runRecoveryGit(t, dir, "checkout", "-b", branch)
}

func runRecoveryGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}
