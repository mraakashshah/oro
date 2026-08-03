package dispatcher //nolint:testpackage // Restart admission requires internal worker and checkpoint state.

import (
	"context"
	"os/exec"
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
			if err := protocol.MigrateBeadSchema(ctx, original.db); err != nil {
				t.Fatalf("migrate restart schema: %v", err)
			}
			const (
				assignmentID = int64(71)
				workerID     = "worker-ready-restart"
				beadID       = "oro-ready-restart"
				targetSHA    = "0123456789abcdef0123456789abcdef01234567"
				targetBranch = "epic/oro-parent"
			)
			worktree := t.TempDir()
			if err := exec.Command("git", "-C", worktree, "init").Run(); err != nil {
				t.Fatalf("initialize restart worktree: %v", err)
			}
			evidenceRoot := filepath.Join(t.TempDir(), "review-evidence")
			original.cfg.ReviewEvidenceDir = evidenceRoot
			if _, err := original.db.ExecContext(ctx, `
INSERT INTO assignments (id, bead_id, worker_id, worktree, qg_evidence_dir, target_sha, target_branch, status)
VALUES (?, ?, ?, ?, ?, ?, ?, 'active')`, assignmentID, beadID, workerID, worktree,
				evidenceRoot, targetSHA, targetBranch); err != nil {
				t.Fatalf("seed canonical assignment: %v", err)
			}
			beads.shown[beadID] = &protocol.BeadDetail{
				ID: beadID, Status: "in_progress", AcceptanceCriteria: "restart resumes review",
			}
			evidencePath, err := canonicalReadyEvidencePath(evidenceRoot, beadID, assignmentID)
			if err != nil {
				t.Fatalf("canonical evidence path: %v", err)
			}
			ready := protocol.ReadyForReviewPayload{
				BeadID: beadID, WorkerID: workerID, AssignmentID: assignmentID, Worktree: worktree,
				QGEvidencePath: evidencePath, TargetSHA: targetSHA,
			}
			writeReadyEvidenceFixture(t, evidencePath, ready)

			blockingSpawner := newBlockingReadyReviewSpawner()
			t.Cleanup(blockingSpawner.releaseAll)
			restarted, err := New(original.cfg, original.db, merge.NewCoordinator(gitRunner),
				ops.NewSpawner(blockingSpawner), beads, worktrees, escalator, nil,
				WithMemoryServices(newTestMemoryServices(original.db)))
			if err != nil {
				t.Fatalf("construct restarted dispatcher: %v", err)
			}
			restarted.registerWorker(workerID, newMockConn())
			reconnect := protocol.ReconnectPayload{
				WorkerID: workerID, BeadID: beadID, State: "awaiting_review",
			}
			if buffered {
				reconnect.BufferedEvents = []protocol.Message{{
					Type: protocol.MsgReadyForReview, ReadyForReview: &ready,
				}}
			}

			restarted.handleReconnect(ctx, workerID, protocol.Message{Type: protocol.MsgReconnect, Reconnect: &reconnect})
			waitFor(t, func() bool { return blockingSpawner.spawnCount() == 1 }, time.Second)
			restarted.handleReconnect(ctx, workerID, protocol.Message{Type: protocol.MsgReconnect, Reconnect: &reconnect})
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
			if gotState != protocol.WorkerReviewing || gotAssignment != assignmentID || gotBead != beadID ||
				gotWorktree != worktree || gotEvidenceRoot != evidenceRoot || gotEvidencePath != evidencePath ||
				gotTargetSHA != targetSHA || gotTargetBranch != targetBranch {
				t.Fatalf("restored worker = state %q assignment %d bead %q worktree %q evidence root %q path %q target %q branch %q",
					gotState, gotAssignment, gotBead, gotWorktree, gotEvidenceRoot, gotEvidencePath, gotTargetSHA, gotTargetBranch)
			}
			var checkpoints int
			if err := restarted.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM review_checkpoints
WHERE origin_assignment_id = ? AND worktree = ? AND target_branch = ? AND target_sha = ?
  AND qg_evidence_path = ?`, assignmentID, worktree, targetBranch, targetSHA, evidencePath).Scan(&checkpoints); err != nil {
				t.Fatalf("count restored checkpoints: %v", err)
			}
			if checkpoints != 1 {
				t.Fatalf("restored checkpoint count = %d, want 1", checkpoints)
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
