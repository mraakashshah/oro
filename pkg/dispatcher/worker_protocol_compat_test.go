package dispatcher //nolint:testpackage // Protocol drain assertions require internal tracked-worker state.

import (
	"context"
	"encoding/json"
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
