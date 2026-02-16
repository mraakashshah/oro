package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestNoMultipleAssignmentsToSameBead verifies that when assignBead is called
// concurrently for the same bead, only one assignment succeeds because the bead
// status is updated to in_progress atomically before worktree creation.
// This test reproduces oro-ptp2: the race condition where multiple workers
// could be spawned for the same P0 bead, causing resource thrashing.
func TestNoMultipleAssignmentsToSameBead(t *testing.T) {
	t.Parallel()

	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Track how many worktrees are created for oro-test1.
	// If the race condition exists, both assignBead calls will create worktrees.
	var worktreeCreateCount atomic.Int32

	// Wrap worktree Create to add delay and track calls
	wtMgr.createFn = func(_ context.Context, beadID string) (string, string, error) {
		if beadID == "oro-test1" {
			// Simulate slow worktree creation to widen the race window
			time.Sleep(50 * time.Millisecond)
			worktreeCreateCount.Add(1)
		}
		return "/tmp/" + beadID, "branch-" + beadID, nil
	}

	// Connect two workers
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})

	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w2",
			ContextPct: 5,
		},
	})

	waitForWorkers(t, d, 2, 1*time.Second)

	// Setup bead detail
	beadSrc.mu.Lock()
	beadSrc.shown["oro-test1"] = &protocol.BeadDetail{
		ID:                 "oro-test1",
		Title:              "Test bead",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}
	beadSrc.mu.Unlock()

	// Get the workers
	d.mu.Lock()
	worker1 := d.workers["w1"]
	worker2 := d.workers["w2"]
	d.mu.Unlock()

	if worker1 == nil || worker2 == nil {
		t.Fatal("workers not registered")
	}

	// Concurrently call assignBead for the SAME bead from two "dispatcher threads".
	// This simulates the race condition where multiple dispatchers pick up the same
	// bead from Ready() before either marks it in_progress.
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		d.assignBead(ctx, worker1, protocol.Bead{ID: "oro-test1", Priority: 0})
	}()

	go func() {
		defer wg.Done()
		d.assignBead(ctx, worker2, protocol.Bead{ID: "oro-test1", Priority: 0})
	}()

	wg.Wait()

	// CRITICAL ASSERTION: Only one worktree should be created.
	// If both assignBead calls proceeded past the status check, worktreeCreateCount would be 2.
	// The fix ensures status is marked in_progress BEFORE worktree creation, so the
	// second call fails the status check and aborts early.
	actualWorktreeCreates := worktreeCreateCount.Load()
	if actualWorktreeCreates != 1 {
		t.Errorf("Race condition detected: expected 1 worktree create, got %d", actualWorktreeCreates)
	}

	// Verify: Only one update to in_progress
	beadSrc.mu.Lock()
	status := beadSrc.updated["oro-test1"]
	beadSrc.mu.Unlock()

	if status != "in_progress" {
		t.Errorf("Expected bead oro-test1 marked in_progress, got %q", status)
	}

	// Verify: Only one worker is busy with oro-test1
	d.mu.Lock()
	busyCount := 0
	for _, w := range d.workers {
		if w.beadID == "oro-test1" && w.state == protocol.WorkerBusy {
			busyCount++
		}
	}
	d.mu.Unlock()

	if busyCount != 1 {
		t.Errorf("Expected exactly 1 busy worker on oro-test1, got %d", busyCount)
	}
}
