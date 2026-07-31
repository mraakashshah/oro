package dispatcher //nolint:testpackage // white-box test needs access to unexported types

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"oro/pkg/testutil/qgserial"

	"oro/pkg/protocol"
)

// TestDispatcherStartSequence_AssignsWorkerUnderParallelStress verifies that
// when the dispatcher starts and multiple workers connect concurrently, every
// ready bead is assigned to exactly one worker — no double-assignments, no bead
// left unassigned, no worker left idle while beads remain.
//
// Run with -race -count=20 to confirm there are no data races and the
// assignment is deterministic across many iterations.
func TestDispatcherStartSequence_AssignsWorkerUnderParallelStress(t *testing.T) {
	qgserial.RequireSerial(t)
	const numWorkers = 4
	const opTimeout = 10 * time.Second

	d, beadSrc, wt, _, _, _ := newTestDispatcher(t)
	d.cfg.HeartbeatTimeout = opTimeout
	wt.createFn = func(_ context.Context, bID, _ string) (string, string, error) {
		return "/tmp/stress-wt-" + bID, "agent/" + bID, nil
	}

	beads := make([]protocol.Bead, numWorkers)
	for i := range beads {
		beads[i] = protocol.Bead{
			ID:       fmt.Sprintf("stress-%d", i),
			Priority: i,
			Type:     "task",
		}
	}
	beadSrc.SetBeads(beads)

	startDispatcherWithTimeout(t, d, opTimeout)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, opTimeout)

	// Open all connections before sending any heartbeats so all workers
	// can register concurrently, maximising the race window.
	conns := make([]net.Conn, numWorkers)
	scanners := make([]*bufio.Scanner, numWorkers)
	for i := range conns {
		conns[i], scanners[i] = connectWorker(t, d.cfg.SocketPath)
	}

	// Send all heartbeats in parallel to simulate simultaneous worker connects.
	var sendWg sync.WaitGroup
	sendWg.Add(numWorkers)
	for i, conn := range conns {
		i, conn := i, conn
		go func() {
			defer sendWg.Done()
			sendMsg(t, conn, protocol.Message{
				Type: protocol.MsgHeartbeat,
				Heartbeat: &protocol.HeartbeatPayload{
					WorkerID:   fmt.Sprintf("stress-worker-%d", i),
					ContextPct: 5,
				},
			})
		}()
	}
	sendWg.Wait()

	// Collect ASSIGN messages from all workers concurrently.
	// Results are sent on a channel to avoid calling t.Fatal from goroutines.
	type result struct {
		idx    int
		beadID string
		err    string
	}
	resultCh := make(chan result, numWorkers)

	for i := range conns {
		i := i
		go func() {
			_ = conns[i].SetReadDeadline(time.Now().Add(opTimeout))
			if !scanners[i].Scan() {
				resultCh <- result{idx: i, err: fmt.Sprintf("worker %d: timed out before ASSIGN", i)}
				return
			}
			var msg protocol.Message
			if unmarshalErr := json.Unmarshal(scanners[i].Bytes(), &msg); unmarshalErr != nil {
				resultCh <- result{idx: i, err: fmt.Sprintf("worker %d: unmarshal: %v", i, unmarshalErr)}
				return
			}
			if msg.Type != protocol.MsgAssign || msg.Assign == nil {
				resultCh <- result{idx: i, err: fmt.Sprintf("worker %d: got %q, want ASSIGN", i, msg.Type)}
				return
			}
			resultCh <- result{idx: i, beadID: msg.Assign.BeadID}
		}()
	}

	// Drain results in the main goroutine and check for double-assignments.
	assigned := make(map[string]int) // beadID → assignment count
	for range conns {
		r := <-resultCh
		if r.err != "" {
			t.Error(r.err)
			continue
		}
		assigned[r.beadID]++
	}

	// Every bead must be assigned exactly once.
	for _, bead := range beads {
		if n := assigned[bead.ID]; n != 1 {
			t.Errorf("bead %s: assigned %d time(s), want 1", bead.ID, n)
		}
	}
}
