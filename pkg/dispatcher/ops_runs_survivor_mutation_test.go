package dispatcher //nolint:testpackage // white-box mutation tests exercise durable ops-run edges

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/storage"
)

type opsSurvivorMutationStore struct {
	db         *sql.DB
	result     sql.Result
	err        error
	queryCalls *int
}

func (s opsSurvivorMutationStore) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return s.result, s.err
}

func (s opsSurvivorMutationStore) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if s.queryCalls != nil {
		*s.queryCalls++
	}
	return s.db.QueryRowContext(ctx, query, args...)
}

func TestOpsSurvivorMutationCreateEdgeOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid status returns validation error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, created, err := createOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-invalid", Status: "corrupt",
		})
		if err == nil || created || !strings.Contains(err.Error(), `invalid ops run status "corrupt"`) {
			t.Fatalf("invalid create result = created %v err %v", created, err)
		}
	})

	t.Run("insert error returns before reading result", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, created, err := createOpsRun(ctx, opsRunMutationStore{
			db: d.db, err: errors.New("injected create rejection"),
		}, OpsRunRecord{Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-rejected"})
		if err == nil || created || !strings.Contains(err.Error(), "injected create rejection") {
			t.Fatalf("rejected create result = created %v err %v", created, err)
		}
	})

	t.Run("nonunique error returns without owner lookup", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		queryCalls := 0
		_, created, err := createOpsRun(ctx, opsSurvivorMutationStore{
			db: d.db, err: errors.New("injected nonunique rejection"), queryCalls: &queryCalls,
		}, OpsRunRecord{Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-nonunique"})
		if err == nil || created {
			t.Fatalf("nonunique result = created %v err %v", created, err)
		}
		if queryCalls != 0 {
			t.Fatalf("blocking owner lookups = %d, want 0 for nonunique error", queryCalls)
		}
	})

	t.Run("last insert id error is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, created, err := createOpsRun(ctx, opsSurvivorMutationStore{
			db: d.db, result: opsRunMutationResult{idErr: errors.New("injected survivor last id failure")},
		}, OpsRunRecord{Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-last-id"})
		if err == nil || created || !strings.Contains(err.Error(), "injected survivor last id failure") {
			t.Fatalf("last id result = created %v err %v", created, err)
		}
	})

	t.Run("load error after insert is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := d.db.ExecContext(ctx, `DROP TABLE ops_runs`); err != nil {
			t.Fatalf("drop ops_runs: %v", err)
		}
		_, created, err := createOpsRun(ctx, opsSurvivorMutationStore{
			db: d.db, result: opsRunMutationResult{id: 71},
		}, OpsRunRecord{Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-load"})
		if err == nil || created || !strings.Contains(err.Error(), "ops_runs") {
			t.Fatalf("load result = created %v err %v", created, err)
		}
	})

	t.Run("blocking duplicate returns exact owner", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		original, created, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-owner", WorkerID: "owner",
		})
		if err != nil || !created {
			t.Fatalf("create owner = created %v err %v", created, err)
		}
		got, created, err := createOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: original.BeadID, WorkerID: "contender",
		})
		if err != nil || created || got.ID != original.ID || got.WorkerID != original.WorkerID {
			t.Fatalf("duplicate result = %+v created %v err %v, want owner %+v", got, created, err, original)
		}
	})

	t.Run("unrelated unique collision cannot invent blocking owner", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		seed, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-index-seed",
		})
		if err != nil {
			t.Fatalf("create index seed: %v", err)
		}
		if err := CompleteOpsRun(ctx, d.db, seed.ID, opsRunStatusResolved, "", "", ""); err != nil {
			t.Fatalf("resolve index seed: %v", err)
		}
		if _, err := d.db.ExecContext(ctx, `CREATE UNIQUE INDEX ops_mut_constant_unique ON ops_runs((1))`); err != nil {
			t.Fatalf("create constant unique index: %v", err)
		}
		_, created, err := createOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-no-owner",
		})
		if err == nil || created || !strings.Contains(err.Error(), "create ops run") {
			t.Fatalf("unique collision result = created %v err %v", created, err)
		}
	})
}

func TestOpsSurvivorMutationCompleteEdgeOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid expected status is rejected first", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, err := completeOpsRunFromStatus(ctx, d.db, 41, opsRunStatusResolved,
			opsRunStatusFailed, "", "", "")
		if err == nil || !strings.Contains(err.Error(), "invalid expected status") {
			t.Fatalf("invalid expected status error = %v", err)
		}
	})

	t.Run("invalid completion status is rejected", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, err := completeOpsRunFromStatus(ctx, d.db, 42, opsRunStatusRunning,
			opsRunStatusRunning, "", "", "")
		if err == nil || !strings.Contains(err.Error(), "invalid ops run completion status") {
			t.Fatalf("invalid completion status error = %v", err)
		}
	})

	t.Run("update error is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, err := completeOpsRunFromStatus(ctx, opsRunMutationStore{
			db: d.db, err: errors.New("injected completion update failure"),
		}, 43, opsRunStatusRunning, opsRunStatusResolved, "", "", "")
		if err == nil || !strings.Contains(err.Error(), "injected completion update failure") {
			t.Fatalf("completion update error = %v", err)
		}
	})

	t.Run("rows affected error is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, err := completeOpsRunFromStatus(ctx, opsSurvivorMutationStore{
			db: d.db, result: opsRunMutationResult{affectedErr: errors.New("injected survivor affected failure")},
		}, 44, opsRunStatusRunning, opsRunStatusResolved, "", "", "")
		if err == nil || !strings.Contains(err.Error(), "injected survivor affected failure") {
			t.Fatalf("rows affected error = %v", err)
		}
	})

	t.Run("missing row is reported", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, err := completeOpsRunFromStatus(ctx, d.db, 999999, opsRunStatusRunning,
			opsRunStatusResolved, "", "", "")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("missing completion row error = %v", err)
		}
	})

	t.Run("reload error is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := d.db.ExecContext(ctx, `DROP TABLE ops_runs`); err != nil {
			t.Fatalf("drop ops_runs: %v", err)
		}
		_, err := completeOpsRunFromStatus(ctx, opsSurvivorMutationStore{
			db: d.db, result: opsRunMutationResult{affected: 0},
		}, 45, opsRunStatusRunning, opsRunStatusResolved, "", "", "")
		if err == nil || !strings.Contains(err.Error(), "ops_runs") {
			t.Fatalf("completion reload error = %v", err)
		}
	})

	t.Run("exact replay reports replay ownership", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-complete-replay",
		})
		if err != nil {
			t.Fatalf("create replay run: %v", err)
		}
		if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusResolved, "resolved", "done", ""); err != nil {
			t.Fatalf("complete replay run: %v", err)
		}
		outcome, err := completeOpsRunFromStatus(ctx, d.db, rec.ID, opsRunStatusRunning,
			opsRunStatusResolved, "resolved", "done", "")
		if err != nil || outcome != opsRunCompletionExactReplay {
			t.Fatalf("exact replay outcome = %v err %v, want %v", outcome, err, opsRunCompletionExactReplay)
		}
	})

	for _, mismatch := range []struct {
		name                                 string
		status, verdict, feedback, errorText string
	}{
		{name: "status", status: opsRunStatusFailed, verdict: "verdict", feedback: "feedback", errorText: "error"},
		{name: "verdict", status: opsRunStatusResolved, verdict: "different", feedback: "feedback", errorText: "error"},
		{name: "feedback", status: opsRunStatusResolved, verdict: "verdict", feedback: "different", errorText: "error"},
		{name: "error", status: opsRunStatusResolved, verdict: "verdict", feedback: "feedback", errorText: "different"},
	} {
		t.Run("replay mismatch "+mismatch.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
				Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-complete-mismatch-" + mismatch.name,
			})
			if err != nil {
				t.Fatalf("create mismatch run: %v", err)
			}
			if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusResolved, "verdict", "feedback", "error"); err != nil {
				t.Fatalf("complete mismatch fixture: %v", err)
			}
			if _, err := completeOpsRunFromStatus(ctx, d.db, rec.ID, opsRunStatusRunning,
				mismatch.status, mismatch.verdict, mismatch.feedback, mismatch.errorText); err == nil {
				t.Fatalf("%s mismatch accepted as exact replay", mismatch.name)
			}
		})
	}
}

func TestOpsSurvivorMutationApplyResolveEdgeOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid arguments", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := d.applyOpsResolve(""); err == nil || !strings.Contains(err.Error(), "requires an ops run ID and reason") {
			t.Fatalf("invalid resolve arguments error = %v", err)
		}
	})

	t.Run("missing run", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := d.applyOpsResolve("999999 operator checked"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("missing resolve run error = %v", err)
		}
	})

	t.Run("load failure", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := d.db.ExecContext(ctx, `DROP TABLE ops_runs`); err != nil {
			t.Fatalf("drop ops_runs: %v", err)
		}
		if _, err := d.applyOpsResolve("1 operator checked"); err == nil || !strings.Contains(err.Error(), "ops_runs") {
			t.Fatalf("resolve load error = %v", err)
		}
	})

	t.Run("invalid durable source status", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-resolve-corrupt",
		})
		if err != nil {
			t.Fatalf("create corrupt-status fixture: %v", err)
		}
		if _, err := d.db.ExecContext(ctx, `UPDATE ops_runs SET status='corrupt' WHERE id=?`, rec.ID); err != nil {
			t.Fatalf("corrupt source status: %v", err)
		}
		if _, err := d.applyOpsResolve(fmt.Sprintf("%d operator checked", rec.ID)); err == nil ||
			!strings.Contains(err.Error(), "invalid expected status") {
			t.Fatalf("corrupt source status error = %v", err)
		}
	})

	t.Run("resolved replay remains explicitly resolved", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-resolve-replay",
		})
		if err != nil {
			t.Fatalf("create resolved replay: %v", err)
		}
		if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusResolved, "", "", "operator checked"); err != nil {
			t.Fatalf("resolve replay fixture: %v", err)
		}
		detail, err := d.applyOpsResolve(fmt.Sprintf("%d checked again", rec.ID))
		if err != nil {
			t.Fatalf("resolved replay: %v", err)
		}
		var response opsResolveResponse
		if err := json.Unmarshal([]byte(detail), &response); err != nil {
			t.Fatalf("decode resolved replay: %v", err)
		}
		if !response.Resolved || response.ID != rec.ID || response.Status != opsRunStatusResolved {
			t.Fatalf("resolved replay response = %+v", response)
		}
	})

	t.Run("invalid decompose result preserves durable blockers", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const beadID = "oro-mut-resolve-invalid-decompose"
		insertDispatcherTestBead(t, d.db, beadID, "task", "", tddAcceptanceForTest())
		rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{Type: string(ops.OpsDecompose), BeadID: beadID})
		if err != nil {
			t.Fatalf("create invalid decompose run: %v", err)
		}
		if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusFailed, "failed", "", "needs children"); err != nil {
			t.Fatalf("fail invalid decompose run: %v", err)
		}
		if _, err := d.applyOpsResolve(fmt.Sprintf("%d operator checked", rec.ID)); err == nil {
			t.Fatal("invalid decompose resolve error = nil")
		}
		if got := fetchOpsRunForTest(t, d.db, rec.ID); got.Status != opsRunStatusFailed {
			t.Fatalf("invalid decompose status = %q, want failed", got.Status)
		}
	})

	t.Run("successful resolve updates response escalation and cooldown", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const beadID = "oro-mut-resolve-side-effects"
		const workerID = "worker-resolve-side-effects"
		escalationID := insertDispatcherTestEscalation(t, d.db, protocol.EscStuckWorker, beadID, workerID)
		rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			EscalationID: escalationID, Type: string(ops.OpsDiagnosis), BeadID: beadID, WorkerID: workerID,
		})
		if err != nil {
			t.Fatalf("create resolve side-effect run: %v", err)
		}
		if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusFailed, "failed", "", "operator required"); err != nil {
			t.Fatalf("fail resolve side-effect run: %v", err)
		}
		d.mu.Lock()
		d.worktreeFailures[beadID] = time.Now()
		d.mu.Unlock()

		detail, err := d.applyOpsResolve(fmt.Sprintf("%d operator accepted", rec.ID))
		if err != nil {
			t.Fatalf("resolve side-effect run: %v", err)
		}
		var response opsResolveResponse
		if err := json.Unmarshal([]byte(detail), &response); err != nil {
			t.Fatalf("decode resolve response: %v", err)
		}
		if !response.Resolved || response.Status != opsRunStatusResolved || response.ID != rec.ID {
			t.Fatalf("resolve response = %+v", response)
		}
		if got := dispatcherTestEscalationStatus(t, d.db, escalationID); got != "acked" {
			t.Fatalf("escalation status = %q, want acked", got)
		}
		d.mu.Lock()
		_, stillCoolingDown := d.worktreeFailures[beadID]
		d.mu.Unlock()
		if stillCoolingDown {
			t.Fatal("assignment cooldown remains after successful resolve")
		}
	})
}

func TestOpsSurvivorMutationRetryEdgeOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("nonblocking lookup error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if err := d.db.Close(); err != nil {
			t.Fatalf("close dispatcher db: %v", err)
		}
		_, _, err := d.supersedeOpsRunForRetry(OpsRunRecord{
			ID: 77, Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-retry-closed", Status: opsRunStatusResolved,
		})
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("nonblocking lookup error = %v", err)
		}
	})

	t.Run("replacement failure is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		rec := opsSurvivorFailedRun(t, d, ops.OpsDiagnosis, "oro-mut-retry-replace-error", "diagnosis failed")
		installOpsRunInsertFailureTrigger(ctx, t, d.db, rec.BeadID)
		if _, _, err := d.supersedeOpsRunForRetry(rec); err == nil ||
			!strings.Contains(err.Error(), "injected replacement insert failure") {
			t.Fatalf("replacement failure error = %v", err)
		}
	})

	t.Run("nonretryable run reports existing blocking owner", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		blocking, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-retry-blocking-owner",
		})
		if err != nil {
			t.Fatalf("create blocking owner: %v", err)
		}
		_, _, err = d.supersedeOpsRunForRetry(OpsRunRecord{
			ID: 707, Type: blocking.Type, BeadID: blocking.BeadID, Status: opsRunStatusResolved,
		})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("blocking ops run %d", blocking.ID)) {
			t.Fatalf("blocking owner error = %v", err)
		}
	})

	t.Run("replacement collision reports owner and rolls back current", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.ops = nil
		current := opsSurvivorFailedRun(t, d, ops.OpsDiagnosis,
			"oro-mut-retry-collision-current", "current incident")
		owner, created, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-retry-collision-owner",
		})
		if err != nil || !created {
			t.Fatalf("create collision owner = created %v err %v", created, err)
		}
		synthetic := current
		synthetic.Type = owner.Type
		synthetic.BeadID = owner.BeadID

		_, routed, err := d.supersedeOpsRunForRetry(synthetic)
		if err == nil || routed || !strings.Contains(err.Error(), fmt.Sprintf("blocking ops run %d", owner.ID)) {
			t.Fatalf("replacement collision = routed %v err %v", routed, err)
		}
		if got := fetchOpsRunForTest(t, d.db, current.ID); got.Status != opsRunStatusFailed {
			t.Fatalf("current status after collision = %q, want failed", got.Status)
		}
		if got := fetchOpsRunForTest(t, d.db, owner.ID); got.Status != opsRunStatusRunning {
			t.Fatalf("owner status after collision = %q, want running", got.Status)
		}
	})

	t.Run("ordinary retry resets durable execution fields and cooldown", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.ops = nil
		const beadID = "oro-mut-retry-normalized"
		rec := opsSurvivorFailedRun(t, d, ops.OpsDiagnosis, beadID, "diagnosis incident")
		if _, err := d.db.ExecContext(ctx, `UPDATE ops_runs SET dispatcher_pid=-77, process_pid=-88 WHERE id=?`, rec.ID); err != nil {
			t.Fatalf("seed stale process identity: %v", err)
		}
		rec = fetchOpsRunForTest(t, d.db, rec.ID)
		d.mu.Lock()
		d.worktreeFailures[beadID] = time.Now()
		d.mu.Unlock()
		replacement, routed, err := d.supersedeOpsRunForRetry(rec)
		if err != nil || routed {
			t.Fatalf("ordinary retry result = routed %v err %v", routed, err)
		}
		if replacement.Status != opsRunStatusRunning || replacement.DispatcherPID != os.Getpid() ||
			replacement.ProcessPID != 0 || replacement.Verdict != "" || replacement.Feedback != "" ||
			replacement.Error != fmt.Sprintf("manual retry of ops run %d", rec.ID) {
			t.Fatalf("ordinary replacement not normalized: %+v", replacement)
		}
		d.mu.Lock()
		_, stillCoolingDown := d.worktreeFailures[beadID]
		d.mu.Unlock()
		if stillCoolingDown {
			t.Fatal("assignment cooldown remains after ordinary retry")
		}
	})

	for _, runType := range []ops.Type{ops.OpsDecompose, ops.OpsEscalation} {
		t.Run("preserves incident and fills routing identity "+string(runType), func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			d.ops = nil
			incident := "original " + string(runType) + " incident"
			rec := opsSurvivorFailedRun(t, d, runType, "oro-mut-retry-"+string(runType), incident)
			if _, err := d.db.ExecContext(ctx, `UPDATE ops_runs SET runtime='', model='' WHERE id=?`, rec.ID); err != nil {
				t.Fatalf("clear runtime/model: %v", err)
			}
			rec = fetchOpsRunForTest(t, d.db, rec.ID)
			replacement, routed, err := d.supersedeOpsRunForRetry(rec)
			if err != nil || routed {
				t.Fatalf("retry result = routed %v err %v", routed, err)
			}
			if replacement.ID == rec.ID || replacement.Error != incident ||
				replacement.Runtime == "" || replacement.Model == "" {
				t.Fatalf("replacement identity/context = %+v", replacement)
			}
		})
	}
}

func opsSurvivorFailedRun(t *testing.T, d *Dispatcher, runType ops.Type, beadID, incident string) OpsRunRecord {
	t.Helper()
	ctx := context.Background()
	rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{Type: string(runType), BeadID: beadID, Error: incident})
	if err != nil {
		t.Fatalf("create failed-run fixture: %v", err)
	}
	if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusFailed, "failed", "old feedback", incident); err != nil {
		t.Fatalf("fail run fixture: %v", err)
	}
	return fetchOpsRunForTest(t, d.db, rec.ID)
}

func TestOpsSurvivorMutationStartupRerouteNormalizationAndAudit(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.ops = nil
	const beadID = "oro-mut-startup-reroute"
	original, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type: string(ops.OpsDiagnosis), BeadID: beadID, WorkerID: "worker-old",
		DispatcherPID: -71, ProcessPID: -72, Verdict: "old verdict", Feedback: "old feedback", Error: "old error",
	})
	if err != nil {
		t.Fatalf("create startup reroute fixture: %v", err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE ops_runs SET runtime='', model='' WHERE id=?`, original.ID); err != nil {
		t.Fatalf("clear startup runtime/model: %v", err)
	}
	original = fetchOpsRunForTest(t, d.db, original.ID)
	if err := d.supersedeAndRerouteOpsRun(ctx, original); err != nil {
		t.Fatalf("supersede and reroute: %v", err)
	}

	var replacementID int64
	if err := d.db.QueryRowContext(ctx, `SELECT id FROM ops_runs WHERE bead_id=? AND id<>?`, beadID, original.ID).
		Scan(&replacementID); err != nil {
		t.Fatalf("load startup replacement id: %v", err)
	}
	replacement := fetchOpsRunForTest(t, d.db, replacementID)
	if replacement.ID == original.ID || replacement.Status != opsRunStatusFailed ||
		replacement.DispatcherPID != os.Getpid() || replacement.ProcessPID != 0 ||
		replacement.Verdict != "" || replacement.Feedback != "" ||
		replacement.Runtime == "" || replacement.Model == "" || replacement.CompletedAt == "" ||
		!strings.Contains(replacement.Error, "could not be routed") {
		t.Fatalf("startup replacement not normalized/failed durably: %+v", replacement)
	}
	if got := fetchOpsRunForTest(t, d.db, original.ID); got.Status != opsRunStatusSuperseded {
		t.Fatalf("original status = %q, want superseded", got.Status)
	}
	var payload string
	if err := d.db.QueryRowContext(ctx, `SELECT payload FROM events WHERE type='ops_run_superseded' AND bead_id=?`, beadID).
		Scan(&payload); err != nil {
		t.Fatalf("load supersede event: %v", err)
	}
	for _, want := range []string{
		fmt.Sprintf(`"ops_run_id":%d`, original.ID),
		fmt.Sprintf(`"new_ops_run_id":%d`, replacement.ID),
		`"type":"diagnosis"`,
		`"routed":false`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("supersede event %q missing %q", payload, want)
		}
	}

	t.Run("replacement error is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		orphaned, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-startup-replace-error", DispatcherPID: -1, ProcessPID: -1,
		})
		if err != nil {
			t.Fatalf("create replacement-error fixture: %v", err)
		}
		installOpsRunInsertFailureTrigger(ctx, t, d.db, orphaned.BeadID)
		if err := d.supersedeAndRerouteOpsRun(ctx, orphaned); err == nil ||
			!strings.Contains(err.Error(), "injected replacement insert failure") {
			t.Fatalf("startup replacement error = %v", err)
		}
	})

	t.Run("replacement collision leaves existing owner untouched", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.ops = nil
		current, created, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-startup-collision-current",
		})
		if err != nil || !created {
			t.Fatalf("create collision current = created %v err %v", created, err)
		}
		owner, created, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-startup-collision-owner",
		})
		if err != nil || !created {
			t.Fatalf("create collision owner = created %v err %v", created, err)
		}
		synthetic := current
		synthetic.Type = owner.Type
		synthetic.BeadID = owner.BeadID

		if err := d.supersedeAndRerouteOpsRun(ctx, synthetic); err != nil {
			t.Fatalf("replacement collision: %v", err)
		}
		if got := fetchOpsRunForTest(t, d.db, current.ID); got.Status != opsRunStatusRunning {
			t.Fatalf("current status after collision = %q, want running", got.Status)
		}
		if got := fetchOpsRunForTest(t, d.db, owner.ID); got.Status != opsRunStatusRunning {
			t.Fatalf("owner status after collision = %q, want running", got.Status)
		}
	})

	t.Run("unroutable terminal write error is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.ops = nil
		orphaned, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-startup-terminal-error", DispatcherPID: -1, ProcessPID: -1,
		})
		if err != nil {
			t.Fatalf("create terminal-error fixture: %v", err)
		}
		if _, err := d.db.ExecContext(ctx, fmt.Sprintf(`
CREATE TRIGGER fail_ops_mut_terminal_update
BEFORE UPDATE OF status ON ops_runs
WHEN OLD.id <> %d AND NEW.status = 'failed'
BEGIN
    SELECT RAISE(FAIL, 'injected survivor terminal failure');
END`, orphaned.ID)); err != nil {
			t.Fatalf("create terminal failure trigger: %v", err)
		}
		if err := d.supersedeAndRerouteOpsRun(ctx, orphaned); err == nil ||
			!strings.Contains(err.Error(), "injected survivor terminal failure") {
			t.Fatalf("startup terminal write error = %v", err)
		}
	})

	t.Run("routed replacement is normalized before result", func(t *testing.T) {
		d, _, _, _, _, spawnMock := newTestDispatcher(t)
		release := make(chan struct{})
		spawnMock.wait = release
		defer func() {
			close(release)
			d.wg.Wait()
		}()
		const beadID = "oro-mut-startup-pending-route"
		orphaned, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: beadID, WorkerID: "old-worker",
			DispatcherPID: -81, ProcessPID: -82, Verdict: "old verdict", Feedback: "old feedback", Error: "old error",
		})
		if err != nil {
			t.Fatalf("create pending-route fixture: %v", err)
		}
		if _, err := d.db.ExecContext(ctx, `UPDATE ops_runs SET runtime='', model='' WHERE id=?`, orphaned.ID); err != nil {
			t.Fatalf("clear pending-route runtime/model: %v", err)
		}
		orphaned = fetchOpsRunForTest(t, d.db, orphaned.ID)
		if err := d.supersedeAndRerouteOpsRun(ctx, orphaned); err != nil {
			t.Fatalf("route pending replacement: %v", err)
		}
		replacement, err := FindBlockingOpsRun(ctx, d.db, orphaned.Type, beadID)
		if err != nil || replacement == nil {
			t.Fatalf("load pending replacement = %+v err %v", replacement, err)
		}
		if replacement.ID == orphaned.ID || replacement.Status != opsRunStatusRunning ||
			replacement.DispatcherPID != os.Getpid() || replacement.ProcessPID != 0 ||
			replacement.Verdict != "" || replacement.Feedback != "" || replacement.Error != "" ||
			replacement.Runtime == "" || replacement.Model == "" {
			t.Fatalf("pending routed replacement not normalized: %+v", replacement)
		}
	})
}

func TestOpsSurvivorMutationReplaceEdgeOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("begin transaction error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if err := d.db.Close(); err != nil {
			t.Fatalf("close replacement db: %v", err)
		}
		_, _, err := replaceOpsRun(ctx, d.db,
			OpsRunRecord{ID: 1, Status: opsRunStatusRunning}, OpsRunRecord{}, "replace")
		if err == nil || !strings.Contains(err.Error(), "begin ops run replacement") {
			t.Fatalf("begin transaction error = %v", err)
		}
	})

	t.Run("completion validation error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, _, err := replaceOpsRun(ctx, d.db,
			OpsRunRecord{ID: 2, Status: "corrupt"}, OpsRunRecord{}, "replace")
		if err == nil || !strings.Contains(err.Error(), "invalid expected status") {
			t.Fatalf("replacement completion error = %v", err)
		}
	})

	t.Run("exact replay does not acquire replacement ownership", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		current, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-replace-replay",
		})
		if err != nil {
			t.Fatalf("create replay owner: %v", err)
		}
		const reason = "replacement already completed"
		if err := CompleteOpsRun(ctx, d.db, current.ID, opsRunStatusSuperseded,
			current.Verdict, current.Feedback, reason); err != nil {
			t.Fatalf("prepare exact replay: %v", err)
		}
		next := current
		next.ID = 0
		_, _, err = replaceOpsRun(ctx, d.db, current, next, reason)
		if err == nil || !strings.Contains(err.Error(), "completion ownership was not acquired") {
			t.Fatalf("exact replay replacement error = %v", err)
		}
	})

	t.Run("replacement insert error is returned", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		current, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-replace-insert-error",
		})
		if err != nil {
			t.Fatalf("create insert-error owner: %v", err)
		}
		installOpsRunInsertFailureTrigger(ctx, t, d.db, current.BeadID)
		next := current
		next.ID = 0
		_, _, err = replaceOpsRun(ctx, d.db, current, next, "replacement insert must fail")
		if err == nil || !strings.Contains(err.Error(), "injected replacement insert failure") {
			t.Fatalf("replacement insert error = %v", err)
		}
	})

	t.Run("blocking next owner is returned without commit", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		current, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-replace-current",
		})
		if err != nil {
			t.Fatalf("create current owner: %v", err)
		}
		existing, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsReview), BeadID: "oro-mut-replace-existing", WorkerID: "existing",
		})
		if err != nil {
			t.Fatalf("create next owner: %v", err)
		}
		next := existing
		next.ID = 0
		next.WorkerID = "contender"
		got, created, err := replaceOpsRun(ctx, d.db, current, next, "try conflicting replacement")
		if err != nil || created || got.ID != existing.ID {
			t.Fatalf("blocking replacement = %+v created %v err %v", got, created, err)
		}
		if got := fetchOpsRunForTest(t, d.db, current.ID); got.Status != opsRunStatusRunning {
			t.Fatalf("rolled-back current status = %q, want running", got.Status)
		}
	})

	t.Run("checkpoint relink error rolls back", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		current, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-replace-relink",
		})
		if err != nil {
			t.Fatalf("create relink owner: %v", err)
		}
		if _, err := d.db.ExecContext(ctx, `DROP TABLE review_checkpoints`); err != nil {
			t.Fatalf("drop review checkpoints: %v", err)
		}
		next := current
		next.ID = 0
		_, _, err = replaceOpsRun(ctx, d.db, current, next, "relink must fail")
		if err == nil || !strings.Contains(err.Error(), "relink review checkpoint") {
			t.Fatalf("checkpoint relink error = %v", err)
		}
		if got := fetchOpsRunForTest(t, d.db, current.ID); got.Status != opsRunStatusRunning {
			t.Fatalf("current after relink rollback = %q, want running", got.Status)
		}
	})
}

func TestOpsSurvivorMutationRouteReviewAdmissionAndCallback(t *testing.T) {
	ctx := context.Background()

	t.Run("missing review context blocks route", func(t *testing.T) {
		d, _, _, _, _, spawnMock := newTestDispatcher(t)
		if d.routeReviewOpsRun(ctx, OpsRunRecord{
			Type: string(ops.OpsReview), BeadID: "oro-mut-review-no-context", WorkerID: "worker-no-context",
		}) {
			t.Fatal("route without review context = true")
		}
		if got := spawnMock.SpawnCount(); got != 0 {
			t.Fatalf("review spawns without context = %d", got)
		}
	})

	t.Run("storage observation failure blocks route", func(t *testing.T) {
		d, _, _, _, _, spawnMock := newTestDispatcher(t)
		opsSurvivorSeedReviewWorker(t, d, "oro-mut-review-observe", "worker-observe")
		d.cfg.StorageController = &storage.Controller{}
		if d.routeReviewOpsRun(ctx, OpsRunRecord{Type: string(ops.OpsReview), BeadID: "oro-mut-review-observe", WorkerID: "worker-observe"}) {
			t.Fatal("route with observation failure = true")
		}
		if got := spawnMock.SpawnCount(); got != 0 {
			t.Fatalf("review spawns after observation failure = %d", got)
		}
	})

	t.Run("closed storage admission blocks route", func(t *testing.T) {
		d, _, _, _, _, spawnMock := newTestDispatcher(t)
		opsSurvivorSeedReviewWorker(t, d, "oro-mut-review-paused", "worker-paused")
		d.cfg.StorageController = opsSurvivorPausedStorageController(t)
		if d.routeReviewOpsRun(ctx, OpsRunRecord{Type: string(ops.OpsReview), BeadID: "oro-mut-review-paused", WorkerID: "worker-paused"}) {
			t.Fatal("route while storage admission closed = true")
		}
		if got := spawnMock.SpawnCount(); got != 0 {
			t.Fatalf("review spawns while admission closed = %d", got)
		}
	})

	t.Run("nil worktree manager blocks valid context", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		opsSurvivorSeedReviewWorker(t, d, "oro-mut-review-nil-worktrees", "worker-nil-worktrees")
		d.worktrees = nil
		if d.routeReviewOpsRun(ctx, OpsRunRecord{Type: string(ops.OpsReview), BeadID: "oro-mut-review-nil-worktrees", WorkerID: "worker-nil-worktrees"}) {
			t.Fatal("route with nil worktree manager = true")
		}
	})

	t.Run("missing worktree blocks valid context", func(t *testing.T) {
		d, _, worktrees, _, _, spawnMock := newTestDispatcher(t)
		opsSurvivorSeedReviewWorker(t, d, "oro-mut-review-missing-worktree", "worker-missing-worktree")
		worktrees.existsFn = func(context.Context, string) bool { return false }
		if d.routeReviewOpsRun(ctx, OpsRunRecord{Type: string(ops.OpsReview), BeadID: "oro-mut-review-missing-worktree", WorkerID: "worker-missing-worktree"}) {
			t.Fatal("route with absent worktree = true")
		}
		if got := spawnMock.SpawnCount(); got != 0 {
			t.Fatalf("review spawns for absent worktree = %d", got)
		}
	})

	t.Run("terminal result reaches assignment handler", func(t *testing.T) {
		d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
		const beadID = "oro-mut-review-callback"
		const workerID = "worker-callback"
		opsSurvivorSeedReviewWorker(t, d, beadID, workerID)
		beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Title: "Callback review"}
		spawnMock.spawnErr = errors.New("injected rerouted review failure")
		rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
			Type: string(ops.OpsReview), BeadID: beadID, WorkerID: workerID,
		})
		if err != nil {
			t.Fatalf("create callback run: %v", err)
		}
		if !d.routeReviewOpsRun(ctx, rec) {
			t.Fatal("route callback review = false")
		}
		d.wg.Wait()
		if got := fetchOpsRunForTest(t, d.db, rec.ID); got.Status != opsRunStatusFailed {
			t.Fatalf("callback ops run status = %q, want failed", got.Status)
		}
		if got := eventCount(t, d.db, "review_failed"); got != 1 {
			t.Fatalf("review callback error events = %d, want 1", got)
		}
	})
}

func opsSurvivorSeedReviewWorker(t *testing.T, d *Dispatcher, beadID, workerID string) {
	t.Helper()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id: workerID, beadID: beadID, state: protocol.WorkerReviewing,
		worktree: t.TempDir(), targetBranch: "epic/ops-survivor", assignmentID: 701,
	}
	d.mu.Unlock()
}

func opsSurvivorPausedStorageController(t *testing.T) *storage.Controller {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open storage catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	if err := catalog.UpsertController(ctx, storage.Controller{
		ID: "dispatcher", OwnerID: "ops-survivor-test", PID: 701, ProcessStart: now.Add(-time.Minute), HeartbeatAt: now,
		Identity: storage.ProcessIdentity{PID: 701, StartMarker: "start", Executable: "oro", ProcessGroup: 701},
	}); err != nil {
		t.Fatalf("register storage controller: %v", err)
	}
	if _, err := storage.NewPauseEpochProtocol(catalog, nil).RequestPause(ctx, now); err != nil {
		t.Fatalf("request storage pause: %v", err)
	}
	controller, err := storage.NewController(storage.ControllerConfig{
		Catalog: catalog, ID: "dispatcher", Drain: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("new paused storage controller: %v", err)
	}
	return controller
}
