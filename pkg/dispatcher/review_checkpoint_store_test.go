//nolint:testpackage // The persistence regression inspects the reopened store database directly.
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestReviewCheckpointStoreCAS(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "checkpoints.sqlite")

	store := openReviewCheckpointStore(ctx, t, dbPath)
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

	reopened := openReviewCheckpointStore(ctx, t, dbPath)
	var state ReviewCheckpointState
	if err := reopened.db.QueryRowContext(ctx, `SELECT state FROM review_checkpoints WHERE id = ?`, created.ID).Scan(&state); err != nil {
		t.Fatalf("read reopened checkpoint: %v", err)
	}
	if state != ReviewCheckpointStateIntegrated {
		t.Fatalf("reopened checkpoint state = %q, want %q", state, ReviewCheckpointStateIntegrated)
	}
}

func TestReviewCheckpointStoreSaveRejectedFindings(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rejected-findings.sqlite")
	store := openReviewCheckpointStore(ctx, t, dbPath)
	checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-rejected-findings"))
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}

	saver, ok := any(store).(interface {
		SaveRejectedFindings(context.Context, int64, []ops.Finding, *ReviewRecoveryArtifactRef) error
	})
	if !ok {
		t.Fatal("ReviewCheckpointStore does not expose SaveRejectedFindings")
	}
	findings := []ops.Finding{{
		ID:             "finding-1",
		Severity:       ops.SevImportant,
		Evidence:       []ops.Evidence{{File: "pkg/example.go", LineStart: 42, LineEnd: 42}},
		ContractImpact: ops.ContractAcceptanceGap,
		RequiredAction: "update acceptance",
	}}
	if err := saver.SaveRejectedFindings(ctx, checkpoint.ID, findings, nil); err != nil {
		t.Fatalf("save rejected findings: %v", err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatalf("close store DB: %v", err)
	}

	reopened := openReviewCheckpointStore(ctx, t, dbPath)
	var got struct {
		ID       string
		Severity string
		File     string
		Line     int
		Impact   string
		Action   string
		Compact  string
	}
	if err := reopened.db.QueryRowContext(ctx, `
SELECT finding_id, severity, file, line, contract_impact, required_action, compact_json
FROM review_checkpoint_findings
WHERE checkpoint_id = ?`, checkpoint.ID).Scan(&got.ID, &got.Severity, &got.File, &got.Line, &got.Impact, &got.Action, &got.Compact); err != nil {
		t.Fatalf("read persisted finding: %v", err)
	}
	if got.ID != "finding-1" || got.Severity != string(ops.SevImportant) || got.File != "pkg/example.go" || got.Line != 42 || got.Impact != string(ops.ContractAcceptanceGap) || got.Action != "update acceptance" {
		t.Fatalf("persisted finding = %#v, want durable structured fields", got)
	}
	var compact ops.Finding
	if err := json.Unmarshal([]byte(got.Compact), &compact); err != nil {
		t.Fatalf("unmarshal compact finding: %v", err)
	}
	if !reflect.DeepEqual(compact, findings[0]) {
		t.Fatalf("compact finding = %#v, want %#v", compact, findings[0])
	}
}

func TestReviewCheckpointStoreSaveRejectedFindingsRejectsUnknownCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "unknown-checkpoint.sqlite"))

	err := store.SaveRejectedFindings(ctx, 999, []ops.Finding{{ID: "finding-1", Severity: ops.SevImportant}}, nil)
	if err == nil {
		t.Fatal("save findings for unknown checkpoint succeeded")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM review_checkpoint_findings`).Scan(&count); err != nil {
		t.Fatalf("count findings: %v", err)
	}
	if count != 0 {
		t.Fatalf("orphaned findings = %d, want 0", count)
	}
}

func reviewCheckpointInput(beadID string) CheckpointInput {
	return CheckpointInput{
		CheckpointKey:      beadID + ":head:target",
		BeadID:             beadID,
		OriginAssignmentID: 17,
		Worktree:           "/tmp/" + beadID,
		Branch:             "agent/" + beadID,
		TargetBranch:       "main",
		HeadSHA:            "head",
		TargetSHA:          "target",
		AcceptanceHash:     "acceptance",
		QGScriptHash:       "qg-script",
		QGMode:             "default",
		ReviewPolicyHash:   "policy",
		TriageRevision:     "triage",
		ReadyAttempt:       "ready-1",
		State:              ReviewCheckpointStateReviewRunning,
	}
}

func openReviewCheckpointStore(ctx context.Context, t *testing.T, path string) *ReviewCheckpointStore {
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
