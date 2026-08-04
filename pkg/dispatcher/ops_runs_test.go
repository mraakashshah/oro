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

func TestCreateOpsRunPropagatesNonUniqueInsertFailure(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	const beadID = "oro-create-ops-arbitrary-insert-error"

	original, _, err := CreateOpsRun(ctx, db, OpsRunRecord{Type: string(ops.OpsDecompose), BeadID: beadID})
	if err != nil {
		t.Fatalf("CreateOpsRun original: %v", err)
	}
	installOpsRunInsertFailureTrigger(ctx, t, db, beadID)

	_, wasCreated, err := CreateOpsRun(ctx, db, OpsRunRecord{Type: string(ops.OpsDecompose), BeadID: beadID})
	if err == nil {
		t.Fatal("CreateOpsRun arbitrary insert error = nil, want propagated error")
	}
	if wasCreated {
		t.Fatal("CreateOpsRun arbitrary insert error created = true, want false")
	}
	if !strings.Contains(err.Error(), "injected replacement insert failure") {
		t.Fatalf("CreateOpsRun error = %q, want injected replacement insert failure", err)
	}
	if got := fetchOpsRunForTest(t, db, original.ID).Status; got != opsRunStatusRunning {
		t.Fatalf("original status = %q, want %q", got, opsRunStatusRunning)
	}
}

func TestStartupOpsRunReplacementInsertFailureRollsBackSupersede(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	const beadID = "oro-startup-replacement-insert-rollback"

	original, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          string(ops.OpsDecompose),
		BeadID:        beadID,
		DispatcherPID: -1,
		ProcessPID:    -1,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun original: %v", err)
	}
	installOpsRunInsertFailureTrigger(ctx, t, d.db, beadID)

	err = d.reconcileOpsRunsOnStartup(ctx)
	if err == nil {
		t.Fatal("reconcileOpsRunsOnStartup replacement insert error = nil, want error")
	}
	if !strings.Contains(err.Error(), "injected replacement insert failure") {
		t.Fatalf("reconcileOpsRunsOnStartup error = %q, want injected replacement insert failure", err)
	}
	after := fetchOpsRunForTest(t, d.db, original.ID)
	if after.Status != opsRunStatusRunning || after.CompletedAt != "" {
		t.Fatalf("original after failed replacement = status %q completed_at %q, want running/empty", after.Status, after.CompletedAt)
	}
	assertOpsRunCount(t, d.db, string(ops.OpsDecompose), beadID, 1)
}

func TestManualOpsRunReplacementInsertFailureRollsBackSupersede(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	const beadID = "oro-manual-replacement-insert-rollback"

	original, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:   string(ops.OpsDecompose),
		BeadID: beadID,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun original: %v", err)
	}
	if err := CompleteOpsRun(ctx, d.db, original.ID, opsRunStatusFailed, "failed", "original feedback", "original failure"); err != nil {
		t.Fatalf("CompleteOpsRun original: %v", err)
	}
	before := fetchOpsRunForTest(t, d.db, original.ID)
	installOpsRunInsertFailureTrigger(ctx, t, d.db, beadID)

	_, _, err = d.supersedeOpsRunForRetry(before)
	if err == nil {
		t.Fatal("supersedeOpsRunForRetry replacement insert error = nil, want error")
	}
	if !strings.Contains(err.Error(), "injected replacement insert failure") {
		t.Fatalf("supersedeOpsRunForRetry error = %q, want injected replacement insert failure", err)
	}
	after := fetchOpsRunForTest(t, d.db, original.ID)
	if after != before {
		t.Fatalf("original after failed manual replacement = %#v, want unchanged %#v", after, before)
	}
	assertOpsRunCount(t, d.db, string(ops.OpsDecompose), beadID, 1)
}

func installOpsRunInsertFailureTrigger(ctx context.Context, t *testing.T, db *sql.DB, beadID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`
CREATE TRIGGER fail_ops_run_replacement_insert
BEFORE INSERT ON ops_runs
WHEN NEW.bead_id = %q
BEGIN
    SELECT RAISE(FAIL, 'injected replacement insert failure');
END`, beadID)); err != nil {
		t.Fatalf("create ops-run insert failure trigger: %v", err)
	}
}

func assertOpsRunCount(t *testing.T, db *sql.DB, runType, beadID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM ops_runs WHERE type=? AND bead_id=?`, runType, beadID,
	).Scan(&got); err != nil {
		t.Fatalf("count ops runs: %v", err)
	}
	if got != want {
		t.Fatalf("ops run count = %d, want %d", got, want)
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

func TestCompleteOpsRunCASPreservesTerminalRowsAndAllowsExactReplay(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	replay, _, err := CreateOpsRun(ctx, db, OpsRunRecord{Type: "decompose", BeadID: "oro-ops-exact-replay"})
	if err != nil {
		t.Fatalf("CreateOpsRun replay: %v", err)
	}
	if err := CompleteOpsRun(ctx, db, replay.ID, opsRunStatusResolved, "original-verdict", "original-feedback", "original-error"); err != nil {
		t.Fatalf("CompleteOpsRun original replay outcome: %v", err)
	}
	wantReplay := fetchOpsRunForTest(t, db, replay.ID)
	if err := CompleteOpsRun(ctx, db, replay.ID, opsRunStatusResolved, "original-verdict", "original-feedback", "original-error"); err != nil {
		t.Fatalf("CompleteOpsRun exact replay: %v", err)
	}
	if got := fetchOpsRunForTest(t, db, replay.ID); got != wantReplay {
		t.Fatalf("exact replay changed terminal row\n got: %#v\nwant: %#v", got, wantReplay)
	}

	for _, terminalStatus := range []string{
		opsRunStatusFailed,
		opsRunStatusStale,
		opsRunStatusResolved,
		opsRunStatusSuperseded,
	} {
		t.Run(terminalStatus, func(t *testing.T) {
			rec, _, err := CreateOpsRun(ctx, db, OpsRunRecord{
				Type:   "decompose",
				BeadID: "oro-ops-late-result-" + terminalStatus,
			})
			if err != nil {
				t.Fatalf("CreateOpsRun: %v", err)
			}
			if err := CompleteOpsRun(ctx, db, rec.ID, terminalStatus, "original-verdict", "original-feedback", "original-error"); err != nil {
				t.Fatalf("CompleteOpsRun original terminal outcome: %v", err)
			}
			want := fetchOpsRunForTest(t, db, rec.ID)

			err = CompleteOpsRun(ctx, db, rec.ID, opsRunStatusResolved, "late-verdict", "late-feedback", "late-error")
			if err == nil {
				t.Fatal("CompleteOpsRun late result = nil, want compare-and-swap rejection")
			}
			if got := fetchOpsRunForTest(t, db, rec.ID); got != want {
				t.Fatalf("late result changed terminal row\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestWatchReroutedOpsRunResultRunsSideEffectsOnlyForAcquiredCompletion(t *testing.T) {
	tests := []struct {
		name          string
		prepare       func(context.Context, *testing.T, *Dispatcher, OpsRunRecord)
		wantCallbacks int
		wantStatus    string
	}{
		{
			name:          "running owner",
			wantCallbacks: 1,
			wantStatus:    opsRunStatusResolved,
		},
		{
			name: "exact terminal replay",
			prepare: func(ctx context.Context, t *testing.T, d *Dispatcher, rec OpsRunRecord) {
				t.Helper()
				if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusResolved, string(ops.VerdictResolved), "rerouted result", ""); err != nil {
					t.Fatalf("prepare exact replay: %v", err)
				}
			},
			wantStatus: opsRunStatusResolved,
		},
		{
			name: "superseded before result",
			prepare: func(ctx context.Context, t *testing.T, d *Dispatcher, rec OpsRunRecord) {
				t.Helper()
				if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusSuperseded, "superseded", "replacement owns work", "manual retry"); err != nil {
					t.Fatalf("prepare superseded run: %v", err)
				}
			},
			wantStatus: opsRunStatusSuperseded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d, _, _, _, _, _ := newTestDispatcher(t)
			rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
				Type:     string(ops.OpsReview),
				BeadID:   "oro-rerouted-side-effect-" + strings.ReplaceAll(tt.name, " ", "-"),
				WorkerID: "worker-rerouted-side-effect",
			})
			if err != nil {
				t.Fatalf("CreateOpsRun: %v", err)
			}
			if tt.prepare != nil {
				tt.prepare(ctx, t, d, rec)
			}
			before := fetchOpsRunForTest(t, d.db, rec.ID)
			resultCh := make(chan ops.Result, 1)
			resultCh <- ops.Result{
				Type:     ops.OpsReview,
				BeadID:   rec.BeadID,
				Verdict:  ops.VerdictResolved,
				Feedback: "rerouted result",
			}
			callbacks := 0

			d.watchReroutedOpsRunResult(ctx, rec, resultCh, func(ops.Result) {
				callbacks++
			})
			d.wg.Wait()

			if callbacks != tt.wantCallbacks {
				t.Fatalf("afterComplete callbacks = %d, want %d", callbacks, tt.wantCallbacks)
			}
			after := fetchOpsRunForTest(t, d.db, rec.ID)
			if after.Status != tt.wantStatus {
				t.Fatalf("ops run status = %q, want %q", after.Status, tt.wantStatus)
			}
			if tt.wantCallbacks == 0 && after != before {
				t.Fatalf("unowned result changed terminal row\n got: %#v\nwant: %#v", after, before)
			}
		})
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

	d.handleEscalationResult(ctx, runID, escalationID, string(protocol.EscOversizedBead), beadID, workerID, resultCh)

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

	d.handleEscalationResult(ctx, runID, escalationID, string(protocol.EscOversizedBead), beadID, workerID, resultCh)

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

func TestEscalationResultCannotCompleteOrAcknowledgeReplacementOpsRun(t *testing.T) {
	tests := []struct {
		name    string
		escType protocol.EscalationType
		runType ops.Type
	}{
		{name: "decompose", escType: protocol.EscOversizedBead, runType: ops.OpsDecompose},
		{name: "write_ac", escType: protocol.EscMissingAC, runType: ops.OpsWriteAC},
		{name: "escalation", escType: protocol.EscStuckWorker, runType: ops.OpsEscalation},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d, _, _, _, _, _ := newTestDispatcher(t)
			beadID := "oro-late-" + tt.name
			workerID := "w-late-" + tt.name
			escalationID := insertDispatcherTestEscalation(t, d.db, tt.escType, beadID, workerID)

			original, created, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
				EscalationID:  escalationID,
				Type:          string(tt.runType),
				BeadID:        beadID,
				WorkerID:      workerID,
				DispatcherPID: os.Getpid(),
				Status:        opsRunStatusRunning,
			})
			if err != nil || !created {
				t.Fatalf("create original ops run: created=%t err=%v", created, err)
			}
			next := original
			next.ID = 0
			next.Status = opsRunStatusRunning
			next.Verdict = ""
			next.Feedback = ""
			next.Error = ""
			next.StartedAt = ""
			next.CompletedAt = ""
			replacement, replaced, err := replaceOpsRun(ctx, d.db, original, next, "test replacement before late result")
			if err != nil || !replaced {
				t.Fatalf("replace original ops run: replaced=%t err=%v", replaced, err)
			}

			originalBefore := fetchOpsRunForTest(t, d.db, original.ID)
			replacementBefore := fetchOpsRunForTest(t, d.db, replacement.ID)
			resultCh := make(chan ops.Result, 1)
			resultCh <- ops.Result{
				Type:    tt.runType,
				BeadID:  beadID,
				Verdict: ops.VerdictFailed,
				Err:     errors.New("late process failed after replacement"),
			}

			d.handleEscalationResult(ctx, original.ID, escalationID, string(tt.escType), beadID, workerID, resultCh)

			if got := fetchOpsRunForTest(t, d.db, original.ID); got != originalBefore {
				t.Fatalf("late result changed superseded original\n got: %#v\nwant: %#v", got, originalBefore)
			}
			if got := fetchOpsRunForTest(t, d.db, replacement.ID); got != replacementBefore {
				t.Fatalf("late result changed replacement\n got: %#v\nwant: %#v", got, replacementBefore)
			}
			if got := dispatcherTestEscalationStatus(t, d.db, escalationID); got != "pending" {
				t.Fatalf("late result escalation status = %q, want pending", got)
			}
			d.mu.Lock()
			_, inCooldown := d.worktreeFailures[beadID]
			d.mu.Unlock()
			if inCooldown {
				t.Fatalf("late result changed assignment cooldown for %s", beadID)
			}
		})
	}
}

func TestRouteExistingRoutableEscalationCarriesExactOpsRunIDToCompletion(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	spawner := &slowBatchSpawner{}
	d.ops = ops.NewSpawner(spawner)

	const (
		beadID   = "oro-routed-exact-owner"
		workerID = "w-routed-exact-owner"
	)
	escalationID := insertDispatcherTestEscalation(t, d.db, protocol.EscOversizedBead, beadID, workerID)
	msg := protocol.FormatEscalation(protocol.EscOversizedBead, beadID, "needs decomposition", "")
	if err := d.routeExistingRoutableEscalation(ctx, escalationID, protocol.EscOversizedBead, beadID, workerID, msg); err != nil {
		t.Fatalf("route existing escalation: %v", err)
	}

	waitFor(t, func() bool {
		spawner.mu.Lock()
		defer spawner.mu.Unlock()
		return len(spawner.processes) == 1
	}, time.Second)
	original, err := FindBlockingOpsRun(ctx, d.db, string(ops.OpsDecompose), beadID)
	if err != nil || original == nil {
		t.Fatalf("find routed original: rec=%#v err=%v", original, err)
	}
	next := *original
	next.ID = 0
	next.Status = opsRunStatusRunning
	next.ProcessPID = 0
	next.Verdict = ""
	next.Feedback = ""
	next.Error = ""
	next.StartedAt = ""
	next.CompletedAt = ""
	replacement, created, err := replaceOpsRun(ctx, d.db, *original, next, "test replacement while routed process is live")
	if err != nil || !created {
		t.Fatalf("replace routed original: created=%t err=%v", created, err)
	}
	originalBefore := fetchOpsRunForTest(t, d.db, original.ID)
	replacementBefore := fetchOpsRunForTest(t, d.db, replacement.ID)

	spawner.mu.Lock()
	process := spawner.processes[0]
	spawner.mu.Unlock()
	if err := process.Kill(); err != nil {
		t.Fatalf("release original process: %v", err)
	}
	d.wg.Wait()

	if got := fetchOpsRunForTest(t, d.db, original.ID); got != originalBefore {
		t.Fatalf("routed late result changed original\n got: %#v\nwant: %#v", got, originalBefore)
	}
	if got := fetchOpsRunForTest(t, d.db, replacement.ID); got != replacementBefore {
		t.Fatalf("routed late result changed replacement\n got: %#v\nwant: %#v", got, replacementBefore)
	}
	if got := dispatcherTestEscalationStatus(t, d.db, escalationID); got != "pending" {
		t.Fatalf("routed late result escalation status = %q, want pending", got)
	}
	d.mu.Lock()
	_, inCooldown := d.worktreeFailures[beadID]
	d.mu.Unlock()
	if inCooldown {
		t.Fatalf("routed late result changed assignment cooldown for %s", beadID)
	}
}

func TestDispatcherStartupMarksOrphanedOpsRunsStale(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, spawnMock := newTestDispatcher(t)
	release := make(chan struct{})
	spawnMock.wait = release
	defer close(release)

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

func TestDispatcherStartupReroutedOpsRunsCompleteExactReplacement(t *testing.T) {
	tests := []struct {
		name        string
		runType     ops.Type
		output      string
		spawnErr    error
		wantStatus  string
		wantVerdict string
		wantError   string
	}{
		{name: "review resolved", runType: ops.OpsReview, output: "review complete\nVERDICT: APPROVED", wantStatus: opsRunStatusResolved, wantVerdict: string(ops.VerdictApproved)},
		{name: "review failed", runType: ops.OpsReview, spawnErr: errors.New("review spawn failed"), wantStatus: opsRunStatusFailed, wantVerdict: string(ops.VerdictFailed), wantError: "review spawn failed"},
		{name: "decompose resolved", runType: ops.OpsDecompose, output: "VERDICT: RESOLVED\ndecomposition complete", wantStatus: opsRunStatusResolved, wantVerdict: string(ops.VerdictResolved)},
		{name: "decompose failed", runType: ops.OpsDecompose, spawnErr: errors.New("decompose spawn failed"), wantStatus: opsRunStatusFailed, wantVerdict: string(ops.VerdictFailed), wantError: "decompose spawn failed"},
		{name: "write ac resolved", runType: ops.OpsWriteAC, output: "acceptance criteria written", wantStatus: opsRunStatusResolved, wantVerdict: string(ops.VerdictResolved)},
		{name: "write ac failed", runType: ops.OpsWriteAC, spawnErr: errors.New("write ac spawn failed"), wantStatus: opsRunStatusFailed, wantVerdict: string(ops.VerdictFailed), wantError: "write ac spawn failed"},
		{name: "diagnosis resolved", runType: ops.OpsDiagnosis, output: "diagnosis complete", wantStatus: opsRunStatusResolved, wantVerdict: string(ops.VerdictResolved)},
		{name: "diagnosis failed", runType: ops.OpsDiagnosis, spawnErr: errors.New("diagnosis spawn failed"), wantStatus: opsRunStatusFailed, wantVerdict: string(ops.VerdictFailed), wantError: "diagnosis spawn failed"},
		{name: "escalation resolved", runType: ops.OpsEscalation, output: "ACK: escalation complete", wantStatus: opsRunStatusResolved, wantVerdict: string(ops.VerdictResolved)},
		{name: "escalation failed", runType: ops.OpsEscalation, spawnErr: errors.New("escalation spawn failed"), wantStatus: opsRunStatusFailed, wantVerdict: string(ops.VerdictFailed), wantError: "escalation spawn failed"},
		{name: "dream resolved", runType: ops.OpsDream, output: "dream complete", wantStatus: opsRunStatusResolved, wantVerdict: string(ops.VerdictResolved)},
		{name: "dream failed", runType: ops.OpsDream, spawnErr: errors.New("dream spawn failed"), wantStatus: opsRunStatusFailed, wantVerdict: string(ops.VerdictFailed), wantError: "dream spawn failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d, _, _, _, _, spawnMock := newTestDispatcher(t)
			spawnMock.verdict = tt.output
			spawnMock.spawnErr = tt.spawnErr
			beadID := "oro-rerouted-" + strings.ReplaceAll(tt.name, " ", "-")
			workerID := "worker-" + beadID
			if tt.runType == ops.OpsReview {
				d.mu.Lock()
				d.worktreeByBead[beadID] = t.TempDir()
				d.mu.Unlock()
			}

			orphaned, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
				Type:          string(tt.runType),
				BeadID:        beadID,
				WorkerID:      workerID,
				DispatcherPID: -1,
				ProcessPID:    -1,
			})
			if err != nil {
				t.Fatalf("CreateOpsRun orphaned %s: %v", tt.runType, err)
			}
			if err := d.reconcileOpsRunsOnStartup(ctx); err != nil {
				t.Fatalf("reconcileOpsRunsOnStartup: %v", err)
			}

			var replacementID int64
			if err := d.db.QueryRowContext(ctx, `
SELECT id FROM ops_runs
WHERE type=? AND bead_id=? AND id<>?
ORDER BY id DESC LIMIT 1`, tt.runType, beadID, orphaned.ID).Scan(&replacementID); err != nil {
				t.Fatalf("query replacement %s run: %v", tt.runType, err)
			}
			waitFor(t, func() bool {
				return fetchOpsRunForTest(t, d.db, replacementID).Status == tt.wantStatus
			}, time.Second)
			replacement := fetchOpsRunForTest(t, d.db, replacementID)
			if replacement.CompletedAt == "" {
				t.Fatal("replacement completed_at is empty")
			}
			if replacement.Verdict != tt.wantVerdict {
				t.Fatalf("replacement verdict = %q, want %q", replacement.Verdict, tt.wantVerdict)
			}
			if !strings.Contains(replacement.Error, tt.wantError) {
				t.Fatalf("replacement error = %q, want substring %q", replacement.Error, tt.wantError)
			}

			if err := d.reconcileOpsRunsOnStartup(ctx); err != nil {
				t.Fatalf("second reconcileOpsRunsOnStartup: %v", err)
			}
			var runCount int
			if err := d.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM ops_runs WHERE type=? AND bead_id=?`, tt.runType, beadID,
			).Scan(&runCount); err != nil {
				t.Fatalf("count %s runs: %v", tt.runType, err)
			}
			if runCount != 2 {
				t.Fatalf("ops run count after second reconciliation = %d, want 2", runCount)
			}
		})
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

func TestRouteOpsRunRestoresExactReviewCheckpointIdentityWithoutWorker(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	const (
		beadID       = "oro-review-durable-target"
		workerID     = "w-review-durable-target"
		targetBranch = "epic/custom-review-target"
	)
	worktree := t.TempDir()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Restore durable review target",
		AcceptanceCriteria: "Test: rerouted review uses durable target | Assert: no default fallback",
	}
	assignmentID, err := d.createAssignment(ctx, beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, assignmentID); err != nil {
		t.Fatalf("requeue assignment: %v", err)
	}
	checkpoint, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, CheckpointInput{
		CheckpointKey:       "checkpoint-" + beadID,
		BeadID:              beadID,
		OriginAssignmentID:  assignmentID,
		CurrentAssignmentID: assignmentID,
		WorkerID:            workerID,
		Worktree:            worktree,
		Branch:              protocol.BranchPrefix + beadID,
		TargetBranch:        targetBranch,
		HeadSHA:             "approved-head",
		TargetSHA:           "target-before-review",
		AcceptanceHash:      "acceptance-hash",
		QGScriptHash:        "qg-script-hash",
		QGMode:              "full",
		ReviewPolicyHash:    "review-policy-hash",
		TriageRevision:      "triage-revision",
		ReadyAttempt:        "ready-attempt",
		State:               ReviewCheckpointStateReviewRunning,
	})
	if err != nil {
		t.Fatalf("create durable checkpoint: %v", err)
	}

	d.mu.Lock()
	delete(d.workers, workerID)
	delete(d.worktreeByBead, beadID)
	d.mu.Unlock()
	if !d.routeOpsRun(ctx, OpsRunRecord{
		ID:       991,
		Type:     string(ops.OpsReview),
		BeadID:   beadID,
		WorkerID: workerID,
	}) {
		t.Fatal("routeOpsRun review from durable checkpoint = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() == 1 }, time.Second)
	spawnMock.mu.Lock()
	spawn := spawnMock.spawns[0]
	spawnMock.mu.Unlock()
	if spawn.workdir != checkpoint.Worktree {
		t.Fatalf("review workdir = %q, want durable %q", spawn.workdir, checkpoint.Worktree)
	}
	if !strings.Contains(spawn.prompt, "merge to "+checkpoint.TargetBranch) {
		t.Fatalf("review prompt omitted durable target %q:\n%s", checkpoint.TargetBranch, spawn.prompt)
	}
	if strings.Contains(spawn.prompt, "merge to "+d.cfg.DefaultBranch) {
		t.Fatalf("review prompt fell back to default target %q:\n%s", d.cfg.DefaultBranch, spawn.prompt)
	}
}

func TestRouteOpsRunRestoresCheckpointLinkedToExactOpsRun(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	const (
		beadID      = "oro-review-exact-ops-checkpoint"
		workerID    = "w-review-exact-ops-checkpoint"
		exactRunID  = int64(4101)
		newerRunID  = int64(4102)
		exactTarget = "epic/exact-ops-target"
		newerTarget = "epic/newer-wrong-target"
	)
	exactWorktree := t.TempDir()
	newerWorktree := t.TempDir()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Restore exact ops checkpoint",
		AcceptanceCriteria: "Assert: rerouted review uses the checkpoint linked to its ops run",
	}
	assignmentID, err := d.createAssignment(ctx, beadID, workerID, exactWorktree)
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, assignmentID); err != nil {
		t.Fatalf("requeue assignment: %v", err)
	}
	store := NewReviewCheckpointStore(d.db)
	exactCheckpoint, err := store.CreateOrReuse(ctx, CheckpointInput{
		CheckpointKey:       "checkpoint-exact-ops-run",
		BeadID:              beadID,
		OriginAssignmentID:  assignmentID,
		CurrentAssignmentID: assignmentID,
		WorkerID:            workerID,
		Worktree:            exactWorktree,
		Branch:              protocol.BranchPrefix + beadID,
		TargetBranch:        exactTarget,
		HeadSHA:             "exact-head",
		TargetSHA:           "exact-target-before",
		AcceptanceHash:      "acceptance-hash",
		QGScriptHash:        "qg-script-hash",
		QGMode:              "full",
		ReviewPolicyHash:    "review-policy-hash",
		TriageRevision:      "triage-revision",
		ReadyAttempt:        "ready-exact",
		State:               ReviewCheckpointStateReviewRunning,
	})
	if err != nil {
		t.Fatalf("create exact checkpoint: %v", err)
	}
	newerCheckpoint, err := store.CreateOrReuse(ctx, CheckpointInput{
		CheckpointKey:       "checkpoint-newer-wrong-ops-run",
		BeadID:              beadID,
		OriginAssignmentID:  assignmentID,
		CurrentAssignmentID: assignmentID,
		WorkerID:            workerID,
		Worktree:            newerWorktree,
		Branch:              protocol.BranchPrefix + beadID,
		TargetBranch:        newerTarget,
		HeadSHA:             "newer-head",
		TargetSHA:           "newer-target-before",
		AcceptanceHash:      "acceptance-hash",
		QGScriptHash:        "qg-script-hash",
		QGMode:              "full",
		ReviewPolicyHash:    "review-policy-hash",
		TriageRevision:      "triage-revision",
		ReadyAttempt:        "ready-newer",
		State:               ReviewCheckpointStateReviewRunning,
	})
	if err != nil {
		t.Fatalf("create newer checkpoint: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE review_checkpoints SET ops_run_id=? WHERE id=?`, exactRunID, exactCheckpoint.ID); err != nil {
		t.Fatalf("link exact checkpoint: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE review_checkpoints SET ops_run_id=? WHERE id=?`, newerRunID, newerCheckpoint.ID); err != nil {
		t.Fatalf("link newer checkpoint: %v", err)
	}

	if !d.routeOpsRun(ctx, OpsRunRecord{ID: exactRunID, Type: string(ops.OpsReview), BeadID: beadID, WorkerID: workerID}) {
		t.Fatal("route exact review ops run = false, want true")
	}
	waitFor(t, func() bool { return spawnMock.SpawnCount() == 1 }, time.Second)
	spawnMock.mu.Lock()
	spawn := spawnMock.spawns[0]
	spawnMock.mu.Unlock()
	if spawn.workdir != exactWorktree {
		t.Fatalf("review workdir = %q, want exact linked %q (newest-by-bead was %q)", spawn.workdir, exactWorktree, newerWorktree)
	}
	if !strings.Contains(spawn.prompt, "merge to "+exactTarget) || strings.Contains(spawn.prompt, "merge to "+newerTarget) {
		t.Fatalf("review prompt did not use exact linked target %q:\n%s", exactTarget, spawn.prompt)
	}
}

func TestRouteOpsRunFailsClosedForAmbiguousLegacyCheckpointOwnership(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	const beadID = "oro-review-ambiguous-legacy-checkpoints"
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Reject ambiguous checkpoint ownership",
		AcceptanceCriteria: "Assert: an ops run never guesses between checkpoints",
	}
	assignmentID, err := d.createAssignment(ctx, beadID, "worker-ambiguous", t.TempDir())
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	first := seedDurableReviewCheckpoint(t, d, beadID, assignmentID, t.TempDir(), ReviewCheckpointStateReviewRunning)
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO review_checkpoints (
  checkpoint_key, bead_id, origin_assignment_id, current_assignment_id, worker_id,
  worktree, branch, target_branch, head_sha, target_sha, acceptance_hash,
  qg_script_hash, qg_mode, review_policy_hash, triage_revision, ready_attempt, state
)
SELECT checkpoint_key || '-ambiguous', bead_id, origin_assignment_id, current_assignment_id, worker_id,
       ?, branch, 'epic/ambiguous-other-target', head_sha || '-other', target_sha,
       acceptance_hash, qg_script_hash, qg_mode, review_policy_hash, triage_revision,
       ready_attempt || '-other', state
FROM review_checkpoints WHERE id=?`, t.TempDir(), first.ID); err != nil {
		t.Fatalf("seed ambiguous checkpoint: %v", err)
	}

	if d.routeOpsRun(ctx, OpsRunRecord{ID: 4201, Type: string(ops.OpsReview), BeadID: beadID, WorkerID: "worker-ambiguous"}) {
		t.Fatal("route ambiguous review ops run = true, want fail closed")
	}
	if got := spawnMock.SpawnCount(); got != 0 {
		t.Fatalf("review spawns = %d, want zero", got)
	}
	var linked int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_checkpoints WHERE bead_id=? AND ops_run_id IS NOT NULL`, beadID).
		Scan(&linked); err != nil {
		t.Fatalf("count linked checkpoints: %v", err)
	}
	if linked != 0 {
		t.Fatalf("legacy checkpoints linked = %d, want zero under ambiguity", linked)
	}
}

func TestSupersedeOpsReviewRetryPreservesContext(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	release := make(chan struct{})
	spawnMock.wait = release
	defer close(release)
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
	release := make(chan struct{})
	spawnMock.wait = release
	defer close(release)
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
	release := make(chan struct{})
	spawnMock.wait = release
	defer close(release)
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
