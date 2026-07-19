package dispatcher //nolint:testpackage // internal white-box test exercising trackedWorker fields

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestStuckWorkerProgressHeartbeatNotProgress proves that heartbeats do NOT
// refresh lastProgress for a busy worker — only real protocol transitions
// such as STATUS, DONE, READY_FOR_REVIEW, and QG events count.
// Covers oro-16yy: in the 2026-05-04 proof run, workers held at 5%
// context for 14+ minutes with only heartbeat traffic, but
// progress-timeout never fired because the heartbeat handler was
// refreshing lastProgress unconditionally.
func TestStuckWorkerProgressHeartbeatNotProgress(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	now := time.Now()
	d.nowFunc = func() time.Time { return now }

	const wid = "w1"
	t0 := now.Add(-30 * time.Minute)
	w := &trackedWorker{
		id:           wid,
		state:        protocol.WorkerBusy,
		beadID:       "oro-bead",
		lastSeen:     now.Add(-1 * time.Second),
		lastProgress: t0,
		contextPct:   5,
	}
	d.mu.Lock()
	d.workers[wid] = w
	d.mu.Unlock()

	// Heartbeat with same context_pct must NOT update lastProgress.
	d.handleHeartbeat(context.Background(), wid, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, BeadID: "oro-bead", ContextPct: 5},
	})
	d.mu.Lock()
	gotFlat := d.workers[wid].lastProgress
	d.mu.Unlock()
	if !gotFlat.Equal(t0) {
		t.Fatalf("flat-heartbeat lastProgress = %v, want unchanged %v (heartbeats alone must not count as progress)", gotFlat, t0)
	}

	// With lastProgress sufficiently stale, the progress-timeout check should fire.
	if !workerProgressTimedOut(w, now, 10*time.Minute) {
		t.Fatalf("workerProgressTimedOut = false, want true (lastProgress is %v ago)", now.Sub(t0))
	}

	// Even a climbing context_pct is heartbeat liveness, not progress.
	d.handleHeartbeat(context.Background(), wid, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, BeadID: "oro-bead", ContextPct: 8},
	})
	d.mu.Lock()
	gotClimbed := d.workers[wid].lastProgress
	d.mu.Unlock()
	if !gotClimbed.Equal(t0) {
		t.Fatalf("climbing-context lastProgress = %v, want unchanged %v (context drift must not count as progress)", gotClimbed, t0)
	}
}
