//nolint:testpackage // The atomicity seam is intentionally store-local.
package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReviewCheckpointStoreReleaseWorkerAtomicity(t *testing.T) {
	t.Run("success preservation", func(t *testing.T) {
		ctx := context.Background()
		store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "success.sqlite"))
		checkpointID, assignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-success", "worker-success", ReviewCheckpointStateReviewRunning, "active")
		before := reviewCheckpointWorkerReleaseRow(ctx, t, store.db, checkpointID)

		released, err := store.ReleaseWorker(ctx, "bead-success", "worker-success")
		if err != nil || !released {
			t.Fatalf("ReleaseWorker() = (%t, %v), want (true, nil)", released, err)
		}

		after := reviewCheckpointWorkerReleaseRow(ctx, t, store.db, checkpointID)
		before["worker_id"] = "<NULL>"
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("checkpoint changed beyond worker ownership:\n before=%v\n  after=%v", before, after)
		}
		var status, completedAt string
		if err := store.db.QueryRowContext(ctx, `SELECT status, COALESCE(completed_at, '') FROM assignments WHERE id=?`, assignmentID).Scan(&status, &completedAt); err != nil {
			t.Fatalf("load released assignment: %v", err)
		}
		if status != "requeued" || completedAt == "" {
			t.Fatalf("released assignment = status %q completed_at %q, want requeued/nonempty", status, completedAt)
		}
	})

	t.Run("terminal and no-match checkpoints are not released", func(t *testing.T) {
		ctx := context.Background()
		store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "terminal.sqlite"))
		checkpointID, assignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-terminal", "worker-terminal", ReviewCheckpointStateReviewRunning, "active")

		if released, err := store.ReleaseWorker(ctx, "bead-terminal", "other-worker"); err != nil || released {
			t.Fatalf("ReleaseWorker(wrong worker) = (%t, %v), want (false, nil)", released, err)
		}
		assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, checkpointID, assignmentID, "worker-terminal", "active")

		for _, state := range []ReviewCheckpointState{ReviewCheckpointStateIntegrated, ReviewCheckpointStateSuperseded} {
			if _, err := store.db.ExecContext(ctx, `UPDATE review_checkpoints SET state=? WHERE id=?`, state, checkpointID); err != nil {
				t.Fatalf("set checkpoint terminal state %q: %v", state, err)
			}
			if released, err := store.ReleaseWorker(ctx, "bead-terminal", "worker-terminal"); err != nil || released {
				t.Fatalf("ReleaseWorker(%s) = (%t, %v), want (false, nil)", state, released, err)
			}
			assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, checkpointID, assignmentID, "worker-terminal", "active")
		}
	})

	t.Run("invalid input fails closed", func(t *testing.T) {
		ctx := context.Background()
		var nilStore *ReviewCheckpointStore
		for name, call := range map[string]func() (bool, error){
			"nil store":  func() (bool, error) { return nilStore.ReleaseWorker(ctx, "bead", "worker") },
			"nil DB":     func() (bool, error) { return (&ReviewCheckpointStore{}).ReleaseWorker(ctx, "bead", "worker") },
			"empty bead": func() (bool, error) { return (&ReviewCheckpointStore{db: &sql.DB{}}).ReleaseWorker(ctx, "", "worker") },
			"empty worker": func() (bool, error) {
				return (&ReviewCheckpointStore{db: &sql.DB{}}).ReleaseWorker(ctx, "bead", "")
			},
		} {
			t.Run(name, func(t *testing.T) {
				released, err := call()
				if err == nil || released {
					t.Fatalf("ReleaseWorker() = (%t, %v), want (false, error)", released, err)
				}
			})
		}
	})

	t.Run("checkpoint CAS mismatch rolls back", func(t *testing.T) {
		ctx := context.Background()
		t.Run("worker ownership drift", func(t *testing.T) {
			store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "worker-cas.sqlite"))
			checkpointID, assignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-worker-cas", "worker-cas", ReviewCheckpointStateReviewRunning, "active")

			released, err := store.releaseWorkerWithHook(ctx, "bead-worker-cas", "worker-cas", func(tx *sql.Tx) error {
				_, hookErr := tx.ExecContext(ctx, `UPDATE review_checkpoints SET worker_id='raced-worker' WHERE id=?`, checkpointID)
				return hookErr
			})
			if released || !errors.Is(err, ErrCheckpointConflict) {
				t.Fatalf("releaseWorkerWithHook() = (%t, %v), want (false, ErrCheckpointConflict)", released, err)
			}
			assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, checkpointID, assignmentID, "worker-cas", "active")
		})

		t.Run("assignment link drift", func(t *testing.T) {
			store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "assignment-link-cas.sqlite"))
			checkpointID, assignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-link-cas", "worker-link-cas", ReviewCheckpointStateReviewRunning, "active")
			result, err := store.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES ('bead-link-cas', 'worker-link-cas', '/tmp/bead-link-cas-next', 'quarantined')`)
			if err != nil {
				t.Fatalf("seed replacement assignment: %v", err)
			}
			replacementAssignmentID, err := result.LastInsertId()
			if err != nil {
				t.Fatalf("read replacement assignment ID: %v", err)
			}

			released, err := store.releaseWorkerWithHook(ctx, "bead-link-cas", "worker-link-cas", func(tx *sql.Tx) error {
				_, hookErr := tx.ExecContext(ctx, `UPDATE review_checkpoints SET current_assignment_id=? WHERE id=?`, replacementAssignmentID, checkpointID)
				return hookErr
			})
			if released || !errors.Is(err, ErrCheckpointConflict) {
				t.Fatalf("releaseWorkerWithHook() = (%t, %v), want (false, ErrCheckpointConflict)", released, err)
			}
			assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, checkpointID, assignmentID, "worker-link-cas", "active")
			assertReviewCheckpointAssignmentLink(ctx, t, store.db, checkpointID, assignmentID)
			assertReviewCheckpointWorkerReleaseAssignmentStatus(ctx, t, store.db, replacementAssignmentID, "quarantined")
		})
	})

	t.Run("assignment transition mismatch rolls back checkpoint", func(t *testing.T) {
		ctx := context.Background()
		store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "assignment-cas.sqlite"))
		checkpointID, assignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-assignment-cas", "worker-assignment-cas", ReviewCheckpointStateReviewRunning, "requeued")

		released, err := store.ReleaseWorker(ctx, "bead-assignment-cas", "worker-assignment-cas")
		if released || !errors.Is(err, ErrCheckpointConflict) {
			t.Fatalf("ReleaseWorker() = (%t, %v), want (false, ErrCheckpointConflict)", released, err)
		}
		assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, checkpointID, assignmentID, "worker-assignment-cas", "requeued")
	})

	t.Run("ambiguous nonterminal owners fail closed", func(t *testing.T) {
		ctx := context.Background()
		store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "ambiguous.sqlite"))
		firstCheckpointID, firstAssignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-ambiguous", "worker-ambiguous", ReviewCheckpointStateReviewRunning, "active")
		secondCheckpointID, secondAssignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-ambiguous", "worker-ambiguous", ReviewCheckpointStateBlocked, "quarantined")

		released, err := store.ReleaseWorker(ctx, "bead-ambiguous", "worker-ambiguous")
		if released || !errors.Is(err, ErrCheckpointOwnershipAmbiguous) {
			t.Fatalf("ReleaseWorker() = (%t, %v), want (false, ErrCheckpointOwnershipAmbiguous)", released, err)
		}
		assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, firstCheckpointID, firstAssignmentID, "worker-ambiguous", "active")
		assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, secondCheckpointID, secondAssignmentID, "worker-ambiguous", "quarantined")
	})

	t.Run("selects only bead-scoped checkpoint", func(t *testing.T) {
		ctx := context.Background()
		store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "bead-scope.sqlite"))
		selectedCheckpointID, selectedAssignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-selected", "shared-worker", ReviewCheckpointStateReviewRunning, "active")
		otherCheckpointID, otherAssignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-other", "shared-worker", ReviewCheckpointStateReviewRunning, "active")

		released, err := store.ReleaseWorker(ctx, "bead-selected", "shared-worker")
		if err != nil || !released {
			t.Fatalf("ReleaseWorker() = (%t, %v), want (true, nil)", released, err)
		}
		assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, selectedCheckpointID, selectedAssignmentID, "", "requeued")
		assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, otherCheckpointID, otherAssignmentID, "shared-worker", "active")
	})

	t.Run("database failure preserves both records", func(t *testing.T) {
		ctx := context.Background()
		store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "database-failure.sqlite"))
		checkpointID, assignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-database-failure", "worker-database-failure", ReviewCheckpointStateReviewRunning, "active")

		released, err := store.releaseWorkerWithHook(ctx, "bead-database-failure", "worker-database-failure", func(tx *sql.Tx) error {
			_, hookErr := tx.ExecContext(ctx, `
CREATE TEMP TRIGGER fail_review_worker_release
BEFORE UPDATE OF status ON assignments
BEGIN
  SELECT RAISE(ABORT, 'injected assignment failure');
END`)
			return hookErr
		})
		if err == nil || released {
			t.Fatalf("releaseWorkerWithHook() = (%t, %v), want (false, error)", released, err)
		}
		assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, checkpointID, assignmentID, "worker-database-failure", "active")
	})

	t.Run("hook failure rolls back unchanged records", func(t *testing.T) {
		ctx := context.Background()
		store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "hook-failure.sqlite"))
		checkpointID, assignmentID := seedReviewCheckpointWorkerRelease(ctx, t, store, "bead-hook-failure", "worker-hook-failure", ReviewCheckpointStateReviewRunning, "active")
		wantErr := errors.New("injected release hook failure")

		released, err := store.releaseWorkerWithHook(ctx, "bead-hook-failure", "worker-hook-failure", func(*sql.Tx) error {
			return wantErr
		})
		if released || !errors.Is(err, wantErr) {
			t.Fatalf("releaseWorkerWithHook() = (%t, %v), want (false, %v)", released, err, wantErr)
		}
		assertReviewCheckpointWorkerReleaseState(ctx, t, store.db, checkpointID, assignmentID, "worker-hook-failure", "active")
	})

	t.Run("cancelled context fails before transaction", func(t *testing.T) {
		store := openReviewCheckpointStore(context.Background(), t, filepath.Join(t.TempDir(), "cancelled.sqlite"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		released, err := store.ReleaseWorker(ctx, "bead-cancelled", "worker-cancelled")
		if released || !errors.Is(err, context.Canceled) {
			t.Fatalf("ReleaseWorker(cancelled) = (%t, %v), want (false, context.Canceled)", released, err)
		}
	})
}

func seedReviewCheckpointWorkerRelease(
	ctx context.Context,
	t *testing.T,
	store *ReviewCheckpointStore,
	beadID, workerID string,
	state ReviewCheckpointState,
	assignmentStatus string,
) (int64, int64) {
	t.Helper()
	result, err := store.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES (?, ?, ?, ?)`, beadID, workerID, "/tmp/"+beadID, assignmentStatus)
	if err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	assignmentID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read assignment ID: %v", err)
	}
	input := reviewCheckpointInput(beadID)
	input.CheckpointKey = fmt.Sprintf("%s:%s:%d", beadID, workerID, assignmentID)
	input.OriginAssignmentID = assignmentID
	input.CurrentAssignmentID = assignmentID
	input.WorkerID = workerID
	input.State = state
	checkpoint, err := store.CreateOrReuse(ctx, input)
	if err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET qg_run_id='qg-run', qg_output_hash='qg-output', qg_evidence_path='/tmp/evidence',
    qg_evidence_sha256='evidence-sha', review_attempt=3, recovery_attempt=2,
    recovery_strategy='retry', failure_fingerprint='fingerprint', blockers_json='["blocker"]',
    verification_json='{"verified":true}', summary='summary', artifact_path='/tmp/artifact',
    artifact_sha256='artifact-sha', artifact_bytes=123, recovery_artifact_path='/tmp/recovery',
    recovery_artifact_sha256='recovery-sha', recovery_artifact_bytes=456,
    recovery_artifact_finding_count=7
WHERE id=?`, checkpoint.ID); err != nil {
		t.Fatalf("seed checkpoint evidence: %v", err)
	}
	return checkpoint.ID, assignmentID
}

func reviewCheckpointWorkerReleaseRow(ctx context.Context, t *testing.T, db *sql.DB, checkpointID int64) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT * FROM review_checkpoints WHERE id=?`, checkpointID)
	if err != nil {
		t.Fatalf("query checkpoint row: %v", err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("list checkpoint columns: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("checkpoint %d missing", checkpointID)
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for i := range values {
		destinations[i] = &values[i]
	}
	if err := rows.Scan(destinations...); err != nil {
		t.Fatalf("scan checkpoint row: %v", err)
	}
	row := make(map[string]string, len(columns))
	for i, column := range columns {
		switch value := values[i].(type) {
		case nil:
			row[column] = "<NULL>"
		case []byte:
			row[column] = string(value)
		default:
			row[column] = fmt.Sprint(value)
		}
	}
	return row
}

func assertReviewCheckpointWorkerReleaseState(
	ctx context.Context,
	t *testing.T,
	db *sql.DB,
	checkpointID, assignmentID int64,
	wantWorker, wantAssignmentStatus string,
) {
	t.Helper()
	var workerID string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(worker_id, '') FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&workerID); err != nil {
		t.Fatalf("load checkpoint worker: %v", err)
	}
	if workerID != wantWorker {
		t.Fatalf("checkpoint worker = %q, want %q", workerID, wantWorker)
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("load assignment status: %v", err)
	}
	if status != wantAssignmentStatus {
		t.Fatalf("assignment status = %q, want %q", status, wantAssignmentStatus)
	}
}

func assertReviewCheckpointAssignmentLink(ctx context.Context, t *testing.T, db *sql.DB, checkpointID, wantAssignmentID int64) {
	t.Helper()
	var assignmentID int64
	if err := db.QueryRowContext(ctx, `SELECT current_assignment_id FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&assignmentID); err != nil {
		t.Fatalf("load checkpoint assignment link: %v", err)
	}
	if assignmentID != wantAssignmentID {
		t.Fatalf("checkpoint assignment link = %d, want %d", assignmentID, wantAssignmentID)
	}
}

func assertReviewCheckpointWorkerReleaseAssignmentStatus(ctx context.Context, t *testing.T, db *sql.DB, assignmentID int64, wantStatus string) {
	t.Helper()
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("load assignment status: %v", err)
	}
	if status != wantStatus {
		t.Fatalf("assignment status = %q, want %q", status, wantStatus)
	}
}
