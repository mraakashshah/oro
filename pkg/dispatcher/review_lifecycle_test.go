//nolint:testpackage // white-box startup lifecycle assertions
package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/storage"
)

// TestReviewCheckpointStartupOrdering ensures startup restores checkpoint work
// state before reconciling orphaned ops runs or routing pending escalations.
func TestReviewCheckpointStartupOrdering(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)

	const (
		beadID     = "review-checkpoint-startup"
		workerID   = "review-checkpoint-worker"
		worktree   = "/tmp/worktree-review-checkpoint-startup"
		checkpoint = "checkpoint-startup-ordering"
	)
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Resume checkpoint review",
		AcceptanceCriteria: "Assert: review resumes from the recovered checkpoint worktree",
	}
	if err := beadSrc.AppendJourney(ctx, beadID, beadstore.JourneyEvent{
		Actor:   "dispatcher",
		Event:   "checkpoint_requested",
		Payload: `{"checkpoint_id":"` + checkpoint + `","worker_id":"` + workerID + `","trigger":"context_threshold"}`,
	}); err != nil {
		t.Fatalf("seed checkpoint journey: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES (?, ?, 'open')`,
		beadID, "Resume checkpoint review"); err != nil {
		t.Fatalf("seed checkpoint bead: %v", err)
	}

	assignmentID, err := d.createAssignment(ctx, beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("create checkpoint assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, assignmentID); err != nil {
		t.Fatalf("requeue checkpoint assignment: %v", err)
	}
	seedDurableReviewCheckpoint(t, d, beadID, assignmentID, worktree, ReviewCheckpointStateReviewRunning)
	orphaned, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          string(ops.OpsReview),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: -1,
		ProcessPID:    -1,
	})
	if err != nil {
		t.Fatalf("create orphaned review run: %v", err)
	}
	escalationID := insertDispatcherTestEscalation(t, d.db, protocol.EscOversizedBead, beadID, workerID)

	if err := d.startupRecovery(ctx); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	var recoveredEventID, escalationEventID int64
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM events WHERE type='checkpoint_recovered' AND bead_id=?`, beadID).
		Scan(&recoveredEventID); err != nil {
		t.Fatalf("query checkpoint recovery event: %v", err)
	}
	if err := d.db.QueryRowContext(ctx,
		`SELECT id FROM events WHERE type='escalation_acked' AND bead_id=?`, beadID).
		Scan(&escalationEventID); err != nil {
		t.Fatalf("query escalation routing event: %v", err)
	}
	if recoveredEventID >= escalationEventID {
		t.Fatalf("checkpoint recovery event id = %d, want before escalation routing event id %d", recoveredEventID, escalationEventID)
	}
	if status := dispatcherTestEscalationStatus(t, d.db, escalationID); status != "acked" {
		t.Fatalf("pending escalation status = %q, want acked", status)
	}

	waitFor(t, func() bool { return spawnMock.SpawnCount() == 1 }, time.Second)

	d.mu.Lock()
	restoredWorktree := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if restoredWorktree != worktree {
		t.Fatalf("restored worktree = %q, want %q", restoredWorktree, worktree)
	}
	if restoredCheckpoint := d.checkpoints.get(beadID); restoredCheckpoint == nil || restoredCheckpoint.checkpointID != checkpoint {
		t.Fatalf("restored checkpoint = %#v, want checkpoint %q", restoredCheckpoint, checkpoint)
	}

	superseded := fetchOpsRunForTest(t, d.db, orphaned.ID)
	if superseded.Status != opsRunStatusSuperseded {
		t.Fatalf("orphaned review status = %q, want %q", superseded.Status, opsRunStatusSuperseded)
	}

	spawnMock.mu.Lock()
	spawn := spawnMock.spawns[0]
	spawnMock.mu.Unlock()
	if spawn.workdir != worktree {
		t.Fatalf("rerouted review worktree = %q, want recovered %q", spawn.workdir, worktree)
	}

	var readyCount int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads_ready WHERE id=?`, beadID).Scan(&readyCount); err != nil {
		t.Fatalf("query ordinary ready queue: %v", err)
	}
	if readyCount != 0 {
		t.Fatalf("checkpoint bead appeared in ordinary ready queue %d times, want 0", readyCount)
	}
	candidate := protocol.Bead{ID: beadID, Title: "Resume checkpoint review", Status: "open", Type: "task"}
	if got := d.filterAssignable(ctx, []protocol.Bead{candidate}); len(got) != 0 {
		t.Fatalf("filterAssignable returned recovered checkpoint bead: %+v", got)
	}

	conn := newMockConn()
	d.mu.Lock()
	d.state = StateRunning
	d.workers["ordinary-worker"] = &trackedWorker{
		id:      "ordinary-worker",
		conn:    conn,
		encoder: json.NewEncoder(conn),
		state:   protocol.WorkerIdle,
	}
	d.mu.Unlock()
	beadSrc.SetBeads([]protocol.Bead{candidate})
	tryAssignAndWait(t, d, ctx)
	if got := countActiveAssignmentsForBead(t, d, beadID); got != 0 {
		t.Fatalf("ordinary active assignments after startup = %d, want 0", got)
	}
}

func TestReviewIntegrationStartupReconciliationFinalizesProvenCheckpointsIdempotently(t *testing.T) {
	ctx := context.Background()
	states := []ReviewCheckpointState{
		ReviewCheckpointStateApproved,
		ReviewCheckpointStateManualIntegrationPending,
		ReviewCheckpointStateIntegrating,
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			d, beadSrc, _, _, _, _ := newTestDispatcher(t)
			repo, baseSHA, approvedSHA := reviewIntegrationGitFixture(t, true)
			d.repoRoot = repo
			d.setCommandRunner(&ExecCommandRunner{})
			beadID := "integration-" + string(state)
			worktree := filepath.Join(repo, ".worktrees", beadID)
			beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
			assignmentID, err := d.createAssignment(ctx, beadID, "worker-"+beadID, worktree)
			if err != nil {
				t.Fatalf("create assignment: %v", err)
			}
			if err := d.requeueAssignment(ctx, assignmentID); err != nil {
				t.Fatalf("requeue assignment: %v", err)
			}
			checkpoint := seedReviewIntegrationCheckpoint(t, d, beadID, assignmentID, worktree, state, baseSHA, approvedSHA)
			if state != ReviewCheckpointStateApproved {
				if _, err := d.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET integration_target_before_sha=?, integration_approved_head_sha=?, integration_step='intent'
WHERE id=?`, baseSHA, approvedSHA, checkpoint.ID); err != nil {
					t.Fatalf("seed integration intent: %v", err)
				}
			}

			for pass := 0; pass < 2; pass++ {
				if err := d.reconcileReviewIntegrationsOnStartup(ctx); err != nil {
					t.Fatalf("reconcile pass %d: %v", pass+1, err)
				}
			}

			var gotState, observedSHA, step, assignmentStatus string
			if err := d.db.QueryRowContext(ctx, `
SELECT state, COALESCE(integration_observed_target_sha, ''), COALESCE(integration_step, '')
FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&gotState, &observedSHA, &step); err != nil {
				t.Fatalf("load reconciled checkpoint: %v", err)
			}
			if gotState != string(ReviewCheckpointStateIntegrated) || observedSHA != approvedSHA || step != "integrated" {
				t.Fatalf("checkpoint = state %q observed %q step %q, want integrated/%s/integrated", gotState, observedSHA, step, approvedSHA)
			}
			if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
				t.Fatalf("load assignment: %v", err)
			}
			if assignmentStatus != "completed" {
				t.Fatalf("assignment status = %q, want completed", assignmentStatus)
			}
			beadSrc.mu.Lock()
			closed := append([]string(nil), beadSrc.closed...)
			beadSrc.mu.Unlock()
			if len(closed) != 1 || closed[0] != beadID {
				t.Fatalf("closed beads = %v, want exactly [%s]", closed, beadID)
			}
		})
	}
}

func TestReviewIntegrationStartupReconciliationManualPendingWithoutProofHasNoSideEffects(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	repo, baseSHA, approvedSHA := reviewIntegrationGitFixture(t, false)
	d.repoRoot = repo
	d.setCommandRunner(&ExecCommandRunner{})
	const beadID = "manual-integration-unproven"
	worktree := filepath.Join(repo, ".worktrees", beadID)
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	assignmentID, err := d.createAssignment(ctx, beadID, "worker-manual", worktree)
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, assignmentID); err != nil {
		t.Fatalf("requeue assignment: %v", err)
	}
	checkpoint := seedReviewIntegrationCheckpoint(t, d, beadID, assignmentID, worktree,
		ReviewCheckpointStateManualIntegrationPending, baseSHA, approvedSHA)
	if _, err := d.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET integration_target_before_sha=?, integration_approved_head_sha=?, integration_step='intent'
WHERE id=?`, baseSHA, approvedSHA, checkpoint.ID); err != nil {
		t.Fatalf("seed integration intent: %v", err)
	}

	if err := d.reconcileReviewIntegrationsOnStartup(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var gotState, assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT state FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&gotState); err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("load assignment: %v", err)
	}
	if gotState != string(ReviewCheckpointStateManualIntegrationPending) || assignmentStatus != "requeued" {
		t.Fatalf("state/assignment = %q/%q, want manual_integration_pending/requeued", gotState, assignmentStatus)
	}
	beadSrc.mu.Lock()
	closed := len(beadSrc.closed)
	beadSrc.mu.Unlock()
	if closed != 0 {
		t.Fatalf("closed beads = %d, want 0", closed)
	}
}

func TestReviewIntegrationStartupReconciliationBlocksAmbiguousTargetOnce(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	repo, baseSHA, approvedSHA := reviewIntegrationGitFixture(t, false)
	runAssignmentTestGit(t, repo, "switch", "main")
	runAssignmentTestGit(t, repo, "commit", "--allow-empty", "-m", "unrelated target movement")
	d.repoRoot = repo
	d.setCommandRunner(&ExecCommandRunner{})
	const beadID = "integration-ambiguous-target"
	worktree := filepath.Join(repo, ".worktrees", beadID)
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	assignmentID, err := d.createAssignment(ctx, beadID, "worker-ambiguous", worktree)
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, assignmentID); err != nil {
		t.Fatalf("requeue assignment: %v", err)
	}
	checkpoint := seedReviewIntegrationCheckpoint(t, d, beadID, assignmentID, worktree,
		ReviewCheckpointStateIntegrating, baseSHA, approvedSHA)
	if _, err := d.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET integration_target_before_sha=?, integration_approved_head_sha=?, integration_step='intent'
WHERE id=?`, baseSHA, approvedSHA, checkpoint.ID); err != nil {
		t.Fatalf("seed integration intent: %v", err)
	}

	for pass := 0; pass < 2; pass++ {
		if err := d.reconcileReviewIntegrationsOnStartup(ctx); err != nil {
			t.Fatalf("reconcile pass %d: %v", pass+1, err)
		}
	}

	var gotState, summary, blockers, assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT state, summary, blockers_json FROM review_checkpoints WHERE id=?`, checkpoint.ID).
		Scan(&gotState, &summary, &blockers); err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if gotState != string(ReviewCheckpointStateBlocked) || !strings.Contains(summary, "target moved without integration proof") {
		t.Fatalf("checkpoint = state %q summary %q, want one durable ambiguous-target block", gotState, summary)
	}
	var blockerList []string
	if err := json.Unmarshal([]byte(blockers), &blockerList); err != nil {
		t.Fatalf("decode blockers: %v", err)
	}
	if len(blockerList) != 1 {
		t.Fatalf("blockers = %v, want exactly one", blockerList)
	}
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("load assignment: %v", err)
	}
	if assignmentStatus != "requeued" {
		t.Fatalf("assignment status = %q, want requeued", assignmentStatus)
	}
	if got := eventCount(t, d.db, "review_integration_blocked"); got != 1 {
		t.Fatalf("blocked events = %d, want 1", got)
	}
	beadSrc.mu.Lock()
	closed := len(beadSrc.closed)
	beadSrc.mu.Unlock()
	if closed != 0 {
		t.Fatalf("closed beads = %d, want 0", closed)
	}
}

func TestReviewIntegrationStartupReconciliationResumesEachFinalizationCrashPoint(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		step             string
		assignmentStatus string
		wantClose        int
	}{
		{step: integrationStepMergeObserved, assignmentStatus: "requeued", wantClose: 1},
		{step: integrationStepAssignmentCompleted, assignmentStatus: "completed", wantClose: 1},
		{step: integrationStepBeadClosed, assignmentStatus: "completed", wantClose: 0},
	}
	for _, tt := range tests {
		t.Run(tt.step, func(t *testing.T) {
			d, beadSrc, _, _, _, _ := newTestDispatcher(t)
			repo, baseSHA, approvedSHA := reviewIntegrationGitFixture(t, true)
			d.repoRoot = repo
			d.setCommandRunner(&ExecCommandRunner{})
			beadID := "integration-crash-" + tt.step
			worktree := filepath.Join(repo, ".worktrees", beadID)
			beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
			assignmentID, err := d.createAssignment(ctx, beadID, "worker-crash", worktree)
			if err != nil {
				t.Fatalf("create assignment: %v", err)
			}
			if err := d.requeueAssignment(ctx, assignmentID); err != nil {
				t.Fatalf("requeue assignment: %v", err)
			}
			if tt.assignmentStatus == "completed" {
				if err := d.completeCheckpointAssignment(ctx, assignmentID, beadID); err != nil {
					t.Fatalf("complete assignment fixture: %v", err)
				}
			}
			checkpoint := seedReviewIntegrationCheckpoint(t, d, beadID, assignmentID, worktree,
				ReviewCheckpointStateIntegrating, baseSHA, approvedSHA)
			if _, err := d.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET integration_target_before_sha=?, integration_approved_head_sha=?,
    integration_observed_target_sha=?, integration_step=?
WHERE id=?`, baseSHA, approvedSHA, approvedSHA, tt.step, checkpoint.ID); err != nil {
				t.Fatalf("seed crash point: %v", err)
			}

			if err := d.reconcileReviewIntegrationsOnStartup(ctx); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			var gotState, gotStep string
			if err := d.db.QueryRowContext(ctx, `SELECT state, integration_step FROM review_checkpoints WHERE id=?`, checkpoint.ID).
				Scan(&gotState, &gotStep); err != nil {
				t.Fatalf("load checkpoint: %v", err)
			}
			if gotState != string(ReviewCheckpointStateIntegrated) || gotStep != integrationStepIntegrated {
				t.Fatalf("checkpoint = %q/%q, want integrated/integrated", gotState, gotStep)
			}
			beadSrc.mu.Lock()
			closed := len(beadSrc.closed)
			beadSrc.mu.Unlock()
			if closed != tt.wantClose {
				t.Fatalf("close calls = %d, want %d", closed, tt.wantClose)
			}
		})
	}
}

func TestReviewIntegrationStartupReconciliationIgnoresTerminalCheckpoints(t *testing.T) {
	ctx := context.Background()
	for _, state := range []ReviewCheckpointState{ReviewCheckpointStateIntegrated, ReviewCheckpointStateSuperseded} {
		t.Run(string(state), func(t *testing.T) {
			d, beadSrc, _, _, _, _ := newTestDispatcher(t)
			repo, baseSHA, approvedSHA := reviewIntegrationGitFixture(t, true)
			d.repoRoot = repo
			d.setCommandRunner(&ExecCommandRunner{})
			beadID := "integration-terminal-" + string(state)
			assignmentID, err := d.createAssignment(ctx, beadID, "worker-terminal", filepath.Join(repo, beadID))
			if err != nil {
				t.Fatalf("create assignment: %v", err)
			}
			checkpoint := seedReviewIntegrationCheckpoint(t, d, beadID, assignmentID, filepath.Join(repo, beadID),
				ReviewCheckpointStateIntegrating, baseSHA, approvedSHA)
			if _, err := d.db.ExecContext(ctx, `UPDATE review_checkpoints SET state=?, integration_step=? WHERE id=?`,
				state, state, checkpoint.ID); err != nil {
				t.Fatalf("seed terminal state: %v", err)
			}

			if err := d.reconcileReviewIntegrationsOnStartup(ctx); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			var gotState string
			if err := d.db.QueryRowContext(ctx, `SELECT state FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&gotState); err != nil {
				t.Fatalf("load checkpoint: %v", err)
			}
			if gotState != string(state) {
				t.Fatalf("state = %q, want %q", gotState, state)
			}
			beadSrc.mu.Lock()
			closed := len(beadSrc.closed)
			beadSrc.mu.Unlock()
			if closed != 0 {
				t.Fatalf("close calls = %d, want 0", closed)
			}
		})
	}
}

func reviewIntegrationGitFixture(t *testing.T, mergeApproved bool) (repo, baseSHA, approvedSHA string) {
	t.Helper()
	repo = t.TempDir()
	runAssignmentTestGit(t, repo, "init", "-b", "main")
	runAssignmentTestGit(t, repo, "config", "user.email", "test@example.com")
	runAssignmentTestGit(t, repo, "config", "user.name", "Oro Test")
	runAssignmentTestGit(t, repo, "commit", "--allow-empty", "-m", "base")
	baseSHA = gitOut(t, repo, "rev-parse", "HEAD")
	runAssignmentTestGit(t, repo, "switch", "-c", "agent/review-integration")
	runAssignmentTestGit(t, repo, "commit", "--allow-empty", "-m", "approved")
	approvedSHA = gitOut(t, repo, "rev-parse", "HEAD")
	runAssignmentTestGit(t, repo, "switch", "main")
	if mergeApproved {
		runAssignmentTestGit(t, repo, "merge", "--ff-only", "agent/review-integration")
	}
	return repo, baseSHA, approvedSHA
}

func seedReviewIntegrationCheckpoint(
	t *testing.T,
	d *Dispatcher,
	beadID string,
	assignmentID int64,
	worktree string,
	state ReviewCheckpointState,
	targetSHA string,
	headSHA string,
) ReviewCheckpoint {
	t.Helper()
	checkpoint, err := NewReviewCheckpointStore(d.db).CreateOrReuse(context.Background(), CheckpointInput{
		CheckpointKey:       "checkpoint-" + beadID,
		BeadID:              beadID,
		OriginAssignmentID:  assignmentID,
		CurrentAssignmentID: assignmentID,
		WorkerID:            "worker-" + beadID,
		Worktree:            worktree,
		Branch:              "agent/review-integration",
		TargetBranch:        "main",
		HeadSHA:             headSHA,
		TargetSHA:           targetSHA,
		AcceptanceHash:      "acceptance-hash",
		QGScriptHash:        "qg-script-hash",
		QGMode:              "full",
		ReviewPolicyHash:    "review-policy-hash",
		TriageRevision:      "triage-revision",
		ReadyAttempt:        "ready-" + beadID,
		State:               state,
	})
	if err != nil {
		t.Fatalf("seed integration checkpoint: %v", err)
	}
	return checkpoint
}

func TestReviewCheckpointStartupQuarantineFailsUnroutableReplacementDurably(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, wtMgr, _, _, spawnMock := newTestDispatcher(t)

	const (
		beadID   = "review-checkpoint-quarantined-startup"
		workerID = "review-checkpoint-quarantined-worker"
		worktree = "/tmp/missing-review-checkpoint-quarantined-startup"
	)
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Quarantine unsafe checkpoint review",
		Status:             "open",
		AcceptanceCriteria: "Assert: an unsafe recovered worktree cannot launch review",
	}
	wtMgr.existsFn = func(_ context.Context, path string) bool { return path != worktree }
	seedReviewCheckpointBead(ctx, t, d, beadID, beadSrc.shown[beadID].Title)

	assignmentID, err := d.createAssignment(ctx, beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("create checkpoint assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, assignmentID); err != nil {
		t.Fatalf("requeue checkpoint assignment: %v", err)
	}
	checkpoint := seedDurableReviewCheckpoint(t, d, beadID, assignmentID, worktree, ReviewCheckpointStateReviewRunning)
	orphaned := seedOrphanedReviewRun(ctx, t, d, beadID, workerID)

	if err := d.startupRecovery(ctx); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	var assignmentStatus, quarantineReason string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query recovered assignment: %v", err)
	}
	if assignmentStatus != "quarantined" {
		t.Fatalf("recovered assignment status = %q, want quarantined", assignmentStatus)
	}
	if err := d.db.QueryRowContext(ctx, `
SELECT reason FROM recovery_quarantines
WHERE assignment_id=? AND bead_id=? AND status='open'`, assignmentID, beadID).Scan(&quarantineReason); err != nil {
		t.Fatalf("query recovery quarantine: %v", err)
	}
	if quarantineReason != "missing_worktree_path" {
		t.Fatalf("recovery quarantine reason = %q, want missing_worktree_path", quarantineReason)
	}
	if checkpoint.State != ReviewCheckpointStateReviewRunning {
		t.Fatalf("seeded checkpoint state = %q, want %q", checkpoint.State, ReviewCheckpointStateReviewRunning)
	}
	assertUnroutableStartupReviewFailedDurably(t, d, orphaned)
	if got := spawnMock.SpawnCount(); got != 0 {
		t.Fatalf("review spawns = %d, want 0", got)
	}
}

func TestReviewCheckpointStartupStoragePauseFailsUnroutableReplacementDurably(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)

	const (
		beadID   = "review-checkpoint-storage-paused-startup"
		workerID = "review-checkpoint-storage-paused-worker"
		worktree = "/tmp/worktree-review-checkpoint-storage-paused-startup"
	)
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Pause checkpoint review admission",
		Status:             "open",
		AcceptanceCriteria: "Assert: storage admission denial cannot strand a replacement run",
	}
	seedReviewCheckpointBead(ctx, t, d, beadID, beadSrc.shown[beadID].Title)

	assignmentID, err := d.createAssignment(ctx, beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("create checkpoint assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, assignmentID); err != nil {
		t.Fatalf("requeue checkpoint assignment: %v", err)
	}
	seedDurableReviewCheckpoint(t, d, beadID, assignmentID, worktree, ReviewCheckpointStateReviewRunning)
	orphaned := seedOrphanedReviewRun(ctx, t, d, beadID, workerID)
	configurePausedStorageAdmission(ctx, t, d)

	if err := d.startupRecovery(ctx); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	d.mu.Lock()
	restoredWorktree := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if restoredWorktree != worktree {
		t.Fatalf("restored worktree = %q, want %q", restoredWorktree, worktree)
	}
	assertUnroutableStartupReviewFailedDurably(t, d, orphaned)
	if got := spawnMock.SpawnCount(); got != 0 {
		t.Fatalf("review spawns = %d, want 0", got)
	}
}

func seedReviewCheckpointBead(ctx context.Context, t *testing.T, d *Dispatcher, beadID, title string) {
	t.Helper()
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES (?, ?, 'open')`, beadID, title); err != nil {
		t.Fatalf("seed checkpoint bead: %v", err)
	}
}

func seedOrphanedReviewRun(ctx context.Context, t *testing.T, d *Dispatcher, beadID, workerID string) OpsRunRecord {
	t.Helper()
	orphaned, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          string(ops.OpsReview),
		BeadID:        beadID,
		WorkerID:      workerID,
		DispatcherPID: -1,
		ProcessPID:    -1,
	})
	if err != nil {
		t.Fatalf("create orphaned review run: %v", err)
	}
	return orphaned
}

func assertUnroutableStartupReviewFailedDurably(t *testing.T, d *Dispatcher, orphaned OpsRunRecord) {
	t.Helper()
	ctx := context.Background()
	if got := fetchOpsRunForTest(t, d.db, orphaned.ID).Status; got != opsRunStatusSuperseded {
		t.Fatalf("orphaned review status = %q, want %q", got, opsRunStatusSuperseded)
	}

	var replacementID int64
	if err := d.db.QueryRowContext(ctx, `
SELECT id FROM ops_runs
WHERE type=? AND bead_id=? AND id<>?
ORDER BY id DESC LIMIT 1`, orphaned.Type, orphaned.BeadID, orphaned.ID).Scan(&replacementID); err != nil {
		t.Fatalf("query replacement review run: %v", err)
	}
	replacement := fetchOpsRunForTest(t, d.db, replacementID)
	if replacement.Status != opsRunStatusFailed {
		t.Fatalf("replacement review status = %q, want %q", replacement.Status, opsRunStatusFailed)
	}
	if replacement.CompletedAt == "" {
		t.Fatal("failed replacement review completed_at is empty")
	}
	if !strings.Contains(replacement.Error, "could not be routed on dispatcher startup") {
		t.Fatalf("failed replacement review error = %q, want durable startup routing diagnostic", replacement.Error)
	}

	var processlessRunning int
	if err := d.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM ops_runs
WHERE type=? AND bead_id=? AND status='running' AND process_pid=0`, orphaned.Type, orphaned.BeadID).Scan(&processlessRunning); err != nil {
		t.Fatalf("count processless running replacements: %v", err)
	}
	if processlessRunning != 0 {
		t.Fatalf("processless running replacements = %d, want 0", processlessRunning)
	}
}

func configurePausedStorageAdmission(ctx context.Context, t *testing.T, d *Dispatcher) {
	t.Helper()
	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open storage catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	if err := catalog.UpsertController(ctx, storage.Controller{
		ID: "dispatcher", OwnerID: "test", PID: 101, ProcessStart: now.Add(-time.Minute), HeartbeatAt: now,
		Identity: storage.ProcessIdentity{PID: 101, StartMarker: "start", Executable: "oro", ProcessGroup: 101},
	}); err != nil {
		t.Fatalf("register storage controller: %v", err)
	}
	controller, err := storage.NewController(storage.ControllerConfig{
		Catalog: catalog,
		ID:      "dispatcher",
		Drain:   func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("new storage controller: %v", err)
	}
	d.cfg.StorageController = controller
	if _, err := storage.NewPauseEpochProtocol(catalog, nil).RequestPause(ctx, now); err != nil {
		t.Fatalf("request storage pause: %v", err)
	}
}

func TestReviewCheckpointAdmissionStates(t *testing.T) {
	states := []ReviewCheckpointState{
		ReviewCheckpointStateQGPassed,
		ReviewCheckpointStateReviewRunning,
		ReviewCheckpointStateRejected,
		ReviewCheckpointStateCorrectionAssigning,
		ReviewCheckpointStateCorrectionAssigned,
		ReviewCheckpointStateContractRepairRunning,
		ReviewCheckpointStateBlocked,
		ReviewCheckpointStateFailed,
		ReviewCheckpointStateRecoveryRunning,
		ReviewCheckpointStateQuarantined,
		ReviewCheckpointStateApproved,
		ReviewCheckpointStateManualIntegrationPending,
		ReviewCheckpointStateIntegrating,
		ReviewCheckpointStateIntegrated,
		ReviewCheckpointStateSuperseded,
	}

	for _, state := range states {
		state := state
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			d, beads, _, _, _, _ := newTestDispatcher(t)
			beadID := "review-admission-" + string(state)
			worktree := "/tmp/worktree-" + beadID
			candidate := protocol.Bead{ID: beadID, Title: "Durable review admission", Status: "open", Type: "task"}
			beads.shown[beadID] = &protocol.BeadDetail{
				ID:                 beadID,
				Title:              candidate.Title,
				Status:             "open",
				AcceptanceCriteria: "Test: durable review admission",
			}
			beads.SetBeads([]protocol.Bead{candidate})
			if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
				t.Fatalf("migrate bead schema: %v", err)
			}
			if _, err := d.db.ExecContext(ctx,
				`INSERT INTO beads (id, title, status) VALUES (?, ?, 'open')`,
				beadID, candidate.Title); err != nil {
				t.Fatalf("seed checkpoint bead: %v", err)
			}
			assignmentID, err := d.createAssignment(ctx, beadID, "review-worker", worktree)
			if err != nil {
				t.Fatalf("create origin assignment: %v", err)
			}
			if err := d.requeueAssignment(ctx, assignmentID); err != nil {
				t.Fatalf("requeue origin assignment: %v", err)
			}
			seedDurableReviewCheckpoint(t, d, beadID, assignmentID, worktree, state)

			terminal := state == ReviewCheckpointStateIntegrated || state == ReviewCheckpointStateSuperseded
			wantReady := 0
			if terminal {
				wantReady = 1
			}
			var readyCount int
			if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM beads_ready WHERE id=?`, beadID).Scan(&readyCount); err != nil {
				t.Fatalf("query beads_ready: %v", err)
			}
			if readyCount != wantReady {
				t.Fatalf("beads_ready count = %d, want %d", readyCount, wantReady)
			}

			filtered := d.filterAssignable(ctx, []protocol.Bead{candidate})
			if got := len(filtered); got != wantReady {
				t.Fatalf("filterAssignable count = %d, want %d: %+v", got, wantReady, filtered)
			}

			conn := newMockConn()
			d.mu.Lock()
			d.state = StateRunning
			d.workers["ordinary-worker"] = &trackedWorker{
				id:      "ordinary-worker",
				conn:    conn,
				encoder: json.NewEncoder(conn),
				state:   protocol.WorkerIdle,
			}
			d.mu.Unlock()
			tryAssignAndWait(t, d, ctx)
			if got := countActiveAssignmentsForBead(t, d, beadID); got != wantReady {
				t.Fatalf("ordinary active assignments = %d, want %d", got, wantReady)
			}
		})
	}
}

func TestResetOrphanedBeadsPreservesNonterminalReviewCheckpointOwnership(t *testing.T) {
	states := []ReviewCheckpointState{
		ReviewCheckpointStateQGPassed,
		ReviewCheckpointStateReviewRunning,
		ReviewCheckpointStateRejected,
		ReviewCheckpointStateCorrectionAssigning,
		ReviewCheckpointStateCorrectionAssigned,
		ReviewCheckpointStateContractRepairRunning,
		ReviewCheckpointStateBlocked,
		ReviewCheckpointStateFailed,
		ReviewCheckpointStateRecoveryRunning,
		ReviewCheckpointStateQuarantined,
		ReviewCheckpointStateApproved,
		ReviewCheckpointStateManualIntegrationPending,
		ReviewCheckpointStateIntegrating,
		ReviewCheckpointStateIntegrated,
		ReviewCheckpointStateSuperseded,
	}

	for _, state := range states {
		state := state
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			d, beads, _, _, _, _ := newTestDispatcher(t)
			beadID := "review-reset-" + string(state)
			worktree := "/tmp/worktree-" + beadID
			beads.inProgressBeads = []protocol.Bead{{ID: beadID, Status: "in_progress"}}
			seedReviewCheckpointBead(ctx, t, d, beadID, "Checkpoint-owned startup bead")
			assignmentID, err := d.createAssignment(ctx, beadID, "review-worker", worktree)
			if err != nil {
				t.Fatalf("create origin assignment: %v", err)
			}
			if err := d.requeueAssignment(ctx, assignmentID); err != nil {
				t.Fatalf("requeue origin assignment: %v", err)
			}
			checkpoint := seedDurableReviewCheckpoint(t, d, beadID, assignmentID, worktree, state)

			reopened, skipped := d.resetOrphanedBeads(ctx, map[string]bool{beadID: true})
			terminal := state == ReviewCheckpointStateIntegrated || state == ReviewCheckpointStateSuperseded
			wantReopened, wantSkipped := 0, 1
			if terminal {
				wantReopened, wantSkipped = 1, 0
			}
			if reopened != wantReopened || skipped != wantSkipped {
				t.Fatalf("reset counts reopened/skipped = %d/%d, want %d/%d", reopened, skipped, wantReopened, wantSkipped)
			}
			beads.mu.Lock()
			status, updated := beads.updated[beadID]
			beads.mu.Unlock()
			if terminal {
				if !updated || status != "open" {
					t.Fatalf("terminal checkpoint bead update = %q/%t, want open/true", status, updated)
				}
			} else if updated {
				t.Fatalf("nonterminal checkpoint bead status changed to %q", status)
			}
			var gotState ReviewCheckpointState
			if err := d.db.QueryRowContext(ctx, `SELECT state FROM review_checkpoints WHERE id=?`, checkpoint.ID).Scan(&gotState); err != nil {
				t.Fatalf("query checkpoint state: %v", err)
			}
			if gotState != state {
				t.Fatalf("checkpoint state = %q, want preserved %q", gotState, state)
			}
			var assignmentStatus string
			if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
				t.Fatalf("query assignment status: %v", err)
			}
			if assignmentStatus != "requeued" {
				t.Fatalf("assignment status = %q, want preserved requeued", assignmentStatus)
			}
		})
	}
}

func TestResetOrphanedBeadsFailsClosedWhenCheckpointOwnershipIsUnobservable(t *testing.T) {
	ctx := context.Background()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const beadID = "review-reset-observation-failed"
	beads.inProgressBeads = []protocol.Bead{{ID: beadID, Status: "in_progress"}}
	if _, err := d.db.ExecContext(ctx, `DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
		t.Fatalf("drop checkpoint admission view: %v", err)
	}

	reopened, skipped := d.resetOrphanedBeads(ctx, map[string]bool{beadID: true})
	if reopened != 0 || skipped != 1 {
		t.Fatalf("reset counts reopened/skipped = %d/%d, want 0/1", reopened, skipped)
	}
	beads.mu.Lock()
	_, updated := beads.updated[beadID]
	beads.mu.Unlock()
	if updated {
		t.Fatal("unobservable checkpoint ownership reopened bead")
	}
	d.mu.Lock()
	observationErr := d.checkpointObservationError
	d.mu.Unlock()
	if !strings.Contains(observationErr, "no such table") {
		t.Fatalf("checkpoint observation error = %q, want missing-view diagnostic", observationErr)
	}
}

func TestAssignBeadRechecksDurableReviewCheckpointAfterFiltering(t *testing.T) {
	ctx := context.Background()
	d, beads, wtMgr, _, _, _ := newTestDispatcher(t)
	const (
		beadID   = "review-admission-race"
		worktree = "/tmp/worktree-review-admission-race-origin"
	)
	candidate := protocol.Bead{ID: beadID, Title: "Race durable checkpoint creation", Status: "open", Type: "task"}
	beads.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              candidate.Title,
		Status:             "open",
		AcceptanceCriteria: "Test: final assignment admission rechecks durable checkpoints",
	}
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate bead schema: %v", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES (?, ?, 'open')`,
		beadID, candidate.Title); err != nil {
		t.Fatalf("seed checkpoint bead: %v", err)
	}
	originAssignmentID, err := d.createAssignment(ctx, beadID, "review-worker", worktree)
	if err != nil {
		t.Fatalf("create origin assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, originAssignmentID); err != nil {
		t.Fatalf("requeue origin assignment: %v", err)
	}

	if got := d.filterAssignable(ctx, []protocol.Bead{candidate}); len(got) != 1 {
		t.Fatalf("candidate did not pass initial filtering: %+v", got)
	}
	seedDurableReviewCheckpoint(t, d, beadID, originAssignmentID, worktree, ReviewCheckpointStateReviewRunning)

	conn := newMockConn()
	worker := &trackedWorker{id: "ordinary-worker", conn: conn, encoder: json.NewEncoder(conn), state: protocol.WorkerIdle}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()
	if err := d.assignBead(ctx, worker, candidate); err != nil {
		t.Fatalf("assignBead: %v", err)
	}

	if got := countActiveAssignmentsForBead(t, d, beadID); got != 0 {
		t.Fatalf("ordinary active assignments = %d, want 0", got)
	}
	wtMgr.mu.Lock()
	created := len(wtMgr.created)
	wtMgr.mu.Unlock()
	if created != 0 {
		t.Fatalf("ordinary worktrees created = %d, want 0", created)
	}
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if writes != 0 {
		t.Fatalf("worker messages = %d, want no ASSIGN", writes)
	}
	beads.mu.Lock()
	status := beads.updated[beadID]
	beads.mu.Unlock()
	if status != "" {
		t.Fatalf("bead status update = %q, want no side effect", status)
	}
	if worker.state != protocol.WorkerIdle || worker.beadID != "" {
		t.Fatalf("worker state = %q bead = %q, want idle and unassigned", worker.state, worker.beadID)
	}
}

func TestAssignBeadCheckpointAdmissionPrecedesMissingAcceptanceSideEffects(t *testing.T) {
	ctx := context.Background()
	d, beads, _, esc, _, _ := newTestDispatcher(t)
	const (
		beadID         = "review-admission-before-missing-ac"
		originWorktree = "/tmp/worktree-review-admission-before-missing-ac"
	)
	candidate := protocol.Bead{ID: beadID, Title: "Checkpoint owns missing AC bead", Status: "open", Type: "task"}
	beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Title: candidate.Title, Status: "open"}
	seedReviewCheckpointBead(ctx, t, d, beadID, candidate.Title)
	originAssignmentID, err := d.createAssignment(ctx, beadID, "review-worker", originWorktree)
	if err != nil {
		t.Fatalf("create origin assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, originAssignmentID); err != nil {
		t.Fatalf("requeue origin assignment: %v", err)
	}
	seedDurableReviewCheckpoint(t, d, beadID, originAssignmentID, originWorktree, ReviewCheckpointStateReviewRunning)

	worker := &trackedWorker{id: "ordinary-worker", conn: newMockConn(), state: protocol.WorkerIdle}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()
	if err := d.assignBead(ctx, worker, candidate); err != nil {
		t.Fatalf("assignBead: %v", err)
	}

	if got := len(esc.Messages()); got != 0 {
		t.Fatalf("checkpoint-owned missing AC escalations = %d, want 0: %v", got, esc.Messages())
	}
	if got := eventCount(t, d.db, "bead_skipped_missing_ac"); got != 0 {
		t.Fatalf("checkpoint-owned missing AC skip events = %d, want 0", got)
	}
	if got := dispatcherTestEscalationCount(t, d.db, protocol.EscMissingAC, beadID); got != 0 {
		t.Fatalf("checkpoint-owned durable missing AC escalations = %d, want 0", got)
	}
	d.mu.Lock()
	_, inCooldown := d.worktreeFailures[beadID]
	d.mu.Unlock()
	if inCooldown {
		t.Fatalf("checkpoint-owned missing AC bead %s entered assignment cooldown", beadID)
	}
	beads.mu.Lock()
	status := beads.updated[beadID]
	beads.mu.Unlock()
	if status != "" {
		t.Fatalf("checkpoint-owned missing AC bead status = %q, want unchanged", status)
	}
}

func TestFilterAssignableCheckpointAdmissionPrecedesEpicAutoClose(t *testing.T) {
	ctx := context.Background()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		beadID         = "review-admission-before-epic-close"
		originWorktree = "/tmp/worktree-review-admission-before-epic-close"
	)
	candidate := protocol.Bead{ID: beadID, Title: "Checkpoint-owned completed epic", Status: "open", Type: "epic"}
	beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Title: candidate.Title, Status: "open", Type: "epic"}
	beads.hasChildrenMap = make(map[string]bool)
	beads.allChildrenClosedMap = make(map[string]bool)
	beads.hasChildrenMap[beadID] = true
	beads.allChildrenClosedMap[beadID] = true
	seedReviewCheckpointBead(ctx, t, d, beadID, candidate.Title)
	originAssignmentID, err := d.createAssignment(ctx, beadID, "review-worker", originWorktree)
	if err != nil {
		t.Fatalf("create origin assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, originAssignmentID); err != nil {
		t.Fatalf("requeue origin assignment: %v", err)
	}
	seedDurableReviewCheckpoint(t, d, beadID, originAssignmentID, originWorktree, ReviewCheckpointStateIntegrating)

	if got := d.filterAssignable(ctx, []protocol.Bead{candidate}); len(got) != 0 {
		t.Fatalf("checkpoint-owned epic remained assignable: %+v", got)
	}
	beads.mu.Lock()
	closed := append([]string(nil), beads.closed...)
	beads.mu.Unlock()
	if len(closed) != 0 {
		t.Fatalf("checkpoint-owned epic auto-closed before admission: %v", closed)
	}
	if got := eventCount(t, d.db, "epic_auto_closed"); got != 0 {
		t.Fatalf("checkpoint-owned epic auto-close events = %d, want 0", got)
	}
}

func TestAssignBeadAtomicallyRejectsCheckpointCreatedDuringWorktreeCreation(t *testing.T) {
	ctx := context.Background()
	d, beads, wtMgr, _, _, _ := newTestDispatcher(t)
	const (
		beadID         = "review-admission-insert-race"
		originWorktree = "/tmp/worktree-review-admission-insert-race-origin"
		newWorktree    = "/tmp/worktree-review-admission-insert-race-new"
	)
	candidate := protocol.Bead{ID: beadID, Title: "Close assignment insert race", Status: "open", Type: "task"}
	beads.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              candidate.Title,
		Status:             "open",
		AcceptanceCriteria: "Test: checkpoint creation races final assignment persistence",
	}
	seedReviewCheckpointBead(ctx, t, d, beadID, candidate.Title)
	originAssignmentID, err := d.createAssignment(ctx, beadID, "review-worker", originWorktree)
	if err != nil {
		t.Fatalf("create origin assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, originAssignmentID); err != nil {
		t.Fatalf("requeue origin assignment: %v", err)
	}
	if got := d.filterAssignable(ctx, []protocol.Bead{candidate}); len(got) != 1 {
		t.Fatalf("candidate did not pass initial filtering: %+v", got)
	}

	wtMgr.createFn = func(_ context.Context, gotBeadID, _ string) (string, string, error) {
		if gotBeadID != beadID {
			t.Fatalf("worktree bead ID = %q, want %q", gotBeadID, beadID)
		}
		seedDurableReviewCheckpoint(t, d, beadID, originAssignmentID, originWorktree, ReviewCheckpointStateReviewRunning)
		return newWorktree, protocol.BranchPrefix + beadID, nil
	}
	conn := newMockConn()
	worker := &trackedWorker{id: "ordinary-worker", conn: conn, encoder: json.NewEncoder(conn), state: protocol.WorkerIdle}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()

	if err := d.assignBead(ctx, worker, candidate); err != nil {
		t.Fatalf("assignBead: %v", err)
	}

	if got := countActiveAssignmentsForBead(t, d, beadID); got != 0 {
		t.Fatalf("ordinary active assignments = %d, want 0", got)
	}
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if writes != 0 {
		t.Fatalf("worker messages = %d, want no ASSIGN", writes)
	}
	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 1 || removed[0] != newWorktree {
		t.Fatalf("removed worktrees = %+v, want [%s]", removed, newWorktree)
	}
	d.mu.Lock()
	_, tracked := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if tracked {
		t.Fatal("checkpoint-blocked assignment retained the new worktree in dispatcher tracking")
	}
	if worker.state != protocol.WorkerIdle || worker.beadID != "" || worker.assignmentID != 0 {
		t.Fatalf("worker after blocked insert = state %q bead %q assignment %d, want idle/unassigned", worker.state, worker.beadID, worker.assignmentID)
	}
}

func seedDurableReviewCheckpoint(
	t *testing.T,
	d *Dispatcher,
	beadID string,
	originAssignmentID int64,
	worktree string,
	state ReviewCheckpointState,
) ReviewCheckpoint {
	t.Helper()
	initialState := state
	if state == ReviewCheckpointStateSuperseded {
		initialState = ReviewCheckpointStateReviewRunning
	}
	checkpoint, err := NewReviewCheckpointStore(d.db).CreateOrReuse(context.Background(), CheckpointInput{
		CheckpointKey:      "checkpoint-" + beadID,
		BeadID:             beadID,
		OriginAssignmentID: originAssignmentID,
		Worktree:           worktree,
		Branch:             protocol.BranchPrefix + beadID,
		TargetBranch:       "main",
		HeadSHA:            fmt.Sprintf("head-%d", originAssignmentID),
		TargetSHA:          fmt.Sprintf("target-%d", originAssignmentID),
		AcceptanceHash:     "acceptance-hash",
		QGScriptHash:       "qg-script-hash",
		QGMode:             "full",
		ReviewPolicyHash:   "review-policy-hash",
		TriageRevision:     "triage-revision",
		ReadyAttempt:       fmt.Sprintf("ready-%d", originAssignmentID),
		State:              initialState,
	})
	if err != nil {
		t.Fatalf("seed durable review checkpoint %q: %v", state, err)
	}
	if state == ReviewCheckpointStateSuperseded {
		if err := NewReviewCheckpointStore(d.db).CompareAndSwap(
			context.Background(), checkpoint.ID, initialState, state,
		); err != nil {
			t.Fatalf("supersede durable review checkpoint: %v", err)
		}
		checkpoint.State = state
	}
	return checkpoint
}

func countActiveAssignmentsForBead(t *testing.T, d *Dispatcher, beadID string) int {
	t.Helper()
	var count int
	if err := d.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`, beadID).Scan(&count); err != nil {
		t.Fatalf("count active assignments: %v", err)
	}
	return count
}
