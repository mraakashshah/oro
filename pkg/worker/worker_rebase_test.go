package worker_test

// Tests for pre-QG rebase: the worker must rebase the worktree onto the
// target branch before running the quality gate so that fixes already landed
// on main (e.g., go.mod vulnerability patches) are visible to QG.
//
// Test plan:
//   - TestRunQGAndReport_RebasesBeforeQG: worker on outdated branch gets rebased
//     onto main so QG sees the sentinel file and passes → READY_FOR_REVIEW.
//   - TestRunQGAndReport_RebaseConflict_QGStillRuns: when rebase has a conflict
//     the worker aborts cleanly and still runs QG → DONE with failure.
//   - TestRunQGAndReport_NoGitRepo_QGStillRuns: non-git worktree (rebase is
//     a no-op) but QG still runs and produces a result.

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// gitRun runs a git command in dir, fataling the test on failure.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // test helper
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// makeOutdatedWorktree creates a git repo on main, creates an agent branch
// from the initial commit, then adds sentinel.txt to main.
// It returns the repo directory (checked out on agent/test, without sentinel.txt).
func makeOutdatedWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@oro.test")
	gitRun(t, dir, "config", "user.name", "Oro Test")

	// Write initial file and commit on main.
	initFile := filepath.Join(dir, "init.txt")
	if err := os.WriteFile(initFile, []byte("init"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "init.txt")
	gitRun(t, dir, "commit", "-m", "initial")

	// Create agent branch from this point (no sentinel.txt yet).
	gitRun(t, dir, "checkout", "-b", "agent/test")

	// Add sentinel.txt to main (simulates a go.mod vuln-fix landing on main).
	gitRun(t, dir, "checkout", "main")
	sentinelFile := filepath.Join(dir, "sentinel.txt")
	if err := os.WriteFile(sentinelFile, []byte("fix"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "sentinel.txt")
	gitRun(t, dir, "commit", "-m", "add sentinel fix")

	// Return to agent branch — sentinel.txt is absent here.
	gitRun(t, dir, "checkout", "agent/test")

	return dir
}

// makeConflictWorktree creates a repo where agent/test and main have
// conflicting edits to conflict.txt so git rebase cannot auto-resolve.
func makeConflictWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@oro.test")
	gitRun(t, dir, "config", "user.name", "Oro Test")

	// Initial commit with conflict.txt
	conflictFile := filepath.Join(dir, "conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "conflict.txt")
	gitRun(t, dir, "commit", "-m", "initial")

	// Agent branch edits conflict.txt differently.
	gitRun(t, dir, "checkout", "-b", "agent/conflict")
	if err := os.WriteFile(conflictFile, []byte("agent change"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "conflict.txt")
	gitRun(t, dir, "commit", "-m", "agent edit")

	// Main also edits conflict.txt differently.
	gitRun(t, dir, "checkout", "main")
	if err := os.WriteFile(conflictFile, []byte("main change"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "conflict.txt")
	gitRun(t, dir, "commit", "-m", "main edit")

	// Return to agent branch.
	gitRun(t, dir, "checkout", "agent/conflict")

	return dir
}

// qgScript writes a quality_gate.sh to dir and returns the script body.
func writeQGScript(t *testing.T, dir, body string) {
	t.Helper()
	script := filepath.Join(dir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil { //nolint:gosec // test helper
		t.Fatal(err)
	}
}

// TestRunQGAndReport_RebasesBeforeQG verifies that the worker rebases the
// worktree onto the target branch before running QG. The QG script passes only
// if sentinel.txt is present (which only appears after a successful rebase).
func TestRunQGAndReport_RebasesBeforeQG(t *testing.T) {
	t.Parallel()

	worktreeDir := makeOutdatedWorktree(t)

	// QG passes only if sentinel.txt exists (present only after rebase onto main).
	writeQGScript(t, worktreeDir, "#!/bin/sh\n[ -f sentinel.txt ] && exit 0 || { echo 'FAIL: sentinel.txt missing'; exit 1; }\n")

	pr, pw := io.Pipe()
	proc := newMockProcess()
	spawner := &mockSpawner{process: proc, stdout: pr}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-rebase", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:       "bead-rebase",
			Worktree:     worktreeDir,
			TargetBranch: "main",
		},
	})

	// Drain STATUS running.
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus || msg.Status.State != "running" {
		t.Fatalf("expected STATUS running, got %s %s", msg.Type, msg.Status.State)
	}

	// Subprocess exits.
	_ = pw.Close()
	close(proc.waitCh)

	// Expect STATUS awaiting_review, then READY_FOR_REVIEW (QG passed after rebase).
	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS after subprocess exit, got %s", msg.Type)
	}

	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgReadyForReview {
		t.Fatalf("expected READY_FOR_REVIEW (QG should pass after rebase), got %s: %s", msg.Type, readDoneOutput(msg))
	}

	cancel()
	<-errCh
}

// TestRunQGAndReport_RebaseConflict_QGStillRuns verifies that when a rebase
// cannot be applied cleanly the worker aborts the rebase, leaves the worktree
// clean, and still runs QG (which in this case always fails).
func TestRunQGAndReport_RebaseConflict_QGStillRuns(t *testing.T) {
	t.Parallel()

	worktreeDir := makeConflictWorktree(t)

	// QG always fails so we can confirm it ran.
	writeQGScript(t, worktreeDir, "#!/bin/sh\necho 'QG ran but failed'\nexit 1\n")

	pr, pw := io.Pipe()
	proc := newMockProcess()
	spawner := &mockSpawner{process: proc, stdout: pr}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-conflict", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:       "bead-conflict",
			Worktree:     worktreeDir,
			TargetBranch: "main",
		},
	})

	// Drain STATUS running.
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus || msg.Status.State != "running" {
		t.Fatalf("expected STATUS running, got %s %s", msg.Type, msg.Status.State)
	}

	// Subprocess exits.
	_ = pw.Close()
	close(proc.waitCh)

	// Expect STATUS awaiting_review.
	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS after subprocess exit, got %s", msg.Type)
	}

	// Then DONE with failure (QG ran and failed; rebase conflict was handled).
	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgDone {
		t.Fatalf("expected DONE after failed QG, got %s", msg.Type)
	}
	if msg.Done.QualityGatePassed {
		t.Error("expected Done.QualityGatePassed=false when QG fails")
	}

	cancel()
	<-errCh
}

// TestRunQGAndReport_NoGitRepo_QGStillRuns verifies that when the worktree has
// no git repo the rebase is skipped gracefully and QG still runs to completion.
func TestRunQGAndReport_NoGitRepo_QGStillRuns(t *testing.T) {
	t.Parallel()

	worktreeDir := t.TempDir() // no git repo

	// QG always passes.
	writeQGScript(t, worktreeDir, "#!/bin/sh\necho 'QG passed (no git)'\nexit 0\n")

	pr, pw := io.Pipe()
	proc := newMockProcess()
	spawner := &mockSpawner{process: proc, stdout: pr}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-nogit", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:        "bead-nogit",
			Worktree:      worktreeDir,
			QGEvidenceDir: t.TempDir(),
			TargetSHA:     strings.Repeat("1", 40),
		},
	})

	// Drain STATUS running.
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus || msg.Status.State != "running" {
		t.Fatalf("expected STATUS running, got %s %s", msg.Type, msg.Status.State)
	}

	// Subprocess exits.
	_ = pw.Close()
	close(proc.waitCh)

	// Expect STATUS awaiting_review.
	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS after subprocess exit, got %s", msg.Type)
	}

	// The gate ran, but durable READY fails closed because a non-git worktree
	// cannot provide the measured post-QG HEAD required by its evidence.
	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgDone || !strings.Contains(readDoneOutput(msg), "read post-quality-gate HEAD:") {
		t.Fatalf("expected DONE with post-QG HEAD error, got %s: %s", msg.Type, readDoneOutput(msg))
	}

	cancel()
	<-errCh
}

// readDoneOutput extracts the output string from a DONE message for diagnostics.
func readDoneOutput(msg protocol.Message) string {
	if msg.Done != nil {
		return msg.Done.QGOutput
	}
	return ""
}
