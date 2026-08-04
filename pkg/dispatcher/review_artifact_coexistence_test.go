package dispatcher

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListPrunableArtifactsClassifiesDeletionConfinement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "checkpoints.sqlite"))

	checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-artifact-kind"))
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	rawPath := filepath.Join(t.TempDir(), "raw-review.json")
	recoveryPath := filepath.Join(t.TempDir(), "checkpoint-1-"+strings.Repeat("a", 64)+".json")
	qgPath := filepath.Join(t.TempDir(), "ready.json")
	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, artifact_path = ?, recovery_artifact_path = ?, qg_evidence_path = ?,
    created_at = ?, updated_at = ?, completed_at = ?
WHERE id = ?`, ReviewCheckpointStateIntegrated, rawPath, recoveryPath, qgPath,
		old, old, old, checkpoint.ID); err != nil {
		t.Fatalf("seed artifact references: %v", err)
	}

	artifacts, err := store.ListPrunableArtifacts(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("list prunable artifacts: %v", err)
	}
	want := map[string]ArtifactRef{
		rawPath:      {Path: rawPath},
		recoveryPath: {Path: recoveryPath, RecoveryArtifact: true},
		qgPath:       {Path: qgPath, QGEvidence: true},
	}
	if len(artifacts) != len(want) {
		t.Fatalf("artifacts = %#v, want %#v", artifacts, want)
	}
	for _, artifact := range artifacts {
		if expected, ok := want[artifact.Path]; !ok || artifact != expected {
			t.Fatalf("artifact = %#v, want %#v", artifact, expected)
		}
	}
}
