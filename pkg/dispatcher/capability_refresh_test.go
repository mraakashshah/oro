package dispatcher //nolint:testpackage // white-box test verifies durable refresh transitions.

import (
	"context"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestCapabilityRefreshSnapshotsWorkerIdentity(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.nowFunc = func() time.Time { return now }
	assignmentID, err := d.createAssignment(ctx, "refresh-race-bead", "refresh-race-worker", t.TempDir())
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	if _, err := d.issueAssignmentCapability(ctx, assignmentID, 1, ActorRoleExecutionWorker); err != nil {
		t.Fatalf("issue predecessor: %v", err)
	}
	worker := &trackedWorker{
		id:           "refresh-race-worker",
		state:        protocol.WorkerBusy,
		conn:         newMockConn(),
		assignmentID: assignmentID,
		execution: WorkerExecutionContext{
			AssignmentID: assignmentID,
			Generation:   1,
			ActorRole:    string(ActorRoleExecutionWorker),
		},
	}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()

	stop := make(chan struct{})
	started := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		close(started)
		generation := int64(1)
		for {
			select {
			case <-stop:
				return
			default:
				generation = 3 - generation
				d.mu.Lock()
				worker.execution.Generation = generation
				d.mu.Unlock()
			}
		}
	}()
	<-started
	now = now.Add(assignmentCapabilityLifetime - capabilityRefreshLead)
	for range 100 {
		if err := d.refreshExpiringCapabilities(ctx, now); err != nil {
			close(stop)
			writer.Wait()
			t.Fatalf("refresh capabilities: %v", err)
		}
	}
	close(stop)
	writer.Wait()
}

func TestCapabilityRefreshCrashPoints(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	newLiveDispatcher := func(t *testing.T, now *time.Time) (*Dispatcher, int64, *mockConn) {
		t.Helper()
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.nowFunc = func() time.Time { return *now }
		assignmentID, err := d.createAssignment(ctx, "refresh-bead", "refresh-worker", t.TempDir())
		if err != nil {
			t.Fatalf("create assignment: %v", err)
		}
		if _, err := d.issueAssignmentCapability(ctx, assignmentID, 1, ActorRoleExecutionWorker); err != nil {
			t.Fatalf("issue predecessor: %v", err)
		}
		conn := newMockConn()
		d.mu.Lock()
		d.workers["refresh-worker"] = &trackedWorker{
			id:           "refresh-worker",
			state:        protocol.WorkerBusy,
			conn:         conn,
			assignmentID: assignmentID,
			execution: WorkerExecutionContext{
				AssignmentID: assignmentID,
				Generation:   1,
				ActorRole:    string(ActorRoleExecutionWorker),
			},
		}
		d.mu.Unlock()
		return d, assignmentID, conn
	}

	t.Run("restart before commit mints a replacement", func(t *testing.T) {
		now := start
		d, assignmentID, conn := newLiveDispatcher(t, &now)
		now = now.Add(assignmentCapabilityLifetime - capabilityRefreshLead)
		if err := d.refreshExpiringCapabilities(ctx, now); err != nil {
			t.Fatalf("refresh after simulated pre-commit restart: %v", err)
		}
		assertRefreshPending(t, d, assignmentID, conn)
	})

	t.Run("expired predecessor during downtime installs replacement before request", func(t *testing.T) {
		now := start
		d, assignmentID, conn := newLiveDispatcher(t, &now)
		now = now.Add(assignmentCapabilityLifetime + time.Second)
		if err := d.refreshExpiringCapabilities(ctx, now); err != nil {
			t.Fatalf("refresh expired predecessor: %v", err)
		}
		assertRefreshPending(t, d, assignmentID, conn)
	})

	t.Run("restart after commit supersedes unacknowledged replacement and mints", func(t *testing.T) {
		now := start
		d, assignmentID, conn := newLiveDispatcher(t, &now)
		now = now.Add(assignmentCapabilityLifetime - capabilityRefreshLead)
		if err := d.refreshExpiringCapabilities(ctx, now); err != nil {
			t.Fatalf("initial refresh: %v", err)
		}
		if err := d.refreshExpiringCapabilities(ctx, now); err != nil {
			t.Fatalf("refresh after simulated post-commit restart: %v", err)
		}
		assertRefreshPending(t, d, assignmentID, conn)
		var superseded int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignment_capabilities WHERE assignment_id=? AND state='superseded'`, assignmentID).Scan(&superseded); err != nil {
			t.Fatalf("count superseded: %v", err)
		}
		if superseded != 1 {
			t.Fatalf("superseded replacements = %d, want 1", superseded)
		}
	})

	t.Run("restart after send supersedes and mints", func(t *testing.T) {
		now := start
		d, assignmentID, conn := newLiveDispatcher(t, &now)
		now = now.Add(assignmentCapabilityLifetime - capabilityRefreshLead)
		if err := d.refreshExpiringCapabilities(ctx, now); err != nil {
			t.Fatalf("initial refresh: %v", err)
		}
		if lastMockConnMessage(t, conn).Type != protocol.MsgCapabilityRefresh {
			t.Fatal("refresh was not delivered before simulated restart")
		}
		if err := d.refreshExpiringCapabilities(ctx, now); err != nil {
			t.Fatalf("refresh after simulated post-send restart: %v", err)
		}
		assertRefreshPending(t, d, assignmentID, conn)
	})

	t.Run("ack revokes predecessor and preserves usable replacement", func(t *testing.T) {
		now := start
		d, assignmentID, conn := newLiveDispatcher(t, &now)
		now = now.Add(assignmentCapabilityLifetime - capabilityRefreshLead)
		if err := d.refreshExpiringCapabilities(ctx, now); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		msg := lastMockConnMessage(t, conn)
		d.handleCapabilityRefreshAck(ctx, "refresh-worker", protocol.Message{
			Type:                 protocol.MsgCapabilityRefreshACK,
			CapabilityRefreshACK: &protocol.CapabilityRefreshACKPayload{AssignmentID: assignmentID, CapabilityID: msg.CapabilityRefresh.CapabilityID},
		})
		var active, revoked int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignment_capabilities WHERE assignment_id=? AND state='active'`, assignmentID).Scan(&active); err != nil {
			t.Fatalf("count active: %v", err)
		}
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignment_capabilities WHERE assignment_id=? AND state='revoked'`, assignmentID).Scan(&revoked); err != nil {
			t.Fatalf("count revoked: %v", err)
		}
		if active != 1 || revoked != 1 {
			t.Fatalf("states after ACK: active=%d revoked=%d, want 1 each", active, revoked)
		}
	})
}

func assertRefreshPending(t *testing.T, d *Dispatcher, assignmentID int64, conn *mockConn) {
	t.Helper()
	msg := lastMockConnMessage(t, conn)
	if msg.Type != protocol.MsgCapabilityRefresh || msg.CapabilityRefresh == nil || msg.CapabilityRefresh.Capability == "" {
		t.Fatalf("refresh message = %#v, want credential refresh", msg)
	}
	var pending int
	if err := d.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM assignment_capabilities WHERE assignment_id=? AND state='pending'`, assignmentID).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending replacements = %d, want 1", pending)
	}
}
