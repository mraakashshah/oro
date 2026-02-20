package dispatcher //nolint:testpackage // needs internal access to db and handleQGFailure

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestPersistAttemptCounts verifies that attempt_count and handoff_count
// are persisted to the SQLite assignments table after QG retries and handoffs.
func TestPersistAttemptCounts(t *testing.T) {
	t.Run("qg_retry_persists_attempt_count", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		conn, scanner := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{
			{ID: "bead-pa1", Title: "Persist attempt test", Priority: 1, Type: "task", Model: protocol.ModelOpus},
		})

		// Drain initial ASSIGN.
		readMsg(t, conn, 2*time.Second)

		// Send a QG failure -- should re-assign and persist attempt_count=1.
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            "bead-pa1",
				WorkerID:          "w1",
				QualityGatePassed: false,
				QGOutput:          "lint failed: unused import",
			},
		})

		// Read the re-ASSIGN message.
		msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
		if !ok {
			t.Fatal("expected re-ASSIGN after QG failure")
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN, got %s", msg.Type)
		}

		// Query the assignments table for attempt_count.
		var attemptCount int
		err := d.db.QueryRow(
			`SELECT attempt_count FROM assignments WHERE bead_id=? AND status='active'`,
			"bead-pa1",
		).Scan(&attemptCount)
		if err != nil {
			t.Fatalf("query attempt_count: %v", err)
		}
		if attemptCount != 1 {
			t.Fatalf("expected attempt_count=1, got %d", attemptCount)
		}
	})

	t.Run("handoff_persists_handoff_count", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{
			{ID: "bead-ho1", Title: "Handoff test", Priority: 1, Type: "task", Model: protocol.ModelSonnet},
		})

		// Wait for the bead to be assigned.
		readMsg(t, conn, 2*time.Second)

		// Send a HANDOFF message.
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgHandoff,
			Handoff: &protocol.HandoffPayload{
				BeadID:   "bead-ho1",
				WorkerID: "w1",
			},
		})

		// Wait for the handoff processing to persist handoff_count.
		waitFor(t, func() bool {
			var hc int
			err := d.db.QueryRow(
				`SELECT handoff_count FROM assignments WHERE bead_id=? AND status='active'`,
				"bead-ho1",
			).Scan(&hc)
			return err == nil && hc == 1
		}, 2*time.Second)

		// Verify final value.
		var handoffCount int
		err := d.db.QueryRow(
			`SELECT handoff_count FROM assignments WHERE bead_id=? AND status='active'`,
			"bead-ho1",
		).Scan(&handoffCount)
		if err != nil {
			t.Fatalf("query handoff_count: %v", err)
		}
		if handoffCount != 1 {
			t.Fatalf("expected handoff_count=1, got %d", handoffCount)
		}
	})

	t.Run("multiple_qg_retries_increment_attempt_count", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		conn, scanner := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{
			{ID: "bead-pa2", Title: "Multi-retry test", Priority: 1, Type: "task", Model: protocol.ModelOpus},
		})

		// Drain initial ASSIGN.
		readMsg(t, conn, 2*time.Second)

		// Send 2 consecutive QG failures.
		for i := 1; i <= 2; i++ {
			sendMsg(t, conn, protocol.Message{
				Type: protocol.MsgDone,
				Done: &protocol.DonePayload{
					BeadID:            "bead-pa2",
					WorkerID:          "w1",
					QualityGatePassed: false,
					QGOutput:          fmt.Sprintf("fail-%d", i),
				},
			})

			// Read the re-ASSIGN.
			msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
			if !ok {
				t.Fatalf("expected re-ASSIGN on attempt %d", i)
			}
			if msg.Type != protocol.MsgAssign {
				t.Fatalf("expected ASSIGN, got %s", msg.Type)
			}
		}

		// Query the assignments table -- attempt_count should reflect the
		// in-memory count. Since the model is already opus, the counter
		// does not reset, so after 2 failures we expect attempt_count=2.
		var attemptCount int
		err := d.db.QueryRow(
			`SELECT attempt_count FROM assignments WHERE bead_id=? AND status='active'`,
			"bead-pa2",
		).Scan(&attemptCount)
		if err != nil {
			t.Fatalf("query attempt_count: %v", err)
		}
		if attemptCount != 2 {
			t.Fatalf("expected attempt_count=2, got %d", attemptCount)
		}
	})
}

// TestPersistBeadCount_NilDB ensures the persist helper is safe when db is nil.
func TestPersistBeadCount_NilDB(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.db = nil

	// Should not panic.
	ctx := context.Background()
	d.persistBeadCount(ctx, "bead-x", "attempt_count", 1)
}

// TestPersistBeadCount_NoMatchingRow ensures no error when there is no active assignment.
func TestPersistBeadCount_NoMatchingRow(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	ctx := context.Background()
	// No assignment row exists for "bead-nope" -- should be a no-op.
	d.persistBeadCount(ctx, "bead-nope", "attempt_count", 5)

	// Verify no rows were affected (the function should not create rows).
	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE bead_id='bead-nope'`).Scan(&count)
	if err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 assignment rows, got %d", count)
	}
}

// TestAssignmentMarkedCompleteOnMerge verifies that after a successful merge,
// the assignments row status is 'completed'. Also verifies QG exhaustion marks complete.
func TestAssignmentMarkedCompleteOnMerge(t *testing.T) {
	t.Run("merge_marks_completed", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-mc1", Title: "Merge complete test", Priority: 1, Type: "task"}})
		readMsg(t, conn, 2*time.Second) // drain ASSIGN

		// Send successful DONE.
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            "bead-mc1",
				WorkerID:          "w1",
				QualityGatePassed: true,
			},
		})

		// Wait for async merge to mark assignment completed.
		waitFor(t, func() bool {
			var s string
			err := d.db.QueryRow(
				`SELECT status FROM assignments WHERE bead_id=?`, "bead-mc1",
			).Scan(&s)
			return err == nil && s == "completed"
		}, 2*time.Second)

		var status string
		err := d.db.QueryRow(
			`SELECT status FROM assignments WHERE bead_id=?`, "bead-mc1",
		).Scan(&status)
		if err != nil {
			t.Fatalf("query status: %v", err)
		}
		if status != "completed" {
			t.Fatalf("expected status='completed', got %q", status)
		}
	})

	t.Run("qg_exhaustion_marks_completed", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		cancel := startDispatcher(t, d)
		defer cancel()

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-mc2", Title: "QG exhaust complete test", Priority: 1, Type: "task"}})
		readMsg(t, conn, 2*time.Second) // drain ASSIGN

		// Seed at maxQGRetries-1 so next failure exhausts.
		d.mu.Lock()
		d.attemptCounts["bead-mc2"] = maxQGRetries - 1
		d.mu.Unlock()

		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            "bead-mc2",
				WorkerID:          "w1",
				QualityGatePassed: false,
				QGOutput:          fmt.Sprintf("unique-exhaust-%d", time.Now().UnixNano()),
			},
		})

		// Wait for QG exhaustion to mark assignment completed.
		waitFor(t, func() bool {
			var s string
			err := d.db.QueryRow(
				`SELECT status FROM assignments WHERE bead_id=?`, "bead-mc2",
			).Scan(&s)
			return err == nil && s == "completed"
		}, 2*time.Second)

		var status string
		err := d.db.QueryRow(
			`SELECT status FROM assignments WHERE bead_id=?`, "bead-mc2",
		).Scan(&status)
		if err != nil {
			t.Fatalf("query status: %v", err)
		}
		if status != "completed" {
			t.Fatalf("expected status='completed' after QG exhaustion, got %q", status)
		}
	})
}

// TestConsolidation_TriggeredAfterNCompletions verifies that memory consolidation
// is triggered after ConsolidateAfterN bead completions.
func TestConsolidation_TriggeredAfterNCompletions(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	d.cfg.ConsolidateAfterN = 2 // trigger after every 2 completions
	cancel := startDispatcher(t, d)
	defer cancel()

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Complete 2 beads — should trigger consolidation after the 2nd.
	for i := 1; i <= 2; i++ {
		beadID := fmt.Sprintf("bead-con%d", i)
		beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: fmt.Sprintf("Consol test %d", i), Priority: 1, Type: "task"}})
		readMsg(t, conn, 2*time.Second) // drain ASSIGN

		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            beadID,
				WorkerID:          "w1",
				QualityGatePassed: true,
			},
		})

		// Wait for the bead's assignment to be marked completed.
		bid := beadID // capture for closure
		waitFor(t, func() bool {
			var s string
			err := d.db.QueryRow(
				`SELECT status FROM assignments WHERE bead_id=?`, bid,
			).Scan(&s)
			return err == nil && s == "completed"
		}, 2*time.Second)
	}

	// Wait for the consolidation goroutine to complete and log its event.
	// (The counter is reset synchronously before the goroutine runs, so
	// waiting for the event is the correct synchronisation point.)
	waitFor(t, func() bool {
		return eventCount(t, d.db, "memory_consolidation") > 0
	}, 3*time.Second)

	// Verify counter was reset after consolidation.
	d.mu.Lock()
	counter := d.completionsSinceConsolidate
	d.mu.Unlock()

	if counter != 0 {
		t.Fatalf("expected completionsSinceConsolidate=0 after consolidation, got %d", counter)
	}

	// Verify consolidation event was logged.
	evCount := eventCount(t, d.db, "memory_consolidation")
	if evCount == 0 {
		t.Fatal("expected memory_consolidation event to be logged, got 0")
	}
}

// Ensure sql import is used.
var _ = (*sql.DB)(nil)
