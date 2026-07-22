//nolint:testpackage // The regression verifies the package-private terminal-state predicate.
package dispatcher

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestReviewArtifactTerminalStateMatrix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openReviewCheckpointStore(ctx, t, "file:review-artifact-terminal-matrix?mode=memory&cache=shared")
	olderThan := time.Now().Add(-time.Hour)
	createdAt := olderThan.Add(-time.Hour).UTC().Format(time.RFC3339Nano)

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
			checkpoint := createMaintenanceCheckpoint(t, ctx, store, i, tc.state)
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

	// A shared artifact stays retained while any checkpoint still references it.
	sharedPath := "/artifacts/shared.json"
	for i, state := range []ReviewCheckpointState{ReviewCheckpointStateIntegrated, ReviewCheckpointStateBlocked} {
		checkpoint := createMaintenanceCheckpoint(t, ctx, store, len(cases)+i, state)
		if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, artifact_path = ?, created_at = ?, updated_at = ?
WHERE id = ?`, state, sharedPath, createdAt, createdAt, checkpoint.ID); err != nil {
			t.Fatalf("seed shared artifact: %v", err)
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

func createMaintenanceCheckpoint(t *testing.T, ctx context.Context, store *ReviewCheckpointStore, index int, state ReviewCheckpointState) ReviewCheckpoint {
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
