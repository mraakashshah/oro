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
