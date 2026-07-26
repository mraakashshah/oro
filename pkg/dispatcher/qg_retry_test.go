package dispatcher //nolint:testpackage // needs internal access to attemptCounts

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/dbutil"
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

	// After maxQGRetries, low-confidence exhaustion stops for triage without
	// manager escalation.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_failure_triage_required") > 0
	}, 2*time.Second)
	for _, m := range esc.Messages() {
		if strings.Contains(m, "bead-qg1") && strings.Contains(m, "quality gate failed") {
			t.Fatalf("unexpected manager escalation for bead-qg1: %s", m)
		}
	}
}

func TestQGRetryReconnectDuringReservationStillDeliversRetry(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	beadID := "bead-qg-reconnect"
	workerID := "worker-qg-reconnect"
	worktree := t.TempDir()

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Title: "QG reconnect retry", Status: "open"}
	beadSrc.mu.Unlock()

	result, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("seed active assignment: %v", err)
	}
	assignmentID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read assignment ID: %v", err)
	}

	oldServer, oldClient := net.Pipe()
	defer func() { _ = oldServer.Close() }()
	defer func() { _ = oldClient.Close() }()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         oldServer,
		encoder:      json.NewEncoder(oldServer),
		state:        protocol.WorkerReserved,
		beadID:       beadID,
		worktree:     worktree,
		assignmentID: assignmentID,
		targetBranch: "main",
	}
	d.mu.Unlock()

	baselineStarted := make(chan struct{})
	releaseBaseline := make(chan struct{})
	d.cfg.RegressionRevert = true
	d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "-C "+worktree+" rev-parse HEAD" {
			return []byte("qg-reconnect-head\n"), nil
		}
		close(baselineStarted)
		<-releaseBaseline
		return []byte("qg-reconnect-head\n"), nil
	}}

	retryDone := make(chan struct{})
	go func() {
		defer close(retryDone)
		d.qgRetryWithReservation(ctx, workerID, beadID, "quality gate failed", 1)
	}()

	select {
	case <-baselineStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked baseline capture")
	}

	newServer, newClient := net.Pipe()
	defer func() { _ = newServer.Close() }()
	defer func() { _ = newClient.Close() }()
	d.mu.Lock()
	d.upsertWorker(workerID, newServer, false)
	d.mu.Unlock()
	d.handleReconnect(ctx, workerID, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: workerID,
			BeadID:   beadID,
			State:    "running",
		},
	})

	close(releaseBaseline)
	msg, ok := readMsg(t, newClient, 2*time.Second)
	if !ok {
		t.Fatal("expected retry ASSIGN on reconnected worker")
	}
	if msg.Type != protocol.MsgAssign || msg.Assign == nil {
		t.Fatalf("retry message = %#v, want ASSIGN", msg)
	}
	if msg.Assign.BeadID != beadID || msg.Assign.Attempt != 1 {
		t.Fatalf("retry assignment = %#v, want bead %q attempt 1", msg.Assign, beadID)
	}

	select {
	case <-retryDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retry completion")
	}

	d.mu.Lock()
	worker := d.workers[workerID]
	workerState := worker.state
	workerAssignmentID := worker.assignmentID
	d.mu.Unlock()
	if workerState != protocol.WorkerBusy || workerAssignmentID != assignmentID {
		t.Fatalf("worker state=%s assignment=%d, want busy assignment=%d", workerState, workerAssignmentID, assignmentID)
	}

	var activeAssignments int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`, beadID).Scan(&activeAssignments); err != nil {
		t.Fatalf("count active assignments: %v", err)
	}
	if activeAssignments != 1 {
		t.Fatalf("active assignments = %d, want 1", activeAssignments)
	}
	if got := eventCount(t, d.db, "qg_retry_assign_sent"); got != 1 {
		t.Fatalf("qg_retry_assign_sent events = %d, want 1", got)
	}
}

func TestReconnectDifferentBeadDoesNotStealQGRetryReservation(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	reservedBeadID := "bead-qg-reserved"
	beadSrc.mu.Lock()
	beadSrc.shown["bead-qg-other"] = &protocol.BeadDetail{ID: "bead-qg-other", Status: "open"}
	beadSrc.mu.Unlock()

	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	d.mu.Lock()
	d.workers["worker-qg-reserved"] = &trackedWorker{
		id:      "worker-qg-reserved",
		conn:    server,
		encoder: json.NewEncoder(server),
		state:   protocol.WorkerReserved,
		beadID:  reservedBeadID,
	}
	d.mu.Unlock()

	d.handleReconnect(context.Background(), "worker-qg-reserved", protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: "worker-qg-reserved",
			BeadID:   "bead-qg-other",
			State:    "running",
		},
	})

	d.mu.Lock()
	worker := d.workers["worker-qg-reserved"]
	state, beadID := worker.state, worker.beadID
	d.mu.Unlock()
	if state != protocol.WorkerReserved || beadID != reservedBeadID {
		t.Fatalf("worker state=%s bead=%q, want reserved bead=%q", state, beadID, reservedBeadID)
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
		if msg.Assign.Model != "gpt-5.6-sol" || msg.Assign.Reasoning != "low" {
			t.Fatalf("expected retry Sol low on attempt %d, got model=%q reasoning=%q", attempt, msg.Assign.Model, msg.Assign.Reasoning)
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
		return eventCount(t, d.db, "qg_failure_triage_required") > 0
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

// --- QG Re-assign Cards Context Tests (oro-8l6) ---

func TestQGRetryReassignIncludesCardsAndEmptyMemoryContext(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Seed a card whose content matches the bead title for FTS5 search.
	ctx := context.Background()
	seedDispatcherCard(ctx, t, d, cards.CardCreateParams{
		ID:          "card-qgmem",
		Type:        cards.CardTypePattern,
		Title:       "QG retry card",
		BodySummary: "QG memory bead always requires format check before submit",
		BodyFull:    "QG retry assignments should include the format check card.",
		Tags:        []string{"qg"},
	})

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
		Labels:             []string{"qg"},
	}
	beadSrc.mu.Unlock()

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qgmem", Title: "QG memory bead", Priority: 1, Type: "task", Labels: []string{"qg"}}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send QG failure — the re-ASSIGN should include Cards.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qgmem",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "test failed: missing format check",
		},
	})

	// Read re-ASSIGN — Cards should be non-empty and legacy MemoryContext empty.
	msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN with card context")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.MemoryContext != "" {
		t.Fatalf("MemoryContext = %q, want empty", msg.Assign.MemoryContext)
	}
	if len(msg.Assign.Cards.Deck) == 0 || msg.Assign.Cards.Deck[0].ID != "card-qgmem" {
		t.Fatalf("Cards.Deck = %#v, want first card-qgmem", msg.Assign.Cards.Deck)
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
	// classifying and releasing instead of re-assigning.
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

	// After maxStuckCount identical outputs, the stuck path classifies the failure
	// and reopens the bead — no generic "QG output repeated" escalation.
	waitFor(t, func() bool {
		beadSrc.mu.Lock()
		defer beadSrc.mu.Unlock()
		return beadSrc.updated["bead-stuck1"] == "open"
	}, 2*time.Second)
	beadSrc.mu.Lock()
	updatedStatus := beadSrc.updated["bead-stuck1"]
	beadSrc.mu.Unlock()
	if updatedStatus != "open" {
		t.Fatalf("expected bead-stuck1 reopened after stuck classification, got status %q", updatedStatus)
	}
	for _, m := range esc.Messages() {
		if strings.Contains(m, "bead-stuck1") && strings.Contains(m, "QG output repeated") {
			t.Fatalf("unexpected old-style stuck escalation: %s", m)
		}
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

	// Negative test: verify stuck escalation did NOT fire. Wait for the triage
	// event to confirm retry-cap processing completed, then assert no
	// stuck-specific escalation exists.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_failure_triage_required") > 0
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

	// Wait for dispatcher to process and stop for triage.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_failure_triage_required") > 0
	}, 2*time.Second)

	// Assert: no manager escalation with "quality gate failed 3 times".
	for _, m := range esc.Messages() {
		if strings.Contains(m, "bead-exh1") && strings.Contains(m, "quality gate failed 3 times") {
			t.Fatalf("unexpected QG retry manager escalation for bead-exh1: %s", m)
		}
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

	// Assert: triage event logged.
	evCount := eventCount(t, d.db, "qg_failure_triage_required")
	if evCount == 0 {
		t.Fatal("expected qg_failure_triage_required event to be logged, but found 0")
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

	// Negative test: verify stuck escalation did NOT fire. Wait for the triage
	// event to confirm retry-cap processing completed, then assert no
	// stuck-specific escalation exists.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_failure_triage_required") > 0
	}, 2*time.Second)
	msgs := esc.Messages()

	for _, m := range msgs {
		if strings.Contains(m, "bead-stuck3") && strings.Contains(m, "QG output repeated") {
			t.Fatalf("should NOT have stuck escalation with only 2 consecutive identical outputs, got: %s", m)
		}
	}
}

// --- QG Exhaustion Triage Policy ---

func TestQGExhaustion_UnclassifiedDoesNotCreateP0Bead(t *testing.T) {
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

	// Wait for dispatcher to process and record the triage event.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_failure_triage_required") > 0
	}, 2*time.Second)

	// Assert: no legacy P0 bug bead is created for low-confidence triage.
	beadSrc.mu.Lock()
	created := append([]createCall(nil), beadSrc.created...)
	updated := beadSrc.updated["bead-p0"]
	deferCalls := append([]deferCall(nil), beadSrc.deferCalls...)
	beadSrc.mu.Unlock()

	for _, c := range created {
		if strings.Contains(c.title, "P0: QG exhausted") {
			t.Fatalf("unexpected legacy QG P0 bead: %+v", c)
		}
	}
	if updated != "open" {
		t.Fatalf("original bead status = %q, want open", updated)
	}
	if len(deferCalls) == 0 || deferCalls[0].id != "bead-p0" {
		t.Fatalf("expected original bead to be deferred for triage, got %+v", deferCalls)
	}
	for _, m := range esc.Messages() {
		if strings.Contains(m, "bead-p0") && strings.Contains(m, "quality gate failed") {
			t.Fatalf("unexpected manager escalation for triage-only QG exhaustion: %s", m)
		}
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
	if len(beadSrc.deferCalls) != 1 {
		t.Fatalf("defer calls = %+v, want one cooldown defer", beadSrc.deferCalls)
	}
	if beadSrc.deferCalls[0].id != beadID {
		t.Fatalf("defer call id = %q, want %q", beadSrc.deferCalls[0].id, beadID)
	}
	if until, err := time.Parse(time.RFC3339, beadSrc.deferCalls[0].until); err != nil || !until.After(time.Now().UTC()) {
		t.Fatalf("defer until = %q, want future RFC3339 timestamp (parse err %v)", beadSrc.deferCalls[0].until, err)
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
	if len(beadSrc.deferCalls) != 1 {
		t.Fatalf("closed original changed defer calls = %+v, want unchanged one call", beadSrc.deferCalls)
	}
}

// --- Transient QG Backoff Test (oro-34e5) ---

func TestHandleQGFailureRecordsOneTransientRetryPerFailure(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	d.transientBackoffFn = func(_ int) time.Duration { return 0 }

	const (
		workerID = "w-transient-once"
		beadID   = "bead-transient-once"
	)
	beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Transient QG once", Priority: 1, Type: "task", Model: protocol.ModelOpus}})
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Title: "Transient QG once", Status: "in_progress"}

	workerConn := newMockConn()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         workerConn,
		state:        protocol.WorkerBusy,
		assignmentID: 1,
		beadID:       beadID,
		worktree:     t.TempDir(),
		model:        protocol.ModelOpus,
		lastSeen:     d.nowFunc(),
		encoder:      json.NewEncoder(workerConn),
	}
	d.mu.Unlock()

	d.handleQGFailure(t.Context(), workerID, beadID, "network timeout: dial tcp 127.0.0.1:3000: connect: connection refused")

	msg, ok := firstWrittenMsg(workerConn)
	if !ok {
		t.Fatal("expected ASSIGN after transient retry")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN after transient retry, got %s", msg.Type)
	}

	d.mu.Lock()
	transientCount := d.transientCounts[beadID]
	d.mu.Unlock()
	if transientCount != 1 {
		t.Fatalf("one transient QG failure must record one transient retry, got %d", transientCount)
	}
}

func TestTransientQGFailureBacksOffWithoutBurningWorkerAttempt(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	// Zero-duration backoff so the test completes without sleeping.
	d.transientBackoffFn = func(_ int) time.Duration { return 0 }
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-transient1", Title: "Transient QG test", Priority: 1, Type: "task", Model: protocol.ModelOpus}})

	// Drain initial ASSIGN.
	readMsg(t, conn, 2*time.Second)

	// Send a QG failure with transient output — must match isTransientQGFailure
	// and not match any higher-priority classifier.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-transient1",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "network timeout: dial tcp 127.0.0.1:3000: connect: connection refused",
		},
	})

	// Assert: qg_transient_retry event logged.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_transient_retry") >= 1
	}, 2*time.Second)

	// Assert: attemptCounts NOT incremented — transient retry must not burn worker-fix attempts.
	d.mu.Lock()
	count := d.attemptCounts["bead-transient1"]
	d.mu.Unlock()
	if count != 0 {
		t.Fatalf("transient QG failure must not increment attemptCounts, got %d", count)
	}

	// Assert: worker receives a re-ASSIGN after the (zero-duration) backoff.
	msg, ok := readMsgFromScanner(t, scanner, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after transient QG backoff")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN after transient retry, got %s", msg.Type)
	}

	d.mu.Lock()
	transientCount := d.transientCounts["bead-transient1"]
	d.mu.Unlock()
	if transientCount != 1 {
		t.Fatalf("one transient QG failure must record one transient retry, got %d", transientCount)
	}
}

func TestQGExhaustion_ReusesInfraIncidentForSystemicFailure(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("SchemaDDL: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("MigrateBeadSchema: %v", err)
	}
	store := beadstore.NewSQLiteStore(db)
	for _, id := range []string{"oro-systemic-a", "oro-systemic-b"} {
		if _, err := store.Create(ctx, beadstore.CreateParams{ID: id, Title: id, Type: "task", Priority: 1}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	d := &Dispatcher{db: db, beads: store}
	cls := QGFailureClassification{
		Class:      QGFailureClassSystemic,
		Decision:   QGFailureDecisionCreateOrReuseInfra,
		Confidence: QGFailureConfidenceHigh,
		Reason:     "same systemic fingerprint across unrelated beads",
	}
	recA := QGFailureRecord{
		ID:          "systemic-occ-a",
		BeadID:      "oro-systemic-a",
		WorkerID:    "worker-a",
		Fingerprint: "qg:systemic-shared",
		Summary:     "quality_gate.sh package loader failure",
		Output:      "quality_gate.sh failed: package loader cannot load stdlib",
		OutputHash:  "hash-systemic-a",
	}
	recB := recA
	recB.ID = "systemic-occ-b"
	recB.BeadID = "oro-systemic-b"
	recB.WorkerID = "worker-b"
	recB.OutputHash = "hash-systemic-b"

	first, err := d.createOrReuseQGInfraIncident(ctx, recA, cls)
	if err != nil {
		t.Fatalf("first createOrReuseQGInfraIncident: %v", err)
	}
	second, err := d.createOrReuseQGInfraIncident(ctx, recB, cls)
	if err != nil {
		t.Fatalf("second createOrReuseQGInfraIncident: %v", err)
	}
	if first.ID == 0 || second.ID != first.ID || second.OccurrenceCount != 2 {
		t.Fatalf("incidents first=%+v second=%+v, want same nonzero id and occurrence_count=2", first, second)
	}

	infra, err := store.Show(ctx, qgIncidentBeadID(first.ID))
	if err != nil {
		t.Fatalf("show infra incident: %v", err)
	}
	if infra == nil {
		t.Fatal("infra incident bead was not created")
	}
	for _, beadID := range []string{"oro-systemic-a", "oro-systemic-b"} {
		if got := strings.Count(infra.Notes, "affected_bead: "+beadID); got != 1 {
			t.Fatalf("affected evidence for %s count = %d, want 1:\n%s", beadID, got, infra.Notes)
		}
	}
}

// TestRepeatedIdenticalQGOutputClassifiedBeforeEscalation verifies that when
// isQGStuck detects maxStuckCount consecutive identical QG outputs,
// handleRepeatedQGOutput classifies the failure and routes to the correct
// cleanup path. All four routing edges must leave:
//   - no active assignment
//   - no stale worker state (worker idle or absent from d.workers)
//   - no stale qgStuckTracker entry
//   - no stranded original bead
func TestRepeatedIdenticalQGOutputClassifiedBeforeEscalation(t *testing.T) {
	const worktree = "/tmp/wt-repeated-qg-test"

	// deterministic class: repeated deterministic failure → reopen original bead,
	// no escalation to manager.
	t.Run("deterministic/reopens_original_bead_no_escalation", func(t *testing.T) {
		d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		const (
			beadID   = "bead-rep-det"
			workerID = "w-rep-det"
		)
		beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
		d.mu.Lock()
		d.qgStuckTracker[beadID] = &qgHistory{hashes: []string{"h1", "h2", "h3"}}
		d.attemptCounts[beadID] = 2
		d.workers[workerID] = &trackedWorker{
			id: workerID, state: protocol.WorkerBusy,
			beadID: beadID, assignmentID: assignmentID,
		}
		d.mu.Unlock()

		rec := QGFailureRecord{
			ID:     fmt.Sprintf("%s:%s:%d:2", beadID, workerID, assignmentID),
			BeadID: beadID, WorkerID: workerID, AssignmentID: assignmentID,
			Component: "worker", Fingerprint: "qg:repeated-det",
			Summary:    "golangci-lint unused variable x",
			Output:     "golangci-lint failed: x declared but not used",
			OutputHash: "hash-rep-det",
		}
		cls := QGFailureClassification{
			Class: QGFailureClassWorkerDeterministic, Decision: QGFailureDecisionReopenOriginal,
			Confidence: QGFailureConfidenceHigh, Reason: "repeated deterministic",
		}

		d.handleRepeatedQGOutput(ctx, workerID, beadID, rec, cls)

		// no active assignment
		var asgStatus string
		if err := d.db.QueryRowContext(ctx,
			`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&asgStatus); err != nil {
			t.Fatalf("query assignment: %v", err)
		}
		if asgStatus != "completed" {
			t.Fatalf("assignment status = %q, want completed", asgStatus)
		}

		// no stale worker state, no stale qgStuckTracker
		d.mu.Lock()
		w := d.workers[workerID]
		_, stuckExists := d.qgStuckTracker[beadID]
		d.mu.Unlock()
		if w != nil && w.beadID != "" {
			t.Fatalf("worker still holds beadID %q after handleRepeatedQGOutput", w.beadID)
		}
		if stuckExists {
			t.Fatal("qgStuckTracker entry not cleared by handleRepeatedQGOutput")
		}

		// original bead reopened (not stranded)
		if beadSrc.updated[beadID] != "open" {
			t.Fatalf("bead status update = %q, want open", beadSrc.updated[beadID])
		}
		if len(beadSrc.deferCalls) != 1 || beadSrc.deferCalls[0].id != beadID {
			t.Fatalf("defer calls = %+v, want cooldown defer for %s", beadSrc.deferCalls, beadID)
		}

		// no escalation to manager
		for _, m := range esc.Messages() {
			if strings.Contains(m, beadID) {
				t.Fatalf("unexpected escalation for deterministic stuck: %s", m)
			}
		}
	})

	// systemic class: repeated infra failure → create/reuse incident bead,
	// no generic escalation to manager.
	t.Run("systemic/creates_infra_incident_no_escalation", func(t *testing.T) {
		d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		const (
			beadID   = "bead-rep-sys"
			workerID = "w-rep-sys"
		)
		beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		// Fresh DB → first QG incident gets id=1 → infra bead id = "oro-qg-incident-1".
		// Mark it nil so ensureQGIncidentBead treats it as not-yet-created.
		beadSrc.shown["oro-qg-incident-1"] = nil
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
		d.mu.Lock()
		d.qgStuckTracker[beadID] = &qgHistory{hashes: []string{"h1", "h2", "h3"}}
		d.workers[workerID] = &trackedWorker{
			id: workerID, state: protocol.WorkerBusy,
			beadID: beadID, assignmentID: assignmentID,
		}
		d.mu.Unlock()

		rec := QGFailureRecord{
			ID:     fmt.Sprintf("%s:%s:%d:2", beadID, workerID, assignmentID),
			BeadID: beadID, WorkerID: workerID, AssignmentID: assignmentID,
			Component: "worker", Fingerprint: "qg:repeated-sys",
			Summary:    "quality_gate.sh package loader failure",
			Output:     "quality_gate.sh failed: package loader cannot load stdlib",
			OutputHash: "hash-rep-sys",
		}
		cls := QGFailureClassification{
			Class: QGFailureClassSystemic, Decision: QGFailureDecisionCreateOrReuseInfra,
			Confidence: QGFailureConfidenceHigh, Reason: "systemic infra repeated",
		}

		d.handleRepeatedQGOutput(ctx, workerID, beadID, rec, cls)

		// no active assignment
		var asgStatus string
		if err := d.db.QueryRowContext(ctx,
			`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&asgStatus); err != nil {
			t.Fatalf("query assignment: %v", err)
		}
		if asgStatus != "completed" {
			t.Fatalf("assignment status = %q, want completed", asgStatus)
		}

		// no stale worker state, no stale qgStuckTracker
		d.mu.Lock()
		w := d.workers[workerID]
		_, stuckExists := d.qgStuckTracker[beadID]
		d.mu.Unlock()
		if w != nil && w.beadID != "" {
			t.Fatalf("worker still holds beadID %q", w.beadID)
		}
		if stuckExists {
			t.Fatal("qgStuckTracker entry not cleared")
		}

		// infra incident bead created (ensureQGIncidentBead called beads.Create)
		beadSrc.mu.Lock()
		numCreated := len(beadSrc.created)
		beadSrc.mu.Unlock()
		if numCreated == 0 {
			t.Fatal("expected infra incident bead to be created for systemic stuck, got none")
		}

		// no escalation to manager
		for _, m := range esc.Messages() {
			if strings.Contains(m, beadID) {
				t.Fatalf("unexpected escalation for systemic stuck: %s", m)
			}
		}
	})

	// unknown class: low-confidence classification → log qg_repeated_triage event,
	// no escalation to manager.
	t.Run("unknown/logs_triage_event_no_escalation", func(t *testing.T) {
		d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		const (
			beadID   = "bead-rep-unk"
			workerID = "w-rep-unk"
		)
		beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
		d.mu.Lock()
		d.qgStuckTracker[beadID] = &qgHistory{hashes: []string{"h1", "h2", "h3"}}
		d.workers[workerID] = &trackedWorker{
			id: workerID, state: protocol.WorkerBusy,
			beadID: beadID, assignmentID: assignmentID,
		}
		d.mu.Unlock()

		rec := QGFailureRecord{
			ID:     fmt.Sprintf("%s:%s:%d:2", beadID, workerID, assignmentID),
			BeadID: beadID, WorkerID: workerID, AssignmentID: assignmentID,
			Component: "worker", Fingerprint: "qg:repeated-unk",
			Summary:    "unrecognized failure pattern",
			Output:     "some unknown error that does not match any known pattern",
			OutputHash: "hash-rep-unk",
		}
		cls := QGFailureClassification{
			Class: QGFailureClassUnknown, Decision: QGFailureDecisionStopForTriage,
			Confidence: QGFailureConfidenceLow, Reason: "could not classify",
		}

		d.handleRepeatedQGOutput(ctx, workerID, beadID, rec, cls)

		// no active assignment
		var asgStatus string
		if err := d.db.QueryRowContext(ctx,
			`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&asgStatus); err != nil {
			t.Fatalf("query assignment: %v", err)
		}
		if asgStatus != "completed" {
			t.Fatalf("assignment status = %q, want completed", asgStatus)
		}

		// no stale worker state, no stale qgStuckTracker
		d.mu.Lock()
		w := d.workers[workerID]
		_, stuckExists := d.qgStuckTracker[beadID]
		d.mu.Unlock()
		if w != nil && w.beadID != "" {
			t.Fatalf("worker still holds beadID %q", w.beadID)
		}
		if stuckExists {
			t.Fatal("qgStuckTracker entry not cleared")
		}

		// triage event logged — classification reached before any escalation
		if eventCount(t, d.db, "qg_repeated_triage") == 0 {
			t.Fatal("expected qg_repeated_triage event to be logged, got 0")
		}

		// no escalation to manager
		for _, m := range esc.Messages() {
			if strings.Contains(m, beadID) {
				t.Fatalf("unexpected escalation for unknown stuck: %s", m)
			}
		}
	})

	// worker disconnected: worker absent from d.workers — function must still
	// clean up all tracking state without attempting any worker send.
	t.Run("disconnected_worker/cleans_up_without_send", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		const (
			beadID   = "bead-rep-disc"
			workerID = "w-rep-disc" // not added to d.workers
		)
		beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, worktree)
		d.mu.Lock()
		d.qgStuckTracker[beadID] = &qgHistory{hashes: []string{"h1", "h2", "h3"}}
		d.attemptCounts[beadID] = 2
		// worker intentionally absent — simulates disconnect before cleanup runs
		d.mu.Unlock()

		rec := QGFailureRecord{
			// AssignmentID = 0: what handleQGFailure computes when worker is gone
			BeadID: beadID, WorkerID: workerID, AssignmentID: 0,
			Component: "worker", Fingerprint: "qg:repeated-disc",
			Summary:    "golangci-lint lint error",
			Output:     "golangci-lint failed: some lint error",
			OutputHash: "hash-rep-disc",
		}
		cls := QGFailureClassification{
			Class: QGFailureClassWorkerDeterministic, Decision: QGFailureDecisionReopenOriginal,
			Confidence: QGFailureConfidenceHigh, Reason: "deterministic",
		}

		// Must not panic even though worker is disconnected.
		d.handleRepeatedQGOutput(ctx, workerID, beadID, rec, cls)

		// qgStuckTracker cleared despite disconnected worker
		d.mu.Lock()
		_, stuckExists := d.qgStuckTracker[beadID]
		d.mu.Unlock()
		if stuckExists {
			t.Fatal("qgStuckTracker entry not cleared for disconnected worker")
		}

		// assignment completed via bead_id fallback (assignmentID=0 path)
		var asgStatus string
		if err := d.db.QueryRowContext(ctx,
			`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&asgStatus); err != nil {
			t.Fatalf("query assignment: %v", err)
		}
		if asgStatus != "completed" {
			t.Fatalf("assignment status = %q, want completed (bead_id fallback)", asgStatus)
		}
	})
}
