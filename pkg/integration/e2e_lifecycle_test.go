// Package integration_test provides end-to-end lifecycle tests for oro.
package integration_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"oro/pkg/dispatcher"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/worker"

	_ "modernc.org/sqlite"
)

// trackingBeadSource extends mockBeadSource with Close tracking.
type trackingBeadSource struct {
	mu      sync.Mutex
	beads   []protocol.Bead
	closed  []string
	closeMu sync.Mutex
}

func (m *trackingBeadSource) Ready(_ context.Context) ([]protocol.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.Bead, len(m.beads))
	copy(out, m.beads)
	return out, nil
}

func (m *trackingBeadSource) Show(_ context.Context, id string) (*protocol.BeadDetail, error) {
	return &protocol.BeadDetail{Title: id, AcceptanceCriteria: "Test: auto | Assert: PASS"}, nil
}

func (m *trackingBeadSource) Close(_ context.Context, id string, _ string) error {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	m.closed = append(m.closed, id)
	return nil
}

func (m *trackingBeadSource) Create(_ context.Context, _, _ string, _ int, _, _ string) (string, error) {
	return "", nil
}

func (m *trackingBeadSource) Sync(_ context.Context) error { return nil }

func (m *trackingBeadSource) AllChildrenClosed(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (m *trackingBeadSource) SetBeads(beads []protocol.Bead) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.beads = beads
}

func (m *trackingBeadSource) ClosedBeads() []string {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	dst := make([]string, len(m.closed))
	copy(dst, m.closed)
	return dst
}

// TestE2E_FullLifecycle exercises the complete oro lifecycle in a single test:
//
//  1. Start directive triggers assignment loop
//  2. Mock BeadSource returns a ready bead
//  3. Worker receives MsgAssign, transitions Idle→Busy
//  4. Worker sends heartbeats
//  5. Worker sends MsgDone with QualityGatePassed=true
//  6. Dispatcher merges via mock GitRunner
//  7. Dispatcher closes bead via BeadSource.Close
//  8. Stop directive triggers graceful shutdown (context cancel → MsgPrepareShutdown)
//  9. Worker sends MsgHandoff + MsgShutdownApproved
//  10. Clean drain verified (dispatcher exits without error)
func TestE2E_FullLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	db := newTestDB(t)

	sockPath := fmt.Sprintf("/tmp/oro-e2e-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	beadSrc := &trackingBeadSource{}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}
	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)
	opsSpawner := ops.NewSpawner(&mockOpsSpawner{})

	cfg := dispatcher.Config{
		SocketPath:           sockPath,
		DBPath:               ":memory:",
		MaxWorkers:           5,
		HeartbeatTimeout:     5 * time.Second,
		PollInterval:         50 * time.Millisecond,
		FallbackPollInterval: 50 * time.Millisecond,
		ShutdownTimeout:      2 * time.Second,
	}

	d, err := dispatcher.New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc)
	if err != nil {
		t.Fatalf("dispatcher.New: %v", err)
	}

	// --- Phase 1: Start dispatcher ---
	ctx, cancel := context.WithCancel(context.Background())
	dispatcherErrCh := make(chan error, 1)
	go func() { dispatcherErrCh <- d.Run(ctx) }()

	waitFor(t, 2*time.Second, "dispatcher listener ready", func() bool {
		conn, dialErr := net.Dial("unix", sockPath)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	})

	// --- Phase 2: Connect worker ---
	spawner := newMockWorkerSpawner()
	w, err := worker.New("w-e2e-1", sockPath, spawner)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}

	workerErrCh := make(chan error, 1)
	workerCtx, workerCancel := context.WithCancel(ctx)
	go func() { workerErrCh <- w.Run(workerCtx) }()
	t.Cleanup(func() { workerCancel() })

	// Register worker with initial heartbeat
	if err := w.SendHeartbeat(ctx, 5); err != nil {
		t.Fatalf("initial heartbeat: %v", err)
	}

	waitFor(t, 2*time.Second, "worker registered", func() bool {
		return d.ConnectedWorkers() >= 1
	})

	// --- Phase 3: Start directive + bead assignment ---
	sendDirective(t, sockPath, "start")
	waitFor(t, 2*time.Second, "dispatcher running", func() bool {
		return d.GetState() == dispatcher.StateRunning
	})

	beadSrc.SetBeads([]protocol.Bead{
		{ID: "e2e-bead-1", Title: "E2E lifecycle bead", Priority: 1, Type: "task"},
	})

	// Wait for assignment
	waitFor(t, 3*time.Second, "worker spawned subprocess", func() bool {
		return len(spawner.SpawnCalls()) > 0
	})

	// Verify correct worktree path
	calls := spawner.SpawnCalls()
	if calls[0].Workdir != "/tmp/worktree-e2e-bead-1" {
		t.Errorf("workdir = %q, want /tmp/worktree-e2e-bead-1", calls[0].Workdir)
	}

	// Verify worker is Busy
	waitFor(t, 2*time.Second, "worker busy", func() bool {
		st, bid, ok := d.WorkerInfo("w-e2e-1")
		return ok && st == protocol.WorkerBusy && bid == "e2e-bead-1"
	})

	// --- Phase 4: Heartbeat during work ---
	if err := w.SendHeartbeat(ctx, 25); err != nil {
		t.Fatalf("mid-work heartbeat: %v", err)
	}
	waitFor(t, 2*time.Second, "heartbeat event logged", func() bool {
		return eventCount(t, db, "heartbeat") >= 2
	})

	// --- Phase 5: Done with QG passed ---
	beadSrc.SetBeads(nil) // Clear to prevent re-assignment
	if err := w.SendDone(ctx, true, ""); err != nil {
		t.Fatalf("send done: %v", err)
	}

	// --- Phase 6: Verify merge ---
	waitFor(t, 3*time.Second, "merged event", func() bool {
		return eventCount(t, db, "merged") > 0
	})

	// --- Phase 7: Verify bead closed via BeadSource.Close ---
	waitFor(t, 2*time.Second, "bead closed", func() bool {
		return len(beadSrc.ClosedBeads()) > 0
	})
	closedBeads := beadSrc.ClosedBeads()
	if closedBeads[0] != "e2e-bead-1" {
		t.Errorf("closed bead = %q, want e2e-bead-1", closedBeads[0])
	}

	// Verify assignment completed in DB
	var status string
	if err := db.QueryRow(`SELECT status FROM assignments WHERE bead_id='e2e-bead-1'`).Scan(&status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "completed" {
		t.Errorf("assignment status = %q, want completed", status)
	}

	// Worker should return to idle
	waitFor(t, 2*time.Second, "worker idle", func() bool {
		st, _, ok := d.WorkerInfo("w-e2e-1")
		return ok && st == protocol.WorkerIdle
	})

	// --- Phase 8: Graceful shutdown via context cancel ---
	// (The dispatcher's shutdown sequence sends PREPARE_SHUTDOWN to all workers)
	cancel()

	// --- Phase 9: Worker sends handoff + shutdown approved ---
	// The worker handles PREPARE_SHUTDOWN automatically in its Run loop by
	// sending handoff and shutdown approved. We verify the dispatcher exits cleanly.

	// --- Phase 10: Clean drain ---
	select {
	case err := <-dispatcherErrCh:
		if err != nil {
			t.Fatalf("dispatcher error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dispatcher shutdown timed out")
	}

	// Verify shutdown events were logged
	waitFor(t, 1*time.Second, "shutdown events", func() bool {
		return eventCount(t, db, "shutdown_approved") > 0 ||
			eventCount(t, db, "worker_dead") > 0 ||
			d.ConnectedWorkers() == 0
	})
}
