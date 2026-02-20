package worker_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// TestHandleExitClaimedRace verifies that exactly one of awaitSubprocessAndReport
// or checkSubprocessHealth sends DONE when the subprocess exits.
//
// The bug: awaitSubprocessAndReport has a broken compare-and-swap —
//
//	w.handleExitClaimed = true           // sets unconditionally
//	claimed := w.handleExitClaimed       // always true after the line above
//	if !claimed { return }               // DEAD CODE: never exits early
//
// When checkSubprocessHealth claims first (sends DONE), awaitSubprocessAndReport
// still proceeds and sends a second DONE. The fix: read old value, set
// conditionally, then check if we won the race (alreadyClaimed).
//
// Run with: go test ./pkg/worker/ -run TestHandleExitClaimed -v -race -count=10
func TestHandleExitClaimedRace(t *testing.T) {
	t.Parallel()

	// quality_gate.sh that fails immediately — awaitSubprocessAndReport sends
	// DONE quickly if it gets past the broken CAS, widening the double-DONE window.
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}

	proc := newMockProcess()
	spawner := &mockSpawner{process: proc}

	dispatcherConn, workerConn := net.Pipe()
	t.Cleanup(func() { _ = dispatcherConn.Close() })

	w := worker.NewWithConn("w-claim-race", workerConn, spawner)
	// 1ms poll: delay = min(2*1ms, 250ms) = 2ms, so checkSubprocessHealth's
	// 2nd tick races tightly with awaitSubprocessAndReport's delay expiry.
	w.SetContextPollInterval(1 * time.Millisecond)
	w.SetHeartbeatInterval(1 * time.Hour) // suppress heartbeats during test

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Collect all messages from the worker. net.Pipe writes block until read,
	// so we drain continuously in a goroutine.
	var (
		msgMu   sync.Mutex
		allMsgs []protocol.Message
	)
	go func() {
		scanner := bufio.NewScanner(dispatcherConn)
		for scanner.Scan() {
			var m protocol.Message
			if json.Unmarshal(scanner.Bytes(), &m) == nil {
				msgMu.Lock()
				allMsgs = append(allMsgs, m)
				msgMu.Unlock()
			}
		}
	}()

	workerErrCh := make(chan error, 1)
	go func() { workerErrCh <- w.Run(ctx) }()

	// Wait for initial heartbeat.
	waitFor(t, func() bool {
		msgMu.Lock()
		defer msgMu.Unlock()
		for _, m := range allMsgs {
			if m.Type == protocol.MsgHeartbeat {
				return true
			}
		}
		return false
	}, 2*time.Second)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-race",
			Worktree: tmpDir,
		},
	})

	// Wait for STATUS running.
	waitFor(t, func() bool {
		msgMu.Lock()
		defer msgMu.Unlock()
		for _, m := range allMsgs {
			if m.Type == protocol.MsgStatus && m.Status != nil && m.Status.State == "running" {
				return true
			}
		}
		return false
	}, 2*time.Second)

	// Subprocess exits — starts the race between awaitSubprocessAndReport and
	// checkSubprocessHealth. With 1ms polling, both try to claim at ~2ms.
	close(proc.waitCh)

	// Wait for at least one DONE to arrive.
	waitFor(t, func() bool {
		msgMu.Lock()
		defer msgMu.Unlock()
		for _, m := range allMsgs {
			if m.Type == protocol.MsgDone {
				return true
			}
		}
		return false
	}, 5*time.Second)

	// Allow a brief window for a second DONE to arrive (buggy code sends two).
	time.Sleep(200 * time.Millisecond)

	// Count DONE messages — exactly 1 expected.
	msgMu.Lock()
	doneCount := 0
	for _, m := range allMsgs {
		if m.Type == protocol.MsgDone {
			doneCount++
		}
	}
	msgMu.Unlock()

	if doneCount != 1 {
		t.Errorf("expected exactly 1 DONE message, got %d — broken handleExitClaimed CAS allows double-claim", doneCount)
	}
}
