//nolint:testpackage // The persistence regression inspects the reopened store database directly.
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
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

func TestReviewCheckpointStoreSaveRejectedFindingsCannotReopenTerminalCheckpoint(t *testing.T) {
	ctx := context.Background()
	terminalStates := []ReviewCheckpointState{
		ReviewCheckpointStateIntegrated,
		ReviewCheckpointStateSuperseded,
	}
	for _, terminalState := range terminalStates {
		t.Run(string(terminalState), func(t *testing.T) {
			store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "terminal-checkpoint.sqlite"))
			checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-terminal-"+string(terminalState)))
			if err != nil {
				t.Fatalf("create checkpoint: %v", err)
			}
			original := []ops.Finding{{ID: "original", Severity: ops.SevImportant}}
			if err := store.SaveRejectedFindings(ctx, checkpoint.ID, original, nil); err != nil {
				t.Fatalf("save original findings: %v", err)
			}
			if err := store.CompareAndSwap(ctx, checkpoint.ID, ReviewCheckpointStateRejected, terminalState); err != nil {
				t.Fatalf("make checkpoint terminal: %v", err)
			}

			err = store.SaveRejectedFindings(ctx, checkpoint.ID, []ops.Finding{{ID: "stale", Severity: ops.SevCritical}}, nil)
			if !errors.Is(err, ErrCheckpointConflict) {
				t.Fatalf("stale save error = %v, want ErrCheckpointConflict", err)
			}
			assertCheckpointStateAndFindingIDs(ctx, t, store, checkpoint.ID, terminalState, []string{"original"})
		})
	}
}

func TestReviewCheckpointStoreSaveRejectedFindingsExactReplayOnly(t *testing.T) {
	ctx := context.Background()
	store := openReviewCheckpointStore(ctx, t, filepath.Join(t.TempDir(), "rejected-replay.sqlite"))
	checkpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-rejected-replay"))
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	findings := []ops.Finding{{ID: "finding-1", Severity: ops.SevImportant, RequiredAction: "preserve this"}}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findings, nil); err != nil {
		t.Fatalf("save findings: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, findings, nil); err != nil {
		t.Fatalf("exact replay: %v", err)
	}

	changed := []ops.Finding{{ID: "finding-1", Severity: ops.SevImportant, RequiredAction: "replace this"}}
	if err := store.SaveRejectedFindings(ctx, checkpoint.ID, changed, nil); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("changed replay error = %v, want ErrCheckpointConflict", err)
	}
	assertCheckpointStateAndFindingIDs(ctx, t, store, checkpoint.ID, ReviewCheckpointStateRejected, []string{"finding-1"})
	var compact string
	if err := store.db.QueryRowContext(ctx, `SELECT compact_json FROM review_checkpoint_findings WHERE checkpoint_id = ?`, checkpoint.ID).Scan(&compact); err != nil {
		t.Fatalf("load replayed finding: %v", err)
	}
	if strings.Contains(compact, "replace this") {
		t.Fatalf("changed replay replaced committed finding: %s", compact)
	}
}

func TestReviewCheckpointStoreCapsCompactFindingsJSON(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "compact-cap.sqlite")
	store := openReviewCheckpointStore(ctx, t, dbPath)

	boundaryCheckpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-compact-boundary"))
	if err != nil {
		t.Fatalf("create boundary checkpoint: %v", err)
	}
	boundaryFinding := ops.Finding{
		ID:             "boundary",
		Severity:       ops.SevImportant,
		ContractImpact: ops.ContractImplementationFix,
		RequiredAction: "preserve required action",
	}
	baseJSON, err := json.Marshal(boundaryFinding)
	if err != nil {
		t.Fatalf("marshal boundary base: %v", err)
	}
	padding := maxReviewCheckpointFindingsJSONBytes - len(baseJSON)
	if padding <= 0 {
		t.Fatalf("boundary base bytes = %d, want below %d", len(baseJSON), maxReviewCheckpointFindingsJSONBytes)
	}
	boundaryFinding.Detail = strings.Repeat("x", padding)
	boundaryJSON, err := json.Marshal(boundaryFinding)
	if err != nil {
		t.Fatalf("marshal exact boundary: %v", err)
	}
	if len(boundaryJSON) != maxReviewCheckpointFindingsJSONBytes {
		t.Fatalf("boundary finding bytes = %d, want %d", len(boundaryJSON), maxReviewCheckpointFindingsJSONBytes)
	}
	if err := store.SaveRejectedFindings(ctx, boundaryCheckpoint.ID, []ops.Finding{boundaryFinding}, nil); err != nil {
		t.Fatalf("save exact boundary finding: %v", err)
	}
	assertCompactFindingBytes(ctx, t, store, boundaryCheckpoint.ID, maxReviewCheckpointFindingsJSONBytes)
	boundaryRecovery, err := store.LoadReviewRecovery(ctx, boundaryCheckpoint.ID)
	if err != nil {
		t.Fatalf("load exact boundary recovery: %v", err)
	}
	if !reflect.DeepEqual(boundaryRecovery.Findings, []ops.Finding{boundaryFinding}) {
		t.Fatalf("boundary recovery findings changed: got %#v want %#v", boundaryRecovery.Findings, []ops.Finding{boundaryFinding})
	}

	overflowCheckpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-compact-overflow"))
	if err != nil {
		t.Fatalf("create overflow checkpoint: %v", err)
	}
	overflowFinding := boundaryFinding
	overflowFinding.ID = "overflow"
	overflowFinding.Detail += "x"
	missingArtifactCheckpoint, err := store.CreateOrReuse(ctx, reviewCheckpointInput("oro-compact-overflow-without-artifact"))
	if err != nil {
		t.Fatalf("create missing-artifact checkpoint: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, missingArtifactCheckpoint.ID, []ops.Finding{overflowFinding}, nil); err == nil {
		t.Fatal("save overflow finding without artifact succeeded, want error")
	}
	assertCheckpointStateAndFindingIDs(ctx, t, store, missingArtifactCheckpoint.ID, ReviewCheckpointStateReviewRunning, nil)

	artifactDir := filepath.Join(root, "review-recovery")
	overflowRef, err := PersistRecoveryArtifact(artifactDir, overflowCheckpoint.ID, []ops.Finding{overflowFinding})
	if err != nil {
		t.Fatalf("persist overflow artifact: %v", err)
	}
	if err := store.SaveRejectedFindings(ctx, overflowCheckpoint.ID, []ops.Finding{overflowFinding}, &overflowRef); err != nil {
		t.Fatalf("save overflow finding: %v", err)
	}
	compactBytes := compactFindingBytes(ctx, t, store, overflowCheckpoint.ID)
	if compactBytes > maxReviewCheckpointFindingsJSONBytes {
		t.Fatalf("overflow compact bytes = %d, want <= %d", compactBytes, maxReviewCheckpointFindingsJSONBytes)
	}
	if compactBytes >= len(boundaryJSON)+1 {
		t.Fatalf("overflow compact bytes = %d, want less than lossless %d", compactBytes, len(boundaryJSON)+1)
	}
	if err := store.db.Close(); err != nil {
		t.Fatalf("close store before restart: %v", err)
	}

	restarted := openReviewCheckpointStore(ctx, t, dbPath)
	overflowRecovery, err := restarted.LoadReviewRecovery(ctx, overflowCheckpoint.ID)
	if err != nil {
		t.Fatalf("load overflow recovery after restart: %v", err)
	}
	if overflowRecovery.Findings != nil || overflowRecovery.FindingsRef == nil || !reflect.DeepEqual(*overflowRecovery.FindingsRef, overflowRef) {
		t.Fatalf("overflow recovery after restart = %#v, want exact ref %#v", overflowRecovery, overflowRef)
	}
	lossless, err := LoadRecoveryArtifact(*overflowRecovery.FindingsRef)
	if err != nil {
		t.Fatalf("load overflow artifact after restart: %v", err)
	}
	if !reflect.DeepEqual(lossless, []ops.Finding{overflowFinding}) {
		t.Fatalf("lossless overflow findings changed: got %#v want %#v", lossless, []ops.Finding{overflowFinding})
	}
}

func assertCompactFindingBytes(ctx context.Context, t *testing.T, store *ReviewCheckpointStore, checkpointID int64, want int) {
	t.Helper()
	if got := compactFindingBytes(ctx, t, store, checkpointID); got != want {
		t.Fatalf("compact finding bytes = %d, want %d", got, want)
	}
}

func compactFindingBytes(ctx context.Context, t *testing.T, store *ReviewCheckpointStore, checkpointID int64) int {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(compact_json)), 0) FROM review_checkpoint_findings WHERE checkpoint_id = ?`, checkpointID).Scan(&got); err != nil {
		t.Fatalf("sum compact finding bytes: %v", err)
	}
	return got
}

func assertCheckpointStateAndFindingIDs(
	ctx context.Context,
	t *testing.T,
	store *ReviewCheckpointStore,
	checkpointID int64,
	wantState ReviewCheckpointState,
	wantIDs []string,
) {
	t.Helper()
	var state ReviewCheckpointState
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM review_checkpoints WHERE id = ?`, checkpointID).Scan(&state); err != nil {
		t.Fatalf("load checkpoint state: %v", err)
	}
	if state != wantState {
		t.Fatalf("checkpoint state = %q, want %q", state, wantState)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT finding_id FROM review_checkpoint_findings WHERE checkpoint_id = ? ORDER BY finding_id`, checkpointID)
	if err != nil {
		t.Fatalf("load checkpoint findings: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var gotIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan checkpoint finding: %v", err)
		}
		gotIDs = append(gotIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate checkpoint findings: %v", err)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("checkpoint finding IDs = %v, want %v", gotIDs, wantIDs)
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
