package dispatcher //nolint:testpackage // Admission tests require durable and tracked assignment state.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestCanonicalReadyEvidenceRejectsSymlinkedParents(t *testing.T) {
	t.Parallel()
	for _, parent := range []string{"bead", "assignment"} {
		t.Run(parent, func(t *testing.T) {
			t.Parallel()
			d, ready, workerID, beadID, opsSpawner := newCanonicalReadyAdmissionTest(t, "")
			root := filepath.Dir(filepath.Dir(filepath.Dir(ready.QGEvidencePath)))
			beadDir := filepath.Join(root, beadID)
			assignmentDir := filepath.Join(beadDir, strconv.FormatInt(ready.AssignmentID, 10))
			if err := os.RemoveAll(beadDir); err != nil {
				t.Fatalf("remove canonical evidence fixture: %v", err)
			}
			external := t.TempDir()
			externalEvidence := filepath.Join(external, readyEvidenceAttempt)
			switch parent {
			case "bead":
				externalAssignment := filepath.Join(external, strconv.FormatInt(ready.AssignmentID, 10))
				if err := os.Mkdir(externalAssignment, 0o700); err != nil {
					t.Fatalf("create external assignment: %v", err)
				}
				externalEvidence = filepath.Join(externalAssignment, readyEvidenceAttempt)
				if err := os.Symlink(external, beadDir); err != nil {
					t.Fatalf("symlink bead parent: %v", err)
				}
			case "assignment":
				if err := os.Mkdir(beadDir, 0o700); err != nil {
					t.Fatalf("create bead parent: %v", err)
				}
				if err := os.Symlink(external, assignmentDir); err != nil {
					t.Fatalf("symlink assignment parent: %v", err)
				}
			}
			data, err := json.Marshal(ready)
			if err != nil {
				t.Fatalf("marshal external evidence: %v", err)
			}
			if err := os.WriteFile(externalEvidence, data, 0o600); err != nil {
				t.Fatalf("write external evidence: %v", err)
			}

			d.handleReadyForReview(context.Background(), workerID, protocol.Message{
				Type: protocol.MsgReadyForReview, ReadyForReview: &ready,
			})
			assertReadyAdmissionRejected(t, d, opsSpawner, workerID, beadID, true, protocol.WorkerBusy, time.Unix(123, 0))
			got, err := os.ReadFile(externalEvidence)
			if err != nil {
				t.Fatalf("read external evidence: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("dispatcher mutated external evidence")
			}
		})
	}
}

func TestCanonicalReadyEvidenceClaimsBeforeCheckpoint(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*trackedWorker)
		state  protocol.WorkerState
	}{
		{name: "idle", mutate: func(w *trackedWorker) { w.state = protocol.WorkerIdle }, state: protocol.WorkerIdle},
		{name: "wrong bead", mutate: func(w *trackedWorker) { w.beadID = "oro-wrong-bead" }, state: protocol.WorkerBusy},
		{name: "wrong worktree", mutate: func(w *trackedWorker) { w.worktree = t.TempDir() }, state: protocol.WorkerBusy},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			d, ready, workerID, beadID, opsSpawner := newCanonicalReadyAdmissionTest(t, "")
			d.mu.Lock()
			test.mutate(d.workers[workerID])
			d.mu.Unlock()
			d.handleReadyForReview(context.Background(), workerID, protocol.Message{
				Type: protocol.MsgReadyForReview, ReadyForReview: &ready,
			})
			assertReadyAdmissionRejected(t, d, opsSpawner, workerID, beadID, true, test.state, time.Unix(123, 0))
		})
	}
}

func TestLegacyReadyEvidenceAdmissionMatrix(t *testing.T) {
	t.Parallel()
	const (
		assignmentID = int64(41)
		workerID     = "worker-legacy-ready"
		beadID       = "bead-legacy-ready"
	)
	tests := []struct {
		name                string
		insertAssignment    bool
		assignmentStatus    string
		assignmentWorkerID  string
		assignmentBeadID    string
		assignmentWorktree  string
		qgEvidenceDir       string
		targetSHA           string
		trackWorker         bool
		trackedAssignmentID int64
		trackedBeadID       string
		trackedWorktree     string
		trackedState        protocol.WorkerState
		connectionWorkerID  string
		readyWorkerID       string
		readyBeadID         string
		wantAccepted        bool
	}{
		{name: "exact active legacy assignment", insertAssignment: true, assignmentStatus: "active", trackWorker: true, wantAccepted: true},
		{name: "missing assignment", trackWorker: true},
		{name: "completed assignment", insertAssignment: true, assignmentStatus: "completed", trackWorker: true},
		{name: "wrong durable worker", insertAssignment: true, assignmentStatus: "active", assignmentWorkerID: "other-worker", trackWorker: true},
		{name: "wrong durable bead", insertAssignment: true, assignmentStatus: "active", assignmentBeadID: "other-bead", trackWorker: true},
		{name: "wrong tracked worktree", insertAssignment: true, assignmentStatus: "active", trackWorker: true, trackedWorktree: "other"},
		{name: "wrong tracked assignment", insertAssignment: true, assignmentStatus: "active", trackWorker: true, trackedAssignmentID: assignmentID + 1},
		{name: "tracked worker not busy", insertAssignment: true, assignmentStatus: "active", trackWorker: true, trackedState: protocol.WorkerIdle},
		{name: "missing tracked worker", insertAssignment: true, assignmentStatus: "active"},
		{name: "wrong connection worker", insertAssignment: true, assignmentStatus: "active", trackWorker: true, connectionWorkerID: "other-connection"},
		{name: "wrong payload worker", insertAssignment: true, assignmentStatus: "active", trackWorker: true, readyWorkerID: "other-payload-worker"},
		{name: "wrong payload bead", insertAssignment: true, assignmentStatus: "active", trackWorker: true, readyBeadID: "other-payload-bead"},
		{name: "evidence root only", insertAssignment: true, assignmentStatus: "active", trackWorker: true, qgEvidenceDir: "/tmp/evidence"},
		{name: "target SHA only", insertAssignment: true, assignmentStatus: "active", trackWorker: true, targetSHA: "target-sha"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, _, _, _, _, opsSpawner := newTestDispatcher(t)
			ctx := context.Background()
			worktree := t.TempDir()
			assignmentWorkerID := firstNonEmpty(test.assignmentWorkerID, workerID)
			assignmentBeadID := firstNonEmpty(test.assignmentBeadID, beadID)
			assignmentWorktree := firstNonEmpty(test.assignmentWorktree, worktree)
			status := firstNonEmpty(test.assignmentStatus, "active")
			if test.insertAssignment {
				if _, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (id, bead_id, worker_id, worktree, qg_evidence_dir, target_sha, status)
VALUES (?, ?, ?, ?, ?, ?, ?)`, assignmentID, assignmentBeadID, assignmentWorkerID,
					assignmentWorktree, test.qgEvidenceDir, test.targetSHA, status); err != nil {
					t.Fatalf("insert assignment: %v", err)
				}
			}

			trackedAssignmentID := test.trackedAssignmentID
			if trackedAssignmentID == 0 {
				trackedAssignmentID = assignmentID
			}
			trackedBeadID := firstNonEmpty(test.trackedBeadID, beadID)
			trackedWorktree := worktree
			if test.trackedWorktree == "other" {
				trackedWorktree = t.TempDir()
			} else if test.trackedWorktree != "" {
				trackedWorktree = test.trackedWorktree
			}
			trackedState := test.trackedState
			if trackedState == "" {
				trackedState = protocol.WorkerBusy
			}
			lastProgress := time.Unix(123, 0)
			if test.trackWorker {
				d.workers[workerID] = &trackedWorker{
					id: workerID, state: trackedState, assignmentID: trackedAssignmentID,
					beadID: trackedBeadID, worktree: trackedWorktree, targetBranch: "main",
					lastProgress: lastProgress,
				}
			}

			connectionWorkerID := firstNonEmpty(test.connectionWorkerID, workerID)
			ready := &protocol.ReadyForReviewPayload{
				BeadID:   firstNonEmpty(test.readyBeadID, beadID),
				WorkerID: firstNonEmpty(test.readyWorkerID, workerID),
			}
			if test.wantAccepted {
				admission, accepted := d.admitReadyForReview(ctx, connectionWorkerID, ready)
				if !accepted {
					t.Fatal("exact active legacy assignment was rejected")
				}
				if admission.assignmentID != assignmentID || admission.beadID != beadID || admission.worktree != worktree {
					t.Fatalf("admission = %#v", admission)
				}
				d.mu.Lock()
				state := d.workers[workerID].state
				d.mu.Unlock()
				if state != protocol.WorkerReviewing {
					t.Fatalf("accepted worker state = %q, want %q", state, protocol.WorkerReviewing)
				}
				return
			}

			d.handleReadyForReview(ctx, connectionWorkerID, protocol.Message{
				Type: protocol.MsgReadyForReview, ReadyForReview: ready,
			})
			assertReadyAdmissionRejected(t, d, opsSpawner, workerID, beadID, test.trackWorker, trackedState, lastProgress)
		})
	}
}

func TestCanonicalReadyEvidenceRejectsDurableWorkerMismatch(t *testing.T) {
	t.Parallel()
	d, ready, workerID, beadID, opsSpawner := newCanonicalReadyAdmissionTest(t, "other-worker")
	d.handleReadyForReview(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgReadyForReview, ReadyForReview: &ready,
	})
	assertReadyAdmissionRejected(t, d, opsSpawner, workerID, beadID, true, protocol.WorkerBusy, time.Unix(123, 0))
}

func TestCanonicalReadyEvidenceRejectsCleanEquivalentPath(t *testing.T) {
	t.Parallel()
	d, ready, workerID, beadID, opsSpawner := newCanonicalReadyAdmissionTest(t, "")
	canonicalPath := ready.QGEvidencePath
	ready.QGEvidencePath = filepath.Dir(canonicalPath) + string(filepath.Separator) + "sub" +
		string(filepath.Separator) + ".." + string(filepath.Separator) + readyEvidenceAttempt
	writeReadyEvidenceFixture(t, canonicalPath, ready)
	d.handleReadyForReview(context.Background(), workerID, protocol.Message{
		Type: protocol.MsgReadyForReview, ReadyForReview: &ready,
	})
	assertReadyAdmissionRejected(t, d, opsSpawner, workerID, beadID, true, protocol.WorkerBusy, time.Unix(123, 0))
}

func newCanonicalReadyAdmissionTest(
	t *testing.T,
	durableWorkerID string,
) (*Dispatcher, protocol.ReadyForReviewPayload, string, string, *mockBatchSpawner) {
	t.Helper()
	d, beadSource, _, _, _, opsSpawner := newTestDispatcher(t)
	if err := protocol.MigrateBeadSchema(context.Background(), d.db); err != nil {
		t.Fatalf("migrate canonical READY schema: %v", err)
	}
	const (
		assignmentID = int64(52)
		workerID     = "worker-canonical-ready"
		beadID       = "bead-canonical-ready"
		targetSHA    = "0123456789abcdef0123456789abcdef01234567"
	)
	if durableWorkerID == "" {
		durableWorkerID = workerID
	}
	worktree := t.TempDir()
	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	d.cfg.ReviewEvidenceDir = evidenceRoot
	if _, err := d.db.Exec(`
INSERT INTO assignments (id, bead_id, worker_id, worktree, qg_evidence_dir, target_sha, status)
VALUES (?, ?, ?, ?, ?, ?, 'active')`, assignmentID, beadID, durableWorkerID, worktree, evidenceRoot, targetSHA); err != nil {
		t.Fatalf("insert canonical assignment: %v", err)
	}
	lastProgress := time.Unix(123, 0)
	d.workers[workerID] = &trackedWorker{
		id: workerID, state: protocol.WorkerBusy, assignmentID: assignmentID,
		beadID: beadID, worktree: worktree, targetBranch: "main", lastProgress: lastProgress,
	}
	beadSource.shown[beadID] = &protocol.BeadDetail{ID: beadID, AcceptanceCriteria: "canonical evidence"}
	evidencePath, err := canonicalReadyEvidencePath(evidenceRoot, beadID, assignmentID)
	if err != nil {
		t.Fatalf("canonical evidence path: %v", err)
	}
	ready := protocol.ReadyForReviewPayload{
		BeadID: beadID, WorkerID: workerID, AssignmentID: assignmentID,
		Worktree: worktree, QGEvidencePath: evidencePath, TargetSHA: targetSHA,
	}
	writeReadyEvidenceFixture(t, evidencePath, ready)
	return d, ready, workerID, beadID, opsSpawner
}

func assertReadyAdmissionRejected(
	t *testing.T,
	d *Dispatcher,
	opsSpawner *mockBatchSpawner,
	workerID, beadID string,
	tracked bool,
	wantState protocol.WorkerState,
	wantLastProgress time.Time,
) {
	t.Helper()
	d.mu.Lock()
	w := d.workers[workerID]
	d.mu.Unlock()
	if tracked {
		if w == nil {
			t.Fatal("tracked worker was removed")
		}
		if w.state != wantState || !w.lastProgress.Equal(wantLastProgress) {
			t.Fatalf("worker side effects = state %q, lastProgress %v; want %q, %v",
				w.state, w.lastProgress, wantState, wantLastProgress)
		}
	}
	var events, checkpointTables, checkpoints int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='ready_for_review'`).Scan(&events); err != nil {
		t.Fatalf("count READY events: %v", err)
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='review_checkpoints'`).Scan(&checkpointTables); err != nil {
		t.Fatalf("find checkpoint table: %v", err)
	}
	if checkpointTables > 0 {
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM review_checkpoints`).Scan(&checkpoints); err != nil {
			t.Fatalf("count checkpoints: %v", err)
		}
	}
	if events != 0 || checkpoints != 0 || opsSpawner.SpawnCount() != 0 {
		t.Fatalf("rejected READY side effects = events %d, checkpoints %d, reviews %d (bead %s)",
			events, checkpoints, opsSpawner.SpawnCount(), beadID)
	}
}
