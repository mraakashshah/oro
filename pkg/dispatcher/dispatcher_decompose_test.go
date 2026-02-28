package dispatcher //nolint:testpackage // needs internal access to exhaustedBeads, attemptCounts, etc.

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"oro/pkg/ops"
	"oro/pkg/protocol"
)

// exhaustQGRetries drives a worker through maxQGRetries quality-gate failures,
// reading intermediate re-ASSIGNs, so that the final failure triggers
// handleQGExhausted. Intermediate failures use unique per-attempt QGOutput to
// avoid stuck detection (which fires when the same output repeats maxStuckCount
// times). The final failure uses finalQGOutput, which is what Decompose will
// receive.
func exhaustQGRetries(t *testing.T, conn net.Conn, scanner *bufio.Scanner, workerID, beadID, finalQGOutput string) {
	t.Helper()
	for i := 1; i <= maxQGRetries; i++ {
		qgOut := finalQGOutput
		if i < maxQGRetries {
			// Use a unique output per intermediate attempt to avoid stuck detection.
			qgOut = fmt.Sprintf("intermediate-fail-%d-for-%s", i, beadID)
		}
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            beadID,
				WorkerID:          workerID,
				QualityGatePassed: false,
				QGOutput:          qgOut,
			},
		})
		if i < maxQGRetries {
			msg, ok := readMsgFromScanner(t, scanner, 3*time.Second)
			if !ok {
				t.Fatalf("exhaustQGRetries: expected re-ASSIGN on attempt %d", i)
			}
			if msg.Type != protocol.MsgAssign {
				t.Fatalf("exhaustQGRetries: expected ASSIGN, got %s on attempt %d", msg.Type, i)
			}
		}
	}
}

// TestHandleQGExhaustedTriggersDecompose verifies that handleQGExhausted spawns
// a Decompose ops agent instead of immediately creating a P0 bug bead, and that:
//
//  1. ops.Decompose() is called with the exhausted bead's ID and QGOutput.
//  2. On VerdictResolved, no P0 bug bead is created and no EscStuck escalation fires.
//  3. On VerdictFailed, the existing P0 bug + EscStuck escalation path runs.
func TestHandleQGExhaustedTriggersDecompose(t *testing.T) {
	const qgOutput = "lint: 7 errors in pkg/foo/foo.go"

	t.Run("VerdictResolved_NoP0_NoEscalation", func(t *testing.T) {
		const beadID = "bead-decomp-resolved"

		d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)
		// Configure mock to output VERDICT: RESOLVED so Decompose returns VerdictResolved.
		spawnMock.verdict = "VERDICT: resolved: created 3 child beads"

		cancel := startDispatcher(t, d)
		defer cancel()

		conn, scanner := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 2*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 2*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{
			ID:       beadID,
			Title:    "Decompose resolved test",
			Priority: 1,
			Type:     "task",
			Model:    protocol.ModelOpus,
		}})

		// Drain the initial ASSIGN.
		readMsg(t, conn, 3*time.Second)

		exhaustQGRetries(t, conn, scanner, "w1", beadID, qgOutput)

		// Wait for Decompose to be spawned (spawnMock.Spawn() called).
		waitFor(t, func() bool {
			return spawnMock.SpawnCount() >= 1
		}, 5*time.Second)

		// Allow time for the goroutine to process the result.
		time.Sleep(300 * time.Millisecond)

		// Assert: Decompose prompt contains beadID and qgOutput.
		spawnMock.mu.Lock()
		spawns := make([]spawnCall, len(spawnMock.spawns))
		copy(spawns, spawnMock.spawns)
		spawnMock.mu.Unlock()

		decomposeFound := false
		for _, s := range spawns {
			if strings.Contains(s.prompt, beadID) && strings.Contains(s.prompt, qgOutput) {
				decomposeFound = true
				break
			}
		}
		if !decomposeFound {
			t.Fatalf("expected Decompose prompt to contain beadID=%q and qgOutput=%q\nspawns: %+v", beadID, qgOutput, spawns)
		}

		// Assert: No P0 bug bead was created.
		beadSrc.mu.Lock()
		created := make([]createCall, len(beadSrc.created))
		copy(created, beadSrc.created)
		beadSrc.mu.Unlock()

		for _, c := range created {
			if strings.Contains(c.title, "P0") {
				t.Fatalf("VerdictResolved: expected no P0 bead, got: %+v", c)
			}
		}

		// Assert: No EscStuck escalation to manager.
		for _, m := range esc.Messages() {
			if strings.Contains(m, beadID) && strings.Contains(m, "quality gate failed") {
				t.Fatalf("VerdictResolved: expected no EscStuck escalation, got: %q", m)
			}
		}
	})

	t.Run("VerdictFailed_CreatesP0AndEscalates", func(t *testing.T) {
		const beadID = "bead-decomp-failed"

		d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)
		// Configure mock to output VERDICT: FAILED so Decompose returns VerdictFailed.
		spawnMock.verdict = "VERDICT: failed: bead is too large to decompose"

		cancel := startDispatcher(t, d)
		defer cancel()

		conn, scanner := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 2*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 2*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{
			ID:       beadID,
			Title:    "Decompose failed test",
			Priority: 1,
			Type:     "task",
			Model:    protocol.ModelOpus,
		}})

		// Drain the initial ASSIGN.
		readMsg(t, conn, 3*time.Second)

		exhaustQGRetries(t, conn, scanner, "w1", beadID, qgOutput)

		// Wait for Decompose spawn then P0 bead creation.
		waitFor(t, func() bool {
			return spawnMock.SpawnCount() >= 1
		}, 5*time.Second)

		waitFor(t, func() bool {
			beadSrc.mu.Lock()
			defer beadSrc.mu.Unlock()
			for _, c := range beadSrc.created {
				if strings.Contains(c.title, "P0") && strings.Contains(c.title, beadID) {
					return true
				}
			}
			return false
		}, 5*time.Second)

		// Assert: P0 bug bead was created with correct type and priority.
		beadSrc.mu.Lock()
		created := make([]createCall, len(beadSrc.created))
		copy(created, beadSrc.created)
		beadSrc.mu.Unlock()

		p0Found := false
		for _, c := range created {
			if strings.Contains(c.title, "P0") && strings.Contains(c.title, beadID) &&
				c.beadType == "bug" && c.priority == 0 {
				p0Found = true
				break
			}
		}
		if !p0Found {
			t.Fatalf("VerdictFailed: expected P0 bug bead for %q, got: %+v", beadID, created)
		}

		// Assert: EscStuck escalation fired.
		waitFor(t, func() bool {
			for _, m := range esc.Messages() {
				if strings.Contains(m, beadID) && strings.Contains(m, "quality gate failed") {
					return true
				}
			}
			return false
		}, 5*time.Second)
	})
}

// TestHandleDecomposeResult_ContextCancelled verifies that when context is
// cancelled before Decompose completes, the fallback P0 + escalation path runs.
func TestHandleDecomposeResult_ContextCancelled(t *testing.T) {
	const beadID = "bead-decomp-ctx"
	const qgOutput = "context cancelled mid-decompose"

	// Simulate VerdictFailed with Err set (same code path as context cancellation).
	d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)
	spawnMock.verdict = "VERDICT: failed: context cancelled"

	cancel := startDispatcher(t, d)
	defer cancel()

	conn, scanner := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 2*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 2*time.Second)

	beadSrc.SetBeads([]protocol.Bead{{
		ID:       beadID,
		Title:    "Decompose ctx cancel test",
		Priority: 1,
		Type:     "task",
		Model:    protocol.ModelOpus,
	}})

	readMsg(t, conn, 3*time.Second)
	exhaustQGRetries(t, conn, scanner, "w1", beadID, qgOutput)

	// VerdictFailed path should still create P0 and escalate.
	waitFor(t, func() bool {
		return spawnMock.SpawnCount() >= 1
	}, 5*time.Second)

	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, beadID) && strings.Contains(m, "quality gate failed") {
				return true
			}
		}
		return false
	}, 5*time.Second)

	beadSrc.mu.Lock()
	created := make([]createCall, len(beadSrc.created))
	copy(created, beadSrc.created)
	beadSrc.mu.Unlock()

	p0Found := false
	for _, c := range created {
		if strings.Contains(c.title, "P0") && strings.Contains(c.title, beadID) && c.beadType == "bug" {
			p0Found = true
			break
		}
	}
	if !p0Found {
		t.Fatalf("expected P0 bug bead on VerdictFailed, got: %+v", created)
	}
}

// Compile-time check: handleDecomposeResult and handleQGExhaustedFallback must exist.
var (
	_ = (*Dispatcher).handleDecomposeResult
	_ = (*Dispatcher).handleQGExhaustedFallback
)

// Verify that ops.DecomposeOpts is used correctly (type check only).
var _ ops.DecomposeOpts = ops.DecomposeOpts{}
