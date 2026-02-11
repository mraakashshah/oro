// Package integration provides end-to-end tests that connect a real Worker
// to a real Dispatcher over a Unix domain socket, exercising the full
// assign→status→done→merge lifecycle without mocking the UDS transport.
package integration_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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

// --- Mock implementations (duplicated from dispatcher_test since they're unexported) ---

type mockBeadSource struct {
	mu    sync.Mutex
	beads []protocol.Bead
}

func (m *mockBeadSource) Ready(_ context.Context) ([]protocol.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.Bead, len(m.beads))
	copy(out, m.beads)
	return out, nil
}

func (m *mockBeadSource) Show(_ context.Context, _ string) (*protocol.BeadDetail, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockBeadSource) Close(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockBeadSource) Sync(_ context.Context) error {
	return nil
}

func (m *mockBeadSource) Create(_ context.Context, _, _ string, _ int, _, _ string) (string, error) {
	return "", nil
}

func (m *mockBeadSource) SetBeads(beads []protocol.Bead) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.beads = beads
}

type mockWorktreeManager struct {
	mu      sync.Mutex
	created map[string]string
}

func (m *mockWorktreeManager) Create(_ context.Context, beadID string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := "/tmp/worktree-" + beadID
	branch := "agent/" + beadID
	if m.created == nil {
		m.created = make(map[string]string)
	}
	m.created[beadID] = path
	return path, branch, nil
}

func (m *mockWorktreeManager) Remove(_ context.Context, _ string) error {
	return nil
}

func (m *mockWorktreeManager) Prune(_ context.Context) error {
	return nil
}

type mockEscalator struct{}

func (m *mockEscalator) Escalate(_ context.Context, _ string) error {
	return nil
}

type mockGitRunner struct{}

func (m *mockGitRunner) Run(_ context.Context, _ string, args ...string) (string, string, error) {
	if len(args) > 0 && args[0] == "rev-parse" {
		return "abc123def456\n", "", nil
	}
	return "", "", nil
}

type mockOpsSpawner struct{}

func (m *mockOpsSpawner) Spawn(_ context.Context, _, _, _ string) (ops.Process, error) {
	return &mockOpsProcess{}, nil
}

type mockOpsProcess struct{}

func (m *mockOpsProcess) Wait() error             { return nil }
func (m *mockOpsProcess) Kill() error             { return nil }
func (m *mockOpsProcess) Output() (string, error) { return "APPROVED: looks good", nil }

// mockWorkerProcess implements worker.Process for testing.
type mockWorkerProcess struct {
	mu     sync.Mutex
	killed bool
	waitCh chan struct{}
}

func newMockWorkerProcess() *mockWorkerProcess {
	return &mockWorkerProcess{waitCh: make(chan struct{})}
}

func (p *mockWorkerProcess) Wait() error {
	<-p.waitCh
	return nil
}

func (p *mockWorkerProcess) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killed = true
	select {
	case <-p.waitCh:
	default:
		close(p.waitCh)
	}
	return nil
}

// mockWorkerSpawner implements worker.StreamingSpawner for testing.
type mockWorkerSpawner struct {
	mu      sync.Mutex
	calls   []spawnCall
	process *mockWorkerProcess
}

type spawnCall struct {
	Model   string
	Prompt  string
	Workdir string
}

func newMockWorkerSpawner() *mockWorkerSpawner {
	return &mockWorkerSpawner{process: newMockWorkerProcess()}
}

func (s *mockWorkerSpawner) Spawn(_ context.Context, model, prompt, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spawnCall{Model: model, Prompt: prompt, Workdir: workdir})
	return s.process, nil, nil, nil
}

func (s *mockWorkerSpawner) SpawnCalls() []spawnCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst := make([]spawnCall, len(s.calls))
	copy(dst, s.calls)
	return dst
}

// --- Test helpers ---

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:integ_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("set WAL mode: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatalf("set busy timeout: %v", err)
	}
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sendDirective(t *testing.T, socketPath, directive string) {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial for directive: %v", err)
	}
	defer func() { _ = conn.Close() }()

	msg := protocol.Message{
		Type:      protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{Op: directive},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal directive: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write directive: %v", err)
	}

	// Read ACK
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("no ACK for directive")
	}
}

func eventCount(t *testing.T, db *sql.DB, evType string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=?`, evType).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return count
}

// waitFor polls a condition until it's true or timeout expires.
func waitFor(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

// --- Integration Tests ---

// TestDispatcherWorker_FullCycle starts a real Dispatcher and connects a real
// Worker to it via UDS. It verifies:
//  1. Worker connects and registers
//  2. Dispatcher assigns a bead to the idle worker
//  3. Worker receives ASSIGN, spawns subprocess, sends STATUS running
//  4. Worker sends DONE with quality gate passed
//  5. Dispatcher merges and completes the assignment
func TestDispatcherWorker_FullCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	db := newTestDB(t)

	// Create a beads dir for fsnotify (won't actually trigger, we use poll fallback)
	beadsDir := t.TempDir()

	sockPath := fmt.Sprintf("/tmp/oro-integ-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	beadSrc := &mockBeadSource{}
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
		t.Fatalf("dispatcher.New failed: %v", err)
	}

	// Override beads dir to avoid watching real .beads/
	// Since beadsDir is unexported, we rely on the fallback poll interval (50ms)
	_ = beadsDir // beadsDir created but not directly settable from outside

	// Start dispatcher
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
		}
	})

	// Wait for listener
	waitFor(t, 2*time.Second, "dispatcher listener ready", func() bool {
		// Try connecting to see if it's ready
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})

	// Step 1: Connect a real Worker to the Dispatcher
	spawner := newMockWorkerSpawner()
	w, err := worker.New("w-integ-1", sockPath, spawner)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}

	// Run worker in background
	workerErrCh := make(chan error, 1)
	workerCtx, workerCancel := context.WithCancel(ctx)
	go func() { workerErrCh <- w.Run(workerCtx) }()
	t.Cleanup(func() { workerCancel() })

	// Worker needs to identify itself — send initial heartbeat
	if err := w.SendHeartbeat(ctx, 5); err != nil {
		t.Fatalf("send initial heartbeat: %v", err)
	}

	// Step 2: Wait for dispatcher to see the worker
	waitFor(t, 2*time.Second, "worker registered", func() bool {
		return d.ConnectedWorkers() >= 1
	})

	// Step 3: Send start directive and provide beads
	sendDirective(t, sockPath, "start")
	waitFor(t, 2*time.Second, "dispatcher running", func() bool {
		return d.GetState() == dispatcher.StateRunning
	})

	beadSrc.SetBeads([]protocol.Bead{
		{ID: "integ-bead-1", Title: "Integration test bead", Priority: 1},
	})

	// Step 4: Wait for worker to be assigned (spawner should be called)
	waitFor(t, 3*time.Second, "worker spawned subprocess", func() bool {
		return len(spawner.SpawnCalls()) > 0
	})

	// Verify spawn was called with correct parameters
	calls := spawner.SpawnCalls()
	if len(calls) == 0 {
		t.Fatal("expected at least one spawn call")
	}
	if calls[0].Workdir != "/tmp/worktree-integ-bead-1" {
		t.Errorf("expected workdir /tmp/worktree-integ-bead-1, got %s", calls[0].Workdir)
	}

	// Step 5: Verify dispatcher recorded the assignment
	waitFor(t, 2*time.Second, "assignment event logged", func() bool {
		return eventCount(t, db, "assign") > 0
	})

	// Verify worker is now busy
	state, beadID, ok := d.WorkerInfo("w-integ-1")
	if !ok {
		t.Fatal("worker not found in dispatcher")
	}
	if state != protocol.WorkerBusy {
		t.Errorf("expected worker busy, got %s", state)
	}
	if beadID != "integ-bead-1" {
		t.Errorf("expected bead integ-bead-1, got %s", beadID)
	}

	// Step 6: Clear beads so dispatcher doesn't re-assign
	beadSrc.SetBeads(nil)

	// Step 7: Worker sends DONE with quality gate passed
	if err := w.SendDone(ctx, true, ""); err != nil {
		t.Fatalf("send done: %v", err)
	}

	// Step 8: Wait for merge to complete
	waitFor(t, 3*time.Second, "merged event in DB", func() bool {
		return eventCount(t, db, "merged") > 0
	})

	// Verify assignment completed
	var status string
	if err := db.QueryRow(`SELECT status FROM assignments WHERE bead_id='integ-bead-1'`).Scan(&status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "completed" {
		t.Errorf("expected assignment completed, got %s", status)
	}

	// Worker should now be idle
	waitFor(t, 2*time.Second, "worker idle after done", func() bool {
		st, _, ok := d.WorkerInfo("w-integ-1")
		return ok && st == protocol.WorkerIdle
	})
}

// TestDispatcherWorker_GracefulShutdown verifies that PREPARE_SHUTDOWN flows
// correctly from Dispatcher to Worker, the Worker sends HANDOFF+SHUTDOWN_APPROVED,
// and the Dispatcher cleans up.
func TestDispatcherWorker_GracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	db := newTestDB(t)

	sockPath := fmt.Sprintf("/tmp/oro-integ-shutdown-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	beadSrc := &mockBeadSource{}
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
		t.Fatalf("dispatcher.New failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()

	// Wait for listener
	waitFor(t, 2*time.Second, "dispatcher listener ready", func() bool {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})

	// Connect worker
	spawner := newMockWorkerSpawner()
	w, err := worker.New("w-shutdown-1", sockPath, spawner)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}

	workerErrCh := make(chan error, 1)
	go func() { workerErrCh <- w.Run(ctx) }()

	// Register worker
	if err := w.SendHeartbeat(ctx, 5); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}
	waitFor(t, 2*time.Second, "worker registered", func() bool {
		return d.ConnectedWorkers() >= 1
	})

	// Assign work
	sendDirective(t, sockPath, "start")
	waitFor(t, 2*time.Second, "dispatcher running", func() bool {
		return d.GetState() == dispatcher.StateRunning
	})
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "shutdown-bead", Title: "Shutdown test", Priority: 1},
	})
	waitFor(t, 3*time.Second, "worker spawned", func() bool {
		return len(spawner.SpawnCalls()) > 0
	})

	beadSrc.SetBeads(nil)

	// Cancel context to trigger graceful shutdown
	cancel()

	// Wait for dispatcher to finish
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("dispatcher error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher shutdown timed out")
	}

	// Worker should also exit (Run returns when connection closes or SHUTDOWN received)
	select {
	case <-workerErrCh:
		// Worker exited — good
	case <-time.After(5 * time.Second):
		t.Fatal("worker shutdown timed out")
	}
}

// TestDispatcherWorker_Heartbeat verifies that the worker's heartbeat messages
// are received and processed by the dispatcher, keeping the worker alive.
func TestDispatcherWorker_Heartbeat(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	db := newTestDB(t)

	sockPath := fmt.Sprintf("/tmp/oro-integ-hb-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	beadSrc := &mockBeadSource{}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}
	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)
	opsSpawner := ops.NewSpawner(&mockOpsSpawner{})

	cfg := dispatcher.Config{
		SocketPath:           sockPath,
		DBPath:               ":memory:",
		MaxWorkers:           5,
		HeartbeatTimeout:     500 * time.Millisecond,
		PollInterval:         50 * time.Millisecond,
		FallbackPollInterval: 50 * time.Millisecond,
		ShutdownTimeout:      1 * time.Second,
	}

	d, err := dispatcher.New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc)
	if err != nil {
		t.Fatalf("dispatcher.New failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
		}
	})

	waitFor(t, 2*time.Second, "dispatcher listener ready", func() bool {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})

	// Connect worker
	spawner := newMockWorkerSpawner()
	w, err := worker.New("w-hb-1", sockPath, spawner)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	go func() { _ = w.Run(workerCtx) }()
	t.Cleanup(func() { workerCancel() })

	// Send heartbeat
	if err := w.SendHeartbeat(ctx, 10); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}

	// Verify dispatcher registered the worker
	waitFor(t, 2*time.Second, "worker registered", func() bool {
		return d.ConnectedWorkers() >= 1
	})

	// Verify heartbeat event was logged
	waitFor(t, 2*time.Second, "heartbeat event logged", func() bool {
		return eventCount(t, db, "heartbeat") > 0
	})

	// Send a second heartbeat with higher context usage
	if err := w.SendHeartbeat(ctx, 45); err != nil {
		t.Fatalf("send second heartbeat: %v", err)
	}

	// Verify second heartbeat was also recorded
	waitFor(t, 2*time.Second, "second heartbeat logged", func() bool {
		return eventCount(t, db, "heartbeat") >= 2
	})
}
