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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg1", Title: "QG test", Priority: 1, Type: "task"}})

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
				QGOutput:          fmt.Sprintf("fail-%d", i),
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
		}
	}

	// After maxQGRetries, should escalate to manager.
	time.Sleep(200 * time.Millisecond)
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

	// Wait for merge to complete (async).
	time.Sleep(200 * time.Millisecond)

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
		ID:    "bead-qgmem",
		Title: "QG memory bead",
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-stuck1", Title: "Stuck test", Priority: 1, Type: "task"}})

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
	time.Sleep(200 * time.Millisecond)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-stuck2", Title: "Stuck reset", Priority: 1, Type: "task"}})

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

	time.Sleep(200 * time.Millisecond)
	msgs := esc.Messages()
	for _, m := range msgs {
		if strings.Contains(m, "bead-stuck2") && strings.Contains(m, "QG output repeated") {
			t.Fatalf("should NOT have stuck escalation when outputs differ, got: %s", m)
		}
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-stuck3", Title: "Independent test", Priority: 1, Type: "task"}})

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

	time.Sleep(200 * time.Millisecond)
	msgs := esc.Messages()

	for _, m := range msgs {
		if strings.Contains(m, "bead-stuck3") && strings.Contains(m, "QG output repeated") {
			t.Fatalf("should NOT have stuck escalation with only 2 consecutive identical outputs, got: %s", m)
		}
	}
}
