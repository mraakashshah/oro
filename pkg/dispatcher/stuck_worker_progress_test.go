package dispatcher //nolint:testpackage // internal white-box test exercising trackedWorker fields

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestStuckWorkerProgressHeartbeatNotProgress proves that heartbeats with
// flat context_pct do NOT refresh lastProgress for a busy worker — only
// real content advancement (context_pct climb, STATUS, DONE) counts.
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

	// Heartbeat with climbing context_pct DOES update lastProgress.
	d.handleHeartbeat(context.Background(), wid, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, BeadID: "oro-bead", ContextPct: 8},
	})
	d.mu.Lock()
	gotClimbed := d.workers[wid].lastProgress
	d.mu.Unlock()
	if !gotClimbed.Equal(now) {
		t.Fatalf("climbing-context lastProgress = %v, want %v (context climb must count as progress)", gotClimbed, now)
	}
}
