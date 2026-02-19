package dispatcher //nolint:testpackage // needs internal access to tracking maps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// countUniqueTrackingKeys returns the number of unique bead IDs across all
// tracking maps. Caller must hold d.mu.
func countUniqueTrackingKeys(d *Dispatcher) int {
	return len(d.allTrackingKeys())
}

// assertTrackingMapsEmpty verifies all five tracking maps are empty for the given beadID.
func assertTrackingMapsEmpty(t *testing.T, d *Dispatcher, beadID string) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()

	if v, ok := d.attemptCounts[beadID]; ok {
		t.Errorf("attemptCounts[%s] = %d, want deleted", beadID, v)
	}
	if v, ok := d.handoffCounts[beadID]; ok {
		t.Errorf("handoffCounts[%s] = %d, want deleted", beadID, v)
	}
	if v, ok := d.rejectionCounts[beadID]; ok {
		t.Errorf("rejectionCounts[%s] = %d, want deleted", beadID, v)
	}
	if _, ok := d.pendingHandoffs[beadID]; ok {
		t.Errorf("pendingHandoffs[%s] still present, want deleted", beadID)
	}
	if _, ok := d.qgStuckTracker[beadID]; ok {
		t.Errorf("qgStuckTracker[%s] still present, want deleted", beadID)
	}
}

// seedTrackingMaps populates all five tracking maps with non-zero entries for beadID.
func seedTrackingMaps(d *Dispatcher, beadID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attemptCounts[beadID] = 1
	d.handoffCounts[beadID] = 1
	d.rejectionCounts[beadID] = 1
	d.pendingHandoffs[beadID] = &pendingHandoff{beadID: beadID, worktree: "/tmp/wt", model: "m"}
	d.qgStuckTracker[beadID] = &qgHistory{hashes: []string{"abc"}}
}

// seedNonQGTrackingMaps populates the tracking maps that do NOT interfere with
// the QG retry/stuck flow. Specifically it skips attemptCounts (would affect
// retry cap), qgStuckTracker (would affect stuck detection), and
// pendingHandoffs (would be consumed by registerWorker). This allows socket-
// based QG tests to verify that unrelated maps are cleaned up on escalation.
func seedNonQGTrackingMaps(d *Dispatcher, beadID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handoffCounts[beadID] = 1
	d.rejectionCounts[beadID] = 1
}

func TestTrackingMaps_ClearedOnEscalation(t *testing.T) {
	t.Run("QG_stuck_escalation_clears_maps", func(t *testing.T) {
		d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadID := "bead-leak-stuck"

		conn, scanner := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Stuck leak test", Priority: 1, Type: "task", Model: protocol.ModelOpus}})

		// Drain initial ASSIGN.
		readMsg(t, conn, 2*time.Second)

		// Seed maps that won't interfere with QG retry/stuck logic.
		// The QG flow itself populates attemptCounts and qgStuckTracker.
		seedNonQGTrackingMaps(d, beadID)

		// Send maxStuckCount identical QG failures to trigger stuck escalation.
		identicalOutput := "FAIL: TestFoo — exact same error"
		for i := 1; i <= maxStuckCount; i++ {
			sendMsg(t, conn, protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:            beadID,
					WorkerID:          "w1",
					QualityGatePassed: false,
					QGOutput:          identicalOutput,
				},
			})
			if i < maxStuckCount {
				readMsgFromScanner(t, scanner, 2*time.Second)
			}
		}

		// Wait for escalation.
		time.Sleep(300 * time.Millisecond)
		msgs := esc.Messages()
		found := false
		for _, m := range msgs {
			if strings.Contains(m, beadID) && strings.Contains(m, "QG output repeated") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected stuck escalation, got: %v", msgs)
		}

		assertTrackingMapsEmpty(t, d, beadID)
	})

	t.Run("QG_retry_cap_escalation_clears_maps", func(t *testing.T) {
		d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadID := "bead-leak-qgcap"

		conn, scanner := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "QG cap leak", Priority: 1, Type: "task", Model: protocol.ModelOpus}})
		readMsg(t, conn, 2*time.Second)

		// Seed maps that won't interfere with QG retry logic.
		seedNonQGTrackingMaps(d, beadID)

		// Send maxQGRetries distinct QG failures (to avoid stuck detection).
		for i := 1; i <= maxQGRetries; i++ {
			sendMsg(t, conn, protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:            beadID,
					WorkerID:          "w1",
					QualityGatePassed: false,
					QGOutput:          fmt.Sprintf("different-error-%d", i),
				},
			})
			if i < maxQGRetries {
				readMsgFromScanner(t, scanner, 2*time.Second)
			}
		}

		time.Sleep(300 * time.Millisecond)
		msgs := esc.Messages()
		found := false
		for _, m := range msgs {
			if strings.Contains(m, beadID) && strings.Contains(m, "quality gate failed") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected QG cap escalation, got: %v", msgs)
		}

		assertTrackingMapsEmpty(t, d, beadID)
	})

	t.Run("review_rejection_cap_escalation_clears_maps", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadID := "bead-leak-review"
		workerID := "w1"

		// Seed tracking maps.
		seedTrackingMaps(d, beadID)

		// Directly call handleReviewResult with rejected verdict exceeding cap.
		// Set rejection count to maxReviewRejections already so next one triggers escalation.
		d.mu.Lock()
		d.rejectionCounts[beadID] = maxReviewRejections
		d.mu.Unlock()

		resultCh := make(chan ops.Result, 1)
		resultCh <- ops.Result{Verdict: ops.VerdictRejected, Feedback: "still broken"}

		ctx := context.Background()
		d.handleReviewResult(ctx, workerID, beadID, resultCh)

		msgs := esc.Messages()
		found := false
		for _, m := range msgs {
			if strings.Contains(m, beadID) && strings.Contains(m, "review rejected") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected review escalation, got: %v", msgs)
		}

		assertTrackingMapsEmpty(t, d, beadID)
	})

	t.Run("review_failed_escalation_clears_maps", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadID := "bead-leak-reviewfail"
		workerID := "w1"

		seedTrackingMaps(d, beadID)

		// Send a review result with unknown verdict (default case = review failed).
		resultCh := make(chan ops.Result, 1)
		resultCh <- ops.Result{Verdict: "error", Feedback: "review process crashed"}

		ctx := context.Background()
		d.handleReviewResult(ctx, workerID, beadID, resultCh)

		msgs := esc.Messages()
		found := false
		for _, m := range msgs {
			if strings.Contains(m, beadID) && strings.Contains(m, "review failed") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected review failed escalation, got: %v", msgs)
		}

		assertTrackingMapsEmpty(t, d, beadID)
	})

	t.Run("diagnosis_escalation_clears_maps", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadID := "bead-leak-diag"
		workerID := "w1"

		seedTrackingMaps(d, beadID)

		// Simulate diagnosis failure (error result).
		resultCh := make(chan ops.Result, 1)
		resultCh <- ops.Result{Err: fmt.Errorf("diagnosis timed out")}

		ctx := context.Background()
		d.handleDiagnosisResult(ctx, beadID, workerID, resultCh)

		msgs := esc.Messages()
		found := false
		for _, m := range msgs {
			if strings.Contains(m, beadID) && strings.Contains(m, "diagnosis failed") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected diagnosis failure escalation, got: %v", msgs)
		}

		assertTrackingMapsEmpty(t, d, beadID)
	})

	t.Run("diagnosis_success_escalation_clears_maps", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadID := "bead-leak-diagok"
		workerID := "w1"

		seedTrackingMaps(d, beadID)

		// Simulate diagnosis success (non-empty feedback, no error).
		resultCh := make(chan ops.Result, 1)
		resultCh <- ops.Result{Feedback: "found root cause: wrong import path"}

		ctx := context.Background()
		d.handleDiagnosisResult(ctx, beadID, workerID, resultCh)

		msgs := esc.Messages()
		found := false
		for _, m := range msgs {
			if strings.Contains(m, beadID) && strings.Contains(m, "diagnosis complete") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected diagnosis complete escalation, got: %v", msgs)
		}

		assertTrackingMapsEmpty(t, d, beadID)
	})

	t.Run("heartbeat_timeout_clears_maps", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadID := "bead-leak-hb"
		workerID := "w-timeout"

		// Manually register a worker that is "expired".
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID, ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		// Set the worker as busy with a bead, and backdate lastSeen.
		d.mu.Lock()
		w := d.workers[workerID]
		w.state = protocol.WorkerBusy
		w.beadID = beadID
		w.lastSeen = d.nowFunc().Add(-2 * d.cfg.HeartbeatTimeout)
		d.mu.Unlock()

		// Seed tracking maps for this bead.
		seedTrackingMaps(d, beadID)

		// Trigger heartbeat check.
		ctx := context.Background()
		d.checkHeartbeats(ctx)

		// Worker should be removed.
		time.Sleep(100 * time.Millisecond)
		if d.ConnectedWorkers() != 0 {
			t.Fatalf("expected 0 workers after heartbeat timeout, got %d", d.ConnectedWorkers())
		}

		assertTrackingMapsEmpty(t, d, beadID)
	})

	t.Run("success_path_still_clears_maps", func(t *testing.T) {
		// Verify the existing success-path cleanup is not broken.
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadID := "bead-leak-success"

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Success test", Priority: 1, Type: "task"}})
		readMsg(t, conn, 2*time.Second)

		// Seed non-QG maps (socket-safe: no pendingHandoffs or attemptCounts).
		seedNonQGTrackingMaps(d, beadID)

		// Send successful DONE.
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            beadID,
				WorkerID:          "w1",
				QualityGatePassed: true,
			},
		})

		// Wait for merge to complete (async).
		time.Sleep(300 * time.Millisecond)

		assertTrackingMapsEmpty(t, d, beadID)
	})
}

// TestClearBeadTracking_DirectCall verifies the clearBeadTracking helper
// clears all five maps in a single call.
func TestClearBeadTracking_DirectCall(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	beadID := "bead-direct"
	seedTrackingMaps(d, beadID)

	// Verify maps are populated.
	d.mu.Lock()
	if d.attemptCounts[beadID] == 0 {
		t.Fatal("setup: attemptCounts should be non-zero")
	}
	d.mu.Unlock()

	d.clearBeadTracking(beadID)

	assertTrackingMapsEmpty(t, d, beadID)
}

// TestClearBeadTracking_IdempotentOnMissingBead verifies clearBeadTracking
// does not panic when called for a bead that has no tracking entries.
func TestClearBeadTracking_IdempotentOnMissingBead(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Should not panic.
	d.clearBeadTracking("nonexistent-bead")

	assertTrackingMapsEmpty(t, d, "nonexistent-bead")
}

// TestPruneStaleTracking verifies that orphaned tracking entries are cleaned
// up within one sweep of pruneStaleTracking.
func TestPruneStaleTracking(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Seed tracking maps for multiple beads.
	orphanedBead1 := "bead-orphan-1"
	orphanedBead2 := "bead-orphan-2"
	activeBead := "bead-active"

	seedTrackingMaps(d, orphanedBead1)
	seedTrackingMaps(d, orphanedBead2)
	seedTrackingMaps(d, activeBead)

	// Create a worker and assign the active bead.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	d.mu.Lock()
	w := d.workers["w1"]
	w.state = protocol.WorkerBusy
	w.beadID = activeBead
	d.mu.Unlock()

	// Run pruneStaleTracking.
	ctx := context.Background()
	d.pruneStaleTracking(ctx)

	// Verify orphaned entries are cleared.
	assertTrackingMapsEmpty(t, d, orphanedBead1)
	assertTrackingMapsEmpty(t, d, orphanedBead2)

	// Verify active bead entries are preserved.
	d.mu.Lock()
	if d.attemptCounts[activeBead] != 1 {
		t.Errorf("active bead attemptCounts should be preserved, got %d", d.attemptCounts[activeBead])
	}
	d.mu.Unlock()
}

// TestQGHistoryCap verifies that qgHistory.hashes is capped at 10 entries
// (sliding window).
func TestQGHistoryCap(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	beadID := "bead-qghistory"

	// Send 15 distinct QG failures to trigger history capping.
	for i := 0; i < 15; i++ {
		output := fmt.Sprintf("error-%d", i)
		_ = d.isQGStuck(beadID, output)
	}

	// Verify history is capped at 10.
	d.mu.Lock()
	hist, ok := d.qgStuckTracker[beadID]
	d.mu.Unlock()

	if !ok {
		t.Fatal("expected qgHistory to exist")
	}
	if len(hist.hashes) > 10 {
		t.Errorf("qgHistory.hashes should be capped at 10, got %d", len(hist.hashes))
	}

	// Verify the latest 10 hashes are retained (sliding window).
	expectedHashes := make([]string, 10)
	for i := 0; i < 10; i++ {
		output := fmt.Sprintf("error-%d", i+5) // last 10: errors 5-14
		expectedHashes[i] = hashQGOutput(output)
	}

	d.mu.Lock()
	for i, h := range hist.hashes {
		if h != expectedHashes[i] {
			t.Errorf("hash[%d]: got %s, want %s", i, h, expectedHashes[i])
		}
	}
	d.mu.Unlock()
}

// TestPruneStaleTracking_ClosedBeadsInSource verifies that beads with
// attemptCounts but closed in the bead source are pruned from tracking maps
// within one heartbeat cycle.
func TestPruneStaleTracking_ClosedBeadsInSource(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	closedBead := "oro-closed"
	readyBead := "oro-ready"

	// Seed tracking maps for both beads.
	seedTrackingMaps(d, closedBead)
	seedTrackingMaps(d, readyBead)

	// Set beadSrc to only return readyBead (closedBead is implicitly closed).
	beadSrc.SetBeads([]protocol.Bead{
		{ID: readyBead, Title: "Ready bead", Priority: 1, Type: "task"},
	})

	// Run pruneStaleTracking.
	ctx := context.Background()
	d.pruneStaleTracking(ctx)

	// Verify closed bead entries are cleared.
	assertTrackingMapsEmpty(t, d, closedBead)

	// Verify ready bead entries are preserved (it's in the ready queue).
	d.mu.Lock()
	if d.attemptCounts[readyBead] != 1 {
		t.Errorf("ready bead attemptCounts should be preserved, got %d", d.attemptCounts[readyBead])
	}
	d.mu.Unlock()
}

// TestBuildStatusJSON_FiltersStaleAttemptCounts verifies that buildStatusJSON
// does not include attempt counts for beads not in ready queue and not assigned
// to any worker.
func TestBuildStatusJSON_FiltersStaleAttemptCounts(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	activeBeadID := "oro-active"
	queuedBeadID := "oro-queued"
	staleBeadID := "oro-stale"

	// Set up beadSrc to return only queuedBeadID in ready queue.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: queuedBeadID, Title: "Queued bead", Priority: 1, Type: "task"},
	})

	// Create a worker and assign activeBeadID.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	d.mu.Lock()
	w := d.workers["w1"]
	w.state = protocol.WorkerBusy
	w.beadID = activeBeadID
	d.mu.Unlock()

	// Seed attemptCounts for all three beads.
	d.mu.Lock()
	d.attemptCounts[activeBeadID] = 2
	d.attemptCounts[queuedBeadID] = 3
	d.attemptCounts[staleBeadID] = 5 // This should NOT appear in status.
	d.mu.Unlock()

	// Build status JSON.
	statusJSON := d.buildStatusJSON()

	// Parse the JSON to verify attemptCounts.
	var status struct {
		AttemptCounts map[string]int `json:"attempt_counts"`
	}
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}

	// Verify activeBeadID and queuedBeadID are present.
	if status.AttemptCounts[activeBeadID] != 2 {
		t.Errorf("activeBeadID attempt count: got %d, want 2", status.AttemptCounts[activeBeadID])
	}
	if status.AttemptCounts[queuedBeadID] != 3 {
		t.Errorf("queuedBeadID attempt count: got %d, want 3", status.AttemptCounts[queuedBeadID])
	}

	// Verify staleBeadID is NOT present.
	if count, exists := status.AttemptCounts[staleBeadID]; exists {
		t.Errorf("staleBeadID should not be in attempt counts, got %d", count)
	}
}

// TestBeadTrackerGrowthIsBounded verifies that pruneStaleTracking keeps map
// sizes bounded. After N beads complete, orphaned entries are pruned within
// one cleanup tick, leaving at most one entry per active bead.
//
// Edges covered:
//   - bead closed without completion event → pruned on next cleanup tick
//   - bead still in_progress (assigned to worker) → NOT pruned
//   - empty maps → cleanup is no-op
func TestBeadTrackerGrowthIsBounded(t *testing.T) {
	t.Run("closed_beads_pruned_leaving_bounded_map", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		const active = 3
		const orphaned = 10

		// Seed orphaned entries (beads that completed or closed externally).
		for i := range orphaned {
			seedTrackingMaps(d, fmt.Sprintf("bead-closed-%d", i))
		}

		// Connect real workers so shutdown sequence can send graceful stop messages.
		activeIDs := make([]string, active)
		for i := range active {
			conn, _ := connectWorker(t, d.cfg.SocketPath)
			wid := fmt.Sprintf("w%d", i)
			sendMsg(t, conn, protocol.Message{
				Type:      protocol.MsgHeartbeat,
				Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, ContextPct: 5},
			})
			activeIDs[i] = fmt.Sprintf("bead-active-%d", i)
		}
		waitForWorkers(t, d, active, 2*time.Second)

		// Seed active tracking entries (acquires its own lock internally).
		for i := range active {
			seedNonQGTrackingMaps(d, activeIDs[i])
		}

		// Assign beads to workers.
		d.mu.Lock()
		for i := range active {
			wid := fmt.Sprintf("w%d", i)
			if w, ok := d.workers[wid]; ok {
				w.state = protocol.WorkerBusy
				w.beadID = activeIDs[i]
			}
		}
		d.mu.Unlock()

		// Bead source returns nothing (orphaned beads are closed).
		beadSrc.SetBeads(nil)

		d.pruneStaleTracking(context.Background())

		// Orphaned entries must be gone.
		for i := range orphaned {
			assertTrackingMapsEmpty(t, d, fmt.Sprintf("bead-closed-%d", i))
		}

		// Map size must be bounded to active beads.
		d.mu.Lock()
		total := countUniqueTrackingKeys(d)
		d.mu.Unlock()

		if total > active {
			t.Errorf("map size after prune = %d, want <= %d (active beads)", total, active)
		}
	})

	t.Run("bead_closed_without_completion_event_pruned_on_tick", func(t *testing.T) {
		// A bead that was closed directly (e.g. `bd close`) without a DONE
		// message reaching the dispatcher — tracking entries leak.
		// pruneStaleTracking must clean these up on the next tick.
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		ghost := "bead-ghost-no-event"
		seedTrackingMaps(d, ghost)

		// No worker assigned, not in ready queue.
		beadSrc.SetBeads(nil)

		d.pruneStaleTracking(context.Background())

		assertTrackingMapsEmpty(t, d, ghost)
	})

	t.Run("in_progress_bead_not_pruned", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		inProgress := "bead-wip"

		// Connect a real worker so shutdown can send graceful stop.
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-wip", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 2*time.Second)

		// Seed tracking maps and mark worker as busy.
		seedNonQGTrackingMaps(d, inProgress)
		d.mu.Lock()
		if w, ok := d.workers["w-wip"]; ok {
			w.state = protocol.WorkerBusy
			w.beadID = inProgress
		}
		d.mu.Unlock()

		beadSrc.SetBeads(nil)

		d.pruneStaleTracking(context.Background())

		// Tracking entries for an in_progress bead must survive.
		d.mu.Lock()
		count := d.handoffCounts[inProgress]
		d.mu.Unlock()

		if count != 1 {
			t.Errorf("in_progress bead should not be pruned; handoffCounts = %d, want 1", count)
		}
	})

	t.Run("empty_maps_cleanup_is_noop", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		beadSrc.SetBeads(nil)

		// Must not panic; total unique keys must remain 0.
		d.pruneStaleTracking(context.Background())

		d.mu.Lock()
		total := countUniqueTrackingKeys(d)
		d.mu.Unlock()

		if total != 0 {
			t.Errorf("empty maps: total keys after noop = %d, want 0", total)
		}
	})
}
