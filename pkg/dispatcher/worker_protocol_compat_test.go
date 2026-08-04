package dispatcher //nolint:testpackage // Protocol drain assertions require internal tracked-worker state.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

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
