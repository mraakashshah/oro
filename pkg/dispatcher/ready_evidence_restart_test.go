package dispatcher //nolint:testpackage // Restart admission requires internal worker and checkpoint state.

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
)

func TestCanonicalReadyEvidenceReconnectRestoresAndReviewsExactlyOnce(t *testing.T) {
	for _, buffered := range []bool{true, false} {
		t.Run(map[bool]string{true: "buffered READY", false: "already sent READY"}[buffered], func(t *testing.T) {
			ctx := context.Background()
			original, beads, worktrees, escalator, gitRunner, _ := newTestDispatcher(t)
			const (
				workerID = "worker-ready-restart"
				beadID   = "oro-ready-restart"
			)
			worktree := t.TempDir()
			evidenceRoot := filepath.Join(t.TempDir(), "review-evidence")
			original.cfg.ReviewEvidenceDir = evidenceRoot
			original.cfg.HeartbeatTimeout = 5 * time.Second
			worktrees.createFn = func(context.Context, string, string) (string, string, error) {
				return worktree, protocol.BranchPrefix + beadID, nil
			}
			worktrees.currentBranchFn = func(context.Context, string) (string, error) {
				return protocol.BranchPrefix + beadID, nil
			}
			beads.shown[beadID] = &protocol.BeadDetail{
				ID: beadID, Status: "open", AcceptanceCriteria: "restart resumes review",
			}

			startDispatcher(t, original)
			conn, _ := connectWorker(t, original.cfg.SocketPath)
			sendMsg(t, conn, protocol.Message{
				Type: protocol.MsgHeartbeat,
				Heartbeat: &protocol.HeartbeatPayload{
					WorkerID: workerID,
				},
			})
			waitForWorkers(t, original, 1, time.Second)
			sendDirective(t, original.cfg.SocketPath, "start")
			waitForState(t, original, StateRunning, time.Second)
			beads.SetBeads([]protocol.Bead{{
				ID: beadID, Title: "Ready restart", AcceptanceCriteria: "restart resumes review", Priority: 0,
			}})

			assigned, ok := readMsg(t, conn, 2*time.Second)
			if !ok || assigned.Type != protocol.MsgAssign || assigned.Assign == nil {
				t.Fatalf("received message = %#v, want production ASSIGN", assigned)
			}
			assignment := assigned.Assign
			if assignment.AssignmentID <= 0 {
				t.Fatalf("production assignment ID = %d, want positive", assignment.AssignmentID)
			}
			var targetBranch string
			if err := original.db.QueryRowContext(ctx,
				`SELECT target_branch FROM assignments WHERE id = ?`, assignment.AssignmentID,
			).Scan(&targetBranch); err != nil {
				t.Fatalf("load production target branch: %v", err)
			}
			evidencePath, err := canonicalReadyEvidencePath(evidenceRoot, beadID, assignment.AssignmentID)
			if err != nil {
				t.Fatalf("canonical evidence path: %v", err)
			}
			ready := protocol.ReadyForReviewPayload{
				BeadID: beadID, WorkerID: workerID, AssignmentID: assignment.AssignmentID, Worktree: worktree,
				QGEvidencePath: evidencePath, TargetSHA: assignment.TargetSHA,
			}
			writeReadyEvidenceFixture(t, evidencePath, ready)

			// Exercise the actual cancellation-driven graceful shutdown. The worker
			// follows the production protocol instead of tests directly rewriting the
			// assignment or calling reconnect handlers.
			beads.SetBeads(nil)
			sendDirective(t, original.cfg.SocketPath, "restart-daemon")
			prepare, ok := readMsg(t, conn, 2*time.Second)
			if !ok || prepare.Type != protocol.MsgPrepareShutdown || prepare.PrepareShutdown == nil {
				t.Fatalf("graceful shutdown message = %#v, want PREPARE_SHUTDOWN", prepare)
			}
			sendMsg(t, conn, protocol.Message{
				Type: protocol.MsgHandoff,
				Handoff: &protocol.HandoffPayload{
					BeadID: beadID, WorkerID: workerID, ContextSummary: "READY already produced",
				},
			})
			sendMsg(t, conn, protocol.Message{
				Type: protocol.MsgShutdownApproved,
				ShutdownApproved: &protocol.ShutdownApprovedPayload{
					WorkerID: workerID,
				},
			})
			waitFor(t, func() bool { return eventCount(t, original.db, "shutdown_approved") == 1 }, 2*time.Second)
			_ = conn.Close()
			waitFor(t, func() bool {
				var status string
				return original.db.QueryRowContext(ctx,
					`SELECT status FROM assignments WHERE id = ?`, assignment.AssignmentID,
				).Scan(&status) == nil && status == "requeued"
			}, 2*time.Second)

			blockingSpawner := newBlockingReadyReviewSpawner()
			t.Cleanup(blockingSpawner.releaseAll)
			restarted, err := New(original.cfg, original.db, merge.NewCoordinator(gitRunner),
				ops.NewSpawner(blockingSpawner), beads, worktrees, escalator, nil,
				WithMemoryServices(newTestMemoryServices(original.db)))
			if err != nil {
				t.Fatalf("construct restarted dispatcher: %v", err)
			}
			startDispatcher(t, restarted)
			reconnectConn, _ := connectWorker(t, restarted.cfg.SocketPath)
			reconnect := protocol.ReconnectPayload{
				WorkerID: workerID, BeadID: beadID, State: "awaiting_review",
			}
			if buffered {
				reconnect.BufferedEvents = []protocol.Message{{
					Type: protocol.MsgReadyForReview, ReadyForReview: &ready,
				}}
			}

			sendMsg(t, reconnectConn, protocol.Message{Type: protocol.MsgReconnect, Reconnect: &reconnect})
			waitFor(t, func() bool { return blockingSpawner.spawnCount() == 1 }, time.Second)
			sendMsg(t, reconnectConn, protocol.Message{Type: protocol.MsgReconnect, Reconnect: &reconnect})
			time.Sleep(20 * time.Millisecond)
			if got := blockingSpawner.spawnCount(); got != 1 {
				t.Fatalf("review spawn count after duplicate reconnect = %d, want 1", got)
			}

			restarted.mu.Lock()
			tracked := restarted.workers[workerID]
			gotState, gotAssignment, gotBead, gotWorktree := tracked.state, tracked.assignmentID, tracked.beadID, tracked.worktree
			gotEvidenceRoot, gotEvidencePath := tracked.qgEvidenceDir, tracked.qgEvidencePath
			gotTargetSHA, gotTargetBranch := tracked.targetSHA, tracked.targetBranch
			restarted.mu.Unlock()
			if gotState != protocol.WorkerReviewing || gotAssignment != assignment.AssignmentID || gotBead != beadID ||
				gotWorktree != worktree || gotEvidenceRoot != evidenceRoot || gotEvidencePath != evidencePath ||
				gotTargetSHA != assignment.TargetSHA || gotTargetBranch != targetBranch {
				t.Fatalf("restored worker = state %q assignment %d bead %q worktree %q evidence root %q path %q target %q branch %q",
					gotState, gotAssignment, gotBead, gotWorktree, gotEvidenceRoot, gotEvidencePath, gotTargetSHA, gotTargetBranch)
			}
			var checkpoints int
			if err := restarted.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM review_checkpoints
WHERE origin_assignment_id = ? AND worktree = ? AND target_branch = ? AND target_sha = ?
	AND qg_evidence_path = ?`, assignment.AssignmentID, worktree, targetBranch, assignment.TargetSHA, evidencePath).Scan(&checkpoints); err != nil {
				t.Fatalf("count restored checkpoints: %v", err)
			}
			if checkpoints != 1 {
				t.Fatalf("restored checkpoint count = %d, want 1", checkpoints)
			}
			var assignmentStatus, durableWorker string
			if err := restarted.db.QueryRowContext(ctx,
				`SELECT status, worker_id FROM assignments WHERE id = ?`, assignment.AssignmentID,
			).Scan(&assignmentStatus, &durableWorker); err != nil {
				t.Fatalf("load recovered assignment: %v", err)
			}
			if assignmentStatus != "active" || durableWorker != workerID {
				t.Fatalf("recovered assignment = status %q worker %q, want active/%q",
					assignmentStatus, durableWorker, workerID)
			}
		})
	}
}

func TestAwaitingReviewReconnectDoesNotReviveNonCanonicalAssignment(t *testing.T) {
	for _, tc := range []struct {
		name          string
		ownerWorker   string
		addNewerState string
	}{
		{name: "different durable worker", ownerWorker: "worker-other"},
		{name: "newer terminal assignment", ownerWorker: "worker-reconnect", addNewerState: "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			d, beads, _, _, _, _ := newTestDispatcher(t)
			const (
				assignmentID = int64(81)
				workerID     = "worker-reconnect"
				beadID       = "oro-no-revive"
				targetSHA    = "0123456789abcdef0123456789abcdef01234567"
			)
			worktree := t.TempDir()
			evidenceRoot := filepath.Join(t.TempDir(), "review-evidence")
			d.cfg.ReviewEvidenceDir = evidenceRoot
			beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "open"}
			if _, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (id, bead_id, worker_id, worktree, qg_evidence_dir, target_sha, target_branch, status)
VALUES (?, ?, ?, ?, ?, ?, 'main', 'requeued')`, assignmentID, beadID, tc.ownerWorker,
				worktree, evidenceRoot, targetSHA); err != nil {
				t.Fatalf("seed requeued assignment: %v", err)
			}
			if tc.addNewerState != "" {
				if _, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (id, bead_id, worker_id, worktree, qg_evidence_dir, target_sha, target_branch, status)
VALUES (?, ?, ?, ?, ?, ?, 'main', ?)`, assignmentID+1, beadID, workerID,
					worktree, evidenceRoot, targetSHA, tc.addNewerState); err != nil {
					t.Fatalf("seed newer terminal assignment: %v", err)
				}
			}
			evidencePath, err := canonicalReadyEvidencePath(evidenceRoot, beadID, assignmentID)
			if err != nil {
				t.Fatalf("canonical evidence path: %v", err)
			}
			writeReadyEvidenceFixture(t, evidencePath, protocol.ReadyForReviewPayload{
				BeadID: beadID, WorkerID: tc.ownerWorker, AssignmentID: assignmentID,
				Worktree: worktree, QGEvidencePath: evidencePath, TargetSHA: targetSHA,
			})

			d.registerWorker(workerID, newMockConn())
			d.handleReconnect(ctx, workerID, protocol.Message{
				Type: protocol.MsgReconnect,
				Reconnect: &protocol.ReconnectPayload{
					WorkerID: workerID, BeadID: beadID, State: "awaiting_review",
					ProtocolVersion: protocol.WorkerProtocolVersion,
					Capabilities:    []string{protocol.CapabilityReadyEvidenceV1},
				},
			})

			var status, durableWorker string
			if err := d.db.QueryRowContext(ctx,
				`SELECT status, worker_id FROM assignments WHERE id = ?`, assignmentID,
			).Scan(&status, &durableWorker); err != nil {
				t.Fatalf("load original assignment: %v", err)
			}
			if status != "requeued" || durableWorker != tc.ownerWorker {
				t.Fatalf("original assignment changed to status %q worker %q; want requeued/%q",
					status, durableWorker, tc.ownerWorker)
			}
			if tc.addNewerState != "" {
				var newerStatus string
				if err := d.db.QueryRowContext(ctx,
					`SELECT status FROM assignments WHERE id = ?`, assignmentID+1,
				).Scan(&newerStatus); err != nil {
					t.Fatalf("load newer terminal assignment: %v", err)
				}
				if newerStatus != tc.addNewerState {
					t.Fatalf("newer assignment status = %q, want %q", newerStatus, tc.addNewerState)
				}
			}
		})
	}
}

type blockingReadyReviewSpawner struct {
	mu      sync.Mutex
	spawns  int
	release chan struct{}
	once    sync.Once
}

func newBlockingReadyReviewSpawner() *blockingReadyReviewSpawner {
	return &blockingReadyReviewSpawner{release: make(chan struct{})}
}

func (s *blockingReadyReviewSpawner) Spawn(context.Context, string, string, string) (ops.Process, error) {
	s.mu.Lock()
	s.spawns++
	s.mu.Unlock()
	return &blockingReadyReviewProcess{release: s.release}, nil
}

func (s *blockingReadyReviewSpawner) spawnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawns
}

func (s *blockingReadyReviewSpawner) releaseAll() {
	s.once.Do(func() { close(s.release) })
}

type blockingReadyReviewProcess struct {
	release <-chan struct{}
}

func (p *blockingReadyReviewProcess) Wait() error {
	<-p.release
	return nil
}

func (p *blockingReadyReviewProcess) Kill() error             { return nil }
func (p *blockingReadyReviewProcess) Output() (string, error) { return "VERDICT: APPROVED", nil }
func (p *blockingReadyReviewProcess) LastOutputAt() time.Time { return time.Time{} }
