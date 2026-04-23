package dispatcher //nolint:testpackage // white-box: accesses internal dispatcher fields

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestDashboardDataHealth verifies the DashboardData interface implementation on Dispatcher.
func TestDashboardDataHealth(t *testing.T) {
	t.Run("Health returns SwarmHealth matching applyHealth", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		var dd DashboardData = d
		health, err := dd.Health()
		if err != nil {
			t.Fatalf("Health() error: %v", err)
		}

		// Verify result matches what applyHealth() returns
		raw, err := d.applyHealth()
		if err != nil {
			t.Fatalf("applyHealth() error: %v", err)
		}
		var expected SwarmHealth
		if err := json.Unmarshal([]byte(raw), &expected); err != nil {
			t.Fatalf("unmarshal applyHealth: %v", err)
		}

		if health.Daemon.PID != expected.Daemon.PID {
			t.Errorf("Health().Daemon.PID = %d, want %d", health.Daemon.PID, expected.Daemon.PID)
		}
		if health.Daemon.State != expected.Daemon.State {
			t.Errorf("Health().Daemon.State = %q, want %q", health.Daemon.State, expected.Daemon.State)
		}
	})

	t.Run("ReadyBeads delegates to BeadSource.Ready", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.mu.Lock()
		beadSrc.beads = []protocol.Bead{{ID: "oro-r1", Title: "Ready bead"}}
		beadSrc.mu.Unlock()

		var dd DashboardData = d
		beads, err := dd.ReadyBeads(context.Background())
		if err != nil {
			t.Fatalf("ReadyBeads() error: %v", err)
		}
		if len(beads) != 1 {
			t.Fatalf("ReadyBeads() len = %d, want 1", len(beads))
		}
		if beads[0].ID != "oro-r1" {
			t.Errorf("ReadyBeads()[0].ID = %q, want %q", beads[0].ID, "oro-r1")
		}
	})

	t.Run("InProgressBeads delegates to BeadSource.InProgress", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.mu.Lock()
		beadSrc.inProgressBeads = []protocol.Bead{{ID: "oro-ip1", Title: "In progress"}}
		beadSrc.mu.Unlock()

		var dd DashboardData = d
		beads, err := dd.InProgressBeads(context.Background())
		if err != nil {
			t.Fatalf("InProgressBeads() error: %v", err)
		}
		if len(beads) != 1 {
			t.Fatalf("InProgressBeads() len = %d, want 1", len(beads))
		}
		if beads[0].ID != "oro-ip1" {
			t.Errorf("InProgressBeads()[0].ID = %q, want %q", beads[0].ID, "oro-ip1")
		}
	})

	t.Run("BlockedBeads delegates to BeadSource.Blocked", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.mu.Lock()
		beadSrc.blockedBeads = []protocol.Bead{{ID: "oro-b1", Title: "Blocked"}}
		beadSrc.mu.Unlock()

		var dd DashboardData = d
		beads, err := dd.BlockedBeads(context.Background())
		if err != nil {
			t.Fatalf("BlockedBeads() error: %v", err)
		}
		if len(beads) != 1 {
			t.Fatalf("BlockedBeads() len = %d, want 1", len(beads))
		}
		if beads[0].ID != "oro-b1" {
			t.Errorf("BlockedBeads()[0].ID = %q, want %q", beads[0].ID, "oro-b1")
		}
	})

	t.Run("ClosedBeads delegates to BeadSource.Closed", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.mu.Lock()
		beadSrc.closedBeads = []protocol.Bead{{ID: "oro-c1", Title: "Closed"}}
		beadSrc.mu.Unlock()

		var dd DashboardData = d
		beads, err := dd.ClosedBeads(context.Background(), 10)
		if err != nil {
			t.Fatalf("ClosedBeads() error: %v", err)
		}
		if len(beads) != 1 {
			t.Fatalf("ClosedBeads() len = %d, want 1", len(beads))
		}
		if beads[0].ID != "oro-c1" {
			t.Errorf("ClosedBeads()[0].ID = %q, want %q", beads[0].ID, "oro-c1")
		}
	})

	t.Run("ShowBead delegates to BeadSource.Show", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.mu.Lock()
		if beadSrc.shown == nil {
			beadSrc.shown = make(map[string]*protocol.BeadDetail)
		}
		beadSrc.shown["oro-s1"] = &protocol.BeadDetail{ID: "oro-s1", Title: "Show bead"}
		beadSrc.mu.Unlock()

		var dd DashboardData = d
		detail, err := dd.ShowBead(context.Background(), "oro-s1")
		if err != nil {
			t.Fatalf("ShowBead() error: %v", err)
		}
		if detail == nil {
			t.Fatal("ShowBead() returned nil")
		}
		if detail.ID != "oro-s1" {
			t.Errorf("ShowBead().ID = %q, want %q", detail.ID, "oro-s1")
		}
	})

	t.Run("RecentEvents queries events ORDER BY created_at DESC LIMIT N", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		// Insert 3 events with distinct timestamps so ORDER BY is deterministic.
		for _, row := range []struct {
			typ, source, ts string
		}{
			{"event_a", "worker", "2026-01-01 10:00:00"},
			{"event_b", "dispatcher", "2026-01-01 10:00:01"},
			{"event_c", "worker", "2026-01-01 10:00:02"},
		} {
			_, err := d.db.ExecContext(ctx,
				`INSERT INTO events (type, source, created_at) VALUES (?, ?, ?)`,
				row.typ, row.source, row.ts,
			)
			if err != nil {
				t.Fatalf("insert event: %v", err)
			}
		}

		var dd DashboardData = d
		events, err := dd.RecentEvents(ctx, 2)
		if err != nil {
			t.Fatalf("RecentEvents() error: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("RecentEvents(2) len = %d, want 2", len(events))
		}
		// Most recent first — event_c then event_b
		if events[0].Type != "event_c" {
			t.Errorf("events[0].Type = %q, want %q", events[0].Type, "event_c")
		}
		if events[1].Type != "event_b" {
			t.Errorf("events[1].Type = %q, want %q", events[1].Type, "event_b")
		}
	})

	t.Run("RecentEvents returns empty slice when no events", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		var dd DashboardData = d
		events, err := dd.RecentEvents(context.Background(), 10)
		if err != nil {
			t.Fatalf("RecentEvents() error: %v", err)
		}
		if events == nil {
			t.Error("RecentEvents() returned nil, want empty slice")
		}
		if len(events) != 0 {
			t.Errorf("RecentEvents() len = %d, want 0", len(events))
		}
	})

	t.Run("RecentEvents handles NULL columns without panic", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		// Insert event with NULL bead_id, worker_id, payload
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO events (type, source, bead_id, worker_id, payload, created_at) VALUES (?, ?, NULL, NULL, NULL, datetime('now'))`,
			"null_test", "dispatcher",
		)
		if err != nil {
			t.Fatalf("insert event: %v", err)
		}

		var dd DashboardData = d
		events, err := dd.RecentEvents(ctx, 5)
		if err != nil {
			t.Fatalf("RecentEvents() error: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("RecentEvents() len = %d, want 1", len(events))
		}
		// NULL columns should scan as empty strings
		if events[0].BeadID != "" {
			t.Errorf("events[0].BeadID = %q, want empty", events[0].BeadID)
		}
		if events[0].WorkerID != "" {
			t.Errorf("events[0].WorkerID = %q, want empty", events[0].WorkerID)
		}
		if events[0].Payload != "" {
			t.Errorf("events[0].Payload = %q, want empty", events[0].Payload)
		}
	})

	t.Run("SubscribeSSE returns channel that receives broadcast events", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		var dd DashboardData = d
		ch := dd.SubscribeSSE()
		if ch == nil {
			t.Fatal("SubscribeSSE() returned nil channel")
		}

		// Broadcast an event; channel should receive it
		d.sseBroadcaster.Send("test_event", "oro-abc", "worker-1")

		select {
		case msg := <-ch:
			if msg == "" {
				t.Error("received empty SSE message")
			}
		default:
			t.Error("expected message on subscribed channel after Send")
		}

		dd.UnsubscribeSSE(ch)
	})

	t.Run("UnsubscribeSSE stops channel from receiving events", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		var dd DashboardData = d
		ch := dd.SubscribeSSE()

		dd.UnsubscribeSSE(ch)

		// After unsubscribe, Send should not deliver to ch
		d.sseBroadcaster.Send("post_unsub", "oro-x", "w")

		select {
		case msg := <-ch:
			t.Errorf("received message after unsubscribe: %q", msg)
		default:
			// correct — no message
		}
	})

	t.Run("Workers exposes heartbeat age and context", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
		d.nowFunc = func() time.Time { return now }
		d.mu.Lock()
		d.workers["worker-1"] = &trackedWorker{
			id:         "worker-1",
			state:      protocol.WorkerBusy,
			beadID:     "oro-123",
			contextPct: 72,
			lastSeen:   now.Add(-12 * time.Second),
		}
		d.mu.Unlock()

		workers, err := d.Workers(context.Background())
		if err != nil {
			t.Fatalf("Workers() error: %v", err)
		}
		if len(workers) != 1 {
			t.Fatalf("Workers() len = %d, want 1", len(workers))
		}
		if workers[0].LastHeartbeatSecs != 12 {
			t.Errorf("LastHeartbeatSecs = %v, want 12", workers[0].LastHeartbeatSecs)
		}
		if workers[0].ContextPct != 72 {
			t.Errorf("ContextPct = %d, want 72", workers[0].ContextPct)
		}
	})

	t.Run("Throughput counts merged events in the last hour", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		_, err := d.db.ExecContext(ctx, `
			INSERT INTO events (type, source, created_at) VALUES
			('merged', 'dispatcher', datetime('now', '-10 minutes')),
			('merged', 'dispatcher', datetime('now', '-50 minutes')),
			('merged', 'dispatcher', datetime('now', '-2 hours')),
			('quality_gate_rejected', 'dispatcher', datetime('now', '-5 minutes'))
		`)
		if err != nil {
			t.Fatalf("insert events: %v", err)
		}

		data, err := d.Throughput(ctx)
		if err != nil {
			t.Fatalf("Throughput() error: %v", err)
		}
		if data.BeadsPerHour != 2 {
			t.Errorf("BeadsPerHour = %d, want 2", data.BeadsPerHour)
		}
	})
}
