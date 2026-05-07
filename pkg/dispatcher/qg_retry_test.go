package dispatcher //nolint:testpackage // needs internal access to attemptCounts

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// readMsgFromScanner reads one line-delimited JSON message from an existing scanner.
func readMsgFromScanner(t *testing.T, scanner *bufio.Scanner, timeout time.Duration) (protocol.Message, bool) {
	t.Helper()
	done := make(chan struct{})
	var msg protocol.Message
	var ok bool
	go func() {
		defer close(done)
		if !scanner.Scan() {
			return
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return
		}
		ok = true
	}()

	select {
	case <-done:
		return msg, ok
	case <-time.After(timeout):
		return protocol.Message{}, false
	}
}

func TestHandleDone_QGFailRetryIncrementsAttempt(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg1", Title: "QG test", Priority: 1, Type: "task", Model: protocol.ModelOpus}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send 3 QG failures — first 2 should re-assign, 3rd should escalate.
	for i := 1; i <= maxQGRetries; i++ {
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            "bead-qg1",
				WorkerID:          "w1",
				QualityGatePassed: false,
				QGOutput:          fmt.Sprintf("unclassified-retry-%d", i),
			},
		})

		if i < maxQGRetries {
			// Should get a re-ASSIGN with incremented attempt.
			msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
			if !ok {
				t.Fatalf("expected re-ASSIGN on attempt %d", i)
			}
			if msg.Type != protocol.MsgAssign {
				t.Fatalf("expected ASSIGN, got %s", msg.Type)
			}
			if msg.Assign.Attempt != i {
				t.Fatalf("expected Attempt=%d, got %d", i, msg.Assign.Attempt)
			}
			waitFor(t, func() bool {
				return eventCount(t, d.db, "qg_retry_assign_sent") >= i
			}, 1*time.Second)
		}
	}

	// After maxQGRetries, should escalate to manager.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, "bead-qg1") && strings.Contains(m, "quality gate failed") {
				return true
			}
		}
		return false
	}, 2*time.Second)
	msgs := esc.Messages()
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "bead-qg1") && strings.Contains(m, "quality gate failed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected escalation for bead-qg1, got messages: %v", msgs)
	}
}

func TestHandleDone_QGFailRetryAttemptContinuesAcrossModelEscalation(t *testing.T) {
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-escalate", Title: "QG escalation attempt test", Priority: 1, Type: "task", Model: protocol.ModelSonnet}})
	readMsg(t, conn, 2*time.Second)

	for attempt := 1; attempt <= 2; attempt++ {
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            "bead-qg-escalate",
				WorkerID:          "w1",
				QualityGatePassed: false,
				QGOutput:          fmt.Sprintf("unique-sonnet-opus-fail-%d", attempt),
			},
		})

		msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
		if !ok {
			t.Fatalf("expected re-ASSIGN on attempt %d", attempt)
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN on attempt %d, got %s", attempt, msg.Type)
		}
		if msg.Assign.Model != protocol.ModelOpus {
			t.Fatalf("expected retry model opus on attempt %d, got %q", attempt, msg.Assign.Model)
		}
		if msg.Assign.Attempt != attempt {
			t.Fatalf("expected Attempt=%d across model escalation, got %d", attempt, msg.Assign.Attempt)
		}
	}

	d.mu.Lock()
	count := d.attemptCounts["bead-qg-escalate"]
	d.mu.Unlock()
	if count != 2 {
		t.Fatalf("attemptCounts after two QG failures = %d, want 2", count)
	}

	var payload string
	if err := d.db.QueryRowContext(context.Background(),
		`SELECT payload FROM events WHERE type='qg_retry_assign_sent' AND bead_id='bead-qg-escalate' ORDER BY id DESC LIMIT 1`,
	).Scan(&payload); err != nil {
		t.Fatalf("query qg_retry_assign_sent payload: %v", err)
	}
	if !strings.Contains(payload, `"attempt":2`) {
		t.Fatalf("latest qg_retry_assign_sent payload = %s, want attempt 2", payload)
	}

	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-escalate",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "unique-sonnet-opus-fail-3",
		},
	})

	if msg, ok := readMsgFromScanner(t, scanner, 300*time.Millisecond); ok && msg.Type == protocol.MsgAssign {
		t.Fatalf("expected no third retry ASSIGN after max total attempts, got %+v", msg.Assign)
	}
	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_retry_escalated") > 0
	}, 2*time.Second)
}

func TestHandleDone_QGFailRetryPassesQGOutput(t *testing.T) {
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg2", Title: "QG output test", Priority: 1, Type: "task"}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send QG failure with specific output.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg2",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "lint: unused variable x on line 42",
		},
	})

	// Read re-ASSIGN — Feedback should contain the QG output.
	msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN with feedback")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.Feedback != "lint: unused variable x on line 42" {
		t.Fatalf("expected QGOutput in Feedback, got %q", msg.Assign.Feedback)
	}
}

func TestHandleDone_QGFailRetryAttemptCountResetsOnSuccess(t *testing.T) {
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg3", Title: "QG reset test", Priority: 1, Type: "task"}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send one QG failure to increment attempt count.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg3",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "test failed",
		},
	})

	// Drain re-ASSIGN.
	readMsgFromScanner(t, scanner, 2*time.Second)

	// Now send successful DONE — should clear the attempt count.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg3",
			WorkerID:          "w1",
			QualityGatePassed: true,
		},
	})

	// Wait for merge to complete (async) — attempt count should be cleared.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.attemptCounts["bead-qg3"] == 0
	}, 2*time.Second)

	// Verify attempt count was cleared.
	d.mu.Lock()
	count := d.attemptCounts["bead-qg3"]
	d.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected attempt count to be reset to 0, got %d", count)
	}
}

// --- QG Re-assign Memory Inclusion Tests (oro-8l6) ---

func TestHandleDone_QGFailReassignIncludesMemories(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Insert a memory whose content matches the bead title for FTS5 search.
	ctx := context.Background()
	_, err := d.memories.Insert(ctx, memory.InsertParams{
		Content:    "QG memory bead always requires format check before submit",
		Type:       "lesson",
		Source:     "self_report",
		BeadID:     "bead-qgmem",
		WorkerID:   "w-prev",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	conn, scanner := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Register bead detail so Show() returns a title for FTS5 matching.
	beadSrc.mu.Lock()
	beadSrc.shown["bead-qgmem"] = &protocol.BeadDetail{
		ID:                 "bead-qgmem",
		Title:              "QG memory bead",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.mu.Unlock()

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qgmem", Title: "QG memory bead", Priority: 1, Type: "task"}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send QG failure — the re-ASSIGN should include MemoryContext.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qgmem",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "test failed: missing format check",
		},
	})

	// Read re-ASSIGN — MemoryContext should be non-empty.
	msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN with memory context")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.MemoryContext == "" {
		t.Fatal("expected non-empty MemoryContext in QG re-assign, got empty string")
	}
	if !strings.Contains(msg.Assign.MemoryContext, "format check") {
		t.Fatalf("MemoryContext should contain memory content, got %q", msg.Assign.MemoryContext)
	}
}

// --- QG Stuck Detection Tests (oro-gjb) ---

func TestHandleDone_QGStuckDetection_IdenticalOutputsEscalate(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-stuck1", Title: "Stuck test", Priority: 1, Type: "task", Model: protocol.ModelOpus}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send 3 identical QG outputs. The stuck detector should fire on the 3rd,
	// escalating instead of re-assigning.
	identicalOutput := "FAIL: TestFoo — expected 42, got 0"
	for i := 1; i <= maxStuckCount; i++ {
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            "bead-stuck1",
				WorkerID:          "w1",
				QualityGatePassed: false,
				QGOutput:          identicalOutput,
			},
		})

		if i < maxStuckCount {
			msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
			if !ok {
				t.Fatalf("expected re-ASSIGN on attempt %d", i)
			}
			if msg.Type != protocol.MsgAssign {
				t.Fatalf("attempt %d: expected ASSIGN, got %s", i, msg.Type)
			}
		}
	}

	// After maxStuckCount identical outputs, should escalate with stuck message.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, "bead-stuck1") && strings.Contains(m, "QG output repeated") {
				return true
			}
		}
		return false
	}, 2*time.Second)
	msgs := esc.Messages()
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "bead-stuck1") && strings.Contains(m, "QG output repeated") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected stuck escalation for bead-stuck1, got messages: %v", msgs)
	}
}

func TestHandleDone_QGStuckDetection_DifferentOutputsReset(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-stuck2", Title: "Stuck reset", Priority: 1, Type: "task", Model: protocol.ModelOpus}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send 2 identical, then 1 different — should NOT trigger stuck detection.
	for i := 1; i <= 3; i++ {
		output := "same error output"
		if i == 3 {
			output = "different error output"
		}
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            "bead-stuck2",
				WorkerID:          "w1",
				QualityGatePassed: false,
				QGOutput:          output,
			},
		})

		if i < maxQGRetries {
			msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
			if !ok {
				t.Fatalf("expected re-ASSIGN on attempt %d", i)
			}
			if msg.Type != protocol.MsgAssign {
				t.Fatalf("attempt %d: expected ASSIGN, got %s", i, msg.Type)
			}
		}
	}

	// Negative test: verify stuck escalation did NOT fire. We wait for the
	// escalation message for bead-stuck2 that we know WILL arrive (the QG retry
	// cap "quality gate failed" message) to confirm processing completed, then
	// assert no stuck-specific escalation exists.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, "bead-stuck2") && strings.Contains(m, "quality gate failed") {
				return true
			}
		}
		return false
	}, 2*time.Second)
	msgs := esc.Messages()
	for _, m := range msgs {
		if strings.Contains(m, "bead-stuck2") && strings.Contains(m, "QG output repeated") {
			t.Fatalf("should NOT have stuck escalation when outputs differ, got: %s", m)
		}
	}
}

// --- QG Retry Exhaustion Test (oro-029) ---

func TestHandleQGFailure_Exhaustion(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-exh1", Title: "Exhaustion test", Priority: 1, Type: "task"}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Seed attemptCounts so the next failure is attempt #3 (= maxQGRetries).
	d.mu.Lock()
	d.attemptCounts["bead-exh1"] = maxQGRetries - 1 // 2
	d.mu.Unlock()

	// Send a single QG failure with a unique output to avoid stuck detection.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-exh1",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "unique-exhaustion-output-abc123",
		},
	})

	// Wait for dispatcher to process and escalate.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, "bead-exh1") && strings.Contains(m, "quality gate failed 3 times") {
				return true
			}
		}
		return false
	}, 2*time.Second)

	// Assert: escalation with "quality gate failed 3 times".
	msgs := esc.Messages()
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "bead-exh1") && strings.Contains(m, "quality gate failed 3 times") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected escalation containing 'quality gate failed 3 times' for bead-exh1, got messages: %v", msgs)
	}

	// Assert: attemptCounts cleared (clearBeadTracking was called).
	d.mu.Lock()
	count := d.attemptCounts["bead-exh1"]
	_, stuckExists := d.qgStuckTracker["bead-exh1"]
	d.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected attemptCounts to be cleared (0), got %d", count)
	}
	if stuckExists {
		t.Fatal("expected qgStuckTracker entry to be cleared after exhaustion")
	}

	// Assert: no ASSIGN sent (worker should NOT get a re-assign).
	msg, ok := readMsgFromScanner(t, scanner, 300*time.Millisecond)
	if ok && msg.Type == protocol.MsgAssign {
		t.Fatal("expected no ASSIGN after retry exhaustion, but got one")
	}

	// Assert: qg_retry_escalated event logged.
	evCount := eventCount(t, d.db, "qg_retry_escalated")
	if evCount == 0 {
		t.Fatal("expected qg_retry_escalated event to be logged, but found 0")
	}
}

func TestHandleDone_QGStuckDetection_IndependentOfAttemptCount(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-stuck3", Title: "Independent test", Priority: 1, Type: "task", Model: protocol.ModelOpus}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send 1 different output, then 2 identical. Stuck detection should NOT fire
	// because we only have 2 consecutive identical (need 3).
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-stuck3",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "unique error",
		},
	})
	readMsgFromScanner(t, scanner, 2*time.Second)

	for i := 0; i < 2; i++ {
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            "bead-stuck3",
				WorkerID:          "w1",
				QualityGatePassed: false,
				QGOutput:          "same lint failure",
			},
		})
		if i < 1 {
			msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
			if !ok {
				t.Fatal("expected re-ASSIGN on attempt 2")
			}
			if msg.Type != protocol.MsgAssign {
				t.Fatalf("expected ASSIGN, got %s", msg.Type)
			}
		}
	}

	// Negative test: verify stuck escalation did NOT fire. Wait for the QG retry
	// cap escalation ("quality gate failed") to confirm processing completed,
	// then assert no stuck-specific escalation exists.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, "bead-stuck3") && strings.Contains(m, "quality gate failed") {
				return true
			}
		}
		return false
	}, 2*time.Second)
	msgs := esc.Messages()

	for _, m := range msgs {
		if strings.Contains(m, "bead-stuck3") && strings.Contains(m, "QG output repeated") {
			t.Fatalf("should NOT have stuck escalation with only 2 consecutive identical outputs, got: %s", m)
		}
	}
}

// --- QG Exhaustion Creates P0 Bead (oro-2ir.2) ---

func TestQGExhaustion_CreatesP0Bead(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-p0", Title: "P0 creation test", Priority: 2, Type: "task"}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Seed attemptCounts to maxQGRetries-1 so next failure triggers exhaustion.
	d.mu.Lock()
	d.attemptCounts["bead-p0"] = maxQGRetries - 1
	d.mu.Unlock()

	// Send QG failure with unique output.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-p0",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "unclassified qg exhausted output xyz",
		},
	})

	// Wait for dispatcher to process and escalate.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, "bead-p0") && strings.Contains(m, "quality gate failed") {
				return true
			}
		}
		return false
	}, 2*time.Second)

	// Assert: escalation still happens.
	msgs := esc.Messages()
	foundEsc := false
	for _, m := range msgs {
		if strings.Contains(m, "bead-p0") && strings.Contains(m, "quality gate failed") {
			foundEsc = true
			break
		}
	}
	if !foundEsc {
		t.Fatalf("expected escalation for bead-p0, got: %v", msgs)
	}

	// Assert: BeadSource.Create was called with P0 priority and QG output in description.
	beadSrc.mu.Lock()
	created := beadSrc.created
	beadSrc.mu.Unlock()

	if len(created) == 0 {
		t.Fatal("expected BeadSource.Create to be called on QG exhaustion, but it was not")
	}
	c := created[0]
	if c.priority != 0 {
		t.Fatalf("expected P0 priority, got %d", c.priority)
	}
	if c.beadType != "bug" {
		t.Fatalf("expected type 'bug', got %q", c.beadType)
	}
	if !strings.Contains(c.description, "unclassified qg exhausted output xyz") {
		t.Fatalf("expected QG output in description, got %q", c.description)
	}
	if c.parent != "bead-p0" {
		t.Fatalf("expected parent 'bead-p0', got %q", c.parent)
	}

	// Assert: no ASSIGN sent (worker should NOT get re-assigned).
	msg, ok := readMsgFromScanner(t, scanner, 300*time.Millisecond)
	if ok && msg.Type == protocol.MsgAssign {
		t.Fatal("expected no ASSIGN after QG exhaustion, but got one")
	}
}

func TestQGExhaustion_ReopensOriginalForDeterministicFailure(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const (
		beadID   = "bead-deterministic-qg"
		workerID = "w-deterministic-qg"
		worktree = "/tmp/wt-deterministic-qg"
	)
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "deterministic qg bead",
		Status:             "in_progress",
		AcceptanceCriteria: "Test: go test ./... | Assert: pass",
	}
	assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.exhaustedBeads[beadID] = true
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerBusy,
		beadID:       beadID,
		assignmentID: assignmentID,
		worktree:     worktree,
	}
	d.mu.Unlock()

	rec := QGFailureRecord{
		ID:           "deterministic-exhaustion-occ",
		BeadID:       beadID,
		WorkerID:     workerID,
		AssignmentID: assignmentID,
		Component:    "worker",
		Fingerprint:  "qg:deterministic",
		Summary:      "golangci-lint unused variable",
		Output:       "golangci-lint failed: unused variable widget",
		OutputHash:   "hash-deterministic",
	}
	cls := QGFailureClassification{
		Class:      QGFailureClassWorkerDeterministic,
		Decision:   QGFailureDecisionReopenOriginal,
		Confidence: QGFailureConfidenceHigh,
		Reason:     "deterministic worker failure exhausted retry budget",
	}

	d.handleClassifiedQGExhaustion(ctx, workerID, beadID, assignmentID, rec, cls)

	var status, storedWorktree string
	if err := d.db.QueryRowContext(ctx, `SELECT status, worktree FROM assignments WHERE id=?`, assignmentID).Scan(&status, &storedWorktree); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "completed" || storedWorktree != worktree {
		t.Fatalf("assignment status/worktree = %s/%s, want completed/%s", status, storedWorktree, worktree)
	}

	d.mu.Lock()
	preservedWorktree := d.worktreeByBead[beadID]
	_, exhausted := d.exhaustedBeads[beadID]
	d.mu.Unlock()
	if preservedWorktree != worktree {
		t.Fatalf("worktreeByBead[%s] = %q, want %q", beadID, preservedWorktree, worktree)
	}
	if exhausted {
		t.Fatal("deterministic reopen left bead marked exhausted")
	}
	if beadSrc.updated[beadID] != "open" {
		t.Fatalf("bead status update = %q, want open", beadSrc.updated[beadID])
	}
	if len(beadSrc.created) != 0 {
		t.Fatalf("created P0 children = %+v, want none", beadSrc.created)
	}
	if notes := beadSrc.shown[beadID].Notes; !strings.Contains(notes, "qg_incident:") || !strings.Contains(notes, "output_hash: hash-deterministic") {
		t.Fatalf("original bead notes missing qg incident/hash evidence:\n%s", notes)
	}

	var occurrences int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qg_failure_occurrences WHERE bead_id=?`, beadID).Scan(&occurrences); err != nil {
		t.Fatalf("count qg occurrences: %v", err)
	}
	if occurrences != 1 {
		t.Fatalf("qg occurrences = %d, want 1", occurrences)
	}

	closedID := "bead-deterministic-closed"
	beadSrc.shown[closedID] = &protocol.BeadDetail{
		ID:     closedID,
		Title:  "already closed deterministic qg bead",
		Status: "closed",
	}
	closedRec := rec
	closedRec.ID = "deterministic-closed-occ"
	closedRec.BeadID = closedID
	closedRec.AssignmentID = 0
	d.handleClassifiedQGExhaustion(ctx, workerID, closedID, 0, closedRec, cls)
	if beadSrc.updated[closedID] == "open" {
		t.Fatal("already closed original bead was reopened")
	}
}
