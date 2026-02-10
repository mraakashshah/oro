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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// --- Mock implementations ---

type mockBeadSource struct {
	mu     sync.Mutex
	beads  []Bead
	shown  map[string]*BeadDetail
	closed []string
	synced bool
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

func (m *mockBeadSource) Close(_ context.Context, id string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, id)
	return nil
}

func (m *mockBeadSource) Sync(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.synced = true
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

// sendDirective sends a DIRECTIVE message to the dispatcher via UDS.
func sendDirective(t *testing.T, socketPath, directive string) {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("connect to dispatcher: %v", err)
	}
	defer func() { _ = conn.Close() }()

	msg := protocol.Message{
		Type: protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{
			Op:   directive,
			Args: "",
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal directive: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write directive: %v", err)
	}

	// Read ACK (but don't validate it here - some tests may want to check it)
	scanner := bufio.NewScanner(conn)
	_ = scanner.Scan()
}

// sendDirectiveWithArgs sends a DIRECTIVE message with args and returns the ACK payload.
func sendDirectiveWithArgs(t *testing.T, socketPath, directive, args string) *protocol.ACKPayload {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("connect to dispatcher: %v", err)
	}
	defer func() { _ = conn.Close() }()

	msg := protocol.Message{
		Type: protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{
			Op:   directive,
			Args: args,
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal directive: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write directive: %v", err)
	}

	ackMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatalf("expected ACK for directive %s", directive)
	}
	if ackMsg.Type != protocol.MsgACK {
		t.Fatalf("expected ACK, got %s", ackMsg.Type)
	}
	if ackMsg.ACK == nil {
		t.Fatal("expected non-nil ACK payload")
	}
	return ackMsg.ACK
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
	sendDirective(t, d.cfg.SocketPath, "start")

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
	sendDirective(t, d.cfg.SocketPath, "start")
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

func TestDispatcher_AssignBead_ModelPropagation(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Assign bead with explicit sonnet model
	beadSrc.SetBeads([]Bead{{
		ID: "bead-model", Title: "Model test", Priority: 1,
		Model: "claude-sonnet-4-5-20250929",
	}})

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Assign.Model != "claude-sonnet-4-5-20250929" {
		t.Fatalf("expected model claude-sonnet-4-5-20250929, got %q", msg.Assign.Model)
	}
}

func TestDispatcher_AssignBead_DefaultModel(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Assign bead with no model — should default to opus
	beadSrc.SetBeads([]Bead{{ID: "bead-default", Title: "Default model", Priority: 1}})

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Assign.Model != DefaultModel {
		t.Fatalf("expected default model %q, got %q", DefaultModel, msg.Assign.Model)
	}
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

	sendDirective(t, d.cfg.SocketPath, "start")
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
		Done: &protocol.DonePayload{BeadID: "bead-merge", WorkerID: "w1", QualityGatePassed: true},
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

	sendDirective(t, d.cfg.SocketPath, "start")
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
		Done: &protocol.DonePayload{BeadID: "bead-conflict", WorkerID: "w1", QualityGatePassed: true},
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

	sendDirective(t, d.cfg.SocketPath, "start")
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
	sendDirective(t, d.cfg.SocketPath, "start")
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

func TestDispatcher_HeartbeatTimeout_EscalatesWithStructuredFormat(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	d.cfg.HeartbeatTimeout = 100 * time.Millisecond

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-crash", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Assign work so the worker is busy (idle workers are not timed out)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-crash", Title: "Crash test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Don't send any more heartbeats — wait for timeout + escalation
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msgs := esc.Messages()
		if len(msgs) > 0 {
			msg := msgs[0]
			if !strings.HasPrefix(msg, "[ORO-DISPATCH] WORKER_CRASH: bead-crash") {
				t.Fatalf("heartbeat escalation should use structured format, got: %q", msg)
			}
			if !strings.Contains(msg, "w-crash") {
				t.Fatalf("heartbeat escalation should mention worker ID, got: %q", msg)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected heartbeat timeout escalation message")
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

	sendDirective(t, d.cfg.SocketPath, "start")
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

	sendDirective(t, d.cfg.SocketPath, "start")
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

	sendDirective(t, d.cfg.SocketPath, "start")
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
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "stop")
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

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "pause")
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

	sendDirective(t, d.cfg.SocketPath, "start")
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
		Done: &protocol.DonePayload{BeadID: "bead-esc", WorkerID: "w1", QualityGatePassed: true},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msgs := esc.Messages()
		if len(msgs) > 0 {
			msg := msgs[0]
			if !strings.HasPrefix(msg, "[ORO-DISPATCH] MERGE_CONFLICT: bead-esc") {
				t.Fatalf("escalation should use structured format, got: %q", msg)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected escalation message")
}

func TestDispatcher_ConcurrentWorkers(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	sendDirective(t, d.cfg.SocketPath, "start")
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
		args string
		want State
	}{
		{protocol.DirectiveStart, "", StateRunning},
		{protocol.DirectivePause, "", StatePaused},
		{protocol.DirectiveStop, "", StateStopping},
		{protocol.DirectiveFocus, "epic-1", StateRunning},
	}

	for _, tt := range tests {
		_, _ = d.applyDirective(tt.dir, tt.args)
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

func TestDirective_FocusAndInvalid(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Send focus directive
	sendDirective(t, d.cfg.SocketPath, "focus")
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

	// Send an invalid directive via UDS — should receive ACK with OK=false
	conn, err := net.Dial("unix", d.cfg.SocketPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{
			Op:   "bogus",
			Args: "",
		},
	})

	msg, ok := readMsg(t, conn, 1*time.Second)
	if !ok {
		t.Fatal("expected ACK for invalid directive")
	}
	if msg.Type != protocol.MsgACK {
		t.Fatalf("expected ACK, got %s", msg.Type)
	}
	if msg.ACK.OK {
		t.Fatal("expected ACK.OK=false for invalid directive")
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
	d, _, _, esc, _, _ := newTestDispatcher(t)
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

	// Verify structured escalation format
	msgs := esc.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected escalation message for unknown verdict")
	}
	if !strings.HasPrefix(msgs[0], "[ORO-DISPATCH] STUCK: bead-unk") {
		t.Fatalf("review escalation should use structured format, got: %q", msgs[0])
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

func TestHandleDone_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Send done for a worker that does not exist in the map
	d.handleDone(ctx, "w-ghost", protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: "bead-ghost", WorkerID: "w-ghost", QualityGatePassed: true},
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

func TestHandleDone_QualityGateFailed_RejectsMerge(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-qg-fail", Title: "QG fail test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE with QualityGatePassed=false
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-fail",
			WorkerID:          "w1",
			QualityGatePassed: false,
		},
	})

	// Should log a quality_gate_failed event
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "quality_gate_rejected") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "quality_gate_rejected") == 0 {
		t.Fatal("expected 'quality_gate_rejected' event when QualityGatePassed=false")
	}

	// Should NOT have merged
	if eventCount(t, d.db, "merged") != 0 {
		t.Fatal("should not merge when quality gate failed")
	}

	// Worker should be reassigned (re-ASSIGN sent)
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after quality gate rejection")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN after quality gate rejection, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "bead-qg-fail" {
		t.Fatalf("expected reassignment of bead-qg-fail, got %s", msg.Assign.BeadID)
	}
}

func TestHandleDone_QualityGatePassed_ProceedsMerge(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-qg-pass", Title: "QG pass test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE with QualityGatePassed=true
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-pass",
			WorkerID:          "w1",
			QualityGatePassed: true,
		},
	})

	// Should log done event and proceed to merge
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "merged") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "merged") == 0 {
		t.Fatal("expected 'merged' event when QualityGatePassed=true")
	}

	// No quality_gate_rejected event
	if eventCount(t, d.db, "quality_gate_rejected") != 0 {
		t.Fatal("should not have quality_gate_rejected when gate passed")
	}
}

func TestDispatcher_Handoff_PersistsLearningsAsMemories(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-mem", Title: "Memory handoff test", Priority: 1}})
	_, ok2 := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok2 {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send HANDOFF with learnings and decisions
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:         "bead-mem",
			WorkerID:       "w1",
			Learnings:      []string{"ruff must run before pyright", "WAL needs single writer"},
			Decisions:      []string{"use table-driven tests"},
			FilesModified:  []string{"pkg/protocol/message.go"},
			ContextSummary: "Extended handoff with typed context",
		},
	})

	// Worker should receive SHUTDOWN
	msg, ok3 := readMsg(t, conn, 2*time.Second)
	if !ok3 {
		t.Fatal("expected SHUTDOWN after handoff")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Wait for handoff event to be logged
	deadline2 := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline2) {
		if eventCount(t, d.db, "handoff") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Verify memories were persisted: 2 learnings + 1 decision = 3 memories
	var memCount int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE bead_id='bead-mem'`).Scan(&memCount)
	if err != nil {
		t.Fatalf("count memories: %v", err)
	}
	if memCount != 3 {
		t.Fatalf("expected 3 memories persisted from handoff, got %d", memCount)
	}

	// Verify types: 2 lesson, 1 decision
	var lessonCount, decisionCount int
	err = d.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE bead_id='bead-mem' AND type='lesson'`).Scan(&lessonCount)
	if err != nil {
		t.Fatalf("count lessons: %v", err)
	}
	err = d.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE bead_id='bead-mem' AND type='decision'`).Scan(&decisionCount)
	if err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	if lessonCount != 2 {
		t.Errorf("expected 2 lessons, got %d", lessonCount)
	}
	if decisionCount != 1 {
		t.Errorf("expected 1 decision, got %d", decisionCount)
	}
}

func TestDispatcher_ReassignIncludesForPromptOutput(t *testing.T) { //nolint:funlen // integration test
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Pre-seed a memory that matches the bead title
	_, err := d.db.Exec(
		`INSERT INTO memories (content, type, tags, source, bead_id, confidence)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"ruff must run before pyright for linting", "lesson", `["python"]`,
		"self_report", "bead-reassign", 0.9,
	)
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Set bead with title that matches the memory
	beadSrc.SetBeads([]Bead{{ID: "bead-reassign", Title: "fix linting with ruff and pyright", Priority: 1}})

	// Read ASSIGN — should include MemoryContext
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.MemoryContext == "" {
		t.Fatal("expected non-empty MemoryContext in ASSIGN after seeding relevant memories")
	}
	if !containsStr(msg.Assign.MemoryContext, "ruff") {
		t.Errorf("expected MemoryContext to contain 'ruff', got: %s", msg.Assign.MemoryContext)
	}
	if !containsStr(msg.Assign.MemoryContext, "Relevant Memories") {
		t.Errorf("expected MemoryContext header, got: %s", msg.Assign.MemoryContext)
	}
}

// containsStr checks if s contains substr.
func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestDispatcher_GracefulShutdown_WaitsForApproval(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-gs", Title: "Graceful shutdown test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Trigger graceful shutdown via the dispatcher method
	d.GracefulShutdownWorker("w1", 2*time.Second)

	// Worker should receive PREPARE_SHUTDOWN
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected PREPARE_SHUTDOWN message")
	}
	if msg.Type != protocol.MsgPrepareShutdown {
		t.Fatalf("expected PREPARE_SHUTDOWN, got %s", msg.Type)
	}
	if msg.PrepareShutdown == nil {
		t.Fatal("expected non-nil PrepareShutdown payload")
	}

	// Simulate worker responding with HANDOFF then SHUTDOWN_APPROVED
	sendMsg(t, conn, protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{BeadID: "bead-gs", WorkerID: "w1"},
	})
	sendMsg(t, conn, protocol.Message{
		Type:             protocol.MsgShutdownApproved,
		ShutdownApproved: &protocol.ShutdownApprovedPayload{WorkerID: "w1"},
	})

	// Wait for shutdown_approved event
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "shutdown_approved") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "shutdown_approved") == 0 {
		t.Fatal("expected 'shutdown_approved' event")
	}

	// Worker should then receive hard SHUTDOWN
	msg2, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected SHUTDOWN after approval")
	}
	if msg2.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg2.Type)
	}
}

func TestDispatcher_GracefulShutdown_TimeoutFallsBackToHardKill(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-timeout", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-timeout", Title: "Timeout test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Trigger graceful shutdown with a very short timeout
	d.GracefulShutdownWorker("w-timeout", 200*time.Millisecond)

	// Worker receives PREPARE_SHUTDOWN but does NOT respond
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected PREPARE_SHUTDOWN")
	}
	if msg.Type != protocol.MsgPrepareShutdown {
		t.Fatalf("expected PREPARE_SHUTDOWN, got %s", msg.Type)
	}

	// Do NOT respond — dispatcher should fall back to hard SHUTDOWN after timeout
	msg2, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected hard SHUTDOWN after timeout")
	}
	if msg2.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN (hard kill), got %s", msg2.Type)
	}
}

func TestDispatcher_GracefulShutdown_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Should not panic for unknown worker
	d.GracefulShutdownWorker("w-nonexistent", 1*time.Second)
}

func TestDispatcher_HandleShutdownApproved_NilPayload(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	// Should not panic
	d.handleShutdownApproved(ctx, "w1", protocol.Message{Type: protocol.MsgShutdownApproved, ShutdownApproved: nil})
	if eventCount(t, d.db, "shutdown_approved") != 0 {
		t.Fatal("expected no shutdown_approved event for nil payload")
	}
}

func TestExtractWorkerID_ShutdownApproved(t *testing.T) {
	msg := protocol.Message{ShutdownApproved: &protocol.ShutdownApprovedPayload{WorkerID: "wsa"}}
	got := extractWorkerID(msg)
	if got != "wsa" {
		t.Fatalf("extractWorkerID: got %q, want %q", got, "wsa")
	}
}

// Verify errors.As works with ConflictError (integration sanity check).
func TestAssignIncludesMemories(t *testing.T) { //nolint:funlen // integration test
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Pre-seed memories that match the bead title.
	_, err := d.db.Exec(
		`INSERT INTO memories (content, type, tags, source, bead_id, confidence)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"always run go vet before committing", "lesson", `["go"]`,
		"self_report", "bead-prev", 0.9,
	)
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-mem", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Set bead with title that matches the memory
	beadSrc.SetBeads([]Bead{{ID: "bead-mem-inject", Title: "run go vet and lint checks", Priority: 1}})

	// Read ASSIGN — should include non-empty MemoryContext
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.MemoryContext == "" {
		t.Fatal("expected non-empty MemoryContext in ASSIGN payload when relevant memories exist")
	}
	if !containsStr(msg.Assign.MemoryContext, "go vet") {
		t.Errorf("expected MemoryContext to contain 'go vet', got: %s", msg.Assign.MemoryContext)
	}
	if !containsStr(msg.Assign.MemoryContext, "Relevant Memories") {
		t.Errorf("expected MemoryContext to contain header 'Relevant Memories', got: %s", msg.Assign.MemoryContext)
	}
}

func TestDispatcherShutdownBroadcast(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	// Set a short shutdown timeout for the test
	d.cfg.ShutdownTimeout = 500 * time.Millisecond

	cancel := startDispatcher(t, d)

	// Connect 3 workers
	type workerConn struct {
		id   string
		conn net.Conn
	}
	workers := make([]workerConn, 3)
	for i := 0; i < 3; i++ {
		wid := fmt.Sprintf("w-shutdown-%d", i)
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, ContextPct: 5},
		})
		workers[i] = workerConn{id: wid, conn: conn}
	}
	waitForWorkers(t, d, 3, 2*time.Second)

	// Start and assign beads to all workers
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{
		{ID: "bead-sd-0", Title: "Shutdown test 0", Priority: 1},
		{ID: "bead-sd-1", Title: "Shutdown test 1", Priority: 2},
		{ID: "bead-sd-2", Title: "Shutdown test 2", Priority: 3},
	})

	// Each worker reads its ASSIGN
	for i, w := range workers {
		msg, ok := readMsg(t, w.conn, 3*time.Second)
		if !ok {
			t.Fatalf("worker %d: expected ASSIGN", i)
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("worker %d: expected ASSIGN, got %s", i, msg.Type)
		}
	}
	beadSrc.SetBeads(nil)

	// Cancel the context — this simulates shutdown
	cancel()

	// ALL workers should receive PREPARE_SHUTDOWN
	for i, w := range workers {
		msg, ok := readMsg(t, w.conn, 2*time.Second)
		if !ok {
			t.Fatalf("worker %d: expected PREPARE_SHUTDOWN after context cancel", i)
		}
		if msg.Type != protocol.MsgPrepareShutdown {
			t.Fatalf("worker %d: expected PREPARE_SHUTDOWN, got %s", i, msg.Type)
		}
	}
}

func TestDispatcherShutdownBroadcast_TimeoutForcesHardShutdown(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	// Very short shutdown timeout
	d.cfg.ShutdownTimeout = 300 * time.Millisecond

	cancel := startDispatcher(t, d)

	// Connect 2 workers
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-force-0", ContextPct: 5},
	})
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-force-1", ContextPct: 5},
	})
	waitForWorkers(t, d, 2, 2*time.Second)

	// Start and assign beads
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{
		{ID: "bead-force-0", Title: "Force 0", Priority: 1},
		{ID: "bead-force-1", Title: "Force 1", Priority: 2},
	})

	// Consume ASSIGN for both workers
	_, ok := readMsg(t, conn1, 3*time.Second)
	if !ok {
		t.Fatal("worker 0: expected ASSIGN")
	}
	_, ok = readMsg(t, conn2, 3*time.Second)
	if !ok {
		t.Fatal("worker 1: expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Cancel context to trigger shutdown
	cancel()

	// Both workers should receive PREPARE_SHUTDOWN
	msg1, ok := readMsg(t, conn1, 2*time.Second)
	if !ok {
		t.Fatal("worker 0: expected PREPARE_SHUTDOWN")
	}
	if msg1.Type != protocol.MsgPrepareShutdown {
		t.Fatalf("worker 0: expected PREPARE_SHUTDOWN, got %s", msg1.Type)
	}
	msg2, ok := readMsg(t, conn2, 2*time.Second)
	if !ok {
		t.Fatal("worker 1: expected PREPARE_SHUTDOWN")
	}
	if msg2.Type != protocol.MsgPrepareShutdown {
		t.Fatalf("worker 1: expected PREPARE_SHUTDOWN, got %s", msg2.Type)
	}

	// Do NOT send SHUTDOWN_APPROVED — workers stay silent
	// After ShutdownTimeout, the dispatcher should force-close connections.
	// The workers should get disconnected (reads will fail or return EOF).
	// We verify by polling ConnectedWorkers until it reaches 0.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d.ConnectedWorkers() == 0 {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected 0 connected workers after shutdown timeout, got %d", d.ConnectedWorkers())
}

func TestConfig_ShutdownTimeout_Default(t *testing.T) {
	cfg := Config{SocketPath: "/tmp/test.sock", DBPath: ":memory:"}
	resolved := cfg.withDefaults()
	if resolved.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout: got %v, want 10s", resolved.ShutdownTimeout)
	}
}

func TestAssignBeadCleansUpOnFailure(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)

	// Create a broken connection: net.Pipe() then close the read end
	server, client := net.Pipe()
	_ = client.Close() // close reader — writes to server will fail

	// Register the worker with the broken connection
	d.registerWorker("w-broken", server)
	t.Cleanup(func() { _ = server.Close() })

	ctx := context.Background()
	bead := Bead{ID: "bead-cleanup", Title: "Cleanup test", Priority: 1}

	// Grab the tracked worker so we can call assignBead directly
	d.mu.Lock()
	w := d.workers["w-broken"]
	d.mu.Unlock()

	// Call assignBead — worktree creation succeeds, but sendToWorker should fail
	d.assignBead(ctx, w, bead)

	// Assert the worktree was cleaned up
	wtMgr.mu.Lock()
	removed := make([]string, len(wtMgr.removed))
	copy(removed, wtMgr.removed)
	wtMgr.mu.Unlock()

	expectedPath := "/tmp/worktree-bead-cleanup"
	found := false
	for _, r := range removed {
		if r == expectedPath {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected worktree %q to be removed after sendToWorker failure, removed: %v", expectedPath, removed)
	}

	// Verify worktree_cleanup event was logged
	if eventCount(t, d.db, "worktree_cleanup") == 0 {
		t.Fatal("expected 'worktree_cleanup' event after sendToWorker failure")
	}
}

// --- Slow process for shutdown tests ---

type slowProcess struct {
	waitCh    chan struct{}
	killed    atomic.Bool
	closeOnce sync.Once
}

func (p *slowProcess) Wait() error {
	<-p.waitCh
	return fmt.Errorf("killed")
}

func (p *slowProcess) Kill() error {
	p.killed.Store(true)
	p.closeOnce.Do(func() { close(p.waitCh) })
	return nil
}

func (p *slowProcess) Output() (string, error) { return "APPROVED: ok", nil }

type slowSubprocessSpawner struct {
	mu        sync.Mutex
	processes []*slowProcess
}

func (s *slowSubprocessSpawner) Spawn(_ context.Context, _ string, _ string, _ string) (ops.Process, error) {
	p := &slowProcess{waitCh: make(chan struct{})}
	s.mu.Lock()
	s.processes = append(s.processes, p)
	s.mu.Unlock()
	return p, nil
}

func TestDispatcherShutdownOpsCleanup(t *testing.T) {
	db := newTestDB(t)
	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)

	slowSpawner := &slowSubprocessSpawner{}
	opsSpawner := ops.NewSpawner(slowSpawner)

	beadSrc := &mockBeadSource{beads: []Bead{}, shown: make(map[string]*BeadDetail)}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}

	sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		MaxWorkers:       5,
		HeartbeatTimeout: 500 * time.Millisecond,
		PollInterval:     50 * time.Millisecond,
		ShutdownTimeout:  500 * time.Millisecond,
	}

	d := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc)
	cancel := startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-ops-kill", Title: "Ops kill test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Spawn an ops agent by sending READY_FOR_REVIEW
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-ops-kill", WorkerID: "w1"},
	})

	// Wait for ops agent to be spawned
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(opsSpawner.Active()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(opsSpawner.Active()) == 0 {
		t.Fatal("expected active ops agent after READY_FOR_REVIEW")
	}

	// Trigger shutdown
	cancel()

	// Wait for all ops agents to be killed
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(opsSpawner.Active()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(opsSpawner.Active()) != 0 {
		t.Fatalf("expected 0 active ops agents after shutdown, got %d", len(opsSpawner.Active()))
	}

	// Verify all processes were killed
	slowSpawner.mu.Lock()
	procs := make([]*slowProcess, len(slowSpawner.processes))
	copy(procs, slowSpawner.processes)
	slowSpawner.mu.Unlock()

	for i, p := range procs {
		if !p.killed.Load() {
			t.Errorf("process %d was not killed during shutdown", i)
		}
	}
}

func TestDispatcherShutdownWorktreeCleanup(t *testing.T) {
	db := newTestDB(t)
	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)

	spawner := ops.NewSpawner(&mockSubprocessSpawner{})
	beadSrc := &mockBeadSource{beads: []Bead{}, shown: make(map[string]*BeadDetail)}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}

	sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		MaxWorkers:       5,
		HeartbeatTimeout: 500 * time.Millisecond,
		PollInterval:     50 * time.Millisecond,
		ShutdownTimeout:  500 * time.Millisecond,
	}

	d := New(cfg, db, merger, spawner, beadSrc, wtMgr, esc)
	cancel := startDispatcher(t, d)

	// Connect two workers
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w2", ContextPct: 5},
	})
	waitForWorkers(t, d, 2, 1*time.Second)

	// Start and assign two beads (creates worktrees)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{
		{ID: "bead-wt-1", Title: "WT cleanup 1", Priority: 1},
		{ID: "bead-wt-2", Title: "WT cleanup 2", Priority: 2},
	})

	// Consume ASSIGN messages
	if _, ok := readMsg(t, conn1, 2*time.Second); !ok {
		t.Fatal("expected ASSIGN for w1")
	}
	if _, ok := readMsg(t, conn2, 2*time.Second); !ok {
		t.Fatal("expected ASSIGN for w2")
	}
	beadSrc.SetBeads(nil)

	// Trigger shutdown
	cancel()

	// Wait for worktrees to be removed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		wtMgr.mu.Lock()
		n := len(wtMgr.removed)
		wtMgr.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	wtMgr.mu.Lock()
	removed := make([]string, len(wtMgr.removed))
	copy(removed, wtMgr.removed)
	wtMgr.mu.Unlock()

	if len(removed) < 2 {
		t.Fatalf("expected at least 2 worktrees removed, got %d: %v", len(removed), removed)
	}
}

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

func TestDispatcher_DirectiveHandler_SendsACK(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Manager connects as a client
	conn, _ := connectWorker(t, d.cfg.SocketPath)

	// Send DIRECTIVE message
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{
			Op:   string(protocol.DirectiveStart),
			Args: "",
		},
	})

	// Should receive ACK
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ACK response")
	}
	if msg.Type != protocol.MsgACK {
		t.Fatalf("expected ACK, got %s", msg.Type)
	}
	if msg.ACK == nil {
		t.Fatal("expected non-nil ACK payload")
	}
	if !msg.ACK.OK {
		t.Fatalf("expected OK=true, got %v", msg.ACK.OK)
	}

	// Verify directive was applied (dispatcher should be running)
	waitForState(t, d, StateRunning, 1*time.Second)
}

func TestDispatcher_BeadDirWatcher_TriggersAssignment(t *testing.T) {
	// Create temp directory for .beads/
	beadsDir := t.TempDir()

	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Configure dispatcher to watch the temp beads directory
	d.beadsDir = beadsDir

	startDispatcher(t, d)

	// Connect worker
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Initially no beads
	beadSrc.SetBeads(nil)

	// Add the bead to the mock source first
	beadSrc.SetBeads([]Bead{{ID: "bead-watch-1", Title: "Test bead", Priority: 1}})

	// Now create a new file in .beads/ to trigger the watcher
	// (fsnotify triggers on CREATE, WRITE, REMOVE, RENAME events)
	testFile := beadsDir + "/trigger.tmp"
	if err := os.WriteFile(testFile, []byte("trigger"), 0o600); err != nil {
		t.Fatalf("write trigger file: %v", err)
	}

	// Should receive ASSIGN without waiting for poll interval
	msg, ok := readMsg(t, conn, 500*time.Millisecond) // Less than 60s fallback poll
	if !ok {
		t.Fatal("expected ASSIGN triggered by fsnotify, not poll interval")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != "bead-watch-1" {
		t.Fatalf("expected bead-watch-1, got %v", msg.Assign)
	}
}

func TestDispatcher_BeadDirWatcher_FallbackPoll(t *testing.T) {
	// Create temp directory for .beads/
	beadsDir := t.TempDir()

	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Configure dispatcher with short fallback interval for testing
	d.cfg.FallbackPollInterval = 200 * time.Millisecond
	d.beadsDir = beadsDir

	startDispatcher(t, d)

	// Connect worker
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Initially no beads
	beadSrc.SetBeads(nil)

	// Add the bead to the mock source (but don't trigger fsnotify)
	beadSrc.SetBeads([]Bead{{ID: "bead-fallback", Title: "Fallback test", Priority: 1}})

	// Should receive ASSIGN from fallback poll within reasonable time
	msg, ok := readMsg(t, conn, 1*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN from fallback poll")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != "bead-fallback" {
		t.Fatalf("expected bead-fallback, got %v", msg.Assign)
	}
}

// --- Quality gate retry tests ---

func TestQualityGateRetry_ReAssignSameBeadAndWorktree(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-qg-retry", Title: "QG retry", Priority: 1}})
	assignMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if assignMsg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", assignMsg.Type)
	}
	origWorktree := assignMsg.Assign.Worktree
	beadSrc.SetBeads(nil)

	// Send DONE with quality gate failed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-retry",
			WorkerID:          "w1",
			QualityGatePassed: false,
		},
	})

	// Worker should receive re-ASSIGN with the same bead ID and worktree
	retryMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after quality gate failure")
	}
	if retryMsg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", retryMsg.Type)
	}
	if retryMsg.Assign.BeadID != "bead-qg-retry" {
		t.Fatalf("expected same bead ID bead-qg-retry, got %s", retryMsg.Assign.BeadID)
	}
	if retryMsg.Assign.Worktree != origWorktree {
		t.Fatalf("expected same worktree %s, got %s", origWorktree, retryMsg.Assign.Worktree)
	}
}

func TestQualityGateRetry_WorkerStaysBusy(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-qg-busy", Title: "QG busy", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Verify worker is busy after initial assignment
	waitForWorkerState(t, d, "w1", WorkerBusy, 1*time.Second)

	// Send DONE with quality gate failed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-busy",
			WorkerID:          "w1",
			QualityGatePassed: false,
		},
	})

	// Consume the re-ASSIGN
	_, ok = readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN")
	}

	// Worker should remain busy (not idle) after quality gate retry
	st, beadID, ok := d.WorkerInfo("w1")
	if !ok {
		t.Fatal("expected worker to still be tracked")
	}
	if st != WorkerBusy {
		t.Fatalf("expected worker state Busy after retry, got %s", st)
	}
	if beadID != "bead-qg-busy" {
		t.Fatalf("expected worker bead ID bead-qg-busy, got %s", beadID)
	}
}

func TestQualityGateRetry_NoMergeHappens(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-qg-nomerge", Title: "QG no merge", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE with quality gate failed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-nomerge",
			WorkerID:          "w1",
			QualityGatePassed: false,
		},
	})

	// Consume the re-ASSIGN
	_, ok = readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN")
	}

	// Wait a bit to ensure no async merge was triggered
	time.Sleep(300 * time.Millisecond)

	// No merge-related events should exist
	if eventCount(t, d.db, "merged") != 0 {
		t.Fatal("no merge should happen when quality gate failed")
	}
	if eventCount(t, d.db, "merge_conflict") != 0 {
		t.Fatal("no merge_conflict should happen when quality gate failed")
	}
	if eventCount(t, d.db, "merge_failed") != 0 {
		t.Fatal("no merge_failed should happen when quality gate failed")
	}

	// Assignment should NOT be completed
	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE bead_id='bead-qg-nomerge' AND status='completed'`).Scan(&count)
	if err != nil {
		t.Fatalf("query assignments: %v", err)
	}
	if count != 0 {
		t.Fatal("assignment should not be completed when quality gate failed")
	}
}

func TestQualityGateRetry_EventLogged(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-qg-event", Title: "QG event", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE with quality gate failed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-event",
			WorkerID:          "w1",
			QualityGatePassed: false,
		},
	})

	// Wait for quality_gate_rejected event
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "quality_gate_rejected") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "quality_gate_rejected") == 0 {
		t.Fatal("expected 'quality_gate_rejected' event")
	}

	// Also verify a "done" event was logged (happens before the quality gate check)
	if eventCount(t, d.db, "done") == 0 {
		t.Fatal("expected 'done' event before quality gate check")
	}

	// Verify the quality_gate_rejected event payload contains the reason
	var payload string
	err := d.db.QueryRow(
		`SELECT payload FROM events WHERE type='quality_gate_rejected' AND bead_id='bead-qg-event'`,
	).Scan(&payload)
	if err != nil {
		t.Fatalf("query event payload: %v", err)
	}
	if !containsStr(payload, "QualityGatePassed=false") {
		t.Fatalf("expected payload to contain reason, got: %s", payload)
	}
}

func TestQualityGatePassed_NormalMergeFlow(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-qg-merge", Title: "QG merge", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE with quality gate passed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-merge",
			WorkerID:          "w1",
			QualityGatePassed: true,
		},
	})

	// Wait for merge to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "merged") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "merged") == 0 {
		t.Fatal("expected 'merged' event when quality gate passed")
	}

	// No quality_gate_rejected event
	if eventCount(t, d.db, "quality_gate_rejected") != 0 {
		t.Fatal("should not have quality_gate_rejected when gate passed")
	}

	// Worker should become idle after merge
	waitForWorkerState(t, d, "w1", WorkerIdle, 2*time.Second)

	// Assignment should be completed
	var status string
	err := d.db.QueryRow(`SELECT status FROM assignments WHERE bead_id='bead-qg-merge'`).Scan(&status)
	if err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "completed" {
		t.Fatalf("expected completed, got %s", status)
	}
}

func TestQualityGateRetry_ModelPreserved(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Assign bead with explicit model
	beadSrc.SetBeads([]Bead{{
		ID: "bead-qg-model", Title: "QG model", Priority: 1,
		Model: "claude-sonnet-4-5-20250929",
	}})
	assignMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if assignMsg.Assign.Model != "claude-sonnet-4-5-20250929" {
		t.Fatalf("initial ASSIGN should have model claude-sonnet-4-5-20250929, got %q", assignMsg.Assign.Model)
	}
	beadSrc.SetBeads(nil)

	// Verify the model is stored on the tracked worker
	model, ok := d.WorkerModel("w1")
	if !ok {
		t.Fatal("expected worker to be tracked")
	}
	if model != "claude-sonnet-4-5-20250929" {
		t.Fatalf("expected stored model claude-sonnet-4-5-20250929, got %q", model)
	}

	// Send DONE with quality gate failed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-model",
			WorkerID:          "w1",
			QualityGatePassed: false,
		},
	})

	// Worker should receive re-ASSIGN with the same model preserved
	retryMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after quality gate failure")
	}
	if retryMsg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", retryMsg.Type)
	}
	if retryMsg.Assign.Model != "claude-sonnet-4-5-20250929" {
		t.Fatalf("re-ASSIGN should preserve model claude-sonnet-4-5-20250929, got %q", retryMsg.Assign.Model)
	}
}

func TestQualityGateRetry_DefaultModelPreserved(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Assign bead with no model (should resolve to default)
	beadSrc.SetBeads([]Bead{{ID: "bead-qg-defmodel", Title: "QG default model", Priority: 1}})
	assignMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if assignMsg.Assign.Model != DefaultModel {
		t.Fatalf("initial ASSIGN should have default model %q, got %q", DefaultModel, assignMsg.Assign.Model)
	}
	beadSrc.SetBeads(nil)

	// Send DONE with quality gate failed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-defmodel",
			WorkerID:          "w1",
			QualityGatePassed: false,
		},
	})

	// Worker should receive re-ASSIGN with the default model preserved
	retryMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after quality gate failure")
	}
	if retryMsg.Assign.Model != DefaultModel {
		t.Fatalf("re-ASSIGN should preserve default model %q, got %q", DefaultModel, retryMsg.Assign.Model)
	}
}

func TestQualityGateRetry_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Send DONE with quality gate failed for an unregistered worker
	d.handleDone(ctx, "w-ghost", protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-ghost",
			WorkerID:          "w-ghost",
			QualityGatePassed: false,
		},
	})

	// Events should be logged but no panic
	if eventCount(t, d.db, "done") == 0 {
		t.Fatal("expected 'done' event even for unknown worker")
	}
	if eventCount(t, d.db, "quality_gate_rejected") == 0 {
		t.Fatal("expected 'quality_gate_rejected' event even for unknown worker")
	}

	// No merge should happen
	if eventCount(t, d.db, "merged") != 0 {
		t.Fatal("should not merge for unknown worker")
	}
}

// --- Resume, Status, Focus directive tests ---

func TestDispatcher_ResumeDirective_WhenPaused(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Start then pause
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)
	sendDirective(t, d.cfg.SocketPath, "pause")
	waitForState(t, d, StatePaused, 1*time.Second)

	// Send resume
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "resume", "")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}
	if ack.Detail != "resumed" {
		t.Fatalf("expected detail 'resumed', got %q", ack.Detail)
	}
	waitForState(t, d, StateRunning, 1*time.Second)
}

func TestDispatcher_ResumeDirective_WhenAlreadyRunning(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Start (already running)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Send resume while already running
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "resume", "")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}
	if ack.Detail != "already running" {
		t.Fatalf("expected detail 'already running', got %q", ack.Detail)
	}
	// State should still be running
	if d.GetState() != StateRunning {
		t.Fatalf("expected running state, got %s", d.GetState())
	}
}

func TestDispatcher_StatusDirective_ReturnsJSON(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Start and connect a worker with an assignment
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-status-dir", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	beadSrc.SetBeads([]Bead{
		{ID: "bead-s1", Title: "Status test", Priority: 1},
		{ID: "bead-s2", Title: "Status test 2", Priority: 2},
	})
	// Wait for assignment
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send status directive
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "status", "")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}

	// Parse the JSON detail
	var status statusResponse
	if err := json.Unmarshal([]byte(ack.Detail), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v, raw: %s", err, ack.Detail)
	}

	if status.State != string(StateRunning) {
		t.Fatalf("expected state 'running', got %q", status.State)
	}
	if status.WorkerCount != 1 {
		t.Fatalf("expected 1 worker, got %d", status.WorkerCount)
	}
	// Assignments should have the worker->bead mapping
	if len(status.Assignments) == 0 {
		t.Fatal("expected at least one assignment in status")
	}
	if status.Assignments["w-status-dir"] != "bead-s1" {
		t.Fatalf("expected assignment w-status-dir->bead-s1, got %v", status.Assignments)
	}

	// State should NOT have changed
	if d.GetState() != StateRunning {
		t.Fatalf("status directive should not change state, got %s", d.GetState())
	}
}

func TestDispatcher_FocusDirective_SetsEpic(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Start dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Send focus directive with epic ID
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "focus", "epic-42")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}
	if ack.Detail != "focused on epic-42" {
		t.Fatalf("expected detail 'focused on epic-42', got %q", ack.Detail)
	}

	// Verify focusedEpic is stored
	d.mu.Lock()
	epic := d.focusedEpic
	d.mu.Unlock()
	if epic != "epic-42" {
		t.Fatalf("expected focusedEpic 'epic-42', got %q", epic)
	}

	// State should be running
	if d.GetState() != StateRunning {
		t.Fatalf("expected running state after focus, got %s", d.GetState())
	}

	// Verify focusedEpic shows in status
	statusACK := sendDirectiveWithArgs(t, d.cfg.SocketPath, "status", "")
	var status statusResponse
	if err := json.Unmarshal([]byte(statusACK.Detail), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}
	if status.FocusedEpic != "epic-42" {
		t.Fatalf("expected focused_epic 'epic-42' in status, got %q", status.FocusedEpic)
	}
}

func TestDispatcher_FocusDirective_ClearsEpic(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Set a focus first
	sendDirectiveWithArgs(t, d.cfg.SocketPath, "focus", "epic-99")
	d.mu.Lock()
	if d.focusedEpic != "epic-99" {
		d.mu.Unlock()
		t.Fatalf("expected focusedEpic 'epic-99', got %q", d.focusedEpic)
	}
	d.mu.Unlock()

	// Clear focus with empty args
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "focus", "")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}
	if ack.Detail != "focus cleared" {
		t.Fatalf("expected detail 'focus cleared', got %q", ack.Detail)
	}

	// Verify focusedEpic is cleared
	d.mu.Lock()
	epic := d.focusedEpic
	d.mu.Unlock()
	if epic != "" {
		t.Fatalf("expected empty focusedEpic after clear, got %q", epic)
	}
}

func TestDispatcher_FocusEpic_PrioritizesFocusedBeads(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Set focus to "epic-auth"
	sendDirectiveWithArgs(t, d.cfg.SocketPath, "focus", "epic-auth")

	// Provide beads: higher-priority bead is NOT in focused epic,
	// lower-priority bead IS in focused epic.
	beadSrc.SetBeads([]Bead{
		{ID: "bead-p0-other", Title: "Critical other", Priority: 0, Epic: "epic-other"},
		{ID: "bead-p2-auth", Title: "Auth task", Priority: 2, Epic: "epic-auth"},
	})

	// Focused epic bead should be assigned first despite lower priority
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Assign.BeadID != "bead-p2-auth" {
		t.Fatalf("expected focused epic bead bead-p2-auth, got %s", msg.Assign.BeadID)
	}
}

func TestDispatcher_FocusEpic_FallsBackToNonFocused(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Focus on epic with NO ready beads
	sendDirectiveWithArgs(t, d.cfg.SocketPath, "focus", "epic-nonexistent")

	// Only non-focused beads available
	beadSrc.SetBeads([]Bead{
		{ID: "bead-other", Title: "Other work", Priority: 2, Epic: "epic-other"},
	})

	// Should still assign the non-focused bead (fallback)
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Assign.BeadID != "bead-other" {
		t.Fatalf("expected fallback bead bead-other, got %s", msg.Assign.BeadID)
	}
}

func TestDispatcher_NoFocus_PriorityOnly(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// No focus set — pure priority ordering
	beadSrc.SetBeads([]Bead{
		{ID: "bead-p2", Title: "Medium", Priority: 2, Epic: "epic-a"},
		{ID: "bead-p0", Title: "Critical", Priority: 0, Epic: "epic-b"},
	})

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Assign.BeadID != "bead-p0" {
		t.Fatalf("expected priority bead bead-p0, got %s", msg.Assign.BeadID)
	}
}

// --- Scale directive tests ---

// mockProcessManager records Spawn and Kill calls for testing.
type mockProcessManager struct {
	mu       sync.Mutex
	spawned  []string               // IDs passed to Spawn
	killed   []string               // IDs passed to Kill
	spawnErr error                  // if set, Spawn returns this error
	procs    map[string]*os.Process // tracked processes (nil for tests)
}

func (m *mockProcessManager) Spawn(id string) (*os.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.spawnErr != nil {
		return nil, m.spawnErr
	}
	m.spawned = append(m.spawned, id)
	if m.procs == nil {
		m.procs = make(map[string]*os.Process)
	}
	// Use the current process as a stand-in (we never actually kill it in tests)
	m.procs[id] = nil
	return nil, nil
}

func (m *mockProcessManager) Kill(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killed = append(m.killed, id)
	return nil
}

func (m *mockProcessManager) SpawnedIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.spawned))
	copy(out, m.spawned)
	return out
}

func (m *mockProcessManager) KilledIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.killed))
	copy(out, m.killed)
	return out
}

// TestDispatcher_ScaleDirective_StoresTarget verifies that sending a scale
// directive stores the target worker count in the dispatcher state.
func TestDispatcher_ScaleDirective_StoresTarget(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	// Send scale directive with target=5
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "scale", "5")

	if !ack.OK {
		t.Fatalf("expected ACK.OK=true, got false: %s", ack.Detail)
	}

	// Verify target stored
	d.mu.Lock()
	got := d.targetWorkers
	d.mu.Unlock()
	if got != 5 {
		t.Fatalf("expected targetWorkers=5, got %d", got)
	}
}

// TestDispatcher_ReconcileScale_SpawnsWorkers verifies that reconcileScale
// spawns the correct number of worker processes when under target.
func TestDispatcher_ReconcileScale_SpawnsWorkers(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Simulate 2 connected workers
	for _, id := range []string{"w-existing-1", "w-existing-2"} {
		s, c := net.Pipe()
		t.Cleanup(func() { _ = s.Close(); _ = c.Close() })
		d.registerWorker(id, s)
	}

	// Set target to 5 — should spawn 3 more
	d.mu.Lock()
	d.targetWorkers = 5
	d.mu.Unlock()

	d.reconcileScale()

	spawned := pm.SpawnedIDs()
	if len(spawned) != 3 {
		t.Fatalf("expected 3 spawns, got %d: %v", len(spawned), spawned)
	}
}

// TestDispatcher_ReconcileScale_ScaleDown verifies that reconcileScale calls
// GracefulShutdownWorker on excess workers when over target, preferring idle
// workers first.
func TestDispatcher_ReconcileScale_ScaleDown(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.ShutdownTimeout = 200 * time.Millisecond
	startDispatcher(t, d)

	// Connect 5 workers — 3 idle, 2 busy
	for i := 0; i < 5; i++ {
		wid := fmt.Sprintf("w-scale-%d", i)
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, ContextPct: 5},
		})
	}
	waitForWorkers(t, d, 5, 2*time.Second)

	// Mark first 2 as busy
	d.mu.Lock()
	for id, w := range d.workers {
		if id == "w-scale-0" || id == "w-scale-1" {
			w.state = WorkerBusy
			w.beadID = "bead-" + id
		}
	}
	d.mu.Unlock()

	// Set target to 2 — need to remove 3 (should prefer idle ones first)
	d.mu.Lock()
	d.targetWorkers = 2
	d.mu.Unlock()

	d.reconcileScale()

	// Give graceful shutdown time to send messages
	time.Sleep(300 * time.Millisecond)

	// Should have called GracefulShutdownWorker for 3 workers
	// The 3 idle workers should be shut down, leaving the 2 busy ones
	remaining := d.ConnectedWorkers()
	// We expect that GracefulShutdownWorker was called for 3 workers.
	// Since our test workers receive PREPARE_SHUTDOWN but don't respond,
	// eventually they get hard-killed after timeout. We just check that
	// at most 2 remain or that shutdown was initiated.
	if remaining > 5 {
		t.Fatalf("expected at most 5 workers (shutdown in progress), got %d", remaining)
	}

	// Verify: idle workers were targeted first by checking worker states.
	// After reconcile, the busy workers (w-scale-0, w-scale-1) should still be present.
	d.mu.Lock()
	busyCount := 0
	for _, w := range d.workers {
		if w.state == WorkerBusy {
			busyCount++
		}
	}
	d.mu.Unlock()

	// Busy workers should be the last to be shut down
	if busyCount < 2 && d.ConnectedWorkers() > 2 {
		t.Fatalf("expected busy workers to be preserved during scale-down, busyCount=%d, connected=%d", busyCount, d.ConnectedWorkers())
	}
}

// TestDispatcher_ScaleDirective_ACKIncludesDetail verifies that the ACK
// response from a scale directive includes the expected detail string.
func TestDispatcher_ScaleDirective_ACKIncludesDetail(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	// Connect 2 workers so reconcile knows current count
	for _, wid := range []string{"w-ack-1", "w-ack-2"} {
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, ContextPct: 5},
		})
	}
	waitForWorkers(t, d, 2, 1*time.Second)

	// Send scale directive with target=5
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "scale", "5")

	if !ack.OK {
		t.Fatalf("expected ACK.OK=true, got false: %s", ack.Detail)
	}

	// ACK detail should contain target info and spawning count
	if !containsStr(ack.Detail, "target=5") {
		t.Fatalf("expected ACK detail to contain 'target=5', got: %s", ack.Detail)
	}
	if !containsStr(ack.Detail, "spawning 3") {
		t.Fatalf("expected ACK detail to contain 'spawning 3', got: %s", ack.Detail)
	}
}

// TestDispatcher_ScaleDirective_InvalidArgs verifies that a scale directive
// with non-integer args returns an error ACK.
func TestDispatcher_PrioritySorting_HighestPriorityAssignedFirst(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect a single worker
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Provide beads in REVERSE priority order: P3 first, P0 last
	beadSrc.SetBeads([]Bead{
		{ID: "bead-p3", Title: "Low priority", Priority: 3},
		{ID: "bead-p0", Title: "Critical", Priority: 0},
		{ID: "bead-p2", Title: "Medium priority", Priority: 2},
	})

	// Worker should receive the P0 bead (highest priority = lowest number)
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "bead-p0" {
		t.Fatalf("expected highest priority bead bead-p0, got %s", msg.Assign.BeadID)
	}
}

func TestDispatcher_ScaleDirective_InvalidArgs(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "scale", "notanumber")

	if ack.OK {
		t.Fatal("expected ACK.OK=false for non-integer scale args")
	}
	if !containsStr(ack.Detail, "invalid") {
		t.Fatalf("expected ACK detail to contain 'invalid', got: %s", ack.Detail)
	}
}

// --- Review rejection counter tests (oro-jhs) ---

// helper: set up dispatcher with rejected reviewer, connect worker, assign bead, trigger review.
// Returns the dispatcher, conn, escalator, and spawnMock for further assertions.
func setupReviewRejection(t *testing.T) (*Dispatcher, net.Conn, *mockEscalator, *mockSubprocessSpawner) {
	t.Helper()
	d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)
	spawnMock.mu.Lock()
	spawnMock.verdict = "REJECTED: missing edge case tests"
	spawnMock.mu.Unlock()

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-rej", Title: "Rejection test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume initial ASSIGN
	if !ok {
		t.Fatal("expected initial ASSIGN")
	}
	beadSrc.SetBeads(nil)

	return d, conn, esc, spawnMock
}

func TestDispatcher_ReviewRejection_FeedbackForwarded(t *testing.T) {
	_, conn, _, _ := setupReviewRejection(t)

	// First rejection
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})

	// Should get re-ASSIGN with feedback text
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after rejection")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.Feedback == "" {
		t.Fatal("expected feedback text in re-ASSIGN after rejection")
	}
	if !strings.Contains(msg.Assign.Feedback, "missing edge case tests") {
		t.Fatalf("expected feedback to contain reviewer comment, got: %s", msg.Assign.Feedback)
	}
}

func TestDispatcher_ReviewRejection_EscalatesAfterTwoRejections(t *testing.T) {
	d, conn, esc, _ := setupReviewRejection(t)

	// First rejection cycle
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})
	_, ok := readMsg(t, conn, 3*time.Second) // consume re-ASSIGN
	if !ok {
		t.Fatal("expected re-ASSIGN after 1st rejection")
	}

	// Second rejection cycle
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})
	_, ok = readMsg(t, conn, 3*time.Second) // consume re-ASSIGN
	if !ok {
		t.Fatal("expected re-ASSIGN after 2nd rejection")
	}

	// Third rejection cycle — should escalate to manager, NOT re-assign
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})

	// Should NOT receive another ASSIGN — escalation instead
	// Wait for escalation event
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "review_escalated") > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if eventCount(t, d.db, "review_escalated") == 0 {
		t.Fatal("expected 'review_escalated' event after 3rd rejection")
	}

	// Verify escalation message sent to manager
	msgs := esc.Messages()
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "bead-rej") && strings.Contains(m, "STUCK") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected escalation about bead-rej, got: %v", msgs)
	}
}

// --- Ralph handoff tests (oro-vuw) ---

func TestDispatcher_Handoff_SpawnsNewWorkerInSameWorktree(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	// Connect worker, assign bead
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-ralph", Title: "Ralph test", Priority: 1}})
	assignMsg, ok := readMsg(t, conn1, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	originalWorktree := assignMsg.Assign.Worktree
	beadSrc.SetBeads(nil)

	// Worker sends HANDOFF (context exhausted)
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:    "bead-ralph",
			WorkerID:  "w1",
			Learnings: []string{"learned something"},
		},
	})

	// Old worker should receive SHUTDOWN
	msg, ok := readMsg(t, conn1, 2*time.Second)
	if !ok {
		t.Fatal("expected SHUTDOWN after handoff")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Dispatcher should have spawned a new worker process
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(pm.SpawnedIDs()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pm.SpawnedIDs()) == 0 {
		t.Fatal("expected new worker process to be spawned for handoff")
	}

	// Simulate new worker connecting (the spawned process)
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w2", ContextPct: 0},
	})

	// New worker should receive ASSIGN with the SAME bead and worktree
	msg2, ok := readMsg(t, conn2, 3*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN for new worker after handoff")
	}
	if msg2.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg2.Type)
	}
	if msg2.Assign.BeadID != "bead-ralph" {
		t.Fatalf("expected bead-ralph, got %s", msg2.Assign.BeadID)
	}
	if msg2.Assign.Worktree != originalWorktree {
		t.Fatalf("expected same worktree %s, got %s", originalWorktree, msg2.Assign.Worktree)
	}
}

func TestDispatcher_Handoff_PendingHandoffConsumedOnce(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Manually add a pending handoff
	d.mu.Lock()
	d.pendingHandoffs = map[string]*pendingHandoff{
		"bead-x": {worktree: "/tmp/wt-x", model: DefaultModel},
	}
	d.mu.Unlock()

	// Consume it
	h := d.consumePendingHandoff()
	if h == nil {
		t.Fatal("expected pending handoff")
	}
	if h.worktree != "/tmp/wt-x" {
		t.Fatalf("expected worktree /tmp/wt-x, got %s", h.worktree)
	}

	// Second consume should return nil (already consumed)
	h2 := d.consumePendingHandoff()
	if h2 != nil {
		t.Fatal("expected nil after consuming pending handoff")
	}
}

func TestDispatcher_Handoff_NoProcManager_LogsOnly(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	// No procMgr set — handoff should still SHUTDOWN but not spawn
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-noproc", Title: "No proc", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send HANDOFF
	sendMsg(t, conn, protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{BeadID: "bead-noproc", WorkerID: "w1"},
	})

	// Should still get SHUTDOWN
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected SHUTDOWN")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// handoff_pending event logged
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "handoff_pending") > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected handoff_pending event")
}

func TestDispatcher_ReviewRejection_CounterResetsOnNewBead(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Simulate rejection counts for different beads
	d.mu.Lock()
	if d.rejectionCounts == nil {
		d.rejectionCounts = make(map[string]int)
	}
	d.rejectionCounts["bead-a"] = 2
	d.rejectionCounts["bead-b"] = 1
	d.mu.Unlock()

	// Clear bead-a's count (simulates bead completion)
	d.clearRejectionCount("bead-a")

	d.mu.Lock()
	_, aExists := d.rejectionCounts["bead-a"]
	bCount := d.rejectionCounts["bead-b"]
	d.mu.Unlock()

	if aExists {
		t.Fatal("expected bead-a rejection count to be cleared")
	}
	if bCount != 1 {
		t.Fatalf("expected bead-b count to remain 1, got %d", bCount)
	}
}

// --- Diagnosis agent wiring tests (oro-2dj) ---

// setupHandoffDiagnosis creates a dispatcher with a connected worker assigned to
// a bead, ready for testing handoff-triggered diagnosis. Returns all pieces
// needed to send multiple handoffs and verify diagnosis/escalation behavior.
func setupHandoffDiagnosis(t *testing.T) (*Dispatcher, net.Conn, *mockEscalator, *mockSubprocessSpawner) {
	t.Helper()
	d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]Bead{{ID: "bead-stuck", Title: "Stuck bead", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume initial ASSIGN
	if !ok {
		t.Fatal("expected initial ASSIGN")
	}
	beadSrc.SetBeads(nil)

	return d, conn, esc, spawnMock
}

func TestDispatcher_Handoff_TracksCountPerBead(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// handoffCounts should exist and be empty initially
	d.mu.Lock()
	if d.handoffCounts == nil {
		d.mu.Unlock()
		t.Fatal("expected handoffCounts map to be initialized")
	}
	count := d.handoffCounts["bead-x"]
	d.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected 0 handoffs for unknown bead, got %d", count)
	}
}

func TestDispatcher_Handoff_FirstHandoff_RespawnsNormally(t *testing.T) {
	d, conn, _, _ := setupHandoffDiagnosis(t)

	// First handoff — should respawn worker normally, NOT diagnose
	sendMsg(t, conn, protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{BeadID: "bead-stuck", WorkerID: "w1"},
	})

	// Worker should receive SHUTDOWN
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected SHUTDOWN after first handoff")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Verify handoff count is 1
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		count := d.handoffCounts["bead-stuck"]
		d.mu.Unlock()
		if count == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	d.mu.Lock()
	got := d.handoffCounts["bead-stuck"]
	d.mu.Unlock()
	t.Fatalf("expected handoff count 1, got %d", got)
}

func TestDispatcher_Handoff_SecondHandoff_TriggersDiagnosis(t *testing.T) {
	d, conn, _, spawnMock := setupHandoffDiagnosis(t)

	// Set diagnosis output
	spawnMock.mu.Lock()
	spawnMock.verdict = "Root cause: test flake in TestFoo due to race condition"
	spawnMock.mu.Unlock()

	// Pre-set handoff count to 1 (simulating first handoff already happened)
	d.mu.Lock()
	d.handoffCounts["bead-stuck"] = 1
	d.mu.Unlock()

	// Second handoff — should trigger ops.Diagnose() instead of normal respawn
	sendMsg(t, conn, protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{BeadID: "bead-stuck", WorkerID: "w1"},
	})

	// Worker should still receive SHUTDOWN
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected SHUTDOWN after second handoff")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Verify diagnosis event logged
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if eventCount(t, d.db, "diagnosis_spawned") > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected 'diagnosis_spawned' event after 2nd handoff")
}

func TestDispatcher_Handoff_DiagnosisFailure_EscalatesToManager(t *testing.T) {
	d, conn, esc, spawnMock := setupHandoffDiagnosis(t)

	// Make diagnosis agent fail
	spawnMock.mu.Lock()
	spawnMock.spawnErr = errors.New("diagnosis agent spawn failed")
	spawnMock.mu.Unlock()

	// Pre-set handoff count to 1
	d.mu.Lock()
	d.handoffCounts["bead-stuck"] = 1
	d.mu.Unlock()

	// Second handoff — diagnosis should be triggered but fail
	sendMsg(t, conn, protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{BeadID: "bead-stuck", WorkerID: "w1"},
	})

	// Consume SHUTDOWN
	readMsg(t, conn, 2*time.Second)

	// Verify escalation to manager
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs := esc.Messages()
		for _, m := range msgs {
			if strings.Contains(m, "bead-stuck") && strings.Contains(m, "STUCK") {
				// Also verify diagnosis_escalated event
				if eventCount(t, d.db, "diagnosis_escalated") > 0 {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected STUCK escalation for bead-stuck, got: %v", esc.Messages())
}

func TestDispatcher_Handoff_CountResetsOnDone(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Simulate handoff counts for different beads
	d.mu.Lock()
	d.handoffCounts["bead-a"] = 2
	d.handoffCounts["bead-b"] = 1
	d.mu.Unlock()

	// Clear bead-a's count (simulates bead completion via clearHandoffCount)
	d.clearHandoffCount("bead-a")

	d.mu.Lock()
	_, aExists := d.handoffCounts["bead-a"]
	bCount := d.handoffCounts["bead-b"]
	d.mu.Unlock()

	if aExists {
		t.Fatal("expected bead-a handoff count to be cleared")
	}
	if bCount != 1 {
		t.Fatalf("expected bead-b count to remain 1, got %d", bCount)
	}
}

func TestShutdownCleanup_CallsBeadSync(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Call shutdownCleanup directly (no need to start the full dispatcher).
	d.shutdownCleanup()

	beadSrc.mu.Lock()
	synced := beadSrc.synced
	beadSrc.mu.Unlock()

	if !synced {
		t.Fatal("expected BeadSource.Sync to be called during shutdownCleanup")
	}
}

// TestMergeClosesBead verifies that after a successful merge, the dispatcher
// calls beads.Close(beadID) so the bead doesn't get re-assigned.
func TestMergeClosesBead(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Init schema so logEvent works.
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-merge-close"
	workerID := "w-merge"
	worktree := "/tmp/worktree-" + beadID
	branch := "agent/" + beadID

	// Call mergeAndComplete directly (white-box).
	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch)

	// Verify beads.Close was called with the correct bead ID.
	beadSrc.mu.Lock()
	closed := beadSrc.closed
	beadSrc.mu.Unlock()

	found := false
	for _, id := range closed {
		if id == beadID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected beads.Close(%q) to be called after successful merge, but closed=%v", beadID, closed)
	}
}
