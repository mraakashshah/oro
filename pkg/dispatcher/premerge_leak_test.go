package dispatcher //nolint:testpackage // white-box tests for mergeAndComplete pre-merge leak guard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

const testPreMergeSecret = "sk-abcdefghijklmnopqrstuvwxyz123456"

func TestCheckPreMergeLeaks_BlocksOnCritical(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, esc, gitRunner, _ := newTestDispatcher(t)
	beadID, workerID, worktree, branch := "oro-leak-block", "worker-leak-block", "/tmp/wt-leak-block", "agent/oro-leak-block"
	assignmentID := seedPreMergeLeakAssignment(t, d, beadSrc, beadID, workerID, worktree)
	d.cfg.LeakScan = LeakScanConfig{Enabled: true, BlockOn: "critical"}
	d.shutdownRunner = &mockCommandRunner{output: []byte("+token=" + testPreMergeSecret + "\n")}

	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "main", assignmentID)

	if len(gitRunner.RebaseCalls()) != 0 {
		t.Fatalf("merger.Merge was called; rebase calls=%v", gitRunner.RebaseCalls())
	}
	assertPreMergeLeakEventCount(t, d, "merge_blocked_secret_leak", 1)
	assertOpenQuarantine(t, d, beadID, "pre_merge_secret_leak")
	if len(esc.Messages()) != 1 {
		t.Fatalf("escalations=%d, want 1", len(esc.Messages()))
	}
	if strings.Contains(strings.Join(esc.Messages(), "\n"), testPreMergeSecret) {
		t.Fatal("escalation leaked raw secret")
	}
	assertWorkerReleased(t, d, workerID)
}

func TestCheckPreMergeLeaks_AllowsClean(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, gitRunner, _ := newTestDispatcher(t)
	beadID, workerID, worktree, branch := "oro-leak-clean", "worker-leak-clean", "/tmp/wt-leak-clean", "agent/oro-leak-clean"
	assignmentID := seedPreMergeLeakAssignment(t, d, beadSrc, beadID, workerID, worktree)
	d.cfg.LeakScan = LeakScanConfig{Enabled: true, BlockOn: "critical"}
	d.shutdownRunner = &mockCommandRunner{output: []byte("+package main\n+const name = \"clean\"\n")}

	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "main", assignmentID)

	if len(gitRunner.RebaseCalls()) == 0 {
		t.Fatal("merger.Merge was not called for clean diff")
	}
	assertPreMergeLeakEventCount(t, d, "merge_blocked_secret_leak", 0)
}

func TestCheckPreMergeLeaks_FailOpenOnGitError(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, gitRunner, _ := newTestDispatcher(t)
	beadID, workerID, worktree, branch := "oro-leak-giterr", "worker-leak-giterr", "/tmp/wt-leak-giterr", "agent/oro-leak-giterr"
	assignmentID := seedPreMergeLeakAssignment(t, d, beadSrc, beadID, workerID, worktree)
	d.cfg.LeakScan = LeakScanConfig{Enabled: true, BlockOn: "critical"}
	d.shutdownRunner = &mockCommandRunner{err: errors.New("git diff failed")}

	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "main", assignmentID)

	if len(gitRunner.RebaseCalls()) == 0 {
		t.Fatal("merger.Merge was not called after git diff error")
	}
	assertPreMergeLeakEventCount(t, d, "merge_blocked_secret_leak", 0)
	assertPreMergeLeakEventCount(t, d, "pre_merge_leakscan_error", 1)
}

func TestCheckPreMergeLeaks_ReportOnlyMode(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, gitRunner, _ := newTestDispatcher(t)
	beadID, workerID, worktree, branch := "oro-leak-report", "worker-leak-report", "/tmp/wt-leak-report", "agent/oro-leak-report"
	assignmentID := seedPreMergeLeakAssignment(t, d, beadSrc, beadID, workerID, worktree)
	d.cfg.LeakScan = LeakScanConfig{Enabled: true, BlockOn: "none"}
	d.shutdownRunner = &mockCommandRunner{output: []byte("+token=" + testPreMergeSecret + "\n")}

	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "main", assignmentID)

	if len(gitRunner.RebaseCalls()) == 0 {
		t.Fatal("merger.Merge was not called in report-only mode")
	}
	assertPreMergeLeakEventCount(t, d, "merge_blocked_secret_leak", 0)
	assertPreMergeLeakEventCount(t, d, "pre_merge_leakscan_warn", 1)
}

func seedPreMergeLeakAssignment(t *testing.T, d *Dispatcher, beadSrc *fakeBeadStore, beadID, workerID, worktree string) int64 {
	t.Helper()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "open", Title: "leak test"}
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerBusy,
		assignmentID: 101,
		beadID:       beadID,
		worktree:     worktree,
		targetBranch: "main",
	}
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()
	mustExec(t, d, `INSERT INTO assignments (id, bead_id, worker_id, worktree, status) VALUES (101, ?, ?, ?, 'active')`, beadID, workerID, worktree)
	return 101
}

func assertOpenQuarantine(t *testing.T, d *Dispatcher, beadID, reason string) {
	t.Helper()
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=? AND reason=? AND status='open'`, beadID, reason).Scan(&count); err != nil {
		t.Fatalf("query recovery quarantine: %v", err)
	}
	if count != 1 {
		t.Fatalf("open quarantine count=%d, want 1", count)
	}
}

func assertPreMergeLeakEventCount(t *testing.T, d *Dispatcher, eventType string, want int) {
	t.Helper()
	if got := eventCount(t, d.db, eventType); got != want {
		t.Fatalf("%s events=%d, want %d", eventType, got, want)
	}
}

func assertWorkerReleased(t *testing.T, d *Dispatcher, workerID string) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workers[workerID]
	if w == nil || w.state != protocol.WorkerIdle || w.beadID != "" || w.assignmentID != 0 {
		t.Fatalf("worker after release=%+v, want idle without assignment", w)
	}
}
