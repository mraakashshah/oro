//nolint:testpackage // Production-path regression exercises dispatcher internals and the real UDS transport.
package dispatcher

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestReadyEvidenceProductionAssignPath(t *testing.T) {
	d, beadSrc, worktrees, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-ready-evidence"
		workerID = "worker-ready-evidence"
	)

	worktree := t.TempDir()
	evidenceRoot := filepath.Join(t.TempDir(), "review-evidence")
	d.cfg.ReviewEvidenceDir = evidenceRoot
	d.cfg.HeartbeatTimeout = 5 * time.Second
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		return worktree, protocol.BranchPrefix + beadID, nil
	}
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Title: "Ready evidence", AcceptanceCriteria: "QG evidence accepted"}

	startDispatcher(t, d)
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID},
	})
	waitForWorkers(t, d, 1, time.Second)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, time.Second)
	beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Ready evidence", AcceptanceCriteria: "QG evidence accepted", Priority: 0}})

	assignMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok || assignMsg.Type != protocol.MsgAssign || assignMsg.Assign == nil {
		var events string
		_ = d.db.QueryRow(`SELECT COALESCE(group_concat(type || ':' || COALESCE(payload, ''), '|'), '') FROM events`).Scan(&events)
		t.Fatalf("received message = %#v, want ASSIGN; events=%s", assignMsg, events)
	}
	assign := assignMsg.Assign
	if assign.AssignmentID <= 0 || assign.QGEvidenceDir != evidenceRoot || assign.TargetSHA == "" {
		t.Fatalf("ASSIGN evidence identity = assignment %d, dir %q, target %q", assign.AssignmentID, assign.QGEvidenceDir, assign.TargetSHA)
	}

	var persisted struct {
		beadID, workerID, worktree, evidenceDir, targetSHA string
	}
	if err := d.db.QueryRowContext(ctx, `
SELECT bead_id, worker_id, worktree, qg_evidence_dir, target_sha
FROM assignments WHERE id = ?`, assign.AssignmentID).Scan(
		&persisted.beadID, &persisted.workerID, &persisted.worktree, &persisted.evidenceDir, &persisted.targetSHA,
	); err != nil {
		t.Fatalf("load durable assignment identity: %v", err)
	}
	if persisted != (struct{ beadID, workerID, worktree, evidenceDir, targetSHA string }{
		beadID, workerID, worktree, evidenceRoot, assign.TargetSHA,
	}) {
		t.Fatalf("durable identity = %#v", persisted)
	}

	evidencePath := filepath.Join(evidenceRoot, beadID, strconv.FormatInt(assign.AssignmentID, 10), "1.json")
	writeReadyEvidenceFixture(t, evidencePath, protocol.ReadyForReviewPayload{
		BeadID: beadID, WorkerID: workerID, AssignmentID: assign.AssignmentID,
		Worktree: worktree, QGEvidencePath: evidencePath, TargetSHA: assign.TargetSHA,
	})

	missing := protocol.ReadyForReviewPayload{BeadID: beadID, WorkerID: workerID}
	sendReadyWithoutFixtureHydration(t, conn, missing)
	time.Sleep(50 * time.Millisecond)
	assertReadyRejectedWithoutSideEffects(t, d, workerID, beadID)

	bad := protocol.ReadyForReviewPayload{
		BeadID: beadID, WorkerID: workerID, AssignmentID: assign.AssignmentID,
		Worktree: worktree, QGEvidencePath: filepath.Join(evidenceRoot, "wrong", "1.json"), TargetSHA: assign.TargetSHA,
	}
	sendMsg(t, conn, protocol.Message{Type: protocol.MsgReadyForReview, ReadyForReview: &bad})
	time.Sleep(50 * time.Millisecond)
	assertReadyRejectedWithoutSideEffects(t, d, workerID, beadID)

	ready := protocol.ReadyForReviewPayload{
		BeadID: beadID, WorkerID: workerID, AssignmentID: assign.AssignmentID,
		Worktree: worktree, QGEvidencePath: evidencePath, TargetSHA: assign.TargetSHA,
	}
	sendMsg(t, conn, protocol.Message{Type: protocol.MsgReadyForReview, ReadyForReview: &ready})
	waitFor(t, func() bool {
		var state string
		return d.db.QueryRowContext(ctx,
			`SELECT state FROM review_checkpoints WHERE origin_assignment_id = ?`, assign.AssignmentID,
		).Scan(&state) == nil && state == string(ReviewCheckpointStateQGPassed)
	}, time.Second)
}

func sendReadyWithoutFixtureHydration(t *testing.T, conn net.Conn, ready protocol.ReadyForReviewPayload) {
	t.Helper()
	data, err := json.Marshal(protocol.Message{Type: protocol.MsgReadyForReview, ReadyForReview: &ready})
	if err != nil {
		t.Fatalf("marshal READY: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write READY: %v", err)
	}
}

func writeReadyEvidenceFixture(t *testing.T, path string, ready protocol.ReadyForReviewPayload) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}
	data, err := json.Marshal(ready)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
}

func assertReadyRejectedWithoutSideEffects(t *testing.T, d *Dispatcher, workerID, beadID string) {
	t.Helper()
	d.mu.Lock()
	state := d.workers[workerID].state
	d.mu.Unlock()
	if state != protocol.WorkerBusy {
		t.Fatalf("worker state after rejected READY = %q, want %q", state, protocol.WorkerBusy)
	}
	var readyEvents, checkpoints int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='ready_for_review' AND bead_id=?`, beadID).Scan(&readyEvents); err != nil {
		t.Fatalf("count READY events: %v", err)
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM review_checkpoints WHERE bead_id=?`, beadID).Scan(&checkpoints); err != nil {
		t.Fatalf("count checkpoints: %v", err)
	}
	if readyEvents != 0 || checkpoints != 0 {
		t.Fatalf("rejected READY side effects = events %d, checkpoints %d", readyEvents, checkpoints)
	}
}
