package dispatcher

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestReviewCheckpointStoreCAS(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "checkpoints.sqlite")

	store := openReviewCheckpointStore(t, ctx, dbPath)
	input := CheckpointInput{
		CheckpointKey:       "oro-cas:head:target",
		BeadID:              "oro-cas",
		OriginAssignmentID:  17,
		CurrentAssignmentID: 17,
		Worktree:            "/tmp/oro-cas",
		Branch:              "agent/oro-cas",
		TargetBranch:        "main",
		HeadSHA:             "head",
		TargetSHA:           "target",
		AcceptanceHash:      "acceptance",
		QGScriptHash:        "qg-script",
		QGMode:              "default",
		ReviewPolicyHash:    "policy",
		TriageRevision:      "triage",
		ReadyAttempt:        "ready-1",
		State:               ReviewCheckpointStateReviewRunning,
	}

	created, err := store.CreateOrReuse(ctx, input)
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	reused, err := store.CreateOrReuse(ctx, input)
	if err != nil {
		t.Fatalf("reuse checkpoint: %v", err)
	}
	if reused.ID != created.ID {
		t.Fatalf("reused checkpoint ID = %d, want canonical ID %d", reused.ID, created.ID)
	}

	if err := store.CompareAndSwap(ctx, created.ID, ReviewCheckpointStateReviewRunning, ReviewCheckpointStateIntegrated); err != nil {
		t.Fatalf("transition checkpoint: %v", err)
	}
	if err := store.CompareAndSwap(ctx, created.ID, ReviewCheckpointStateReviewRunning, ReviewCheckpointStateSuperseded); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("stale transition error = %v, want ErrCheckpointConflict", err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatalf("close store DB: %v", err)
	}

	reopened := openReviewCheckpointStore(t, ctx, dbPath)
	var state ReviewCheckpointState
	if err := reopened.db.QueryRowContext(ctx, `SELECT state FROM review_checkpoints WHERE id = ?`, created.ID).Scan(&state); err != nil {
		t.Fatalf("read reopened checkpoint: %v", err)
	}
	if state != ReviewCheckpointStateIntegrated {
		t.Fatalf("reopened checkpoint state = %q, want %q", state, ReviewCheckpointStateIntegrated)
	}
}

func openReviewCheckpointStore(t *testing.T, ctx context.Context, path string) *ReviewCheckpointStore {
	t.Helper()
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open checkpoint DB: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate checkpoint DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewReviewCheckpointStore(db)
}
