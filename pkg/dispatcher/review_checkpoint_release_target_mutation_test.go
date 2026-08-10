package dispatcher //nolint:testpackage // exact store-boundary mutation owners

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestReviewCheckpointWorkerReleaseTargetFailuresAndZeroAssignment(t *testing.T) {
	ctx := context.Background()

	t.Run("serialization write failure", func(t *testing.T) {
		tx := beginMinimalReleaseTargetTx(t, "")
		target, err := loadReviewCheckpointWorkerReleaseTarget(ctx, tx, "bead", "worker")
		if target != nil || err == nil || !strings.Contains(err.Error(), "serialize review checkpoint worker release") {
			t.Fatalf("target/error = %#v/%v, want serialization error", target, err)
		}
	})

	t.Run("owner count failure", func(t *testing.T) {
		tx := beginMinimalReleaseTargetTx(t, `CREATE TABLE review_checkpoints (updated_at TEXT)`)
		target, err := loadReviewCheckpointWorkerReleaseTarget(ctx, tx, "bead", "worker")
		if target != nil || err == nil || !strings.Contains(err.Error(), "count review checkpoint owners") {
			t.Fatalf("target/error = %#v/%v, want owner-count error", target, err)
		}
	})

	t.Run("owner row failure", func(t *testing.T) {
		tx := beginMinimalReleaseTargetTx(t, `
CREATE TABLE review_checkpoints (updated_at TEXT, bead_id TEXT, state TEXT);
INSERT INTO review_checkpoints (bead_id, state) VALUES ('bead', 'review_running')`)
		target, err := loadReviewCheckpointWorkerReleaseTarget(ctx, tx, "bead", "worker")
		if target != nil || err == nil || !strings.Contains(err.Error(), "load review checkpoint owner") {
			t.Fatalf("target/error = %#v/%v, want owner-row error", target, err)
		}
	})

	t.Run("zero assignment is corrupt", func(t *testing.T) {
		store := openReviewCheckpointStore(ctx, t, t.TempDir()+"/zero-assignment.sqlite")
		checkpointID, _ := seedReviewCheckpointWorkerRelease(ctx, t, store,
			"bead-zero-assignment", "worker-zero-assignment", ReviewCheckpointStateReviewRunning, "active")
		if _, err := store.db.ExecContext(ctx,
			`UPDATE review_checkpoints SET current_assignment_id=NULL WHERE id=?`, checkpointID); err != nil {
			t.Fatalf("clear current assignment: %v", err)
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin zero-assignment transaction: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		target, err := loadReviewCheckpointWorkerReleaseTarget(ctx, tx,
			"bead-zero-assignment", "worker-zero-assignment")
		if target != nil || !errors.Is(err, ErrCheckpointOwnershipCorrupt) {
			t.Fatalf("target/error = %#v/%v, want ErrCheckpointOwnershipCorrupt", target, err)
		}
	})
}

func beginMinimalReleaseTargetTx(t *testing.T, schema string) *sql.Tx {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open minimal release-target DB: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if schema != "" {
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("create minimal release-target schema: %v", err)
		}
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin minimal release-target transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}
