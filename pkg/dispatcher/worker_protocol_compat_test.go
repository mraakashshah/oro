package dispatcher //nolint:testpackage // Protocol drain assertions require internal tracked-worker state.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestLegacyIdleWorkerIsExplicitlyDrained(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	done := make(chan struct{})
	go func() {
		d.handleConn(context.Background(), server)
		close(done)
	}()

	legacy := protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID: "worker-legacy-idle",
		},
	}
	if err := json.NewEncoder(client).Encode(legacy); err != nil {
		t.Fatalf("send legacy heartbeat: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	var reply protocol.Message
	if err := json.NewDecoder(client).Decode(&reply); err != nil {
		t.Fatalf("decode drain reply: %v", err)
	}
	if reply.Type != protocol.MsgShutdown {
		t.Fatalf("drain reply = %s, want %s", reply.Type, protocol.MsgShutdown)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("legacy idle connection remained open")
	}
	if got := d.ConnectedWorkers(); got != 0 {
		t.Fatalf("connected workers = %d, want 0", got)
	}
	var events int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='worker_protocol_drained' AND worker_id='worker-legacy-idle'`).Scan(&events); err != nil {
		t.Fatalf("count protocol drain events: %v", err)
	}
	if events != 1 {
		t.Fatalf("protocol drain events = %d, want 1", events)
	}
}

func TestLegacyActiveWorkerFinishesButCannotReceiveNewAssignment(t *testing.T) {
	t.Parallel()
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		workerID = "worker-legacy-active"
		beadID   = "oro-legacy-active"
	)
	beads.mu.Lock()
	beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	beads.mu.Unlock()
	insertActiveAssignment(t, d, beadID, workerID, t.TempDir())
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	go d.handleConn(context.Background(), server)

	legacy := protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: workerID,
			BeadID:   beadID,
			State:    "running",
		},
	}
	if err := json.NewEncoder(client).Encode(legacy); err != nil {
		t.Fatalf("send legacy reconnect: %v", err)
	}
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w := d.workers[workerID]
		return w != nil && w.drainAfterAssignment && w.state == protocol.WorkerBusy
	}, time.Second)

	d.mu.Lock()
	w := d.workers[workerID]
	w.state = protocol.WorkerIdle
	w.beadID = ""
	d.mu.Unlock()
	if err := d.assignBead(context.Background(), w, protocol.Bead{ID: "oro-must-not-assign"}); err != nil {
		t.Fatalf("drained assignment guard: %v", err)
	}
	d.mu.Lock()
	state, assignedBead := w.state, w.beadID
	_, assigning := d.assigningBeads["oro-must-not-assign"]
	d.mu.Unlock()
	if state != protocol.WorkerIdle || assignedBead != "" || assigning {
		t.Fatalf("draining worker was assigned: state=%s bead=%q assigning=%v", state, assignedBead, assigning)
	}

	_ = client.Close()
}

func TestLegacyIdleReconnectWithBufferedReadyRestoresOwnership(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		workerID = "worker-legacy-buffered-ready"
		beadID   = "oro-legacy-buffered-ready"
	)
	beads.mu.Lock()
	beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
	beads.mu.Unlock()
	startDispatcher(t, d)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, t.TempDir())
	if _, err := d.db.Exec(`UPDATE assignments SET status='requeued' WHERE id=?`, assignmentID); err != nil {
		t.Fatalf("seed requeued assignment: %v", err)
	}

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendLegacyReconnect(t, conn, protocol.ReconnectPayload{
		WorkerID: workerID,
		BeadID:   beadID,
		State:    "idle",
		BufferedEvents: []protocol.Message{{
			Type: protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{
				WorkerID: workerID,
				BeadID:   beadID,
			},
		}},
	})

	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w := d.workers[workerID]
		return w != nil && w.state == protocol.WorkerReviewing &&
			w.assignmentID == assignmentID && w.beadID == beadID
	}, time.Second)
	var status string
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("load assignment status: %v", err)
	}
	if status != "active" {
		t.Fatalf("assignment status = %q, want active while review owns it", status)
	}
}

func TestLegacyIdleReconnectWithoutBufferedReadyRequeuesBeforeDrain(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		workerID = "worker-legacy-no-ready"
		beadID   = "oro-legacy-no-ready"
	)
	seedLegacyAuthoritativeBead(beads, beadID)
	startDispatcher(t, d)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, t.TempDir())

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendLegacyReconnect(t, conn, protocol.ReconnectPayload{
		WorkerID: workerID,
		BeadID:   beadID,
		State:    "idle",
	})
	reply, ok := readMsg(t, conn, time.Second)
	if !ok || reply.Type != protocol.MsgShutdown {
		t.Fatalf("drain reply = %#v, want SHUTDOWN", reply)
	}
	var status string
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("load assignment status: %v", err)
	}
	if status != "requeued" {
		t.Fatalf("assignment status at drain = %q, want requeued", status)
	}
	assertLegacyBeadOpenAndReady(t, beads, beadID)
	var active int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id=? AND status='active'`, assignmentID).Scan(&active); err != nil {
		t.Fatalf("count active ownership: %v", err)
	}
	d.mu.Lock()
	w := d.workers[workerID]
	inMemoryOwned := w != nil && (w.assignmentID != 0 || w.beadID != "")
	d.mu.Unlock()
	if active != 0 || inMemoryOwned {
		t.Fatalf("ownership after successful drain: active=%d in_memory=%v", active, inMemoryOwned)
	}
}

func TestLegacyIdleReconnectSQLiteStoreDoesNotReenterWriter(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := t.Context()
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate native bead schema: %v", err)
	}
	store := beadstore.NewSQLiteStore(d.db)
	d.beads = store
	const (
		workerID = "worker-legacy-sqlite-release"
		beadID   = "oro-legacy-sqlite-release"
	)
	if _, err := store.Create(ctx, beadstore.CreateParams{
		ID: beadID, Title: "Legacy SQLite release", Type: "task", Status: "in_progress",
	}); err != nil {
		t.Fatalf("create native bead: %v", err)
	}
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, t.TempDir())
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id: workerID, state: protocol.WorkerBusy, assignmentID: assignmentID,
		beadID: beadID, drainAfterAssignment: true,
	}
	d.mu.Unlock()

	reconnectCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if !d.handleLegacyIdleReconnect(reconnectCtx, workerID, &protocol.ReconnectPayload{
		WorkerID: workerID, BeadID: beadID, State: "idle",
	}) {
		t.Fatal("legacy idle reconnect was not handled")
	}
	if err := reconnectCtx.Err(); err != nil {
		t.Fatalf("legacy idle reconnect stalled on its own SQLite writer: %v", err)
	}

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("load released assignment: %v", err)
	}
	bead, err := store.Show(ctx, beadID)
	if err != nil {
		t.Fatalf("show released native bead: %v", err)
	}
	d.mu.Lock()
	workerState := d.workers[workerID].state
	d.mu.Unlock()
	if assignmentStatus != "requeued" || bead.Status != "open" || workerState != protocol.WorkerIdle {
		t.Fatalf("released state = assignment %q bead %q worker %q, want requeued/open/idle",
			assignmentStatus, bead.Status, workerState)
	}
}

func TestLegacyReconnectNeverReservesWriterBeforeValidReadyReleasesDispatcherLock(t *testing.T) {
	d, beads, worktrees, _, _, _ := newTestDispatcher(t)
	useFileBackedLegacyAssignmentDB(t, d)
	ctx := t.Context()
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate READY schema: %v", err)
	}
	const (
		readyWorkerID  = "worker-ready-lock-order"
		readyBeadID    = "oro-ready-lock-order"
		legacyWorkerID = "worker-legacy-lock-order"
		legacyBeadID   = "oro-legacy-lock-order"
		targetSHA      = "0123456789abcdef0123456789abcdef01234567"
	)
	readyWorktree := t.TempDir()
	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	d.cfg.ReviewEvidenceDir = evidenceRoot
	result, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, qg_evidence_dir, target_sha, target_branch, status)
VALUES (?, ?, ?, ?, ?, 'main', 'active')`, readyBeadID, readyWorkerID, readyWorktree, evidenceRoot, targetSHA)
	if err != nil {
		t.Fatalf("insert canonical READY assignment: %v", err)
	}
	readyAssignmentID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("load canonical READY assignment ID: %v", err)
	}
	readyPath, err := canonicalReadyEvidencePath(evidenceRoot, readyBeadID, readyAssignmentID)
	if err != nil {
		t.Fatalf("canonical READY evidence path: %v", err)
	}
	ready := protocol.ReadyForReviewPayload{
		BeadID: readyBeadID, WorkerID: readyWorkerID, AssignmentID: readyAssignmentID,
		Worktree: readyWorktree, QGEvidencePath: readyPath, TargetSHA: targetSHA,
	}
	writeReadyEvidenceFixture(t, readyPath, ready)
	beads.mu.Lock()
	beads.shown[readyBeadID] = &protocol.BeadDetail{
		ID: readyBeadID, AcceptanceCriteria: "canonical lock-order evidence",
	}
	beads.mu.Unlock()

	seedLegacyAuthoritativeBead(beads, legacyBeadID)
	legacyAssignmentID := insertActiveAssignment(t, d, legacyBeadID, legacyWorkerID, t.TempDir())
	d.mu.Lock()
	d.workers[readyWorkerID] = &trackedWorker{
		id: readyWorkerID, state: protocol.WorkerBusy, assignmentID: readyAssignmentID,
		beadID: readyBeadID, worktree: readyWorktree, targetBranch: "main",
	}
	d.workers[legacyWorkerID] = &trackedWorker{
		id: legacyWorkerID, state: protocol.WorkerBusy, assignmentID: legacyAssignmentID,
		beadID: legacyBeadID, drainAfterAssignment: true,
	}
	d.mu.Unlock()

	readyLocked := make(chan struct{})
	continueReady := make(chan struct{})
	worktrees.branchHeadFn = func(branch string) (string, error) {
		if branch == protocol.BranchPrefix+readyBeadID {
			close(readyLocked)
			<-continueReady
		}
		return "head-" + branch, nil
	}
	legacyWriterHeld := make(chan struct{})
	continueLegacy := make(chan struct{})
	legacyPreAdmission := make(chan struct{})
	continueLegacyAdmission := make(chan struct{})
	d.testLegacyReconnectAdmissionHook = func() {
		close(legacyPreAdmission)
		<-continueLegacyAdmission
	}
	d.testLegacyReconnectClaimedHook = func() {
		close(legacyWriterHeld)
		<-continueLegacy
	}

	type readyResult struct {
		identity durableReadyIdentity
		accepted bool
	}
	legacyDone := make(chan bool, 1)
	go func() {
		legacyDone <- d.handleLegacyIdleReconnect(ctx, legacyWorkerID, &protocol.ReconnectPayload{
			WorkerID: legacyWorkerID, BeadID: legacyBeadID, State: "idle",
		})
	}()
	select {
	case <-legacyPreAdmission:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy reconnect did not reach pre-admission boundary")
	}

	readyResults := make(chan readyResult, 1)
	go func() {
		identity, accepted := d.acceptReadyEvidence(ctx, readyWorkerID, &ready)
		readyResults <- readyResult{identity: identity, accepted: accepted}
	}()
	select {
	case <-readyLocked:
	case <-time.After(2 * time.Second):
		t.Fatal("valid READY did not reach dispatcher-locked database boundary")
	}
	close(continueLegacyAdmission)

	legacyReservedWriterFirst := false
	select {
	case <-legacyWriterHeld:
		legacyReservedWriterFirst = true
	case <-time.After(100 * time.Millisecond):
	}
	close(continueReady)

	var gotReady readyResult
	select {
	case gotReady = <-readyResults:
	case <-time.After(2 * time.Second):
		close(continueLegacy)
		t.Fatal("valid READY stalled behind reconnect writer while holding dispatcher lock")
	}
	if !legacyReservedWriterFirst {
		select {
		case <-legacyWriterHeld:
		case <-time.After(2 * time.Second):
			t.Fatal("legacy reconnect did not resume after valid READY released dispatcher lock")
		}
	}
	close(continueLegacy)
	select {
	case handled := <-legacyDone:
		if !handled {
			t.Fatal("legacy reconnect was not handled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("legacy reconnect stalled after lock-order interleaving")
	}
	if legacyReservedWriterFirst {
		t.Fatal("legacy reconnect reserved SQLite writer while valid READY held dispatcher lock")
	}
	if !gotReady.accepted || gotReady.identity.assignmentID != readyAssignmentID {
		t.Fatalf("valid READY result = accepted %t assignment %d, want accepted assignment %d",
			gotReady.accepted, gotReady.identity.assignmentID, readyAssignmentID)
	}
}

func TestCanonicalReconnectNeverReservesWriterBeforeValidReadyReleasesDispatcherLock(t *testing.T) {
	d, beads, worktrees, _, _, _ := newTestDispatcher(t)
	useFileBackedLegacyAssignmentDB(t, d)
	ctx := t.Context()
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		t.Fatalf("migrate READY schema: %v", err)
	}
	const targetSHA = "0123456789abcdef0123456789abcdef01234567"
	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	d.cfg.ReviewEvidenceDir = evidenceRoot
	seedReady := func(workerID, beadID string) (protocol.ReadyForReviewPayload, int64) {
		t.Helper()
		worktree := t.TempDir()
		result, err := d.db.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, qg_evidence_dir, target_sha, target_branch, status)
VALUES (?, ?, ?, ?, ?, 'main', 'active')`, beadID, workerID, worktree, evidenceRoot, targetSHA)
		if err != nil {
			t.Fatalf("insert canonical assignment for %s: %v", beadID, err)
		}
		assignmentID, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("load canonical assignment ID for %s: %v", beadID, err)
		}
		evidencePath, err := canonicalReadyEvidencePath(evidenceRoot, beadID, assignmentID)
		if err != nil {
			t.Fatalf("canonical evidence path for %s: %v", beadID, err)
		}
		ready := protocol.ReadyForReviewPayload{
			BeadID: beadID, WorkerID: workerID, AssignmentID: assignmentID,
			Worktree: worktree, QGEvidencePath: evidencePath, TargetSHA: targetSHA,
		}
		writeReadyEvidenceFixture(t, evidencePath, ready)
		return ready, assignmentID
	}
	const (
		readyWorkerID     = "worker-ready-canonical-order"
		readyBeadID       = "oro-ready-canonical-order"
		reconnectWorkerID = "worker-reconnect-canonical-order"
		reconnectBeadID   = "oro-reconnect-canonical-order"
	)
	ready, readyAssignmentID := seedReady(readyWorkerID, readyBeadID)
	reconnectReady, reconnectAssignmentID := seedReady(reconnectWorkerID, reconnectBeadID)
	beads.mu.Lock()
	beads.shown[readyBeadID] = &protocol.BeadDetail{
		ID: readyBeadID, AcceptanceCriteria: "canonical lock-order evidence",
	}
	beads.mu.Unlock()
	d.mu.Lock()
	d.workers[readyWorkerID] = &trackedWorker{
		id: readyWorkerID, state: protocol.WorkerBusy, assignmentID: readyAssignmentID,
		beadID: readyBeadID, worktree: ready.Worktree, targetBranch: "main",
	}
	d.workers[reconnectWorkerID] = &trackedWorker{id: reconnectWorkerID, state: protocol.WorkerIdle}
	d.mu.Unlock()

	readyLocked := make(chan struct{})
	continueReady := make(chan struct{})
	worktrees.branchHeadFn = func(branch string) (string, error) {
		if branch == protocol.BranchPrefix+readyBeadID {
			close(readyLocked)
			<-continueReady
		}
		return "head-" + branch, nil
	}
	canonicalWriterHeld := make(chan struct{})
	d.testCanonicalReconnectAdmissionHook = func() { close(canonicalWriterHeld) }

	type readyResult struct {
		identity durableReadyIdentity
		accepted bool
	}
	readyResults := make(chan readyResult, 1)
	go func() {
		identity, accepted := d.acceptReadyEvidence(ctx, readyWorkerID, &ready)
		readyResults <- readyResult{identity: identity, accepted: accepted}
	}()
	select {
	case <-readyLocked:
	case <-time.After(2 * time.Second):
		t.Fatal("valid READY did not reach dispatcher-locked database boundary")
	}

	type reconnectResult struct {
		identity durableReadyIdentity
		restored bool
	}
	reconnectResults := make(chan reconnectResult, 1)
	go func() {
		identity, _, _, restored := d.restoreCanonicalReadyReconnect(ctx, reconnectWorkerID, &protocol.ReconnectPayload{
			WorkerID: reconnectWorkerID, BeadID: reconnectBeadID, State: "awaiting_review",
		})
		reconnectResults <- reconnectResult{identity: identity, restored: restored}
	}()
	canonicalReservedWriterFirst := false
	select {
	case <-canonicalWriterHeld:
		canonicalReservedWriterFirst = true
	case <-time.After(100 * time.Millisecond):
	}
	close(continueReady)

	var gotReady readyResult
	select {
	case gotReady = <-readyResults:
	case <-time.After(2 * time.Second):
		t.Fatal("valid READY stalled behind canonical reconnect writer")
	}
	if !canonicalReservedWriterFirst {
		select {
		case <-canonicalWriterHeld:
		case <-time.After(2 * time.Second):
			t.Fatal("canonical reconnect did not resume after valid READY released dispatcher lock")
		}
	}
	var gotReconnect reconnectResult
	select {
	case gotReconnect = <-reconnectResults:
	case <-time.After(2 * time.Second):
		t.Fatal("canonical reconnect stalled after lock-order interleaving")
	}
	if canonicalReservedWriterFirst {
		t.Fatal("canonical reconnect reserved SQLite writer while valid READY held dispatcher lock")
	}
	if !gotReady.accepted || gotReady.identity.assignmentID != readyAssignmentID {
		t.Fatalf("valid READY result = accepted %t assignment %d, want accepted assignment %d",
			gotReady.accepted, gotReady.identity.assignmentID, readyAssignmentID)
	}
	wantReconnectIdentity := durableReadyIdentity{
		assignmentID: reconnectAssignmentID, beadID: reconnectBeadID, workerID: reconnectWorkerID,
		worktree: reconnectReady.Worktree, evidenceRoot: evidenceRoot, targetSHA: targetSHA, targetBranch: "main",
	}
	if !gotReconnect.restored || gotReconnect.identity != wantReconnectIdentity {
		t.Fatalf("canonical reconnect result = restored %t identity %#v", gotReconnect.restored, gotReconnect.identity)
	}
}

func TestLegacyIdleReconnectBeadReopenFailureRetainsOwnership(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		workerID = "worker-legacy-bead-failure"
		beadID   = "oro-legacy-bead-failure"
	)
	seedLegacyAuthoritativeBead(beads, beadID)
	beads.mu.Lock()
	beads.statusIfFn = func(context.Context, string, string, string) (bool, error) {
		return false, errors.New("forced authoritative bead failure")
	}
	beads.mu.Unlock()
	startDispatcher(t, d)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, t.TempDir())

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendLegacyReconnect(t, conn, protocol.ReconnectPayload{WorkerID: workerID, BeadID: beadID, State: "idle"})
	assertLegacyReconnectOwnershipRetained(t, d, conn, workerID, beadID, assignmentID)
	assertLegacyBeadInProgressAndNotReady(t, beads, beadID)
}

func TestLegacyIdleReconnectAssignmentRequeueFailureRetainsOwnership(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		workerID = "worker-legacy-assignment-failure"
		beadID   = "oro-legacy-assignment-failure"
	)
	seedLegacyAuthoritativeBead(beads, beadID)
	startDispatcher(t, d)
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, t.TempDir())
	if _, err := d.db.Exec(`
CREATE TRIGGER fail_legacy_idle_requeue
BEFORE UPDATE OF status ON assignments
WHEN NEW.id = OLD.id AND NEW.status = 'requeued'
BEGIN
  SELECT RAISE(ABORT, 'forced assignment requeue failure');
END`); err != nil {
		t.Fatalf("create assignment failure trigger: %v", err)
	}

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendLegacyReconnect(t, conn, protocol.ReconnectPayload{WorkerID: workerID, BeadID: beadID, State: "idle"})
	assertLegacyReconnectOwnershipRetained(t, d, conn, workerID, beadID, assignmentID)
	assertLegacyBeadInProgressAndNotReady(t, beads, beadID)
}

func TestLegacyIdleReconnectCannotClaimOlderAssignmentThanLiveOwner(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		staleWorkerID = "worker-legacy-stale-owner"
		liveWorkerID  = "worker-current-live-owner"
		beadID        = "oro-legacy-canonical-owner"
	)
	seedLegacyAuthoritativeBead(beads, beadID)
	startDispatcher(t, d)
	staleAssignmentID := insertActiveAssignment(t, d, beadID, staleWorkerID, t.TempDir())
	if _, err := d.db.Exec(`UPDATE assignments SET status='requeued' WHERE id=?`, staleAssignmentID); err != nil {
		t.Fatalf("seed stale requeued assignment: %v", err)
	}
	liveAssignmentID := insertActiveAssignment(t, d, beadID, liveWorkerID, t.TempDir())

	liveConn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, liveConn, protocol.Message{Type: protocol.MsgHeartbeat, Heartbeat: &protocol.HeartbeatPayload{WorkerID: liveWorkerID}})
	waitForWorkers(t, d, 1, time.Second)
	d.mu.Lock()
	d.workers[liveWorkerID].state = protocol.WorkerBusy
	d.workers[liveWorkerID].assignmentID = liveAssignmentID
	d.workers[liveWorkerID].beadID = beadID
	d.mu.Unlock()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w := d.workers[liveWorkerID]
		return w != nil && w.state == protocol.WorkerBusy && w.assignmentID == liveAssignmentID && w.beadID == beadID
	}, time.Second)

	staleConn, _ := connectWorker(t, d.cfg.SocketPath)
	sendLegacyReconnect(t, staleConn, protocol.ReconnectPayload{WorkerID: staleWorkerID, BeadID: beadID, State: "idle"})
	_ = staleConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var reply protocol.Message
	err := json.NewDecoder(staleConn).Decode(&reply)
	if err == nil {
		t.Fatalf("stale reconnect received reply %#v, want fail-closed connection without SHUTDOWN", reply)
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("stale reconnect connection closed: %v", err)
	}
	_ = staleConn.SetReadDeadline(time.Time{})

	d.mu.Lock()
	staleWorker := d.workers[staleWorkerID]
	liveWorker := d.workers[liveWorkerID]
	staleClaimed := staleWorker != nil && (staleWorker.assignmentID != 0 || staleWorker.beadID != "")
	liveOwned := liveWorker != nil && liveWorker.state == protocol.WorkerBusy &&
		liveWorker.assignmentID == liveAssignmentID && liveWorker.beadID == beadID
	d.mu.Unlock()
	if staleClaimed || !liveOwned {
		t.Fatalf("in-memory ownership: stale_claimed=%v live_owned=%v", staleClaimed, liveOwned)
	}
	var staleStatus, liveStatus string
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, staleAssignmentID).Scan(&staleStatus); err != nil {
		t.Fatalf("load stale assignment: %v", err)
	}
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, liveAssignmentID).Scan(&liveStatus); err != nil {
		t.Fatalf("load live assignment: %v", err)
	}
	if staleStatus != "requeued" || liveStatus != "active" {
		t.Fatalf("durable ownership: stale=%q live=%q", staleStatus, liveStatus)
	}
	assertLegacyBeadInProgressAndNotReady(t, beads, beadID)
}

func TestLegacyIdleReconnectOwnershipTransferBeforeRestoreFailsClosed(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		staleWorkerID = "worker-legacy-raced-owner"
		liveWorkerID  = "worker-raced-live-owner"
		beadID        = "oro-legacy-raced-owner"
	)
	useFileBackedLegacyAssignmentDB(t, d)
	seedLegacyAuthoritativeBead(beads, beadID)
	liveWorktree := t.TempDir()
	transferConn := prepareLegacyTransferConn(t, d)
	transferResults := make(chan legacyOwnershipTransferResult, 1)
	d.testLegacyReconnectClaimedHook = func() {
		transferResults <- attemptLegacyOwnershipTransfer(transferConn, beadID, liveWorkerID, liveWorktree)
	}
	startDispatcher(t, d)
	staleAssignmentID := insertActiveAssignment(t, d, beadID, staleWorkerID, t.TempDir())
	if _, err := d.db.Exec(`UPDATE assignments SET status='requeued' WHERE id=?`, staleAssignmentID); err != nil {
		t.Fatalf("seed stale requeued assignment: %v", err)
	}

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendLegacyReconnect(t, conn, protocol.ReconnectPayload{
		WorkerID: staleWorkerID,
		BeadID:   beadID,
		State:    "idle",
		BufferedEvents: []protocol.Message{{
			Type: protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{
				WorkerID: staleWorkerID,
				BeadID:   beadID,
			},
		}},
	})
	transfer := <-transferResults
	assertLegacyTransferSerialized(t, transfer)
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w := d.workers[staleWorkerID]
		if w == nil {
			return false
		}
		return w.assignmentID == staleAssignmentID && w.beadID == beadID
	}, time.Second)

	canonicalAssignmentID, canonicalWorkerID := canonicalLegacyOwner(t, d, beadID)
	d.mu.Lock()
	staleWorker := d.workers[staleWorkerID]
	memoryAssignmentID, memoryBeadID := staleWorker.assignmentID, staleWorker.beadID
	d.mu.Unlock()
	if memoryAssignmentID != staleAssignmentID || memoryBeadID != beadID ||
		canonicalAssignmentID != staleAssignmentID || canonicalWorkerID != staleWorkerID {
		t.Fatalf("serialized ownership mismatch: transfer=%+v memory=(%d,%q) canonical=(%d,%q)",
			transfer, memoryAssignmentID, memoryBeadID, canonicalAssignmentID, canonicalWorkerID)
	}
	assertLegacyBeadInProgressAndNotReady(t, beads, beadID)
}

func TestLegacyIdleReconnectTransferAfterCanonicalVerifyNeverRestoresStaleOwner(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		staleWorkerID = "worker-legacy-post-verify"
		liveWorkerID  = "worker-live-post-verify"
		beadID        = "oro-legacy-post-verify"
	)
	useFileBackedLegacyAssignmentDB(t, d)
	seedLegacyAuthoritativeBead(beads, beadID)
	liveWorktree := t.TempDir()
	transferConn := prepareLegacyTransferConn(t, d)
	transferResults := make(chan legacyOwnershipTransferResult, 1)
	d.testLegacyReconnectVerifiedHook = func() {
		transferResults <- attemptLegacyOwnershipTransfer(transferConn, beadID, liveWorkerID, liveWorktree)
	}
	startDispatcher(t, d)
	staleAssignmentID := insertActiveAssignment(t, d, beadID, staleWorkerID, t.TempDir())

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendLegacyReconnect(t, conn, protocol.ReconnectPayload{
		WorkerID: staleWorkerID,
		BeadID:   beadID,
		State:    "idle",
		BufferedEvents: []protocol.Message{{
			Type: protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{
				WorkerID: staleWorkerID,
				BeadID:   beadID,
			},
		}},
	})
	transfer := <-transferResults
	assertLegacyTransferSerialized(t, transfer)
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w := d.workers[staleWorkerID]
		return w != nil && w.assignmentID != 0
	}, time.Second)

	canonicalAssignmentID, canonicalWorkerID := canonicalLegacyOwner(t, d, beadID)
	d.mu.Lock()
	staleWorker := d.workers[staleWorkerID]
	memoryAssignmentID, memoryBeadID := staleWorker.assignmentID, staleWorker.beadID
	d.mu.Unlock()
	if memoryAssignmentID != canonicalAssignmentID || memoryBeadID != beadID || canonicalWorkerID != staleWorkerID {
		t.Fatalf("post-verify ownership mismatch: transfer=%+v stale_assignment=%d memory=(%d,%q) canonical=(%d,%q)",
			transfer, staleAssignmentID, memoryAssignmentID, memoryBeadID, canonicalAssignmentID, canonicalWorkerID)
	}
	assertLegacyBeadInProgressAndNotReady(t, beads, beadID)
}

func TestLegacyIdleReconnectTransferAfterRequeueNeverReopensOwnedBead(t *testing.T) {
	d, beads, _, _, _, _ := newTestDispatcher(t)
	const (
		staleWorkerID = "worker-legacy-post-requeue"
		liveWorkerID  = "worker-live-post-requeue"
		beadID        = "oro-legacy-post-requeue"
	)
	useFileBackedLegacyAssignmentDB(t, d)
	seedLegacyAuthoritativeBead(beads, beadID)
	liveWorktree := t.TempDir()
	transferConn := prepareLegacyTransferConn(t, d)
	transferResults := make(chan legacyOwnershipTransferResult, 1)
	d.testLegacyReconnectRequeuedHook = func() {
		transferResults <- attemptLegacyAssignmentCreate(transferConn, beadID, liveWorkerID, liveWorktree)
	}
	startDispatcher(t, d)
	staleAssignmentID := insertActiveAssignment(t, d, beadID, staleWorkerID, t.TempDir())

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendLegacyReconnect(t, conn, protocol.ReconnectPayload{WorkerID: staleWorkerID, BeadID: beadID, State: "idle"})
	transfer := <-transferResults
	assertLegacyTransferSerialized(t, transfer)
	reply, ok := readMsg(t, conn, time.Second)
	if !ok || reply.Type != protocol.MsgShutdown {
		t.Fatalf("drain reply = %#v, want SHUTDOWN", reply)
	}

	var activeCount int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`, beadID).Scan(&activeCount); err != nil {
		t.Fatalf("count active assignments: %v", err)
	}
	if activeCount == 0 {
		assertLegacyBeadOpenAndReady(t, beads, beadID)
		return
	}
	canonicalAssignmentID, canonicalWorkerID := canonicalLegacyOwner(t, d, beadID)
	if canonicalAssignmentID != transfer.assignmentID || canonicalWorkerID != liveWorkerID {
		t.Fatalf("unexpected post-requeue owner: transfer=%+v canonical=(%d,%q) stale=%d",
			transfer, canonicalAssignmentID, canonicalWorkerID, staleAssignmentID)
	}
	assertLegacyBeadInProgressAndNotReady(t, beads, beadID)
}

type legacyOwnershipTransferResult struct {
	assignmentID int64
	succeeded    bool
	err          error
}

func attemptLegacyOwnershipTransfer(
	conn *sql.Conn,
	beadID, workerID, worktree string,
) legacyOwnershipTransferResult {
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return legacyOwnershipTransferResult{err: err}
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `ROLLBACK`) }()
	result, err := conn.ExecContext(ctx, `
UPDATE assignments SET status='requeued', completed_at=datetime('now')
WHERE bead_id=? AND status='active'`, beadID)
	if err != nil {
		return legacyOwnershipTransferResult{err: err}
	}
	if rowsAffected(result) != 1 {
		return legacyOwnershipTransferResult{err: errors.New("ownership transfer did not requeue one active assignment")}
	}
	result, err = conn.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES (?, ?, ?, 'active')`, beadID, workerID, worktree)
	if err != nil {
		return legacyOwnershipTransferResult{err: err}
	}
	assignmentID, err := result.LastInsertId()
	if err != nil {
		return legacyOwnershipTransferResult{err: err}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return legacyOwnershipTransferResult{err: err}
	}
	return legacyOwnershipTransferResult{assignmentID: assignmentID, succeeded: true}
}

func attemptLegacyAssignmentCreate(
	conn *sql.Conn,
	beadID, workerID, worktree string,
) legacyOwnershipTransferResult {
	ctx := context.Background()
	result, err := conn.ExecContext(ctx, `
INSERT INTO assignments (bead_id, worker_id, worktree, status)
VALUES (?, ?, ?, 'active')`, beadID, workerID, worktree)
	if err != nil {
		return legacyOwnershipTransferResult{err: err}
	}
	assignmentID, err := result.LastInsertId()
	if err != nil {
		return legacyOwnershipTransferResult{err: err}
	}
	return legacyOwnershipTransferResult{assignmentID: assignmentID, succeeded: true}
}

func prepareLegacyTransferConn(t *testing.T, d *Dispatcher) *sql.Conn {
	t.Helper()
	conn, err := d.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("open legacy transfer connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(context.Background(), `PRAGMA busy_timeout=10`); err != nil {
		t.Fatalf("set legacy transfer busy timeout: %v", err)
	}
	return conn
}

func assertLegacyTransferSerialized(t *testing.T, transfer legacyOwnershipTransferResult) {
	t.Helper()
	if transfer.succeeded {
		t.Fatalf("competing ownership transfer bypassed assignment admission: %+v", transfer)
	}
	if !isSQLiteBusyError(transfer.err) {
		t.Fatalf("competing ownership transfer error = %v, want SQLite writer exclusion", transfer.err)
	}
}

func useFileBackedLegacyAssignmentDB(t *testing.T, d *Dispatcher) {
	t.Helper()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "legacy-assignment.db"))
	if err != nil {
		t.Fatalf("open file-backed legacy assignment database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("initialize file-backed legacy assignment database: %v", err)
	}
	d.db = db
}

func canonicalLegacyOwner(t *testing.T, d *Dispatcher, beadID string) (int64, string) {
	t.Helper()
	var assignmentID int64
	var workerID string
	if err := d.db.QueryRow(`
SELECT id, worker_id FROM assignments
WHERE bead_id=? AND status='active' ORDER BY id DESC LIMIT 1`, beadID).Scan(&assignmentID, &workerID); err != nil {
		t.Fatalf("load canonical legacy owner: %v", err)
	}
	return assignmentID, workerID
}

func sendLegacyReconnect(t *testing.T, conn net.Conn, reconnect protocol.ReconnectPayload) {
	t.Helper()
	if err := json.NewEncoder(conn).Encode(protocol.Message{
		Type:      protocol.MsgReconnect,
		Reconnect: &reconnect,
	}); err != nil {
		t.Fatalf("send legacy reconnect: %v", err)
	}
}

func seedLegacyAuthoritativeBead(beads *fakeBeadStore, beadID string) {
	bead := protocol.Bead{ID: beadID, Status: "in_progress"}
	beads.mu.Lock()
	defer beads.mu.Unlock()
	beads.shown[beadID] = &bead
	beads.inProgressBeads = []protocol.Bead{bead}
	beads.beads = nil
	if beads.updated == nil {
		beads.updated = make(map[string]string)
	}
	beads.updated[beadID] = "in_progress"
	beads.statusIfFn = func(_ context.Context, id, expected, next string) (bool, error) {
		beads.mu.Lock()
		defer beads.mu.Unlock()
		if id != beadID || beads.updated[id] != expected {
			return false, nil
		}
		beads.updated[id] = next
		beads.shown[id].Status = next
		if next == "open" {
			beads.inProgressBeads = nil
			beads.beads = []protocol.Bead{*beads.shown[id]}
		}
		return true, nil
	}
}

func assertLegacyReconnectOwnershipRetained(
	t *testing.T,
	d *Dispatcher,
	conn net.Conn,
	workerID, beadID string,
	assignmentID int64,
) {
	t.Helper()
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w := d.workers[workerID]
		return w != nil && w.state == protocol.WorkerBusy &&
			w.assignmentID == assignmentID && w.beadID == beadID
	}, time.Second)
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var reply protocol.Message
	err := json.NewDecoder(conn).Decode(&reply)
	if err == nil {
		t.Fatalf("unexpected reply while ownership retained: %#v", reply)
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("connection closed while ownership retained: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	var status string
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("load retained assignment: %v", err)
	}
	if status != "active" {
		t.Fatalf("retained assignment status = %q, want active", status)
	}
}

func assertLegacyBeadOpenAndReady(t *testing.T, beads *fakeBeadStore, beadID string) {
	t.Helper()
	detail, err := beads.Show(context.Background(), beadID)
	if err != nil || detail == nil || detail.Status != "open" {
		t.Fatalf("authoritative bead after release = %#v, err=%v; want open", detail, err)
	}
	ready, err := beads.Ready(context.Background())
	if err != nil {
		t.Fatalf("load authoritative ready beads: %v", err)
	}
	if !containsLegacyReadyBead(ready, beadID) {
		t.Fatalf("authoritative Ready() = %#v, want %s", ready, beadID)
	}
}

func assertLegacyBeadInProgressAndNotReady(t *testing.T, beads *fakeBeadStore, beadID string) {
	t.Helper()
	detail, err := beads.Show(context.Background(), beadID)
	if err != nil || detail == nil || detail.Status != "in_progress" {
		t.Fatalf("authoritative bead after failed release = %#v, err=%v; want in_progress", detail, err)
	}
	ready, err := beads.Ready(context.Background())
	if err != nil {
		t.Fatalf("load authoritative ready beads: %v", err)
	}
	if containsLegacyReadyBead(ready, beadID) {
		t.Fatalf("failed release made %s Ready: %#v", beadID, ready)
	}
}

func containsLegacyReadyBead(beads []protocol.Bead, beadID string) bool {
	for _, bead := range beads {
		if bead.ID == beadID {
			return true
		}
	}
	return false
}
