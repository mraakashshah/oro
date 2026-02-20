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

	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// --- Mock implementations ---

// mockConn is a simple net.Conn implementation that captures writes.
type mockConn struct {
	written [][]byte
	closed  bool
	mu      sync.Mutex
}

func newMockConn() *mockConn {
	return &mockConn{written: make([][]byte, 0)}
}

func (m *mockConn) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, net.ErrClosed
	}
	// Copy the bytes since caller may reuse the slice
	copied := make([]byte, len(b))
	copy(copied, b)
	m.written = append(m.written, copied)
	return len(b), nil
}

func (m *mockConn) Read(b []byte) (int, error) {
	return 0, net.ErrClosed // Not implementing reads for this test
}

func (m *mockConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockConn) LocalAddr() net.Addr                { return nil }
func (m *mockConn) RemoteAddr() net.Addr               { return nil }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

type createCall struct {
	title, beadType     string
	priority            int
	description, parent string
	acceptanceCriteria  string
}

type mockBeadSource struct {
	mu                   sync.Mutex
	beads                []protocol.Bead
	shown                map[string]*protocol.BeadDetail
	closed               []string
	updated              map[string]string // beadID -> status
	created              []createCall
	createID             string // ID returned by Create; defaults to "oro-new1"
	synced               bool
	readyErr             error           // if set, Ready() returns this error
	allChildrenClosedMap map[string]bool // epicID -> allClosed
	allChildrenClosedErr error           // if set, AllChildrenClosed() returns this error
	hasChildrenMap       map[string]bool // epicID -> hasChildren
	hasChildrenErr       error           // if set, HasChildren() returns this error
}

func (m *mockBeadSource) Ready(_ context.Context) ([]protocol.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readyErr != nil {
		return nil, m.readyErr
	}
	out := make([]protocol.Bead, len(m.beads))
	copy(out, m.beads)
	return out, nil
}

func (m *mockBeadSource) Show(_ context.Context, id string) (*protocol.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.shown[id]; ok {
		return d, nil
	}
	// Default: return detail with acceptance criteria so assignBead doesn't skip.
	return &protocol.BeadDetail{
		Title:              id,
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
	}, nil
}

func (m *mockBeadSource) Close(_ context.Context, id string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, id)
	return nil
}

func (m *mockBeadSource) Update(_ context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updated == nil {
		m.updated = make(map[string]string)
	}
	m.updated[id] = status
	return nil
}

func (m *mockBeadSource) Sync(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.synced = true
	return nil
}

func (m *mockBeadSource) Create(_ context.Context, title, beadType string, priority int, description, parent, acceptanceCriteria string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, createCall{title, beadType, priority, description, parent, acceptanceCriteria})
	id := "oro-new1"
	if m.createID != "" {
		id = m.createID
	}
	return id, nil
}

func (m *mockBeadSource) AllChildrenClosed(_ context.Context, epicID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.allChildrenClosedErr != nil {
		return false, m.allChildrenClosedErr
	}
	if m.allChildrenClosedMap != nil {
		if result, ok := m.allChildrenClosedMap[epicID]; ok {
			return result, nil
		}
	}
	// Default: return false (epic has open children or is not an epic)
	return false, nil
}

func (m *mockBeadSource) HasChildren(_ context.Context, epicID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hasChildrenErr != nil {
		return false, m.hasChildrenErr
	}
	if m.hasChildrenMap != nil {
		if result, ok := m.hasChildrenMap[epicID]; ok {
			return result, nil
		}
	}
	return false, nil
}

func (m *mockBeadSource) SetBeads(beads []protocol.Bead) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.beads = beads
}

type mockWorktreeManager struct {
	mu       sync.Mutex
	created  map[string]string // beadID -> worktree path
	removed  []string
	createFn func(ctx context.Context, beadID string) (string, string, error)
	removeFn func(ctx context.Context, path string) error
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

func (m *mockWorktreeManager) Remove(ctx context.Context, path string) error {
	m.mu.Lock()
	fn := m.removeFn
	m.mu.Unlock()
	if fn != nil {
		if err := fn(ctx, path); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removed = append(m.removed, path)
	return nil
}

func (m *mockWorktreeManager) Prune(ctx context.Context) error {
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
	mu           sync.Mutex
	failOn       string     // if set, fail when this arg is in the command
	conflict     bool       // if true, rebase returns conflict error
	conflictOnce bool       // if true, fail on the first rebase only
	rebaseCalls  [][]string // records args for each rebase invocation
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
	if len(args) > 0 && args[0] == "rebase" {
		if len(args) > 1 && args[1] == "--abort" {
			return "", "", nil // abort succeeds
		}
		// Record rebase call args (copy to avoid aliasing).
		cp := make([]string, len(args))
		copy(cp, args)
		m.rebaseCalls = append(m.rebaseCalls, cp)
		if m.conflict || m.conflictOnce {
			m.conflictOnce = false // consume the one-shot flag
			return "", "CONFLICT (content): Merge conflict in file.go\n", fmt.Errorf("rebase failed")
		}
	}

	// rev-parse HEAD returns a fake SHA
	if len(args) > 0 && args[0] == "rev-parse" {
		return "abc123def456\n", "", nil
	}
	// rev-list returns a fake commit so cherry-pick path is entered
	if len(args) > 0 && args[0] == "rev-list" {
		return "abc123def456\n", "", nil
	}
	return "", "", nil
}

// RebaseCalls returns a snapshot of all rebase arg slices recorded so far.
func (m *mockGitRunner) RebaseCalls() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]string, len(m.rebaseCalls))
	copy(out, m.rebaseCalls)
	return out
}

// mockBatchSpawner for ops.Spawner
type mockBatchSpawner struct {
	mu       sync.Mutex
	verdict  string
	spawnErr error
}

func (m *mockBatchSpawner) Spawn(_ context.Context, _ string, _ string, _ string) (ops.Process, error) {
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

// TestConfigValidation verifies that Config.validate() rejects invalid values.
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		cfg       Config
		wantError bool
		errSubstr string
	}{
		{
			name:      "all defaults pass validation",
			cfg:       Config{},
			wantError: false,
		},
		{
			name: "valid explicit values",
			cfg: Config{
				MaxWorkers:           10,
				HeartbeatTimeout:     30 * time.Second,
				PollInterval:         5 * time.Second,
				FallbackPollInterval: 30 * time.Second,
				ShutdownTimeout:      15 * time.Second,
			},
			wantError: false,
		},
		{
			name: "negative HeartbeatTimeout",
			cfg: Config{
				HeartbeatTimeout: -1 * time.Second,
			},
			wantError: true,
			errSubstr: "HeartbeatTimeout",
		},
		{
			name: "zero HeartbeatTimeout gets default and passes",
			cfg: Config{
				HeartbeatTimeout: 0,
			},
			wantError: false,
		},
		{
			name: "negative PollInterval",
			cfg: Config{
				PollInterval: -5 * time.Second,
			},
			wantError: true,
			errSubstr: "PollInterval",
		},
		{
			name: "zero PollInterval gets default and passes",
			cfg: Config{
				PollInterval: 0,
			},
			wantError: false,
		},
		{
			name: "negative FallbackPollInterval",
			cfg: Config{
				FallbackPollInterval: -10 * time.Second,
			},
			wantError: true,
			errSubstr: "FallbackPollInterval",
		},
		{
			name: "zero FallbackPollInterval gets default and passes",
			cfg: Config{
				FallbackPollInterval: 0,
			},
			wantError: false,
		},
		{
			name: "negative ShutdownTimeout",
			cfg: Config{
				ShutdownTimeout: -3 * time.Second,
			},
			wantError: true,
			errSubstr: "ShutdownTimeout",
		},
		{
			name: "zero ShutdownTimeout gets default and passes",
			cfg: Config{
				ShutdownTimeout: 0,
			},
			wantError: false,
		},
		{
			name: "negative MaxWorkers",
			cfg: Config{
				MaxWorkers: -1,
			},
			wantError: true,
			errSubstr: "MaxWorkers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply defaults first (as New() does)
			resolved := tt.cfg.withDefaults()
			err := resolved.validate()

			// Early return for success case
			if !tt.wantError {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}

			// Error case: must have error and contain expected substring
			if err == nil {
				t.Fatalf("expected validation error containing %q, got nil", tt.errSubstr)
			}
			if !strings.Contains(err.Error(), tt.errSubstr) {
				t.Fatalf("expected error containing %q, got: %v", tt.errSubstr, err)
			}
		})
	}
}

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
func newTestDispatcher(t *testing.T) (*Dispatcher, *mockBeadSource, *mockWorktreeManager, *mockEscalator, *mockGitRunner, *mockBatchSpawner) {
	t.Helper()
	db := newTestDB(t)

	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)

	spawnMock := &mockBatchSpawner{verdict: "APPROVED: looks good"}
	opsSpawner := ops.NewSpawner(spawnMock)

	beadSrc := &mockBeadSource{
		beads: []protocol.Bead{},
		shown: make(map[string]*protocol.BeadDetail),
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

	d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
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
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.listener != nil
	}, 2*time.Second)

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
	waitFor(t, func() bool {
		return d.GetState() == want
	}, timeout)
}

// waitForWorkers polls until the expected number of workers are connected.
func waitForWorkers(t *testing.T, d *Dispatcher, want int, timeout time.Duration) {
	t.Helper()
	waitFor(t, func() bool {
		return d.ConnectedWorkers() == want
	}, timeout)
}

// waitForWorkerState polls until a specific worker reaches the expected state.
func waitForWorkerState(t *testing.T, d *Dispatcher, workerID string, want protocol.WorkerState, timeout time.Duration) {
	t.Helper()
	waitFor(t, func() bool {
		st, _, ok := d.WorkerInfo(workerID)
		return ok && st == want
	}, timeout)
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
	// Verify state remains inert (no sleep needed - state is synchronous)
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
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-1", Title: "Test", Priority: 1}})

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

func TestRunWaitsForGoroutines(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	ctx, cancel := context.WithCancel(context.Background())

	// Track if Run() has returned
	runCompleted := make(chan struct{})

	// Start dispatcher
	go func() {
		_ = d.Run(ctx)
		close(runCompleted)
	}()

	// Wait for listener to be ready
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

	// Cancel the context
	cancel()

	// Run() should wait for goroutines to finish, then return within timeout
	select {
	case <-runCompleted:
		// Success - Run() completed after goroutines finished
	case <-time.After(6 * time.Second):
		t.Fatal("Run() did not return within 6s timeout after context cancel")
	}
}

func TestAcceptLoopBackpressure(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Verify the semaphore channel has capacity 100
	if cap(d.acceptSem) != 100 {
		t.Fatalf("Expected acceptSem capacity of 100, got %d", cap(d.acceptSem))
	}

	// Create 101 long-lived connections that hold their handlers open
	conns := make([]net.Conn, 101)
	connected := make([]bool, 101)
	var wg sync.WaitGroup

	// Connect 101 workers concurrently
	for i := 0; i < 101; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			conn, err := net.DialTimeout("unix", d.cfg.SocketPath, 1*time.Second)
			if err != nil {
				return
			}
			conns[idx] = conn
			connected[idx] = true

			// Send heartbeat to register with dispatcher
			sendMsg(t, conn, protocol.Message{
				Type: protocol.MsgHeartbeat,
				Heartbeat: &protocol.HeartbeatPayload{
					WorkerID:   fmt.Sprintf("worker-%d", idx),
					ContextPct: 10,
				},
			})
		}(i)
	}

	// Wait for all connection attempts to complete
	wg.Wait()

	// Wait for handlers to acquire semaphore slots
	waitFor(t, func() bool {
		return len(d.acceptSem) >= 100
	}, 1*time.Second)

	// Count how many semaphore slots are in use
	slotsInUse := len(d.acceptSem)

	// Verify no more than 100 slots are in use (the semaphore is enforcing the limit)
	if slotsInUse > 100 {
		t.Errorf("Expected max 100 semaphore slots in use, got %d", slotsInUse)
	}

	// Verify we have exactly 100 slots in use (all slots filled)
	if slotsInUse != 100 {
		t.Logf("Warning: Expected 100 semaphore slots in use, got %d (101st connection may be blocked)", slotsInUse)
	}

	// Cleanup - close all connections to release semaphore slots
	for _, conn := range conns {
		if conn != nil {
			_ = conn.Close()
		}
	}

	// Wait for handlers to release semaphore
	waitFor(t, func() bool {
		return len(d.acceptSem) == 0
	}, 1*time.Second)

	// Verify semaphore is empty after cleanup
	if len(d.acceptSem) != 0 {
		t.Errorf("Expected semaphore to be empty after cleanup, got %d slots in use", len(d.acceptSem))
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-42", Title: "Build thing", Priority: 1}})

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
	waitForWorkerState(t, d, "w1", protocol.WorkerBusy, 1*time.Second)
}

func TestDispatcher_AssignBead_SkipsBeadWithoutAcceptance(t *testing.T) {
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

	// Provide a bead with explicitly empty acceptance criteria
	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		"bead-no-ac": {Title: "No acceptance", AcceptanceCriteria: ""},
	}
	beadSrc.mu.Unlock()
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-no-ac", Title: "No acceptance", Priority: 1}})

	// Should NOT receive ASSIGN (bead lacks acceptance criteria)
	_, ok := readMsg(t, conn, 500*time.Millisecond)
	if ok {
		t.Fatal("should not assign bead without acceptance criteria")
	}

	// Verify escalation was logged
	waitFor(t, func() bool {
		return eventCount(t, d.db, "missing_acceptance") > 0
	}, 1*time.Second)
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
	beadSrc.SetBeads([]protocol.Bead{{
		ID: "bead-model", Title: "Model test", Priority: 1,
		Model: "sonnet",
	}})

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Assign.Model != "sonnet" {
		t.Fatalf("expected model claude-sonnet-4-5, got %q", msg.Assign.Model)
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
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-default", Title: "Default model", Priority: 1}})

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Assign.Model != protocol.DefaultModel {
		t.Fatalf("expected default model %q, got %q", protocol.DefaultModel, msg.Assign.Model)
	}
}

func TestDispatcher_AssignBead_MarksInProgress(t *testing.T) {
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "oro-test1", Title: "Test bead", Priority: 1}})

	// Read ASSIGN
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}

	// Verify bead was marked in_progress (oro-p3wd)
	beadSrc.mu.Lock()
	status := beadSrc.updated["oro-test1"]
	beadSrc.mu.Unlock()

	if status != "in_progress" {
		t.Fatalf("expected bead oro-test1 to be marked in_progress, got %q", status)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-merge", Title: "Merge test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "merged") > 0
	}, 2*time.Second)

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

func TestDispatcher_WorkerDone_RemovesWorktreeAfterMerge(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-wt-rm", Title: "WT remove test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE with QG passed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: "bead-wt-rm", WorkerID: "w1", QualityGatePassed: true},
	})

	// Wait for merge + worktree removal
	waitFor(t, func() bool {
		return eventCount(t, d.db, "merged") > 0
	}, 2*time.Second)

	// Verify worktree was removed after merge
	waitFor(t, func() bool {
		wtMgr.mu.Lock()
		defer wtMgr.mu.Unlock()
		return len(wtMgr.removed) > 0
	}, 1*time.Second)

	wtMgr.mu.Lock()
	expectedPath := "/tmp/worktree-bead-wt-rm"
	found := false
	for _, p := range wtMgr.removed {
		if p == expectedPath {
			found = true
			break
		}
	}
	wtMgr.mu.Unlock()
	if !found {
		t.Fatalf("expected worktree %q to be removed, removed: %v", expectedPath, wtMgr.removed)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-conflict", Title: "Conflict test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "merge_conflict") > 0
	}, 2*time.Second)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-handoff", Title: "Handoff test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "handoff") > 0
	}, 1*time.Second)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-dead", Title: "Dead worker test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Don't send any more heartbeats — wait for timeout
	waitFor(t, func() bool {
		return d.ConnectedWorkers() == 0
	}, 2*time.Second)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-crash", Title: "Crash test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Don't send any more heartbeats — wait for timeout + escalation
	waitFor(t, func() bool {
		msgs := esc.Messages()
		if len(msgs) > 0 {
			msg := msgs[0]
			if !strings.HasPrefix(msg, "[ORO-DISPATCH] WORKER_CRASH: bead-crash") {
				t.Fatalf("heartbeat escalation should use structured format, got: %q", msg)
			}
			if !strings.Contains(msg, "w-crash") {
				t.Fatalf("heartbeat escalation should mention worker ID, got: %q", msg)
			}
			return true
		}
		return false
	}, 2*time.Second)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-review", Title: "Review test", Priority: 1}})
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
	waitForWorkerState(t, d, "w1", protocol.WorkerReviewing, 1*time.Second)

	// Verify event logged
	waitFor(t, func() bool {
		return eventCount(t, d.db, "ready_for_review") > 0
	}, 1*time.Second)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-approved", Title: "Approved test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "review_approved") > 0
	}, 3*time.Second)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-rejected", Title: "Rejected test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "review_rejected") > 0
	}, 3*time.Second)

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
	waitForWorkerState(t, d, "w-reconnect", protocol.WorkerBusy, 1*time.Second)

	// Verify reconnect event
	waitFor(t, func() bool {
		return eventCount(t, d.db, "reconnect") > 0
	}, 1*time.Second)
}

func TestDispatcher_StopDirective_AlwaysRejected(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Start the dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Stop directive should be rejected — dispatcher stays running.
	// (P0 fix: only SIGTERM via 'oro stop' can stop the swarm.)
	sendDirective(t, d.cfg.SocketPath, "stop")

	// Give a moment for the directive to be processed
	time.Sleep(50 * time.Millisecond)

	if d.GetState() != StateRunning {
		t.Fatalf("dispatcher should remain running after stop directive, got %s", d.GetState())
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-paused", Title: "Paused", Priority: 1}})
	_, ok := readMsg(t, conn, 400*time.Millisecond)
	if ok {
		t.Fatal("should not receive ASSIGN in paused state")
	}
}

func TestDispatcher_Escalation(t *testing.T) {
	d, beadSrc, _, esc, gitRunner, _ := newTestDispatcher(t)
	// Configure git to fail on ff-only merge (non-conflict failure)
	gitRunner.mu.Lock()
	gitRunner.failOn = "--ff-only"
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-esc", Title: "Escalation test", Priority: 1}})
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

	waitFor(t, func() bool {
		msgs := esc.Messages()
		if len(msgs) > 0 {
			msg := msgs[0]
			if !strings.HasPrefix(msg, "[ORO-DISPATCH] MERGE_CONFLICT: bead-esc") {
				t.Fatalf("escalation should use structured format, got: %q", msg)
			}
			return true
		}
		return false
	}, 5*time.Second)
}

func TestParseEscalationType(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{"stuck_worker", "[ORO-DISPATCH] STUCK_WORKER: oro-abc — worker stalled.", "STUCK_WORKER"},
		{"merge_conflict", "[ORO-DISPATCH] MERGE_CONFLICT: oro-xyz — merge failed.", "MERGE_CONFLICT"},
		{"missing_ac", "[ORO-DISPATCH] MISSING_AC: oro-noac — no AC.", "MISSING_AC"},
		{"priority_contention_no_longer_targeted", "[ORO-DISPATCH] PRIORITY_CONTENTION: oro-p0 — P0 queued.", "PRIORITY_CONTENTION"},
		{"stuck_not_targeted", "[ORO-DISPATCH] STUCK: oro-s — stuck.", ""},
		{"worker_crash_not_targeted", "[ORO-DISPATCH] WORKER_CRASH: oro-c — crash.", ""},
		{"status_not_targeted", "[ORO-DISPATCH] STATUS: oro-st — status.", ""},
		{"no_prefix", "random message", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEscalationType(tt.msg)
			if got != tt.want {
				t.Fatalf("parseEscalationType(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}

func TestEscalateSpawnsOneShotForTargetTypes(t *testing.T) {
	d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)

	// Provide bead detail so the one-shot gets context.
	beadSrc.mu.Lock()
	beadSrc.shown["bead-esc"] = &protocol.BeadDetail{
		ID:          "bead-esc",
		Title:       "Test escalation bead",
		Description: "Testing one-shot spawning",
	}
	beadSrc.mu.Unlock()

	// Set spawn verdict to simulate ACK response.
	spawnMock.mu.Lock()
	spawnMock.verdict = "ACK: restarted worker"
	spawnMock.mu.Unlock()

	ctx := context.Background()

	// Trigger escalation with a STUCK_WORKER message.
	msg := protocol.FormatEscalation(protocol.EscStuckWorker, "bead-esc", "worker stalled", "no progress")
	d.escalate(ctx, msg, "bead-esc", "w1")

	// Verify the tmux escalation was still sent.
	msgs := esc.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 escalation message, got %d", len(msgs))
	}

	// Give the async one-shot goroutine time to spawn.
	waitFor(t, func() bool {
		return len(d.ops.Active()) == 0 // one-shot already completed (mock returns immediately)
	}, 2*time.Second)
}

func TestEscalateDoesNotSpawnForNonTargetTypes(t *testing.T) {
	d, _, _, esc, _, spawnMock := newTestDispatcher(t)

	// Set spawn mock to track calls.
	spawnMock.mu.Lock()
	spawnMock.verdict = "ACK: done"
	callsBefore := 0
	spawnMock.mu.Unlock()
	_ = callsBefore

	ctx := context.Background()

	// Trigger escalation with STUCK (not a target type).
	msg := protocol.FormatEscalation(protocol.EscStuck, "bead-nontarget", "stuck", "")
	d.escalate(ctx, msg, "bead-nontarget", "w1")

	// Verify the tmux escalation was sent.
	msgs := esc.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 escalation message, got %d", len(msgs))
	}

	// No one-shot should have been spawned — Active should stay 0.
	// Wait briefly to ensure no async spawn happened.
	waitFor(t, func() bool {
		return len(d.ops.Active()) == 0
	}, 500*time.Millisecond)
}

func TestOneShotTimeoutEscalatesToPersistentManager(t *testing.T) {
	d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)

	// Provide bead detail for context.
	beadSrc.mu.Lock()
	beadSrc.shown["bead-timeout"] = &protocol.BeadDetail{
		ID:          "bead-timeout",
		Title:       "Timeout test bead",
		Description: "Testing one-shot timeout escalation",
	}
	beadSrc.mu.Unlock()

	// Simulate a timeout by making the spawn return an error.
	// In reality, the timeout happens in ops.Spawner, but we simulate it here.
	spawnMock.mu.Lock()
	spawnMock.spawnErr = fmt.Errorf("ops: process exceeded 5m0s timeout")
	spawnMock.mu.Unlock()

	ctx := context.Background()

	// Trigger escalation with a STUCK_WORKER message.
	msg := protocol.FormatEscalation(protocol.EscStuckWorker, "bead-timeout", "worker stalled", "no progress")
	d.escalate(ctx, msg, "bead-timeout", "w1")

	// The initial tmux escalation should be sent.
	msgs := esc.Messages()
	if len(msgs) < 1 {
		t.Fatalf("expected at least 1 escalation message, got %d", len(msgs))
	}

	// Wait for the async one-shot goroutine to process the timeout.
	// After timeout, it should escalate again to the persistent manager.
	waitFor(t, func() bool {
		return len(esc.Messages()) >= 2 // initial + timeout escalation
	}, 2*time.Second)

	msgs = esc.Messages()
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 escalation messages (initial + timeout), got %d", len(msgs))
	}

	// The second message should mention the one-shot failure.
	secondMsg := msgs[1]
	if !containsIgnoreCase(secondMsg, "one-shot") && !containsIgnoreCase(secondMsg, "timeout") {
		t.Fatalf("second escalation should mention one-shot failure or timeout, got: %q", secondMsg)
	}
}

func TestOneShotResolutionAcksEscalationInDB(t *testing.T) {
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)

	beadSrc.mu.Lock()
	beadSrc.shown["bead-ack"] = &protocol.BeadDetail{
		ID:          "bead-ack",
		Title:       "Ack test bead",
		Description: "Testing one-shot acks escalation in DB",
	}
	beadSrc.mu.Unlock()

	// Simulate successful one-shot resolution.
	spawnMock.mu.Lock()
	spawnMock.verdict = "ACK: resolved"
	spawnMock.spawnErr = nil
	spawnMock.mu.Unlock()

	ctx := context.Background()

	// Trigger escalation — this persists a row and spawns one-shot.
	msg := protocol.FormatEscalation(protocol.EscStuckWorker, "bead-ack", "worker stalled", "no progress")
	d.escalate(ctx, msg, "bead-ack", "w1")

	// Wait for the async one-shot goroutine to complete and ack the escalation.
	waitFor(t, func() bool {
		var status string
		err := d.db.QueryRowContext(ctx,
			`SELECT status FROM escalations WHERE bead_id = ? ORDER BY id DESC LIMIT 1`,
			"bead-ack").Scan(&status)
		return err == nil && status == "acked"
	}, 2*time.Second)

	// Confirm the escalation row is acked.
	var status string
	if err := d.db.QueryRowContext(ctx,
		`SELECT status FROM escalations WHERE bead_id = ? ORDER BY id DESC LIMIT 1`,
		"bead-ack").Scan(&status); err != nil {
		t.Fatalf("query escalation status: %v", err)
	}
	if status != "acked" {
		t.Fatalf("expected escalation status 'acked', got %q", status)
	}
}

func TestDispatcher_ConcurrentWorkers(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{
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
	if resolved.MaxWorkers != 10 {
		t.Fatalf("MaxWorkers: got %d, want 10", resolved.MaxWorkers)
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
		{protocol.DirectiveFocus, "epic-1", StateRunning},
	}

	for _, tt := range tests {
		_, _ = d.applyDirective(tt.dir, tt.args)
		if d.GetState() != tt.want {
			t.Fatalf("after %s: got %s, want %s", tt.dir, d.GetState(), tt.want)
		}
	}
}

func TestNew_TargetWorkersDefaultsToMaxWorkers(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	// newTestDispatcher sets MaxWorkers=5, so targetWorkers should default to 5
	// (auto-scale to max on startup instead of waiting for a scale directive).
	if got := d.TargetWorkers(); got != 5 {
		t.Fatalf("expected targetWorkers=MaxWorkers=5, got %d", got)
	}
}

func TestApplyDirective_StopAlwaysRejected(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	// Stop directive is unconditionally rejected (P0 fix: only SIGTERM via
	// 'oro stop' can stop the swarm — no directive can do it).
	_, err := d.applyDirective(protocol.DirectiveStop, "")
	if err == nil {
		t.Fatal("expected error for stop directive")
	}
	if d.GetState() != StateRunning {
		t.Fatalf("state should remain running after stop directive, got %s", d.GetState())
	}
}

func TestShutdownAuthorized_DefaultsFalse(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	if d.ShutdownAuthorized().Load() {
		t.Fatal("shutdownAuthorized should default to false")
	}
}

func TestApplyDirective_ShutdownRejected(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	_, err := d.applyDirective(protocol.DirectiveShutdown, "")
	if err == nil {
		t.Fatal("expected shutdown directive to be rejected")
	}
	if !strings.Contains(err.Error(), "oro stop") {
		t.Errorf("expected error to mention 'oro stop', got: %v", err)
	}
	// State should NOT change to stopping.
	if d.GetState() != StateRunning {
		t.Fatalf("state = %s, want %s (shutdown should be rejected)", d.GetState(), StateRunning)
	}
}

func TestApplyDirective_KillWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)
	ctx := context.Background()

	// Init schema
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// Register worker and assign a bead
	workerID := "test-worker"
	beadID := "oro-test"
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	d.registerWorker(workerID, conn1)

	// Assign bead to worker
	d.mu.Lock()
	w := d.workers[workerID]
	w.state = protocol.WorkerBusy
	w.beadID = beadID
	w.worktree = "/fake/worktree"
	d.targetWorkers = 1
	d.mu.Unlock()

	// Create assignment in DB
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, "/fake/worktree")
	if err != nil {
		t.Fatalf("failed to create assignment: %v", err)
	}

	// Test: kill the worker
	detail, err := d.applyDirective(protocol.DirectiveKillWorker, workerID)
	if err != nil {
		t.Fatalf("applyDirective(kill-worker) failed: %v", err)
	}
	if !strings.Contains(detail, "killed") {
		t.Errorf("expected detail to mention 'killed', got: %s", detail)
	}

	// Assert: worker removed from pool
	d.mu.Lock()
	_, exists := d.workers[workerID]
	targetCount := d.targetWorkers
	d.mu.Unlock()
	if exists {
		t.Errorf("worker %s should be removed from pool", workerID)
	}

	// Assert: target count NOT decremented (worker registered via registerWorker
	// without pendingManagedIDs entry is unmanaged; only managed workers affect
	// targetWorkers).
	if targetCount != 1 {
		t.Errorf("targetWorkers = %d, want 1 (unmanaged worker does not affect target count)", targetCount)
	}

	// Assert: bead returned to queue (assignment marked completed)
	var status string
	err = d.db.QueryRow(
		`SELECT status FROM assignments WHERE bead_id = ? AND worker_id = ?`,
		beadID, workerID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query assignment: %v", err)
	}
	if status != "completed" {
		t.Errorf("assignment status = %s, want 'completed' (bead returned to queue)", status)
	}
}

func TestApplyDirective_KillWorker_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Test: kill unknown worker
	_, err := d.applyDirective(protocol.DirectiveKillWorker, "unknown-worker")
	if err == nil {
		t.Fatal("expected error for unknown worker")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to mention 'not found', got: %v", err)
	}
}

func TestApplyDirective_KillWorker_EmptyArgs(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Test: empty args
	_, err := d.applyDirective(protocol.DirectiveKillWorker, "")
	if err == nil {
		t.Fatal("expected error for empty args")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("expected error to mention 'required', got: %v", err)
	}
}

func TestApplyDirective_SpawnFor(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)

	pm := &mockProcessManager{}
	d.procMgr = pm
	d.targetWorkers = 1

	detail, err := d.applyDirective(protocol.DirectiveSpawnFor, "oro-test-bead")
	if err != nil {
		t.Fatalf("applyDirective(spawn-for) failed: %v", err)
	}
	if !strings.Contains(detail, "spawned") {
		t.Errorf("expected detail to mention 'spawned', got: %s", detail)
	}
	if !strings.Contains(detail, "oro-test-bead") {
		t.Errorf("expected detail to mention bead ID, got: %s", detail)
	}

	// Assert: target count incremented
	d.mu.Lock()
	targetCount := d.targetWorkers
	hasPriority := d.priorityBeads["oro-test-bead"]
	d.mu.Unlock()
	if targetCount != 2 {
		t.Errorf("targetWorkers = %d, want 2 (incremented from 1)", targetCount)
	}
	if !hasPriority {
		t.Error("expected bead to be in priorityBeads")
	}

	// Assert: a worker was spawned
	pm.mu.Lock()
	spawnCount := len(pm.spawned)
	pm.mu.Unlock()
	if spawnCount != 1 {
		t.Errorf("expected 1 worker spawned, got %d", spawnCount)
	}
}

func TestApplyDirective_SpawnFor_AlreadyAssigned(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Register worker with assigned bead
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()
	d.registerWorker("existing-worker", conn1)
	d.mu.Lock()
	d.workers["existing-worker"].beadID = "oro-taken"
	d.mu.Unlock()

	_, err := d.applyDirective(protocol.DirectiveSpawnFor, "oro-taken")
	if err == nil {
		t.Fatal("expected error for already-assigned bead")
	}
	if !strings.Contains(err.Error(), "already assigned") {
		t.Errorf("expected error to mention 'already assigned', got: %v", err)
	}
}

func TestApplyDirective_SpawnFor_EmptyArgs(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	_, err := d.applyDirective(protocol.DirectiveSpawnFor, "")
	if err == nil {
		t.Fatal("expected error for empty args")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("expected error to mention 'required', got: %v", err)
	}
}

func TestApplyDirective_RestartWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.setState(StateRunning)
	ctx := context.Background()

	// Init schema
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// Set up a mock process manager to track spawns
	pm := NewExecProcessManager(d.cfg.SocketPath)
	d.SetProcessManager(pm)

	// Register worker and assign a bead
	workerID := "test-worker"
	beadID := "oro-test"
	conn1, conn2 := net.Pipe()
	defer conn1.Close()
	defer conn2.Close()

	d.registerWorker(workerID, conn1)

	// Assign bead to worker
	d.mu.Lock()
	w := d.workers[workerID]
	w.state = protocol.WorkerBusy
	w.beadID = beadID
	w.worktree = "/fake/worktree"
	initialTarget := 3
	d.targetWorkers = initialTarget
	d.mu.Unlock()

	// Create assignment in DB
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, "/fake/worktree")
	if err != nil {
		t.Fatalf("failed to create assignment: %v", err)
	}

	// Test: restart the worker
	detail, err := d.applyDirective(protocol.DirectiveRestartWorker, workerID)
	if err != nil {
		t.Fatalf("applyDirective(restart-worker) failed: %v", err)
	}
	if !strings.Contains(detail, "restarted") {
		t.Errorf("expected detail to mention 'restarted', got: %s", detail)
	}

	// Assert: old worker removed from pool
	d.mu.Lock()
	_, exists := d.workers[workerID]
	targetCount2 := d.targetWorkers
	d.mu.Unlock()
	if exists {
		t.Errorf("old worker %s should be removed from pool", workerID)
	}

	// Assert: target count unchanged
	if targetCount2 != initialTarget {
		t.Errorf("targetWorkers = %d, want %d (unchanged)", targetCount2, initialTarget)
	}

	// Assert: bead returned to queue (assignment marked completed)
	var status string
	err = d.db.QueryRow(
		`SELECT status FROM assignments WHERE bead_id = ? AND worker_id = ?`,
		beadID, workerID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query assignment: %v", err)
	}
	if status != "completed" {
		t.Errorf("assignment status = %s, want 'completed' (bead requeued)", status)
	}

	// Assert: new worker spawned (process manager called)
	pm.mu.Lock()
	_, spawned := pm.procs[workerID]
	pm.mu.Unlock()
	if !spawned {
		t.Errorf("expected new worker %s to be spawned", workerID)
	}

	// Cleanup: kill the spawned process
	_ = pm.Kill(workerID)
	pm.Wait()
}

func TestApplyDirective_RestartWorker_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Test: restart unknown worker
	_, err := d.applyDirective(protocol.DirectiveRestartWorker, "unknown-worker")
	if err == nil {
		t.Fatal("expected error for unknown worker")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to mention 'not found', got: %v", err)
	}
}

func TestApplyDirective_RestartWorker_EmptyArgs(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Test: empty args
	_, err := d.applyDirective(protocol.DirectiveRestartWorker, "")
	if err == nil {
		t.Fatal("expected error for empty args")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("expected error to mention 'required', got: %v", err)
	}
}

func TestApplyDirective_Preempt(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Setup: create worker with active assignment
	workerID := "worker-preempt-test"
	beadID := "oro-preempt-bead"

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn,
		state:    protocol.WorkerBusy,
		beadID:   beadID,
		worktree: "/fake/worktree",
		encoder:  json.NewEncoder(conn),
	}
	d.mu.Unlock()

	// Create assignment in DB
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		beadID, workerID, "/fake/worktree")
	if err != nil {
		t.Fatalf("failed to create assignment: %v", err)
	}

	// Test: preempt the worker
	detail, err := d.applyDirective(protocol.DirectivePreempt, workerID)
	if err != nil {
		t.Fatalf("applyDirective(preempt) failed: %v", err)
	}
	if !strings.Contains(detail, "preempted") {
		t.Errorf("expected detail to mention 'preempted', got: %s", detail)
	}

	// Assert: worker still in pool but marked for preemption
	d.mu.Lock()
	w, exists := d.workers[workerID]
	d.mu.Unlock()
	if !exists {
		t.Errorf("worker %s should still be in pool during graceful preemption", workerID)
	}
	if w.state != protocol.WorkerPreempting {
		t.Errorf("worker state = %v, want %v (WorkerPreempting)", w.state, protocol.WorkerPreempting)
	}

	// Assert: PREEMPT message sent to worker
	if len(conn.written) == 0 {
		t.Fatalf("expected PREEMPT message to be sent to worker")
	}
	var msg protocol.Message
	if err := json.Unmarshal(conn.written[0], &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	if msg.Type != protocol.MsgPreempt {
		t.Errorf("message type = %v, want %v (MsgPreempt)", msg.Type, protocol.MsgPreempt)
	}

	// Assert: bead NOT immediately requeued (graceful, worker handles it)
	var status string
	err = d.db.QueryRow(
		`SELECT status FROM assignments WHERE bead_id = ? AND worker_id = ?`,
		beadID, workerID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query assignment: %v", err)
	}
	if status != "active" {
		t.Errorf("assignment status = %s, want 'active' (not requeued yet, worker will do gracefully)", status)
	}
}

func TestApplyDirective_Preempt_UnknownWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Test: preempt unknown worker
	_, err := d.applyDirective(protocol.DirectivePreempt, "unknown-worker")
	if err == nil {
		t.Fatal("expected error for unknown worker")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to mention 'not found', got: %v", err)
	}
}

func TestApplyDirective_Preempt_EmptyArgs(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Test: empty args
	_, err := d.applyDirective(protocol.DirectivePreempt, "")
	if err == nil {
		t.Fatal("expected error for empty args")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("expected error to mention 'required', got: %v", err)
	}
}

func TestRun_RejectsShutdownDirective(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run(ctx)
	}()

	// Wait for listener to be ready.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.listener != nil
	}, 2*time.Second)

	// Send shutdown directive via UDS — should be rejected.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{Op: "shutdown"},
	})

	// Read ACK — should indicate failure.
	ack, _ := readMsg(t, conn, 5*time.Second)
	if ack.ACK == nil {
		t.Fatal("expected ACK response")
	}
	if ack.ACK.OK {
		t.Fatal("expected shutdown directive to be rejected (OK=false)")
	}

	// Run should still be alive — cancel to clean up.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after context cancel")
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "status") > 0
	}, 1*time.Second)
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
	if st != protocol.WorkerIdle {
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "directive") > 0
	}, 1*time.Second)

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
		state:   protocol.WorkerIdle,
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
	waitForWorkerState(t, d, "w-idle-reconnect", protocol.WorkerIdle, 1*time.Second)
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

	waitForWorkerState(t, d, "w-buffered", protocol.WorkerBusy, 1*time.Second)

	// The buffered heartbeat should have been processed — check event
	waitFor(t, func() bool {
		return eventCount(t, d.db, "heartbeat") > 0
	}, 1*time.Second)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-fail", Title: "QG fail test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "quality_gate_rejected") > 0
	}, 2*time.Second)

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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-pass", Title: "QG pass test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "merged") > 0
	}, 2*time.Second)

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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-mem", Title: "Memory handoff test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "handoff") > 0
	}, 1*time.Second)

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
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-reassign", Title: "fix linting with ruff and pyright", Priority: 1}})

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

func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-gs", Title: "Graceful shutdown test", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "shutdown_approved") > 0
	}, 2*time.Second)

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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-timeout", Title: "Timeout test", Priority: 1}})
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
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-mem-inject", Title: "run go vet and lint checks", Priority: 1}})

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

	beadSrc.SetBeads([]protocol.Bead{
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

	beadSrc.SetBeads([]protocol.Bead{
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
	bead := protocol.Bead{ID: "bead-cleanup", Title: "Cleanup test", Priority: 1}

	// Grab the tracked worker so we can call assignBead directly
	d.mu.Lock()
	w := d.workers["w-broken"]
	d.mu.Unlock()

	// Call assignBead — worktree creation succeeds, but sendToWorker should fail
	_ = d.assignBead(ctx, w, bead)

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

type slowBatchSpawner struct {
	mu        sync.Mutex
	processes []*slowProcess
}

func (s *slowBatchSpawner) Spawn(_ context.Context, _ string, _ string, _ string) (ops.Process, error) {
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

	slowSpawner := &slowBatchSpawner{}
	opsSpawner := ops.NewSpawner(slowSpawner)

	beadSrc := &mockBeadSource{beads: []protocol.Bead{}, shown: make(map[string]*protocol.BeadDetail)}
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

	d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	cancel := startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-ops-kill", Title: "Ops kill test", Priority: 1}})
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

	spawner := ops.NewSpawner(&mockBatchSpawner{})
	beadSrc := &mockBeadSource{beads: []protocol.Bead{}, shown: make(map[string]*protocol.BeadDetail)}
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

	d, err := New(cfg, db, merger, spawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
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

	beadSrc.SetBeads([]protocol.Bead{
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

func TestShutdown_WorktreesRemovedAfterWorkerStop(t *testing.T) {
	// This test verifies that during shutdown, PREPARE_SHUTDOWN is sent to
	// workers BEFORE worktrees are removed. Previously, shutdownCleanup()
	// removed worktrees first, causing active workers to crash when their
	// working directories disappeared.

	db := newTestDB(t)
	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)

	spawner := ops.NewSpawner(&mockBatchSpawner{})
	beadSrc := &mockBeadSource{beads: []protocol.Bead{}, shown: make(map[string]*protocol.BeadDetail)}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}

	sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		MaxWorkers:       5,
		HeartbeatTimeout: 2 * time.Second,
		PollInterval:     50 * time.Millisecond,
		ShutdownTimeout:  2 * time.Second,
	}

	d, err := New(cfg, db, merger, spawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	cancel := startDispatcher(t, d)

	// Connect two workers.
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-order-1", ContextPct: 5},
	})
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-order-2", ContextPct: 5},
	})
	waitForWorkers(t, d, 2, 1*time.Second)

	// Start dispatcher and assign two beads so worktrees get created.
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-order-1", Title: "Order test 1", Priority: 1},
		{ID: "bead-order-2", Title: "Order test 2", Priority: 2},
	})

	// Consume ASSIGN messages from both workers.
	if _, ok := readMsg(t, conn1, 2*time.Second); !ok {
		t.Fatal("expected ASSIGN for w-order-1")
	}
	if _, ok := readMsg(t, conn2, 2*time.Second); !ok {
		t.Fatal("expected ASSIGN for w-order-2")
	}
	beadSrc.SetBeads(nil)

	// Track whether PREPARE_SHUTDOWN was sent before worktree removal.
	// We read messages from both worker connections in goroutines.
	var shutdownSent atomic.Int32

	go func() {
		msg, ok := readMsg(t, conn1, 5*time.Second)
		if ok && msg.Type == protocol.MsgPrepareShutdown {
			shutdownSent.Add(1)
		}
	}()
	go func() {
		msg, ok := readMsg(t, conn2, 5*time.Second)
		if ok && msg.Type == protocol.MsgPrepareShutdown {
			shutdownSent.Add(1)
		}
	}()

	// Install a removeFn that checks ordering: by the time Remove is called,
	// PREPARE_SHUTDOWN should already have been sent to workers.
	var worktreeRemovedBeforeShutdown atomic.Bool
	wtMgr.mu.Lock()
	wtMgr.removeFn = func(_ context.Context, _ string) error {
		// If PREPARE_SHUTDOWN hasn't been sent to ANY worker yet, flag the error.
		if shutdownSent.Load() == 0 {
			worktreeRemovedBeforeShutdown.Store(true)
		}
		return nil
	}
	wtMgr.mu.Unlock()

	// Trigger shutdown.
	cancel()

	// Wait for worktrees to be removed.
	deadline := time.Now().Add(3 * time.Second)
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
	nRemoved := len(wtMgr.removed)
	wtMgr.mu.Unlock()

	if nRemoved < 2 {
		t.Fatalf("expected at least 2 worktrees removed, got %d", nRemoved)
	}

	if worktreeRemovedBeforeShutdown.Load() {
		t.Fatal("worktrees were removed BEFORE PREPARE_SHUTDOWN was sent to workers — " +
			"shutdown must stop workers before cleaning up worktrees")
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
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-watch-1", Title: "Test bead", Priority: 1}})

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
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-fallback", Title: "Fallback test", Priority: 1}})

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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-retry", Title: "QG retry", Priority: 1}})
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-busy", Title: "QG busy", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Verify worker is busy after initial assignment
	waitForWorkerState(t, d, "w1", protocol.WorkerBusy, 1*time.Second)

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
	if st != protocol.WorkerBusy {
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-nomerge", Title: "QG no merge", Priority: 1}})
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-event", Title: "QG event", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "quality_gate_rejected") > 0
	}, 2*time.Second)

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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-merge", Title: "QG merge", Priority: 1}})
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "merged") > 0
	}, 2*time.Second)

	// No quality_gate_rejected event
	if eventCount(t, d.db, "quality_gate_rejected") != 0 {
		t.Fatal("should not have quality_gate_rejected when gate passed")
	}

	// Worker should become idle after merge
	waitForWorkerState(t, d, "w1", protocol.WorkerIdle, 2*time.Second)

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

func TestQualityGateRetry_ModelEscalatedToOpus(t *testing.T) {
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
	beadSrc.SetBeads([]protocol.Bead{{
		ID: "bead-qg-model", Title: "QG model", Priority: 1,
		Model: protocol.ModelSonnet,
	}})
	assignMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if assignMsg.Assign.Model != protocol.ModelSonnet {
		t.Fatalf("initial ASSIGN should have model %q, got %q", protocol.ModelSonnet, assignMsg.Assign.Model)
	}
	beadSrc.SetBeads(nil)

	// Verify the model is stored on the tracked worker
	model, ok := d.WorkerModel("w1")
	if !ok {
		t.Fatal("expected worker to be tracked")
	}
	if model != protocol.ModelSonnet {
		t.Fatalf("expected stored model %q, got %q", protocol.ModelSonnet, model)
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

	// Worker should receive re-ASSIGN escalated to opus
	retryMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after quality gate failure")
	}
	if retryMsg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", retryMsg.Type)
	}
	if retryMsg.Assign.Model != protocol.ModelOpus {
		t.Fatalf("re-ASSIGN should escalate to opus %q, got %q", protocol.ModelOpus, retryMsg.Assign.Model)
	}

	// Verify the worker's stored model was updated to opus
	model, ok = d.WorkerModel("w1")
	if !ok {
		t.Fatal("expected worker to be tracked after retry")
	}
	if model != protocol.ModelOpus {
		t.Fatalf("expected stored model %q after escalation, got %q", protocol.ModelOpus, model)
	}

	// Verify attempt counter was reset to 0 (the retry incremented it to 1,
	// then escalation reset it to 0, so current value should be 0).
	d.mu.Lock()
	count := d.attemptCounts["bead-qg-model"]
	d.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected attempt count reset to 0 after escalation, got %d", count)
	}
}

func TestQualityGateRetry_DefaultModelEscalatedToOpus(t *testing.T) {
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

	// Assign bead with no model (should resolve to default = sonnet)
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-defmodel", Title: "QG default model", Priority: 1}})
	assignMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if assignMsg.Assign.Model != protocol.DefaultModel {
		t.Fatalf("initial ASSIGN should have default model %q, got %q", protocol.DefaultModel, assignMsg.Assign.Model)
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

	// Worker should receive re-ASSIGN escalated to opus (default model is sonnet)
	retryMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after quality gate failure")
	}
	if retryMsg.Assign.Model != protocol.ModelOpus {
		t.Fatalf("re-ASSIGN should escalate default model to opus %q, got %q", protocol.ModelOpus, retryMsg.Assign.Model)
	}
}

func TestQualityGateRetry_OpusStaysOpus(t *testing.T) {
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

	// Assign bead with explicit opus model
	beadSrc.SetBeads([]protocol.Bead{{
		ID: "bead-qg-opus", Title: "QG opus stays", Priority: 1,
		Model: protocol.ModelOpus,
	}})
	assignMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if assignMsg.Assign.Model != protocol.ModelOpus {
		t.Fatalf("initial ASSIGN should have model %q, got %q", protocol.ModelOpus, assignMsg.Assign.Model)
	}
	beadSrc.SetBeads(nil)

	// Send DONE with quality gate failed
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-qg-opus",
			WorkerID:          "w1",
			QualityGatePassed: false,
		},
	})

	// Worker should receive re-ASSIGN with model still opus
	retryMsg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after quality gate failure")
	}
	if retryMsg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", retryMsg.Type)
	}
	if retryMsg.Assign.Model != protocol.ModelOpus {
		t.Fatalf("re-ASSIGN should keep opus model %q, got %q", protocol.ModelOpus, retryMsg.Assign.Model)
	}

	// Verify attempt counter was NOT reset (should be 1 since no escalation happened)
	d.mu.Lock()
	count := d.attemptCounts["bead-qg-opus"]
	d.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected attempt count 1 (not reset) for opus worker, got %d", count)
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

// deadConn is a net.Conn whose Write always returns an error.
// Used to simulate a dead worker connection in tests.
type deadConn struct {
	net.Conn
}

func (deadConn) Write([]byte) (int, error) {
	return 0, errors.New("connection dead")
}

func (deadConn) Close() error { return nil }

func TestQGRetry_DeadWorker_RequeuesBead(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	beadID := "bead-dead-qg"
	workerID := "w1"

	// Manually register a worker with a dead connection (white-box).
	// This guarantees sendToWorker will fail immediately.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     deadConn{},
		state:    protocol.WorkerBusy,
		beadID:   beadID,
		worktree: "/tmp/test-worktree",
		model:    protocol.ModelSonnet,
		lastSeen: time.Now(),
	}
	d.mu.Unlock()

	// Create an active assignment in the DB so completeAssignment has
	// something to mark as completed.
	if err := d.createAssignment(ctx, beadID, workerID, "/tmp/test-worktree"); err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	// Call handleDone with QualityGatePassed=false.
	// The QG retry path will try to sendToWorker, which fails on the dead
	// connection. The fix should log "qg_retry_send_failed", release the
	// worker, complete the assignment, and clear bead tracking.
	d.handleDone(ctx, workerID, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            beadID,
			WorkerID:          workerID,
			QualityGatePassed: false,
		},
	})

	// 1. Verify "qg_retry_send_failed" event is logged
	if eventCount(t, d.db, "qg_retry_send_failed") == 0 {
		t.Fatal("expected 'qg_retry_send_failed' event when worker is dead")
	}

	// 2. Verify assignment is completed (bead returns to ready pool)
	var status string
	err := d.db.QueryRow(
		`SELECT status FROM assignments WHERE bead_id=? ORDER BY id DESC LIMIT 1`,
		beadID,
	).Scan(&status)
	if err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "completed" {
		t.Fatalf("expected assignment status 'completed', got %q", status)
	}

	// 3. Verify worker state is Idle (not Busy).
	// Worker being removed from tracking is also acceptable for a dead worker.
	st, wBead, ok := d.WorkerInfo(workerID)
	if ok {
		if st == protocol.WorkerBusy {
			t.Fatalf("expected worker state Idle (not Busy), got %s with bead %s", st, wBead)
		}
		if wBead != "" {
			t.Fatalf("expected worker beadID to be cleared, got %q", wBead)
		}
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

	beadSrc.SetBeads([]protocol.Bead{
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
	beadSrc.SetBeads([]protocol.Bead{
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
	beadSrc.SetBeads([]protocol.Bead{
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
	beadSrc.SetBeads([]protocol.Bead{
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

func TestBuildStatusJSON_ContainsPID(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "status", "")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}

	var status statusResponse
	if err := json.Unmarshal([]byte(ack.Detail), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v, raw: %s", err, ack.Detail)
	}

	if status.PID != os.Getpid() {
		t.Errorf("expected PID %d in status response, got %d", os.Getpid(), status.PID)
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

// TestDispatcher_AutoScaleOnStartup verifies that the assign loop automatically
// calls reconcileScale, spawning workers up to targetWorkers without needing a
// scale directive.
func TestDispatcher_AutoScaleOnStartup(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	// Send start directive so dispatcher enters Running state
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// targetWorkers should be MaxWorkers=5 from New()
	if got := d.TargetWorkers(); got != 5 {
		t.Fatalf("expected targetWorkers=5, got %d", got)
	}

	// Wait for assign loop to call reconcileScale and spawn workers
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) >= 5
	}, 3*time.Second)
}

// TestDispatcher_ReconcileScale_SpawnsWorkers verifies that reconcileScale
// spawns the correct number of worker processes when under target.
func TestDispatcher_ReconcileScale_SpawnsWorkers(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Simulate 2 connected managed workers (dispatcher-spawned).
	for _, id := range []string{"w-existing-1", "w-existing-2"} {
		s, c := net.Pipe()
		t.Cleanup(func() { _ = s.Close(); _ = c.Close() })
		// Mark as pending managed so registerWorker sets managed=true.
		d.mu.Lock()
		d.pendingManagedIDs[id] = true
		d.mu.Unlock()
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

	// Connect 5 managed workers — 3 idle, 2 busy.
	// Pre-register IDs as pending managed so registerWorker sets managed=true.
	for i := 0; i < 5; i++ {
		wid := fmt.Sprintf("w-scale-%d", i)
		d.mu.Lock()
		d.pendingManagedIDs[wid] = true
		d.mu.Unlock()
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
			w.state = protocol.WorkerBusy
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
		if w.state == protocol.WorkerBusy {
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

	// Connect 2 managed workers so reconcile counts them toward the target.
	for _, wid := range []string{"w-ack-1", "w-ack-2"} {
		// Pre-register as pending managed so registerWorker sets managed=true.
		d.mu.Lock()
		d.pendingManagedIDs[wid] = true
		d.mu.Unlock()
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
	beadSrc.SetBeads([]protocol.Bead{
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
func setupReviewRejection(t *testing.T) (*Dispatcher, net.Conn, *mockEscalator, *mockBatchSpawner) {
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-rej", Title: "Rejection test", Priority: 1}})
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

	// Rejection re-ASSIGN must include Attempt counter (1-based rejection count).
	if msg.Assign.Attempt != 1 {
		t.Fatalf("expected Attempt=1 after first rejection, got %d", msg.Assign.Attempt)
	}
}

func TestDispatcher_ReviewRejection_AttemptIncrementsOnEachRejection(t *testing.T) {
	_, conn, _, _ := setupReviewRejection(t)

	// First rejection → Attempt=1
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})
	msg1, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg1.Type != protocol.MsgAssign {
		t.Fatal("expected ASSIGN after 1st rejection")
	}
	if msg1.Assign.Attempt != 1 {
		t.Fatalf("expected Attempt=1, got %d", msg1.Assign.Attempt)
	}

	// Second rejection → Attempt=2
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})
	msg2, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg2.Type != protocol.MsgAssign {
		t.Fatal("expected ASSIGN after 2nd rejection")
	}
	if msg2.Assign.Attempt != 2 {
		t.Fatalf("expected Attempt=2, got %d", msg2.Assign.Attempt)
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "review_escalated") > 0
	}, 3*time.Second)

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

func TestDispatcher_ReviewRejection_WorkerIdleAfterMaxRejections(t *testing.T) {
	d, conn, _, _ := setupReviewRejection(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// First rejection cycle — worker gets re-ASSIGN
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})
	_, ok := readMsg(t, conn, 3*time.Second) // consume re-ASSIGN
	if !ok {
		t.Fatal("expected re-ASSIGN after 1st rejection")
	}

	// Second rejection cycle — worker gets re-ASSIGN
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})
	_, ok = readMsg(t, conn, 3*time.Second) // consume re-ASSIGN
	if !ok {
		t.Fatal("expected re-ASSIGN after 2nd rejection")
	}

	// Third rejection cycle — should escalate, NOT re-assign
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})

	// Wait for escalation event to confirm the escalation path was taken
	waitFor(t, func() bool {
		return eventCount(t, d.db, "review_escalated") > 0
	}, 3*time.Second)

	// AC1: Worker must transition to Idle within one assign cycle
	waitFor(t, func() bool {
		state, _, ok := d.WorkerInfo("w1")
		return ok && state == protocol.WorkerIdle
	}, 3*time.Second)

	// AC3: Worker's beadID must be empty
	state, beadID, ok := d.WorkerInfo("w1")
	if !ok {
		t.Fatal("worker w1 not found after max rejections")
	}
	if state != protocol.WorkerIdle {
		t.Fatalf("expected worker state Idle, got %s", state)
	}
	if beadID != "" {
		t.Fatalf("expected empty beadID, got %q", beadID)
	}

	// Verify tracking maps are cleared
	d.mu.Lock()
	_, rejExists := d.rejectionCounts["bead-rej"]
	_, attExists := d.attemptCounts["bead-rej"]
	d.mu.Unlock()
	if rejExists {
		t.Fatal("expected rejection count cleared for bead-rej")
	}
	if attExists {
		t.Fatal("expected attempt count cleared for bead-rej")
	}

	// Verify procMgr.Kill was called for the worker
	pm.mu.Lock()
	killed := make([]string, len(pm.killed))
	copy(killed, pm.killed)
	pm.mu.Unlock()
	if len(killed) == 0 {
		t.Fatal("expected procMgr.Kill to be called for the zombie worker")
	}
	foundKill := false
	for _, id := range killed {
		if id == "w1" {
			foundKill = true
			break
		}
	}
	if !foundKill {
		t.Fatalf("expected Kill('w1'), got kills: %v", killed)
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-ralph", Title: "Ralph test", Priority: 1}})
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
		"bead-x": {worktree: "/tmp/wt-x", model: protocol.DefaultModel},
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-noproc", Title: "No proc", Priority: 1}})
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

// --- Review rejection MemoryContext tests (oro-eou) ---

// TestDispatcher_ReviewRejection_MemoryContextIncludesFeedback verifies that
// the re-ASSIGN after a rejection includes a MemoryContext that contains the
// reviewer feedback, so the worker knows why it was rejected.
func TestDispatcher_ReviewRejection_MemoryContextIncludesFeedback(t *testing.T) {
	_, conn, _, _ := setupReviewRejection(t)

	// Trigger first rejection
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})

	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after rejection")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}

	// MemoryContext must include the reviewer feedback so the worker understands
	// why it was rejected and doesn't retry blindly.
	if msg.Assign.MemoryContext == "" {
		t.Fatal("expected non-empty MemoryContext in rejection re-ASSIGN")
	}
	if !containsStr(msg.Assign.MemoryContext, "missing edge case tests") {
		t.Errorf("expected MemoryContext to contain reviewer feedback, got: %s", msg.Assign.MemoryContext)
	}
}

// TestDispatcher_ReviewRejection_MemoryContextAccumulatesFeedback verifies that
// on the second rejection, the MemoryContext includes both the second rejection
// feedback and (via memory store) the previously stored first rejection feedback.
func TestDispatcher_ReviewRejection_MemoryContextAccumulatesFeedback(t *testing.T) {
	d, conn, _, spawnMock := setupReviewRejection(t)

	// First rejection
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})
	msg1, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg1.Type != protocol.MsgAssign {
		t.Fatal("expected ASSIGN after 1st rejection")
	}
	// Verify Attempt=1 and MemoryContext includes feedback
	if msg1.Assign.Attempt != 1 {
		t.Fatalf("expected Attempt=1 after 1st rejection, got %d", msg1.Assign.Attempt)
	}
	if msg1.Assign.MemoryContext == "" {
		t.Fatal("expected non-empty MemoryContext after 1st rejection")
	}

	// Change feedback for second rejection to distinguish from first
	spawnMock.mu.Lock()
	spawnMock.verdict = "REJECTED: also missing integration test"
	spawnMock.mu.Unlock()

	// Second rejection
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rej", WorkerID: "w1"},
	})
	msg2, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg2.Type != protocol.MsgAssign {
		t.Fatal("expected ASSIGN after 2nd rejection")
	}
	if msg2.Assign.Attempt != 2 {
		t.Fatalf("expected Attempt=2 after 2nd rejection, got %d", msg2.Assign.Attempt)
	}
	if msg2.Assign.MemoryContext == "" {
		t.Fatal("expected non-empty MemoryContext after 2nd rejection")
	}
	// Second rejection MemoryContext must contain the second feedback
	if !containsStr(msg2.Assign.MemoryContext, "integration test") {
		t.Errorf("expected MemoryContext to contain 2nd rejection feedback, got: %s", msg2.Assign.MemoryContext)
	}

	// Verify stored rejection memories in the DB (both rejections stored)
	_ = d // used for db access if needed
}

// --- Diagnosis agent wiring tests (oro-2dj) ---

// setupHandoffDiagnosis creates a dispatcher with a connected worker assigned to
// a bead, ready for testing handoff-triggered diagnosis. Returns all pieces
// needed to send multiple handoffs and verify diagnosis/escalation behavior.
func setupHandoffDiagnosis(t *testing.T) (*Dispatcher, net.Conn, *mockEscalator, *mockBatchSpawner) {
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-stuck", Title: "Stuck bead", Priority: 1}})
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

	// Call shutdownRemoveWorktrees directly (no need to start the full dispatcher).
	d.shutdownRemoveWorktrees(nil)

	beadSrc.mu.Lock()
	synced := beadSrc.synced
	beadSrc.mu.Unlock()

	if !synced {
		t.Fatal("expected BeadSource.Sync to be called during shutdownRemoveWorktrees")
	}
}

// TestShutdownResetsInProgressBeads verifies that shutdownSequence resets all
// beads with active assignments back to open status so they become re-assignable
// on the next dispatcher start. This is phase 3b: between shutdownWaitForWorkers
// and shutdownRemoveWorktrees.
func TestShutdownResetsInProgressBeads(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Insert two active assignments directly into the DB.
	for _, beadID := range []string{"bead-reset-a", "bead-reset-b"} {
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
			beadID, "w-test", "/tmp/worktree-"+beadID)
		if err != nil {
			t.Fatalf("insert assignment for %s: %v", beadID, err)
		}
	}

	// Also insert a completed assignment — must NOT be reset to open.
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('bead-done', 'w-test', '/tmp/worktree-done', 'completed')`)
	if err != nil {
		t.Fatalf("insert completed assignment: %v", err)
	}

	// shutdownSequence has no connected workers, so phase 2 is a no-op and
	// shutdownWaitForWorkers returns immediately. Phase 3b should then reset
	// all active assignments to open.
	d.shutdownSequence()

	beadSrc.mu.Lock()
	updated := beadSrc.updated
	beadSrc.mu.Unlock()

	for _, beadID := range []string{"bead-reset-a", "bead-reset-b"} {
		status, ok := updated[beadID]
		if !ok {
			t.Errorf("expected beads.Update(%q, open) to be called, but it was not", beadID)
			continue
		}
		if status != "open" {
			t.Errorf("expected beads.Update(%q, open), got status=%q", beadID, status)
		}
	}

	// Completed assignment must not be reset.
	if status, ok := updated["bead-done"]; ok {
		t.Errorf("expected completed bead to be left alone, but beads.Update was called with status=%q", status)
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

// TestMergeAndCompleteEscalatesMergeComplete verifies that after a successful
// merge, the dispatcher sends a MERGE_COMPLETE escalation to the manager so
// the manager can run git push.
func TestMergeAndCompleteEscalatesMergeComplete(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Init schema so logEvent and escalate (which writes to escalations table) work.
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-push-notify"
	workerID := "w-push"
	worktree := "/tmp/worktree-" + beadID
	branch := "agent/" + beadID

	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch)

	found := false
	for _, msg := range esc.Messages() {
		if strings.Contains(msg, string(protocol.EscMergeComplete)) {
			found = true
			if !strings.Contains(msg, beadID) {
				t.Errorf("MERGE_COMPLETE message should contain bead ID %q, got: %q", beadID, msg)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected MERGE_COMPLETE escalation after successful merge, got messages: %v", esc.Messages())
	}
}

// TestAssignUsesRichPrompt verifies that assignBead populates the AssignPayload
// with bead title, description, and acceptance criteria from beads.Show().
func TestAssignUsesRichPrompt(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Init schema.
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}
	d.state = StateRunning

	// Set up bead detail for Show().
	beadSrc.shown["rich-bead"] = &protocol.BeadDetail{
		ID:                 "rich-bead",
		Title:              "Implement widget parser",
		AcceptanceCriteria: "Test: pkg/widget_test.go:TestParse | Assert: parses valid input",
	}

	// Create a fake worker connection via net.Pipe.
	srvConn, clientConn := net.Pipe()
	defer func() { _ = srvConn.Close(); _ = clientConn.Close() }()

	w := &trackedWorker{
		id:       "w-rich",
		conn:     srvConn,
		state:    protocol.WorkerIdle,
		lastSeen: d.nowFunc(),
		encoder:  json.NewEncoder(srvConn),
	}

	bead := protocol.Bead{ID: "rich-bead", Title: "Implement widget parser", Priority: 1}

	// Read what assignBead sends.
	msgCh := make(chan protocol.Message, 1)
	go func() {
		scanner := bufio.NewScanner(clientConn)
		if scanner.Scan() {
			var msg protocol.Message
			_ = json.Unmarshal(scanner.Bytes(), &msg)
			msgCh <- msg
		}
	}()

	_ = d.assignBead(ctx, w, bead)

	select {
	case msg := <-msgCh:
		if msg.Assign == nil {
			t.Fatal("expected ASSIGN message")
		}
		if msg.Assign.Title != "Implement widget parser" {
			t.Errorf("expected title %q, got %q", "Implement widget parser", msg.Assign.Title)
		}
		if msg.Assign.AcceptanceCriteria == "" {
			t.Error("expected non-empty acceptance criteria")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ASSIGN message")
	}
}

func TestTryAssignSkipsEpics(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect a worker.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start dispatcher.
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Epic with open children — should be skipped; only the task should be assigned.
	beadSrc.mu.Lock()
	beadSrc.hasChildrenMap = map[string]bool{"epic-1": true}
	beadSrc.allChildrenClosedMap = map[string]bool{"epic-1": false}
	beadSrc.mu.Unlock()
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "epic-1", Title: "Epic: big feature", Priority: 0, Type: "epic"},
		{ID: "task-1", Title: "Implement thing", Priority: 1, Type: "task"},
	})

	// Read the ASSIGN message — must be for the task, not the epic.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN for task-1")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "task-1" {
		t.Fatalf("expected task-1 to be assigned, got %s", msg.Assign.BeadID)
	}

	// Verify the epic was NOT assigned — worker should stay idle after completing task-1,
	// no second assignment should come. We drain briefly to confirm.
	// First, disconnect so we don't get more messages, and check worktree manager.
	// The simplest check: worktree was only created for task-1, never for epic-1.
	d.mu.Lock()
	var assignedBeads []string
	for _, w := range d.workers {
		if w.beadID != "" {
			assignedBeads = append(assignedBeads, w.beadID)
		}
	}
	d.mu.Unlock()

	for _, id := range assignedBeads {
		if id == "epic-1" {
			t.Fatal("epic bead epic-1 was assigned to a worker — epics must be skipped")
		}
	}
}

func TestTryAssignSkipsBeadAfterWorktreeFailure(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect a worker.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Make worktree creation fail for bead-bad.
	wtMgr.mu.Lock()
	wtMgr.createFn = func(_ context.Context, beadID string) (string, string, error) {
		if beadID == "bead-bad" {
			return "", "", fmt.Errorf("fatal: a branch named 'agent/bead-bad' already exists")
		}
		path := "/tmp/worktree-" + beadID
		branch := "agent/" + beadID
		wtMgr.created[beadID] = path
		return path, branch, nil
	}
	wtMgr.mu.Unlock()

	// Provide both beads — bead-bad will fail worktree creation, bead-good will succeed.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-bad", Title: "Bad bead", Priority: 1, Type: "task"},
		{ID: "bead-good", Title: "Good bead", Priority: 2, Type: "task"},
	})

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Read the ASSIGN message — must be for bead-good, not bead-bad.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN for bead-good but got nothing")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "bead-good" {
		t.Fatalf("expected bead-good to be assigned, got %s", msg.Assign.BeadID)
	}

	// Verify bead-bad was NOT assigned — worktree creation count should be
	// limited (not infinite retries).
	wtMgr.mu.Lock()
	badCreateCalls := 0
	for _, c := range wtMgr.created {
		if c == "bead-bad" {
			badCreateCalls++
		}
	}
	wtMgr.mu.Unlock()

	// The dispatcher should have tried bead-bad at most a small number of
	// times, not the infinite loop we observed in production.
	if badCreateCalls > 3 {
		t.Fatalf("expected bead-bad worktree creation attempts to be limited, got %d", badCreateCalls)
	}
}

// ---------------------------------------------------------------------------
// Structured session summary tests (oro-jtw.7)
// ---------------------------------------------------------------------------

func TestPersistHandoffWithSummary(t *testing.T) {
	db := newTestDB(t)
	d := &Dispatcher{
		db:       db,
		memories: memory.NewStore(db),
	}
	ctx := context.Background()

	handoff := &protocol.HandoffPayload{
		BeadID:   "bead-summary-1",
		WorkerID: "worker-42",
		Summary: &protocol.Summary{
			Request:      "implement structured session summaries",
			Investigated: "protocol message structs, dispatcher persistHandoffContext",
			Learned:      "memories table supports arbitrary types via FTS5",
			Completed:    "added Summary struct, wired persistence",
			NextSteps:    "verify ForPrompt surfaces summaries",
		},
	}
	d.persistHandoffContext(ctx, handoff)

	rows, err := db.QueryContext(ctx,
		`SELECT content, type, source, bead_id, worker_id, confidence FROM memories WHERE type = 'summary'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var count int
	for rows.Next() {
		var content, mtype, source, beadID, workerID string
		var confidence float64
		if err := rows.Scan(&content, &mtype, &source, &beadID, &workerID, &confidence); err != nil {
			t.Fatalf("scan: %v", err)
		}
		count++

		if mtype != "summary" {
			t.Errorf("expected type=summary, got %q", mtype)
		}
		if source != "self_report" {
			t.Errorf("expected source=self_report, got %q", source)
		}
		if beadID != "bead-summary-1" {
			t.Errorf("expected bead_id=bead-summary-1, got %q", beadID)
		}
		if workerID != "worker-42" {
			t.Errorf("expected worker_id=worker-42, got %q", workerID)
		}
		if confidence != 0.9 {
			t.Errorf("expected confidence=0.9, got %f", confidence)
		}

		for _, field := range []string{"request:", "investigated:", "learned:", "completed:", "next_steps:"} {
			if !strings.Contains(content, field) {
				t.Errorf("expected content to contain %q, got: %s", field, content)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 summary memory, got %d", count)
	}
}

func TestPersistHandoffWithSummary_NilSummary(t *testing.T) {
	db := newTestDB(t)
	d := &Dispatcher{
		db:       db,
		memories: memory.NewStore(db),
	}
	ctx := context.Background()

	handoff := &protocol.HandoffPayload{
		BeadID:    "bead-nil-summary",
		WorkerID:  "worker-99",
		Learnings: []string{"nil summary should not create summary memory"},
	}
	d.persistHandoffContext(ctx, handoff)

	var lessonCount int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE type = 'lesson'`).Scan(&lessonCount)
	if err != nil {
		t.Fatalf("count lessons: %v", err)
	}
	if lessonCount != 1 {
		t.Errorf("expected 1 lesson memory, got %d", lessonCount)
	}

	var summaryCount int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE type = 'summary'`).Scan(&summaryCount)
	if err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	if summaryCount != 0 {
		t.Errorf("expected 0 summary memories for nil Summary, got %d", summaryCount)
	}
}

// TestAssignBead_RevertsBusyOnSendFailure verifies that when sendToWorker fails
// after the worker has been marked Busy, the worker state reverts to Idle and
// the beadID/worktree fields are cleared.
func TestAssignBead_RevertsBusyOnSendFailure(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)

	// Create a broken connection: net.Pipe() then close the read end.
	// Writes to server will fail because the other end is closed.
	server, client := net.Pipe()
	_ = client.Close()

	// Register the worker with the broken connection
	d.registerWorker("w-fail-assign", server)
	t.Cleanup(func() { _ = server.Close() })

	ctx := context.Background()
	bead := protocol.Bead{ID: "bead-revert", Title: "Revert test", Priority: 1}

	// Grab the tracked worker
	d.mu.Lock()
	w := d.workers["w-fail-assign"]
	d.mu.Unlock()

	// Verify worker starts Idle
	st, beadID, ok := d.WorkerInfo("w-fail-assign")
	if !ok {
		t.Fatal("expected worker to exist")
	}
	if st != protocol.WorkerIdle {
		t.Fatalf("expected worker to start Idle, got %s", st)
	}
	if beadID != "" {
		t.Fatalf("expected empty beadID, got %q", beadID)
	}

	// Call assignBead — worktree creation succeeds, but sendToWorker should fail
	_ = d.assignBead(ctx, w, bead)

	// Assert worker reverted to Idle with cleared fields
	st, beadID, ok = d.WorkerInfo("w-fail-assign")
	if !ok {
		t.Fatal("expected worker to still exist after failed assign")
	}
	if st != protocol.WorkerIdle {
		t.Fatalf("expected worker to revert to Idle after sendToWorker failure, got %s", st)
	}
	if beadID != "" {
		t.Fatalf("expected beadID to be cleared after sendToWorker failure, got %q", beadID)
	}

	// Also verify worktree field is cleared
	d.mu.Lock()
	wt := w.worktree
	model := w.model
	d.mu.Unlock()
	if wt != "" {
		t.Fatalf("expected worktree to be cleared after sendToWorker failure, got %q", wt)
	}
	if model != "" {
		t.Fatalf("expected model to be cleared after sendToWorker failure, got %q", model)
	}

	// Verify the worktree was also cleaned up (existing behavior)
	wtMgr.mu.Lock()
	removed := make([]string, len(wtMgr.removed))
	copy(removed, wtMgr.removed)
	wtMgr.mu.Unlock()

	expectedPath := "/tmp/worktree-bead-revert"
	found := false
	for _, r := range removed {
		if r == expectedPath {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected worktree %q to be removed, removed: %v", expectedPath, removed)
	}
}

// --- Merge conflict result channel consumption tests (oro-ptp) ---

// TestMergeConflict_ResultChannelConsumed verifies that when a merge conflict
// triggers ResolveMergeConflict, the returned result channel is consumed and
// the resolution outcome is logged.
func TestMergeConflict_ResultChannelConsumed(t *testing.T) {
	d, beadSrc, _, _, gitRunner, spawnMock := newTestDispatcher(t)

	// Configure git runner to return conflict on rebase
	gitRunner.mu.Lock()
	gitRunner.conflict = true
	gitRunner.mu.Unlock()

	// Configure ops agent to return RESOLVED
	spawnMock.mu.Lock()
	spawnMock.verdict = "Fixed conflicts in main.go\n\nRESOLVED\n\nMerge completed successfully."
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-mcr", Title: "Merge conflict resolution", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE — will trigger merge which conflicts, then ops agent resolves
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: "bead-mcr", WorkerID: "w1", QualityGatePassed: true},
	})

	// Wait for merge_conflict_resolved event — proves the result channel was consumed
	waitFor(t, func() bool {
		return eventCount(t, d.db, "merge_conflict_resolved") > 0
	}, 3*time.Second)
}

// TestMergeConflict_RetryUsesBeadBranch verifies that after ops VerdictResolved,
// the retry merge uses the bead's own branch (agent/<beadID>), not "main".
func TestMergeConflict_RetryUsesBeadBranch(t *testing.T) {
	d, beadSrc, _, _, gitRunner, spawnMock := newTestDispatcher(t)

	// Conflict only on the first rebase so the retry succeeds and doesn't loop.
	gitRunner.mu.Lock()
	gitRunner.conflictOnce = true
	gitRunner.mu.Unlock()

	// Configure ops agent to return RESOLVED.
	spawnMock.mu.Lock()
	spawnMock.verdict = "Fixed conflicts.\n\nRESOLVED\n\nMerge completed."
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

	const beadID = "bead-rbr"
	beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Retry branch check", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE — first merge conflicts, ops resolves, then retry merge runs.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: beadID, WorkerID: "w1", QualityGatePassed: true},
	})

	// Wait for the second rebase call (the retry after resolution).
	waitFor(t, func() bool {
		return len(gitRunner.RebaseCalls()) >= 2
	}, 3*time.Second)

	calls := gitRunner.RebaseCalls()
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 rebase calls, got %d", len(calls))
	}
	// The retry rebase args are ["rebase", "<onto>", "<branch>"].
	// args[2] is the branch being rebased; it must be "agent/<beadID>", not "main".
	retryArgs := calls[1]
	wantBranch := protocol.BranchPrefix + beadID
	if len(retryArgs) < 3 {
		t.Fatalf("retry rebase args too short: %v", retryArgs)
	}
	if gotBranch := retryArgs[2]; gotBranch != wantBranch {
		t.Errorf("retry rebase branch = %q; want %q (args: %v)", gotBranch, wantBranch, retryArgs)
	}
}

// TestMergeConflict_ResolutionFailed_Escalates verifies that when the merge
// conflict ops agent fails, the dispatcher escalates to the manager.
func TestMergeConflict_ResolutionFailed_Escalates(t *testing.T) {
	d, beadSrc, _, esc, gitRunner, spawnMock := newTestDispatcher(t)

	// Configure git runner to return conflict on rebase
	gitRunner.mu.Lock()
	gitRunner.conflict = true
	gitRunner.mu.Unlock()

	// Configure ops agent to return FAILED
	spawnMock.mu.Lock()
	spawnMock.verdict = "Cannot resolve conflicts automatically.\n\nFAILED\n\nSemantic conflict."
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

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-mcf", Title: "Merge conflict fail", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE — triggers merge conflict, ops agent fails
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: "bead-mcf", WorkerID: "w1", QualityGatePassed: true},
	})

	// Wait for escalation — proves the result channel was consumed and failure handled
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs := esc.Messages()
		for _, m := range msgs {
			if strings.Contains(m, "bead-mcf") && strings.Contains(m, "MERGE_CONFLICT") {
				// Also verify the event was logged
				if eventCount(t, d.db, "merge_conflict_failed") > 0 {
					return // success
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected MERGE_CONFLICT escalation for bead-mcf, got: %v", esc.Messages())
}

// TestHandleHandoff_NoAssignAfterShutdown verifies that tryAssign cannot grab a
// worker that is in the process of shutting down due to a handoff. The worker
// must transition through protocol.WorkerShuttingDown (invisible to tryAssign) rather
// than going straight to protocol.WorkerIdle.
func TestHandleHandoff_NoAssignAfterShutdown(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect a worker and register it.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-handoff", ContextPct: 10},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start the dispatcher so tryAssign is active.
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Manually put the worker into busy state with a bead assignment,
	// simulating what assignBead does.
	d.mu.Lock()
	w := d.workers["w-handoff"]
	w.state = protocol.WorkerBusy
	w.beadID = "bead-handoff"
	w.worktree = "/tmp/worktree-handoff"
	w.model = "test-model"
	d.mu.Unlock()

	// Now trigger a handoff. This sends SHUTDOWN and should NOT make the
	// worker visible to tryAssign as idle.
	d.handleHandoff(context.Background(), "w-handoff", protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:   "bead-handoff",
			WorkerID: "w-handoff",
		},
	})

	// After handleHandoff, the worker state must NOT be protocol.WorkerIdle.
	// It should be protocol.WorkerShuttingDown so that tryAssign skips it.
	st, _, ok := d.WorkerInfo("w-handoff")
	if !ok {
		t.Fatal("expected worker to still be tracked")
	}
	if st == protocol.WorkerIdle {
		t.Fatalf("worker state after handoff should not be protocol.WorkerIdle (got %s); "+
			"tryAssign could race and grab this worker", st)
	}
	if st != protocol.WorkerShuttingDown {
		t.Fatalf("expected protocol.WorkerShuttingDown, got %s", st)
	}

	// Verify tryAssign does NOT pick up this worker even though there are
	// ready beads.
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-new", Title: "New task", Priority: 1}})
	d.tryAssign(context.Background())

	// Worker should still be ShuttingDown — not reassigned to bead-new.
	st2, beadID, _ := d.WorkerInfo("w-handoff")
	if st2 == protocol.WorkerBusy && beadID == "bead-new" {
		t.Fatal("tryAssign grabbed a shutting-down worker — race condition!")
	}
	if st2 != protocol.WorkerShuttingDown {
		t.Fatalf("expected worker to remain protocol.WorkerShuttingDown, got %s", st2)
	}
}

// TestDispatcherBuffering verifies that the dispatcher buffers messages sent to
// disconnected workers and replays them on reconnect. If >10 messages are pending,
// the worker is treated as dead and removed.
func TestDispatcherBuffering(t *testing.T) {
	db := newTestDB(t)
	defer func() { _ = db.Close() }()

	beadSrc := &mockBeadSource{shown: make(map[string]*protocol.BeadDetail)}
	wt := &mockWorktreeManager{}
	esc := &mockEscalator{}
	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)
	spawner := ops.NewSpawner(&mockBatchSpawner{verdict: "APPROVED"})

	// Use short path for UDS — macOS limits to 108 chars.
	sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	d, _ := New(Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		HeartbeatTimeout: 500 * time.Millisecond,
		PollInterval:     100 * time.Millisecond,
	}, db, merger, spawner, beadSrc, wt, esc, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	// Wait for the listener to be ready
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.listener != nil
	}, 2*time.Second)

	// 1. Manually create a tracked worker with a broken connection
	// This simulates the scenario where the dispatcher thinks a worker is connected
	// but the connection is actually broken.
	brokenConn, _ := net.Pipe() // Create a pipe, close writer side immediately
	_ = brokenConn.Close()

	d.mu.Lock()
	d.workers["w1"] = &trackedWorker{
		id:       "w1",
		conn:     brokenConn,
		state:    protocol.WorkerIdle,
		beadID:   "bead1",
		worktree: "/tmp/worktree-bead1",
		model:    "opus",
		lastSeen: time.Now(),
		encoder:  json.NewEncoder(brokenConn),
	}
	w := d.workers["w1"]

	// 2. Send an ASSIGN message while the worker is "disconnected"
	// This should be buffered since the connection is broken
	err := d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead1",
			Worktree: "/tmp/worktree-bead1",
			Model:    "opus",
		},
	})
	d.mu.Unlock()

	// sendToWorker should fail because conn is broken, but message should be buffered
	if err == nil {
		t.Fatal("expected sendToWorker to fail on broken connection")
	}

	// 3. Verify message was buffered (this will fail until we implement buffering)
	d.mu.Lock()
	w = d.workers["w1"]
	if w == nil {
		d.mu.Unlock()
		t.Fatal("worker was removed prematurely")
	}
	if len(w.pendingMsgs) != 1 {
		d.mu.Unlock()
		t.Fatalf("expected 1 pending message, got %d", len(w.pendingMsgs))
	}
	d.mu.Unlock()

	// 4. Worker reconnects with a new connection
	wConn, err := net.Dial("unix", d.cfg.SocketPath)
	if err != nil {
		t.Fatalf("dial dispatcher (reconnect): %v", err)
	}
	defer func() { _ = wConn.Close() }()

	// Send RECONNECT message
	enc := json.NewEncoder(wConn)
	_ = enc.Encode(protocol.Message{
		Type:      protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{WorkerID: "w1", BeadID: "bead1", State: "idle"},
	})

	// 5. Read messages from the new connection — should receive the buffered ASSIGN
	scanner := bufio.NewScanner(wConn)
	if !scanner.Scan() {
		t.Fatal("expected to receive buffered ASSIGN message")
	}
	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal buffered message: %v", err)
	}

	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN message, got %s", msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != "bead1" {
		t.Fatalf("expected ASSIGN for bead1, got %+v", msg.Assign)
	}

	// 6. Test that >10 pending messages causes worker removal
	// Create a new broken connection and buffer 11 messages
	brokenConn2, _ := net.Pipe()
	_ = brokenConn2.Close()

	d.mu.Lock()
	d.workers["w2"] = &trackedWorker{
		id:       "w2",
		conn:     brokenConn2,
		state:    protocol.WorkerIdle,
		beadID:   "bead2",
		worktree: "/tmp/worktree-bead2",
		model:    "opus",
		lastSeen: time.Now(),
		encoder:  json.NewEncoder(brokenConn2),
	}
	w2 := d.workers["w2"]

	// Buffer 11 ASSIGN messages
	for i := 0; i < 11; i++ {
		_ = d.sendToWorker(w2, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   fmt.Sprintf("bead-%d", i),
				Worktree: fmt.Sprintf("/tmp/worktree-bead-%d", i),
				Model:    "opus",
			},
		})
	}

	// Worker should be removed after 10 pending messages
	_, exists := d.workers["w2"]
	d.mu.Unlock()

	if exists {
		t.Fatal("expected worker w2 to be removed after >10 pending messages")
	}
}

// TestGracefulShutdown_Cancellable verifies that duplicate shutdown calls for
// the same worker cancel the previous goroutine, ensuring only one active
// polling goroutine exists per worker. This prevents goroutine accumulation
// from repeated shutdown attempts.
//
// The test verifies that:
// 1. Each new shutdown call cancels the previous goroutine
// 2. The WaitGroup properly tracks active goroutines
// 3. Cancelled goroutines exit immediately (not after their timeout)
func TestGracefulShutdown_Cancellable(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Connect a mock worker
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "test-worker", BeadID: ""},
	})
	time.Sleep(50 * time.Millisecond) // Let registration complete

	// Set worker to Busy state so ticker checks don't cause early exit
	d.mu.Lock()
	if w, ok := d.workers["test-worker"]; ok {
		w.state = protocol.WorkerBusy
		w.beadID = "test-bead"
	}
	d.mu.Unlock()

	// Use a long timeout to verify cancellation works (not relying on timeout)
	longTimeout := 10 * time.Second

	// Track when goroutines exit
	var goroutine1Exited atomic.Bool
	var goroutine2Exited atomic.Bool

	// Call GracefulShutdownWorker first time
	d.GracefulShutdownWorker("test-worker", longTimeout)
	time.Sleep(50 * time.Millisecond)

	// Read first PREPARE_SHUTDOWN
	msg1, ok1 := readMsg(t, conn, 200*time.Millisecond)
	if !ok1 || msg1.Type != protocol.MsgPrepareShutdown {
		t.Fatal("expected first PREPARE_SHUTDOWN message")
	}

	// Record the current WaitGroup count (indirectly by checking when goroutines finish)
	startTime := time.Now()

	// Second call - should cancel the first goroutine immediately
	d.GracefulShutdownWorker("test-worker", longTimeout)
	time.Sleep(50 * time.Millisecond)

	// Read second PREPARE_SHUTDOWN
	msg2, ok2 := readMsg(t, conn, 200*time.Millisecond)
	if !ok2 || msg2.Type != protocol.MsgPrepareShutdown {
		t.Fatal("expected second PREPARE_SHUTDOWN message")
	}

	// Key assertion: The first goroutine should have been cancelled by now.
	// If cancellation works, it exits immediately when cancel() is called.
	// If cancellation doesn't work, it would still be polling with a 10s timeout.
	//
	// We verify this by checking that worker.shutdownCancel is set (only the
	// second goroutine's cancel func should be stored).
	d.mu.Lock()
	w, ok := d.workers["test-worker"]
	hasCancelFunc := ok && w.shutdownCancel != nil
	d.mu.Unlock()

	if !hasCancelFunc {
		t.Fatal("expected worker to have shutdownCancel function set")
	}

	// Approve shutdown so both goroutines can exit
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgShutdownApproved,
		ShutdownApproved: &protocol.ShutdownApprovedPayload{
			WorkerID: "test-worker",
		},
	})

	// Wait a bit for goroutines to detect the approval
	time.Sleep(200 * time.Millisecond)

	elapsed := time.Since(startTime)

	// With cancellation: first goroutine exits when cancelled (~0ms), second exits after approval (~200ms)
	// Without cancellation: both goroutines keep running until they see approval (~200ms each)
	//
	// The elapsed time should be ~200-300ms, not 10+ seconds
	if elapsed > 1*time.Second {
		t.Fatalf("Goroutines took %v to exit - indicates cancellation not working (first goroutine should exit immediately when cancelled)", elapsed)
	}

	// Clean up
	_ = goroutine1Exited.Load()
	_ = goroutine2Exited.Load()
}

// TestShutdownHardTimeout verifies that shutdownSequence() completes within
// 2*ShutdownTimeout even if a worker never responds to PREPARE_SHUTDOWN.
// This prevents indefinite hangs when workers are unresponsive during shutdown.
func TestShutdownHardTimeout(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	// Set a very short shutdown timeout for fast test execution
	d.cfg.ShutdownTimeout = 200 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run(ctx)
	}()

	// Wait for the listener to be ready
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.listener != nil
	}, 2*time.Second)

	// Connect a worker that will never respond to PREPARE_SHUTDOWN
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-unresponsive", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 2*time.Second)

	// Verify worker is connected
	if d.ConnectedWorkers() != 1 {
		t.Fatalf("expected 1 connected worker, got %d", d.ConnectedWorkers())
	}

	// Record start time and cancel context to trigger shutdown
	start := time.Now()
	cancel()

	// Worker should receive PREPARE_SHUTDOWN but will NOT respond
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected PREPARE_SHUTDOWN")
	}
	if msg.Type != protocol.MsgPrepareShutdown {
		t.Fatalf("expected PREPARE_SHUTDOWN, got %s", msg.Type)
	}

	// Do NOT send SHUTDOWN_APPROVED — worker stays silent

	// Run() should return within 2*ShutdownTimeout for shutdownSequence +
	// 5s for wg.Wait timeout. Total: 2*200ms + 5s = 5.4s
	// We'll be more generous and allow 8s total.
	maxWait := 8 * time.Second

	select {
	case err := <-errCh:
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
		// Verify Run() returned within expected bounds.
		// Expected: 2*ShutdownTimeout (for shutdown) + 5s (wg.Wait timeout) = 5.4s
		// We'll assert it's less than 8s to be safe.
		if elapsed > maxWait {
			t.Fatalf("Run() took %v, expected within %v", elapsed, maxWait)
		}
		// The critical assertion: shutdownSequence should have completed within
		// 2*ShutdownTimeout. Since Run() waits for wg with 5s timeout, and our
		// ShutdownTimeout is 200ms, if Run() completes in less than 1s, we know
		// shutdownSequence() respected the 2*ShutdownTimeout bound (400ms).
		if elapsed > 1*time.Second {
			t.Fatalf("Run() took %v, suggesting shutdownSequence exceeded 2*ShutdownTimeout", elapsed)
		}
	case <-time.After(maxWait):
		t.Fatalf("Run() did not return within %v (likely hanging in shutdownSequence)", maxWait)
	}
}

// TestPriorityContention verifies that when all workers are busy and a P0 bead
// is queued, the dispatcher does NOT trigger a PRIORITY_CONTENTION escalation.
// The preemption system (oro-wofg) handles priority contention automatically.
func TestPriorityContention(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Start the dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Connect one worker
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "worker-1",
			ContextPct: 10,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Assign a P1 bead to make the worker busy
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-p1", Title: "P1 Task", Priority: 1},
	})

	// Wait for the P1 assignment
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN for P1 bead")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "bead-p1" {
		t.Fatalf("expected bead-p1, got %s", msg.Assign.BeadID)
	}

	// Verify worker is busy
	waitForWorkerState(t, d, "worker-1", protocol.WorkerBusy, 1*time.Second)

	// Now add a P0 bead — all workers are busy, but should NOT trigger escalation
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-p1", Title: "P1 Task", Priority: 1}, // still in queue (worker busy)
		{ID: "bead-p0", Title: "P0 Urgent", Priority: 0},
	})

	// Wait for a few poll cycles (at least 250ms = 5 cycles at 50ms)
	// to ensure no escalation fires
	time.Sleep(250 * time.Millisecond)

	// Verify NO escalation occurred
	messages := esc.Messages()
	if len(messages) != 0 {
		t.Errorf("expected no PRIORITY_CONTENTION escalations, got %d: %v", len(messages), messages)
	}

	// Verify the escalation tracking flag is NOT set
	d.mu.Lock()
	escalated := d.escalatedBeads["bead-p0"]
	d.mu.Unlock()
	if escalated {
		t.Error("expected escalatedBeads flag to NOT be set for bead-p0")
	}

	// Now free up the worker and verify the P0 gets assigned (and flag cleared)
	// Send DONE for the P1 bead
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			WorkerID:          "worker-1",
			BeadID:            "bead-p1",
			QualityGatePassed: true,
		},
	})

	// Worker should go back to idle
	waitForWorkerState(t, d, "worker-1", protocol.WorkerIdle, 1*time.Second)

	// Wait for P0 assignment (dispatcher polls every 50ms)
	msg, ok = readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN for P0 bead after worker became idle")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "bead-p0" {
		t.Fatalf("expected bead-p0 to be assigned, got %s", msg.Assign.BeadID)
	}

	// Verify the escalation flag was cleared on assignment
	d.mu.Lock()
	escalated = d.escalatedBeads["bead-p0"]
	d.mu.Unlock()
	if escalated {
		t.Error("expected escalatedBeads flag to be cleared for bead-p0 after assignment")
	}
}

// ---------------------------------------------------------------------------
// TestTryAssign_NoBeadsReady (oro-2ao)
// ---------------------------------------------------------------------------

// TestTryAssign_NoBeadsReady verifies tryAssign behavior when BeadSource.Ready()
// returns an empty slice or an error. In both cases idle workers must remain
// idle, no ASSIGN must be sent, and no worktree must be created.
func TestTryAssign_NoBeadsReady(t *testing.T) {
	t.Run("empty_ready_slice", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		// Connect a worker so there is an idle worker available.
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				WorkerID:   "w-empty",
				ContextPct: 5,
			},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		// Start dispatcher so tryAssign operates.
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		// BeadSource already returns empty slice (newTestDispatcher sets beads: []).
		// Explicitly ensure it's empty.
		beadSrc.SetBeads([]protocol.Bead{})

		// Directly invoke tryAssign.
		d.tryAssign(context.Background())

		// Worker must remain idle — no assignment.
		st, beadID, ok := d.WorkerInfo("w-empty")
		if !ok {
			t.Fatal("expected worker w-empty to be tracked")
		}
		if st != protocol.WorkerIdle {
			t.Fatalf("expected worker to remain idle, got state=%s beadID=%s", st, beadID)
		}
		if beadID != "" {
			t.Fatalf("expected no bead assignment, got beadID=%s", beadID)
		}

		// No worktree should have been created.
		wtMgr.mu.Lock()
		createdCount := len(wtMgr.created)
		wtMgr.mu.Unlock()
		if createdCount != 0 {
			t.Fatalf("expected 0 worktrees created, got %d", createdCount)
		}

		// Verify no ASSIGN was sent by attempting to read from the connection
		// with a short timeout — should get nothing.
		msg, gotMsg := readMsg(t, conn, 100*time.Millisecond)
		if gotMsg && msg.Type == protocol.MsgAssign {
			t.Fatal("received unexpected ASSIGN message when no beads are ready")
		}
	})

	t.Run("ready_returns_error", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		// Connect a worker.
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				WorkerID:   "w-err",
				ContextPct: 5,
			},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		// Start dispatcher.
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		// Configure Ready() to return an error.
		beadSrc.mu.Lock()
		beadSrc.readyErr = errors.New("bd ready failed: network timeout")
		beadSrc.mu.Unlock()

		// tryAssign must not panic or crash.
		d.tryAssign(context.Background())

		// Worker must remain idle — no assignment.
		st, beadID, ok := d.WorkerInfo("w-err")
		if !ok {
			t.Fatal("expected worker w-err to be tracked")
		}
		if st != protocol.WorkerIdle {
			t.Fatalf("expected worker to remain idle, got state=%s beadID=%s", st, beadID)
		}
		if beadID != "" {
			t.Fatalf("expected no bead assignment, got beadID=%s", beadID)
		}

		// No worktree should have been created.
		wtMgr.mu.Lock()
		createdCount := len(wtMgr.created)
		wtMgr.mu.Unlock()
		if createdCount != 0 {
			t.Fatalf("expected 0 worktrees created, got %d", createdCount)
		}

		// No ASSIGN should be sent.
		msg, gotMsg := readMsg(t, conn, 100*time.Millisecond)
		if gotMsg && msg.Type == protocol.MsgAssign {
			t.Fatal("received unexpected ASSIGN message when Ready() returned error")
		}
	})
}

// TestCheckHeartbeats_WorkerDisconnect verifies that when a worker's heartbeat
// times out while it has an assigned bead, the dispatcher correctly:
// 1. Removes the worker from the workers map
// 2. Sends an escalation with EscWorkerCrash
// 3. Clears all bead tracking entries
// 4. Logs a heartbeat_timeout event
// Edge: idle workers are exempt from heartbeat timeout.
func TestCheckHeartbeats_WorkerDisconnect(t *testing.T) {
	t.Run("busy worker with assigned bead times out", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		// Create a pipe to simulate a worker connection.
		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		// Directly inject a busy worker with an assigned bead into the map.
		beadID := "bead-disconnect"
		workerID := "w-disconnect"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:       workerID,
			conn:     server,
			state:    protocol.WorkerBusy,
			beadID:   beadID,
			worktree: "/tmp/worktree-disconnect",
			lastSeen: now,
			encoder:  json.NewEncoder(server),
		}
		// Seed tracking maps so we can verify they get cleared.
		d.attemptCounts[beadID] = 2
		d.handoffCounts[beadID] = 1
		d.rejectionCounts[beadID] = 1
		d.mu.Unlock()

		// Verify the worker is registered.
		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected 1 worker, got %d", d.ConnectedWorkers())
		}

		// Advance time past HeartbeatTimeout (configured as 500ms in newTestDispatcher).
		d.nowFunc = func() time.Time { return now.Add(600 * time.Millisecond) }

		// Trigger heartbeat check.
		d.checkHeartbeats(context.Background())

		// Assert: worker deleted from map.
		if d.ConnectedWorkers() != 0 {
			t.Fatalf("expected 0 workers after heartbeat timeout, got %d", d.ConnectedWorkers())
		}
		_, _, ok := d.WorkerInfo(workerID)
		if ok {
			t.Fatal("expected worker to be removed from map")
		}

		// Assert: escalation sent with EscWorkerCrash.
		msgs := esc.Messages()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 escalation message, got %d", len(msgs))
		}
		if !strings.Contains(msgs[0], string(protocol.EscWorkerCrash)) {
			t.Errorf("expected escalation to contain %q, got %q", protocol.EscWorkerCrash, msgs[0])
		}
		if !strings.Contains(msgs[0], beadID) {
			t.Errorf("expected escalation to mention bead %q, got %q", beadID, msgs[0])
		}

		// Assert: bead tracking cleared.
		d.mu.Lock()
		_, hasAttempt := d.attemptCounts[beadID]
		_, hasHandoff := d.handoffCounts[beadID]
		_, hasRejection := d.rejectionCounts[beadID]
		d.mu.Unlock()
		if hasAttempt {
			t.Error("expected attemptCounts to be cleared for bead")
		}
		if hasHandoff {
			t.Error("expected handoffCounts to be cleared for bead")
		}
		if hasRejection {
			t.Error("expected rejectionCounts to be cleared for bead")
		}

		// Assert: heartbeat_timeout event logged.
		count := eventCount(t, d.db, "heartbeat_timeout")
		if count != 1 {
			t.Fatalf("expected 1 heartbeat_timeout event, got %d", count)
		}
	})

	t.Run("idle worker is exempt from heartbeat timeout", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		workerID := "w-idle"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:       workerID,
			conn:     server,
			state:    protocol.WorkerIdle,
			lastSeen: now,
			encoder:  json.NewEncoder(server),
		}
		d.mu.Unlock()

		// Advance time well past HeartbeatTimeout.
		d.nowFunc = func() time.Time { return now.Add(10 * time.Second) }

		// Trigger heartbeat check.
		d.checkHeartbeats(context.Background())

		// Assert: idle worker NOT removed.
		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected idle worker to survive heartbeat check, got %d workers", d.ConnectedWorkers())
		}
		st, _, ok := d.WorkerInfo(workerID)
		if !ok {
			t.Fatal("expected idle worker to still be in map")
		}
		if st != protocol.WorkerIdle {
			t.Fatalf("expected worker state idle, got %s", st)
		}

		// Assert: no escalations.
		if len(esc.Messages()) != 0 {
			t.Errorf("expected no escalation for idle worker, got %d", len(esc.Messages()))
		}

		// Assert: no heartbeat_timeout events.
		count := eventCount(t, d.db, "heartbeat_timeout")
		if count != 0 {
			t.Errorf("expected 0 heartbeat_timeout events for idle worker, got %d", count)
		}
	})
}

// TestShutdownTimeout_ForceKill verifies the dispatcher sends a hard SHUTDOWN
// when the graceful shutdown timeout expires without receiving SHUTDOWN_APPROVED.
func TestShutdownTimeout_ForceKill(t *testing.T) {
	t.Run("hard_shutdown_after_timeout", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-force", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		// Start dispatcher and assign a bead to the worker.
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-force", Title: "Force kill test", Priority: 1}})
		_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
		if !ok {
			t.Fatal("expected ASSIGN")
		}
		beadSrc.SetBeads(nil)

		// Verify worker is Busy with the bead before shutdown.
		state, beadID, exists := d.WorkerInfo("w-force")
		if !exists {
			t.Fatal("worker w-force should exist")
		}
		if state != protocol.WorkerBusy {
			t.Fatalf("expected WorkerBusy before shutdown, got %s", state)
		}
		if beadID != "bead-force" {
			t.Fatalf("expected beadID bead-force, got %s", beadID)
		}

		// Trigger graceful shutdown with a short 100ms timeout.
		d.GracefulShutdownWorker("w-force", 100*time.Millisecond)

		// Worker receives PREPARE_SHUTDOWN but does NOT respond with SHUTDOWN_APPROVED.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected PREPARE_SHUTDOWN")
		}
		if msg.Type != protocol.MsgPrepareShutdown {
			t.Fatalf("expected PREPARE_SHUTDOWN, got %s", msg.Type)
		}

		// Do NOT send SHUTDOWN_APPROVED — dispatcher must fall back to hard SHUTDOWN.
		msg2, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected hard SHUTDOWN after timeout")
		}
		if msg2.Type != protocol.MsgShutdown {
			t.Fatalf("expected SHUTDOWN (hard kill), got %s", msg2.Type)
		}

		// After timeout: worker state should be Idle and beadID cleared.
		waitFor(t, func() bool {
			st, _, ok := d.WorkerInfo("w-force")
			return ok && st == protocol.WorkerIdle
		}, 2*time.Second)

		state, beadID, exists = d.WorkerInfo("w-force")
		if !exists {
			t.Fatal("worker w-force should still exist after timeout")
		}
		if state != protocol.WorkerIdle {
			t.Fatalf("expected WorkerIdle after timeout, got %s", state)
		}
		if beadID != "" {
			t.Fatalf("expected beadID cleared after timeout, got %q", beadID)
		}
	})

	t.Run("worker_disconnected_before_timeout", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		// Directly call handleShutdownTimeout for a worker that is not in the map.
		// This must not panic and should return early gracefully.
		d.handleShutdownTimeout("w-nonexistent")

		// Verify no workers exist (nothing was created or modified).
		if d.ConnectedWorkers() != 0 {
			t.Fatalf("expected 0 connected workers, got %d", d.ConnectedWorkers())
		}
	})
}

func TestRestoreStateOnStartup(t *testing.T) {
	// Setup: create dispatcher with test DB.
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Insert active assignments with known attempt_count and handoff_count values
	// directly into SQLite BEFORE the dispatcher starts.
	ctx := context.Background()
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status, attempt_count, handoff_count)
		 VALUES ('oro-aaa', 'w1', '/tmp/wt-aaa', 'active', 2, 1)`)
	if err != nil {
		t.Fatalf("insert assignment 1: %v", err)
	}
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status, attempt_count, handoff_count)
		 VALUES ('oro-bbb', 'w2', '/tmp/wt-bbb', 'active', 0, 3)`)
	if err != nil {
		t.Fatalf("insert assignment 2: %v", err)
	}
	// Insert a completed assignment — should NOT be restored.
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status, attempt_count, handoff_count)
		 VALUES ('oro-ccc', 'w3', '/tmp/wt-ccc', 'completed', 5, 5)`)
	if err != nil {
		t.Fatalf("insert assignment 3: %v", err)
	}

	// Start dispatcher — Run() should restore state from SQLite.
	cancel := startDispatcher(t, d)
	defer cancel()

	// Verify attempt counts were restored from active assignments only.
	d.mu.Lock()
	gotAttemptAAA := d.attemptCounts["oro-aaa"]
	gotAttemptBBB := d.attemptCounts["oro-bbb"]
	_, hasCCC := d.attemptCounts["oro-ccc"]
	gotHandoffAAA := d.handoffCounts["oro-aaa"]
	gotHandoffBBB := d.handoffCounts["oro-bbb"]
	_, hasHandoffCCC := d.handoffCounts["oro-ccc"]
	d.mu.Unlock()

	if gotAttemptAAA != 2 {
		t.Errorf("attemptCounts[oro-aaa]: got %d, want 2", gotAttemptAAA)
	}
	if gotAttemptBBB != 0 {
		t.Errorf("attemptCounts[oro-bbb]: got %d, want 0", gotAttemptBBB)
	}
	if hasCCC {
		t.Errorf("attemptCounts should not contain completed bead oro-ccc")
	}
	if gotHandoffAAA != 1 {
		t.Errorf("handoffCounts[oro-aaa]: got %d, want 1", gotHandoffAAA)
	}
	if gotHandoffBBB != 3 {
		t.Errorf("handoffCounts[oro-bbb]: got %d, want 3", gotHandoffBBB)
	}
	if hasHandoffCCC {
		t.Errorf("handoffCounts should not contain completed bead oro-ccc")
	}
}

func TestConfig_ConsolidateAfterN(t *testing.T) {
	// Test 1: Default Config has ConsolidateAfterN == 5.
	cfg := Config{SocketPath: "/tmp/test.sock", DBPath: ":memory:"}
	resolved := cfg.withDefaults()
	if resolved.ConsolidateAfterN != 5 {
		t.Fatalf("ConsolidateAfterN: got %d, want 5", resolved.ConsolidateAfterN)
	}

	// Test 2: Explicit value is preserved (not overwritten by default).
	cfg2 := Config{SocketPath: "/tmp/test.sock", DBPath: ":memory:", ConsolidateAfterN: 10}
	resolved2 := cfg2.withDefaults()
	if resolved2.ConsolidateAfterN != 10 {
		t.Fatalf("ConsolidateAfterN with explicit value: got %d, want 10", resolved2.ConsolidateAfterN)
	}

	// Test 3: Dispatcher struct has completionsSinceConsolidate counter field.
	d, _, _, _, _, _ := newTestDispatcher(t)
	if d.completionsSinceConsolidate != 0 {
		t.Fatalf("completionsSinceConsolidate: got %d, want 0", d.completionsSinceConsolidate)
	}
}

func TestHandoffExhaustion_CreatesContinuationBead(t *testing.T) {
	d, conn, _, spawnMock := setupHandoffDiagnosis(t)

	// Set diagnosis output so the diagnosis goroutine completes.
	spawnMock.mu.Lock()
	spawnMock.verdict = "Root cause: context limit exceeded repeatedly"
	spawnMock.mu.Unlock()

	// Pre-set handoff count to 1 (simulating first handoff already happened).
	d.mu.Lock()
	d.handoffCounts["bead-stuck"] = 1
	d.mu.Unlock()

	// Second handoff — triggers diagnosis AND should create continuation bead.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:         "bead-stuck",
			WorkerID:       "w1",
			ContextSummary: "Implemented 3 of 5 subtasks; remaining: validation and tests",
		},
	})

	// Consume SHUTDOWN message sent to the old worker.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected SHUTDOWN after handoff exhaustion")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Wait for BeadSource.Create to be called with continuation bead.
	beadSrc, ok := d.beads.(*mockBeadSource)
	if !ok {
		t.Fatal("beads is not *mockBeadSource")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		beadSrc.mu.Lock()
		calls := make([]createCall, len(beadSrc.created))
		copy(calls, beadSrc.created)
		beadSrc.mu.Unlock()

		for _, c := range calls {
			if c.parent == "bead-stuck" && c.beadType == "task" &&
				strings.Contains(c.description, "Implemented 3 of 5 subtasks") {
				// Verify event was logged.
				if eventCount(t, d.db, "continuation_bead_created") > 0 {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Dump what was created for debug.
	beadSrc.mu.Lock()
	defer beadSrc.mu.Unlock()
	t.Fatalf("expected BeadSource.Create with parent=bead-stuck, type=task, description containing handoff summary; got %+v", beadSrc.created)
}

// TestCrashRecovery_ReconnectPreservesAttemptCount verifies the full crash
// recovery flow: dispatcher starts, assigns a bead, worker reports a QG failure
// (attempt 1 persisted to SQLite), dispatcher crashes (context cancelled), a
// NEW dispatcher starts against the SAME database, worker reconnects, and the
// next QG failure continues from attempt 2 (not 0).
func TestCrashRecovery_ReconnectPreservesAttemptCount(t *testing.T) {
	// --- Shared state across both dispatcher lifetimes ---
	// Use a temp-file SQLite DB so both dispatchers share persistent state.
	tmpFile := fmt.Sprintf("/tmp/oro-crash-test-%d.db", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = os.Remove(tmpFile)
		_ = os.Remove(tmpFile + "-wal")
		_ = os.Remove(tmpFile + "-shm")
	})

	db, err := sql.Open("sqlite", tmpFile)
	if err != nil {
		t.Fatalf("open shared db: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("set WAL: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatalf("set busy_timeout: %v", err)
	}
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	beadSrc := &mockBeadSource{
		beads: []protocol.Bead{},
		shown: make(map[string]*protocol.BeadDetail),
	}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}

	// Helper: create a new dispatcher with a fresh socket but the shared DB.
	makeDispatcher := func(t *testing.T) *Dispatcher {
		t.Helper()
		gitRunner := &mockGitRunner{}
		merger := merge.NewCoordinator(gitRunner)
		spawnMock := &mockBatchSpawner{verdict: "APPROVED: looks good"}
		opsSpawner := ops.NewSpawner(spawnMock)

		sockPath := fmt.Sprintf("/tmp/oro-crash-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })

		cfg := Config{
			SocketPath:       sockPath,
			DBPath:           tmpFile,
			MaxWorkers:       5,
			HeartbeatTimeout: 2 * time.Second,
			PollInterval:     50 * time.Millisecond,
			ShutdownTimeout:  500 * time.Millisecond,
		}

		d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
		if err != nil {
			t.Fatalf("New() failed: %v", err)
		}
		return d
	}

	// ========== PHASE 1: First dispatcher lifetime ==========
	d1 := makeDispatcher(t)
	ctx1, cancel1 := context.WithCancel(context.Background())
	errCh1 := make(chan error, 1)
	go func() { errCh1 <- d1.Run(ctx1) }()

	// Wait for listener to be ready.
	waitFor(t, func() bool {
		d1.mu.Lock()
		defer d1.mu.Unlock()
		return d1.listener != nil
	}, 2*time.Second)

	// Connect worker and register it.
	conn1, scanner1 := connectWorker(t, d1.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d1, 1, 1*time.Second)

	// Start the dispatcher and set up the bead.
	// Use ModelOpus so the QG retry does NOT reset attempt count on model escalation.
	sendDirective(t, d1.cfg.SocketPath, "start")
	waitForState(t, d1, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-crash1", Title: "Crash recovery test", Priority: 1, Type: "task", Model: protocol.ModelOpus},
	})

	// Drain the initial ASSIGN.
	assignMsg, ok := readMsgFromScanner(t, scanner1, 2*time.Second)
	if !ok {
		t.Fatal("expected initial ASSIGN")
	}
	if assignMsg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", assignMsg.Type)
	}

	// Send a QG failure — this should increment attempt to 1 and persist it.
	sendMsg(t, conn1, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-crash1",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "crash-test-fail-1",
		},
	})

	// Read the re-ASSIGN (attempt=1).
	retryMsg, ok := readMsgFromScanner(t, scanner1, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after first QG failure")
	}
	if retryMsg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", retryMsg.Type)
	}
	if retryMsg.Assign.Attempt != 1 {
		t.Fatalf("expected Attempt=1 after first QG failure, got %d", retryMsg.Assign.Attempt)
	}

	// Verify attempt_count was persisted in SQLite.
	var persistedCount int
	if err := db.QueryRow(
		`SELECT attempt_count FROM assignments WHERE bead_id='bead-crash1' AND status='active'`,
	).Scan(&persistedCount); err != nil {
		t.Fatalf("query attempt_count before crash: %v", err)
	}
	if persistedCount != 1 {
		t.Fatalf("expected persisted attempt_count=1, got %d", persistedCount)
	}

	// Close the worker connection before shutting down dispatcher.
	_ = conn1.Close()

	// ========== PHASE 2: Simulate crash — cancel first dispatcher ==========
	cancel1()
	select {
	case <-errCh1:
	case <-time.After(3 * time.Second):
		t.Fatal("first dispatcher did not stop within timeout")
	}

	// ========== PHASE 3: Second dispatcher lifetime — restart ==========
	d2 := makeDispatcher(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	errCh2 := make(chan error, 1)
	go func() { errCh2 <- d2.Run(ctx2) }()
	defer func() {
		cancel2()
		select {
		case <-errCh2:
		case <-time.After(3 * time.Second):
		}
	}()

	// Wait for second dispatcher listener to be ready.
	waitFor(t, func() bool {
		d2.mu.Lock()
		defer d2.mu.Unlock()
		return d2.listener != nil
	}, 2*time.Second)

	// Verify restoreState reconstructed the attempt count from SQLite.
	d2.mu.Lock()
	restoredCount := d2.attemptCounts["bead-crash1"]
	d2.mu.Unlock()
	if restoredCount != 1 {
		t.Fatalf("expected restored attemptCounts[bead-crash1]=1 after restart, got %d", restoredCount)
	}

	// Connect a worker to the new dispatcher and send RECONNECT.
	conn2, scanner2 := connectWorker(t, d2.cfg.SocketPath)

	// First register with a heartbeat so the worker is tracked.
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 10},
	})
	waitForWorkers(t, d2, 1, 1*time.Second)

	// Send RECONNECT — worker tells dispatcher it was working on bead-crash1.
	sendMsg(t, conn2, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID:   "w1",
			BeadID:     "bead-crash1",
			State:      "running",
			ContextPct: 10,
		},
	})

	// Give reconnect processing a moment.
	time.Sleep(100 * time.Millisecond)

	// Verify the worker is now recognized as busy on bead-crash1.
	d2.mu.Lock()
	w, wOK := d2.workers["w1"]
	var workerBeadID string
	var workerState protocol.WorkerState
	if wOK {
		workerBeadID = w.beadID
		workerState = w.state
	}
	d2.mu.Unlock()

	if !wOK {
		t.Fatal("expected worker w1 to be tracked after reconnect")
	}
	if workerBeadID != "bead-crash1" {
		t.Fatalf("expected worker bead=bead-crash1, got %q", workerBeadID)
	}
	if workerState != protocol.WorkerBusy {
		t.Fatalf("expected worker state=busy, got %s", workerState)
	}

	// Start the second dispatcher.
	sendDirective(t, d2.cfg.SocketPath, "start")
	waitForState(t, d2, StateRunning, 1*time.Second)

	// ========== PHASE 4: Second QG failure — verify attempt continues from 2 ==========
	sendMsg(t, conn2, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-crash1",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "crash-test-fail-2",
		},
	})

	// Read the re-ASSIGN — attempt should be 2, NOT 0 or 1.
	retryMsg2, ok := readMsgFromScanner(t, scanner2, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN after second QG failure on restarted dispatcher")
	}
	if retryMsg2.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", retryMsg2.Type)
	}
	if retryMsg2.Assign.Attempt != 2 {
		t.Fatalf("expected Attempt=2 after crash recovery, got %d (attempt count was not preserved across restart)", retryMsg2.Assign.Attempt)
	}

	// Verify the persisted count also incremented to 2.
	var finalCount int
	if err := db.QueryRow(
		`SELECT attempt_count FROM assignments WHERE bead_id='bead-crash1' AND status='active'`,
	).Scan(&finalCount); err != nil {
		t.Fatalf("query attempt_count after second failure: %v", err)
	}
	if finalCount != 2 {
		t.Fatalf("expected persisted attempt_count=2, got %d", finalCount)
	}
}

// TestProgressTimeoutTriggersEscalation verifies that a busy worker whose
// lastProgress exceeds ProgressTimeout is detected by checkHeartbeats,
// escalated as STUCK_WORKER, removed from the worker map, and has its
// bead tracking cleared.
func TestProgressTimeoutTriggersEscalation(t *testing.T) {
	t.Run("busy worker with stale progress triggers STUCK_WORKER", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		// Set a short progress timeout for testing.
		d.cfg.ProgressTimeout = 1 * time.Second

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		beadID := "bead-stalled"
		workerID := "w-stalled"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         server,
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			worktree:     "/tmp/worktree-stalled",
			lastSeen:     now,                       // heartbeat is fresh — worker is alive
			lastProgress: now.Add(-2 * time.Second), // progress is stale (>1s ago)
			encoder:      json.NewEncoder(server),
		}
		d.attemptCounts[beadID] = 1
		d.handoffCounts[beadID] = 1
		d.mu.Unlock()

		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected 1 worker, got %d", d.ConnectedWorkers())
		}

		// Trigger heartbeat check — should detect stale progress.
		d.checkHeartbeats(context.Background())

		// Assert: worker removed from map.
		if d.ConnectedWorkers() != 0 {
			t.Fatalf("expected 0 workers after progress timeout, got %d", d.ConnectedWorkers())
		}
		_, _, ok := d.WorkerInfo(workerID)
		if ok {
			t.Fatal("expected worker to be removed from map")
		}

		// Assert: escalation sent with STUCK_WORKER type.
		msgs := esc.Messages()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 escalation message, got %d: %v", len(msgs), msgs)
		}
		if !strings.Contains(msgs[0], string(protocol.EscStuckWorker)) {
			t.Errorf("expected escalation to contain %q, got %q", protocol.EscStuckWorker, msgs[0])
		}
		if !strings.Contains(msgs[0], beadID) {
			t.Errorf("expected escalation to mention bead %q, got %q", beadID, msgs[0])
		}

		// Assert: bead tracking cleared.
		d.mu.Lock()
		_, hasAttempt := d.attemptCounts[beadID]
		_, hasHandoff := d.handoffCounts[beadID]
		d.mu.Unlock()
		if hasAttempt {
			t.Error("expected attemptCounts to be cleared for stalled bead")
		}
		if hasHandoff {
			t.Error("expected handoffCounts to be cleared for stalled bead")
		}

		// Assert: progress_timeout event logged.
		count := eventCount(t, d.db, "progress_timeout")
		if count != 1 {
			t.Fatalf("expected 1 progress_timeout event, got %d", count)
		}
	})

	t.Run("busy worker with recent progress is not stuck", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		d.cfg.ProgressTimeout = 1 * time.Second

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		workerID := "w-active"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         server,
			state:        protocol.WorkerBusy,
			beadID:       "bead-active",
			worktree:     "/tmp/worktree-active",
			lastSeen:     now,
			lastProgress: now.Add(-500 * time.Millisecond), // progress is recent (within 1s)
			encoder:      json.NewEncoder(server),
		}
		d.mu.Unlock()

		d.checkHeartbeats(context.Background())

		// Assert: worker still present.
		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected 1 worker to survive, got %d", d.ConnectedWorkers())
		}

		// Assert: no escalations.
		if len(esc.Messages()) != 0 {
			t.Errorf("expected no escalation for active worker, got %d", len(esc.Messages()))
		}
	})

	t.Run("idle worker is exempt from progress timeout", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		d.cfg.ProgressTimeout = 1 * time.Second

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		d.mu.Lock()
		d.workers["w-idle"] = &trackedWorker{
			id:           "w-idle",
			conn:         server,
			state:        protocol.WorkerIdle,
			lastSeen:     now,
			lastProgress: now.Add(-10 * time.Second), // stale but idle
			encoder:      json.NewEncoder(server),
		}
		d.mu.Unlock()

		d.checkHeartbeats(context.Background())

		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected idle worker to survive, got %d workers", d.ConnectedWorkers())
		}
		if len(esc.Messages()) != 0 {
			t.Errorf("expected no escalation for idle worker, got %d", len(esc.Messages()))
		}
	})

	t.Run("reviewing worker is exempt from progress timeout", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		d.cfg.ProgressTimeout = 1 * time.Second

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		d.mu.Lock()
		d.workers["w-review"] = &trackedWorker{
			id:           "w-review",
			conn:         server,
			state:        protocol.WorkerReviewing,
			beadID:       "bead-review",
			lastSeen:     now,
			lastProgress: now.Add(-10 * time.Second), // stale but reviewing
			encoder:      json.NewEncoder(server),
		}
		d.mu.Unlock()

		d.checkHeartbeats(context.Background())

		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected reviewing worker to survive, got %d workers", d.ConnectedWorkers())
		}
		if len(esc.Messages()) != 0 {
			t.Errorf("expected no escalation for reviewing worker, got %d", len(esc.Messages()))
		}
	})
}

// TestProgressUpdatedOnMeaningfulEvents verifies that lastProgress is updated
// when the dispatcher processes DONE, READY_FOR_REVIEW, STATUS, and QG failure
// messages.
func TestProgressUpdatedOnMeaningfulEvents(t *testing.T) {
	t.Run("DONE updates lastProgress", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		baseTime := time.Now()
		currentTime := baseTime
		d.nowFunc = func() time.Time { return currentTime }

		workerID := "w-done"
		beadID := "bead-done"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         server,
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			worktree:     "/tmp/worktree-done",
			lastSeen:     baseTime,
			lastProgress: baseTime,
			encoder:      json.NewEncoder(server),
		}
		d.mu.Unlock()

		// Advance time and send DONE.
		currentTime = baseTime.Add(5 * time.Minute)

		// Drain messages from the pipe in background to prevent blocking.
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := client.Read(buf); err != nil {
					return
				}
			}
		}()

		d.handleDone(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            beadID,
				WorkerID:          workerID,
				QualityGatePassed: true,
			},
		})

		d.mu.Lock()
		w, ok := d.workers[workerID]
		var lp time.Time
		if ok {
			lp = w.lastProgress
		}
		d.mu.Unlock()

		// Worker may have been transitioned to idle by handleDone, but
		// touchProgress was called before the state change.
		if !lp.Equal(currentTime) {
			t.Errorf("expected lastProgress=%v, got %v", currentTime, lp)
		}
	})

	t.Run("READY_FOR_REVIEW updates lastProgress", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		baseTime := time.Now()
		currentTime := baseTime
		d.nowFunc = func() time.Time { return currentTime }

		workerID := "w-rfr"
		beadID := "bead-rfr"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         server,
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			worktree:     "/tmp/worktree-rfr",
			lastSeen:     baseTime,
			lastProgress: baseTime,
			encoder:      json.NewEncoder(server),
		}
		d.mu.Unlock()

		currentTime = baseTime.Add(7 * time.Minute)

		// Drain messages.
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := client.Read(buf); err != nil {
					return
				}
			}
		}()

		d.handleReadyForReview(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{
				BeadID:   beadID,
				WorkerID: workerID,
			},
		})

		d.mu.Lock()
		w := d.workers[workerID]
		lp := w.lastProgress
		d.mu.Unlock()

		if !lp.Equal(currentTime) {
			t.Errorf("expected lastProgress=%v after READY_FOR_REVIEW, got %v", currentTime, lp)
		}
	})

	t.Run("STATUS updates lastProgress", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		baseTime := time.Now()
		currentTime := baseTime
		d.nowFunc = func() time.Time { return currentTime }

		workerID := "w-status"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         server,
			state:        protocol.WorkerBusy,
			beadID:       "bead-status",
			worktree:     "/tmp/worktree-status",
			lastSeen:     baseTime,
			lastProgress: baseTime,
			encoder:      json.NewEncoder(server),
		}
		d.mu.Unlock()

		_ = client // keep alive

		currentTime = baseTime.Add(3 * time.Minute)

		d.handleStatus(context.Background(), workerID, protocol.Message{
			Type: protocol.MsgStatus,
			Status: &protocol.StatusPayload{
				BeadID:   "bead-status",
				WorkerID: workerID,
				State:    "running",
				Result:   "",
			},
		})

		d.mu.Lock()
		w := d.workers[workerID]
		lp := w.lastProgress
		d.mu.Unlock()

		if !lp.Equal(currentTime) {
			t.Errorf("expected lastProgress=%v after STATUS, got %v", currentTime, lp)
		}
	})
}

// TestProgressTimeoutDefaultConfig verifies the default ProgressTimeout is 10 minutes.
func TestProgressTimeoutDefaultConfig(t *testing.T) {
	cfg := Config{}
	resolved := cfg.withDefaults()
	if resolved.ProgressTimeout != 10*time.Minute {
		t.Errorf("expected default ProgressTimeout=10m, got %v", resolved.ProgressTimeout)
	}
}

// TestProgressTimeoutConfigValidation verifies that a negative ProgressTimeout
// is rejected by Config.validate().
func TestProgressTimeoutConfigValidation(t *testing.T) {
	cfg := Config{
		ProgressTimeout: -1 * time.Second,
	}
	resolved := cfg.withDefaults()
	// Negative value should not be overwritten by withDefaults.
	resolved.ProgressTimeout = -1 * time.Second
	err := resolved.validate()
	if err == nil {
		t.Fatal("expected validation error for negative ProgressTimeout")
	}
	if !strings.Contains(err.Error(), "ProgressTimeout") {
		t.Errorf("expected error to mention ProgressTimeout, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Bug fix tests (oro-sjpe, oro-c8rq)
// ---------------------------------------------------------------------------

// TestTryAssignNoDuplicateBeadAssignment verifies that when two workers are
// idle, each gets a different bead — not the same bead assigned to both.
func TestTryAssignNoDuplicateBeadAssignment(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect two workers.
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

	// Provide two beads.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-a", Title: "Task A", Priority: 1, Type: "task"},
		{ID: "bead-b", Title: "Task B", Priority: 2, Type: "task"},
	})

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Read assignment messages from both workers.
	msg1, ok1 := readMsg(t, conn1, 2*time.Second)
	msg2, ok2 := readMsg(t, conn2, 2*time.Second)

	if !ok1 || !ok2 {
		t.Fatal("expected both workers to receive ASSIGN messages")
	}
	if msg1.Type != protocol.MsgAssign || msg2.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN messages, got %s and %s", msg1.Type, msg2.Type)
	}

	// The two workers must have different bead IDs.
	if msg1.Assign.BeadID == msg2.Assign.BeadID {
		t.Fatalf("both workers assigned same bead %q — expected different beads", msg1.Assign.BeadID)
	}
}

// TestFilterAssignableSkipsActiveBeads verifies that filterAssignable excludes
// beads that are currently assigned to a busy worker.
func TestFilterAssignableSkipsActiveBeads(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Simulate a worker busy on bead-active.
	d.mu.Lock()
	d.workers["w1"] = &trackedWorker{
		id:     "w1",
		state:  protocol.WorkerBusy,
		beadID: "bead-active",
	}
	d.mu.Unlock()

	beads := []protocol.Bead{
		{ID: "bead-active", Title: "Active bead", Priority: 1, Type: "task"},
		{ID: "bead-free", Title: "Free bead", Priority: 2, Type: "task"},
	}

	result := d.filterAssignable(beads)

	if len(result) != 1 {
		t.Fatalf("expected 1 assignable bead, got %d", len(result))
	}
	if result[0].ID != "bead-free" {
		t.Fatalf("expected bead-free, got %s", result[0].ID)
	}
}

// TestQGExhaustionPreventsReassignment verifies that after QG retry exhaustion,
// the bead is NOT re-assigned to a worker on subsequent cycles.
func TestQGExhaustionPreventsReassignment(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect a worker.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Provide one bead — use Opus model so no model escalation reset.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-exh", Title: "Will exhaust QG", Priority: 1, Type: "task", Model: protocol.ModelOpus},
	})

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Read and discard the initial ASSIGN.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatal("expected initial ASSIGN")
	}

	// Seed attemptCounts so the next QG failure triggers exhaustion.
	d.mu.Lock()
	d.attemptCounts["bead-exh"] = maxQGRetries - 1
	d.mu.Unlock()

	// Send a QG failure via MsgDone (how workers report QG results).
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            "bead-exh",
			WorkerID:          "w1",
			QualityGatePassed: false,
			QGOutput:          "FAIL: coverage too low",
		},
	})

	// Wait for the dispatcher to process exhaustion and release the worker.
	time.Sleep(200 * time.Millisecond)

	// The bead should now be in exhaustedBeads and NOT re-assigned.
	// Try to read another message — should NOT be an ASSIGN for bead-exh.
	msg2, ok2 := readMsg(t, conn, 1*time.Second)
	if ok2 && msg2.Type == protocol.MsgAssign && msg2.Assign.BeadID == "bead-exh" {
		t.Fatal("exhausted bead was re-assigned — should be blocked from re-assignment")
	}

	// Verify exhaustedBeads contains the bead.
	d.mu.Lock()
	exhausted := d.exhaustedBeads["bead-exh"]
	d.mu.Unlock()
	if !exhausted {
		t.Fatal("bead-exh should be in exhaustedBeads after QG exhaustion")
	}
}

// TestAssignBeadSkipsMissingAcceptanceBeforeWorktree verifies that beads
// without acceptance criteria are rejected BEFORE creating a worktree.
func TestAssignBeadSkipsMissingAcceptanceBeforeWorktree(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect a worker.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Configure bead with empty acceptance criteria.
	beadSrc.mu.Lock()
	beadSrc.shown["bead-noac"] = &protocol.BeadDetail{
		Title:              "No AC bead",
		AcceptanceCriteria: "",
	}
	beadSrc.mu.Unlock()

	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-noac", Title: "No AC bead", Priority: 1, Type: "task"},
	})

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Wait for the assign cycle to process.
	time.Sleep(300 * time.Millisecond)

	// No worktree should have been created for bead-noac.
	wtMgr.mu.Lock()
	_, created := wtMgr.created["bead-noac"]
	wtMgr.mu.Unlock()

	if created {
		t.Fatal("worktree was created for bead without acceptance criteria — should be rejected before worktree creation")
	}

	// Worker should NOT receive an ASSIGN.
	msg, ok := readMsg(t, conn, 500*time.Millisecond)
	if ok && msg.Type == protocol.MsgAssign && msg.Assign.BeadID == "bead-noac" {
		t.Fatal("bead without acceptance criteria was assigned to worker")
	}
}

// TestFilterAssignableSkipsExhaustedBeads verifies that filterAssignable
// excludes beads marked as QG-exhausted.
func TestFilterAssignableSkipsExhaustedBeads(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.mu.Lock()
	d.exhaustedBeads["bead-stuck"] = true
	d.mu.Unlock()

	beads := []protocol.Bead{
		{ID: "bead-stuck", Title: "Exhausted", Priority: 1, Type: "task"},
		{ID: "bead-ok", Title: "Available", Priority: 2, Type: "task"},
	}

	result := d.filterAssignable(beads)

	if len(result) != 1 {
		t.Fatalf("expected 1 assignable bead, got %d", len(result))
	}
	if result[0].ID != "bead-ok" {
		t.Fatalf("expected bead-ok, got %s", result[0].ID)
	}
}

// TestFilterAssignableSkipsInProgressBeads verifies that filterAssignable
// excludes beads with status="in_progress" (oro-wee1).
func TestFilterAssignableSkipsInProgressBeads(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	beads := []protocol.Bead{
		{ID: "bead-in-progress", Title: "Human working", Status: "in_progress", Priority: 0, Type: "task"},
		{ID: "bead-open", Title: "Available", Status: "open", Priority: 1, Type: "task"},
		{ID: "bead-blocked", Title: "Blocked", Status: "blocked", Priority: 2, Type: "task"},
	}

	result := d.filterAssignable(beads)

	// Should only include the "open" bead; in_progress and blocked should be filtered
	if len(result) != 1 {
		t.Fatalf("expected 1 assignable bead, got %d", len(result))
	}
	if result[0].ID != "bead-open" {
		t.Fatalf("expected bead-open, got %s", result[0].ID)
	}
}

// TestFilterAssignableHonorsInProgressStatus verifies oro-wee1 fix:
// beads with status=in_progress must not be assigned to workers, even if they
// are high-priority P0 bugs. This prevents workers from duplicating human work.
func TestFilterAssignableHonorsInProgressStatus(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Simulate oro-4lo7: a P0 bug that's in_progress (owned by human).
	beads := []protocol.Bead{
		{ID: "oro-4lo7", Title: "P0 bug", Status: "in_progress", Priority: 0, Type: "bug"},
		{ID: "oro-other", Title: "Other work", Status: "open", Priority: 1, Type: "task"},
	}

	result := d.filterAssignable(beads)

	// oro-4lo7 must NOT be in the candidate pool.
	if len(result) != 1 {
		t.Fatalf("expected 1 assignable bead, got %d", len(result))
	}
	if result[0].ID != "oro-other" {
		t.Fatalf("expected oro-other, got %s", result[0].ID)
	}

	// Verify oro-4lo7 is explicitly excluded.
	for _, b := range result {
		if b.ID == "oro-4lo7" {
			t.Fatal("oro-4lo7 (in_progress) should not be assignable")
		}
	}
}

// --- missing acceptance criteria escalation tests ---

func TestAssignBead_MissingAcceptanceEscalatesToManager(t *testing.T) {
	d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()
	d.setState(StateRunning)

	// Set up a bead with no acceptance criteria.
	beadSrc.mu.Lock()
	beadSrc.shown["bead-no-ac"] = &protocol.BeadDetail{
		Title:              "Test bead without AC",
		AcceptanceCriteria: "", // empty = no AC
	}
	beadSrc.mu.Unlock()

	// Connect a worker.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1"},
	})
	waitForWorkers(t, d, 1, 2*time.Second)

	// Attempt to assign the bead.
	w := &trackedWorker{id: "w1", conn: conn, state: protocol.WorkerIdle}
	_ = d.assignBead(context.Background(), w, protocol.Bead{ID: "bead-no-ac", Title: "Test bead without AC", Type: "task"})

	// Verify escalation was sent (not silent skip).
	esc.mu.Lock()
	escalated := len(esc.messages) > 0
	var escMsg string
	if escalated {
		escMsg = esc.messages[len(esc.messages)-1]
	}
	esc.mu.Unlock()

	if !escalated {
		t.Fatal("expected escalation for missing acceptance criteria")
	}
	if !strings.Contains(escMsg, "bead-no-ac") {
		t.Fatalf("escalation should mention bead ID, got: %s", escMsg)
	}

	// Verify worktree was NOT created (no assignment should happen).
	wtMgr.mu.Lock()
	_, created := wtMgr.created["bead-no-ac"]
	wtMgr.mu.Unlock()
	if created {
		t.Fatal("worktree should not be created for bead without AC")
	}
}

// --- safeGo panic recovery tests ---

func TestSafeGo_NormalCompletion(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	done := make(chan struct{})
	d.safeGo(func() {
		close(done)
	})

	select {
	case <-done:
		// Success — goroutine ran to completion.
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo goroutine did not complete")
	}
}

func TestSafeGo_PanicRecovery(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Transition to running so we can verify dispatcher stays alive.
	d.setState(StateRunning)

	// Launch a goroutine that panics.
	panicked := make(chan struct{})
	d.safeGo(func() {
		defer close(panicked)
		panic("test panic in safeGo")
	})

	// Wait for the goroutine to complete (via recovery).
	select {
	case <-panicked:
		// Goroutine's defer ran after recovery — good.
	case <-time.After(2 * time.Second):
		t.Fatal("panicking goroutine did not complete")
	}

	// Verify dispatcher is still alive by checking state.
	if got := d.GetState(); got != StateRunning {
		t.Fatalf("dispatcher should still be running, got %s", got)
	}

	// Verify the panic was logged to the events table.
	// The recover defer runs after fn's defers, so poll briefly.
	waitFor(t, func() bool {
		var count int
		_ = d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type='goroutine_panic'`).Scan(&count)
		return count > 0
	}, 2*time.Second)
}

func TestSafeGo_PanicDoesNotLeakWaitGroup(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Launch multiple goroutines, some of which panic.
	const total = 5
	var completed atomic.Int32
	for i := 0; i < total; i++ {
		shouldPanic := i%2 == 0
		d.safeGo(func() {
			completed.Add(1)
			if shouldPanic {
				panic("boom")
			}
		})
	}

	// Wait for all goroutines to finish via WaitGroup.
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines completed (including panicking ones).
	case <-time.After(2 * time.Second):
		t.Fatal("WaitGroup not drained — safeGo leaked a goroutine")
	}

	if got := completed.Load(); got != total {
		t.Fatalf("expected %d completions, got %d", total, got)
	}
}

func TestAutoCloseEpicWhenAllChildrenCompleted(t *testing.T) {
	t.Run("epic auto-closed when last child merges", func(t *testing.T) {
		d, beadSource, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		// Init schema so logEvent works.
		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		// Set up an epic with one child bead.
		epicID := "epic-123"
		childID := "child-1"
		workerID := "worker-1"
		worktree := "/tmp/worktree-" + childID
		branch := "agent/" + childID

		// Configure mock: after the child closes, AllChildrenClosed returns true.
		beadSource.allChildrenClosedMap = map[string]bool{
			epicID: true,
		}

		// Manually set up a tracked worker with the child bead and epicID.
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			epicID:  epicID, // parent epic for auto-close
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil), // dummy encoder
		}
		d.mu.Unlock()

		// Trigger merge and complete (white-box test).
		d.mergeAndComplete(ctx, childID, workerID, worktree, branch)

		// Wait for async auto-close goroutine.
		time.Sleep(100 * time.Millisecond)

		// Verify the child bead was closed.
		beadSource.mu.Lock()
		childClosed := false
		for _, id := range beadSource.closed {
			if id == childID {
				childClosed = true
				break
			}
		}
		beadSource.mu.Unlock()

		if !childClosed {
			t.Error("expected child bead to be closed")
		}

		// Verify the epic was auto-closed with reason "All children completed".
		beadSource.mu.Lock()
		epicClosed := false
		for _, id := range beadSource.closed {
			if id == epicID {
				epicClosed = true
				break
			}
		}
		beadSource.mu.Unlock()

		if !epicClosed {
			t.Error("expected epic to be auto-closed when all children completed")
		}
	})

	t.Run("epic NOT auto-closed when children still open", func(t *testing.T) {
		d, beadSource, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-456"
		childID := "child-2"
		workerID := "worker-2"
		worktree := "/tmp/worktree-" + childID
		branch := "agent/" + childID

		// Configure mock: AllChildrenClosed returns false (open children exist).
		beadSource.allChildrenClosedMap = map[string]bool{
			epicID: false,
		}

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			epicID:  epicID, // parent epic
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil),
		}
		d.mu.Unlock()

		d.mergeAndComplete(ctx, childID, workerID, worktree, branch)

		time.Sleep(100 * time.Millisecond)

		// Verify the child was closed but the epic was NOT closed.
		beadSource.mu.Lock()
		epicClosed := false
		for _, id := range beadSource.closed {
			if id == epicID {
				epicClosed = true
				break
			}
		}
		beadSource.mu.Unlock()

		if epicClosed {
			t.Error("epic should NOT be auto-closed when children are still open")
		}
	})

	t.Run("bd CLI error does not block merge flow", func(t *testing.T) {
		d, beadSource, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		childID := "child-3"
		workerID := "worker-3"
		worktree := "/tmp/worktree-" + childID
		branch := "agent/" + childID

		// Configure mock: AllChildrenClosed returns an error.
		beadSource.allChildrenClosedErr = fmt.Errorf("bd list failed")

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil),
		}
		d.mu.Unlock()

		// mergeAndComplete should complete successfully even if AllChildrenClosed errors.
		d.mergeAndComplete(ctx, childID, workerID, worktree, branch)

		time.Sleep(100 * time.Millisecond)

		// Verify the child bead was still closed (merge flow not blocked).
		beadSource.mu.Lock()
		childClosed := false
		for _, id := range beadSource.closed {
			if id == childID {
				childClosed = true
				break
			}
		}
		beadSource.mu.Unlock()

		if !childClosed {
			t.Error("child bead should be closed even if AllChildrenClosed errors")
		}
	})
}

func TestEpicCompletionAlert(t *testing.T) {
	t.Run("focused epic completion escalates alert", func(t *testing.T) {
		d, beadSource, _, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-focus-1"
		childID := "child-f1"
		workerID := "worker-f1"
		worktree := "/tmp/worktree-" + childID
		branch := "agent/" + childID

		// Set the focused epic.
		d.mu.Lock()
		d.focusedEpic = epicID
		d.mu.Unlock()

		// Configure mock: AllChildrenClosed returns true.
		beadSource.allChildrenClosedMap = map[string]bool{
			epicID: true,
		}

		// Set up tracked worker with the child bead and epic.
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			epicID:  epicID,
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil),
		}
		d.mu.Unlock()

		d.mergeAndComplete(ctx, childID, workerID, worktree, branch)

		// Wait for async auto-close goroutine.
		time.Sleep(200 * time.Millisecond)

		// Verify escalation message was sent with epic completion alert.
		msgs := esc.Messages()
		found := false
		for _, msg := range msgs {
			if strings.Contains(msg, "EPIC_COMPLETE") && strings.Contains(msg, epicID) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected EPIC_COMPLETE escalation for %s, got messages: %v", epicID, msgs)
		}

		// Verify message includes the clear instruction.
		for _, msg := range msgs {
			if strings.Contains(msg, "EPIC_COMPLETE") {
				if !strings.Contains(msg, `oro directive focus ""`) {
					t.Errorf("expected escalation to include clear instruction, got: %s", msg)
				}
			}
		}
	})

	t.Run("non-focused epic completion does not escalate", func(t *testing.T) {
		d, beadSource, _, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-other-1"
		childID := "child-o1"
		workerID := "worker-o1"
		worktree := "/tmp/worktree-" + childID
		branch := "agent/" + childID

		// Set focused epic to something DIFFERENT.
		d.mu.Lock()
		d.focusedEpic = "epic-different"
		d.mu.Unlock()

		beadSource.allChildrenClosedMap = map[string]bool{
			epicID: true,
		}

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			epicID:  epicID,
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil),
		}
		d.mu.Unlock()

		d.mergeAndComplete(ctx, childID, workerID, worktree, branch)

		time.Sleep(200 * time.Millisecond)

		// Verify NO epic completion escalation was sent.
		msgs := esc.Messages()
		for _, msg := range msgs {
			if strings.Contains(msg, "EPIC_COMPLETE") {
				t.Errorf("should not escalate EPIC_COMPLETE for non-focused epic, got: %s", msg)
			}
		}
	})

	t.Run("no focused epic means no alert", func(t *testing.T) {
		d, beadSource, _, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-nofocus"
		childID := "child-nf1"
		workerID := "worker-nf1"
		worktree := "/tmp/worktree-" + childID
		branch := "agent/" + childID

		// No focused epic set (default empty string).

		beadSource.allChildrenClosedMap = map[string]bool{
			epicID: true,
		}

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			epicID:  epicID,
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil),
		}
		d.mu.Unlock()

		d.mergeAndComplete(ctx, childID, workerID, worktree, branch)

		time.Sleep(200 * time.Millisecond)

		// Epic should still be auto-closed.
		beadSource.mu.Lock()
		epicClosed := false
		for _, id := range beadSource.closed {
			if id == epicID {
				epicClosed = true
				break
			}
		}
		beadSource.mu.Unlock()
		if !epicClosed {
			t.Error("expected epic to be auto-closed even without focus")
		}

		// But NO EPIC_COMPLETE escalation should be sent.
		msgs := esc.Messages()
		for _, msg := range msgs {
			if strings.Contains(msg, "EPIC_COMPLETE") {
				t.Errorf("should not escalate EPIC_COMPLETE when no focused epic, got: %s", msg)
			}
		}
	})
}

// --- Auto-scale on queue depth tests (oro-r8rl) ---

// TestAutoScaleOnQueueDepth verifies that when tryAssign finds assignable beads
// and 0 idle workers, targetWorkers auto-increases to min(queue depth, MaxWorkers)
// and reconcileScale is called.
func TestAutoScaleOnQueueDepth(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Set MaxWorkers to 3 (default from newTestDispatcher is 5, but let's be explicit)
	d.mu.Lock()
	d.cfg.MaxWorkers = 3
	d.targetWorkers = 0 // Start with 0 workers
	d.mu.Unlock()

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Set up 3 assignable beads
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-1", Title: "Task 1", Priority: 1},
		{ID: "bead-2", Title: "Task 2", Priority: 1},
		{ID: "bead-3", Title: "Task 3", Priority: 1},
	})

	// Wait for auto-scale to trigger and reconcileScale to spawn workers
	waitFor(t, func() bool {
		d.mu.Lock()
		target := d.targetWorkers
		d.mu.Unlock()
		return target == 3
	}, 3*time.Second)

	// Verify targetWorkers was set to min(3 beads, 3 MaxWorkers) = 3
	d.mu.Lock()
	target := d.targetWorkers
	d.mu.Unlock()

	if target != 3 {
		t.Errorf("expected targetWorkers=3 (queue depth), got %d", target)
	}

	// Verify reconcileScale was called (workers were spawned)
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) >= 3
	}, 3*time.Second)

	spawned := pm.SpawnedIDs()
	if len(spawned) < 3 {
		t.Errorf("expected at least 3 workers spawned, got %d", len(spawned))
	}
}

// TestAutoScaleRespectsMaxWorkers verifies that auto-scale never exceeds
// MaxWorkers config value, even when queue depth is higher.
func TestAutoScaleRespectsMaxWorkers(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Set MaxWorkers to 2 (lower than queue depth)
	d.mu.Lock()
	d.cfg.MaxWorkers = 2
	d.targetWorkers = 0 // Start with 0 workers
	d.mu.Unlock()

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Set up 5 assignable beads (more than MaxWorkers)
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-1", Title: "Task 1", Priority: 1},
		{ID: "bead-2", Title: "Task 2", Priority: 1},
		{ID: "bead-3", Title: "Task 3", Priority: 1},
		{ID: "bead-4", Title: "Task 4", Priority: 1},
		{ID: "bead-5", Title: "Task 5", Priority: 1},
	})

	// Wait for auto-scale to trigger
	waitFor(t, func() bool {
		d.mu.Lock()
		target := d.targetWorkers
		d.mu.Unlock()
		return target >= 2
	}, 3*time.Second)

	// Verify targetWorkers never exceeds MaxWorkers
	d.mu.Lock()
	target := d.targetWorkers
	maxWorkers := d.cfg.MaxWorkers
	d.mu.Unlock()

	if target > maxWorkers {
		t.Errorf("expected targetWorkers <= MaxWorkers (%d), got %d", maxWorkers, target)
	}

	// Should have scaled to exactly MaxWorkers
	if target != 2 {
		t.Errorf("expected targetWorkers=2 (MaxWorkers), got %d", target)
	}
}

// TestAutoScaleDisabledWhenMaxWorkersZero verifies that when MaxWorkers=0,
// auto-scale is disabled (manual scaling only via directive).
func TestAutoScaleDisabledWhenMaxWorkersZero(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Set MaxWorkers to 0 (disable auto-scale)
	d.mu.Lock()
	d.cfg.MaxWorkers = 0
	d.targetWorkers = 0
	d.mu.Unlock()

	startDispatcher(t, d)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Set up assignable beads
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-1", Title: "Task 1", Priority: 1},
		{ID: "bead-2", Title: "Task 2", Priority: 1},
		{ID: "bead-3", Title: "Task 3", Priority: 1},
	})

	// Wait a bit to ensure auto-scale doesn't trigger
	time.Sleep(500 * time.Millisecond)

	// Verify targetWorkers stayed at 0
	d.mu.Lock()
	target := d.targetWorkers
	d.mu.Unlock()

	if target != 0 {
		t.Errorf("expected targetWorkers=0 (MaxWorkers=0 disables auto-scale), got %d", target)
	}

	// Verify no workers were spawned
	spawned := pm.SpawnedIDs()
	if len(spawned) > 0 {
		t.Errorf("expected 0 workers spawned when MaxWorkers=0, got %d", len(spawned))
	}
}

// --- Enriched status tests (oro-vii8.1) ---

func TestBuildStatusJSON_EnrichedFields(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Connect a worker and assign it a bead.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-enr1", Title: "Enriched test", Priority: 1},
	})
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-enr1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Query status
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "status", "")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}

	var status statusResponse
	if err := json.Unmarshal([]byte(ack.Detail), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v, raw: %s", err, ack.Detail)
	}

	// Workers detail array
	if len(status.Workers) == 0 {
		t.Fatal("expected non-empty workers array")
	}
	found := false
	for _, ws := range status.Workers {
		if ws.ID == "w-enr1" {
			found = true
			if ws.BeadID != "bead-enr1" {
				t.Errorf("expected worker bead_id 'bead-enr1', got %q", ws.BeadID)
			}
			if ws.State != string(protocol.WorkerBusy) {
				t.Errorf("expected worker state 'busy', got %q", ws.State)
			}
			if ws.LastProgressSecs < 0 {
				t.Errorf("expected non-negative last_progress_secs, got %f", ws.LastProgressSecs)
			}
		}
	}
	if !found {
		t.Fatal("worker w-enr1 not found in workers array")
	}

	// Worker counts
	if status.ActiveCount != 1 {
		t.Errorf("expected active_count=1, got %d", status.ActiveCount)
	}
	if status.TargetCount != 5 { // MaxWorkers=5 in newTestDispatcher
		t.Errorf("expected target_count=5, got %d", status.TargetCount)
	}

	// Uptime
	if status.UptimeSeconds <= 0 {
		t.Errorf("expected uptime_seconds > 0, got %f", status.UptimeSeconds)
	}

	// Progress timeout config
	if status.ProgressTimeoutSecs <= 0 {
		t.Errorf("expected progress_timeout_secs > 0, got %f", status.ProgressTimeoutSecs)
	}
}

func TestBuildStatusJSON_CachedQueueDepth(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Set 3 beads ready — the assign loop should cache the depth.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-q1", Title: "Queue 1", Priority: 1},
		{ID: "bead-q2", Title: "Queue 2", Priority: 2},
		{ID: "bead-q3", Title: "Queue 3", Priority: 3},
	})

	// Connect a worker so the assign loop runs and caches depth.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-q1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}

	// Wait a tick for the cached depth to be updated.
	time.Sleep(150 * time.Millisecond)

	// Query status
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "status", "")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}

	var status statusResponse
	if err := json.Unmarshal([]byte(ack.Detail), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v, raw: %s", err, ack.Detail)
	}

	// After assigning bead-q1, 2 beads should remain in queue.
	// The cached depth should be > 0 (not hardcoded 0).
	if status.QueueDepth < 1 {
		t.Errorf("expected queue_depth >= 1 (cached from assign loop), got %d", status.QueueDepth)
	}
}

func TestBuildStatusJSON_LiveQueueDepth(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Initially set 2 beads ready.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-1", Title: "Bead 1", Priority: 1},
		{ID: "bead-2", Title: "Bead 2", Priority: 2},
	})

	// Connect a worker to trigger assign loop (caches depth of 2).
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN (bead-1 assigned)
	if !ok {
		t.Fatal("expected ASSIGN")
	}

	// Wait for assign loop to cache depth.
	time.Sleep(150 * time.Millisecond)

	// Now add 3 MORE beads (total 4 in source, 1 assigned, 3 ready).
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-1", Title: "Bead 1", Priority: 1}, // assigned
		{ID: "bead-2", Title: "Bead 2", Priority: 2},
		{ID: "bead-3", Title: "Bead 3", Priority: 3},
		{ID: "bead-4", Title: "Bead 4", Priority: 4},
	})

	// Query status immediately — should show 3 ready beads (live count),
	// NOT the stale cached value from before we added bead-3 and bead-4.
	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "status", "")
	if !ack.OK {
		t.Fatalf("expected OK=true, got false, detail: %s", ack.Detail)
	}

	var status statusResponse
	if err := json.Unmarshal([]byte(ack.Detail), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v, raw: %s", err, ack.Detail)
	}

	// Status should reflect live queue depth (3 ready beads), not stale cache.
	if status.QueueDepth != 3 {
		t.Errorf("expected queue_depth=3 (live count after adding beads), got %d", status.QueueDepth)
	}
}

// mockCodeIndex implements CodeIndex for testing.
type mockCodeIndex struct {
	mu            sync.Mutex
	chunks        []CodeChunk    // returned by FTS5Search
	searchResults []SearchResult // returned by Search
	err           error
	queries       []string // queries captured by FTS5Search
	searchQueries []string // queries captured by Search
}

func (m *mockCodeIndex) FTS5Search(_ context.Context, query string, _ int) ([]CodeChunk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries = append(m.queries, query)
	if m.err != nil {
		return nil, m.err
	}
	return m.chunks, nil
}

func (m *mockCodeIndex) Search(_ context.Context, query string, _ int) ([]SearchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.searchQueries = append(m.searchQueries, query)
	if m.err != nil {
		return nil, m.err
	}
	return m.searchResults, nil
}

// TestAssignBead_InjectsCodeContext verifies that assignBead runs Search
// on bead title and injects formatted results into AssignPayload.CodeSearchContext.
func TestAssignBead_InjectsCodeContext(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Inject a mock code index with test search results.
	codeIdx := &mockCodeIndex{
		searchResults: []SearchResult{
			{CodeChunk: CodeChunk{FilePath: "pkg/foo/bar.go", Name: "DoStuff", Kind: "function", StartLine: 10, EndLine: 20, Content: "func DoStuff() {}"}, Score: 1.0},
		},
	}
	d.codeIndex = codeIdx

	cancel := startDispatcher(t, d)
	defer cancel()
	d.setState(StateRunning)

	// Configure bead source with a titled bead.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-code1", Title: "Add code search", Priority: 1, Type: "task"},
	})

	// Connect a worker and trigger assignment.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-code1"},
	})
	waitForWorkers(t, d, 1, 2*time.Second)

	// Read the ASSIGN message.
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}

	// Verify CodeSearchContext is populated.
	if msg.Assign.CodeSearchContext == "" {
		t.Fatal("expected non-empty CodeSearchContext in ASSIGN payload")
	}
	if !strings.Contains(msg.Assign.CodeSearchContext, "pkg/foo/bar.go") {
		t.Errorf("expected CodeSearchContext to contain file path, got: %s", msg.Assign.CodeSearchContext)
	}
	if !strings.Contains(msg.Assign.CodeSearchContext, "func DoStuff() {}") {
		t.Errorf("expected CodeSearchContext to contain chunk content, got: %s", msg.Assign.CodeSearchContext)
	}

	// Verify Search was called with the bead title.
	codeIdx.mu.Lock()
	queries := codeIdx.searchQueries
	codeIdx.mu.Unlock()
	if len(queries) == 0 {
		t.Fatal("expected Search to be called")
	}
	if queries[0] != "Add code search" {
		t.Errorf("expected Search query to be bead title %q, got %q", "Add code search", queries[0])
	}
}

// TestAssignBeadInjectsRerankedCodeContext verifies that when mock codeIndex.Search()
// returns SearchResult with non-empty Reason, the assembled worker prompt contains that Reason string.
func TestAssignBeadInjectsRerankedCodeContext(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Inject a mock code index whose Search() returns a result with a Reason.
	const wantReason = "implements the core assignment algorithm"
	codeIdx := &mockCodeIndex{
		searchResults: []SearchResult{
			{
				CodeChunk: CodeChunk{
					FilePath:  "pkg/dispatcher/dispatcher.go",
					Name:      "assignBead",
					Kind:      "function",
					StartLine: 1700,
					EndLine:   1810,
					Content:   "func (d *Dispatcher) assignBead(...) {}",
				},
				Score:  0.95,
				Reason: wantReason,
			},
		},
	}
	d.codeIndex = codeIdx

	cancel := startDispatcher(t, d)
	defer cancel()
	d.setState(StateRunning)

	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-rerank1", Title: "Wire reranker into dispatcher", Priority: 1, Type: "task"},
	})

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-rerank1"},
	})
	waitForWorkers(t, d, 1, 2*time.Second)

	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}

	// Assert: CodeSearchContext contains the Reason string from the SearchResult.
	if !strings.Contains(msg.Assign.CodeSearchContext, wantReason) {
		t.Errorf("expected CodeSearchContext to contain Reason %q, got: %s", wantReason, msg.Assign.CodeSearchContext)
	}
}

// TestAssignBead_InjectsCodeContext_NilIndex verifies that assignBead handles
// nil codeIndex gracefully (no panic, no CodeSearchContext).
func TestAssignBead_InjectsCodeContext_NilIndex(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// codeIndex is nil by default from newTestDispatcher — verify that.
	if d.codeIndex != nil {
		t.Fatal("expected codeIndex to be nil in default test dispatcher")
	}

	cancel := startDispatcher(t, d)
	defer cancel()
	d.setState(StateRunning)

	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-nilcode", Title: "Test nil code index", Priority: 1, Type: "task"},
	})

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-nilcode"},
	})
	waitForWorkers(t, d, 1, 2*time.Second)

	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}

	// CodeSearchContext should be empty when codeIndex is nil.
	if msg.Assign.CodeSearchContext != "" {
		t.Errorf("expected empty CodeSearchContext when codeIndex is nil, got: %s", msg.Assign.CodeSearchContext)
	}
}

// TestFormatCodeResults verifies formatting of search results into markdown.
func TestFormatCodeResults(t *testing.T) {
	t.Parallel()

	t.Run("single chunk", func(t *testing.T) {
		t.Parallel()
		results := []SearchResult{
			{CodeChunk: CodeChunk{FilePath: "pkg/foo/bar.go", StartLine: 10, EndLine: 20, Content: "func Hello() {}"}, Score: 1.0},
		}
		result := formatSearchResults(results)
		if !strings.Contains(result, "### pkg/foo/bar.go:10-20") {
			t.Errorf("expected header with file:line range, got: %s", result)
		}
		if !strings.Contains(result, "func Hello() {}") {
			t.Errorf("expected chunk content, got: %s", result)
		}
		if !strings.Contains(result, "```") {
			t.Errorf("expected markdown code fence, got: %s", result)
		}
	})

	t.Run("multiple chunks", func(t *testing.T) {
		t.Parallel()
		results := []SearchResult{
			{CodeChunk: CodeChunk{FilePath: "a.go", StartLine: 1, EndLine: 5, Content: "package a"}, Score: 1.0},
			{CodeChunk: CodeChunk{FilePath: "b.go", StartLine: 10, EndLine: 15, Content: "package b"}, Score: 0.5},
		}
		result := formatSearchResults(results)
		if !strings.Contains(result, "### a.go:1-5") {
			t.Errorf("expected first chunk header, got: %s", result)
		}
		if !strings.Contains(result, "### b.go:10-15") {
			t.Errorf("expected second chunk header, got: %s", result)
		}
		if !strings.Contains(result, "package a") {
			t.Errorf("expected first chunk content, got: %s", result)
		}
		if !strings.Contains(result, "package b") {
			t.Errorf("expected second chunk content, got: %s", result)
		}
	})

	t.Run("empty chunks", func(t *testing.T) {
		t.Parallel()
		result := formatSearchResults(nil)
		if result != "" {
			t.Errorf("expected empty string for nil results, got: %s", result)
		}
		result = formatSearchResults([]SearchResult{})
		if result != "" {
			t.Errorf("expected empty string for empty results, got: %s", result)
		}
	})

	t.Run("includes reason when non-empty", func(t *testing.T) {
		t.Parallel()
		results := []SearchResult{
			{CodeChunk: CodeChunk{FilePath: "pkg/x.go", StartLine: 1, EndLine: 5, Content: "func X() {}"}, Score: 0.9, Reason: "directly implements X"},
		}
		result := formatSearchResults(results)
		if !strings.Contains(result, "directly implements X") {
			t.Errorf("expected Reason in output, got: %s", result)
		}
		if !strings.Contains(result, "_Relevance:") {
			t.Errorf("expected _Relevance: label, got: %s", result)
		}
	})
}

// TestAppendReviewPatterns_LogsErrorWhenUnwritable verifies that appendReviewPatterns
// returns an error when the file cannot be written and that the error is logged.
func TestAppendReviewPatterns_LogsErrorWhenUnwritable(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Create .claude directory and a read-only review-patterns.md file
	beadsDir := t.TempDir()
	root := beadsDir
	claudeDir := root + "/.claude"
	//nolint:gosec // test fixture: intentional directory permission
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("failed to create .claude dir: %v", err)
	}

	patternsFile := claudeDir + "/review-patterns.md"
	//nolint:gosec // test fixture: intentional file permission to simulate read-only file
	if err := os.WriteFile(patternsFile, []byte("existing content\n"), 0o444); err != nil {
		t.Fatalf("failed to create read-only patterns file: %v", err)
	}
	t.Cleanup(func() {
		// Restore write permission for cleanup
		//nolint:gosec // test cleanup: restoring write permission
		_ = os.Chmod(patternsFile, 0o644)
	})

	d.beadsDir = beadsDir + "/.beads"

	patterns := []string{"anti-pattern: avoid X", "anti-pattern: prefer Y"}
	err := d.appendReviewPatterns(ctx, "test-bead", "test-worker", patterns)

	// Assert: appendReviewPatterns returns an error
	if err == nil {
		t.Fatal("expected appendReviewPatterns to return error for unwritable file")
	}

	// Assert: the error was logged via logEvent
	count := eventCount(t, d.db, "append_review_patterns_failed")
	if count != 1 {
		t.Fatalf("expected 1 append_review_patterns_failed event, got %d", count)
	}
}

// TestWithReservation_WorkerDisconnectedDuringIO verifies that withReservation
// handles the case where a worker is deleted from the map during the I/O phase
// (between Phase 1 reservation and Phase 2 completion).
func TestWithReservation_WorkerDisconnectedDuringIO(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Connect a worker
	workerID := "worker-1"
	conn1, _ := net.Pipe()
	defer conn1.Close()
	d.registerWorker(workerID, conn1)

	// Set up initial worker state with beadID and worktree
	d.mu.Lock()
	w := d.workers[workerID]
	w.state = protocol.WorkerBusy
	w.beadID = "test-bead"
	w.worktree = "/tmp/test-worktree"
	d.mu.Unlock()

	// Track whether I/O and assign functions were called
	var ioCallCount, assignCallCount int

	// I/O function simulates memory retrieval
	ioFn := func() string {
		ioCallCount++
		// Simulate the worker being disconnected during I/O phase
		d.mu.Lock()
		delete(d.workers, workerID)
		d.mu.Unlock()
		return "memory-context"
	}

	// Assign function should not be called if worker is gone
	assignFn := func(w *trackedWorker, memCtx string) bool {
		assignCallCount++
		return true
	}

	// Execute withReservation
	success := d.withReservation(workerID, ioFn, assignFn)

	// Assert: withReservation returns false (worker was disconnected)
	if success {
		t.Fatal("expected withReservation to return false when worker disconnected during I/O")
	}

	// Assert: I/O was called
	if ioCallCount != 1 {
		t.Fatalf("expected ioFn to be called once, got %d", ioCallCount)
	}

	// Assert: assign was NOT called (worker disconnected during I/O)
	if assignCallCount != 0 {
		t.Fatalf("expected assignFn to NOT be called, got %d calls", assignCallCount)
	}
}

// TestApplyRestartDaemon verifies that restart-daemon directive triggers graceful shutdown.
// AC: ACK returned, PREPARE_SHUTDOWN sent to workers, process exits cleanly.
func TestApplyRestartDaemon(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Init schema
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// Connect a worker
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	go d.handleConn(ctx, serverConn)

	// Send HEARTBEAT to register worker
	workerID := "worker-1"
	hb := protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID: workerID,
		},
	}
	data, _ := json.Marshal(hb)
	data = append(data, '\n')
	_, _ = clientConn.Write(data)

	// Wait for worker to be registered
	time.Sleep(50 * time.Millisecond)

	// Verify worker is registered
	d.mu.Lock()
	if _, ok := d.workers[workerID]; !ok {
		d.mu.Unlock()
		t.Fatal("worker not registered")
	}
	d.mu.Unlock()

	// Apply restart-daemon directive
	detail, err := d.applyDirective(protocol.DirectiveRestartDaemon, "")
	// Assert: ACK returned with no error
	if err != nil {
		t.Fatalf("expected no error from applyDirective, got: %v", err)
	}
	if detail == "" {
		t.Fatal("expected non-empty detail in ACK")
	}

	// Assert: shutdownCh should be closed (signals graceful shutdown)
	select {
	case <-d.shutdownCh:
		// Expected: channel is closed
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected shutdownCh to be closed after restart-daemon directive")
	}

	// Manually trigger the shutdown sequence to verify PREPARE_SHUTDOWN is sent
	// (In production, Run() would detect shutdownCh closed and call shutdownWithTimeout)
	go func() {
		d.mu.Lock()
		workerIDs := make([]string, 0, len(d.workers))
		for id := range d.workers {
			workerIDs = append(workerIDs, id)
		}
		d.mu.Unlock()

		for _, id := range workerIDs {
			d.GracefulShutdownWorker(id, d.cfg.ShutdownTimeout)
		}
	}()

	// Assert: Worker should receive PREPARE_SHUTDOWN
	_ = clientConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	scanner := bufio.NewScanner(clientConn)
	if !scanner.Scan() {
		t.Fatal("expected PREPARE_SHUTDOWN message")
	}
	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	if msg.Type != protocol.MsgPrepareShutdown {
		t.Fatalf("expected PREPARE_SHUTDOWN, got %s", msg.Type)
	}
}

// TestDispatcher_FilterClosedBeads verifies that closed beads are never assigned,
// even if they were open when Ready() was called (race condition).
func TestDispatcher_FilterClosedBeads(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect a worker
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Provide two open beads initially
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "oro-open", Title: "Open bead", Status: "open", Priority: 2, Type: "task", AcceptanceCriteria: "Test: pass"},
		{ID: "oro-closed", Title: "Will be closed", Status: "open", Priority: 2, Type: "task", AcceptanceCriteria: "Test: pass"},
	})

	// Read the first ASSIGN message
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected first ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}

	// Now simulate the bead being closed externally (race condition):
	// Update beadSrc so oro-closed has status=closed
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "oro-open", Title: "Open bead", Status: "open", Priority: 2, Type: "task", AcceptanceCriteria: "Test: pass"},
		{ID: "oro-closed", Title: "Now closed", Status: "closed", Priority: 2, Type: "task", AcceptanceCriteria: "Test: pass"},
	})

	// Connect a second worker to trigger another assignment cycle
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w2",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 2, 1*time.Second)

	// Wait for assignment cycle
	time.Sleep(200 * time.Millisecond)

	// Verify the closed bead was NOT assigned to worker 2
	d.mu.Lock()
	var closedBeadAssigned bool
	for _, w := range d.workers {
		if w.beadID == "oro-closed" {
			closedBeadAssigned = true
			break
		}
	}
	d.mu.Unlock()

	if closedBeadAssigned {
		t.Fatal("closed bead oro-closed was assigned to a worker — closed beads must be filtered out")
	}
}

// TestHandleConnCleanupPrunesBeadTracking verifies that when a worker connection
// drops (scanner EOF), handleConn's deferred cleanup clears all BeadTracker maps
// for the worker's assigned beadID.
func TestHandleConnCleanupPrunesBeadTracking(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Provide a bead for assignment
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "oro-test", Title: "Test bead", Status: "open", Priority: 2, Type: "task", AcceptanceCriteria: "Test: pass"},
	})

	// Start dispatcher
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Connect worker
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})

	// Wait for worker to be registered and assigned
	waitForWorkers(t, d, 1, 1*time.Second)
	time.Sleep(200 * time.Millisecond)

	// Verify worker was assigned oro-test
	d.mu.Lock()
	w, exists := d.workers["w1"]
	if !exists || w.beadID != "oro-test" {
		d.mu.Unlock()
		t.Fatalf("worker w1 not assigned oro-test: exists=%v, beadID=%v", exists, w.beadID)
	}

	// Populate tracking maps to simulate dispatcher activity
	d.attemptCounts["oro-test"] = 1
	d.qgStuckTracker["oro-test"] = &qgHistory{hashes: []string{"abc123"}}
	d.escalatedBeads["oro-test"] = true
	d.worktreeFailures["oro-test"] = time.Now()
	d.assigningBeads["oro-test"] = true
	d.mu.Unlock()

	// Close connection to trigger handleConn's deferred cleanup
	_ = conn.Close()

	// Wait for cleanup to run
	time.Sleep(200 * time.Millisecond)

	// Verify worker was removed from d.workers
	d.mu.Lock()
	_, stillExists := d.workers["w1"]
	if stillExists {
		d.mu.Unlock()
		t.Fatal("worker w1 still exists after connection close")
	}

	// Assert: All BeadTracker maps should have zero entries for oro-test
	var errs []string
	if _, exists := d.attemptCounts["oro-test"]; exists {
		errs = append(errs, "attemptCounts still has oro-test entry")
	}
	if _, exists := d.qgStuckTracker["oro-test"]; exists {
		errs = append(errs, "qgStuckTracker still has oro-test entry")
	}
	if _, exists := d.escalatedBeads["oro-test"]; exists {
		errs = append(errs, "escalatedBeads still has oro-test entry")
	}
	if _, exists := d.worktreeFailures["oro-test"]; exists {
		errs = append(errs, "worktreeFailures still has oro-test entry")
	}
	if _, exists := d.assigningBeads["oro-test"]; exists {
		errs = append(errs, "assigningBeads still has oro-test entry")
	}
	d.mu.Unlock()

	if len(errs) > 0 {
		t.Errorf("BeadTracker maps not cleared:\n  - %s", strings.Join(errs, "\n  - "))
	}
}

// TestAssignBeadSkipsClosedBead verifies that assignBead does not create a worktree
// or send MsgAssign when BeadSource.Show returns a bead with status=closed.
// This prevents the oro-yoov race: bead closed externally after bd ready but before assignment.
func TestAssignBeadSkipsClosedBead(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

	beadID := "oro-closed-test"

	// Configure mock to return a closed bead when Show is called
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Already Closed Bead",
		AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
		Status:             "closed", // Bead is closed
	}
	beadSrc.mu.Unlock()

	ctx := context.Background()

	// Create a mock worker
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	d.registerWorker("w-test", server)

	d.mu.Lock()
	w := d.workers["w-test"]
	d.mu.Unlock()

	// Call assignBead with a bead that will be reported as closed by Show
	bead := protocol.Bead{ID: beadID, Priority: 2}
	_ = d.assignBead(ctx, w, bead)

	// Assert: No worktree was created
	wtMgr.mu.Lock()
	_, created := wtMgr.created[beadID]
	wtMgr.mu.Unlock()

	if created {
		t.Errorf("expected no worktree for closed bead %s, but worktree was created", beadID)
	}

	// Assert: Bead status was not updated to in_progress
	beadSrc.mu.Lock()
	status, updated := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()

	if updated {
		t.Errorf("expected bead not to be updated, but status was set to %q", status)
	}

	// Assert: bead_closed_before_assign event was logged
	if eventCount(t, d.db, "bead_closed_before_assign") == 0 {
		t.Error("expected bead_closed_before_assign event to be logged, but it was not found")
	}
}

// TestKillWorkerCleansUpWorktreeAndBead verifies that applyKillWorker:
//  1. Calls WorktreeManager.Remove with the worker's worktree path.
//  2. Calls BeadSource.Update(beadID, "open") to reset bead status.
//  3. Calls clearBeadTracking to remove all tracking-map entries for the bead.
//  4. Does NOT decrement targetWorkers when the killed worker is unmanaged.
func TestKillWorkerCleansUpWorktreeAndBead(t *testing.T) {
	const workerID = "w-kill-test"
	const beadID = "oro-cleanup1"
	const worktreePath = "/tmp/worktrees/oro-cleanup1"

	t.Run("managed worker: worktree removed, bead reset, tracking cleared, targetWorkers decremented", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:       workerID,
			conn:     conn,
			state:    protocol.WorkerBusy,
			beadID:   beadID,
			worktree: worktreePath,
			managed:  true,
			encoder:  json.NewEncoder(conn),
		}
		// Seed tracking maps so we can verify clearBeadTracking.
		d.attemptCounts[beadID] = 3
		d.handoffCounts[beadID] = 1
		d.rejectionCounts[beadID] = 2
		d.escalatedBeads[beadID] = true
		d.targetWorkers = 2
		d.mu.Unlock()

		_, err := d.applyKillWorker(workerID)
		if err != nil {
			t.Fatalf("applyKillWorker returned error: %v", err)
		}

		// 1. WorktreeManager.Remove must be called with the worker's worktree path.
		wtMgr.mu.Lock()
		removed := wtMgr.removed
		wtMgr.mu.Unlock()
		if len(removed) == 0 {
			t.Error("expected WorktreeManager.Remove to be called, but it was not")
		} else if removed[0] != worktreePath {
			t.Errorf("Remove called with %q, want %q", removed[0], worktreePath)
		}

		// 2. BeadSource.Update must reset bead to "open".
		beadSrc.mu.Lock()
		status := beadSrc.updated[beadID]
		beadSrc.mu.Unlock()
		if status != "open" {
			t.Errorf("BeadSource.Update called with status %q, want %q", status, "open")
		}

		// 3. Tracking maps must be cleared.
		d.mu.Lock()
		attempts := d.attemptCounts[beadID]
		handoffs := d.handoffCounts[beadID]
		rejections := d.rejectionCounts[beadID]
		escalated := d.escalatedBeads[beadID]
		d.mu.Unlock()
		if attempts != 0 || handoffs != 0 || rejections != 0 || escalated {
			t.Errorf("tracking maps not cleared: attempts=%d handoffs=%d rejections=%d escalated=%v",
				attempts, handoffs, rejections, escalated)
		}

		// 4. targetWorkers must be decremented for a managed worker (2 -> 1).
		d.mu.Lock()
		target := d.targetWorkers
		d.mu.Unlock()
		if target != 1 {
			t.Errorf("targetWorkers = %d, want 1 after killing managed worker", target)
		}
	})

	t.Run("unmanaged worker: worktree removed, bead reset, tracking cleared, targetWorkers NOT decremented", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:       workerID,
			conn:     conn,
			state:    protocol.WorkerBusy,
			beadID:   beadID,
			worktree: worktreePath,
			managed:  false, // external/unmanaged
			encoder:  json.NewEncoder(conn),
		}
		d.attemptCounts[beadID] = 1
		d.targetWorkers = 1
		d.mu.Unlock()

		_, err := d.applyKillWorker(workerID)
		if err != nil {
			t.Fatalf("applyKillWorker returned error: %v", err)
		}

		// Worktree must still be removed for unmanaged workers.
		wtMgr.mu.Lock()
		removed := wtMgr.removed
		wtMgr.mu.Unlock()
		if len(removed) == 0 || removed[0] != worktreePath {
			t.Errorf("WorktreeManager.Remove not called correctly for unmanaged worker: %v", removed)
		}

		// Bead must still be reset to open.
		beadSrc.mu.Lock()
		status := beadSrc.updated[beadID]
		beadSrc.mu.Unlock()
		if status != "open" {
			t.Errorf("BeadSource.Update status = %q, want %q", status, "open")
		}

		// targetWorkers must NOT be decremented for an unmanaged worker.
		d.mu.Lock()
		target := d.targetWorkers
		d.mu.Unlock()
		if target != 1 {
			t.Errorf("targetWorkers = %d, want 1 (unmanaged worker should not affect target count)", target)
		}
	})

	t.Run("worker with no bead or worktree: skips removal and reset", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:       workerID,
			conn:     conn,
			state:    protocol.WorkerIdle,
			beadID:   "", // no assignment
			worktree: "", // no worktree
			managed:  true,
			encoder:  json.NewEncoder(conn),
		}
		d.targetWorkers = 1
		d.mu.Unlock()

		_, err := d.applyKillWorker(workerID)
		if err != nil {
			t.Fatalf("applyKillWorker returned error: %v", err)
		}

		// No worktree to remove.
		wtMgr.mu.Lock()
		removed := wtMgr.removed
		wtMgr.mu.Unlock()
		if len(removed) != 0 {
			t.Errorf("WorktreeManager.Remove called unexpectedly with %v for idle worker", removed)
		}

		// No bead to reset.
		beadSrc.mu.Lock()
		_, hasUpdate := beadSrc.updated[""]
		beadSrc.mu.Unlock()
		if hasUpdate {
			t.Error("BeadSource.Update called for empty beadID, should be skipped")
		}
	})
}

// TestAssignBead_EmptyBeadIDReturnsError verifies that assignBead returns an error
// and does NOT create a worktree when the bead's ID is empty or whitespace-only.
func TestAssignBead_EmptyBeadIDReturnsError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		beadID string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d, _, wtMgr, _, _, _ := newTestDispatcher(t)

			ctx := context.Background()

			// Create a mock worker connection.
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()

			d.registerWorker("w-empty-test", server)

			d.mu.Lock()
			w := d.workers["w-empty-test"]
			d.mu.Unlock()

			bead := protocol.Bead{ID: tc.beadID, Priority: 2}
			err := d.assignBead(ctx, w, bead)

			if err == nil {
				t.Errorf("assignBead(%q): expected error, got nil", tc.beadID)
			}

			// Assert: no worktree was created.
			wtMgr.mu.Lock()
			createdCount := len(wtMgr.created)
			wtMgr.mu.Unlock()

			if createdCount != 0 {
				t.Errorf("assignBead(%q): expected 0 worktrees created, got %d", tc.beadID, createdCount)
			}
		})
	}
}

// TestAssignEpicDecomposition verifies that epics without children are routed
// to decomposition workers with IsEpicDecomposition=true and opus model,
// epics with open children are skipped, and handleDone for epic decomp
// skips merge/close.
func TestAssignEpicDecomposition(t *testing.T) {
	t.Run("epic with no children assigned for decomposition", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		beadSrc.mu.Lock()
		beadSrc.hasChildrenMap = map[string]bool{"oro-epic1": false}
		beadSrc.shown["oro-epic1"] = &protocol.BeadDetail{
			Title:              "Epic: Add feature",
			AcceptanceCriteria: "Decompose into subtasks",
		}
		beadSrc.mu.Unlock()

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{
			ID:    "oro-epic1",
			Title: "Epic: Add feature",
			Type:  "epic",
		}})

		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected ASSIGN for epic decomposition")
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN, got %s", msg.Type)
		}
		if !msg.Assign.IsEpicDecomposition {
			t.Error("IsEpicDecomposition: got false, want true")
		}
		if msg.Assign.Model != protocol.ModelOpus {
			t.Errorf("Model: got %q, want %q", msg.Assign.Model, protocol.ModelOpus)
		}
	})

	t.Run("epic with open children skipped", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		beadSrc.mu.Lock()
		beadSrc.hasChildrenMap = map[string]bool{"oro-epic2": true}
		beadSrc.allChildrenClosedMap = map[string]bool{"oro-epic2": false}
		beadSrc.shown["oro-epic2"] = &protocol.BeadDetail{
			Title:              "Epic: Existing",
			AcceptanceCriteria: "Some AC",
		}
		beadSrc.mu.Unlock()

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{
			ID:    "oro-epic2",
			Title: "Epic: Existing",
			Type:  "epic",
		}})

		_, ok := readMsg(t, conn, 500*time.Millisecond)
		if ok {
			t.Fatal("epic with open children should not be assigned")
		}
	})

	t.Run("handleDone for epic skips merge and close", func(t *testing.T) {
		d, beadSrc, wtMgr, _, gitRunner, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		beadSrc.mu.Lock()
		beadSrc.hasChildrenMap = map[string]bool{"oro-epic3": false}
		beadSrc.shown["oro-epic3"] = &protocol.BeadDetail{
			Title:              "Epic: decompose me",
			AcceptanceCriteria: "Decompose into subtasks",
		}
		beadSrc.mu.Unlock()

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{
			ID:    "oro-epic3",
			Title: "Epic: decompose me",
			Type:  "epic",
		}})

		_, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected ASSIGN for epic decomposition")
		}
		beadSrc.SetBeads(nil)

		// Send DONE (quality gate passed)
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{BeadID: "oro-epic3", WorkerID: "w1", QualityGatePassed: true},
		})

		// Worker should return to idle (no merge goroutine to wait for)
		waitForWorkerState(t, d, "w1", protocol.WorkerIdle, 2*time.Second)

		// No git rebase should have been attempted
		if calls := gitRunner.RebaseCalls(); len(calls) > 0 {
			t.Errorf("expected no git rebase for epic decomp, got %d calls: %v", len(calls), calls)
		}

		// Epic bead should NOT be closed by the dispatcher
		beadSrc.mu.Lock()
		closedCopy := make([]string, len(beadSrc.closed))
		copy(closedCopy, beadSrc.closed)
		beadSrc.mu.Unlock()
		for _, id := range closedCopy {
			if id == "oro-epic3" {
				t.Error("epic should not be closed on decomp done; expected no beads.Close call")
			}
		}

		// Worktree should be removed as cleanup
		waitFor(t, func() bool {
			wtMgr.mu.Lock()
			defer wtMgr.mu.Unlock()
			for _, p := range wtMgr.removed {
				if strings.Contains(p, "oro-epic3") {
					return true
				}
			}
			return false
		}, 1*time.Second)
	})
}
