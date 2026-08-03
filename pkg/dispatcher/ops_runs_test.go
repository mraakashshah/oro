package dispatcher //nolint:testpackage // exercises dispatcher-owned DB helpers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestOpsRunPersistenceLifecycle(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	if _, _, err := CreateOpsRun(ctx, nil, OpsRunRecord{Type: "decompose", BeadID: "oro-nil"}); err == nil {
		t.Fatal("CreateOpsRun nil db error = nil, want error")
	}

	rec := OpsRunRecord{
		Type:          "decompose",
		BeadID:        "oro-life",
		WorkerID:      "worker-1",
		DispatcherPID: 101,
		ProcessPID:    202,
		Runtime:       "codex",
		Model:         "gpt-5.5",
	}
	created, wasCreated, err := CreateOpsRun(ctx, db, rec)
	if err != nil {
		t.Fatalf("CreateOpsRun first: %v", err)
	}
	if !wasCreated {
		t.Fatal("CreateOpsRun first created = false, want true")
	}
	if created.ID == 0 {
		t.Fatal("CreateOpsRun first ID = 0, want persisted ID")
	}
	if created.Status != "running" {
		t.Fatalf("CreateOpsRun first status = %q, want running", created.Status)
	}

	duplicate, wasCreated, err := CreateOpsRun(ctx, db, OpsRunRecord{
		Type:     "decompose",
		BeadID:   "oro-life",
		WorkerID: "worker-2",
	})
	if err != nil {
		t.Fatalf("CreateOpsRun duplicate: %v", err)
	}
	if wasCreated {
		t.Fatal("CreateOpsRun duplicate created = true, want existing blocking row")
	}
	if duplicate.ID != created.ID {
		t.Fatalf("duplicate ID = %d, want existing ID %d", duplicate.ID, created.ID)
	}
	if duplicate.WorkerID != "worker-1" {
		t.Fatalf("duplicate WorkerID = %q, want original record", duplicate.WorkerID)
	}

	if err := CompleteOpsRun(ctx, db, created.ID, "resolved", "ok", "fixed", ""); err != nil {
		t.Fatalf("CompleteOpsRun resolved: %v", err)
	}
	if err := CompleteOpsRun(ctx, db, created.ID, "resolved", "ok", "fixed", ""); err != nil {
		t.Fatalf("CompleteOpsRun repeated resolved: %v", err)
	}
	blocking, err := FindBlockingOpsRun(ctx, db, "decompose", "oro-life")
	if err != nil {
		t.Fatalf("FindBlockingOpsRun after resolved: %v", err)
	}
	if blocking != nil {
		t.Fatalf("FindBlockingOpsRun after resolved = %#v, want nil", blocking)
	}

	next, wasCreated, err := CreateOpsRun(ctx, db, rec)
	if err != nil {
		t.Fatalf("CreateOpsRun after resolved: %v", err)
	}
	if !wasCreated {
		t.Fatal("CreateOpsRun after resolved created = false, want true")
	}
	if next.ID == created.ID {
		t.Fatalf("CreateOpsRun after resolved reused ID %d, want new row", next.ID)
	}
}

func TestFindBlockingOpsRun(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	if _, err := FindBlockingOpsRun(ctx, nil, "decompose", "oro-nil"); err == nil {
		t.Fatal("FindBlockingOpsRun nil db error = nil, want error")
	}

	for _, status := range []string{"running", "failed", "stale"} {
		rec, _, err := CreateOpsRun(ctx, db, OpsRunRecord{
			Type:   "decompose",
			BeadID: "oro-" + status,
			Status: status,
		})
		if err != nil {
			t.Fatalf("CreateOpsRun %s: %v", status, err)
		}
		blocking, err := FindBlockingOpsRun(ctx, db, "decompose", "oro-"+status)
		if err != nil {
			t.Fatalf("FindBlockingOpsRun %s: %v", status, err)
		}
		if blocking == nil {
			t.Fatalf("FindBlockingOpsRun %s = nil, want row", status)
		}
		if blocking.ID != rec.ID || blocking.Status != status {
			t.Fatalf("FindBlockingOpsRun %s = %#v, want ID %d status %q", status, blocking, rec.ID, status)
		}
	}

	for _, status := range []string{"resolved", "superseded"} {
		if _, _, err := CreateOpsRun(ctx, db, OpsRunRecord{
			Type:   "decompose",
			BeadID: "oro-" + status,
			Status: status,
		}); err != nil {
			t.Fatalf("CreateOpsRun %s: %v", status, err)
		}
		blocking, err := FindBlockingOpsRun(ctx, db, "decompose", "oro-"+status)
		if err != nil {
			t.Fatalf("FindBlockingOpsRun %s: %v", status, err)
		}
		if blocking != nil {
			t.Fatalf("FindBlockingOpsRun %s = %#v, want nil", status, blocking)
		}
	}
}

func TestOpsRunCompletionStates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	if err := CompleteOpsRun(ctx, nil, 1, "resolved", "", "", ""); err == nil {
		t.Fatal("CompleteOpsRun nil db error = nil, want error")
	}

	for _, status := range []string{"failed", "stale", "resolved", "superseded"} {
		rec, _, err := CreateOpsRun(ctx, db, OpsRunRecord{
			Type:   "decompose",
			BeadID: "oro-complete-" + status,
		})
		if err != nil {
			t.Fatalf("CreateOpsRun %s: %v", status, err)
		}
		if err := CompleteOpsRun(ctx, db, rec.ID, status, "verdict-"+status, "feedback", "error"); err != nil {
			t.Fatalf("CompleteOpsRun %s: %v", status, err)
		}
		got := fetchOpsRunForTest(t, db, rec.ID)
		if got.Status != status {
			t.Fatalf("status after CompleteOpsRun(%s) = %q", status, got.Status)
		}
		if got.CompletedAt == "" {
			t.Fatalf("CompletedAt after CompleteOpsRun(%s) is empty", status)
		}

		blocking, err := FindBlockingOpsRun(ctx, db, "decompose", "oro-complete-"+status)
		if err != nil {
			t.Fatalf("FindBlockingOpsRun %s: %v", status, err)
		}
		wantBlocking := status == "failed" || status == "stale"
		if (blocking != nil) != wantBlocking {
			t.Fatalf("FindBlockingOpsRun %s present = %v, want %v", status, blocking != nil, wantBlocking)
		}
	}

	rec, _, err := CreateOpsRun(ctx, db, OpsRunRecord{Type: "decompose", BeadID: "oro-invalid-status"})
	if err != nil {
		t.Fatalf("CreateOpsRun invalid-status fixture: %v", err)
	}
	if err := CompleteOpsRun(ctx, db, rec.ID, "wat", "", "", ""); err == nil {
		t.Fatal("CompleteOpsRun invalid status error = nil, want error")
	}
}

func TestDecomposeOpsRunPersistsTerminalOutcome(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, spawnMock := newTestDispatcher(t)

	const (
		beadID   = "oro-decompose-terminal-outcome"
		workerID = "w-decompose-terminal-outcome"
		verdict  = "resolved"
		feedback = "decomposition closed the parent after creating child tasks"
	)

	seedValidDecomposeResultForTest(t, d.db, beadID)
	if _, err := d.db.ExecContext(ctx, `UPDATE beads SET status='closed' WHERE id=?`, beadID); err != nil {
		t.Fatalf("close decompose parent: %v", err)
	}
	runID := insertDispatcherTestOpsRun(t, d, ops.OpsDecompose, beadID, workerID)
	escalationID := insertDispatcherTestEscalation(t, d.db, protocol.EscOversizedBead, beadID, workerID)
	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{
		Type:     ops.OpsDecompose,
		BeadID:   beadID,
		Verdict:  ops.Verdict(verdict),
		Feedback: feedback,
	}

	d.handleEscalationResult(ctx, escalationID, string(protocol.EscOversizedBead), beadID, workerID, resultCh)

	afterResult := fetchOpsRunForTest(t, d.db, runID)
	if afterResult.Status != opsRunStatusResolved {
		t.Fatalf("terminal ops_run status = %q, want %q", afterResult.Status, opsRunStatusResolved)
	}
	if afterResult.CompletedAt == "" {
		t.Fatal("terminal ops_run completed_at is empty")
	}
	if afterResult.Verdict != verdict {
		t.Fatalf("terminal ops_run verdict = %q, want %q", afterResult.Verdict, verdict)
	}
	if afterResult.Feedback != feedback {
		t.Fatalf("terminal ops_run feedback = %q, want %q", afterResult.Feedback, feedback)
	}

	if err := d.reconcileOpsRunsOnStartup(ctx); err != nil {
		t.Fatalf("reconcileOpsRunsOnStartup after terminal persistence: %v", err)
	}
	afterRestart := fetchOpsRunForTest(t, d.db, runID)
	if afterRestart != afterResult {
		t.Fatalf("terminal ops_run after restart = %#v, want unchanged %#v", afterRestart, afterResult)
	}
	if got := spawnMock.SpawnCount(); got != 0 {
		t.Fatalf("restart rerouted terminal decompose ops_run %d times, want 0", got)
	}
}

func TestDecomposeOpsRunPersistsFailedVerdict(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)

	const (
		beadID   = "oro-decompose-failed-verdict"
		workerID = "w-decompose-failed-verdict"
		verdict  = "failed"
		feedback = "the decomposition could not produce valid child tasks"
	)

	seedValidDecomposeResultForTest(t, d.db, beadID)
	if _, err := d.db.ExecContext(ctx, `UPDATE beads SET status='closed' WHERE id=?`, beadID); err != nil {
		t.Fatalf("close decompose parent: %v", err)
	}
	runID := insertDispatcherTestOpsRun(t, d, ops.OpsDecompose, beadID, workerID)
	escalationID := insertDispatcherTestEscalation(t, d.db, protocol.EscOversizedBead, beadID, workerID)
	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{
		Type:     ops.OpsDecompose,
		BeadID:   beadID,
		Verdict:  ops.Verdict(verdict),
		Feedback: feedback,
	}

	d.handleEscalationResult(ctx, escalationID, string(protocol.EscOversizedBead), beadID, workerID, resultCh)

	afterResult := fetchOpsRunForTest(t, d.db, runID)
	if afterResult.Status != opsRunStatusFailed {
		t.Fatalf("failed verdict ops_run status = %q, want %q", afterResult.Status, opsRunStatusFailed)
	}
	if afterResult.CompletedAt == "" {
		t.Fatal("failed verdict ops_run completed_at is empty")
	}
	if afterResult.Verdict != verdict {
		t.Fatalf("failed verdict ops_run verdict = %q, want %q", afterResult.Verdict, verdict)
	}
	if afterResult.Feedback != feedback {
		t.Fatalf("failed verdict ops_run feedback = %q, want %q", afterResult.Feedback, feedback)
	}
	blocking, err := FindBlockingOpsRun(ctx, d.db, string(ops.OpsDecompose), beadID)
	if err != nil {
		t.Fatalf("find failed verdict blocking ops_run: %v", err)
	}
	if blocking == nil || blocking.ID != runID {
		t.Fatalf("failed verdict blocking ops_run = %#v, want run %d", blocking, runID)
	}
}

func TestDispatcherStartupMarksOrphanedOpsRunsStale(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, spawnMock := newTestDispatcher(t)

	live, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          "decompose",
		BeadID:        "oro-live-ops",
		DispatcherPID: 999999,
		ProcessPID:    os.Getpid(),
	})
	if err != nil {
		t.Fatalf("CreateOpsRun live orphan: %v", err)
	}
	dead, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          "decompose",
		BeadID:        "oro-dead-ops",
		DispatcherPID: 999999,
		ProcessPID:    -1,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun dead orphan: %v", err)
	}

	if err := d.startupRecovery(ctx); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	liveAfter := fetchOpsRunForTest(t, d.db, live.ID)
	if liveAfter.Status != "stale" {
		t.Fatalf("live orphan status = %q, want stale", liveAfter.Status)
	}

	deadAfter := fetchOpsRunForTest(t, d.db, dead.ID)
	if deadAfter.Status != "superseded" {
		t.Fatalf("dead orphan status = %q, want superseded", deadAfter.Status)
	}

	blocking, err := FindBlockingOpsRun(ctx, d.db, "decompose", "oro-dead-ops")
	if err != nil {
		t.Fatalf("FindBlockingOpsRun rerouted dead orphan: %v", err)
	}
	if blocking == nil {
		t.Fatal("FindBlockingOpsRun rerouted dead orphan = nil, want new running row")
	}
	if blocking.ID == dead.ID {
		t.Fatalf("rerouted blocking row reused superseded ID %d", blocking.ID)
	}
	if blocking.Status != "running" {
		t.Fatalf("rerouted status = %q, want running", blocking.Status)
	}

	waitFor(t, func() bool {
		return spawnMock.SpawnCount() > 0
	}, time.Second)
}

func TestDispatcherStartupReturnsUnroutableReplacementFailureWriteError(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)

	orphaned, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          string(ops.OpsReview),
		BeadID:        "oro-review-replacement-failure-write",
		WorkerID:      "w-review-replacement-failure-write",
		DispatcherPID: -1,
		ProcessPID:    -1,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun orphaned review: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, fmt.Sprintf(`
CREATE TRIGGER fail_unroutable_replacement_terminal_write
BEFORE UPDATE OF status ON ops_runs
WHEN OLD.id <> %d AND NEW.status = 'failed'
BEGIN
    SELECT RAISE(FAIL, 'injected replacement failure write');
END`, orphaned.ID)); err != nil {
		t.Fatalf("create failure-write trigger: %v", err)
	}

	err = d.reconcileOpsRunsOnStartup(ctx)
	if err == nil {
		t.Fatal("reconcileOpsRunsOnStartup error = nil, want replacement failure-write error")
	}
	if !strings.Contains(err.Error(), "injected replacement failure write") {
		t.Fatalf("reconcileOpsRunsOnStartup error = %q, want injected replacement failure write", err)
	}
	if got := fetchOpsRunForTest(t, d.db, orphaned.ID).Status; got != opsRunStatusSuperseded {
		t.Fatalf("orphaned review status = %q, want %q", got, opsRunStatusSuperseded)
	}
}

func TestRouteOpsRunRoutesReviewOpsRun(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)

	beadID := "oro-review-retry"
	workerID := "w-review"
	worktree := t.TempDir()
	targetBranch := "epic/review-target"
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Review retried ops run",
		AcceptanceCriteria: "Test: pkg/dispatcher/ops_runs_test.go:TestRouteOpsRunRoutesReviewOpsRun | Assert: routed",
	}

	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		beadID:       beadID,
		worktree:     worktree,
		targetBranch: targetBranch,
	}
	d.mu.Unlock()

	routed := d.routeOpsRun(ctx, OpsRunRecord{
		Type:     "review",
		BeadID:   beadID,
		WorkerID: workerID,
	})
	if !routed {
		t.Fatal("routeOpsRun review = false, want true")
	}

	waitFor(t, func() bool { return spawnMock.SpawnCount() > 0 }, time.Second)

	spawnMock.mu.Lock()
	if len(spawnMock.spawns) != 1 {
		spawnMock.mu.Unlock()
		t.Fatalf("review spawn count = %d, want 1", len(spawnMock.spawns))
	}
	spawn := spawnMock.spawns[0]
	spawnMock.mu.Unlock()
	if spawn.workdir != worktree {
		t.Fatalf("review workdir = %q, want %q", spawn.workdir, worktree)
	}
	for _, want := range []string{
		beadID,
		"Review retried ops run",
		"Test: pkg/dispatcher/ops_runs_test.go:TestRouteOpsRunRoutesReviewOpsRun",
		"merge to " + targetBranch,
	} {
		if !strings.Contains(spawn.prompt, want) {
			t.Fatalf("review prompt missing %q\nprompt:\n%s", want, spawn.prompt)
		}
	}
	d.wg.Wait()

	defaultBranchBeadID := "oro-review-default-branch"
	defaultBranchWorktree := t.TempDir()
	d.cfg.DefaultBranch = "develop"
	beadSrc.shown[defaultBranchBeadID] = &protocol.BeadDetail{
		ID:                 defaultBranchBeadID,
		Title:              "Review default branch",
		AcceptanceCriteria: "Assert: uses dispatcher default branch",
	}
	d.mu.Lock()
	d.worktreeByBead[defaultBranchBeadID] = defaultBranchWorktree
	d.workers["w-default-branch"] = &trackedWorker{
		id:       "w-default-branch",
		beadID:   defaultBranchBeadID,
		worktree: defaultBranchWorktree,
	}
	d.mu.Unlock()

	if !d.routeOpsRun(ctx, OpsRunRecord{
		Type:     "review",
		BeadID:   defaultBranchBeadID,
		WorkerID: "w-default-branch",
	}) {
		t.Fatal("routeOpsRun review with default branch = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() > 1 }, time.Second)

	spawnMock.mu.Lock()
	defaultBranchPrompt := spawnMock.spawns[1].prompt
	spawnMock.mu.Unlock()
	if !strings.Contains(defaultBranchPrompt, "merge to develop") {
		t.Fatalf("review prompt should use dispatcher default branch; prompt:\n%s", defaultBranchPrompt)
	}

	if d.routeOpsRun(ctx, OpsRunRecord{
		Type:   "review",
		BeadID: "oro-review-no-worktree",
	}) {
		t.Fatal("routeOpsRun review without tracked worktree = true, want false")
	}
	if got := spawnMock.SpawnCount(); got != 2 {
		t.Fatalf("review spawn count after missing worktree = %d, want 2", got)
	}

	missingDetailBeadID := "oro-review-missing-detail"
	missingDetailWorktree := t.TempDir()
	d.mu.Lock()
	d.worktreeByBead[missingDetailBeadID] = missingDetailWorktree
	d.workers["w-missing-detail"] = &trackedWorker{
		id:           "w-missing-detail",
		beadID:       missingDetailBeadID,
		worktree:     missingDetailWorktree,
		targetBranch: "epic/missing-detail",
	}
	d.mu.Unlock()
	if !d.routeOpsRun(ctx, OpsRunRecord{
		Type:     "review",
		BeadID:   missingDetailBeadID,
		WorkerID: "w-missing-detail",
	}) {
		t.Fatal("routeOpsRun review with missing bead detail = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() > 2 }, time.Second)
}

func TestSupersedeOpsReviewRetryPreservesContext(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	beadID := "oro-review-retry"
	workerID := "w-review-retry"
	worktree := t.TempDir()

	beadSrc.shown[beadID] = &protocol.Bead{
		ID:                 beadID,
		Title:              "Review retry",
		AcceptanceCriteria: "Test: retry review | Assert: context is preserved",
	}
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerReviewing,
		assignmentID: 42,
		beadID:       beadID,
		worktree:     worktree,
		targetBranch: "epic/retry-review",
		runtime:      "codex",
		model:        "gpt-5.5",
	}
	d.worktreeByBead[beadID] = worktree
	d.worktreeFailures[beadID] = time.Now()
	d.mu.Unlock()

	original, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		EscalationID:  77,
		Type:          string(ops.OpsReview),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: 123,
		ProcessPID:    456,
		Runtime:       "codex",
		Model:         "gpt-5.5",
		Status:        opsRunStatusRunning,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun original: %v", err)
	}
	if err := CompleteOpsRun(ctx, d.db, original.ID, opsRunStatusFailed, "failed", "old feedback", "review crashed"); err != nil {
		t.Fatalf("CompleteOpsRun original failed: %v", err)
	}
	failed := fetchOpsRunForTest(t, d.db, original.ID)

	replacement, routed, err := d.supersedeOpsRunForRetry(failed)
	if err != nil {
		t.Fatalf("supersedeOpsRunForRetry: %v", err)
	}
	if !routed {
		t.Fatal("routed = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() == 1 }, time.Second)
	if got := spawnMock.SpawnCount(); got != 1 {
		t.Fatalf("spawn count = %d, want 1", got)
	}

	superseded := fetchOpsRunForTest(t, d.db, original.ID)
	if superseded.Status != opsRunStatusSuperseded {
		t.Fatalf("original status = %q, want superseded", superseded.Status)
	}
	if superseded.Verdict != "failed" || superseded.Feedback != "old feedback" {
		t.Fatalf("original audit context = verdict %q feedback %q, want preserved", superseded.Verdict, superseded.Feedback)
	}
	if !strings.Contains(superseded.Error, fmt.Sprintf("manual retry superseded ops run %d", original.ID)) {
		t.Fatalf("original error = %q, want manual retry supersede note", superseded.Error)
	}

	if replacement.Type != string(ops.OpsReview) ||
		replacement.BeadID != beadID ||
		replacement.WorkerID != workerID ||
		replacement.Runtime != "codex" ||
		replacement.Model != "gpt-5.5" {
		t.Fatalf("replacement context = %#v, want review/bead/worker/runtime/model preserved", replacement)
	}
	if replacement.Status != opsRunStatusRunning {
		t.Fatalf("replacement status = %q, want running", replacement.Status)
	}
	if replacement.Verdict != "" || replacement.Feedback != "" {
		t.Fatalf("replacement verdict/feedback = %q/%q, want cleared", replacement.Verdict, replacement.Feedback)
	}
	if !strings.Contains(replacement.Error, fmt.Sprintf("manual retry of ops run %d", original.ID)) {
		t.Fatalf("replacement error = %q, want manual retry note", replacement.Error)
	}

	blocking, err := FindBlockingOpsRun(ctx, d.db, string(ops.OpsReview), beadID)
	if err != nil {
		t.Fatalf("FindBlockingOpsRun after retry: %v", err)
	}
	if blocking == nil || blocking.ID != replacement.ID {
		t.Fatalf("blocking after retry = %#v, want replacement %d", blocking, replacement.ID)
	}
	var count int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM ops_runs
WHERE type = ? AND bead_id = ? AND status = ?`, string(ops.OpsReview), beadID, opsRunStatusRunning).Scan(&count); err != nil {
		t.Fatalf("count running replacements: %v", err)
	}
	if count != 1 {
		t.Fatalf("running replacement count = %d, want 1", count)
	}
	d.mu.Lock()
	_, failureStillPresent := d.worktreeFailures[beadID]
	d.mu.Unlock()
	if failureStillPresent {
		t.Fatal("worktree failure context was not cleared after retry")
	}

	staleOriginal := superseded
	_, _, err = d.supersedeOpsRunForRetry(staleOriginal)
	if err == nil {
		t.Fatal("second supersedeOpsRunForRetry error = nil, want existing replacement error")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("retry ops run %d", original.ID)) ||
		!strings.Contains(err.Error(), fmt.Sprintf("blocking ops run %d", replacement.ID)) {
		t.Fatalf("second retry error = %q, want original and replacement IDs", err)
	}
}

func TestSupersedeRuntimeIncidentRetryPreservesContext(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	beadID := "oro-runtime-retry"
	workerID := "w-runtime-retry"
	worktree := t.TempDir()

	beadSrc.shown[beadID] = &protocol.Bead{
		ID:                 beadID,
		Title:              "Runtime retry",
		Description:        "Original runtime incident context",
		AcceptanceCriteria: "Test: retry runtime incident | Assert: context is preserved",
	}
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		state:    protocol.WorkerBusy,
		beadID:   beadID,
		worktree: worktree,
		runtime:  "codex",
		model:    "gpt-5.5",
	}
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	original, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		EscalationID:  88,
		Type:          string(ops.OpsDecompose),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: 123,
		ProcessPID:    456,
		Runtime:       "codex",
		Model:         "gpt-5.5",
		Status:        opsRunStatusRunning,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun original: %v", err)
	}
	if err := CompleteOpsRun(ctx, d.db, original.ID, opsRunStatusFailed, "failed", "old feedback", "runtime crashed"); err != nil {
		t.Fatalf("CompleteOpsRun original failed: %v", err)
	}
	failed := fetchOpsRunForTest(t, d.db, original.ID)

	replacement, routed, err := d.supersedeOpsRunForRetry(failed)
	if err != nil {
		t.Fatalf("supersedeOpsRunForRetry: %v", err)
	}
	if !routed {
		t.Fatal("routed = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() == 1 }, time.Second)
	if got := spawnMock.SpawnCount(); got != 1 {
		t.Fatalf("spawn count = %d, want 1", got)
	}

	superseded := fetchOpsRunForTest(t, d.db, original.ID)
	if superseded.Status != opsRunStatusSuperseded {
		t.Fatalf("original status = %q, want superseded", superseded.Status)
	}
	if superseded.Verdict != "failed" || superseded.Feedback != "old feedback" {
		t.Fatalf("original audit context = verdict %q feedback %q, want preserved", superseded.Verdict, superseded.Feedback)
	}
	if !strings.Contains(superseded.Error, fmt.Sprintf("manual retry superseded ops run %d", original.ID)) {
		t.Fatalf("original error = %q, want manual retry supersede note", superseded.Error)
	}

	if replacement.Type != string(ops.OpsDecompose) ||
		replacement.BeadID != beadID ||
		replacement.WorkerID != workerID ||
		replacement.EscalationID != 88 ||
		replacement.Runtime != "codex" ||
		replacement.Model != "gpt-5.5" {
		t.Fatalf("replacement context = %#v, want decompose/bead/worker/escalation/runtime/model preserved", replacement)
	}
	if replacement.Status != opsRunStatusRunning {
		t.Fatalf("replacement status = %q, want running", replacement.Status)
	}
	if replacement.ProcessPID != 0 {
		t.Fatalf("replacement process pid = %d, want cleared", replacement.ProcessPID)
	}
	if replacement.Verdict != "" || replacement.Feedback != "" {
		t.Fatalf("replacement verdict/feedback = %q/%q, want cleared", replacement.Verdict, replacement.Feedback)
	}
	if replacement.Error != "runtime crashed" {
		t.Fatalf("replacement error = %q, want runtime incident error preserved", replacement.Error)
	}

	blocking, err := FindBlockingOpsRun(ctx, d.db, string(ops.OpsDecompose), beadID)
	if err != nil {
		t.Fatalf("FindBlockingOpsRun after retry: %v", err)
	}
	if blocking == nil || blocking.ID != replacement.ID {
		t.Fatalf("blocking after retry = %#v, want replacement %d", blocking, replacement.ID)
	}
	var runningCount int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM ops_runs
WHERE type = ? AND bead_id = ? AND status = ?`, string(ops.OpsDecompose), beadID, opsRunStatusRunning).Scan(&runningCount); err != nil {
		t.Fatalf("count running replacements: %v", err)
	}
	if runningCount != 1 {
		t.Fatalf("running replacement count = %d, want 1", runningCount)
	}

	_, _, err = d.supersedeOpsRunForRetry(superseded)
	if err == nil {
		t.Fatal("second supersedeOpsRunForRetry error = nil, want existing replacement error")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("retry ops run %d", original.ID)) ||
		!strings.Contains(err.Error(), fmt.Sprintf("blocking ops run %d", replacement.ID)) {
		t.Fatalf("second retry error = %q, want original and replacement IDs", err)
	}
}

func TestRouteOpsRunRetriesDecomposeIncident(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, spawnMock := newTestDispatcher(t)
	beadID := "oro-decompose-retry"
	worktree := t.TempDir()
	incidentErr := "runtime incident: worker exceeded retry budget"

	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	original, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:   string(ops.OpsDecompose),
		BeadID: beadID,
		Status: opsRunStatusRunning,
		Error:  incidentErr,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun original: %v", err)
	}
	if err := CompleteOpsRun(ctx, d.db, original.ID, opsRunStatusFailed, "failed", "old feedback", incidentErr); err != nil {
		t.Fatalf("CompleteOpsRun original failed: %v", err)
	}
	failed := fetchOpsRunForTest(t, d.db, original.ID)

	replacement, routed, err := d.supersedeOpsRunForRetry(failed)
	if err != nil {
		t.Fatalf("supersedeOpsRunForRetry: %v", err)
	}
	if !routed {
		t.Fatal("routeOpsRun decompose retry = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() == 1 }, time.Second)

	spawnMock.mu.Lock()
	if len(spawnMock.spawns) != 1 {
		spawnMock.mu.Unlock()
		t.Fatalf("spawn count = %d, want 1", len(spawnMock.spawns))
	}
	spawn := spawnMock.spawns[0]
	spawnMock.mu.Unlock()

	if spawn.workdir != worktree {
		t.Fatalf("decompose retry workdir = %q, want %q", spawn.workdir, worktree)
	}
	for _, want := range []string{beadID, incidentErr} {
		if !strings.Contains(spawn.prompt, want) {
			t.Fatalf("decompose retry prompt missing %q\nprompt:\n%s", want, spawn.prompt)
		}
	}
	if strings.Contains(spawn.prompt, "ORPHANED_OPS_RUN") {
		t.Fatalf("decompose retry invoked escalation prompt:\n%s", spawn.prompt)
	}

	persisted := fetchOpsRunForTest(t, d.db, replacement.ID)
	if persisted.Status != opsRunStatusRunning {
		t.Fatalf("replacement status = %q, want running", persisted.Status)
	}
}

func TestRouteOpsRunRetriesEscalationIncident(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	beadID := "oro-escalation-retry"
	workerID := "w-escalation-retry"
	worktree := t.TempDir()
	incidentErr := protocol.FormatEscalation(protocol.EscStuckWorker, beadID, "worker stopped reporting progress", "runtime retry context")

	beadSrc.shown[beadID] = &protocol.Bead{
		ID:          beadID,
		Title:       "Escalation retry",
		Description: "Original escalation incident context",
	}
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	original, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:     string(ops.OpsEscalation),
		BeadID:   beadID,
		WorkerID: workerID,
		Status:   opsRunStatusRunning,
		Error:    incidentErr,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun original: %v", err)
	}
	if err := CompleteOpsRun(ctx, d.db, original.ID, opsRunStatusFailed, "failed", "old feedback", incidentErr); err != nil {
		t.Fatalf("CompleteOpsRun original failed: %v", err)
	}
	failed := fetchOpsRunForTest(t, d.db, original.ID)

	replacement, routed, err := d.supersedeOpsRunForRetry(failed)
	if err != nil {
		t.Fatalf("supersedeOpsRunForRetry: %v", err)
	}
	if !routed {
		t.Fatal("routeOpsRun escalation retry = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() == 1 }, time.Second)

	spawnMock.mu.Lock()
	if len(spawnMock.spawns) != 1 {
		spawnMock.mu.Unlock()
		t.Fatalf("spawn count = %d, want 1", len(spawnMock.spawns))
	}
	spawn := spawnMock.spawns[0]
	spawnMock.mu.Unlock()

	if replacement.Type != string(ops.OpsEscalation) ||
		replacement.BeadID != beadID ||
		replacement.WorkerID != workerID {
		t.Fatalf("replacement context = %#v, want escalation/bead/worker preserved", replacement)
	}
	if replacement.Error != incidentErr {
		t.Fatalf("replacement error = %q, want original runtime incident error", replacement.Error)
	}
	if spawn.workdir != worktree {
		t.Fatalf("escalation retry workdir = %q, want %q", spawn.workdir, worktree)
	}
	for _, want := range []string{
		"Escalation type: " + string(protocol.EscStuckWorker),
		beadID,
		"Escalation retry",
		"Original escalation incident context",
		"Recent history: " + incidentErr,
	} {
		if !strings.Contains(spawn.prompt, want) {
			t.Fatalf("escalation retry prompt missing %q\nprompt:\n%s", want, spawn.prompt)
		}
	}
	if strings.Contains(spawn.prompt, "You are a task decomposition agent") {
		t.Fatalf("escalation retry invoked decompose prompt:\n%s", spawn.prompt)
	}

	fallbackBeadID := "oro-escalation-retry-fallback"
	fallbackWorktree := t.TempDir()
	d.mu.Lock()
	d.worktreeByBead[fallbackBeadID] = fallbackWorktree
	d.mu.Unlock()

	if !d.routeOpsRun(ctx, OpsRunRecord{
		Type:   string(ops.OpsEscalation),
		BeadID: fallbackBeadID,
		Error:  "runtime incident without formatted escalation type",
	}) {
		t.Fatal("routeOpsRun fallback escalation retry = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() == 2 }, time.Second)

	spawnMock.mu.Lock()
	fallbackSpawn := spawnMock.spawns[1]
	spawnMock.mu.Unlock()

	if fallbackSpawn.workdir != fallbackWorktree {
		t.Fatalf("fallback escalation retry workdir = %q, want %q", fallbackSpawn.workdir, fallbackWorktree)
	}
	for _, want := range []string{
		"Escalation type: ORPHANED_OPS_RUN",
		fallbackBeadID,
		"Recent history: runtime incident without formatted escalation type",
	} {
		if !strings.Contains(fallbackSpawn.prompt, want) {
			t.Fatalf("fallback escalation retry prompt missing %q\nprompt:\n%s", want, fallbackSpawn.prompt)
		}
	}
}

func fetchOpsRunForTest(t *testing.T, db *sql.DB, id int64) OpsRunRecord {
	t.Helper()
	rec, err := scanOpsRun(db.QueryRowContext(context.Background(), `
SELECT id, escalation_id, type, bead_id, worker_id, dispatcher_pid, process_pid, runtime, model, status, verdict, feedback, error, started_at, completed_at
FROM ops_runs
WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("ops run %d not found", id)
		}
		t.Fatalf("scan ops run %d: %v", id, err)
	}
	return rec
}
