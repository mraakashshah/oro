//nolint:testpackage // white-box startup lifecycle assertions
package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/ops"
	"oro/pkg/protocol"
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
