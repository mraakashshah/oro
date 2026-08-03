package dispatcher //nolint:testpackage // internal white-box test exercising trackedWorker fields

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestMeaningfulProgressEventsPersisted(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "oro-progress"
		workerID = "worker-progress"
	)
	worktree := t.TempDir()
	assignmentID := seedReviewAssignment(t, d, beadID, workerID, worktree)

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerBusy,
		assignmentID: assignmentID,
		beadID:       beadID,
		worktree:     worktree,
		contextPct:   25,
	}
	d.mu.Unlock()

	// Assignment is a durable progress boundary, independent of worktree reuse.
	d.recordWorkerProgress(ctx, workerID, beadID, "assign")

	// READY_FOR_REVIEW is another durable progress boundary.
	d.handleReadyForReview(ctx, workerID, protocol.Message{
		Type: protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{
			BeadID: beadID, WorkerID: workerID,
		},
	})

	// Only a context_pct increase is durable progress; a flat heartbeat is
	// liveness metadata and must not append another progress event.
	d.handleHeartbeat(ctx, workerID, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			BeadID:     beadID,
			ContextPct: 30,
		},
	})
	d.handleHeartbeat(ctx, workerID, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			BeadID:     beadID,
			ContextPct: 30,
		},
	})

	rows, err := d.db.QueryContext(ctx, `
		SELECT source, created_at
		FROM events
		WHERE type = 'worker_progress' AND bead_id = ? AND worker_id = ?
		ORDER BY id
	`, beadID, workerID)
	if err != nil {
		t.Fatalf("query worker progress events: %v", err)
	}
	defer rows.Close()

	var sources []string
	for rows.Next() {
		var source string
		var createdAt sql.NullString
		if err := rows.Scan(&source, &createdAt); err != nil {
			t.Fatalf("scan worker progress event: %v", err)
		}
		if !createdAt.Valid || createdAt.String == "" {
			t.Fatalf("worker progress event %q has no timestamp", source)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate worker progress events: %v", err)
	}

	want := []string{"assign", "ready_for_review", "context_pct_increase"}
	if len(sources) != len(want) {
		t.Fatalf("worker progress sources = %v, want %v", sources, want)
	}
	for i, source := range sources {
		if source != want[i] {
			t.Fatalf("worker progress source[%d] = %q, want %q", i, source, want[i])
		}
	}
}

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
