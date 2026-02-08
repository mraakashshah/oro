package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// --- Mock implementations ---

type mockBeadSource struct {
	mu    sync.Mutex
	beads []Bead
	shown map[string]*BeadDetail
}

func (m *mockBeadSource) Ready(_ context.Context) ([]Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Bead, len(m.beads))
	copy(out, m.beads)
	return out, nil
}

func (m *mockBeadSource) Show(_ context.Context, id string) (*BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.shown[id]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("bead %s not found", id)
}

func (m *mockBeadSource) Close(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockBeadSource) SetBeads(beads []Bead) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.beads = beads
}

type mockWorktreeManager struct {
	mu       sync.Mutex
	created  map[string]string // beadID -> worktree path
	removed  []string
	createFn func(ctx context.Context, beadID string) (string, string, error)
}

func (m *mockWorktreeManager) Create(ctx context.Context, beadID string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createFn != nil {
		return m.createFn(ctx, beadID)
	}
	path := "/tmp/worktree-" + beadID
	branch := "agent/" + beadID
	if m.created == nil {
		m.created = make(map[string]string)
	}
	m.created[beadID] = path
	return path, branch, nil
}

func (m *mockWorktreeManager) Remove(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removed = append(m.removed, path)
	return nil
}

type mockEscalator struct {
	mu       sync.Mutex
	messages []string
}

func (m *mockEscalator) Escalate(_ context.Context, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

func (m *mockEscalator) Messages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.messages))
	copy(out, m.messages)
	return out
}

// mockGitRunner for merge.Coordinator — always succeeds unless configured otherwise.
type mockGitRunner struct {
	mu       sync.Mutex
	failOn   string // if set, fail when this arg is in the command
	conflict bool   // if true, rebase returns conflict error
}

func (m *mockGitRunner) Run(_ context.Context, _ string, args ...string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, a := range args {
		if m.failOn != "" && a == m.failOn {
			return "", "", fmt.Errorf("mock git failure on %s", a)
		}
	}

	// Check if this is a rebase and we should conflict
	if m.conflict && len(args) > 0 && args[0] == "rebase" {
		if len(args) > 1 && args[1] == "--abort" {
			return "", "", nil // abort succeeds
		}
		return "", "CONFLICT (content): Merge conflict in file.go\n", fmt.Errorf("rebase failed")
	}

	// rev-parse HEAD returns a fake SHA
	if len(args) > 0 && args[0] == "rev-parse" {
		return "abc123def456\n", "", nil
	}
	return "", "", nil
}

// mockSubprocessSpawner for ops.Spawner
type mockSubprocessSpawner struct {
	mu       sync.Mutex
	verdict  string
	spawnErr error
}

func (m *mockSubprocessSpawner) Spawn(_ context.Context, _ string, _ string, _ string) (ops.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.spawnErr != nil {
		return nil, m.spawnErr
	}
	return &mockProcess{output: m.verdict}, nil
}

type mockProcess struct {
	output string
}

func (m *mockProcess) Wait() error             { return nil }
func (m *mockProcess) Kill() error             { return nil }
func (m *mockProcess) Output() (string, error) { return m.output, nil }

// --- Test helpers ---

// newTestDB creates an in-memory SQLite database with the protocol schema.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	// Use a shared-cache in-memory DB so all connections see the same data.
	dsn := fmt.Sprintf("file:test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// Enable WAL mode
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

// newTestDispatcher creates a Dispatcher with mocks and an in-memory DB.
// It returns the dispatcher and all mocks for assertions.
func newTestDispatcher(t *testing.T) (*Dispatcher, *mockBeadSource, *mockWorktreeManager, *mockEscalator, *mockGitRunner, *mockSubprocessSpawner) {
	t.Helper()
	db := newTestDB(t)

	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)

	spawnMock := &mockSubprocessSpawner{verdict: "APPROVED: looks good"}
	opsSpawner := ops.NewSpawner(spawnMock)

	beadSrc := &mockBeadSource{
		beads: []Bead{},
		shown: make(map[string]*BeadDetail),
	}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}

	// Use short path for UDS — macOS limits to 108 chars.
	sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		MaxWorkers:       5,
		HeartbeatTimeout: 500 * time.Millisecond,
		PollInterval:     50 * time.Millisecond,
	}

	d := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc)
	return d, beadSrc, wtMgr, esc, gitRunner, spawnMock
}

// startDispatcher starts the dispatcher in the background and returns a cancel func.
func startDispatcher(t *testing.T, d *Dispatcher) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run(ctx)
	}()

	// Wait for the listener to be ready
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		ln := d.listener
		d.mu.Unlock()
		if ln != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Cleanup(func() {
		cancel()
		// Drain error channel
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})

	return cancel
}

// connectWorker connects a mock worker to the dispatcher's UDS socket and returns
// the connection and a scanner for reading messages.
func connectWorker(t *testing.T, socketPath string) (net.Conn, *bufio.Scanner) {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("connect to dispatcher: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	scanner := bufio.NewScanner(conn)
	return conn, scanner
}

// sendMsg sends a protocol.Message as line-delimited JSON over the connection.
func sendMsg(t *testing.T, conn net.Conn, msg protocol.Message) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readMsg reads one line-delimited JSON message from the scanner.
func readMsg(t *testing.T, conn net.Conn, timeout time.Duration) (protocol.Message, bool) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return protocol.Message{}, false
	}
	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return msg, true
}

// insertCommand inserts a pending command directly into SQLite.
func insertCommand(t *testing.T, db *sql.DB, directive string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO commands (directive, args, status) VALUES (?, '', 'pending')`, directive)
	if err != nil {
		t.Fatalf("insert command: %v", err)
	}
}

// waitForState polls until the dispatcher reaches the expected state or times out.
func waitForState(t *testing.T, d *Dispatcher, want State, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.GetState() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("dispatcher state: got %s, want %s", d.GetState(), want)
}

// waitForWorkers polls until the expected number of workers are connected.
func waitForWorkers(t *testing.T, d *Dispatcher, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.ConnectedWorkers() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connected workers: got %d, want %d", d.ConnectedWorkers(), want)
}

// waitForWorkerState polls until a specific worker reaches the expected state.
func waitForWorkerState(t *testing.T, d *Dispatcher, workerID string, want WorkerState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, _, ok := d.WorkerInfo(workerID)
		if ok && st == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, _, _ := d.WorkerInfo(workerID)
	t.Fatalf("worker %s state: got %s, want %s", workerID, st, want)
}

// eventCount returns the number of events with the given type.
func eventCount(t *testing.T, db *sql.DB, evType string) int {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=?`, evType).Scan(&count)
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	return count
}

// --- Tests ---

func TestDispatcher_StartsInert(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	if d.GetState() != StateInert {
		t.Fatalf("expected inert state, got %s", d.GetState())
	}

	// Even with beads available, no assignments should happen in inert state
	time.Sleep(100 * time.Millisecond)
	if d.GetState() != StateInert {
		t.Fatalf("dispatcher should remain inert without start directive")
	}
}

func TestDispatcher_StartDirective_BeginsAssigning(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Insert start command
	insertCommand(t, d.db, "start")

	waitForState(t, d, StateRunning, 1*time.Second)

	// Now add beads and connect a worker
	beadSrc.SetBeads([]Bead{{ID: "bead-1", Title: "Test", Priority: 1}})

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "worker-1",
			ContextPct: 10,
		},
	})

	waitForWorkers(t, d, 1, 1*time.Second)

	// Wait for assignment
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "bead-1" {
		t.Fatalf("expected bead-1, got %s", msg.Assign.BeadID)
	}
}

func TestDispatcher_AssignBead(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect worker first
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start + provide beads
	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-42", Title: "Build thing", Priority: 1}})

	// Read ASSIGN
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "bead-42" {
		t.Fatalf("expected bead-42, got %s", msg.Assign.BeadID)
	}
	if msg.Assign.Worktree == "" {
		t.Fatal("expected non-empty worktree path")
	}

	// Verify worker state changed to busy
	waitForWorkerState(t, d, "w1", WorkerBusy, 1*time.Second)
}

func TestDispatcher_WorkerDone_MergesClean(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect and assign
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-merge", Title: "Merge test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}

	// Clear beads so it doesn't re-assign
	beadSrc.SetBeads(nil)

	// Send DONE
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: "bead-merge", WorkerID: "w1"},
	})

	// Wait for merge to complete (logged as "merged" event)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "merged") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "merged") == 0 {
		t.Fatal("expected 'merged' event in DB")
	}

	// Assignment should be completed
	var status string
	err := d.db.QueryRow(`SELECT status FROM assignments WHERE bead_id='bead-merge'`).Scan(&status)
	if err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "completed" {
		t.Fatalf("expected completed, got %s", status)
	}
}

func TestDispatcher_WorkerDone_MergeConflict_SpawnsOpsAgent(t *testing.T) {
	d, beadSrc, _, _, gitRunner, _ := newTestDispatcher(t)
	// Configure git runner to return conflict on rebase
	gitRunner.mu.Lock()
	gitRunner.conflict = true
	gitRunner.mu.Unlock()

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-conflict", Title: "Conflict test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE — will trigger merge which conflicts
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: "bead-conflict", WorkerID: "w1"},
	})

	// Wait for merge_conflict event
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "merge_conflict") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "merge_conflict") == 0 {
		t.Fatal("expected 'merge_conflict' event — ops agent should have been spawned")
	}
}

func TestDispatcher_Handoff_RespawnsWorker(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-handoff", Title: "Handoff test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send HANDOFF
	sendMsg(t, conn, protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{BeadID: "bead-handoff", WorkerID: "w1"},
	})

	// Worker should receive SHUTDOWN
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected SHUTDOWN after handoff")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Verify handoff event logged
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "handoff") > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected 'handoff' event")
}

func TestDispatcher_HeartbeatTimeout_DetectsDeadWorker(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	// Use a very short heartbeat timeout
	d.cfg.HeartbeatTimeout = 100 * time.Millisecond

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-dead", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Assign work so the worker is busy (idle workers are not timed out)
	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-dead", Title: "Dead worker test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Don't send any more heartbeats — wait for timeout
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.ConnectedWorkers() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("worker should have been removed after heartbeat timeout, still have %d", d.ConnectedWorkers())
}

func TestDispatcher_ReadyForReview_SpawnsReviewer(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-review", Title: "Review test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send READY_FOR_REVIEW
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-review", WorkerID: "w1"},
	})

	// Worker state should change to reviewing
	waitForWorkerState(t, d, "w1", WorkerReviewing, 1*time.Second)

	// Verify event logged
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "ready_for_review") > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected 'ready_for_review' event")
}

func TestDispatcher_ReviewApproved_WorkerSignalsDone(t *testing.T) {
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	spawnMock.mu.Lock()
	spawnMock.verdict = "APPROVED: all tests pass"
	spawnMock.mu.Unlock()

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-approved", Title: "Approved test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send READY_FOR_REVIEW
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-approved", WorkerID: "w1"},
	})

	// Wait for review_approved event
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "review_approved") > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected 'review_approved' event")
}

func TestDispatcher_ReviewRejected_FeedbackSent(t *testing.T) {
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	spawnMock.mu.Lock()
	spawnMock.verdict = "REJECTED: missing tests for edge case"
	spawnMock.mu.Unlock()

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-rejected", Title: "Rejected test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rejected", WorkerID: "w1"},
	})

	// Wait for review_rejected event
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "review_rejected") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "review_rejected") == 0 {
		t.Fatal("expected 'review_rejected' event")
	}

	// After rejection, worker should receive re-ASSIGN with feedback (the bead re-assigned)
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected message after rejection")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN after rejection, got %s", msg.Type)
	}
}

func TestDispatcher_Reconnect_ResumesWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)

	// Send RECONNECT
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID:   "w-reconnect",
			BeadID:     "bead-reconnect",
			State:      "running",
			ContextPct: 30,
		},
	})

	// Wait for worker to be tracked as busy
	waitForWorkerState(t, d, "w-reconnect", WorkerBusy, 1*time.Second)

	// Verify reconnect event
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "reconnect") > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected 'reconnect' event")
}

func TestDispatcher_StopDirective_FinishesCurrent(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Start, then stop
	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	insertCommand(t, d.db, "stop")
	waitForState(t, d, StateStopping, 1*time.Second)

	// Connect a worker — it should NOT receive assignments in stopping state
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-stop", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-noassign", Title: "Should not be assigned", Priority: 1}})

	// Wait a couple poll cycles — no ASSIGN should arrive
	time.Sleep(200 * time.Millisecond)
	_, ok := readMsg(t, conn, 200*time.Millisecond)
	if ok {
		t.Fatal("should not receive ASSIGN in stopping state")
	}
}

func TestDispatcher_PauseDirective(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	insertCommand(t, d.db, "pause")
	waitForState(t, d, StatePaused, 1*time.Second)

	// No new assignments while paused
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-pause", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-paused", Title: "Paused", Priority: 1}})
	time.Sleep(200 * time.Millisecond)
	_, ok := readMsg(t, conn, 200*time.Millisecond)
	if ok {
		t.Fatal("should not receive ASSIGN in paused state")
	}
}

func TestDispatcher_Escalation(t *testing.T) {
	d, beadSrc, _, esc, gitRunner, _ := newTestDispatcher(t)
	// Configure git to fail (non-conflict failure)
	gitRunner.mu.Lock()
	gitRunner.failOn = "merge"
	gitRunner.mu.Unlock()

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-esc", Title: "Escalation test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE — merge will fail (not conflict) → escalation
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: "bead-esc", WorkerID: "w1"},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msgs := esc.Messages()
		if len(msgs) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected escalation message")
}

func TestDispatcher_ConcurrentWorkers(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	insertCommand(t, d.db, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{
		{ID: "bead-a", Title: "A", Priority: 1},
		{ID: "bead-b", Title: "B", Priority: 2},
		{ID: "bead-c", Title: "C", Priority: 3},
	})

	// Connect 3 workers
	conns := make([]net.Conn, 3)
	for i := 0; i < 3; i++ {
		wid := fmt.Sprintf("w-%d", i)
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		conns[i] = conn
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, ContextPct: 5},
		})
	}

	waitForWorkers(t, d, 3, 1*time.Second)

	// Each worker should receive an ASSIGN
	assigned := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, conn := range conns {
		wg.Add(1)
		go func(c net.Conn, _ int) {
			defer wg.Done()
			msg, ok := readMsg(t, c, 3*time.Second)
			if ok && msg.Type == protocol.MsgAssign && msg.Assign != nil {
				mu.Lock()
				assigned[msg.Assign.BeadID] = true
				mu.Unlock()
			}
		}(conn, i)
	}
	wg.Wait()

	if len(assigned) < 2 {
		t.Fatalf("expected at least 2 beads assigned to concurrent workers, got %d", len(assigned))
	}
}

// --- Pure function tests ---

func TestExtractWorkerID(t *testing.T) {
	tests := []struct {
		name string
		msg  protocol.Message
		want string
	}{
		{
			name: "heartbeat",
			msg:  protocol.Message{Type: protocol.MsgHeartbeat, Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1"}},
			want: "w1",
		},
		{
			name: "done",
			msg:  protocol.Message{Type: protocol.MsgDone, Done: &protocol.DonePayload{WorkerID: "w2"}},
			want: "w2",
		},
		{
			name: "reconnect",
			msg:  protocol.Message{Type: protocol.MsgReconnect, Reconnect: &protocol.ReconnectPayload{WorkerID: "w3"}},
			want: "w3",
		},
		{
			name: "empty",
			msg:  protocol.Message{Type: protocol.MsgAssign},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWorkerID(tt.msg)
			if got != tt.want {
				t.Fatalf("extractWorkerID: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfig_Defaults(t *testing.T) {
	cfg := Config{SocketPath: "/tmp/test.sock", DBPath: ":memory:"}
	resolved := cfg.withDefaults()
	if resolved.MaxWorkers != 5 {
		t.Fatalf("MaxWorkers: got %d, want 5", resolved.MaxWorkers)
	}
	if resolved.HeartbeatTimeout != 45*time.Second {
		t.Fatalf("HeartbeatTimeout: got %v, want 45s", resolved.HeartbeatTimeout)
	}
	if resolved.PollInterval != 10*time.Second {
		t.Fatalf("PollInterval: got %v, want 10s", resolved.PollInterval)
	}
}

func TestApplyDirective(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	tests := []struct {
		dir  protocol.Directive
		want State
	}{
		{protocol.DirectiveStart, StateRunning},
		{protocol.DirectivePause, StatePaused},
		{protocol.DirectiveStop, StateStopping},
		{protocol.DirectiveFocus, StateRunning},
	}

	for _, tt := range tests {
		d.applyDirective(tt.dir)
		if d.GetState() != tt.want {
			t.Fatalf("after %s: got %s, want %s", tt.dir, d.GetState(), tt.want)
		}
	}
}

func TestState_Constants(t *testing.T) {
	// Verify state string values for clarity
	if StateInert != "inert" {
		t.Fatalf("StateInert: %s", StateInert)
	}
	if StateRunning != "running" {
		t.Fatalf("StateRunning: %s", StateRunning)
	}
	if StatePaused != "paused" {
		t.Fatalf("StatePaused: %s", StatePaused)
	}
	if StateStopping != "stopping" {
		t.Fatalf("StateStopping: %s", StateStopping)
	}
}

// --- New coverage tests ---

func TestHandleStatus_LogsEvent(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-status", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Send STATUS message
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			WorkerID: "w-status",
			BeadID:   "bead-s1",
			State:    "coding",
			Result:   "in progress",
		},
	})

	// Wait for status event
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "status") > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected 'status' event logged")
}

func TestHandleStatus_NilPayload(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	// Call handleStatus with nil Status — should return early without panic
	d.handleStatus(ctx, "w1", protocol.Message{Type: protocol.MsgStatus, Status: nil})
	// No event should be logged
	if eventCount(t, d.db, "status") != 0 {
		t.Fatal("expected no status event for nil payload")
	}
}

func TestExtractWorkerID_AllBranches(t *testing.T) {
	tests := []struct {
		name string
		msg  protocol.Message
		want string
	}{
		{
			name: "status",
			msg:  protocol.Message{Status: &protocol.StatusPayload{WorkerID: "ws"}},
			want: "ws",
		},
		{
			name: "handoff",
			msg:  protocol.Message{Handoff: &protocol.HandoffPayload{WorkerID: "wh"}},
			want: "wh",
		},
		{
			name: "ready_for_review",
			msg:  protocol.Message{ReadyForReview: &protocol.ReadyForReviewPayload{WorkerID: "wr"}},
			want: "wr",
		},
		{
			name: "all_nil",
			msg:  protocol.Message{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWorkerID(tt.msg)
			if got != tt.want {
				t.Fatalf("extractWorkerID(%s): got %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestRegisterWorker_NewAndReRegister(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Create a pipe to simulate a connection
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

	// Register new worker
	d.registerWorker("w-new", server)
	if d.ConnectedWorkers() != 1 {
		t.Fatalf("expected 1 worker, got %d", d.ConnectedWorkers())
	}
	st, _, ok := d.WorkerInfo("w-new")
	if !ok {
		t.Fatal("expected worker to be tracked")
	}
	if st != WorkerIdle {
		t.Fatalf("expected idle, got %s", st)
	}

	// Re-register same worker with a new connection (simulates reconnect)
	server2, client2 := net.Pipe()
	t.Cleanup(func() { _ = server2.Close(); _ = client2.Close() })

	d.registerWorker("w-new", server2)
	if d.ConnectedWorkers() != 1 {
		t.Fatalf("expected still 1 worker after re-register, got %d", d.ConnectedWorkers())
	}
}

func TestProcessCommands_FocusAndPause(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Insert focus command
	insertCommand(t, d.db, "focus")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Verify directive event logged
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "directive") > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if eventCount(t, d.db, "directive") == 0 {
		t.Fatal("expected 'directive' event for focus")
	}

	// Insert an invalid directive — should be skipped (markCommandProcessed still called)
	_, err := d.db.Exec(`INSERT INTO commands (directive, args, status) VALUES ('bogus', '', 'pending')`)
	if err != nil {
		t.Fatalf("insert bogus command: %v", err)
	}

	// Wait for it to be processed
	time.Sleep(200 * time.Millisecond)

	// The bogus command should be marked processed (not pending)
	var status string
	err = d.db.QueryRow(`SELECT status FROM commands WHERE directive='bogus'`).Scan(&status)
	if err != nil {
		t.Fatalf("query bogus command: %v", err)
	}
	if status != "processed" {
		t.Fatalf("expected bogus command processed, got %s", status)
	}
}

func TestSQLiteHelpers_ClosedDB(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Close the DB to force errors
	_ = d.db.Close()

	// logEvent should return error
	err := d.logEvent(ctx, "test", "test", "", "", "")
	if err == nil {
		t.Fatal("expected error from logEvent on closed db")
	}

	// logEventLocked should return error
	err = d.logEventLocked(ctx, "test", "test", "", "", "")
	if err == nil {
		t.Fatal("expected error from logEventLocked on closed db")
	}

	// createAssignment should return error
	err = d.createAssignment(ctx, "b1", "w1", "/tmp/wt")
	if err == nil {
		t.Fatal("expected error from createAssignment on closed db")
	}

	// completeAssignment should return error
	err = d.completeAssignment(ctx, "b1")
	if err == nil {
		t.Fatal("expected error from completeAssignment on closed db")
	}

	// pendingCommands should return error
	_, err = d.pendingCommands(ctx)
	if err == nil {
		t.Fatal("expected error from pendingCommands on closed db")
	}

	// markCommandProcessed should return error
	err = d.markCommandProcessed(ctx, 1)
	if err == nil {
		t.Fatal("expected error from markCommandProcessed on closed db")
	}
}

func TestSendToWorker_BrokenConn(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Create a pipe and close the read end to simulate broken connection
	server, client := net.Pipe()
	_ = client.Close() // close the reader — writes to server will fail

	w := &trackedWorker{
		id:      "w-broken",
		conn:    server,
		state:   WorkerIdle,
		encoder: json.NewEncoder(server),
	}

	err := d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
	if err == nil {
		t.Fatal("expected error writing to broken connection")
	}
	_ = server.Close()
}

func TestHandleReconnect_IdleState(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)

	// Send RECONNECT with idle state (not "running")
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID:   "w-idle-reconnect",
			BeadID:     "bead-idle",
			State:      "idle",
			ContextPct: 15,
		},
	})

	// Should be tracked as idle
	waitForWorkerState(t, d, "w-idle-reconnect", WorkerIdle, 1*time.Second)
}

func TestHandleReconnect_WithBufferedEvents(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)

	// Send RECONNECT with a buffered heartbeat event
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID:   "w-buffered",
			BeadID:     "bead-buf",
			State:      "running",
			ContextPct: 20,
			BufferedEvents: []protocol.Message{
				{
					Type:      protocol.MsgHeartbeat,
					Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-buffered", BeadID: "bead-buf", ContextPct: 25},
				},
			},
		},
	})

	waitForWorkerState(t, d, "w-buffered", WorkerBusy, 1*time.Second)

	// The buffered heartbeat should have been processed — check event
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "heartbeat") > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected buffered heartbeat event to be processed")
}

func TestHandleReconnect_NilPayload(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	// Should not panic
	d.handleReconnect(ctx, "w1", protocol.Message{Type: protocol.MsgReconnect, Reconnect: nil})
	if eventCount(t, d.db, "reconnect") != 0 {
		t.Fatal("expected no reconnect event for nil payload")
	}
}

func TestHandleReviewResult_ContextCancelled(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan ops.Result, 1)

	// Cancel before sending result
	cancel()

	// Should return without blocking
	d.handleReviewResult(ctx, "w1", "b1", resultCh)
	// No panic, no events
}

func TestHandleReviewResult_UnknownVerdict(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-unk", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	ctx := context.Background()
	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{Verdict: "UNKNOWN_VERDICT", Feedback: "something weird"}

	d.handleReviewResult(ctx, "w-unk", "bead-unk", resultCh)

	// Should log review_failed and escalate
	if eventCount(t, d.db, "review_failed") == 0 {
		t.Fatal("expected 'review_failed' event for unknown verdict")
	}
}

func TestHandleHeartbeat_NilPayload(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	d.handleHeartbeat(ctx, "w1", protocol.Message{Type: protocol.MsgHeartbeat, Heartbeat: nil})
	if eventCount(t, d.db, "heartbeat") != 0 {
		t.Fatal("expected no heartbeat event for nil payload")
	}
}

func TestHandleDone_NilPayload(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	d.handleDone(ctx, "w1", protocol.Message{Type: protocol.MsgDone, Done: nil})
	if eventCount(t, d.db, "done") != 0 {
		t.Fatal("expected no done event for nil payload")
	}
}

func TestHandleHandoff_NilPayload(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	d.handleHandoff(ctx, "w1", protocol.Message{Type: protocol.MsgHandoff, Handoff: nil})
	if eventCount(t, d.db, "handoff") != 0 {
		t.Fatal("expected no handoff event for nil payload")
	}
}

func TestHandleReadyForReview_NilPayload(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	d.handleReadyForReview(ctx, "w1", protocol.Message{Type: protocol.MsgReadyForReview, ReadyForReview: nil})
	if eventCount(t, d.db, "ready_for_review") != 0 {
		t.Fatal("expected no ready_for_review event for nil payload")
	}
}

func TestProcessCommands_ClosedDB(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Close DB to force pendingCommands error
	_ = d.db.Close()

	// Should not panic — just returns early
	d.processCommands(ctx)
}

func TestHandleDone_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Send done for a worker that does not exist in the map
	d.handleDone(ctx, "w-ghost", protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: "bead-ghost", WorkerID: "w-ghost"},
	})

	// Event logged but no merge triggered (no worktree)
	if eventCount(t, d.db, "done") == 0 {
		t.Fatal("expected 'done' event even for unknown worker")
	}
	// No merge event since worker had no worktree
	if eventCount(t, d.db, "merged") != 0 {
		t.Fatal("expected no 'merged' event for unknown worker")
	}
}

func TestHandleHandoff_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	d.handleHandoff(ctx, "w-ghost", protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{BeadID: "bead-ghost", WorkerID: "w-ghost"},
	})

	if eventCount(t, d.db, "handoff") == 0 {
		t.Fatal("expected 'handoff' event even for unknown worker")
	}
}

func TestHandleReadyForReview_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	d.handleReadyForReview(ctx, "w-ghost", protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-ghost", WorkerID: "w-ghost"},
	})

	if eventCount(t, d.db, "ready_for_review") == 0 {
		t.Fatal("expected 'ready_for_review' event even for unknown worker")
	}
}

func TestHandleReconnect_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Reconnect for a worker not yet registered — registerWorker happens before handleMessage
	// in handleConn, but we can call handleReconnect directly for a worker that is not in the map
	d.handleReconnect(ctx, "w-ghost", protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: "w-ghost",
			BeadID:   "bead-ghost",
			State:    "running",
		},
	})

	// Event should be logged even if worker not tracked
	if eventCount(t, d.db, "reconnect") == 0 {
		t.Fatal("expected 'reconnect' event")
	}
}

// Verify errors.As works with ConflictError (integration sanity check).
func TestConflictError_ErrorsAs(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &merge.ConflictError{Files: []string{"a.go"}, BeadID: "b1"})
	var ce *merge.ConflictError
	if !errors.As(err, &ce) {
		t.Fatal("errors.As should match ConflictError")
	}
	if ce.BeadID != "b1" {
		t.Fatalf("BeadID: got %s, want b1", ce.BeadID)
	}
}
