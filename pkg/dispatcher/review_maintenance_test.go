//nolint:testpackage // The regression verifies the package-private terminal-state predicate.
package dispatcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestReviewArtifactTerminalStateMatrix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openReviewCheckpointStore(ctx, t, "file:review-artifact-terminal-matrix?mode=memory&cache=shared")
	olderThan := time.Date(2026, time.July, 22, 10, 0, 0, 0, time.UTC)
	createdAt := "2026-07-22 08:00:00"

	cases := []struct {
		state    ReviewCheckpointState
		eligible bool
	}{
		{ReviewCheckpointStateIntegrated, true},
		{ReviewCheckpointStateSuperseded, true},
		{ReviewCheckpointStateApproved, false},
		{ReviewCheckpointStateRejected, false},
		{ReviewCheckpointStateBlocked, false},
		{ReviewCheckpointStateFailed, false},
		{ReviewCheckpointStateQuarantined, false},
		{ReviewCheckpointStateManualIntegrationPending, false},
		{ReviewCheckpointState("unknown"), false},
	}

	wantPaths := make([]string, 0, 2)
	for i, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := isReviewArtifactTerminal(tc.state); got != tc.eligible {
				t.Fatalf("isReviewArtifactTerminal(%q) = %t, want %t", tc.state, got, tc.eligible)
			}

			path := fmt.Sprintf("/artifacts/%d.json", i)
			checkpoint := createMaintenanceCheckpoint(ctx, t, store, i, tc.state)
			if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, artifact_path = ?, created_at = ?, updated_at = ?
WHERE id = ?`, tc.state, path, createdAt, createdAt, checkpoint.ID); err != nil {
				t.Fatalf("seed artifact: %v", err)
			}
			if tc.eligible {
				wantPaths = append(wantPaths, path)
			}
		})
	}

	// SQLite persists checkpoint timestamps with datetime('now'). A terminal
	// artifact from later on the cutoff day must not compare as older merely
	// because that format uses a space instead of RFC3339's T separator.
	freshPath := "/artifacts/fresh.json"
	freshCheckpoint := createMaintenanceCheckpoint(ctx, t, store, len(cases), ReviewCheckpointStateIntegrated)
	if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, artifact_path = ?, created_at = ?, updated_at = ?
WHERE id = ?`, ReviewCheckpointStateIntegrated, freshPath, "2026-07-22 20:00:00", "2026-07-22 20:00:00", freshCheckpoint.ID); err != nil {
		t.Fatalf("seed fresh artifact: %v", err)
	}

	// A shared artifact stays retained while any checkpoint still references it.
	sharedPath := "/artifacts/shared.json"
	for i, state := range []ReviewCheckpointState{ReviewCheckpointStateIntegrated, ReviewCheckpointStateBlocked} {
		checkpoint := createMaintenanceCheckpoint(ctx, t, store, len(cases)+1+i, state)
		if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, artifact_path = ?, created_at = ?, updated_at = ?
WHERE id = ?`, state, sharedPath, createdAt, createdAt, checkpoint.ID); err != nil {
			t.Fatalf("seed shared artifact: %v", err)
		}
	}

	// A shared artifact also stays retained when every reference is terminal
	// but one reference is newer than the retention cutoff.
	freshSharedPath := "/artifacts/shared-fresh.json"
	for i, timestamp := range []string{createdAt, "2026-07-22 20:00:00"} {
		checkpoint := createMaintenanceCheckpoint(ctx, t, store, len(cases)+3+i, ReviewCheckpointStateIntegrated)
		if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, artifact_path = ?, created_at = ?, updated_at = ?
WHERE id = ?`, ReviewCheckpointStateIntegrated, freshSharedPath, timestamp, timestamp, checkpoint.ID); err != nil {
			t.Fatalf("seed shared artifact with timestamp %q: %v", timestamp, err)
		}
	}

	artifacts, err := store.ListPrunableArtifacts(ctx, olderThan)
	if err != nil {
		t.Fatalf("list prunable artifacts: %v", err)
	}
	gotPaths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		gotPaths = append(gotPaths, artifact.Path)
	}
	slices.Sort(gotPaths)
	slices.Sort(wantPaths)
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("prunable artifact paths = %v, want %v", gotPaths, wantPaths)
	}
}

func TestReviewArtifactJanitorScheduled(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate review checkpoint schema: %v", err)
	}
	d.reviewArtifactRetention = time.Hour
	d.reviewMaintenanceInterval = time.Millisecond

	store := NewReviewCheckpointStore(d.db)
	artifactDir := t.TempDir()
	duePath := filepath.Join(artifactDir, "due.json")
	activePath := filepath.Join(artifactDir, "active.json")
	for _, path := range []string{duePath, activePath} {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatalf("write artifact %s: %v", path, err)
		}
	}
	retryPath := filepath.Join(artifactDir, "retry.json")
	if err := os.Mkdir(retryPath, 0o700); err != nil {
		t.Fatalf("create retry artifact directory: %v", err)
	}
	retryChild := filepath.Join(retryPath, "pending")
	if err := os.WriteFile(retryChild, []byte("pending"), 0o600); err != nil {
		t.Fatalf("write retry artifact child: %v", err)
	}

	oldTimestamp := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	seedReviewArtifact(ctx, t, store, 100, duePath, ReviewCheckpointStateIntegrated, oldTimestamp)
	seedReviewArtifact(ctx, t, store, 101, activePath, ReviewCheckpointStateIntegrated, oldTimestamp)
	seedReviewArtifact(ctx, t, store, 102, activePath, ReviewCheckpointStateReviewRunning, oldTimestamp)
	seedReviewArtifact(ctx, t, store, 103, retryPath, ReviewCheckpointStateIntegrated, oldTimestamp)

	cancel := startDispatcher(t, d)
	waitFor(t, func() bool {
		_, err := os.Stat(duePath)
		if !os.IsNotExist(err) {
			return false
		}
		artifacts, err := store.ListPrunableArtifacts(ctx, time.Now().Add(-time.Hour))
		return err == nil && len(artifacts) == 1 && artifacts[0].Path == retryPath
	}, time.Second)
	cancel()

	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active referenced artifact stat: %v", err)
	}
	artifacts, err := store.ListPrunableArtifacts(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("list artifacts after scheduled prune: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Path != retryPath {
		t.Fatalf("prunable artifacts after failed deletion = %v, want only %q", artifacts, retryPath)
	}
	if err := os.Remove(retryChild); err != nil {
		t.Fatalf("clear retry artifact failure: %v", err)
	}

	// A restart-safe duplicate tick sees the durable acknowledgement, retries a
	// failed deletion, and retains the active reference.
	d.pruneReviewArtifacts(ctx)
	if _, err := os.Stat(retryPath); !os.IsNotExist(err) {
		t.Fatalf("retried artifact stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active referenced artifact after duplicate tick: %v", err)
	}
}

func seedReviewArtifact(
	ctx context.Context,
	t *testing.T,
	store *ReviewCheckpointStore,
	index int,
	path string,
	state ReviewCheckpointState,
	timestamp string,
) {
	t.Helper()
	checkpoint := createMaintenanceCheckpoint(ctx, t, store, index, state)
	if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, artifact_path = ?, created_at = ?, updated_at = ?
WHERE id = ?`, state, path, timestamp, timestamp, checkpoint.ID); err != nil {
		t.Fatalf("seed review artifact %s: %v", path, err)
	}
}

func createMaintenanceCheckpoint(ctx context.Context, t *testing.T, store *ReviewCheckpointStore, index int, state ReviewCheckpointState) ReviewCheckpoint {
	t.Helper()
	initialState := state
	if state == ReviewCheckpointStateSuperseded {
		initialState = ReviewCheckpointStateIntegrated
	}
	checkpoint, err := store.CreateOrReuse(ctx, CheckpointInput{
		CheckpointKey:      fmt.Sprintf("artifact-%d", index),
		BeadID:             fmt.Sprintf("oro-artifact-%d", index),
		OriginAssignmentID: int64(index + 1),
		Worktree:           fmt.Sprintf("/tmp/oro-artifact-%d", index),
		Branch:             fmt.Sprintf("agent/oro-artifact-%d", index),
		TargetBranch:       "main",
		HeadSHA:            fmt.Sprintf("head-%d", index),
		TargetSHA:          "target",
		AcceptanceHash:     "acceptance",
		QGScriptHash:       "qg-script",
		QGMode:             "default",
		ReviewPolicyHash:   "policy",
		TriageRevision:     "triage",
		ReadyAttempt:       "ready",
		State:              initialState,
	})
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	return checkpoint
}
