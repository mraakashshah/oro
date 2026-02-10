package dispatcher //nolint:testpackage // needs internal access to attemptCounts

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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

	beadSrc.SetBeads([]Bead{{ID: "bead-qg1", Title: "QG test", Priority: 1, Type: "task"}})

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

	beadSrc.SetBeads([]Bead{{ID: "bead-qg2", Title: "QG output test", Priority: 1, Type: "task"}})

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

	beadSrc.SetBeads([]Bead{{ID: "bead-qg3", Title: "QG reset test", Priority: 1, Type: "task"}})

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
