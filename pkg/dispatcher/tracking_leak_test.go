package dispatcher //nolint:testpackage // needs internal access to tracking maps

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

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

		beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Stuck leak test", Priority: 1, Type: "task"}})

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

		beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "QG cap leak", Priority: 1, Type: "task"}})
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
