package protocol

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"oro/pkg/dbutil"
)

func TestRebuildReviewCheckpointsReportsTransactionStartFailure(t *testing.T) {
	t.Parallel()
	db := openRebuildFailureDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	err := rebuildReviewCheckpoints(context.Background(), db, nil)
	if err == nil || !strings.Contains(err.Error(), "begin review checkpoint rebuild") {
		t.Fatalf("rebuild error = %v, want transaction start failure", err)
	}
}

func TestRebuildReviewCheckpointsReportsReadyViewDropFailure(t *testing.T) {
	t.Parallel()
	db := openRebuildFailureDB(t)
	if _, err := db.Exec(`CREATE TABLE beads_ready (id TEXT)`); err != nil {
		t.Fatalf("create ready-view name collision: %v", err)
	}
	err := rebuildReviewCheckpoints(context.Background(), db, nil)
	if err == nil || !strings.Contains(err.Error(), "drop ready view") {
		t.Fatalf("rebuild error = %v, want ready view drop failure", err)
	}
}

func TestRebuildReviewCheckpointsReportsAdmissionViewDropFailure(t *testing.T) {
	t.Parallel()
	db := openRebuildFailureDB(t)
	if _, err := db.Exec(`CREATE TABLE review_checkpoints_blocking_assignment (bead_id TEXT)`); err != nil {
		t.Fatalf("create admission-view name collision: %v", err)
	}
	err := rebuildReviewCheckpoints(context.Background(), db, nil)
	if err == nil || !strings.Contains(err.Error(), "drop assignment admission view") {
		t.Fatalf("rebuild error = %v, want admission view drop failure", err)
	}
}

func TestRebuildReviewCheckpointsReportsMissingLegacyTable(t *testing.T) {
	t.Parallel()
	db := openRebuildFailureDB(t)
	err := rebuildReviewCheckpoints(context.Background(), db, nil)
	if err == nil || !strings.Contains(err.Error(), "rename legacy review checkpoints") {
		t.Fatalf("rebuild error = %v, want missing legacy table failure", err)
	}
}

func openRebuildFailureDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
