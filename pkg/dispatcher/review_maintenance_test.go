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

	"oro/pkg/evidencefs"
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

func TestReviewEvidenceParticipatesInArtifactRetention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openReviewCheckpointStore(ctx, t, "file:review-evidence-retention?mode=memory&cache=shared")
	checkpoint := createMaintenanceCheckpoint(ctx, t, store, 200, ReviewCheckpointStateIntegrated)
	evidencePath := filepath.Join(t.TempDir(), "1.json")
	oldTimestamp := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, qg_evidence_path = ?, qg_evidence_sha256 = 'evidence-hash', created_at = ?, updated_at = ?
WHERE id = ?`, ReviewCheckpointStateIntegrated, evidencePath, oldTimestamp, oldTimestamp, checkpoint.ID); err != nil {
		t.Fatalf("seed retained QG evidence: %v", err)
	}

	artifacts, err := store.ListPrunableArtifacts(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("list prunable QG evidence: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Path != evidencePath || !artifacts[0].QGEvidence {
		t.Fatalf("prunable QG evidence = %v, want %q", artifacts, evidencePath)
	}
	if err := store.ClearPrunedArtifact(ctx, evidencePath); err != nil {
		t.Fatalf("clear pruned QG evidence: %v", err)
	}
	var retained, retainedHash string
	if err := store.db.QueryRowContext(ctx,
		`SELECT COALESCE(qg_evidence_path, ''), COALESCE(qg_evidence_sha256, '') FROM review_checkpoints WHERE id = ?`, checkpoint.ID,
	).Scan(&retained, &retainedHash); err != nil {
		t.Fatalf("load cleared QG evidence: %v", err)
	}
	if retained != "" || retainedHash != "" {
		t.Fatalf("QG evidence reference retained after clear: path=%q hash=%q", retained, retainedHash)
	}
}

func TestCheckpointReviewEvidencePruneDoesNotFollowReplacedParents(t *testing.T) {
	for _, replacedParent := range []string{"bead", "assignment"} {
		t.Run(replacedParent, func(t *testing.T) {
			ctx := context.Background()
			d, _, _, _, _, _ := newTestDispatcher(t)
			migrateReviewMaintenanceSchema(t, d)
			root := filepath.Join(t.TempDir(), "review-evidence")
			d.cfg.ReviewEvidenceDir = root
			d.reviewArtifactRetention = time.Hour
			now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
			d.nowFunc = func() time.Time { return now }
			const (
				beadID       = "oro-checkpoint-symlink"
				assignmentID = int64(41)
			)
			path := writeMaintenanceEvidence(t, root, beadID, assignmentID, now.Add(-2*time.Hour))
			checkpointID := seedCheckpointQGEvidence(ctx, t, d, path, now.Add(-2*time.Hour))

			externalParent := t.TempDir()
			externalAssignment := externalParent
			if replacedParent == "bead" {
				externalAssignment = filepath.Join(externalParent, "41")
				if err := os.Mkdir(externalAssignment, 0o700); err != nil {
					t.Fatalf("create external assignment: %v", err)
				}
			}
			externalPath := filepath.Join(externalAssignment, readyEvidenceAttempt)
			if err := os.WriteFile(externalPath, []byte("external"), 0o600); err != nil {
				t.Fatalf("write external evidence: %v", err)
			}
			beadDir := filepath.Join(root, beadID)
			assignmentDir := filepath.Join(beadDir, "41")
			replacedPath, heldPath, target := beadDir, filepath.Join(root, "held-bead"), externalParent
			if replacedParent == "assignment" {
				replacedPath = assignmentDir
				heldPath = filepath.Join(beadDir, "held-assignment")
				target = externalAssignment
			}
			if err := os.Rename(replacedPath, heldPath); err != nil {
				t.Fatalf("hold canonical %s parent: %v", replacedParent, err)
			}
			if err := os.Symlink(target, replacedPath); err != nil {
				t.Fatalf("replace %s parent with symlink: %v", replacedParent, err)
			}

			d.pruneReviewArtifacts(ctx)
			data, err := os.ReadFile(externalPath)
			if err != nil || string(data) != "external" {
				t.Fatalf("external target changed: data=%q err=%v", data, err)
			}
			assertCheckpointQGEvidenceReference(t, d, checkpointID, path)
		})
	}
}

func TestCheckpointReviewEvidencePruneRemovesCanonicalFile(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	migrateReviewMaintenanceSchema(t, d)
	root := filepath.Join(t.TempDir(), "review-evidence")
	d.cfg.ReviewEvidenceDir = root
	d.reviewArtifactRetention = time.Hour
	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return now }
	path := writeMaintenanceEvidence(t, root, "oro-checkpoint-canonical", 51, now.Add(-2*time.Hour))
	checkpointID := seedCheckpointQGEvidence(ctx, t, d, path, now.Add(-2*time.Hour))

	d.pruneReviewArtifacts(ctx)
	assertMaintenanceFileMissing(t, path)
	assertCheckpointQGEvidenceReference(t, d, checkpointID, "")
}

func seedCheckpointQGEvidence(
	ctx context.Context,
	t *testing.T,
	d *Dispatcher,
	path string,
	terminalAt time.Time,
) int64 {
	t.Helper()
	checkpoint := createMaintenanceCheckpoint(ctx, t, NewReviewCheckpointStore(d.db), int(terminalAt.UnixNano()), ReviewCheckpointStateIntegrated)
	timestamp := terminalAt.UTC().Format(time.RFC3339Nano)
	if _, err := d.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state=?, qg_evidence_path=?, qg_evidence_sha256='evidence-hash', created_at=?, updated_at=?, completed_at=?
WHERE id=?`, ReviewCheckpointStateIntegrated, path, timestamp, timestamp, timestamp, checkpoint.ID); err != nil {
		t.Fatalf("seed checkpoint QG evidence: %v", err)
	}
	return checkpoint.ID
}

func assertCheckpointQGEvidenceReference(t *testing.T, d *Dispatcher, checkpointID int64, want string) {
	t.Helper()
	var path string
	if err := d.db.QueryRow(`SELECT COALESCE(qg_evidence_path, '') FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&path); err != nil {
		t.Fatalf("load checkpoint QG evidence reference: %v", err)
	}
	if path != want {
		t.Fatalf("checkpoint QG evidence reference = %q, want %q", path, want)
	}
}

func TestReviewEvidenceOrphanSweepAfterCrashRetainsFreshAndLive(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	migrateReviewMaintenanceSchema(t, d)
	root := filepath.Join(t.TempDir(), "review-evidence")
	d.cfg.ReviewEvidenceDir = root
	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Hour)
	d.nowFunc = func() time.Time { return now }
	d.reviewArtifactRetention = time.Hour

	orphan := writeMaintenanceEvidence(t, root, "oro-crash-orphan", 11, cutoff.Add(-time.Minute))
	fresh := writeMaintenanceEvidence(t, root, "oro-fresh-contender", 12, cutoff.Add(time.Minute))
	live := writeMaintenanceEvidence(t, root, "oro-live-assignment", 13, cutoff.Add(-time.Minute))
	if _, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (id, bead_id, worker_id, worktree, qg_evidence_dir, status)
VALUES (?, ?, 'worker-live', '/tmp/live', ?, 'active')`, 13, "oro-live-assignment", root); err != nil {
		t.Fatalf("seed live assignment: %v", err)
	}

	d.pruneReviewArtifacts(ctx)
	assertMaintenanceFileMissing(t, orphan)
	assertMaintenanceFileExists(t, fresh)
	assertMaintenanceFileExists(t, live)
}

func TestReviewEvidenceOrphanSweepRetainsCheckpointReference(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	migrateReviewMaintenanceSchema(t, d)
	root := filepath.Join(t.TempDir(), "review-evidence")
	d.cfg.ReviewEvidenceDir = root
	cutoff := time.Date(2026, time.August, 3, 11, 0, 0, 0, time.UTC)
	path := writeMaintenanceEvidence(t, root, "oro-checkpoint-live", 21, cutoff.Add(-time.Minute))
	checkpoint := createMaintenanceCheckpoint(ctx, t, NewReviewCheckpointStore(d.db), 321, ReviewCheckpointStateReviewRunning)
	if _, err := d.db.ExecContext(ctx,
		`UPDATE review_checkpoints SET qg_evidence_path=? WHERE id=?`, path, checkpoint.ID); err != nil {
		t.Fatalf("seed checkpoint evidence reference: %v", err)
	}

	d.pruneReviewEvidenceOrphans(ctx, cutoff)
	assertMaintenanceFileExists(t, path)
}

func TestReviewEvidenceOrphanSweepFailsClosedOnDatabaseError(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	migrateReviewMaintenanceSchema(t, d)
	root := filepath.Join(t.TempDir(), "review-evidence")
	d.cfg.ReviewEvidenceDir = root
	cutoff := time.Date(2026, time.August, 3, 11, 0, 0, 0, time.UTC)
	path := writeMaintenanceEvidence(t, root, "oro-db-failure", 31, cutoff.Add(-time.Minute))
	if err := d.db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	d.pruneReviewEvidenceOrphans(context.Background(), cutoff)
	assertMaintenanceFileExists(t, path)
}

func TestReviewEvidenceOrphanSweepDoesNotEscapeSymlink(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	migrateReviewMaintenanceSchema(t, d)
	root := filepath.Join(t.TempDir(), "review-evidence")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create evidence root: %v", err)
	}
	d.cfg.ReviewEvidenceDir = root
	external := t.TempDir()
	externalAssignment := filepath.Join(external, "41")
	if err := os.Mkdir(externalAssignment, 0o700); err != nil {
		t.Fatalf("create external assignment: %v", err)
	}
	externalPath := filepath.Join(externalAssignment, "1.json")
	if err := os.WriteFile(externalPath, []byte("external"), 0o600); err != nil {
		t.Fatalf("write external evidence: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(root, "oro-symlink")); err != nil {
		t.Fatalf("create bead symlink: %v", err)
	}

	d.pruneReviewEvidenceOrphans(context.Background(), time.Now().Add(time.Hour))
	data, err := os.ReadFile(externalPath)
	if err != nil || string(data) != "external" {
		t.Fatalf("external evidence changed: data=%q err=%v", data, err)
	}
}

func writeMaintenanceEvidence(t *testing.T, root, beadID string, assignmentID int64, modTime time.Time) string {
	t.Helper()
	if err := evidencefs.WriteFile(root, []string{beadID, fmt.Sprint(assignmentID)}, "1.json", []byte("evidence")); err != nil {
		t.Fatalf("write evidence fixture: %v", err)
	}
	path := filepath.Join(root, beadID, fmt.Sprint(assignmentID), "1.json")
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("age evidence fixture: %v", err)
	}
	return path
}

func migrateReviewMaintenanceSchema(t *testing.T, d *Dispatcher) {
	t.Helper()
	if err := protocol.MigrateBeadSchema(context.Background(), d.db); err != nil {
		t.Fatalf("migrate review maintenance schema: %v", err)
	}
}

func assertMaintenanceFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected retained file %s: %v", path, err)
	}
}

func assertMaintenanceFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected pruned file %s, stat error %v", path, err)
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
