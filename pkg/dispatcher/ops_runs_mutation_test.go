package dispatcher //nolint:testpackage // mutation tests exercise white-box OpsRun behavior

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

type opsRunMutationResult struct {
	id          int64
	affected    int64
	idErr       error
	affectedErr error
}

func (r opsRunMutationResult) LastInsertId() (int64, error) { return r.id, r.idErr }
func (r opsRunMutationResult) RowsAffected() (int64, error) { return r.affected, r.affectedErr }

type opsRunMutationStore struct {
	db     *sql.DB
	result sql.Result
	err    error
}

func (s opsRunMutationStore) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return s.result, s.err
}

func (s opsRunMutationStore) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, query, args...)
}

func TestOpsRunMutationLowLevelFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("create returns last insert id error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, created, err := createOpsRun(ctx, opsRunMutationStore{
			db:     d.db,
			result: opsRunMutationResult{idErr: errors.New("injected last id failure")},
		}, OpsRunRecord{Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-id"})
		if err == nil || created || !strings.Contains(err.Error(), "last id failure") {
			t.Fatalf("create last-id result = created %v err %v", created, err)
		}
	})

	t.Run("create returns load error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := d.db.Exec(`DROP TABLE ops_runs`); err != nil {
			t.Fatalf("drop ops runs: %v", err)
		}
		_, created, err := createOpsRun(ctx, opsRunMutationStore{
			db:     d.db,
			result: opsRunMutationResult{id: 41},
		}, OpsRunRecord{Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-create-load"})
		if err == nil || created || !strings.Contains(err.Error(), "ops_runs") {
			t.Fatalf("create load result = created %v err %v", created, err)
		}
	})

	t.Run("completion returns rows affected error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		_, err := completeOpsRunFromStatus(ctx, opsRunMutationStore{
			db:     d.db,
			result: opsRunMutationResult{affectedErr: errors.New("injected affected failure")},
		}, 42, opsRunStatusRunning, opsRunStatusResolved, "ok", "done", "")
		if err == nil || !strings.Contains(err.Error(), "affected failure") {
			t.Fatalf("completion affected error = %v", err)
		}
	})

	t.Run("completion returns reload error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := d.db.Exec(`DROP TABLE ops_runs`); err != nil {
			t.Fatalf("drop ops runs: %v", err)
		}
		_, err := completeOpsRunFromStatus(ctx, opsRunMutationStore{
			db:     d.db,
			result: opsRunMutationResult{affected: 0},
		}, 43, opsRunStatusRunning, opsRunStatusResolved, "ok", "done", "")
		if err == nil || !strings.Contains(err.Error(), "ops_runs") {
			t.Fatalf("completion reload error = %v", err)
		}
	})

	t.Run("completion rejects invalid expectation and missing id", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		if _, err := completeOpsRunFromStatus(ctx, d.db, 1, opsRunStatusResolved, opsRunStatusFailed, "", "", ""); err == nil {
			t.Fatal("invalid expected status error = nil")
		}
		if _, err := completeOpsRunFromStatus(ctx, d.db, 999999, opsRunStatusRunning, opsRunStatusResolved, "", "", ""); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("missing completion error = %v", err)
		}
	})
}

func TestOpsRunMutationExactReplayRequiresEveryField(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name                                 string
		status, verdict, feedback, errorText string
	}{
		{name: "status", status: opsRunStatusFailed, verdict: "original-verdict", feedback: "original-feedback", errorText: "original-error"},
		{name: "verdict", status: opsRunStatusResolved, verdict: "different-verdict", feedback: "original-feedback", errorText: "original-error"},
		{name: "feedback", status: opsRunStatusResolved, verdict: "original-verdict", feedback: "different-feedback", errorText: "original-error"},
		{name: "error", status: opsRunStatusResolved, verdict: "original-verdict", feedback: "original-feedback", errorText: "different-error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			rec, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-replay-" + tt.name})
			if err != nil {
				t.Fatalf("create replay fixture: %v", err)
			}
			if err := CompleteOpsRun(ctx, d.db, rec.ID, opsRunStatusResolved,
				"original-verdict", "original-feedback", "original-error"); err != nil {
				t.Fatalf("complete replay fixture: %v", err)
			}
			if _, err := completeOpsRunFromStatus(ctx, d.db, rec.ID, opsRunStatusRunning,
				tt.status, tt.verdict, tt.feedback, tt.errorText); err == nil {
				t.Fatalf("mismatched %s accepted as exact replay", tt.name)
			}
		})
	}
}

func TestOpsRunMutationTerminalResultMapping(t *testing.T) {
	boom := errors.New("review exploded")
	for _, tt := range []struct {
		name                       string
		result                     ops.Result
		status, verdict, errorText string
	}{
		{name: "implicit resolved", result: ops.Result{}, status: opsRunStatusResolved, verdict: string(ops.VerdictResolved)},
		{name: "explicit resolved", result: ops.Result{Verdict: ops.VerdictResolved, Feedback: "done"}, status: opsRunStatusResolved, verdict: string(ops.VerdictResolved)},
		{name: "implicit failed", result: ops.Result{Verdict: ops.VerdictFailed, Feedback: "bad result"}, status: opsRunStatusFailed, verdict: string(ops.VerdictFailed), errorText: "bad result"},
		{name: "error with implicit verdict", result: ops.Result{Err: boom}, status: opsRunStatusFailed, verdict: string(ops.VerdictFailed), errorText: boom.Error()},
		{name: "error preserves explicit verdict", result: ops.Result{Verdict: ops.VerdictResolved, Err: boom}, status: opsRunStatusFailed, verdict: string(ops.VerdictResolved), errorText: boom.Error()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, verdict, errorText := terminalOpsRunResult(tt.result)
			if status != tt.status || verdict != tt.verdict || errorText != tt.errorText {
				t.Fatalf("terminal result = %q/%q/%q, want %q/%q/%q",
					status, verdict, errorText, tt.status, tt.verdict, tt.errorText)
			}
		})
	}
}

func TestOpsRunMutationRetryNormalizesReplacement(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	original, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type: string(ops.OpsDiagnosis), BeadID: "oro-mut-retry", WorkerID: "old-worker",
		DispatcherPID: 111, ProcessPID: 222, Runtime: "codex", Model: "old-model",
	})
	if err != nil {
		t.Fatalf("create retry fixture: %v", err)
	}
	if err := CompleteOpsRun(ctx, d.db, original.ID, opsRunStatusFailed, "failed-verdict", "old-feedback", "old-error"); err != nil {
		t.Fatalf("fail retry fixture: %v", err)
	}
	failed, err := loadOpsRunByID(ctx, d.db, original.ID)
	if err != nil {
		t.Fatalf("load retry fixture: %v", err)
	}
	d.ops = nil
	replacement, routed, err := d.supersedeOpsRunForRetry(failed)
	if err != nil {
		t.Fatalf("supersede retry fixture: %v", err)
	}
	if routed {
		t.Fatal("retry without ops manager routed")
	}
	if replacement.ID == original.ID || replacement.Status != opsRunStatusRunning || replacement.DispatcherPID != os.Getpid() ||
		replacement.ProcessPID != 0 || replacement.Verdict != "" || replacement.Feedback != "" ||
		replacement.Error != fmt.Sprintf("manual retry of ops run %d", original.ID) || replacement.CompletedAt != "" {
		t.Fatalf("replacement not normalized: %+v", replacement)
	}
}

func TestOpsRunMutationReviewContextIdentity(t *testing.T) {
	ctx := context.Background()

	t.Run("nil and empty receiver inputs", func(t *testing.T) {
		var nilDispatcher *Dispatcher
		if got := nilDispatcher.reviewContextForOpsRun(ctx, OpsRunRecord{BeadID: "oro-mut-nil"}); got != (reviewOpsRunContext{}) {
			t.Fatalf("nil dispatcher context = %+v", got)
		}
		d, _, _, _, _, _ := newTestDispatcher(t)
		if got := d.reviewContextForOpsRun(ctx, OpsRunRecord{}); got != (reviewOpsRunContext{}) {
			t.Fatalf("empty bead context = %+v", got)
		}
	})

	t.Run("exact worker identity and state", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		w := &trackedWorker{
			id: "review-worker", beadID: "oro-mut-review", worktree: "/tmp/review-worktree",
			targetBranch: "epic/review-target", assignmentID: 71, state: protocol.WorkerIdle,
		}
		d.mu.Lock()
		d.workers[w.id] = w
		got := d.reviewContextFromWorkerLocked(OpsRunRecord{BeadID: w.beadID, WorkerID: w.id})
		d.mu.Unlock()
		want := reviewOpsRunContext{worktree: w.worktree, targetBranch: w.targetBranch, workerID: w.id, assignmentID: w.assignmentID}
		if got != want || w.state != protocol.WorkerReviewing {
			t.Fatalf("exact worker context/state = %+v/%q, want %+v/%q", got, w.state, want, protocol.WorkerReviewing)
		}
	})

	t.Run("wrong requested worker falls back to matching worker", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.mu.Lock()
		d.workers["wrong"] = &trackedWorker{id: "wrong", beadID: "other", worktree: "/tmp/wrong", targetBranch: "wrong"}
		d.workers["nil"] = nil
		d.workers["matching"] = &trackedWorker{
			id: "matching", beadID: "oro-mut-fallback", worktree: "/tmp/right",
			targetBranch: "epic/right", assignmentID: 88,
		}
		got := d.reviewContextFromWorkerLocked(OpsRunRecord{BeadID: "oro-mut-fallback", WorkerID: "wrong"})
		d.mu.Unlock()
		want := reviewOpsRunContext{worktree: "/tmp/right", targetBranch: "epic/right", workerID: "matching", assignmentID: 88}
		if got != want {
			t.Fatalf("fallback worker context = %+v, want %+v", got, want)
		}
	})

	t.Run("checkpoint restores durable identity", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		input := reviewCheckpointInput("oro-mut-checkpoint")
		input.OriginAssignmentID = 91
		input.CurrentAssignmentID = 0
		input.WorkerID = ""
		checkpoint, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, input)
		if err != nil {
			t.Fatalf("create checkpoint: %v", err)
		}
		got := d.reviewContextForOpsRun(ctx, OpsRunRecord{BeadID: input.BeadID, WorkerID: "record-worker"})
		want := reviewOpsRunContext{
			worktree: checkpoint.Worktree, targetBranch: checkpoint.TargetBranch,
			workerID: "record-worker", assignmentID: input.OriginAssignmentID,
		}
		if got != want {
			t.Fatalf("checkpoint context = %+v, want %+v", got, want)
		}
		d.mu.Lock()
		cached := d.worktreeByBead[input.BeadID]
		d.mu.Unlock()
		if cached != checkpoint.Worktree {
			t.Fatalf("cached worktree = %q, want %q", cached, checkpoint.Worktree)
		}
	})

	t.Run("tracked worktree falls back to default target", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg.DefaultBranch = "develop"
		d.mu.Lock()
		d.worktreeByBead["oro-mut-tracked"] = "/tmp/tracked"
		d.mu.Unlock()
		got := d.reviewContextForOpsRun(ctx, OpsRunRecord{BeadID: "oro-mut-tracked"})
		if got.worktree != "/tmp/tracked" || got.targetBranch != "develop" {
			t.Fatalf("tracked context = %+v, want worktree/default target", got)
		}
	})
}

func TestOpsRunMutationWatcherRejectsZeroIdentity(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{Verdict: ops.VerdictResolved}
	completed := make(chan struct{}, 1)
	d.watchReroutedOpsRunResult(context.Background(), OpsRunRecord{ID: 0, Type: string(ops.OpsReview), BeadID: "oro-mut-zero"},
		resultCh, func(ops.Result) { completed <- struct{}{} })
	d.wg.Wait()
	select {
	case <-completed:
		t.Fatal("zero-id watcher ran completion side effect")
	default:
	}
	if got := eventCount(t, d.db, "ops_run_complete_failed"); got != 0 {
		t.Fatalf("zero-id watcher completion failure events = %d, want 0", got)
	}
}
