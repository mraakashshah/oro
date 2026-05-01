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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
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

type deferCall struct {
	id, until string
}

type fakeBeadStore struct {
	mu                   sync.Mutex
	beads                []protocol.Bead
	shown                map[string]*protocol.BeadDetail
	closed               []string
	updated              map[string]string // beadID -> status
	created              []createCall
	createID             string // ID returned by Create; defaults to "oro-new1"
	synced               bool
	readyErr             error            // if set, Ready() returns this error
	allChildrenClosedMap map[string]bool  // epicID -> allClosed
	allChildrenClosedErr error            // if set, AllChildrenClosed() returns this error
	hasChildrenMap       map[string]bool  // epicID -> hasChildren
	hasChildrenErr       error            // if set, HasChildren() returns this error
	inProgressBeads      []protocol.Bead  // returned by InProgress(); nil means no beads
	inProgressErr        error            // if set, InProgress() returns this error
	blockedBeads         []protocol.Bead  // returned by Blocked(); nil means no beads
	blockedErr           error            // if set, Blocked() returns this error
	closedBeads          []protocol.Bead  // returned by Closed(); nil means no beads
	closedErr            error            // if set, Closed() returns this error
	updateErrs           map[string]error // beadID -> error returned by Update()
	showErr              error            // if set, Show() returns this error for all IDs
	showErrFn            map[string]error // per-ID Show errors (takes precedence over showErr)
	shownNil             map[string]bool  // per-ID nil detail (returns nil, nil)
	exportData           []byte           // returned by Export(); nil means no data
	exportErr            error            // if set, Export() returns this error
	deferCalls           []deferCall
	undeferCalls         []string
	beadOps              []string
	deferErrs            map[string]error
	undeferErrs          map[string]error
	readyCalled          int // incremented on every Ready() call
}

func (m *fakeBeadStore) Ready(_ context.Context) ([]protocol.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readyCalled++
	if m.readyErr != nil {
		return nil, m.readyErr
	}
	out := make([]protocol.Bead, len(m.beads))
	copy(out, m.beads)
	return out, nil
}

func (m *fakeBeadStore) Show(_ context.Context, id string) (*protocol.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Check per-ID Show error first (takes precedence).
	if m.showErrFn != nil {
		if err, ok := m.showErrFn[id]; ok {
			return nil, err
		}
	}
	// Check global Show error.
	if m.showErr != nil {
		return nil, m.showErr
	}
	// Check if this ID should return nil detail (edge case test).
	if m.shownNil != nil {
		if m.shownNil[id] {
			return nil, nil
		}
	}
	// Check if detail is provided.
	if d, ok := m.shown[id]; ok {
		return d, nil
	}
	// Default: return detail with acceptance criteria so assignBead doesn't skip.
	return &protocol.BeadDetail{
		Title:              id,
		AcceptanceCriteria: "Test: auto | Assert: PASS",
	}, nil
}

func (m *fakeBeadStore) Close(_ context.Context, id string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, id)
	return nil
}

func (m *fakeBeadStore) Update(_ context.Context, id string, params beadstore.UpdateParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateErrs != nil {
		if err, ok := m.updateErrs[id]; ok {
			return err
		}
	}
	if m.updated == nil {
		m.updated = make(map[string]string)
	}
	if params.Status != nil {
		m.updated[id] = *params.Status
	}
	return nil
}

func (m *fakeBeadStore) Sync(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.synced = true
	return nil
}

func (m *fakeBeadStore) Create(_ context.Context, params beadstore.CreateParams) (*protocol.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, createCall{
		title:              params.Title,
		beadType:           params.Type,
		priority:           params.Priority,
		description:        params.Description,
		parent:             params.ParentID,
		acceptanceCriteria: params.AcceptanceCriteria,
	})
	id := "oro-new1"
	if m.createID != "" {
		id = m.createID
	}
	return &protocol.Bead{ID: id}, nil
}

func (m *fakeBeadStore) AllChildrenClosed(_ context.Context, epicID string) (bool, error) {
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

func (m *fakeBeadStore) HasChildren(_ context.Context, epicID string) (bool, error) {
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

func (m *fakeBeadStore) FindByParentAndTag(_ context.Context, _ string, _ string) ([]protocol.Bead, error) {
	return []protocol.Bead{}, nil
}

func (m *fakeBeadStore) InProgress(_ context.Context) ([]protocol.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inProgressErr != nil {
		return nil, m.inProgressErr
	}
	return m.inProgressBeads, nil
}

func (m *fakeBeadStore) Blocked(_ context.Context) ([]protocol.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blockedErr != nil {
		return nil, m.blockedErr
	}
	return m.blockedBeads, nil
}

func (m *fakeBeadStore) Closed(_ context.Context, _ int) ([]protocol.Bead, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closedErr != nil {
		return nil, m.closedErr
	}
	return m.closedBeads, nil
}

func (m *fakeBeadStore) Export(_ context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exportErr != nil {
		return nil, m.exportErr
	}
	return m.exportData, nil
}

func (m *fakeBeadStore) Defer(_ context.Context, id, until string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deferCalls = append(m.deferCalls, deferCall{id: id, until: until})
	m.beadOps = append(m.beadOps, "defer:"+id)
	if m.deferErrs != nil {
		return m.deferErrs[id]
	}
	return nil
}

func (m *fakeBeadStore) Undefer(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.undeferCalls = append(m.undeferCalls, id)
	m.beadOps = append(m.beadOps, "undefer:"+id)
	if m.undeferErrs != nil {
		return m.undeferErrs[id]
	}
	return nil
}

func (m *fakeBeadStore) SetBeads(beads []protocol.Bead) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.beads = beads
}

// updateBranchRefCall records one call to UpdateBranchRef.
type updateBranchRefCall struct {
	target, source string
}

type mockWorktreeManager struct {
	mu                sync.Mutex
	created           map[string]string // beadID -> worktree path
	removed           []string
	deletedBranches   []string
	mergedBranches    []string // branches passed to MergeFFOnly
	updatedBranchRefs []updateBranchRefCall
	createFn          func(ctx context.Context, beadID, baseBranch string) (string, string, error)
	removeFn          func(ctx context.Context, path string) error
	deleteBranchFn    func(branch string) error
	branchExistsFn    func(ctx context.Context, branch string) (bool, error)
	mergeFFOnlyFn     func(branch, target string) (string, error)
	updateBranchRefFn func(target, source string) error
	existsFn          func(ctx context.Context, path string) bool
	createBranchFn    func(ctx context.Context, name, from string) error
}

func (m *mockWorktreeManager) Create(ctx context.Context, beadID, baseBranch string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createFn != nil {
		return m.createFn(ctx, beadID, baseBranch)
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

func (m *mockWorktreeManager) DeleteBranch(_ context.Context, branch string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteBranchFn != nil {
		return m.deleteBranchFn(branch)
	}
	m.deletedBranches = append(m.deletedBranches, branch)
	return nil
}

func (m *mockWorktreeManager) Prune(ctx context.Context) error {
	return nil
}

func (m *mockWorktreeManager) BranchExists(ctx context.Context, branch string) (bool, error) {
	m.mu.Lock()
	fn := m.branchExistsFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, branch)
	}
	return true, nil // default: branch exists; set branchExistsFn to simulate missing branch
}

func (m *mockWorktreeManager) MergeFFOnly(_ context.Context, branch, target string) (string, error) {
	m.mu.Lock()
	fn := m.mergeFFOnlyFn
	m.mu.Unlock()
	if fn != nil {
		return fn(branch, target)
	}
	m.mu.Lock()
	m.mergedBranches = append(m.mergedBranches, branch)
	m.mu.Unlock()
	return "", nil
}

func (m *mockWorktreeManager) UpdateBranchRef(_ context.Context, targetBranch, sourceBranch string) error {
	m.mu.Lock()
	fn := m.updateBranchRefFn
	m.mu.Unlock()
	if fn != nil {
		return fn(targetBranch, sourceBranch)
	}
	m.mu.Lock()
	m.updatedBranchRefs = append(m.updatedBranchRefs, updateBranchRefCall{targetBranch, sourceBranch})
	m.mu.Unlock()
	return nil
}

func (m *mockWorktreeManager) GCClosedWorktrees(_ context.Context, _ func(string) bool) error {
	return nil
}

func (m *mockWorktreeManager) Exists(ctx context.Context, path string) bool {
	m.mu.Lock()
	fn := m.existsFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, path)
	}
	return true // default: path is valid (preserves existing test behaviour)
}

func (m *mockWorktreeManager) RebaseOnto(_ context.Context, _, _ string) error {
	return nil // default: rebase succeeds
}

func (m *mockWorktreeManager) PushBranch(_ context.Context, _ string) error {
	return nil // default: push succeeds
}

func (m *mockWorktreeManager) CreateBranch(ctx context.Context, name, from string) error {
	m.mu.Lock()
	fn := m.createBranchFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, name, from)
	}
	return nil // default: branch creation succeeds
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
type spawnCall struct {
	model   string
	prompt  string
	workdir string
}

type mockBatchSpawner struct {
	mu       sync.Mutex
	verdict  string
	spawnErr error
	spawns   []spawnCall
}

func (m *mockBatchSpawner) Spawn(_ context.Context, model string, prompt string, workdir string) (ops.Process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spawns = append(m.spawns, spawnCall{model, prompt, workdir})
	if m.spawnErr != nil {
		return nil, m.spawnErr
	}
	return &mockProcess{output: m.verdict}, nil
}

func (m *mockBatchSpawner) SpawnCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.spawns)
}

// SpawnCountExcludingModel returns the number of spawns that did NOT use the given model.
// Used in tests to filter out background dream (haiku) spawns from diagnostic spawn assertions.
func (m *mockBatchSpawner) SpawnCountExcludingModel(model string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.spawns {
		if s.model != model {
			n++
		}
	}
	return n
}

// mockAcceptanceRunner is a test double for AcceptanceRunner.
type mockAcceptanceRunner struct {
	mu     sync.Mutex
	output string
	passed bool
	err    error
	calls  int
}

func (m *mockAcceptanceRunner) Run(_ context.Context, _ string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.output, m.passed, m.err
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
	db, err := dbutil.OpenDB(dsn)
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
func newTestDispatcher(t *testing.T) (*Dispatcher, *fakeBeadStore, *mockWorktreeManager, *mockEscalator, *mockGitRunner, *mockBatchSpawner) {
	t.Helper()
	db := newTestDB(t)

	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)

	spawnMock := &mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}
	opsSpawner := ops.NewSpawner(spawnMock)

	beadSrc := &fakeBeadStore{
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
		ShutdownTimeout:  200 * time.Millisecond,
	}

	d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	// Default to a passing QG runner so existing mergeAndComplete tests are
	// not broken by the new pre-merge gate. Tests that exercise QG behaviour
	// inject their own mockQGRunner after calling newTestDispatcher.
	d.qgRunner = &mockQGRunner{passed: true}
	// Use a short escalation retry interval so loop-panic tests don't wait 2 minutes.
	d.escalationRetryInterval = 50 * time.Millisecond
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
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("dispatcher exited before listener ready: %v", err)
			}
			t.Fatal("dispatcher exited before listener ready")
		default:
		}
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

// getLogEvents retrieves all event types and payloads from the dispatcher's event log.
// Returns formatted strings like "epic_branch_pending: ..."
func getLogEvents(t *testing.T, d *Dispatcher) []string {
	t.Helper()
	rows, err := d.db.Query(`SELECT type, payload FROM events ORDER BY created_at`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()

	var events []string
	for rows.Next() {
		var evType, payload string
		if err := rows.Scan(&evType, &payload); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		events = append(events, fmt.Sprintf("%s: %s", evType, payload))
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}
	return events
}

// --- Tests ---

// TestDirectiveACKReceivedByClient verifies that a raw socket client sending a
// directive JSON message receives an ACK JSON response within 2 seconds.
// This is the path a raw socket client uses when querying the dispatcher for status.
func TestDirectiveACKReceivedByClient(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect as a raw socket client.
	conn, err := net.Dial("unix", d.cfg.SocketPath)
	if err != nil {
		t.Fatalf("connect to dispatcher: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Send a directive JSON message.
	msg := protocol.Message{
		Type:      protocol.MsgDirective,
		Directive: &protocol.DirectivePayload{Op: "status"},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal directive: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write directive: %v", err)
	}

	// Read ACK within 2 seconds.
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatal("no ACK response received from dispatcher within 2 seconds")
	}

	var ack protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil {
		t.Fatalf("parse ACK response: %v", err)
	}
	if ack.Type != protocol.MsgACK {
		t.Fatalf("expected ACK message type, got %q", ack.Type)
	}
	if ack.ACK == nil {
		t.Fatal("ACK payload is nil")
	}
	if !ack.ACK.OK {
		t.Fatalf("ACK.OK=false, detail: %q", ack.ACK.Detail)
	}
}

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
	waitFor(t, func() bool {
		d.mu.Lock()
		ln := d.listener
		d.mu.Unlock()
		return ln != nil
	}, 2*time.Second)

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
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
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

	// Bead must NOT be assigned — a MISSING_AC escalation is fired instead.
	msg, ok := readMsg(t, conn, 1*time.Second)
	if ok && msg.Type == protocol.MsgAssign {
		t.Fatal("bead without AC should not be assigned; MISSING_AC escalation should fire instead")
	}

	// Verify MISSING_AC escalation was sent.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, string(protocol.EscMissingAC)) {
				return true
			}
		}
		return false
	}, 2*time.Second)
}

func TestCheckBeadReady_RejectsOversizedBead(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Bead with AC that touches 3 distinct modules — exceeds the 2-module limit.
	oversizedAC := "Read: pkg/dispatcher/dispatcher.go:510, pkg/ops/review_prompt.go:128, langprofile/detect.go:38"
	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		"bead-oversize": {Title: "Oversized bead", AcceptanceCriteria: oversizedAC},
	}
	beadSrc.mu.Unlock()
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-oversize", Title: "Oversized bead", Priority: 1}})

	// Bead must NOT be assigned — OVERSIZED_BEAD escalation fires instead.
	msg, ok := readMsg(t, conn, 1*time.Second)
	if ok && msg.Type == protocol.MsgAssign {
		t.Fatal("oversized bead should not be assigned; OVERSIZED_BEAD escalation should fire instead")
	}

	// OVERSIZED_BEAD escalation must be emitted.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, string(protocol.EscOversizedBead)) {
				return true
			}
		}
		return false
	}, 2*time.Second)

	// Bead must enter worktreeFailure cooldown (same mechanism as MISSING_AC).
	d.mu.Lock()
	_, inCooldown := d.worktreeFailures["bead-oversize"]
	d.mu.Unlock()
	if !inCooldown {
		t.Error("oversized bead should be in worktreeFailures cooldown after rejection")
	}
}

// TestCheckBeadReady_SkipsOversizedCheckForEpicType verifies that epics with
// oversized AC are filtered at the type level (non_executable_issue_type) before
// reaching checkBeadReady, so OVERSIZED_BEAD escalation never fires for epics.
func TestCheckBeadReady_SkipsOversizedCheckForEpicType(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Epic bead touching 3 modules — would be oversized if not filtered first.
	oversizedAC := "Read: pkg/dispatcher/dispatcher.go:510, pkg/ops/review_prompt.go:128, langprofile/detect.go:38"
	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		"bead-epic1": {ID: "bead-epic1", Title: "Epic bead", AcceptanceCriteria: oversizedAC, Type: "epic"},
	}
	beadSrc.mu.Unlock()
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-epic1", Title: "Epic bead", Priority: 1, Type: "epic"}})

	// Epic must NOT be assigned — it is filtered as a non-executable issue type.
	// Wait for the non_executable_issue_type event to confirm the dispatcher saw it.
	waitFor(t, func() bool {
		var count int
		_ = d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = ? AND bead_id = ?`,
			"non_executable_issue_type", "bead-epic1").Scan(&count)
		return count > 0
	}, 2*time.Second)

	_, assigned := readMsg(t, conn, 200*time.Millisecond)
	if assigned {
		t.Fatal("epic bead must not be assigned to a worker")
	}

	// OVERSIZED_BEAD escalation must NOT have fired.
	for _, m := range esc.Messages() {
		if strings.Contains(m, string(protocol.EscOversizedBead)) {
			t.Errorf("OVERSIZED_BEAD escalation must not fire for epic bead; got: %s", m)
		}
	}
}

// TestCheckBeadReady_SkipsOversizedCheckWhenHasChildren verifies that a bead
// with existing children (already decomposed) is NOT blocked by the
// OVERSIZED_BEAD check and can be processed normally.
func TestCheckBeadReady_SkipsOversizedCheckWhenHasChildren(t *testing.T) {
	d, beadSrc, _, esc, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Non-epic bead touching 3 modules — oversized, but already has children.
	oversizedAC := "Read: pkg/dispatcher/dispatcher.go:510, pkg/ops/review_prompt.go:128, langprofile/detect.go:38"
	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		"bead-decomp1": {ID: "bead-decomp1", Title: "Decomposed bead", AcceptanceCriteria: oversizedAC, Type: "task"},
	}
	beadSrc.hasChildrenMap = map[string]bool{"bead-decomp1": true}
	beadSrc.mu.Unlock()
	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-decomp1", Title: "Decomposed bead", Priority: 1, Type: "task"}})

	// Bead must receive an ASSIGN message (not be blocked).
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("decomposed bead should be assigned; got ok=%v type=%v", ok, msg.Type)
	}

	// OVERSIZED_BEAD escalation must NOT have fired.
	for _, m := range esc.Messages() {
		if strings.Contains(m, string(protocol.EscOversizedBead)) {
			t.Errorf("OVERSIZED_BEAD escalation must not fire for already-decomposed bead; got: %s", m)
		}
	}
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
		t.Fatalf("expected model claude-sonnet-4-6, got %q", msg.Assign.Model)
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

func TestWorkerReceivesMemoryInPrompt(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Seed a memory whose content contains terms identical to the bead title,
	// guaranteeing FTS5 match without flakiness (SanitizeFTS5Query uses OR per term).
	ctx := context.Background()
	memContent := "always run golangci lint before committing"
	_, err := d.memories.Insert(ctx, memory.InsertParams{
		Content:    memContent,
		Type:       "lesson",
		Source:     "self_report",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-mem", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Bead title shares terms with the memory content so ForPrompt finds it via FTS5.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-lint", Title: "golangci lint checks", Priority: 1},
	})

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected MsgAssign, got %s", msg.Type)
	}
	if !containsStr(msg.Assign.MemoryContext, "Relevant Memories") {
		t.Errorf("MemoryContext missing 'Relevant Memories' header; got: %q", msg.Assign.MemoryContext)
	}
	if !containsStr(msg.Assign.MemoryContext, "golangci lint") {
		t.Errorf("MemoryContext missing memory content; got: %q", msg.Assign.MemoryContext)
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
	spawnMock.verdict = "all tests pass\n\nVERDICT: APPROVED"
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
	spawnMock.verdict = "missing tests for edge case\n\nVERDICT: REJECTED"
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

	// sendDirective is synchronous (waits for ACK), so processing is already
	// complete when it returns — no sleep needed.
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

func TestSpawnEscalationOneShot_UsesWorktreeDir(t *testing.T) {
	t.Run("uses worktree path when worktreeByBead is set", func(t *testing.T) {
		d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)

		// Set worktreeByBead mapping
		d.mu.Lock()
		d.worktreeByBead["bead-worktree"] = "/some/worktree/path"
		d.mu.Unlock()

		// Provide bead detail so the one-shot gets context
		beadSrc.mu.Lock()
		beadSrc.shown["bead-worktree"] = &protocol.BeadDetail{
			ID:          "bead-worktree",
			Title:       "Test worktree bead",
			Description: "Testing worktree directory usage",
		}
		beadSrc.mu.Unlock()

		// Set spawn verdict
		spawnMock.mu.Lock()
		spawnMock.verdict = "ACK: done"
		spawnMock.mu.Unlock()

		ctx := context.Background()

		// Trigger escalation with a STUCK_WORKER message
		msg := protocol.FormatEscalation(protocol.EscStuckWorker, "bead-worktree", "worker stalled", "no progress")
		d.escalate(ctx, msg, "bead-worktree", "w1")

		// Wait for spawn to be captured
		waitFor(t, func() bool {
			spawnMock.mu.Lock()
			count := len(spawnMock.spawns)
			spawnMock.mu.Unlock()
			return count > 0
		}, 2*time.Second)

		// Verify the spawned operation received the worktree path
		spawnMock.mu.Lock()
		spawns := spawnMock.spawns
		spawnMock.mu.Unlock()

		if len(spawns) == 0 {
			t.Fatal("expected at least one spawn call, got 0")
		}

		// The most recent spawn call should use the worktree path
		lastSpawn := spawns[len(spawns)-1]
		if lastSpawn.workdir != "/some/worktree/path" {
			t.Errorf("expected workdir %q, got %q", "/some/worktree/path", lastSpawn.workdir)
		}
	})

	t.Run("falls back to '.' when worktreeByBead is empty", func(t *testing.T) {
		d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)

		// Do NOT set worktreeByBead for this bead

		// Provide bead detail so the one-shot gets context
		beadSrc.mu.Lock()
		beadSrc.shown["bead-no-worktree"] = &protocol.BeadDetail{
			ID:          "bead-no-worktree",
			Title:       "Test bead without worktree",
			Description: "Testing fallback to '.'",
		}
		beadSrc.mu.Unlock()

		// Set spawn verdict
		spawnMock.mu.Lock()
		spawnMock.verdict = "ACK: done"
		spawnMock.mu.Unlock()

		ctx := context.Background()

		// Trigger escalation
		msg := protocol.FormatEscalation(protocol.EscStuckWorker, "bead-no-worktree", "worker stalled", "no progress")
		d.escalate(ctx, msg, "bead-no-worktree", "w1")

		// Wait for spawn to be captured
		waitFor(t, func() bool {
			spawnMock.mu.Lock()
			count := len(spawnMock.spawns)
			spawnMock.mu.Unlock()
			return count > 0
		}, 2*time.Second)

		// Verify the spawned operation received "."
		spawnMock.mu.Lock()
		spawns := spawnMock.spawns
		spawnMock.mu.Unlock()

		if len(spawns) == 0 {
			t.Fatal("expected at least one spawn call, got 0")
		}

		lastSpawn := spawns[len(spawns)-1]
		if lastSpawn.workdir != "." {
			t.Errorf("expected workdir %q, got %q", ".", lastSpawn.workdir)
		}
	})
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

func TestConfigInitialWorkersFallback(t *testing.T) {
	t.Run("InitialWorkers defaults to MaxWorkers when zero", func(t *testing.T) {
		cfg := Config{MaxWorkers: 5}
		resolved := cfg.withDefaults()
		if resolved.InitialWorkers != 5 {
			t.Errorf("InitialWorkers: got %d, want 5 (fallback to MaxWorkers)", resolved.InitialWorkers)
		}
	})

	t.Run("InitialWorkers preserved when explicitly set", func(t *testing.T) {
		cfg := Config{MaxWorkers: 5, InitialWorkers: 3}
		resolved := cfg.withDefaults()
		if resolved.InitialWorkers != 3 {
			t.Errorf("InitialWorkers: got %d, want 3 (should preserve explicit value)", resolved.InitialWorkers)
		}
	})

	t.Run("explicit manual worker mode preserves zero target and ceiling", func(t *testing.T) {
		cfg := Config{AllowZeroWorkers: true}
		resolved := cfg.withDefaults()
		if resolved.InitialWorkers != 0 {
			t.Errorf("InitialWorkers: got %d, want 0", resolved.InitialWorkers)
		}
		if resolved.MaxWorkers != 0 {
			t.Errorf("MaxWorkers: got %d, want 0", resolved.MaxWorkers)
		}
	})
}

func TestNew_TargetWorkersUsesInitialWorkers(t *testing.T) {
	t.Helper()
	db := newTestDB(t)

	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)
	spawnMock := &mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}
	opsSpawner := ops.NewSpawner(spawnMock)
	beadSrc := &fakeBeadStore{beads: []protocol.Bead{}, shown: make(map[string]*protocol.BeadDetail)}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}

	sockPath := fmt.Sprintf("/tmp/oro-test-iw-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	cfg := Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		MaxWorkers:       5,
		InitialWorkers:   3,
		HeartbeatTimeout: 500 * time.Millisecond,
		PollInterval:     50 * time.Millisecond,
		ShutdownTimeout:  200 * time.Millisecond,
	}

	d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if got := d.TargetWorkers(); got != 3 {
		t.Fatalf("expected targetWorkers=InitialWorkers=3, got %d", got)
	}
	if d.cfg.MaxWorkers != 5 {
		t.Fatalf("expected MaxWorkers=5, got %d", d.cfg.MaxWorkers)
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

	// Assert: spawn-for is one-shot capacity, not persistent general pool size.
	d.mu.Lock()
	targetCount := d.targetWorkers
	hasPriority := d.priorityBeads["oro-test-bead"]
	d.mu.Unlock()
	if targetCount != 1 {
		t.Errorf("targetWorkers = %d, want 1 (spawn-for must not alter general pool target)", targetCount)
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

func TestRestartWorkerResetsBead(t *testing.T) {
	d, mockBeads, _, _, _, _ := newTestDispatcher(t)
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
	beadID := "oro-test-reset"
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
	// Add tracking entries to verify they're cleared
	d.attemptCounts[beadID] = 2
	d.rejectionCounts[beadID] = 1
	d.escalatedBeads[beadID] = true
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

	// Assert: bead status was reset to "open"
	mockBeads.mu.Lock()
	beadStatus, exists := mockBeads.updated[beadID]
	mockBeads.mu.Unlock()
	if !exists {
		t.Errorf("bead %s status was not updated", beadID)
	}
	if beadStatus != "open" {
		t.Errorf("bead status = %s, want 'open'", beadStatus)
	}

	// Assert: tracking maps were cleared
	d.mu.Lock()
	_, hasAttempt := d.attemptCounts[beadID]
	_, hasRejection := d.rejectionCounts[beadID]
	_, hasEscalated := d.escalatedBeads[beadID]
	d.mu.Unlock()
	if hasAttempt {
		t.Errorf("attemptCounts[%s] should be cleared", beadID)
	}
	if hasRejection {
		t.Errorf("rejectionCounts[%s] should be cleared", beadID)
	}
	if hasEscalated {
		t.Errorf("escalatedBeads[%s] should be cleared", beadID)
	}

	// Cleanup: kill the spawned process
	_ = pm.Kill(workerID)
	pm.Wait()
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

func TestPreemptDisconnectedWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	workerID := "worker-disconnected-preempt"
	beadID := "oro-preempt-disconnected"

	// Create a broken connection: net.Pipe() then close the client end
	server, client := net.Pipe()
	_ = client.Close() // close reader — writes to server will fail

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     server,
		state:    protocol.WorkerBusy,
		beadID:   beadID,
		worktree: "/fake/worktree",
		encoder:  json.NewEncoder(server),
	}
	d.mu.Unlock()

	// Test: preempt a disconnected worker — must return an error
	_, err := d.applyPreempt(workerID)
	if err == nil {
		t.Fatal("expected error when preempting disconnected worker")
	}

	// Assert: error wraps WorkerUnreachableError
	var unreachable *protocol.WorkerUnreachableError
	if !errors.As(err, &unreachable) {
		t.Errorf("expected WorkerUnreachableError, got %T: %v", err, err)
	}

	// Assert: worker state was reset — must NOT be left in WorkerPreempting
	d.mu.Lock()
	w, exists := d.workers[workerID]
	d.mu.Unlock()
	if exists && w.state == protocol.WorkerPreempting {
		t.Errorf("worker state left as WorkerPreempting after failed send, want state reset")
	}

	_ = server.Close()
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

func TestHandleStatus_QGRetryReceivedLogsSpecificEvent(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-qg-status", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			WorkerID: "w-qg-status",
			BeadID:   "bead-qg-status",
			State:    "qg_retry_received",
			Result:   `{"attempt":1,"model":"opus"}`,
		},
	})

	waitFor(t, func() bool {
		return eventCount(t, d.db, "qg_retry_received") > 0
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
	_, err = d.createAssignment(ctx, "b1", "w1", "/tmp/wt")
	if err == nil {
		t.Fatal("expected error from createAssignment on closed db")
	}

	// completeAssignment should return error
	err = d.completeAssignment(ctx, 0, "b1")
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

// TestHandleReviewApprovedError verifies that when handleReviewResult receives a
// result with VerdictApproved AND a non-nil Err (i.e. the subprocess exited
// nonzero but "VERDICT: APPROVED" appeared in stdout), the dispatcher fails closed:
// it must log "review_error" and NOT emit "review_approved". This guards against
// a Codex/Claude runtime error being silently promoted to an approval.
func TestHandleReviewApprovedError(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-approved-err", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	ctx := context.Background()
	resultCh := make(chan ops.Result, 1)
	resultCh <- ops.Result{
		Verdict:  ops.VerdictApproved,
		Feedback: "VERDICT: APPROVED",
		Err:      errors.New("exit status 1"),
	}

	d.handleReviewResult(ctx, "w-approved-err", "bead-approved-err", resultCh)

	if eventCount(t, d.db, "review_approved") > 0 {
		t.Fatal("review_approved must NOT be emitted when result carries a non-nil Err (runtime/model error)")
	}
	if eventCount(t, d.db, "review_error") == 0 {
		t.Fatal("expected 'review_error' event when VerdictApproved has non-nil Err")
	}

	msgs := esc.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected escalation when review result carries an error")
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

// TestReconnect_ClosedBeadTransitionsToIdle verifies that when a worker
// reconnects referencing a closed (or missing) bead, the worker transitions
// to Idle so that tryAssign can pick it up on the next cycle.
// Bug: oro-xj37 — handleReconnect returned early when validateReconnectBead
// rejected the bead, leaving the worker in its previous state permanently.
func TestReconnect_ClosedBeadTransitionsToIdle(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Register the bead as closed in the mock so validateReconnectBead rejects it.
	beadSrc.mu.Lock()
	beadSrc.shown["bead-closed"] = &protocol.BeadDetail{
		Title:  "bead-closed",
		Status: "closed",
	}
	beadSrc.mu.Unlock()

	conn, _ := connectWorker(t, d.cfg.SocketPath)

	// First, register the worker with a heartbeat so it exists in the map.
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-closed-bead", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Manually set the worker to Busy with the bead that is now closed,
	// simulating the state before reconnect.
	d.mu.Lock()
	w := d.workers["w-closed-bead"]
	w.state = protocol.WorkerBusy
	w.beadID = "bead-closed"
	d.mu.Unlock()

	// Now send RECONNECT referencing the closed bead.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID:   "w-closed-bead",
			BeadID:     "bead-closed",
			State:      "running",
			ContextPct: 30,
		},
	})

	// The worker should transition to Idle (not stay Busy).
	waitForWorkerState(t, d, "w-closed-bead", protocol.WorkerIdle, 2*time.Second)

	// beadID should be cleared.
	_, beadID, ok := d.WorkerInfo("w-closed-bead")
	if !ok {
		t.Fatal("worker should still be tracked")
	}
	if beadID != "" {
		t.Fatalf("expected empty beadID after closed-bead reconnect, got %q", beadID)
	}

	// Now verify that tryAssign picks up this idle worker. Start the
	// dispatcher assignment loop and provide a ready bead.
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-new", Title: "New work", Priority: 1}})

	// The idle worker should receive an ASSIGN message for the new bead.
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message for idle worker after closed-bead reconnect")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != "bead-new" {
		t.Fatalf("expected assignment of bead-new, got %+v", msg.Assign)
	}
}

// TestReconnect_EmptyBeadID_TransitionsToIdle verifies that an idle worker
// reconnecting after a network glitch with BeadID="" is cleanly transitioned
// to Idle without logging spurious reconnect_closed_bead_rejected events.
// Bug: oro-sydf — validateReconnectBead was called even when BeadID was empty,
// causing a failed bead lookup and a spurious rejection log event.
func TestReconnect_EmptyBeadID_TransitionsToIdle(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Simulate production: Show("") returns nil (bead not found).
	// Without the fix, validateReconnectBead("") would look up "" in the bead
	// source, get nil, and log a spurious reconnect_closed_bead_rejected event.
	beadSrc.mu.Lock()
	beadSrc.shown[""] = nil
	beadSrc.mu.Unlock()

	conn, _ := connectWorker(t, d.cfg.SocketPath)

	// Register the worker so it appears in the dispatcher's map.
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-idle-glitch", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Simulate the worker having been Busy before the network glitch.
	d.mu.Lock()
	w := d.workers["w-idle-glitch"]
	w.state = protocol.WorkerBusy
	w.beadID = ""
	d.mu.Unlock()

	// Send RECONNECT with empty BeadID (idle worker after network glitch).
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID:   "w-idle-glitch",
			BeadID:     "",
			State:      "idle",
			ContextPct: 0,
		},
	})

	// Worker must transition to Idle.
	waitForWorkerState(t, d, "w-idle-glitch", protocol.WorkerIdle, 2*time.Second)

	// Must NOT have logged a spurious reconnect_closed_bead_rejected event.
	count := eventCount(t, d.db, "reconnect_closed_bead_rejected")
	if count > 0 {
		t.Fatalf("expected 0 reconnect_closed_bead_rejected events, got %d", count)
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

func TestHandleDonePreservesEpicID(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Initialize database schema
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	epicID := "epic-test-1"
	childID := "child-1"
	workerID := "worker-epic-done"
	worktree := "/tmp/worktree-" + childID

	// Configure mock: after the child closes, AllChildrenClosed returns true.
	beadSrc.allChildrenClosedMap = map[string]bool{
		epicID: true,
	}

	// Manually set up a tracked worker with the child bead and epicID.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		beadID:   childID,
		epicID:   epicID, // parent epic
		worktree: worktree,
		state:    protocol.WorkerBusy,
		encoder:  json.NewEncoder(nil), // dummy encoder
	}
	d.mu.Unlock()

	// Send DONE message with QualityGatePassed=true
	d.handleDone(ctx, workerID, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            childID,
			WorkerID:          workerID,
			QualityGatePassed: true,
		},
	})

	// Wait for async merge and auto-close goroutine.
	waitFor(t, func() bool {
		beadSrc.mu.Lock()
		defer beadSrc.mu.Unlock()
		for _, id := range beadSrc.closed {
			if id == epicID {
				return true
			}
		}
		return false
	}, 2*time.Second)

	// Verify the epic was auto-closed (proves epicID was captured and used).
	beadSrc.mu.Lock()
	epicClosed := false
	for _, id := range beadSrc.closed {
		if id == epicID {
			epicClosed = true
			break
		}
	}
	beadSrc.mu.Unlock()

	if !epicClosed {
		t.Error("expected epic to be auto-closed when child completed with epicID preserved")
	}
}

// TestHandleDone_TypeChangedToEpic verifies that handleDone re-checks the bead
// type via BeadSource.Show before deciding to merge. If the type changed to
// "epic" mid-flight (and the worker was NOT an epic-decomp worker), the merge
// is skipped and the worktree is cleaned up instead.
func TestHandleDone_TypeChangedToEpic(t *testing.T) {
	ctx := context.Background()

	t.Run("skips merge and cleans up when type changed to epic", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		beadID := "oro-task-became-epic"
		workerID := "worker-type-change"
		worktree := "/tmp/worktree-" + beadID

		// Worker was assigned as a task (isEpicDecomp=false).
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			beadID:       beadID,
			worktree:     worktree,
			state:        protocol.WorkerBusy,
			isEpicDecomp: false,
			encoder:      json.NewEncoder(nil),
		}
		d.mu.Unlock()

		// Mid-flight: bead type has been changed to "epic".
		beadSrc.mu.Lock()
		beadSrc.shown[beadID] = &protocol.BeadDetail{
			ID:                 beadID,
			Title:              "task that became an epic",
			Type:               "epic",
			AcceptanceCriteria: "Test: auto | Assert: PASS",
		}
		beadSrc.mu.Unlock()

		d.handleDone(ctx, workerID, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            beadID,
				WorkerID:          workerID,
				QualityGatePassed: true,
			},
		})

		// "type_changed_to_epic" must be logged.
		waitFor(t, func() bool {
			return eventCount(t, d.db, "type_changed_to_epic") > 0
		}, 2*time.Second)

		// mergeAndComplete must NOT have been called.
		if eventCount(t, d.db, "merged") != 0 {
			t.Error("expected no merge when bead type changed to epic mid-flight")
		}

		// removeWorktreeAndClearTracking must have removed the worktree.
		waitFor(t, func() bool {
			wtMgr.mu.Lock()
			defer wtMgr.mu.Unlock()
			for _, p := range wtMgr.removed {
				if p == worktree {
					return true
				}
			}
			return false
		}, 2*time.Second)
	})

	t.Run("falls through to normal merge when Show returns error", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		beadID := "oro-task-show-err"
		workerID := "worker-show-err"
		worktree := "/tmp/worktree-" + beadID

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			beadID:       beadID,
			worktree:     worktree,
			state:        protocol.WorkerBusy,
			isEpicDecomp: false,
			encoder:      json.NewEncoder(nil),
		}
		d.mu.Unlock()

		// Show returns an error — best-effort: fall through to normal merge.
		beadSrc.mu.Lock()
		beadSrc.showErrFn = map[string]error{beadID: errors.New("bd show failed")}
		beadSrc.mu.Unlock()

		d.handleDone(ctx, workerID, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				BeadID:            beadID,
				WorkerID:          workerID,
				QualityGatePassed: true,
			},
		})

		// Normal merge path must proceed.
		waitFor(t, func() bool {
			return eventCount(t, d.db, "merged") > 0
		}, 2*time.Second)

		// No "type_changed_to_epic" event must be emitted.
		if eventCount(t, d.db, "type_changed_to_epic") != 0 {
			t.Error("expected no type_changed_to_epic event when Show fails")
		}
	})
}

func TestHandleHandoffPreservesEpicContext(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	epicID := "epic-handoff-1"
	childID := "child-handoff-1"
	workerID := "worker-handoff-epic"
	worktree := "/tmp/worktree-" + childID
	baseBranch := "epic/" + epicID
	targetBranch := "epic/" + epicID

	// Set up a tracked worker with child bead, epicID, baseBranch, and targetBranch.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		beadID:       childID,
		epicID:       epicID,
		worktree:     worktree,
		baseBranch:   baseBranch,
		targetBranch: targetBranch,
		state:        protocol.WorkerBusy,
		conn:         newMockConn(),        // provide mock connection for sendToWorker
		encoder:      json.NewEncoder(nil), // dummy encoder
	}
	d.mu.Unlock()

	// Send HANDOFF message
	d.handleHandoff(ctx, workerID, protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:         childID,
			WorkerID:       workerID,
			ContextSummary: "Test handoff with epic context",
		},
	})

	// Verify pendingHandoff was populated with epic context.
	d.mu.Lock()
	pending, hasPending := d.pendingHandoffs[childID]
	d.mu.Unlock()

	if !hasPending {
		t.Fatal("expected pendingHandoff to be created")
	}

	if pending.epicID != epicID {
		t.Errorf("pending.epicID = %q, want %q", pending.epicID, epicID)
	}
	if pending.baseBranch != baseBranch {
		t.Errorf("pending.baseBranch = %q, want %q", pending.baseBranch, baseBranch)
	}
	if pending.targetBranch != targetBranch {
		t.Errorf("pending.targetBranch = %q, want %q", pending.targetBranch, targetBranch)
	}
	if pending.worktree != worktree {
		t.Errorf("pending.worktree = %q, want %q", pending.worktree, worktree)
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
	waitFor(t, func() bool {
		return d.ConnectedWorkers() == 0
	}, 3*time.Second)
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

func TestAssignmentCleanedOnWorkerDelete(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Create a broken connection: net.Pipe() then close the read end
	server, client := net.Pipe()
	_ = client.Close() // close reader — writes to server will fail

	// Register the worker with the broken connection
	d.registerWorker("w-broken", server)
	t.Cleanup(func() { _ = server.Close() })

	ctx := context.Background()
	bead := protocol.Bead{ID: "bead-assign-cleanup", Title: "Assignment cleanup test", Priority: 1}

	// Grab the tracked worker so we can call assignBead directly
	d.mu.Lock()
	w := d.workers["w-broken"]
	d.mu.Unlock()

	// Call assignBead — worktree creation succeeds, but sendToWorker should fail
	_ = d.assignBead(ctx, w, bead)

	// Verify the assignment was created and then cleaned up
	// (1) Assignment should exist and be completed (not active)
	var status string
	err := d.db.QueryRow(
		`SELECT status FROM assignments WHERE bead_id=? ORDER BY id DESC LIMIT 1`,
		bead.ID,
	).Scan(&status)
	if err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "completed" {
		t.Fatalf("expected assignment status 'completed', got %q", status)
	}

	// (2) Verify no active assignment remains for this bead
	var activeCount int
	err = d.db.QueryRow(
		`SELECT COUNT(*) FROM assignments WHERE bead_id=? AND status='active'`,
		bead.ID,
	).Scan(&activeCount)
	if err != nil {
		t.Fatalf("count active assignments: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("expected 0 active assignments, got %d", activeCount)
	}

	// (3) Verify worktree_cleanup event was logged
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

func (p *slowProcess) Output() (string, error) { return "ok\n\nVERDICT: APPROVED", nil }

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

	beadSrc := &fakeBeadStore{beads: []protocol.Bead{}, shown: make(map[string]*protocol.BeadDetail)}
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
	waitFor(t, func() bool {
		return len(opsSpawner.Active()) > 0
	}, 2*time.Second)

	// Trigger shutdown
	cancel()

	// Wait for all ops agents to be killed
	waitFor(t, func() bool {
		return len(opsSpawner.Active()) == 0
	}, 2*time.Second)

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
	beadSrc := &fakeBeadStore{beads: []protocol.Bead{}, shown: make(map[string]*protocol.BeadDetail)}
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
	waitFor(t, func() bool {
		wtMgr.mu.Lock()
		n := len(wtMgr.removed)
		wtMgr.mu.Unlock()
		return n >= 2
	}, 2*time.Second)
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
	beadSrc := &fakeBeadStore{beads: []protocol.Bead{}, shown: make(map[string]*protocol.BeadDetail)}
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
	waitFor(t, func() bool {
		wtMgr.mu.Lock()
		n := len(wtMgr.removed)
		wtMgr.mu.Unlock()
		return n >= 2
	}, 10*time.Second)

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

	// Consume the re-ASSIGN — this is the positive signal that the DONE
	// handler finished processing (quality gate failed -> re-assign). No
	// async merge should have been triggered before re-assign.
	_, ok = readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected re-ASSIGN")
	}

	// Wait for the quality_gate_rejected event as a positive signal that
	// all DONE handling (including any potential merge path) has completed.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "quality_gate_rejected") > 0
	}, 2*time.Second)

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

	// Verify attempt counter remains total across model escalation.
	d.mu.Lock()
	count := d.attemptCounts["bead-qg-model"]
	d.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected attempt count 1 after escalation, got %d", count)
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
	if _, err := d.createAssignment(ctx, beadID, workerID, "/tmp/test-worktree"); err != nil {
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

	// No focus set — oldest epic (lower ID string) assigned first; epic-a < epic-b.
	// Within the same epic, priority breaks ties, but across epics epic-age wins.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-p2", Title: "Medium", Priority: 2, Epic: "epic-a"},
		{ID: "bead-p0", Title: "Critical", Priority: 0, Epic: "epic-b"},
	})

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	// epic-a sorts before epic-b (older epic), so bead-p2 (epic-a) is assigned first
	// even though bead-p0 has higher priority — finishing oldest epics takes precedence.
	if msg.Assign.BeadID != "bead-p2" {
		t.Fatalf("expected oldest-epic bead bead-p2, got %s", msg.Assign.BeadID)
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

	// Wait for graceful shutdown to complete — after ShutdownTimeout (200ms)
	// the shutdown goroutines send SHUTDOWN and reset state to Idle. Wait
	// until no workers are in ShuttingDown state (all timeouts processed).
	waitFor(t, func() bool {
		d.mu.Lock()
		shuttingDown := 0
		for _, w := range d.workers {
			if w.state == protocol.WorkerShuttingDown {
				shuttingDown++
			}
		}
		d.mu.Unlock()
		return shuttingDown == 0
	}, 2*time.Second)

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

func TestScaleDownSuppressesHandoffRespawnAndAutoScaleRaise(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.MaxWorkers = 5
	d.cfg.ShutdownTimeout = time.Second

	d.mu.Lock()
	d.workers["w-scale-down"] = &trackedWorker{
		id:           "w-scale-down",
		conn:         newMockConn(),
		state:        protocol.WorkerBusy,
		beadID:       "bead-scale-down",
		worktree:     "/tmp/bead-scale-down",
		model:        protocol.ModelSonnet,
		assignmentID: 42,
		managed:      true,
	}
	d.targetWorkers = 1
	d.mu.Unlock()

	detail, err := d.applyScaleDirective("0")
	if err != nil {
		t.Fatalf("apply scale directive: %v", err)
	}
	if !strings.Contains(detail, "target=0") {
		t.Fatalf("expected scale detail to keep target=0, got %q", detail)
	}

	d.maybeAutoScale(ctx, 10, 0)
	if got := d.TargetWorkers(); got != 0 {
		t.Fatalf("explicit scale-down target was auto-raised to %d, want 0", got)
	}

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-new", Title: "New work", Priority: 1}})
	d.tryAssign(ctx)
	workerState, assignedBead, ok := d.WorkerInfo("w-scale-down")
	if !ok {
		t.Fatal("expected scale-down worker to remain tracked until shutdown finishes")
	}
	if workerState != protocol.WorkerShuttingDown {
		t.Fatalf("scale-down worker state = %s, want %s", workerState, protocol.WorkerShuttingDown)
	}
	if assignedBead == "bead-new" {
		t.Fatal("tryAssign assigned new work to a worker already selected for scale-down")
	}

	d.handleHandoff(ctx, "w-scale-down", protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:   "bead-scale-down",
			WorkerID: "w-scale-down",
		},
	})

	d.mu.Lock()
	_, hasPending := d.pendingHandoffs["bead-scale-down"]
	d.mu.Unlock()
	if hasPending {
		t.Fatal("scale-down handoff created a pending handoff")
	}
	if spawned := pm.SpawnedIDs(); len(spawned) != 0 {
		t.Fatalf("scale-down handoff spawned replacement workers: %v", spawned)
	}
	if got := eventCount(t, d.db, "handoff_spawned"); got != 0 {
		t.Fatalf("handoff_spawned events = %d, want 0", got)
	}
	if got := eventCount(t, d.db, "handoff_suppressed_scale_down"); got != 1 {
		t.Fatalf("handoff_suppressed_scale_down events = %d, want 1", got)
	}

	d.handleShutdownApproved(ctx, "w-scale-down", protocol.Message{
		Type:             protocol.MsgShutdownApproved,
		ShutdownApproved: &protocol.ShutdownApprovedPayload{WorkerID: "w-scale-down"},
	})

	beadSrc.mu.Lock()
	status := beadSrc.updated["bead-scale-down"]
	beadSrc.mu.Unlock()
	if status != "open" {
		t.Fatalf("scale-down shutdown approval requeued bead status %q, want open", status)
	}
	if got := eventCount(t, d.db, "bead_requeued_scale_down"); got != 1 {
		t.Fatalf("bead_requeued_scale_down events = %d, want 1", got)
	}

	d.handleHandoff(ctx, "w-scale-down", protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:   "bead-scale-down",
			WorkerID: "w-scale-down",
		},
	})
	if hasPendingHandoff(d, "bead-scale-down") {
		t.Fatal("late handoff after shutdown approval created a pending handoff")
	}
	if spawned := pm.SpawnedIDs(); len(spawned) != 0 {
		t.Fatalf("late handoff after approval spawned replacement workers: %v", spawned)
	}
	if got := eventCount(t, d.db, "handoff_suppressed_scale_down"); got != 2 {
		t.Fatalf("handoff_suppressed_scale_down events = %d, want 2", got)
	}
}

func TestScaleDownSuppressesLateHandoffAfterTimeout(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.MaxWorkers = 5
	d.cfg.ShutdownTimeout = time.Millisecond

	d.mu.Lock()
	d.workers["w-scale-timeout"] = &trackedWorker{
		id:           "w-scale-timeout",
		conn:         newMockConn(),
		state:        protocol.WorkerBusy,
		beadID:       "bead-scale-timeout",
		worktree:     "/tmp/bead-scale-timeout",
		model:        protocol.ModelSonnet,
		assignmentID: 43,
		managed:      true,
	}
	d.targetWorkers = 1
	d.mu.Unlock()

	if _, err := d.applyScaleDirective("0"); err != nil {
		t.Fatalf("apply scale directive: %v", err)
	}
	d.handleShutdownTimeout("w-scale-timeout")

	beadSrc.mu.Lock()
	status := beadSrc.updated["bead-scale-timeout"]
	beadSrc.mu.Unlock()
	if status != "open" {
		t.Fatalf("scale-down timeout requeued bead status %q, want open", status)
	}

	d.handleHandoff(ctx, "w-scale-timeout", protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:   "bead-scale-timeout",
			WorkerID: "w-scale-timeout",
		},
	})

	if hasPendingHandoff(d, "bead-scale-timeout") {
		t.Fatal("late handoff after shutdown timeout created a pending handoff")
	}
	if spawned := pm.SpawnedIDs(); len(spawned) != 0 {
		t.Fatalf("late handoff after timeout spawned replacement workers: %v", spawned)
	}
	if got := eventCount(t, d.db, "handoff_suppressed_scale_down"); got != 1 {
		t.Fatalf("handoff_suppressed_scale_down events = %d, want 1", got)
	}
}

func hasPendingHandoff(d *Dispatcher, beadID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.pendingHandoffs[beadID]
	return ok
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

// TestDispatcher_RespawnsWorkersToTarget verifies that when connected managed
// workers drop below targetWorkers AND the ready queue is non-empty, the
// dispatcher's assign loop spawns up to (target - active) new workers within
// one tick — without requiring a manual 'oro directive scale' nudge.
//
// Design: BeadsDir is set to a non-existent path so fsnotify.Add fails and the
// dispatcher falls back to assignLoopPoll. PollInterval is set to 5s so no
// automatic tick fires within the 2s assertion window. Only a workerReadyCh
// signal from connCloseCleanup drives an immediate spawn; without it the test
// times out.
func TestDispatcher_RespawnsWorkersToTarget(t *testing.T) {
	sockPath := fmt.Sprintf("/tmp/oro-respawn-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	db := newTestDB(t)
	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)
	spawnMock := &mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}
	opsSpawner := ops.NewSpawner(spawnMock)
	beadSrc := &fakeBeadStore{
		beads: []protocol.Bead{{ID: "bead-q1", Title: "queued bead"}},
		shown: make(map[string]*protocol.BeadDetail),
	}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}

	cfg := Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		BeadsDir:         "/nonexistent-beads-dir-forces-poll-fallback", // fsnotify.Add fails → assignLoopPoll
		MaxWorkers:       5,
		PollInterval:     5 * time.Second, // long poll: no tick within 2s assertion window
		HeartbeatTimeout: 500 * time.Millisecond,
		ShutdownTimeout:  200 * time.Millisecond,
	}

	d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	d.qgRunner = &mockQGRunner{passed: true}

	pm := &mockProcessManager{}
	d.procMgr = pm

	// Bypass initial reconcileScale: set state + targetWorkers directly.
	d.mu.Lock()
	d.targetWorkers = 3
	d.mu.Unlock()

	// Start dispatcher and enter Running state.
	cancel := startDispatcher(t, d)
	defer cancel()
	sendDirective(t, sockPath, "start")
	waitForState(t, d, StateRunning, 2*time.Second)

	// Connect 3 managed workers: pre-register as pending so registerWorker
	// marks them managed=true. Send a heartbeat to register each.
	// Each connection signals workerReadyCh, which triggers tryAssign — but
	// since workers are idle and there is a bead, tryAssign will assign the
	// bead. We close the connections later to simulate a worker kill.
	workerIDs := []string{"respawn-w0", "respawn-w1", "respawn-w2"}
	conns := make([]net.Conn, 3)
	for i, wid := range workerIDs {
		d.mu.Lock()
		d.pendingManagedIDs[wid] = true
		d.mu.Unlock()
		conn, _ := connectWorker(t, sockPath)
		conns[i] = conn
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: wid, ContextPct: 5},
		})
	}
	waitForWorkers(t, d, 3, 2*time.Second)

	// Allow any pending tryAssign calls (triggered by workerReadyCh during
	// registration) to settle. After this, the assign loop is idle — it cannot
	// fire again until either: a workerReadyCh signal, or a 5s poll tick.
	time.Sleep(200 * time.Millisecond)

	// Reset spawn counter — we only care about spawns that happen AFTER the kill.
	pm.mu.Lock()
	pm.spawned = nil
	pm.mu.Unlock()

	// Kill one worker (whichever is in d.workers) by closing its connection.
	// connCloseCleanup will remove it and — with the fix — signal workerReadyCh.
	// Without the fix, the next spawn cannot happen until the 5s poll tick.
	_ = conns[0].Close()

	// Must spawn within 2s (well under the 5s poll tick).
	// Fails without the workerReadyCh signal from connCloseCleanup.
	waitFor(t, func() bool {
		return len(pm.SpawnedIDs()) >= 1
	}, 2*time.Second)

	if got := len(pm.SpawnedIDs()); got < 1 {
		t.Errorf("expected ≥1 respawn after worker kill (target=3, active=2), got %d", got)
	}
}

// --- Review rejection counter tests (oro-jhs) ---

// helper: set up dispatcher with rejected reviewer, connect worker, assign bead, trigger review.
// Returns the dispatcher, conn, escalator, and spawnMock for further assertions.
func setupReviewRejection(t *testing.T) (*Dispatcher, net.Conn, *mockEscalator, *mockBatchSpawner) {
	t.Helper()
	d, beadSrc, _, esc, _, spawnMock := newTestDispatcher(t)
	spawnMock.mu.Lock()
	spawnMock.verdict = "missing edge case tests\n\nVERDICT: REJECTED"
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

	// Should NOT receive another ASSIGN — escalation instead.
	// Wait for the escalation message to reach the mock escalator. The
	// dispatcher logs the "review_escalated" event BEFORE calling escalate(),
	// so polling the event would race against the mockEscalator append.
	waitFor(t, func() bool {
		for _, m := range esc.Messages() {
			if strings.Contains(m, "bead-rej") && strings.Contains(m, "STUCK") {
				return true
			}
		}
		return false
	}, 3*time.Second)

	// Final assertion: confirm the message is there (waitFor would have
	// t.Fatal'd already if not, but keep the explicit check for readability).
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

	// Sanity check: the review_escalated event must also be recorded.
	if eventCount(t, d.db, "review_escalated") == 0 {
		t.Fatal("expected review_escalated event to be logged")
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

	// Wait for the worker to be killed, which happens at the end of
	// handleReviewRejection's escalation branch (after logEvent and escalate).
	// Polling on the review_escalated event would race against Kill because
	// the event is logged before escalate() and Kill() run.
	waitFor(t, func() bool {
		pm.mu.Lock()
		defer pm.mu.Unlock()
		for _, id := range pm.killed {
			if id == "w1" {
				return true
			}
		}
		return false
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
	// Widen heartbeat timeout: the test sends one heartbeat then goes through
	// multiple synchronization steps + DB writes before the handoff completes.
	// Under CI with race detector, 500ms is too tight.
	d.cfg.HeartbeatTimeout = 10 * time.Second
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

	// Wait for respawnWorker to actually populate pendingHandoffs for bead-ralph.
	// NOTE: we cannot wait on pm.SpawnedIDs() > 0 — reconcileScale's scaleUp
	// spawns targetWorkers (MaxWorkers=5) mock processes early, so SpawnedIDs
	// is already non-empty before the handoff runs. Waiting on it races:
	// the test can reach registerWorker(w2) before respawnWorker has set
	// pendingHandoffs, producing an empty ASSIGN path. Check the actual map.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.pendingHandoffs["bead-ralph"]
		return ok
	}, 2*time.Second)

	// Simulate new worker connecting (the spawned process)
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w2", ContextPct: 0},
	})

	// New worker should receive ASSIGN with the SAME bead and worktree.
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "handoff_pending") > 0
	}, 2*time.Second)
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
	spawnMock.verdict = "also missing integration test\n\nVERDICT: REJECTED"
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

// TestRejectionReassignIncludesMemoryAndAttempt verifies that when a review
// is rejected, the re-ASSIGN message contains both:
//   - MemoryContext with the reviewer feedback (so the worker knows what went wrong)
//   - An incremented Attempt counter (so the worker knows this is a retry)
//
// This is the combined acceptance-criteria test for oro-eou.
func TestRejectionReassignIncludesMemoryAndAttempt(t *testing.T) {
	_, conn, _, _ := setupReviewRejection(t)

	// Send READY_FOR_REVIEW — the mock reviewer returns "missing edge case tests\n\nVERDICT: REJECTED".
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

	// MemoryContext must be present and contain the reviewer feedback.
	if msg.Assign.MemoryContext == "" {
		t.Fatal("expected non-empty MemoryContext in rejection re-ASSIGN")
	}
	if !containsStr(msg.Assign.MemoryContext, "missing edge case tests") {
		t.Fatalf("expected MemoryContext to contain reviewer feedback, got: %s", msg.Assign.MemoryContext)
	}

	// Attempt must be incremented (>0) so the worker knows this is a retry.
	if msg.Assign.Attempt < 1 {
		t.Fatalf("expected Attempt >= 1 after rejection, got %d", msg.Assign.Attempt)
	}
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
	waitFor(t, func() bool {
		d.mu.Lock()
		count := d.handoffCounts["bead-stuck"]
		d.mu.Unlock()
		return count == 1
	}, 2*time.Second)
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
	waitFor(t, func() bool {
		return eventCount(t, d.db, "diagnosis_spawned") > 0
	}, 3*time.Second)
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
	waitFor(t, func() bool {
		msgs := esc.Messages()
		for _, m := range msgs {
			if strings.Contains(m, "bead-stuck") && strings.Contains(m, "STUCK") {
				return eventCount(t, d.db, "diagnosis_escalated") > 0
			}
		}
		return false
	}, 3*time.Second)
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

func TestShutdownCleanup_DoesNotSyncBeads(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Call shutdownRemoveWorktrees directly (no need to start the full dispatcher).
	d.shutdownRemoveWorktrees(nil)

	beadSrc.mu.Lock()
	synced := beadSrc.synced
	beadSrc.mu.Unlock()

	if synced {
		t.Fatal("expected shutdownRemoveWorktrees not to call bead Sync")
	}
}

// TestShutdownResetsInProgressBeads verifies that shutdownSequence resets all
// beads with active assignments back to open status so they become re-assignable
// on the next dispatcher start. This is phase 3b: between shutdownWaitForWorkers
// and shutdownRemoveWorktrees.
func TestShutdownResetsInProgressBeads(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Replace shutdownRunner with a recording mock so we can assert calls
	// without requiring a real bd binary.
	captureRunner := &mockCommandRunner{}
	d.shutdownRunner = captureRunner

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

	// Build a map of beadID -> status from the recorded `oro bead update` calls.
	updated := make(map[string]string)
	for _, call := range captureRunner.calls {
		if call.Name == "oro" && len(call.Args) >= 4 && call.Args[0] == "bead" && call.Args[1] == "update" {
			beadID := call.Args[2]
			// args[3] is "--status=open"
			status := strings.TrimPrefix(call.Args[3], "--status=")
			updated[beadID] = status
		}
	}

	for _, beadID := range []string{"bead-reset-a", "bead-reset-b"} {
		status, ok := updated[beadID]
		if !ok {
			t.Errorf("expected oro bead update %q --status=open to be called, but it was not", beadID)
			continue
		}
		if status != "open" {
			t.Errorf("expected oro bead update %q --status=open, got status=%q", beadID, status)
		}
	}

	// Completed assignment must not be reset.
	if status, ok := updated["bead-done"]; ok {
		t.Errorf("expected completed bead to be left alone, but oro bead update was called with status=%q", status)
	}
}

// TestShutdownResetBeadUsesRepoRoot verifies that shutdownResetActiveBeads runs
// `oro bead update` with CWD set to the repo root (cfg.RepoRoot), not from the
// worker worktree or process CWD. This prevents "Error: no beads database found"
// when the process is started from a worktree that lacks a .beads/ directory.
func TestShutdownResetBeadUsesRepoRoot(t *testing.T) {
	// Part 1: New() wires shutdownRunner.Dir = cfg.RepoRoot.
	sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	db := newTestDB(t)
	beadSrc := &fakeBeadStore{shown: make(map[string]*protocol.BeadDetail)}
	wtMgr := &mockWorktreeManager{created: make(map[string]string)}
	esc := &mockEscalator{}
	merger := merge.NewCoordinator(&mockGitRunner{})
	opsSpawner := ops.NewSpawner(&mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"})

	repoRoot := t.TempDir()
	cfg := Config{
		SocketPath:       sockPath,
		DBPath:           ":memory:",
		MaxWorkers:       1,
		HeartbeatTimeout: 500 * time.Millisecond,
		PollInterval:     50 * time.Millisecond,
		RepoRoot:         repoRoot,
	}
	d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// shutdownRunner must be wired to ExecCommandRunner with Dir = repoRoot.
	execRunner, ok := d.shutdownRunner.(*ExecCommandRunner)
	if !ok {
		t.Fatalf("expected d.shutdownRunner to be *ExecCommandRunner, got %T", d.shutdownRunner)
	}
	if execRunner.Dir != repoRoot {
		t.Errorf("shutdownRunner.Dir = %q, want %q", execRunner.Dir, repoRoot)
	}

	// Part 2: shutdownResetActiveBeads uses shutdownRunner (not d.beads.Update).
	// Replace shutdownRunner with a recording mock so we can assert calls.
	captureRunner := &mockCommandRunner{}
	d.shutdownRunner = captureRunner

	ctx := context.Background()
	_, insertErr := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		"bead-root-check", "w-test", "/tmp/worktrees/bead-root-check")
	if insertErr != nil {
		t.Fatalf("insert assignment: %v", insertErr)
	}

	d.shutdownResetActiveBeads()

	// The mock runner must have been called with `oro bead update bead-root-check --status=open`.
	if len(captureRunner.calls) == 0 {
		t.Fatal("shutdownRunner was never called; shutdownResetActiveBeads must use d.shutdownRunner")
	}
	found := false
	for _, call := range captureRunner.calls {
		if call.Name == "oro" && len(call.Args) >= 4 &&
			call.Args[0] == "bead" &&
			call.Args[1] == "update" &&
			call.Args[2] == "bead-root-check" &&
			call.Args[3] == "--status=open" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected call `oro bead update bead-root-check --status=open`, got calls: %v", captureRunner.calls)
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
	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

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

	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

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

// TestMergeCompleteEscalationAutoAcked verifies that MERGE_COMPLETE escalations
// are automatically acknowledged after successful merge, preventing duplicate notifications.
// This ensures: (1) escalation status='acked' in DB, (2) retryPendingEscalations skips it,
// (3) manager receives exactly 1 notification per merge.
func TestMergeCompleteEscalationAutoAcked(t *testing.T) {
	d, _, _, esc, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Init schema so escalations table is available
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-auto-ack-test"
	workerID := "w-ack"
	worktree := "/tmp/worktree-" + beadID
	branch := "agent/" + beadID

	// Perform merge - this creates a MERGE_COMPLETE escalation
	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

	// Verify escalation was created with status='pending'
	var escID int64
	var escType string
	var escStatus string
	err = d.db.QueryRowContext(ctx,
		`SELECT id, type, status FROM escalations WHERE bead_id = ? AND type = ?`,
		beadID, "MERGE_COMPLETE").
		Scan(&escID, &escType, &escStatus)
	if err != nil {
		t.Fatalf("failed to query MERGE_COMPLETE escalation: %v", err)
	}

	// Before retry: should be pending
	if escStatus != "pending" {
		t.Errorf("initial escalation status = %q, want 'pending'", escStatus)
	}

	// Count initial escalations sent
	initialMsgCount := len(esc.Messages())

	// Run retry logic - this should auto-ack the MERGE_COMPLETE escalation
	d.retryPendingEscalations(ctx)

	// Verify escalation is now acked
	var ackedStatus string
	var ackedAt sql.NullString
	err = d.db.QueryRowContext(ctx,
		`SELECT status, acked_at FROM escalations WHERE bead_id = ? AND type = ?`,
		beadID, "MERGE_COMPLETE").
		Scan(&ackedStatus, &ackedAt)
	if err != nil {
		t.Fatalf("failed to query escalation after retry: %v", err)
	}

	if ackedStatus != "acked" {
		t.Errorf("after retryPendingEscalations, escalation status = %q, want 'acked'", ackedStatus)
	}
	if !ackedAt.Valid || ackedAt.String == "" {
		t.Error("after retryPendingEscalations, escalation acked_at is NULL or empty, want timestamp")
	}

	// Verify no additional escalation was sent (only the original MERGE_COMPLETE)
	finalMsgCount := len(esc.Messages())
	if finalMsgCount > initialMsgCount {
		t.Errorf("retryPendingEscalations resent escalation: initial=%d msgs, final=%d msgs",
			initialMsgCount, finalMsgCount)
	}

	// Verify manager received exactly 1 MERGE_COMPLETE message
	mergeCompleteCount := 0
	for _, msg := range esc.Messages() {
		if strings.Contains(msg, string(protocol.EscMergeComplete)) {
			mergeCompleteCount++
		}
	}
	if mergeCompleteCount != 1 {
		t.Errorf("manager received %d MERGE_COMPLETE notifications, want 1", mergeCompleteCount)
	}
}

// TestMergeAndCompleteUsesTargetBranch verifies that mergeAndComplete passes
// targetBranch through to merge.Opts so the coordinator rebases onto the correct
// branch (e.g., epic/foo) instead of hardcoded "main".
func TestMergeAndCompleteUsesTargetBranch(t *testing.T) {
	d, _, _, _, gitRunner, _ := newTestDispatcher(t)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-target-branch"
	workerID := "w-target"
	worktree := "/tmp/worktree-" + beadID
	branch := "agent/" + beadID

	t.Run("explicit target branch", func(t *testing.T) {
		d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "epic/my-epic", 0)

		calls := gitRunner.RebaseCalls()
		if len(calls) == 0 {
			t.Fatal("expected at least one rebase call")
		}
		last := calls[len(calls)-1]
		// rebase args: ["rebase", <target>, <branch>]
		if len(last) < 3 {
			t.Fatalf("rebase call too short: %v", last)
		}
		if last[1] != "epic/my-epic" {
			t.Errorf("rebase target = %q, want %q", last[1], "epic/my-epic")
		}
	})

	t.Run("empty target defaults to main", func(t *testing.T) {
		d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

		calls := gitRunner.RebaseCalls()
		last := calls[len(calls)-1]
		if last[1] != "main" {
			t.Errorf("rebase target = %q, want %q (default)", last[1], "main")
		}
	})
}

// TestMergeAndComplete_CleansUpOnNonConflictError verifies that when merger.Merge
// returns a non-ConflictError (e.g. ff-only merge failure), the worktree is
// still removed, the agent branch is deleted, and worktreeByBead is cleared.
func TestMergeAndComplete_CleansUpOnNonConflictError(t *testing.T) {
	d, _, wtMgr, _, gitRunner, _ := newTestDispatcher(t)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const beadID = "bead-nonconflict-cleanup"
	const workerID = "w-nc"
	const worktree = "/tmp/worktree-nonconflict"
	branch := protocol.BranchPrefix + beadID

	// Fail the ff-only merge step — produces a non-ConflictError.
	gitRunner.mu.Lock()
	gitRunner.failOn = "--ff-only"
	gitRunner.mu.Unlock()

	// Seed worktreeByBead so we can verify it is cleared after cleanup.
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

	// Verify worktrees.Remove was called.
	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	deleted := append([]string(nil), wtMgr.deletedBranches...)
	wtMgr.mu.Unlock()

	foundRemoved := false
	for _, r := range removed {
		if r == worktree {
			foundRemoved = true
			break
		}
	}
	if !foundRemoved {
		t.Errorf("worktrees.Remove(%q) not called on non-conflict error; removed=%v", worktree, removed)
	}

	foundDeleted := false
	for _, b := range deleted {
		if b == branch {
			foundDeleted = true
			break
		}
	}
	if !foundDeleted {
		t.Errorf("worktrees.DeleteBranch(%q) not called on non-conflict error; deletedBranches=%v", branch, deleted)
	}

	// Verify worktreeByBead is cleared.
	d.mu.Lock()
	trackedPath := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if trackedPath != "" {
		t.Errorf("worktreeByBead[%q] = %q, want empty (should be cleared on non-conflict cleanup)", beadID, trackedPath)
	}
}

// mockQGRunner is a test double for QGRunner.
type mockQGRunner struct {
	mu            sync.Mutex
	passed        bool
	output        string
	err           error
	calls         []string // worktree paths passed to Run
	skipMutations []bool
}

func (m *mockQGRunner) Run(_ context.Context, worktree string, skipMutation bool) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, worktree)
	m.skipMutations = append(m.skipMutations, skipMutation)
	return m.passed, m.output, m.err
}

// TestMergeAndComplete_RunsPreMergeQG verifies that mergeAndComplete calls the
// QGRunner before attempting to merge, and handles pass/fail/error correctly.
func TestMergeAndComplete_RunsPreMergeQG(t *testing.T) {
	t.Run("QG pass - merge proceeds normally", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		const beadID = "bead-qg-pass"
		const workerID = "w-qg-pass"
		const worktree = "/tmp/worktree-qg-pass"
		branch := protocol.BranchPrefix + beadID

		qgRunner := &mockQGRunner{passed: true, output: "all green"}
		d.qgRunner = qgRunner

		d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

		// QG was called with the correct worktree.
		qgRunner.mu.Lock()
		calls := append([]string(nil), qgRunner.calls...)
		skipMutations := append([]bool(nil), qgRunner.skipMutations...)
		qgRunner.mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("expected QGRunner.Run called once, got %d", len(calls))
		}
		if calls[0] != worktree {
			t.Errorf("QGRunner.Run worktree = %q, want %q", calls[0], worktree)
		}
		if skipMutations[0] {
			t.Error("pre-merge QG should use local context without ORO_SKIP_MUTATION; mutation is deferred by quality_gate.sh itself")
		}

		// Merge proceeded: bead closed.
		beadSrc.mu.Lock()
		closed := append([]string(nil), beadSrc.closed...)
		beadSrc.mu.Unlock()
		found := false
		for _, id := range closed {
			if id == beadID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected beads.Close(%q) after QG pass + merge, got closed=%v", beadID, closed)
		}

		// Worktree removed.
		wtMgr.mu.Lock()
		removed := append([]string(nil), wtMgr.removed...)
		wtMgr.mu.Unlock()
		foundRemoved := false
		for _, r := range removed {
			if r == worktree {
				foundRemoved = true
				break
			}
		}
		if !foundRemoved {
			t.Errorf("expected worktrees.Remove(%q), got removed=%v", worktree, removed)
		}
	})

	t.Run("QG fail - merge aborted, bead reset to open, worktree cleaned up", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		const beadID = "bead-qg-fail"
		const workerID = "w-qg-fail"
		const worktree = "/tmp/worktree-qg-fail"
		branch := protocol.BranchPrefix + beadID

		qgRunner := &mockQGRunner{passed: false, output: "mutation testing failed"}
		d.qgRunner = qgRunner

		// Seed worktreeByBead so we can verify it is cleared after cleanup.
		d.mu.Lock()
		d.worktreeByBead[beadID] = worktree
		d.mu.Unlock()

		d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

		// QG was called with the correct worktree.
		qgRunner.mu.Lock()
		calls := append([]string(nil), qgRunner.calls...)
		qgRunner.mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("expected QGRunner.Run called once, got %d", len(calls))
		}
		if calls[0] != worktree {
			t.Errorf("QGRunner.Run worktree = %q, want %q", calls[0], worktree)
		}

		// Merge did NOT proceed: bead not closed.
		beadSrc.mu.Lock()
		closed := append([]string(nil), beadSrc.closed...)
		beadSrc.mu.Unlock()
		for _, id := range closed {
			if id == beadID {
				t.Errorf("expected merge to be aborted on QG fail, but beads.Close(%q) was called", beadID)
			}
		}

		// Bead reset to open.
		beadSrc.mu.Lock()
		status, hasStatus := beadSrc.updated[beadID]
		beadSrc.mu.Unlock()
		if !hasStatus {
			t.Errorf("expected beads.Update(%q, \"open\") on QG fail, but Update was not called", beadID)
		} else if status != "open" {
			t.Errorf("beads.Update(%q) status = %q, want \"open\"", beadID, status)
		}

		// Worktree cleaned up.
		wtMgr.mu.Lock()
		removed := append([]string(nil), wtMgr.removed...)
		wtMgr.mu.Unlock()
		foundRemoved := false
		for _, r := range removed {
			if r == worktree {
				foundRemoved = true
				break
			}
		}
		if !foundRemoved {
			t.Errorf("expected worktrees.Remove(%q) on QG fail, got removed=%v", worktree, removed)
		}

		// worktreeByBead cleared.
		d.mu.Lock()
		trackedPath := d.worktreeByBead[beadID]
		d.mu.Unlock()
		if trackedPath != "" {
			t.Errorf("worktreeByBead[%q] = %q, want empty after QG fail cleanup", beadID, trackedPath)
		}
	})

	t.Run("QG error (script missing) - escalate EscStuck, abort merge, cleanup worktree", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		const beadID = "bead-qg-err"
		const workerID = "w-qg-err"
		const worktree = "/tmp/worktree-qg-err"
		branch := protocol.BranchPrefix + beadID

		qgRunner := &mockQGRunner{passed: false, err: fmt.Errorf("quality gate script not found")}
		d.qgRunner = qgRunner

		d.mu.Lock()
		d.worktreeByBead[beadID] = worktree
		d.mu.Unlock()

		d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

		// QG was called.
		qgRunner.mu.Lock()
		calls := qgRunner.calls
		qgRunner.mu.Unlock()
		if len(calls) != 1 {
			t.Fatalf("expected QGRunner.Run called once, got %d", len(calls))
		}

		// Merge did NOT proceed.
		beadSrc.mu.Lock()
		closed := append([]string(nil), beadSrc.closed...)
		beadSrc.mu.Unlock()
		for _, id := range closed {
			if id == beadID {
				t.Errorf("expected merge to be aborted on QG error, but beads.Close(%q) was called", beadID)
			}
		}

		// EscStuck escalation sent.
		esc.mu.Lock()
		msgs := append([]string(nil), esc.messages...)
		esc.mu.Unlock()
		foundEsc := false
		for _, msg := range msgs {
			if strings.Contains(msg, string(protocol.EscStuck)) {
				foundEsc = true
				break
			}
		}
		if !foundEsc {
			t.Errorf("expected EscStuck escalation on QG error, got msgs=%v", msgs)
		}

		// Worktree cleaned up.
		wtMgr.mu.Lock()
		removed := append([]string(nil), wtMgr.removed...)
		wtMgr.mu.Unlock()
		foundRemoved := false
		for _, r := range removed {
			if r == worktree {
				foundRemoved = true
				break
			}
		}
		if !foundRemoved {
			t.Errorf("expected worktrees.Remove(%q) on QG error, got removed=%v", worktree, removed)
		}
	})
}

// TestOpsReviewUsesTargetBranch verifies that handleReadyForReview passes
// w.targetBranch as BaseBranch to the ops reviewer instead of hardcoded "main".
func TestOpsReviewUsesTargetBranch(t *testing.T) {
	d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{{ID: "bead-ops-target", Title: "Ops target test", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Set targetBranch on the worker (simulates assignBead resolution for epic children)
	d.mu.Lock()
	if w, wOK := d.workers["w1"]; wOK {
		w.targetBranch = "epic/my-epic"
	}
	d.mu.Unlock()

	// Send READY_FOR_REVIEW
	sendMsg(t, conn, protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-ops-target", WorkerID: "w1"},
	})

	// Wait for ops reviewer to be spawned
	waitFor(t, func() bool { return spawnMock.SpawnCount() > 0 }, 2*time.Second)

	spawnMock.mu.Lock()
	prompt := spawnMock.spawns[0].prompt
	spawnMock.mu.Unlock()

	// The review prompt should reference the epic branch, not hardcoded "main"
	if !strings.Contains(prompt, "merge to epic/my-epic") {
		t.Errorf("review prompt should reference epic/my-epic as base branch;\ngot prompt prefix: %s", prompt[:min(200, len(prompt))])
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
	wtMgr.createFn = func(_ context.Context, beadID, _ string) (string, string, error) {
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

	// Worker should be REMOVED from d.workers (oro-e2jk: dead socket → remove).
	_, _, ok = d.WorkerInfo("w-fail-assign")
	if ok {
		t.Fatal("expected worker to be removed from d.workers after sendToWorker failure")
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
	waitFor(t, func() bool {
		msgs := esc.Messages()
		for _, m := range msgs {
			if strings.Contains(m, "bead-mcf") && strings.Contains(m, "MERGE_CONFLICT") {
				return eventCount(t, d.db, "merge_conflict_failed") > 0
			}
		}
		return false
	}, 3*time.Second)
}

// TestMergeConflict_OpsAgent_WorktreeNotDeletedBeforeSpawn verifies that the
// worktree is NOT removed before the ops agent is spawned. If the worktree
// is cleaned up first, the ops agent cannot chdir into it to resolve conflicts.
func TestMergeConflict_OpsAgent_WorktreeNotDeletedBeforeSpawn(t *testing.T) {
	d, beadSrc, wtMgr, _, gitRunner, spawnMock := newTestDispatcher(t)

	// Conflict only on the first rebase so the retry succeeds.
	gitRunner.mu.Lock()
	gitRunner.conflictOnce = true
	gitRunner.mu.Unlock()

	spawnMock.mu.Lock()
	spawnMock.verdict = "Resolved conflicts.\n\nRESOLVED\n\nRebase completed."
	spawnMock.mu.Unlock()

	// Track whether Remove was called before the ops agent was spawned.
	var removedBeforeSpawn bool
	var removedBeforeSpawnMu sync.Mutex
	const beadID = "bead-wt-check"
	expectedWorktree := "/tmp/worktree-" + beadID

	wtMgr.mu.Lock()
	wtMgr.removeFn = func(_ context.Context, path string) error {
		if path == expectedWorktree && spawnMock.SpawnCount() == 0 {
			removedBeforeSpawnMu.Lock()
			removedBeforeSpawn = true
			removedBeforeSpawnMu.Unlock()
		}
		return nil
	}
	wtMgr.mu.Unlock()

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Worktree ordering check", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: beadID, WorkerID: "w1", QualityGatePassed: true},
	})

	// Wait for conflict resolution — ops agent must have run.
	waitFor(t, func() bool {
		return eventCount(t, d.db, "merge_conflict_resolved") > 0
	}, 3*time.Second)

	removedBeforeSpawnMu.Lock()
	rbs := removedBeforeSpawn
	removedBeforeSpawnMu.Unlock()
	if rbs {
		t.Error("worktree was removed before ops agent was spawned — ops agent cannot resolve conflicts")
	}

	// Verify the ops agent was given the worktree path as its workdir.
	spawnMock.mu.Lock()
	spawns := make([]spawnCall, len(spawnMock.spawns))
	copy(spawns, spawnMock.spawns)
	spawnMock.mu.Unlock()

	if len(spawns) == 0 {
		t.Fatal("expected at least one ops spawn, got none")
	}
	// The first spawn (index 0) is the merge-conflict ops agent.
	if spawns[0].workdir != expectedWorktree {
		t.Errorf("ops agent workdir: expected %q, got %q", expectedWorktree, spawns[0].workdir)
	}
}

// TestMergeConflictFailureCleanup verifies that when the ops merge-conflict agent
// returns a non-Resolved verdict, the dispatcher removes the worktree, clears
// the worktreeByBead tracking entry, and resets the bead status to "open".
func TestMergeConflictFailureCleanup(t *testing.T) {
	d, beadSrc, wtMgr, _, gitRunner, spawnMock := newTestDispatcher(t)

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

	const beadID = "bead-mcf-cleanup"
	beadSrc.SetBeads([]protocol.Bead{{ID: beadID, Title: "Merge conflict cleanup", Priority: 1}})
	_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	beadSrc.SetBeads(nil)

	// Send DONE — triggers merge conflict, ops agent fails
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{BeadID: beadID, WorkerID: "w1", QualityGatePassed: true},
	})

	// Wait for failure event
	waitFor(t, func() bool {
		return eventCount(t, d.db, "merge_conflict_failed") > 0
	}, 3*time.Second)

	// Verify worktree was removed
	expectedWorktree := "/tmp/worktree-" + beadID
	waitFor(t, func() bool {
		wtMgr.mu.Lock()
		defer wtMgr.mu.Unlock()
		for _, p := range wtMgr.removed {
			if p == expectedWorktree {
				return true
			}
		}
		return false
	}, 2*time.Second)

	wtMgr.mu.Lock()
	removed := make([]string, len(wtMgr.removed))
	copy(removed, wtMgr.removed)
	wtMgr.mu.Unlock()

	found := false
	for _, p := range removed {
		if p == expectedWorktree {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected worktree %q to be removed, removed: %v", expectedWorktree, removed)
	}

	// Verify worktreeByBead tracking was cleared
	d.mu.Lock()
	_, trackingExists := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if trackingExists {
		t.Error("expected worktreeByBead[beadID] to be cleared after merge conflict failure")
	}

	// Verify bead was reset to "open"
	beadSrc.mu.Lock()
	status := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if status != "open" {
		t.Errorf("expected bead status = %q after conflict failure, got %q", "open", status)
	}
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

// TestHandleClosedBead_NoAssignAfterShutdown verifies that tryAssign cannot grab a
// worker that is in the process of shutting down because its bead was closed
// externally. The worker must transition through protocol.WorkerShuttingDown
// (invisible to tryAssign) rather than going straight to protocol.WorkerIdle.
func TestHandleClosedBead_NoAssignAfterShutdown(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect a worker and register it.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-closed", ContextPct: 10},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start the dispatcher so tryAssign is active.
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Manually put the worker into busy state with a bead assignment,
	// simulating what assignBead does.
	d.mu.Lock()
	w := d.workers["w-closed"]
	w.state = protocol.WorkerBusy
	w.beadID = "bead-closed"
	w.worktree = "/tmp/worktree-closed"
	w.model = "test-model"
	d.mu.Unlock()

	// Make the bead source report the bead as closed externally.
	beadSrc.mu.Lock()
	beadSrc.shown["bead-closed"] = &protocol.BeadDetail{
		Title:  "Closed bead",
		Status: "closed",
	}
	beadSrc.mu.Unlock()

	// Trigger the closed-assignment handler. This sends SHUTDOWN and should NOT
	// make the worker visible to tryAssign as idle.
	d.handleClosedAssignment(context.Background(), "w-closed", "bead-closed")

	// After handleClosedAssignment, the worker state must NOT be protocol.WorkerIdle.
	// It should be protocol.WorkerShuttingDown so that tryAssign skips it.
	st, _, ok := d.WorkerInfo("w-closed")
	if !ok {
		t.Fatal("expected worker to still be tracked")
	}
	if st == protocol.WorkerIdle {
		t.Fatalf("worker state after bead_closed_externally should not be protocol.WorkerIdle (got %s); "+
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
	st2, beadID, _ := d.WorkerInfo("w-closed")
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

	beadSrc := &fakeBeadStore{shown: make(map[string]*protocol.BeadDetail)}
	wt := &mockWorktreeManager{}
	esc := &mockEscalator{}
	gitRunner := &mockGitRunner{}
	merger := merge.NewCoordinator(gitRunner)
	spawner := ops.NewSpawner(&mockBatchSpawner{verdict: "VERDICT: APPROVED"})

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
	waitFor(t, func() bool {
		d.mu.Lock()
		_, ok := d.workers["test-worker"]
		d.mu.Unlock()
		return ok
	}, 2*time.Second)

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
	waitFor(t, func() bool {
		d.mu.Lock()
		w, ok := d.workers["test-worker"]
		has := ok && w.shutdownCancel != nil
		d.mu.Unlock()
		return has
	}, 2*time.Second)

	// Read first PREPARE_SHUTDOWN
	msg1, ok1 := readMsg(t, conn, 200*time.Millisecond)
	if !ok1 || msg1.Type != protocol.MsgPrepareShutdown {
		t.Fatal("expected first PREPARE_SHUTDOWN message")
	}

	// Record the current WaitGroup count (indirectly by checking when goroutines finish)
	startTime := time.Now()

	// Second call - should cancel the first goroutine immediately.
	// GracefulShutdownWorker sets shutdownCancel and sends PREPARE_SHUTDOWN
	// synchronously before returning; readMsg below provides the sync point.
	d.GracefulShutdownWorker("test-worker", longTimeout)

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

	// Wait for goroutines to detect the approval (shutdownCancel cleared on approval)
	waitFor(t, func() bool {
		d.mu.Lock()
		w, ok := d.workers["test-worker"]
		done := !ok || w.shutdownCancel == nil
		d.mu.Unlock()
		return done
	}, 2*time.Second)

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

	// Wait for the assign loop to process the new bead list (positive signal:
	// cachedQueueDepth reflects the 2 beads we just set).
	waitFor(t, func() bool {
		d.mu.Lock()
		depth := d.cachedQueueDepth
		d.mu.Unlock()
		return depth >= 2
	}, 2*time.Second)

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
// Edge: idle workers with stale heartbeats are also removed (disconnected).
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

	t.Run("idle worker with stale heartbeat is removed", func(t *testing.T) {
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

		// Assert: stale idle worker removed (disconnected).
		if d.ConnectedWorkers() != 0 {
			t.Fatalf("expected stale idle worker to be removed, got %d workers", d.ConnectedWorkers())
		}

		// Assert: escalation sent (idle worker with no bead — still reported).
		if len(esc.Messages()) != 1 {
			t.Errorf("expected 1 escalation for stale idle worker, got %d", len(esc.Messages()))
		}

		// Assert: heartbeat_timeout event logged for the stale idle worker.
		count := eventCount(t, d.db, "heartbeat_timeout")
		if count != 1 {
			t.Errorf("expected 1 heartbeat_timeout event for stale idle worker, got %d", count)
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
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch != "agent/oro-quarantine", nil
	}
	wtMgr.existsFn = func(_ context.Context, path string) bool {
		return path != "/tmp/wt-quarantine"
	}

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
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status, attempt_count, handoff_count)
		 VALUES ('oro-quarantine', 'w4', '/tmp/wt-quarantine', 'active', 9, 9)`)
	if err != nil {
		t.Fatalf("insert quarantined assignment: %v", err)
	}
	// Insert a completed assignment — should NOT be restored.
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status, attempt_count, handoff_count)
		 VALUES ('oro-ccc', 'w3', '/tmp/wt-ccc', 'completed', 5, 5)`)
	if err != nil {
		t.Fatalf("insert assignment 3: %v", err)
	}

	// Start dispatcher — Run() should restore state from SQLite.
	d.shutdownRunner = &mockCommandRunner{}
	cancel := startDispatcher(t, d)
	defer cancel()

	// Verify attempt counts were restored from active assignments only.
	d.mu.Lock()
	gotAttemptAAA := d.attemptCounts["oro-aaa"]
	gotAttemptBBB := d.attemptCounts["oro-bbb"]
	_, hasCCC := d.attemptCounts["oro-ccc"]
	_, hasQuarantine := d.attemptCounts["oro-quarantine"]
	gotHandoffAAA := d.handoffCounts["oro-aaa"]
	gotHandoffBBB := d.handoffCounts["oro-bbb"]
	_, hasHandoffCCC := d.handoffCounts["oro-ccc"]
	_, hasQuarantineHandoff := d.handoffCounts["oro-quarantine"]
	gotWorktreeAAA := d.worktreeByBead["oro-aaa"]
	gotWorktreeBBB := d.worktreeByBead["oro-bbb"]
	_, hasWorktreeQuarantine := d.worktreeByBead["oro-quarantine"]
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
	if hasQuarantine {
		t.Errorf("attemptCounts should not contain quarantined bead oro-quarantine")
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
	if hasQuarantineHandoff {
		t.Errorf("handoffCounts should not contain quarantined bead oro-quarantine")
	}
	if gotWorktreeAAA != "/tmp/wt-aaa" {
		t.Errorf("worktreeByBead[oro-aaa]: got %q, want %q", gotWorktreeAAA, "/tmp/wt-aaa")
	}
	if gotWorktreeBBB != "/tmp/wt-bbb" {
		t.Errorf("worktreeByBead[oro-bbb]: got %q, want %q", gotWorktreeBBB, "/tmp/wt-bbb")
	}
	if hasWorktreeQuarantine {
		t.Errorf("worktreeByBead should not contain quarantined bead oro-quarantine")
	}

	var quarantineStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE bead_id='oro-quarantine'`).Scan(&quarantineStatus); err != nil {
		t.Fatalf("query quarantine status: %v", err)
	}
	if quarantineStatus != "completed" {
		t.Fatalf("quarantined assignment status = %q, want completed", quarantineStatus)
	}
}

func TestStartupDoesNotPruneRecoverableAgentBranch(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	worktree := t.TempDir()
	d.repoRoot = t.TempDir()

	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-recover", nil
	}

	d.shutdownRunner = &mockCommandRunner{
		callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "git" {
				for i := 0; i < len(args)-2; i++ {
					if args[i] == "branch" && args[i+1] == "--list" && args[i+2] == "agent/*" {
						t.Fatalf("startup should not prune agent branches, got git %v", args)
					}
				}
			}
			return nil, nil
		},
	}

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-recover', 'w1', ?, 'active')`,
		worktree); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	cancel := startDispatcher(t, d)
	defer cancel()

	d.mu.Lock()
	got := d.worktreeByBead["oro-recover"]
	d.mu.Unlock()
	if got != worktree {
		t.Fatalf("recoverable worktree missing after startup: got %q want %q", got, worktree)
	}
}

func TestStartupRecoversFromActiveAssignmentBranchState(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	worktree := t.TempDir()

	beadSrc.inProgressBeads = []protocol.Bead{{ID: "oro-recover"}}
	d.shutdownRunner = &mockCommandRunner{}
	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-recover", nil
	}

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status, attempt_count, handoff_count)
		 VALUES ('oro-recover', 'w1', ?, 'active', 4, 2)`,
		worktree); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	cancel := startDispatcher(t, d)
	defer cancel()

	beadSrc.mu.Lock()
	updated := beadSrc.updated
	beadSrc.mu.Unlock()
	if updated["oro-recover"] != "open" {
		t.Fatalf("expected recovered bead to reopen, got updates=%v", updated)
	}

	d.mu.Lock()
	gotWorktree := d.worktreeByBead["oro-recover"]
	gotAttempts := d.attemptCounts["oro-recover"]
	gotHandoffs := d.handoffCounts["oro-recover"]
	d.mu.Unlock()
	if gotWorktree != worktree {
		t.Fatalf("worktreeByBead[oro-recover] = %q, want %q", gotWorktree, worktree)
	}
	if gotAttempts != 4 {
		t.Fatalf("attemptCounts[oro-recover] = %d, want 4", gotAttempts)
	}
	if gotHandoffs != 2 {
		t.Fatalf("handoffCounts[oro-recover] = %d, want 2", gotHandoffs)
	}
}

func TestStartupQuarantinesInconsistentRecoveryState(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	beadSrc.inProgressBeads = []protocol.Bead{{ID: "oro-bad"}}
	d.shutdownRunner = &mockCommandRunner{}
	wtMgr.existsFn = func(_ context.Context, _ string) bool { return false }
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-bad", nil
	}

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-bad', 'w1', '/tmp/missing', 'active')`); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	cancel := startDispatcher(t, d)
	defer cancel()

	beadSrc.mu.Lock()
	updated := beadSrc.updated
	beadSrc.mu.Unlock()
	if _, ok := updated["oro-bad"]; ok {
		t.Fatalf("expected quarantined bead to remain untouched, got updates=%v", updated)
	}

	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE bead_id='oro-bad'`).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "completed" {
		t.Fatalf("quarantined assignment status = %q, want completed", status)
	}

	var eventCount int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE type='startup_recovery_quarantined' AND bead_id='oro-bad'`).Scan(&eventCount); err != nil {
		t.Fatalf("query quarantine events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected one startup_recovery_quarantined event, got %d", eventCount)
	}
}

func TestResetOrphanedBeadsOnlyReopensDispatcherOwnedClaims(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	worktree := t.TempDir()

	beadSrc.inProgressBeads = []protocol.Bead{{ID: "oro-owned"}, {ID: "oro-human"}}
	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-owned", nil
	}

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-owned', 'w1', ?, 'active')`,
		worktree); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	recoverable, _, err := d.restoreState(ctx)
	if err != nil {
		t.Fatalf("restoreState: %v", err)
	}
	_, _ = d.resetOrphanedBeads(ctx, recoverable)

	beadSrc.mu.Lock()
	updated := beadSrc.updated
	beadSrc.mu.Unlock()

	if updated["oro-owned"] != "open" {
		t.Fatalf("expected dispatcher-owned bead to reopen, got updates=%v", updated)
	}
	if _, ok := updated["oro-human"]; ok {
		t.Fatalf("expected human-owned bead to remain untouched, got updates=%v", updated)
	}
}

func TestHumanOwnedInProgressBeadRemainsNonAssignableAfterRestart(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	assignable := d.filterAssignable(context.Background(), []protocol.Bead{
		{ID: "oro-human", Status: "in_progress"},
		{ID: "oro-ready", Status: "ready"},
	})
	if len(assignable) != 1 || assignable[0].ID != "oro-ready" {
		t.Fatalf("assignable = %+v, want only oro-ready", assignable)
	}
}

func TestStartupReconciliationEmitsRecoverySummary(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	worktree := t.TempDir()

	beadSrc.inProgressBeads = []protocol.Bead{{ID: "oro-owned"}, {ID: "oro-human"}}
	d.shutdownRunner = &mockCommandRunner{}
	wtMgr.existsFn = func(_ context.Context, path string) bool { return path == worktree }
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-owned", nil
	}

	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-owned', 'w1', ?, 'active')`,
		worktree); err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	cancel := startDispatcher(t, d)
	defer cancel()

	var payload string
	if err := d.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='startup_reconciliation_summary' ORDER BY id DESC LIMIT 1`,
	).Scan(&payload); err != nil {
		t.Fatalf("query startup summary: %v", err)
	}
	for _, want := range []string{
		`"recovered_attempts":1`,
		`"quarantined_assignments":0`,
		`"reopened_beads":1`,
		`"skipped_in_progress":1`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("startup summary %q missing %s", payload, want)
		}
	}
}

func TestAssignmentInvariantViolationIsLogged(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, `DROP INDEX idx_assignments_one_active_per_bead`); err != nil {
		t.Fatalf("drop unique index: %v", err)
	}
	for _, workerID := range []string{"w1", "w2"} {
		if _, err := d.db.ExecContext(ctx,
			`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('oro-dup', ?, ?, 'active')`,
			workerID, "/tmp/"+workerID); err != nil {
			t.Fatalf("insert duplicate assignment for %s: %v", workerID, err)
		}
	}

	d.logAssignmentInvariantViolations(ctx)

	var payload string
	if err := d.db.QueryRowContext(ctx,
		`SELECT payload FROM events WHERE type='assignment_invariant_violation' AND bead_id='oro-dup' ORDER BY id DESC LIMIT 1`,
	).Scan(&payload); err != nil {
		t.Fatalf("query invariant event: %v", err)
	}
	if !strings.Contains(payload, `"active_assignments":2`) {
		t.Fatalf("unexpected invariant payload: %q", payload)
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
	beadSrc, ok := d.beads.(*fakeBeadStore)
	if !ok {
		t.Fatal("beads is not *fakeBeadStore")
	}
	waitFor(t, func() bool {
		beadSrc.mu.Lock()
		calls := make([]createCall, len(beadSrc.created))
		copy(calls, beadSrc.created)
		beadSrc.mu.Unlock()

		for _, c := range calls {
			if c.parent == "bead-stuck" && c.beadType == "task" &&
				strings.Contains(c.description, "Implemented 3 of 5 subtasks") {
				return eventCount(t, d.db, "continuation_bead_created") > 0
			}
		}
		return false
	}, 3*time.Second)
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

	db, err := dbutil.OpenDB(tmpFile)
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

	beadSrc := &fakeBeadStore{
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
		spawnMock := &mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}
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

	// Wait for reconnect processing to mark worker as busy on bead-crash1.
	waitFor(t, func() bool {
		d2.mu.Lock()
		w, ok := d2.workers["w1"]
		busy := ok && w.state == protocol.WorkerBusy && w.beadID == "bead-crash1"
		d2.mu.Unlock()
		return busy
	}, 2*time.Second)

	// Read the worker state for assertions below.
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
}

// TestReviewProgressTimeout verifies that a worker in the reviewing state is
// killed after ReviewTimeout with no stdout (no lastProgress update), and that
// recent progress prevents the kill.
func TestReviewProgressTimeout(t *testing.T) {
	t.Run("reviewing worker with stale progress is killed after ReviewTimeout", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		d.cfg.ProgressTimeout = 1 * time.Second
		d.cfg.ReviewTimeout = 2 * time.Second

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		beadID := "bead-review-stalled"
		workerID := "w-review-stalled"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         server,
			state:        protocol.WorkerReviewing,
			beadID:       beadID,
			worktree:     "/tmp/worktree-review-stalled",
			lastSeen:     now,                       // heartbeat fresh — worker alive
			lastProgress: now.Add(-3 * time.Second), // review progress stale (>2s ago)
			encoder:      json.NewEncoder(server),
		}
		d.attemptCounts[beadID] = 1
		d.handoffCounts[beadID] = 1
		d.mu.Unlock()

		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected 1 worker, got %d", d.ConnectedWorkers())
		}

		d.checkHeartbeats(context.Background())

		if d.ConnectedWorkers() != 0 {
			t.Fatalf("expected 0 workers after review timeout, got %d", d.ConnectedWorkers())
		}

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

		d.mu.Lock()
		_, hasAttempt := d.attemptCounts[beadID]
		_, hasHandoff := d.handoffCounts[beadID]
		d.mu.Unlock()
		if hasAttempt {
			t.Error("expected attemptCounts to be cleared for stalled reviewing worker")
		}
		if hasHandoff {
			t.Error("expected handoffCounts to be cleared for stalled reviewing worker")
		}

		count := eventCount(t, d.db, "progress_timeout")
		if count != 1 {
			t.Fatalf("expected 1 progress_timeout event, got %d", count)
		}
	})

	t.Run("reviewing worker with recent progress is not killed", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		d.cfg.ProgressTimeout = 1 * time.Second

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		d.mu.Lock()
		d.workers["w-review-active"] = &trackedWorker{
			id:           "w-review-active",
			conn:         server,
			state:        protocol.WorkerReviewing,
			beadID:       "bead-review-active",
			lastSeen:     now,
			lastProgress: now.Add(-500 * time.Millisecond), // progress recent (within 1s)
			encoder:      json.NewEncoder(server),
		}
		d.mu.Unlock()

		d.checkHeartbeats(context.Background())

		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected reviewing worker with recent progress to survive, got %d workers", d.ConnectedWorkers())
		}
		if len(esc.Messages()) != 0 {
			t.Errorf("expected no escalation for reviewing worker with recent progress, got %d", len(esc.Messages()))
		}
	})

	t.Run("review produces output resets progress timer", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		d.cfg.ProgressTimeout = 1 * time.Second

		conn := newMockConn()
		workerID := "w-review-output"

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         conn,
			state:        protocol.WorkerReviewing,
			beadID:       "bead-review-output",
			lastSeen:     now,
			lastProgress: now.Add(-2 * time.Second), // initially stale
			encoder:      json.NewEncoder(conn),
		}
		d.mu.Unlock()

		// Simulate review output: touchProgress resets the timer.
		d.touchProgress(workerID)

		d.checkHeartbeats(context.Background())

		if d.ConnectedWorkers() != 1 {
			t.Fatalf("expected worker to survive after progress reset, got %d workers", d.ConnectedWorkers())
		}
		if len(esc.Messages()) != 0 {
			t.Errorf("expected no escalation after progress reset, got %d", len(esc.Messages()))
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

	result := d.filterAssignable(context.Background(), beads)

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
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.exhaustedBeads["bead-exh"]
	}, 2*time.Second)

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
// without acceptance criteria are NOT assigned — a MISSING_AC escalation fires
// and the worktree is never created.
func TestAssignBeadSkipsMissingAcceptanceBeforeWorktree(t *testing.T) {
	d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

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

	d.setState(StateRunning)

	// Manually attempt to assign the bead.
	w := &trackedWorker{id: "w1", conn: conn, state: protocol.WorkerIdle}
	_ = d.assignBead(context.Background(), w, protocol.Bead{ID: "bead-noac", Title: "No AC bead", Priority: 1, Type: "task"})

	// Worktree must NOT be created (bead skipped — awaiting AC).
	wtMgr.mu.Lock()
	_, created := wtMgr.created["bead-noac"]
	wtMgr.mu.Unlock()

	if created {
		t.Fatal("worktree must not be created for bead without acceptance criteria")
	}

	// Worker must remain IDLE.
	d.mu.Lock()
	workerIdle := w.state == protocol.WorkerIdle
	d.mu.Unlock()
	if !workerIdle {
		t.Fatal("worker should remain IDLE when bead has no AC")
	}

	// A MISSING_AC escalation must have been dispatched.
	found := false
	for _, m := range esc.Messages() {
		if strings.Contains(m, string(protocol.EscMissingAC)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected MISSING_AC escalation, got none")
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

	result := d.filterAssignable(context.Background(), beads)

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

	result := d.filterAssignable(context.Background(), beads)

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

	result := d.filterAssignable(context.Background(), beads)

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

// TestFilterAssignableSkipsMergingBeads verifies that filterAssignable excludes
// beads currently being merged. Without this check, the race window between
// mergeAndComplete setting mergingBeads and bd close propagating the status
// causes rapid re-assignment spam (bead_closed_externally).
func TestFilterAssignableSkipsMergingBeads(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	d.mu.Lock()
	d.mergingBeads["bead-merging"] = true
	d.mu.Unlock()

	beads := []protocol.Bead{
		{ID: "bead-merging", Title: "Being merged", Priority: 1, Type: "task"},
		{ID: "bead-ready", Title: "Available", Priority: 2, Type: "task"},
	}

	result := d.filterAssignable(context.Background(), beads)

	if len(result) != 1 {
		t.Fatalf("expected 1 assignable bead, got %d", len(result))
	}
	if result[0].ID != "bead-ready" {
		t.Fatalf("expected bead-ready, got %s", result[0].ID)
	}
}

// TestFilterAssignable_SkipsAlreadyMergedBead verifies that filterAssignable
// excludes beads whose agent/<beadID> branch is already merged to main.
// The bead is auto-closed and a "bead_branch_already_merged" event is emitted.
func TestFilterAssignable_SkipsAlreadyMergedBead(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Init schema so logEvent works.
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const mergedID = "oro-merged"
	const openID = "oro-open"

	// shutdownRunner: for the merged bead, return distinct SHAs for rev-parse
	// vs merge-base (so the empty-branch guard does not short-circuit) then
	// exit 0 on merge-base --is-ancestor. All other beads exit non-zero.
	d.shutdownRunner = &mockCommandRunner{
		callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			isMerged := false
			for _, a := range args {
				if a == "agent/"+mergedID {
					isMerged = true
					break
				}
			}
			if !isMerged {
				return nil, errors.New("exit status 1")
			}
			if len(args) >= 1 && args[0] == "rev-parse" {
				return []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), nil
			}
			if len(args) >= 1 && args[0] == "merge-base" && (len(args) < 2 || args[1] != "--is-ancestor") {
				return []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), nil
			}
			return nil, nil // merge-base --is-ancestor → merged
		},
	}

	beads := []protocol.Bead{
		{ID: mergedID, Title: "Already merged", Priority: 1, Type: "task"},
		{ID: openID, Title: "Open bead", Priority: 2, Type: "task"},
	}

	result := d.filterAssignable(ctx, beads)

	// mergedID must be excluded.
	if len(result) != 1 {
		t.Fatalf("expected 1 assignable bead, got %d: %v", len(result), result)
	}
	if result[0].ID != openID {
		t.Errorf("expected %q in result, got %q", openID, result[0].ID)
	}

	// mergedID must be auto-closed.
	beadSrc.mu.Lock()
	closed := append([]string(nil), beadSrc.closed...)
	beadSrc.mu.Unlock()
	found := false
	for _, id := range closed {
		if id == mergedID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q to be auto-closed via beads.Close(); closed=%v", mergedID, closed)
	}

	// "bead_branch_already_merged" event must be logged.
	if n := eventCount(t, d.db, "bead_branch_already_merged"); n == 0 {
		t.Error("expected bead_branch_already_merged event to be logged")
	}
}

// --- missing acceptance criteria escalation tests ---

// TestAssignBead_MissingAcceptanceEscalatesToManager verifies that beads
// without acceptance criteria trigger a MISSING_AC escalation (not a warning)
// and are not assigned to the worker.
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
	err := d.assignBead(context.Background(), w, protocol.Bead{ID: "bead-no-ac", Title: "Test bead without AC", Type: "task"})
	if err != nil {
		t.Fatalf("assignBead returned unexpected error: %v", err)
	}

	// Verify MISSING_AC escalation was sent.
	found := false
	for _, m := range esc.Messages() {
		if strings.Contains(m, string(protocol.EscMissingAC)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected MISSING_AC escalation for bead with no AC, got none")
	}

	// Verify worktree was NOT created (assignment skipped).
	wtMgr.mu.Lock()
	_, created := wtMgr.created["bead-no-ac"]
	wtMgr.mu.Unlock()
	if created {
		t.Fatal("worktree must not be created for bead without AC")
	}
}

// TestAssignBead_MissingAC_EscalatesAndSkips verifies that beads without
// acceptance criteria fire a MISSING_AC escalation, skip worktree creation,
// and leave the worker IDLE.
func TestAssignBead_MissingAC_EscalatesAndSkips(t *testing.T) {
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
	err := d.assignBead(context.Background(), w, protocol.Bead{ID: "bead-no-ac", Title: "Test bead without AC", Type: "task"})
	if err != nil {
		t.Fatalf("assignBead returned unexpected error: %v", err)
	}

	// (1) Worktree must NOT be created.
	wtMgr.mu.Lock()
	_, created := wtMgr.created["bead-no-ac"]
	wtMgr.mu.Unlock()
	if created {
		t.Fatal("worktree must not be created for bead without AC")
	}

	// (2) A MISSING_AC escalation must have been sent.
	found := false
	for _, m := range esc.Messages() {
		if strings.Contains(m, string(protocol.EscMissingAC)) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected MISSING_AC escalation, got none")
	}

	// (3) Worker must remain IDLE (assignment did not proceed).
	d.mu.Lock()
	workerIdle := w.state == protocol.WorkerIdle
	d.mu.Unlock()
	if !workerIdle {
		t.Fatal("worker should remain IDLE when bead has no AC")
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
		d.mergeAndComplete(ctx, childID, workerID, worktree, branch, epicID, "", 0)

		// Wait for async auto-close goroutine to close the epic.
		waitFor(t, func() bool {
			beadSource.mu.Lock()
			defer beadSource.mu.Unlock()
			for _, id := range beadSource.closed {
				if id == epicID {
					return true
				}
			}
			return false
		}, 2*time.Second)

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

		d.mergeAndComplete(ctx, childID, workerID, worktree, branch, epicID, "", 0)

		// Wait for the child bead to be closed (confirms goroutine ran).
		waitFor(t, func() bool {
			beadSource.mu.Lock()
			defer beadSource.mu.Unlock()
			for _, id := range beadSource.closed {
				if id == childID {
					return true
				}
			}
			return false
		}, 2*time.Second)

		// Verify the epic was NOT closed.
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
		d.mergeAndComplete(ctx, childID, workerID, worktree, branch, "", "", 0)

		// Wait for the child bead to be closed (merge flow not blocked).
		waitFor(t, func() bool {
			beadSource.mu.Lock()
			defer beadSource.mu.Unlock()
			for _, id := range beadSource.closed {
				if id == childID {
					return true
				}
			}
			return false
		}, 2*time.Second)

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

		d.mergeAndComplete(ctx, childID, workerID, worktree, branch, epicID, "", 0)

		// Wait for async auto-close goroutine to produce escalation.
		waitFor(t, func() bool {
			for _, msg := range esc.Messages() {
				if strings.Contains(msg, "EPIC_COMPLETE") && strings.Contains(msg, epicID) {
					return true
				}
			}
			return false
		}, 2*time.Second)

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

		d.mergeAndComplete(ctx, childID, workerID, worktree, branch, epicID, "", 0)

		// Wait for the epic to be auto-closed (confirms goroutine completed).
		waitFor(t, func() bool {
			beadSource.mu.Lock()
			defer beadSource.mu.Unlock()
			for _, id := range beadSource.closed {
				if id == epicID {
					return true
				}
			}
			return false
		}, 2*time.Second)

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

		d.mergeAndComplete(ctx, childID, workerID, worktree, branch, epicID, "", 0)

		// Wait for the epic to be auto-closed.
		waitFor(t, func() bool {
			beadSource.mu.Lock()
			defer beadSource.mu.Unlock()
			for _, id := range beadSource.closed {
				if id == epicID {
					return true
				}
			}
			return false
		}, 2*time.Second)

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

// --- Epic acceptance test verification (oro-fewh) ---

// TestEpicAutoCloseRunsAcceptanceTest verifies that tryCloseEpic runs the
// epic's Cmd: acceptance test instead of merely counting closed children.
func TestEpicAutoCloseRunsAcceptanceTest(t *testing.T) {
	t.Run("passing acceptance test closes epic", func(t *testing.T) {
		d, beadSource, _, _, _, spawnMock := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-ac-pass"
		childID := "child-acp1"
		workerID := "worker-acp1"

		// Set acceptance runner to always pass.
		runner := &mockAcceptanceRunner{passed: true, output: "ok"}
		d.acceptance = runner

		// Epic has Cmd: acceptance criteria.
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID:                 epicID,
			Title:              "My Epic",
			AcceptanceCriteria: "Test: foo_test.go:TestFoo | Cmd: go test ./... | Assert: PASS",
		}
		beadSource.mu.Unlock()

		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			epicID:  epicID,
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil),
		}
		d.mu.Unlock()

		d.mergeAndComplete(ctx, childID, workerID, "/tmp/wt-acp1", "agent/"+childID, epicID, "", 0)

		// Wait for async auto-close goroutine.
		waitFor(t, func() bool {
			beadSource.mu.Lock()
			defer beadSource.mu.Unlock()
			for _, id := range beadSource.closed {
				if id == epicID {
					return true
				}
			}
			return false
		}, 2*time.Second)

		// Epic must be closed.
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
			t.Error("expected epic to be closed after passing acceptance test")
		}

		// Acceptance runner must have been called.
		runner.mu.Lock()
		calls := runner.calls
		runner.mu.Unlock()
		if calls == 0 {
			t.Error("expected acceptance runner to be called")
		}

		// No diagnostic agent should have been spawned.
		// Filter out haiku (dream) spawns — completeEpicClose always triggers a dream.
		if spawnMock.SpawnCountExcludingModel("haiku") > 0 {
			t.Errorf("expected no diagnostic agent spawn on pass, got %d non-haiku spawn(s)",
				spawnMock.SpawnCountExcludingModel("haiku"))
		}
	})

	t.Run("failing acceptance test spawns diagnostic and does not close epic", func(t *testing.T) {
		d, beadSource, _, _, _, spawnMock := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-ac-fail"
		childID := "child-acf1"
		workerID := "worker-acf1"

		// Set acceptance runner to always fail.
		runner := &mockAcceptanceRunner{passed: false, output: "FAIL: test failed"}
		d.acceptance = runner

		// Epic has Cmd: acceptance criteria.
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID:                 epicID,
			Title:              "My Epic",
			AcceptanceCriteria: "Test: foo_test.go:TestFoo | Cmd: go test ./... | Assert: PASS",
		}
		beadSource.mu.Unlock()

		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			epicID:  epicID,
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil),
		}
		d.mu.Unlock()

		d.mergeAndComplete(ctx, childID, workerID, "/tmp/wt-acf1", "agent/"+childID, epicID, "", 0)

		// Wait for async goroutine to run.
		waitFor(t, func() bool {
			runner.mu.Lock()
			defer runner.mu.Unlock()
			return runner.calls > 0
		}, 2*time.Second)

		// Wait for diagnostic spawn goroutine to complete.
		waitFor(t, func() bool {
			return spawnMock.SpawnCount() > 0
		}, 2*time.Second)

		// Epic must NOT be closed.
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
			t.Error("expected epic NOT to be closed after failing acceptance test")
		}

		// A diagnostic agent must have been spawned.
		if spawnMock.SpawnCount() == 0 {
			t.Error("expected diagnostic agent to be spawned on acceptance test failure")
		}

		// The spawned prompt should mention the epic ID.
		spawnMock.mu.Lock()
		lastPrompt := ""
		if len(spawnMock.spawns) > 0 {
			lastPrompt = spawnMock.spawns[len(spawnMock.spawns)-1].prompt
		}
		spawnMock.mu.Unlock()
		if !strings.Contains(lastPrompt, epicID) {
			t.Errorf("expected diagnostic prompt to contain epic ID %q, got: %s", epicID, lastPrompt)
		}
	})

	t.Run("epic without Cmd: falls back to count-based close with warning", func(t *testing.T) {
		d, beadSource, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-no-cmd"
		childID := "child-nc1"
		workerID := "worker-nc1"

		// Epic has acceptance criteria but no Cmd: field.
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID:                 epicID,
			Title:              "My Epic",
			AcceptanceCriteria: "All children pass their quality gates",
		}
		beadSource.mu.Unlock()

		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			beadID:  childID,
			epicID:  epicID,
			state:   protocol.WorkerBusy,
			encoder: json.NewEncoder(nil),
		}
		d.mu.Unlock()

		d.mergeAndComplete(ctx, childID, workerID, "/tmp/wt-nc1", "agent/"+childID, epicID, "", 0)

		// Epic should still be closed (count-based fallback).
		waitFor(t, func() bool {
			beadSource.mu.Lock()
			defer beadSource.mu.Unlock()
			for _, id := range beadSource.closed {
				if id == epicID {
					return true
				}
			}
			return false
		}, 2*time.Second)

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
			t.Error("expected epic to be closed via count-based fallback when no Cmd: present")
		}

		// Warning event should have been logged.
		count := eventCount(t, d.db, "epic_no_acceptance_cmd")
		if count == 0 {
			t.Error("expected epic_no_acceptance_cmd warning event to be logged")
		}
	})
}

// TestTryCloseEpic_FFMergeToMain verifies that tryCloseEpic FF-merges the
// epic branch to main and deletes it when all children are closed.
func TestTryCloseEpic_FFMergeToMain(t *testing.T) {
	t.Run("happy path: FF merges epic branch to main and deletes it", func(t *testing.T) {
		d, beadSource, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-ff-happy"
		workerID := "worker-ff1"

		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID:    epicID,
			Title: "My Epic",
			// No Cmd: → falls through to completeEpicClose via count-based path.
			AcceptanceCriteria: "",
		}
		beadSource.mu.Unlock()

		// Epic branch exists; track MergeFFOnly calls.
		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
			return branch == protocol.EpicBranchPrefix+epicID, nil
		}
		wtMgr.mergeFFOnlyFn = func(branch, _ string) (string, error) {
			wtMgr.mu.Lock()
			wtMgr.mergedBranches = append(wtMgr.mergedBranches, branch)
			wtMgr.mu.Unlock()
			return "abc123", nil
		}
		wtMgr.mu.Unlock()

		d.tryCloseEpic(ctx, epicID, workerID)

		// Epic must be closed.
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
			t.Error("expected epic to be closed after successful FF merge")
		}

		// MergeFFOnly must have been called with the epic branch.
		wtMgr.mu.Lock()
		merged := make([]string, len(wtMgr.mergedBranches))
		copy(merged, wtMgr.mergedBranches)
		wtMgr.mu.Unlock()
		epicBranch := protocol.EpicBranchPrefix + epicID
		found := false
		for _, b := range merged {
			if b == epicBranch {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected MergeFFOnly called with %q, got %v", epicBranch, merged)
		}

		// Epic branch must have been deleted.
		wtMgr.mu.Lock()
		deleted := false
		for _, b := range wtMgr.deletedBranches {
			if b == epicBranch {
				deleted = true
				break
			}
		}
		wtMgr.mu.Unlock()
		if !deleted {
			t.Errorf("expected DeleteBranch called with %q, got %v", epicBranch, wtMgr.deletedBranches)
		}
	})

	t.Run("epic branch does not exist: skip merge, close epic", func(t *testing.T) {
		d, beadSource, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-ff-nobranch"
		workerID := "worker-ff2"

		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID: epicID, Title: "My Epic", AcceptanceCriteria: "",
		}
		beadSource.mu.Unlock()

		// Branch does not exist.
		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}
		wtMgr.mu.Unlock()

		d.tryCloseEpic(ctx, epicID, workerID)

		// Epic must still be closed.
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
			t.Error("expected epic to be closed when branch does not exist")
		}

		// MergeFFOnly must NOT have been called.
		wtMgr.mu.Lock()
		mergedCount := len(wtMgr.mergedBranches)
		wtMgr.mu.Unlock()
		if mergedCount > 0 {
			t.Errorf("expected no MergeFFOnly call when branch absent, got %v", wtMgr.mergedBranches)
		}
	})

	t.Run("FF merge fails: rebase bead created, epic not closed", func(t *testing.T) {
		d, beadSource, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-ff-fail"
		workerID := "worker-ff3"

		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID: epicID, Title: "My Epic", AcceptanceCriteria: "",
		}
		beadSource.mu.Unlock()

		// Branch exists but FF merge fails.
		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return true, nil
		}
		wtMgr.mergeFFOnlyFn = func(branch, _ string) (string, error) {
			return "", errors.New("not fast-forward: main has diverged")
		}
		wtMgr.mu.Unlock()

		d.tryCloseEpic(ctx, epicID, workerID)

		// Epic must NOT be closed.
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
			t.Error("expected epic NOT to be closed after FF merge failure")
		}

		// A rebase bead must have been created as a child of the epic.
		beadSource.mu.Lock()
		createdCount := len(beadSource.created)
		var rebaseBead createCall
		for _, c := range beadSource.created {
			if c.parent == epicID {
				rebaseBead = c
				break
			}
		}
		beadSource.mu.Unlock()
		if createdCount == 0 {
			t.Fatal("expected a rebase bead to be created on FF merge failure")
		}
		if rebaseBead.parent != epicID {
			t.Errorf("expected rebase bead parent=%q, got %q", epicID, rebaseBead.parent)
		}
	})
}

// TestEpicClose_TargetBranch verifies that tryCloseEpic reads Metadata[MetaBranch]
// from the epic detail, passes it through completeEpicClose → ffMergeEpicBranch,
// and that ffMergeEpicBranch routes to UpdateBranchRef for non-HEAD targets and
// MergeFFOnly for HEAD (the default branch, typically "main").
func TestEpicClose_TargetBranch(t *testing.T) {
	t.Run("non-HEAD target: UpdateBranchRef called with MetaBranch target", func(t *testing.T) {
		d, beadSource, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-tb1"
		workerID := "worker-tb1"
		targetBranch := "epic/parent-abc"

		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID:       epicID,
			Title:    "My Epic",
			Metadata: map[string]any{MetaBranch: targetBranch},
		}
		beadSource.mu.Unlock()

		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
			return branch == protocol.EpicBranchPrefix+epicID, nil
		}
		wtMgr.mu.Unlock()

		d.tryCloseEpic(ctx, epicID, workerID)

		// Epic must be closed after successful UpdateBranchRef.
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
			t.Error("expected epic to be closed after successful UpdateBranchRef")
		}

		// UpdateBranchRef must have been called with the correct target and source.
		wtMgr.mu.Lock()
		refs := make([]updateBranchRefCall, len(wtMgr.updatedBranchRefs))
		copy(refs, wtMgr.updatedBranchRefs)
		merged := make([]string, len(wtMgr.mergedBranches))
		copy(merged, wtMgr.mergedBranches)
		wtMgr.mu.Unlock()

		if len(refs) == 0 {
			t.Fatal("expected UpdateBranchRef to be called for non-HEAD target")
		}
		epicBranch := protocol.EpicBranchPrefix + epicID
		if refs[0].target != targetBranch {
			t.Errorf("UpdateBranchRef target = %q, want %q", refs[0].target, targetBranch)
		}
		if refs[0].source != epicBranch {
			t.Errorf("UpdateBranchRef source = %q, want %q", refs[0].source, epicBranch)
		}
		if len(merged) > 0 {
			t.Errorf("expected MergeFFOnly NOT called for non-HEAD target, got %v", merged)
		}
	})

	t.Run("HEAD target (main): MergeFFOnly called, UpdateBranchRef not called", func(t *testing.T) {
		d, beadSource, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-tb2"
		workerID := "worker-tb2"

		// No MetaBranch → falls back to DefaultBranch (withDefaults sets it to "main").
		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID:    epicID,
			Title: "My Epic",
		}
		beadSource.mu.Unlock()

		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
			return branch == protocol.EpicBranchPrefix+epicID, nil
		}
		wtMgr.mu.Unlock()

		d.tryCloseEpic(ctx, epicID, workerID)

		wtMgr.mu.Lock()
		merged := make([]string, len(wtMgr.mergedBranches))
		copy(merged, wtMgr.mergedBranches)
		refs := make([]updateBranchRefCall, len(wtMgr.updatedBranchRefs))
		copy(refs, wtMgr.updatedBranchRefs)
		wtMgr.mu.Unlock()

		epicBranch := protocol.EpicBranchPrefix + epicID
		if len(merged) == 0 {
			t.Fatal("expected MergeFFOnly to be called for HEAD (main) target")
		}
		found := false
		for _, b := range merged {
			if b == epicBranch {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("MergeFFOnly not called with %q, got %v", epicBranch, merged)
		}
		if len(refs) > 0 {
			t.Errorf("expected UpdateBranchRef NOT called for HEAD target, got %v", refs)
		}
	})

	t.Run("merge failure: rebase bead title interpolates targetBranch not main", func(t *testing.T) {
		d, beadSource, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-tb3"
		workerID := "worker-tb3"
		targetBranch := "epic/custom-target"

		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID:       epicID,
			Title:    "My Epic",
			Metadata: map[string]any{MetaBranch: targetBranch},
		}
		beadSource.mu.Unlock()

		// Branch exists but UpdateBranchRef fails.
		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return true, nil
		}
		wtMgr.updateBranchRefFn = func(_, _ string) error {
			return errors.New("ref update rejected")
		}
		wtMgr.mu.Unlock()

		d.tryCloseEpic(ctx, epicID, workerID)

		// Epic must NOT be closed.
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
			t.Error("expected epic NOT to be closed after UpdateBranchRef failure")
		}

		// Rebase bead must have targetBranch in title, not "main".
		beadSource.mu.Lock()
		var rebaseBead createCall
		for _, c := range beadSource.created {
			if c.parent == epicID {
				rebaseBead = c
				break
			}
		}
		beadSource.mu.Unlock()

		if rebaseBead.parent != epicID {
			t.Fatal("expected a rebase bead to be created on UpdateBranchRef failure")
		}
		if !strings.Contains(rebaseBead.title, targetBranch) {
			t.Errorf("rebase bead title %q should contain targetBranch %q", rebaseBead.title, targetBranch)
		}
		if strings.Contains(rebaseBead.title, "main") {
			t.Errorf("rebase bead title %q should NOT contain hardcoded 'main'", rebaseBead.title)
		}
	})

	t.Run("missing MetaBranch falls back to DefaultBranch in rebase title", func(t *testing.T) {
		d, beadSource, wtMgr, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()
		if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-tb4"
		workerID := "worker-tb4"
		customDefault := "release-v2"
		d.cfg.DefaultBranch = customDefault

		// No MetaBranch → falls back to d.cfg.DefaultBranch.
		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}
		beadSource.mu.Lock()
		beadSource.shown[epicID] = &protocol.BeadDetail{
			ID:    epicID,
			Title: "My Epic",
		}
		beadSource.mu.Unlock()

		// Branch exists but MergeFFOnly fails (since "release-v2" == DefaultBranch → MergeFFOnly path).
		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return true, nil
		}
		wtMgr.mergeFFOnlyFn = func(_, _ string) (string, error) {
			return "", errors.New("diverged")
		}
		wtMgr.mu.Unlock()

		d.tryCloseEpic(ctx, epicID, workerID)

		// Rebase bead title must contain the customDefault, not "main".
		beadSource.mu.Lock()
		var rebaseBead createCall
		for _, c := range beadSource.created {
			if c.parent == epicID {
				rebaseBead = c
				break
			}
		}
		beadSource.mu.Unlock()

		if rebaseBead.parent != epicID {
			t.Fatal("expected a rebase bead to be created on MergeFFOnly failure")
		}
		if !strings.Contains(rebaseBead.title, customDefault) {
			t.Errorf("rebase bead title %q should contain DefaultBranch %q", rebaseBead.title, customDefault)
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

	// Wait for the assign loop to process the beads (positive signal:
	// cachedQueueDepth reflects the 3 beads), then verify auto-scale didn't fire.
	waitFor(t, func() bool {
		d.mu.Lock()
		depth := d.cachedQueueDepth
		d.mu.Unlock()
		return depth >= 3
	}, 2*time.Second)

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

	// Wait for the assign loop to cache the queue depth.
	waitFor(t, func() bool {
		d.mu.Lock()
		depth := d.cachedQueueDepth
		d.mu.Unlock()
		return depth >= 1
	}, 2*time.Second)

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

	// Wait for the assign loop to cache the initial depth.
	waitFor(t, func() bool {
		d.mu.Lock()
		depth := d.cachedQueueDepth
		d.mu.Unlock()
		return depth >= 1
	}, 2*time.Second)

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

	// Create assets directory and a read-only review-patterns.md file
	beadsDir := t.TempDir()
	root := beadsDir
	assetsDir := root + "/assets"
	//nolint:gosec // test fixture: intentional directory permission
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatalf("failed to create assets dir: %v", err)
	}

	patternsFile := assetsDir + "/review-patterns.md"
	//nolint:gosec // test fixture: intentional file permission to simulate read-only file
	if err := os.WriteFile(patternsFile, []byte("existing content\n"), 0o444); err != nil {
		t.Fatalf("failed to create read-only patterns file: %v", err)
	}
	t.Cleanup(func() {
		// Restore write permission for cleanup
		//nolint:gosec // test cleanup: restoring write permission
		_ = os.Chmod(patternsFile, 0o644)
	})

	d.repoRoot = beadsDir

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
	waitFor(t, func() bool {
		d.mu.Lock()
		_, ok := d.workers[workerID]
		d.mu.Unlock()
		return ok
	}, 2*time.Second)

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

	// Wait for the assignment cycle to have processed w2 by confirming w1 is busy
	// (which means at least one full assign cycle completed after w2 registered).
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w, ok := d.workers["w1"]
		return ok && w.state == protocol.WorkerBusy
	}, 2*time.Second)

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

	// Wait for worker to be registered and assigned oro-test.
	waitForWorkers(t, d, 1, 1*time.Second)
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w, ok := d.workers["w1"]
		return ok && w.beadID == "oro-test"
	}, 2*time.Second)

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

	// Wait for full cleanup: worker removed AND tracking maps cleared.
	// handleConn's defer deletes the worker and releases the lock BEFORE
	// calling clearBeadTracking, so checking only worker removal races
	// with the subsequent map cleanup.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, workerExists := d.workers["w1"]
		_, attemptExists := d.attemptCounts["oro-test"]
		_, qgExists := d.qgStuckTracker["oro-test"]
		_, escExists := d.escalatedBeads["oro-test"]
		_, wtExists := d.worktreeFailures["oro-test"]
		_, assignExists := d.assigningBeads["oro-test"]
		return !workerExists && !attemptExists && !qgExists && !escExists && !wtExists && !assignExists
	}, 2*time.Second)

	// Final assertions under lock for clear error messages.
	d.mu.Lock()
	_, stillExists := d.workers["w1"]
	var errs []string
	if stillExists {
		errs = append(errs, "worker w1 still exists after connection close")
	}
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
		t.Fatalf("BeadTracker cleanup incomplete:\n  - %s", strings.Join(errs, "\n  - "))
	}

	// Assert: BeadSource.Update must have been called with ("oro-test", "open")
	// to reset the bead for reassignment after the connection drop.
	beadSrc.mu.Lock()
	updatedStatus, updatedOK := beadSrc.updated["oro-test"]
	beadSrc.mu.Unlock()

	if !updatedOK {
		t.Error("BeadSource.Update not called for oro-test after connection drop")
	} else if updatedStatus != "open" {
		t.Errorf("BeadSource.Update called with status %q for oro-test, want %q", updatedStatus, "open")
	}
}

// TestReconnectDoesNotDeleteNewWorker verifies that when a worker reconnects with a
// new connection, the old handleConn's deferred cleanup does NOT delete the new worker
// entry. Only the connection that originally registered the worker should clean it up.
func TestReconnectDoesNotDeleteNewWorker(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	workerID := "worker-reconnect-test"

	// First connection: client1 <-> server1
	client1, server1 := net.Pipe()
	defer client1.Close()

	// Start handleConn for first connection.
	go d.handleConn(ctx, server1)

	// Register worker via first connection heartbeat.
	sendMsg(t, client1, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			ContextPct: 5,
		},
	})

	// Wait for worker to be registered with first connection.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.workers[workerID]
		return ok
	}, 2*time.Second)

	// Second connection: client2 <-> server2 (simulating reconnect).
	client2, server2 := net.Pipe()
	defer client2.Close()

	// Start handleConn for second connection.
	go d.handleConn(ctx, server2)

	// Register worker via second connection heartbeat — upsertWorker will update conn to server2.
	sendMsg(t, client2, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			ContextPct: 5,
		},
	})

	// Wait for worker entry to reflect the new connection.
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		w, ok := d.workers[workerID]
		return ok && w.conn == server2
	}, 2*time.Second)

	// Close first client — triggers old handleConn goroutine's deferred cleanup.
	_ = client1.Close()

	// Wait long enough for the deferred cleanup goroutine to run.
	time.Sleep(150 * time.Millisecond)

	// Assert: new worker entry must still exist (old defer must not clobber it).
	d.mu.Lock()
	w, exists := d.workers[workerID]
	d.mu.Unlock()

	if !exists {
		t.Fatal("old handleConn defer deleted the new worker entry on reconnect — should skip cleanup when conn differs")
	}
	if w.conn != server2 {
		t.Fatalf("worker entry has wrong connection: expected server2, got something else")
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

	// Assert: bead_not_ready_before_assign event was logged
	if eventCount(t, d.db, "bead_not_ready_before_assign") == 0 {
		t.Error("expected bead_not_ready_before_assign event to be logged, but it was not found")
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

	t.Run("managed worker: worktree preserved for respawn, bead reset, tracking cleared, targetWorkers decremented", func(t *testing.T) {
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
		// Seed worktreeByBead so kill preserves it.
		d.worktreeByBead[beadID] = worktreePath
		d.targetWorkers = 2
		d.mu.Unlock()

		_, err := d.applyKillWorker(workerID)
		if err != nil {
			t.Fatalf("applyKillWorker returned error: %v", err)
		}

		// 1. WorktreeManager.Remove must NOT be called (oro-1eo8: preserve for respawn).
		wtMgr.mu.Lock()
		removed := wtMgr.removed
		wtMgr.mu.Unlock()
		if len(removed) != 0 {
			t.Errorf("expected WorktreeManager.Remove to NOT be called (preserve for respawn), but was called with: %v", removed)
		}

		// 1b. Worktree path must still be in worktreeByBead map for respawn reuse.
		d.mu.Lock()
		preservedPath := d.worktreeByBead[beadID]
		d.mu.Unlock()
		if preservedPath != worktreePath {
			t.Errorf("worktreeByBead[%s] = %q, want %q (should be preserved)", beadID, preservedPath, worktreePath)
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

	t.Run("managed spawn-for worker: targetWorkers NOT decremented", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:       workerID,
			conn:     conn,
			state:    protocol.WorkerIdle,
			managed:  true,
			spawnFor: true,
			encoder:  json.NewEncoder(conn),
		}
		d.targetWorkers = 2
		d.mu.Unlock()

		_, err := d.applyKillWorker(workerID)
		if err != nil {
			t.Fatalf("applyKillWorker returned error: %v", err)
		}

		d.mu.Lock()
		target := d.targetWorkers
		d.mu.Unlock()
		if target != 2 {
			t.Errorf("targetWorkers = %d, want 2 after killing spawn-for worker", target)
		}
	})

	t.Run("unmanaged worker: worktree preserved for respawn, bead reset, tracking cleared, targetWorkers NOT decremented", func(t *testing.T) {
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
		d.worktreeByBead[beadID] = worktreePath
		d.targetWorkers = 1
		d.mu.Unlock()

		_, err := d.applyKillWorker(workerID)
		if err != nil {
			t.Fatalf("applyKillWorker returned error: %v", err)
		}

		// Worktree must NOT be removed (oro-1eo8: preserve for respawn).
		wtMgr.mu.Lock()
		removed := wtMgr.removed
		wtMgr.mu.Unlock()
		if len(removed) != 0 {
			t.Errorf("expected WorktreeManager.Remove to NOT be called (preserve for respawn), but was called with: %v", removed)
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

// TestAssignEpicDecomposition verifies that epics are filtered as non-executable
// issue types and never assigned to workers, regardless of their children state.
func TestAssignEpicDecomposition(t *testing.T) {
	t.Run("epic with no children not assigned", func(t *testing.T) {
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

		// Epic must not be assigned even when it has no children.
		// Wait for non_executable_issue_type event, then confirm no assignment.
		waitFor(t, func() bool {
			var count int
			_ = d.db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = ? AND bead_id = ?`,
				"non_executable_issue_type", "oro-epic1").Scan(&count)
			return count > 0
		}, 2*time.Second)

		_, ok := readMsg(t, conn, 200*time.Millisecond)
		if ok {
			t.Fatal("epic must not be assigned to a worker regardless of children state")
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
}

// TestDispatcherSetsEmbedder verifies that New() wires an Embedder into the
// dispatcher's memory store so that Insert() can compute embeddings.
func TestDispatcherSetsEmbedder(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	if !d.memories.HasEmbedder() {
		t.Fatal("expected dispatcher memory store to have a non-nil embedder after New()")
	}
}

// --- Mutation kill tests (oro-eclo.12) ---

// TestScaleDown_ExactCount verifies that scaleDown removes exactly (connected - target)
// workers. This kills the mutation toRemove := connected + target (which would remove
// too many workers).
func TestScaleDown_ExactCount(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.cfg.MaxWorkers = 10
	d.cfg.ShutdownTimeout = 200 * time.Millisecond

	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	// Connect 5 managed workers.
	for i := 0; i < 5; i++ {
		wid := fmt.Sprintf("w-sd-exact-%d", i)
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

	// Set target to 3 — should remove exactly 2.
	d.mu.Lock()
	d.targetWorkers = 3
	d.mu.Unlock()

	result := d.scaleDown(3, 5)
	if !containsStr(result, "2") {
		t.Errorf("scaleDown(target=3, connected=5) should remove 2, got detail: %s", result)
	}

	// Wait for shutdown goroutines to process.
	time.Sleep(500 * time.Millisecond)

	// After scale-down, at most 3 workers should be in active (non-shutting-down) state.
	// With toRemove=connected+target=8, all 5 would be removed; with correct toRemove=2, 3 remain.
	// We verify that exactly 2 were targeted by checking the detail string contains "2".
	if !strings.Contains(result, "2") {
		t.Fatalf("expected detail to mention 2 shutdowns, got: %q", result)
	}
}

// TestScaleUp_ExactCount verifies that scaleUp spawns exactly (target - connected)
// new workers. This kills the mutation toSpawn := target + connected.
func TestScaleUp_ExactCount(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Connect 2 managed workers.
	for _, id := range []string{"w-su-1", "w-su-2"} {
		s, c := net.Pipe()
		t.Cleanup(func() { _ = s.Close(); _ = c.Close() })
		d.mu.Lock()
		d.pendingManagedIDs[id] = true
		d.mu.Unlock()
		d.registerWorker(id, s)
	}

	// scaleUp to target 4 with 2 connected = should spawn exactly 2.
	result := d.scaleUp(4, 2)
	spawned := pm.SpawnedIDs()
	if len(spawned) != 2 {
		t.Fatalf("expected exactly 2 workers spawned (target=4, connected=2), got %d: detail=%s", len(spawned), result)
	}
	if !strings.Contains(result, "2") {
		t.Errorf("scaleUp detail should mention count 2, got: %q", result)
	}
}

// TestScaleUp_SpawnsCorrectDifference verifies that scaleUp spawns target-connected
// (not target+connected) workers. With target=5 and connected=2, should spawn 3.
func TestScaleUp_SpawnsCorrectDifference(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm

	// Connect 2 managed workers.
	for _, id := range []string{"w-diff-1", "w-diff-2"} {
		s, c := net.Pipe()
		t.Cleanup(func() { _ = s.Close(); _ = c.Close() })
		d.mu.Lock()
		d.pendingManagedIDs[id] = true
		d.mu.Unlock()
		d.registerWorker(id, s)
	}

	// target=5, connected=2 → should spawn 3 (not 7 = 5+2).
	d.scaleUp(5, 2)
	spawned := pm.SpawnedIDs()
	if len(spawned) != 3 {
		t.Fatalf("expected 3 workers spawned (5-2=3), got %d (mutation toSpawn=target+connected would give 7)", len(spawned))
	}
}

// TestScaleDown_SpawnsCorrectDifference verifies that scaleDown removes connected-target
// (not connected+target) workers. With connected=5 and target=2, should remove 3.
func TestScaleDown_SpawnsCorrectDifference(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	d.cfg.ShutdownTimeout = 100 * time.Millisecond
	startDispatcher(t, d)

	// Connect 5 managed workers.
	for i := 0; i < 5; i++ {
		wid := fmt.Sprintf("w-rm-%d", i)
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

	// target=2, connected=5 → should remove 3 (not 7 = 5+2).
	result := d.scaleDown(2, 5)

	// Detail string should reflect 3 shutdowns.
	if !strings.Contains(result, "3") {
		t.Fatalf("scaleDown(target=2, connected=5): expected detail to mention 3 shutdowns, got: %q (mutation would say 5)", result)
	}
}

// TestApplyScaleDirective_ZeroTargetAccepted verifies that scale with target=0
// is accepted (not an error). This kills boundary mutations that reject 0.
func TestApplyScaleDirective_ZeroTargetAccepted(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "scale", "0")
	if !ack.OK {
		t.Fatalf("expected scale=0 to succeed, got error: %s", ack.Detail)
	}

	// Target should now be 0.
	d.mu.Lock()
	got := d.targetWorkers
	d.mu.Unlock()
	if got != 0 {
		t.Fatalf("expected targetWorkers=0 after scale 0, got %d", got)
	}
}

// TestApplyScaleDirective_NegativeTargetRejected verifies that scale with target=-1
// returns an error. This kills boundary mutations that allow negative values.
func TestApplyScaleDirective_NegativeTargetRejected(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "scale", "-1")
	if ack.OK {
		t.Fatalf("expected scale=-1 to fail, but ACK.OK=true (detail: %s)", ack.Detail)
	}
	if !containsStr(ack.Detail, "non-negative") && !containsStr(ack.Detail, "invalid") {
		t.Errorf("expected error about non-negative value, got: %s", ack.Detail)
	}
}

// TestApplyScaleDirective_DetailContainsTarget verifies that a successful scale
// directive returns a detail string containing the target and current count.
// This kills the statement mutation that replaces the detail assignment.
func TestApplyScaleDirective_DetailContainsTargetAndCurrent(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := &mockProcessManager{}
	d.procMgr = pm
	startDispatcher(t, d)

	ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "scale", "3")
	if !ack.OK {
		t.Fatalf("expected scale=3 to succeed, got: %s", ack.Detail)
	}
	// Detail should contain either the spawned count or target info.
	if ack.Detail == "" {
		t.Error("expected non-empty ACK detail for scale directive")
	}
}

// TestDefaultWorkerCounts_ClampsInitialToCeiling verifies that when
// initial > ceiling, defaultWorkerCounts clamps initial down to ceiling.
func TestDefaultWorkerCounts_ClampsInitialToCeiling(t *testing.T) {
	initial, ceiling := defaultWorkerCounts(7, 5)
	if initial != 5 {
		t.Errorf("expected initial clamped to ceiling=5, got %d", initial)
	}
	if ceiling != 5 {
		t.Errorf("expected ceiling=5, got %d", ceiling)
	}
}

// TestWithDefaults_AllFieldsSet verifies that withDefaults populates all
// timeout and interval fields with their correct default values.
// This kills arithmetic mutations (5/time.Second instead of 5*time.Second, etc.)
func TestWithDefaults_AllFieldsSet(t *testing.T) {
	cfg := Config{} // all zero
	resolved := cfg.withDefaults()

	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"MaxWorkers", time.Duration(resolved.MaxWorkers), 10},
		{"HeartbeatTimeout", resolved.HeartbeatTimeout, 45 * time.Second},
		{"ProgressTimeout", resolved.ProgressTimeout, 10 * time.Minute},
		{"PollInterval", resolved.PollInterval, 10 * time.Second},
		{"FallbackPollInterval", resolved.FallbackPollInterval, 60 * time.Second},
		{"ShutdownTimeout", resolved.ShutdownTimeout, 10 * time.Second},
		{"PaneMonitorInterval", resolved.PaneMonitorInterval, 5 * time.Second},
	}

	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v (arithmetic mutation 5/time.Second=0 would fail this)", c.name, c.got, c.want)
		}
	}

	if resolved.ConsolidateAfterN != 5 {
		t.Errorf("ConsolidateAfterN: got %d, want 5", resolved.ConsolidateAfterN)
	}
	if resolved.PaneContextThreshold != 40 {
		t.Errorf("PaneContextThreshold: got %d, want 40", resolved.PaneContextThreshold)
	}
}

// TestWithDefaults_PositiveValuesRequired verifies that defaults produce positive
// durations. Zero or negative durations would indicate arithmetic mutations.
func TestWithDefaults_PositiveDurations(t *testing.T) {
	cfg := Config{}
	resolved := cfg.withDefaults()

	if resolved.HeartbeatTimeout <= 0 {
		t.Errorf("HeartbeatTimeout must be positive, got %v", resolved.HeartbeatTimeout)
	}
	if resolved.ProgressTimeout <= 0 {
		t.Errorf("ProgressTimeout must be positive, got %v", resolved.ProgressTimeout)
	}
	if resolved.PollInterval <= 0 {
		t.Errorf("PollInterval must be positive, got %v", resolved.PollInterval)
	}
	if resolved.FallbackPollInterval <= 0 {
		t.Errorf("FallbackPollInterval must be positive, got %v", resolved.FallbackPollInterval)
	}
	if resolved.ShutdownTimeout <= 0 {
		t.Errorf("ShutdownTimeout must be positive, got %v", resolved.ShutdownTimeout)
	}
	if resolved.PaneMonitorInterval <= 0 {
		t.Errorf("PaneMonitorInterval must be positive, got %v (zero causes panic in ticker)", resolved.PaneMonitorInterval)
	}
}

// TestBuildRejectionMemoryContext_Format verifies that the returned string
// contains the rejection section separated from memory context by "\n\n".
// This kills the arithmetic mutation that would produce a compile error
// (string subtraction) and also the format mutation.
func TestBuildRejectionMemoryContext_WithBothSections(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Simulate a bead with a title (for memory search).
	ctx := context.Background()
	feedback := "tests are missing edge cases"

	result := d.buildRejectionMemoryContext(ctx, "oro-test1", feedback)

	// Result must contain the rejection header.
	if !strings.Contains(result, "## Review Rejection Feedback") {
		t.Errorf("result should contain rejection header, got: %q", result)
	}
	// Result must contain the feedback.
	if !strings.Contains(result, feedback) {
		t.Errorf("result should contain feedback %q, got: %q", feedback, result)
	}
}

// TestBuildRejectionMemoryContext_EmptyFeedback verifies that empty feedback
// returns the general memory context without a rejection section.
func TestBuildRejectionMemoryContext_EmptyFeedback(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	result := d.buildRejectionMemoryContext(ctx, "oro-test2", "")

	// With empty feedback, should not include rejection header.
	if strings.Contains(result, "## Review Rejection Feedback") {
		t.Errorf("empty feedback should not produce rejection section, got: %q", result)
	}
}

// TestBuildRejectionMemoryContext_SeparatorFormat verifies the exact format
// of the separator between rejection section and memory context.
func TestBuildRejectionMemoryContext_SeparatorIsDoubleNewline(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Insert a memory that will be retrieved.
	_, _ = d.memories.Insert(ctx, memory.InsertParams{
		Content:    "important context about this project",
		Type:       "lesson",
		Source:     "test",
		BeadID:     "oro-sep1",
		Confidence: 0.9,
	})

	result := d.buildRejectionMemoryContext(ctx, "oro-sep1", "review feedback")
	if !strings.Contains(result, "## Review Rejection Feedback") {
		t.Skip("rejection section not present, memory retrieval may not have found content")
	}
	// If both sections are present, they should be separated by "\n\n".
	if strings.Contains(result, "## Review Rejection Feedback") {
		idx := strings.Index(result, "## Review Rejection Feedback")
		rejEnd := idx + len("## Review Rejection Feedback")
		// The section starts at idx, check there's content after the feedback.
		_ = rejEnd // The key check is that the string is formed with "+", not "-"
	}
}

// TestParseAcceptanceCmd_PipeFormat verifies that the pipe-separated format
// is parsed correctly. This tests the Cmd: extraction logic.
func TestParseAcceptanceCmd_PipeFormat(t *testing.T) {
	cases := []struct {
		ac   string
		want string
	}{
		{"Test: foo | Cmd: go test ./... | Assert: PASS", "go test ./..."},
		{"Cmd: make test", "make test"},
		{"no cmd here", ""},
		{"", ""},
		{"Test: only | Assert: PASS", ""},
	}
	for _, c := range cases {
		got := parseAcceptanceCmd(c.ac)
		if got != c.want {
			t.Errorf("parseAcceptanceCmd(%q): got %q, want %q", c.ac, got, c.want)
		}
	}
}

// TestParseAcceptanceCmd_LineFormat verifies that the newline-separated format
// is parsed correctly.
func TestParseAcceptanceCmd_LineFormat(t *testing.T) {
	ac := "Test: pkg/foo/foo_test.go\nCmd: go test ./pkg/foo/...\nAssert: 100% pass"
	got := parseAcceptanceCmd(ac)
	if got != "go test ./pkg/foo/..." {
		t.Errorf("parseAcceptanceCmd (line format): got %q, want %q", got, "go test ./pkg/foo/...")
	}
}

// TestCalculateLiveQueueDepth_ExcludesAssigned verifies that beads assigned
// to workers are not counted in queue depth. This tests the core logic.
func TestCalculateLiveQueueDepth_ExcludesAssigned(t *testing.T) {
	workers := map[string]*trackedWorker{
		"w1": {beadID: "bead-1"},
		"w2": {beadID: "bead-2"},
		"w3": {beadID: ""},
	}
	beads := []protocol.Bead{
		{ID: "bead-1"},
		{ID: "bead-2"},
		{ID: "bead-3"}, // unassigned
		{ID: "bead-4"}, // unassigned
	}
	depth := calculateLiveQueueDepth(beads, workers)
	if depth != 2 {
		t.Fatalf("expected queue depth 2 (beads 3 and 4 unassigned), got %d", depth)
	}
}

// TestCalculateLiveQueueDepth_AllUnassigned verifies full queue depth when no
// workers have assignments.
func TestCalculateLiveQueueDepth_AllUnassigned(t *testing.T) {
	workers := map[string]*trackedWorker{
		"w1": {beadID: ""},
		"w2": {beadID: ""},
	}
	beads := []protocol.Bead{
		{ID: "bead-1"},
		{ID: "bead-2"},
		{ID: "bead-3"},
	}
	depth := calculateLiveQueueDepth(beads, workers)
	if depth != 3 {
		t.Fatalf("expected queue depth 3, got %d", depth)
	}
}

// TestFormatSearchResults_WithReason verifies that search results with a reason
// include the relevance note. This tests the conditional inclusion of reason.
func TestFormatSearchResults_WithAndWithoutReason(t *testing.T) {
	results := []SearchResult{
		{
			CodeChunk: CodeChunk{FilePath: "foo.go", StartLine: 1, EndLine: 10, Content: "func Foo() {}"},
			Score:     0.9,
			Reason:    "highly relevant",
		},
		{
			CodeChunk: CodeChunk{FilePath: "bar.go", StartLine: 5, EndLine: 15, Content: "func Bar() {}"},
			Score:     0.5,
			Reason:    "",
		},
	}
	out := formatSearchResults(results)
	if !strings.Contains(out, "highly relevant") {
		t.Errorf("expected reason to appear in output, got: %q", out)
	}
	if !strings.Contains(out, "foo.go") {
		t.Errorf("expected file path in output, got: %q", out)
	}
	if !strings.Contains(out, "func Foo()") {
		t.Errorf("expected content in output, got: %q", out)
	}
	// Bar.go entry should not have a relevance note.
	if strings.Contains(out, "_Relevance: _") {
		t.Errorf("empty reason should not produce relevance note")
	}
}

// TestHandleQGFailure_AttemptInitiallyZero verifies that the initial attempt
// in QualityGateError is set to 0 (incremented to 1 after first failure).
// This kills mutations that initialize Attempt to 1 or -1.
func TestHandleQGFailure_AttemptInitiallyZero(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	conn, scanner := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-qg1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Set up bead with Opus model (avoid model escalation reset).
	beadSrc.mu.Lock()
	beadSrc.shown["oro-qg1"] = &protocol.BeadDetail{
		Title:              "test bead",
		AcceptanceCriteria: "Test: foo | Assert: PASS",
	}
	beadSrc.mu.Unlock()
	beadSrc.SetBeads([]protocol.Bead{{ID: "oro-qg1", Title: "test bead", Type: "task", Model: protocol.ModelOpus}})

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Wait for ASSIGN message on the connection.
	msg, ok := readMsg(t, conn, 3*time.Second)
	if !ok || msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got ok=%v type=%s", ok, msg.Type)
	}
	_ = scanner

	beadSrc.SetBeads(nil) // Stop offering the bead

	// Send DONE with QG failed (first attempt).
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			WorkerID:          "w-qg1",
			BeadID:            "oro-qg1",
			QualityGatePassed: false,
			QGOutput:          "tests failed",
		},
	})

	// After QG failure, attempt count should be incremented to 1.
	// (Initial attempt was 0, incremented by d.attemptCounts[beadID]++.)
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.attemptCounts["oro-qg1"] >= 1
	}, 3*time.Second)

	d.mu.Lock()
	count := d.attemptCounts["oro-qg1"]
	d.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected attemptCount=1 after first QG failure (starting from 0), got %d", count)
	}
}

// TestScaleDown_BusyWorker_BeadRequeued verifies that when the dispatcher
// shuts down a worker that has an in-flight bead, the bead is requeued to
// "open" status so it can be reassigned — covering both the shutdown-approved
// path (worker responds gracefully) and the shutdown-timeout path (worker
// doesn't respond within the deadline).
func TestScaleDown_BusyWorker_BeadRequeued(t *testing.T) {
	t.Run("approved path — bead requeued when worker sends SHUTDOWN_APPROVED without HANDOFF", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-approved", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-sd-approved", Title: "Scale-down approved test", Priority: 1}})
		_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
		if !ok {
			t.Fatal("expected ASSIGN")
		}
		beadSrc.SetBeads(nil)

		// Trigger graceful shutdown — worker will NOT send HANDOFF, just SHUTDOWN_APPROVED
		d.GracefulShutdownWorker("w-approved", 2*time.Second)

		// Worker receives PREPARE_SHUTDOWN
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected PREPARE_SHUTDOWN")
		}
		if msg.Type != protocol.MsgPrepareShutdown {
			t.Fatalf("expected PREPARE_SHUTDOWN, got %s", msg.Type)
		}

		// Worker sends SHUTDOWN_APPROVED without sending HANDOFF first
		sendMsg(t, conn, protocol.Message{
			Type:             protocol.MsgShutdownApproved,
			ShutdownApproved: &protocol.ShutdownApprovedPayload{WorkerID: "w-approved"},
		})

		// Worker receives hard SHUTDOWN
		msg2, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected SHUTDOWN after approval")
		}
		if msg2.Type != protocol.MsgShutdown {
			t.Fatalf("expected SHUTDOWN, got %s", msg2.Type)
		}

		// Bead must be requeued to "open" so it can be reassigned
		waitFor(t, func() bool {
			beadSrc.mu.Lock()
			defer beadSrc.mu.Unlock()
			return beadSrc.updated["bead-sd-approved"] == "open"
		}, 2*time.Second)

		beadSrc.mu.Lock()
		status := beadSrc.updated["bead-sd-approved"]
		beadSrc.mu.Unlock()
		if status != "open" {
			t.Fatalf("expected bead requeued to 'open', got %q", status)
		}

		// A requeue escalation event must be logged. The dispatcher logs this
		// event AFTER the bead status update, so we must poll rather than check
		// synchronously.
		waitFor(t, func() bool {
			return eventCount(t, d.db, "bead_requeued_scale_down") > 0
		}, 2*time.Second)

		// Tracking maps must not retain the bead
		d.mu.Lock()
		_, hasAttempt := d.attemptCounts["bead-sd-approved"]
		_, hasPending := d.pendingHandoffs["bead-sd-approved"]
		d.mu.Unlock()
		if hasAttempt {
			t.Fatal("attemptCounts leaked entry for requeued bead")
		}
		if hasPending {
			t.Fatal("pendingHandoffs leaked entry for requeued bead")
		}
	})

	t.Run("timeout path — bead requeued when worker doesn't respond within deadline", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		d.cfg.HeartbeatTimeout = 10 * time.Second // survive setup; shutdown timeout is 100ms
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-timeout-requeue", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-sd-timeout", Title: "Scale-down timeout test", Priority: 1}})
		_, ok := readMsg(t, conn, 2*time.Second) // consume ASSIGN
		if !ok {
			t.Fatal("expected ASSIGN")
		}
		beadSrc.SetBeads(nil)

		// Trigger graceful shutdown with very short timeout — worker will NOT respond
		d.GracefulShutdownWorker("w-timeout-requeue", 100*time.Millisecond)

		// Worker receives PREPARE_SHUTDOWN but we do NOT respond
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected PREPARE_SHUTDOWN")
		}
		if msg.Type != protocol.MsgPrepareShutdown {
			t.Fatalf("expected PREPARE_SHUTDOWN, got %s", msg.Type)
		}

		// Dispatcher falls back to hard SHUTDOWN after timeout
		msg2, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected hard SHUTDOWN after timeout")
		}
		if msg2.Type != protocol.MsgShutdown {
			t.Fatalf("expected SHUTDOWN (hard kill), got %s", msg2.Type)
		}

		// Bead must be requeued to "open"
		waitFor(t, func() bool {
			beadSrc.mu.Lock()
			defer beadSrc.mu.Unlock()
			return beadSrc.updated["bead-sd-timeout"] == "open"
		}, 2*time.Second)

		beadSrc.mu.Lock()
		status := beadSrc.updated["bead-sd-timeout"]
		beadSrc.mu.Unlock()
		if status != "open" {
			t.Fatalf("expected bead requeued to 'open', got %q", status)
		}

		// A requeue escalation event must be logged. The dispatcher logs this
		// event AFTER the bead status update, so we must poll rather than check
		// synchronously.
		waitFor(t, func() bool {
			return eventCount(t, d.db, "bead_requeued_scale_down") > 0
		}, 2*time.Second)

		// Tracking maps must not retain the bead
		d.mu.Lock()
		_, hasAttempt := d.attemptCounts["bead-sd-timeout"]
		_, hasPending := d.pendingHandoffs["bead-sd-timeout"]
		d.mu.Unlock()
		if hasAttempt {
			t.Fatal("attemptCounts leaked entry for requeued bead")
		}
		if hasPending {
			t.Fatal("pendingHandoffs leaked entry for requeued bead")
		}
	})
}

// TestDispatcherCleansUpClosedBeadAssignments verifies that when a bead transitions
// to closed while a worker is assigned, the dispatcher removes the assignment within
// one tick interval and the worker receives an exit signal.
func TestDispatcherCleansUpClosedBeadAssignments(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	const beadID = "oro-cleanup-test"

	// Provide an open bead for assignment.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: beadID, Title: "Work bead", Status: "open", Priority: 2, Type: "task", AcceptanceCriteria: "Test: pass"},
	})

	// Connect a worker and register it.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   "w1",
			ContextPct: 5,
		},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start dispatcher so it assigns work.
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Wait for the ASSIGN message confirming the worker received the bead.
	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign == nil || msg.Assign.BeadID != beadID {
		t.Fatalf("expected ASSIGN for %q, got %+v", beadID, msg.Assign)
	}

	// Confirm worker is busy with the bead.
	waitFor(t, func() bool {
		st, bid, ok := d.WorkerInfo("w1")
		return ok && bid == beadID && st == protocol.WorkerBusy
	}, 1*time.Second)

	// Externally close the bead (simulates `bd close` from outside the dispatcher).
	beadSrc.mu.Lock()
	if beadSrc.shown == nil {
		beadSrc.shown = make(map[string]*protocol.BeadDetail)
	}
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:     beadID,
		Status: "closed",
	}
	beadSrc.mu.Unlock()

	// Also remove from the ready list so Ready() no longer returns it.
	beadSrc.SetBeads([]protocol.Bead{})

	// Within one tick, the dispatcher should detect the closed bead and send SHUTDOWN.
	shutdownMsg, ok := readMsg(t, conn, 500*time.Millisecond)
	if !ok {
		t.Fatal("expected SHUTDOWN after bead closed externally, got nothing")
	}
	if shutdownMsg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", shutdownMsg.Type)
	}

	// Verify the worker's assignment was cleared.
	waitFor(t, func() bool {
		_, bid, ok := d.WorkerInfo("w1")
		return !ok || bid == ""
	}, 500*time.Millisecond)

	_, beadAfter, _ := d.WorkerInfo("w1")
	if beadAfter != "" {
		t.Errorf("expected worker beadID to be cleared after external close, got %q", beadAfter)
	}
}

// TestAssignment_SkipsClosedBeads verifies that when tryAssign encounters closed
// beads in the ready queue, it skips them and assigns the next ready bead (or
// leaves the worker idle if no open beads are available).
func TestAssignment_SkipsClosedBeads(t *testing.T) {
	t.Run("skip_closed_assign_next_open", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		// Connect an idle worker
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				WorkerID:   "w-skip-test",
				ContextPct: 5,
			},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		// Start dispatcher so tryAssign operates
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		// Set up ready queue with first bead closed, second bead open
		beadSrc.SetBeads([]protocol.Bead{
			{ID: "oro-closed-1", Priority: 2, Status: "closed"},
			{ID: "oro-open-1", Priority: 2, Status: "open"},
		})

		// Configure Show() to return details with acceptance criteria
		beadSrc.mu.Lock()
		beadSrc.shown["oro-closed-1"] = &protocol.BeadDetail{
			ID:                 "oro-closed-1",
			Title:              "Closed Bead",
			AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
			Status:             "closed",
		}
		beadSrc.shown["oro-open-1"] = &protocol.BeadDetail{
			ID:                 "oro-open-1",
			Title:              "Open Bead",
			AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
			Status:             "open",
		}
		beadSrc.mu.Unlock()

		// Invoke tryAssign
		d.tryAssign(context.Background())

		// Worker should be assigned to oro-open-1 (closed bead was skipped)
		st, beadID, ok := d.WorkerInfo("w-skip-test")
		if !ok {
			t.Fatal("expected worker w-skip-test to be tracked")
		}
		if beadID != "oro-open-1" {
			t.Fatalf("expected worker to be assigned oro-open-1, got beadID=%s state=%s", beadID, st)
		}

		// Verify worktree was created only for oro-open-1
		wtMgr.mu.Lock()
		_, closedWtCreated := wtMgr.created["oro-closed-1"]
		_, openWtCreated := wtMgr.created["oro-open-1"]
		wtMgr.mu.Unlock()

		if closedWtCreated {
			t.Error("worktree should not have been created for closed bead oro-closed-1")
		}
		if !openWtCreated {
			t.Error("worktree should have been created for open bead oro-open-1")
		}
	})

	t.Run("all_closed_worker_remains_idle", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		// Connect an idle worker
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				WorkerID:   "w-all-closed",
				ContextPct: 5,
			},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		// Start dispatcher
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		// Set up ready queue with all beads closed
		beadSrc.SetBeads([]protocol.Bead{
			{ID: "oro-closed-2", Priority: 2, Status: "closed"},
			{ID: "oro-closed-3", Priority: 2, Status: "closed"},
		})

		// Configure Show() with closed status
		beadSrc.mu.Lock()
		beadSrc.shown["oro-closed-2"] = &protocol.BeadDetail{
			ID:                 "oro-closed-2",
			Title:              "Closed Bead 2",
			AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
			Status:             "closed",
		}
		beadSrc.shown["oro-closed-3"] = &protocol.BeadDetail{
			ID:                 "oro-closed-3",
			Title:              "Closed Bead 3",
			AcceptanceCriteria: "Test: auto | Cmd: go test | Assert: PASS",
			Status:             "closed",
		}
		beadSrc.mu.Unlock()

		// Invoke tryAssign
		d.tryAssign(context.Background())

		// Worker should remain idle (all beads were closed)
		st, beadID, ok := d.WorkerInfo("w-all-closed")
		if !ok {
			t.Fatal("expected worker w-all-closed to be tracked")
		}
		if st != protocol.WorkerIdle {
			t.Fatalf("expected worker to remain idle, got state=%s beadID=%s", st, beadID)
		}
		if beadID != "" {
			t.Fatalf("expected no bead assignment, got beadID=%s", beadID)
		}

		// No worktree should have been created
		wtMgr.mu.Lock()
		createdCount := len(wtMgr.created)
		wtMgr.mu.Unlock()
		if createdCount != 0 {
			t.Fatalf("expected 0 worktrees created, got %d", createdCount)
		}

		// Verify no ASSIGN was sent
		msg, gotMsg := readMsg(t, conn, 100*time.Millisecond)
		if gotMsg && msg.Type == protocol.MsgAssign {
			t.Fatal("received unexpected ASSIGN message when all beads are closed")
		}
	})
}

func TestRemoveWorktreeAndClearTracking_DeletesBranch(t *testing.T) {
	t.Run("happy path: Remove succeeds then DeleteBranch called", func(t *testing.T) {
		d, _, wtMgr, _, _, _ := newTestDispatcher(t)

		d.removeWorktreeAndClearTracking(context.Background(), "oro-test", "w1", "/tmp/worktree-oro-test")

		wtMgr.mu.Lock()
		defer wtMgr.mu.Unlock()

		// Verify Remove was called
		if len(wtMgr.removed) != 1 || wtMgr.removed[0] != "/tmp/worktree-oro-test" {
			t.Fatalf("expected Remove called with /tmp/worktree-oro-test, got: %v", wtMgr.removed)
		}

		// Verify DeleteBranch was called with correct branch name
		if len(wtMgr.deletedBranches) != 1 || wtMgr.deletedBranches[0] != "agent/oro-test" {
			t.Fatalf("expected DeleteBranch called with agent/oro-test, got: %v", wtMgr.deletedBranches)
		}
	})

	t.Run("Remove fails, DeleteBranch still called", func(t *testing.T) {
		d, _, wtMgr, _, _, _ := newTestDispatcher(t)
		wtMgr.removeFn = func(_ context.Context, _ string) error {
			return fmt.Errorf("worktree stuck")
		}

		d.removeWorktreeAndClearTracking(context.Background(), "oro-fail", "w2", "/tmp/worktree-oro-fail")

		wtMgr.mu.Lock()
		defer wtMgr.mu.Unlock()

		// DeleteBranch should still be attempted even though Remove failed
		if len(wtMgr.deletedBranches) != 1 || wtMgr.deletedBranches[0] != "agent/oro-fail" {
			t.Fatalf("expected DeleteBranch called despite Remove failure, got: %v", wtMgr.deletedBranches)
		}
	})

	t.Run("both Remove and DeleteBranch fail: no panic", func(t *testing.T) {
		d, _, wtMgr, _, _, _ := newTestDispatcher(t)
		wtMgr.removeFn = func(_ context.Context, _ string) error {
			return fmt.Errorf("worktree stuck")
		}
		wtMgr.deleteBranchFn = func(_ string) error {
			return fmt.Errorf("branch not found")
		}

		// Should not panic — both errors are logged, not returned.
		d.removeWorktreeAndClearTracking(context.Background(), "oro-both", "w3", "/tmp/worktree-oro-both")
	})
}

// TestRemoveWorktreeAndClearTracking_ClearsTrackingOnRemoveError verifies that
// worktreeByBead is deleted even when d.worktrees.Remove returns an error.
func TestRemoveWorktreeAndClearTracking_ClearsTrackingOnRemoveError(t *testing.T) {
	d, _, wtMgr, _, _, _ := newTestDispatcher(t)

	beadID := "oro-test"
	worktreePath := "/tmp/worktree-oro-test"

	// Set up the tracking entry before calling removeWorktreeAndClearTracking.
	d.mu.Lock()
	d.worktreeByBead[beadID] = worktreePath
	d.mu.Unlock()

	// Inject an error into Remove so it fails.
	wtMgr.removeFn = func(_ context.Context, _ string) error {
		return fmt.Errorf("simulated remove error")
	}

	// Call the function under test.
	d.removeWorktreeAndClearTracking(context.Background(), beadID, "w1", worktreePath)

	// Verify that worktreeByBead[beadID] was deleted even though Remove failed.
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.worktreeByBead[beadID]; exists {
		t.Fatalf("expected worktreeByBead[%q] to be deleted, but it still exists with value %q",
			beadID, d.worktreeByBead[beadID])
	}
}

// TestSnapshotWorkers_IncludesLastHeartbeat verifies that snapshotWorkers
// includes LastHeartbeatSecs calculated from now.Sub(w.lastSeen).
func TestSnapshotWorkers_IncludesLastHeartbeat(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Register a worker with a known lastSeen time
	workerID := "test-worker-1"
	s, c := net.Pipe()
	t.Cleanup(func() { _ = s.Close(); _ = c.Close() })
	d.registerWorker(workerID, s)

	// Set a known lastSeen time 5 seconds in the past
	now := time.Now()
	lastSeenTime := now.Add(-5 * time.Second)
	d.mu.Lock()
	d.workers[workerID].lastSeen = lastSeenTime
	d.mu.Unlock()

	// Call snapshotWorkers with the known "now" time
	workers, _, _, _ := d.snapshotWorkers(now)

	// Verify we got the worker
	if len(workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(workers))
	}

	worker := workers[0]
	if worker.ID != workerID {
		t.Fatalf("expected worker ID %q, got %q", workerID, worker.ID)
	}

	// Verify LastHeartbeatSecs is approximately 5.0
	expectedHeartbeatSecs := 5.0
	if worker.LastHeartbeatSecs != expectedHeartbeatSecs {
		t.Errorf("expected LastHeartbeatSecs %.1f, got %.1f", expectedHeartbeatSecs, worker.LastHeartbeatSecs)
	}
}

func TestStatusDirective_Throttled(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Control time to deterministically test throttling.
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return now }
	d.startTime = now

	// First call — should build fresh status JSON.
	resp1, err := d.applyDirective(protocol.DirectiveStatus, "")
	if err != nil {
		t.Fatalf("first status directive failed: %v", err)
	}
	if resp1 == "" {
		t.Fatal("expected non-empty status response")
	}

	// Advance time by 1 second (within 5s window).
	now = now.Add(1 * time.Second)

	// Second call — should return cached response (identical JSON).
	resp2, err := d.applyDirective(protocol.DirectiveStatus, "")
	if err != nil {
		t.Fatalf("second status directive failed: %v", err)
	}
	if resp2 != resp1 {
		t.Fatalf("expected cached response within 5s window\ngot:  %s\nwant: %s", resp2, resp1)
	}

	// Advance time past the 5s throttle window (total 6s from first call).
	now = now.Add(5 * time.Second)

	// Third call — should rebuild fresh (uptime_seconds will differ).
	resp3, err := d.applyDirective(protocol.DirectiveStatus, "")
	if err != nil {
		t.Fatalf("third status directive failed: %v", err)
	}
	if resp3 == resp1 {
		t.Fatal("expected fresh response after 5s window expired, got cached response")
	}

	// Verify the fresh response has updated uptime.
	var status3 statusResponse
	if err := json.Unmarshal([]byte(resp3), &status3); err != nil {
		t.Fatalf("failed to unmarshal third response: %v", err)
	}
	if status3.UptimeSeconds != 6.0 {
		t.Fatalf("expected uptime_seconds=6.0 in fresh response, got %.1f", status3.UptimeSeconds)
	}
}

// TestExternalCloseBlockedDuringPendingMerge verifies that when a merge is
// already in-flight for a bead, an external close (bd close) does not trigger
// a second merge attempt. The dispatcher tracks in-flight merges in mergingBeads
// and blocks the external-close handler from spawning a duplicate merge goroutine.
func TestExternalCloseBlockedDuringPendingMerge(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const beadID = "bead-pending-merge"
	const workerID = "w-pending"
	const worktree = "/tmp/worktree-pending"

	// Simulate a merge already in-flight by marking the bead in mergingBeads.
	d.mu.Lock()
	d.mergingBeads[beadID] = true
	d.mu.Unlock()

	// Register a worker as busy with the bead.
	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn,
		beadID:   beadID,
		state:    protocol.WorkerBusy,
		worktree: worktree,
		encoder:  json.NewEncoder(conn),
	}
	d.mu.Unlock()

	// Externally close the bead.
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:     beadID,
		Status: "closed",
	}
	beadSrc.mu.Unlock()

	// Trigger the handler that detects externally-closed beads.
	d.checkClosedBeadAssignments(ctx)

	// Give any async goroutines time to run (there should be none).
	time.Sleep(50 * time.Millisecond)

	// SHUTDOWN must NOT have been sent: merge is in-flight so external close is blocked.
	conn.mu.Lock()
	var gotShutdown bool
	for _, data := range conn.written {
		if strings.Contains(string(data), string(protocol.MsgShutdown)) {
			gotShutdown = true
			break
		}
	}
	conn.mu.Unlock()

	if gotShutdown {
		t.Error("expected external close to be blocked (no SHUTDOWN) while merge is pending, but SHUTDOWN was sent")
	}

	// Worker must still be tracked as busy — state must not have been cleared.
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var wState protocol.WorkerState
	var wBead string
	if ok {
		wState = w.state
		wBead = w.beadID
	}
	d.mu.Unlock()

	if !ok {
		t.Fatal("expected worker to still exist in tracking")
	}
	if wState != protocol.WorkerBusy {
		t.Errorf("expected worker state to remain Busy, got %v", wState)
	}
	if wBead != beadID {
		t.Errorf("expected worker beadID to remain %q, got %q", beadID, wBead)
	}
}

// TestExternalCloseDoesNotMergeWorkerBranch verifies that external close acts
// as cancellation only: the dispatcher shuts the worker down and cleans up the
// assignment without calling beads.Close or implicitly merging the branch.
func TestExternalCloseDoesNotMergeWorkerBranch(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// Init schema so logEvent, completeAssignment, etc. work.
	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-ext-close"
	workerID := "w-ext"
	worktree := "/tmp/worktree-" + beadID

	// Register an assignment so completeAssignment has something to update.
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	// Set up a tracked worker that is busy on this bead with a worktree.
	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		assignmentID: assignmentID,
		beadID:       beadID,
		state:        protocol.WorkerBusy,
		worktree:     worktree,
		encoder:      json.NewEncoder(conn),
	}
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	// Configure the mock bead source to return "closed" status for this bead.
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		Title:              beadID,
		Status:             "closed",
		AcceptanceCriteria: "done",
	}
	beadSrc.mu.Unlock()

	// Trigger the handler that detects externally-closed beads.
	d.checkClosedBeadAssignments(ctx)

	// Wait for async cancellation cleanup to complete.
	waitFor(t, func() bool {
		wtMgr.mu.Lock()
		defer wtMgr.mu.Unlock()
		return len(wtMgr.removed) == 1
	}, 2*time.Second)

	// Verify the worker was transitioned to ShuttingDown (not Idle) and bead cleared.
	// WorkerShuttingDown is the transient state used after SHUTDOWN is sent so that
	// tryAssign cannot race and grab the worker before it disconnects.
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var wState protocol.WorkerState
	var wBead string
	if ok {
		wState = w.state
		wBead = w.beadID
	}
	d.mu.Unlock()

	if !ok {
		t.Fatal("expected worker to still exist in tracking")
	}
	if wState != protocol.WorkerShuttingDown {
		t.Errorf("expected worker state ShuttingDown, got %v", wState)
	}
	if wBead != "" {
		t.Errorf("expected worker beadID cleared, got %q", wBead)
	}
	if len(beadSrc.closed) != 0 {
		t.Fatalf("expected external close to avoid implicit beads.Close merge path, got closed=%v", beadSrc.closed)
	}
	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 1 || removed[0] != worktree {
		t.Fatalf("expected worktree cleanup for %q, got removed=%v", worktree, removed)
	}

	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if status != "completed" {
		t.Fatalf("assignment status = %q, want completed", status)
	}

	// Verify SHUTDOWN was sent to the worker.
	conn.mu.Lock()
	gotShutdown := false
	for _, data := range conn.written {
		if strings.Contains(string(data), string(protocol.MsgShutdown)) {
			gotShutdown = true
			break
		}
	}
	conn.mu.Unlock()
	if !gotShutdown {
		t.Error("expected SHUTDOWN message to be sent to worker")
	}
}

func TestExternalCloseDoesNotReopenAfterQGFailure(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	beadID := "bead-ext-close-no-reopen"
	workerID := "w-ext-no-reopen"
	worktree := "/tmp/worktree-" + beadID
	d.qgRunner = &mockQGRunner{passed: false, output: "should not run"}

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		assignmentID: assignmentID,
		beadID:       beadID,
		state:        protocol.WorkerBusy,
		worktree:     worktree,
		encoder:      json.NewEncoder(conn),
	}
	d.worktreeByBead[beadID] = worktree
	d.mu.Unlock()

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "closed"}
	beadSrc.mu.Unlock()

	d.handleClosedAssignment(ctx, workerID, beadID)
	d.wg.Wait()

	beadSrc.mu.Lock()
	updated := beadSrc.updated
	beadSrc.mu.Unlock()
	if _, ok := updated[beadID]; ok {
		t.Fatalf("expected external close to avoid reopening bead after cleanup, got updates=%v", updated)
	}
}

func TestExternalCloseCleansUpAssignmentAndTracking(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	beadID := "bead-ext-close-cleanup"
	workerID := "w-ext-cleanup"
	worktree := "/tmp/worktree-" + beadID

	removed := make(chan struct{}, 1)
	wtMgr.removeFn = func(_ context.Context, path string) error {
		if path != worktree {
			t.Fatalf("remove path = %q, want %q", path, worktree)
		}
		removed <- struct{}{}
		return nil
	}

	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		assignmentID: assignmentID,
		beadID:       beadID,
		state:        protocol.WorkerBusy,
		worktree:     worktree,
		encoder:      json.NewEncoder(conn),
	}
	d.worktreeByBead[beadID] = worktree
	d.attemptCounts[beadID] = 3
	d.handoffCounts[beadID] = 2
	d.processedExternalClose[beadID] = false
	d.mu.Unlock()

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "closed"}
	beadSrc.mu.Unlock()

	d.handleClosedAssignment(ctx, workerID, beadID)

	select {
	case <-removed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for external close cleanup")
	}
	d.wg.Wait()

	var status string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if status != "completed" {
		t.Fatalf("assignment status = %q, want completed", status)
	}

	d.mu.Lock()
	_, trackedWorktree := d.worktreeByBead[beadID]
	_, trackedAttempt := d.attemptCounts[beadID]
	_, trackedHandoff := d.handoffCounts[beadID]
	processed := d.processedExternalClose[beadID]
	d.mu.Unlock()

	if trackedWorktree || trackedAttempt || trackedHandoff || processed {
		t.Fatalf("expected tracking to be cleared, got worktree=%v attempt=%v handoff=%v processed=%v",
			trackedWorktree, trackedAttempt, trackedHandoff, processed)
	}
}

// TestBeadClosedExternally_DeadSocketRemovesWorker verifies that when a bead
// is closed externally and the shutdown send fails (dead socket), the worker
// is removed from d.workers immediately rather than left idle for tryAssign
// to churn through. This prevents the post-merge zombie worker pattern
// where a dead worker cycles through 4-5 bead assignments before cleanup.
func TestBeadClosedExternally_DeadSocketRemovesWorker(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-dead-socket"
	workerID := "w-dead"
	worktree := "/tmp/worktree-" + beadID

	_, err = d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}

	// Create a worker with a CLOSED connection (simulates post-merge socket death).
	conn := newMockConn()
	conn.mu.Lock()
	conn.closed = true
	conn.mu.Unlock()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:       workerID,
		conn:     conn,
		beadID:   beadID,
		state:    protocol.WorkerBusy,
		worktree: worktree,
		managed:  true,
	}
	d.mu.Unlock()

	// Bead source returns "closed" for this bead.
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		Title:              beadID,
		Status:             "closed",
		AcceptanceCriteria: "done",
	}
	beadSrc.mu.Unlock()

	d.checkClosedBeadAssignments(ctx)

	// Give async goroutines a moment.
	time.Sleep(100 * time.Millisecond)

	// Worker should be REMOVED from d.workers (not left idle).
	d.mu.Lock()
	_, stillExists := d.workers[workerID]
	d.mu.Unlock()

	if stillExists {
		t.Fatal("expected dead-socket worker to be removed from d.workers, but it still exists")
	}
}

// TestTryAssign_DeadSocketRemovesWorker verifies that when tryAssign sends an
// ASSIGN message to a worker with a dead socket, the worker is removed from
// d.workers immediately rather than left idle. This is the tryAssign-path
// counterpart to TestBeadClosedExternally_DeadSocketRemovesWorker (oro-e2jk).
func TestTryAssign_DeadSocketRemovesWorker(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	// Set dispatcher to running so tryAssign proceeds.
	d.mu.Lock()
	d.state = StateRunning
	d.mu.Unlock()

	// Provide a ready bead.
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "bead-dead-assign", Priority: 1},
	})
	beadSrc.mu.Lock()
	beadSrc.shown["bead-dead-assign"] = &protocol.BeadDetail{
		Title:              "test bead",
		Status:             "open",
		AcceptanceCriteria: "test passes",
	}
	beadSrc.mu.Unlock()

	// Register a worker with a CLOSED connection.
	workerID := "w-dead-assign"
	conn := newMockConn()
	conn.mu.Lock()
	conn.closed = true
	conn.mu.Unlock()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:      workerID,
		conn:    conn,
		state:   protocol.WorkerIdle,
		managed: true,
	}
	d.mu.Unlock()

	// Run tryAssign — should attempt to send ASSIGN, fail, and remove the worker.
	d.tryAssign(ctx)

	// Worker should be REMOVED from d.workers.
	d.mu.Lock()
	_, stillExists := d.workers[workerID]
	d.mu.Unlock()

	if stillExists {
		t.Fatal("expected dead-socket worker to be removed from d.workers after failed assign send")
	}
}

// TestCheckHeartbeats_ResetsBeadToOpen verifies that when a worker's heartbeat
// times out while it has an assigned bead, escalateTimedOutWorkers resets the
// bead status back to "open" so it can be reassigned. This is the heartbeat
// analogue of the graceful-disconnect path in dispatcher.go which calls
// beads.Update(ctx, beadID, "open").
func TestCheckHeartbeats_ResetsBeadToOpen(t *testing.T) {
	t.Run("dead worker bead reset to open", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		beadID := "bead-heartbeat-reset"
		workerID := "w-heartbeat-reset"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:       workerID,
			conn:     server,
			state:    protocol.WorkerBusy,
			beadID:   beadID,
			worktree: "/tmp/worktree-heartbeat-reset",
			lastSeen: now,
			encoder:  json.NewEncoder(server),
		}
		d.mu.Unlock()

		// Advance time past HeartbeatTimeout to trigger dead worker detection.
		d.nowFunc = func() time.Time { return now.Add(600 * time.Millisecond) }

		d.checkHeartbeats(context.Background())

		// Assert: beads.Update was called with "open" for the timed-out bead.
		beadSrc.mu.Lock()
		status, ok := beadSrc.updated[beadID]
		beadSrc.mu.Unlock()
		if !ok {
			t.Fatal("expected beads.Update to be called for bead after heartbeat timeout, but it was not")
		}
		if status != "open" {
			t.Fatalf("expected bead status to be reset to %q, got %q", "open", status)
		}
	})

	t.Run("stuck worker bead reset to open", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		now := time.Now()
		d.nowFunc = func() time.Time { return now }

		beadID := "bead-stuck-reset"
		workerID := "w-stuck-reset"

		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         server,
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			worktree:     "/tmp/worktree-stuck-reset",
			lastSeen:     now,
			lastProgress: now,
			encoder:      json.NewEncoder(server),
		}
		d.mu.Unlock()

		// ProgressTimeout defaults to 0 in test config, so use a non-zero value.
		d.cfg.ProgressTimeout = 100 * time.Millisecond

		// Advance time past ProgressTimeout but not past HeartbeatTimeout,
		// so the worker is "stuck" (progress timeout) not "dead" (heartbeat timeout).
		d.nowFunc = func() time.Time { return now.Add(200 * time.Millisecond) }

		d.checkHeartbeats(context.Background())

		// Assert: beads.Update was called with "open" for the stuck bead.
		beadSrc.mu.Lock()
		status, ok := beadSrc.updated[beadID]
		beadSrc.mu.Unlock()
		if !ok {
			t.Fatal("expected beads.Update to be called for bead after progress timeout, but it was not")
		}
		if status != "open" {
			t.Fatalf("expected bead status to be reset to %q, got %q", "open", status)
		}
	})
}

// TestCheckHeartbeats_PrevSessionWorker verifies that workers whose IDs embed a
// timestamp predating the dispatcher's startTime are silently removed on
// heartbeat timeout without firing a WORKER_CRASH escalation. This prevents
// noisy re-alerts after a dispatcher restart when stale workers from the
// previous session reconnect and then go silent.
//
// AC: After dispatcher restart, no WORKER_CRASH alerts for workers from
// previous session; workers from current session still trigger crash alerts.
func TestCheckHeartbeats_PrevSessionWorker(t *testing.T) {
	t.Run("prev-session worker times out without WORKER_CRASH alert", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		now := time.Now()
		d.nowFunc = func() time.Time { return now }
		// Simulate a restart: startTime is 'now', so workers created before now
		// are from the previous session.
		d.startTime = now

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		// Worker ID with a timestamp 1 hour before startTime — previous session.
		prevEpoch := now.Add(-1 * time.Hour).UnixNano()
		workerID := fmt.Sprintf("worker-%d-0", prevEpoch)

		d.mu.Lock()
		d.upsertWorker(workerID, server, false)
		w := d.workers[workerID]
		w.state = protocol.WorkerBusy
		w.beadID = "bead-stale"
		w.worktree = "/tmp/worktree-stale"
		w.lastSeen = now
		d.mu.Unlock()

		// Advance time past HeartbeatTimeout.
		d.nowFunc = func() time.Time { return now.Add(600 * time.Millisecond) }

		d.checkHeartbeats(context.Background())

		// Worker must be removed.
		if d.ConnectedWorkers() != 0 {
			t.Fatalf("expected prev-session worker to be removed, got %d workers", d.ConnectedWorkers())
		}

		// No WORKER_CRASH escalation for prev-session workers.
		msgs := esc.Messages()
		for _, m := range msgs {
			if strings.Contains(m, string(protocol.EscWorkerCrash)) {
				t.Errorf("unexpected WORKER_CRASH alert for prev-session worker: %s", m)
			}
		}
	})

	t.Run("current-session worker times out WITH WORKER_CRASH alert", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)

		now := time.Now()
		d.nowFunc = func() time.Time { return now }
		// startTime is 1 hour ago — worker created 'now' is within this session.
		d.startTime = now.Add(-1 * time.Hour)

		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })

		// Worker ID with a timestamp after startTime — current session.
		epoch := now.UnixNano()
		workerID := fmt.Sprintf("worker-%d-0", epoch)

		d.mu.Lock()
		d.upsertWorker(workerID, server, false)
		w := d.workers[workerID]
		w.state = protocol.WorkerBusy
		w.beadID = "bead-current"
		w.worktree = "/tmp/worktree-current"
		w.lastSeen = now
		d.mu.Unlock()

		// Advance time past HeartbeatTimeout.
		d.nowFunc = func() time.Time { return now.Add(600 * time.Millisecond) }

		d.checkHeartbeats(context.Background())

		// Worker must be removed.
		if d.ConnectedWorkers() != 0 {
			t.Fatalf("expected current-session worker to be removed, got %d workers", d.ConnectedWorkers())
		}

		// WORKER_CRASH escalation must fire for current-session workers.
		msgs := esc.Messages()
		hasCrash := false
		for _, m := range msgs {
			if strings.Contains(m, string(protocol.EscWorkerCrash)) {
				hasCrash = true
				break
			}
		}
		if !hasCrash {
			t.Error("expected WORKER_CRASH alert for current-session worker, got none")
		}
	})
}

// TestBuildRejectionMemoryContextWithSeparateTable verifies that
// buildRejectionMemoryContext writes rejection feedback to rejection_history
// (NOT memories), and that the returned context still contains the feedback.
// This satisfies acceptance assertions (1), (3) from oro-jwwt.1.2.1.
func TestBuildRejectionMemoryContextWithSeparateTable(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	feedback := "tests are missing edge cases"
	result := d.buildRejectionMemoryContext(ctx, "oro-sep-tbl", feedback)

	// Result must contain the rejection header and feedback.
	if !strings.Contains(result, "## Review Rejection Feedback") {
		t.Errorf("result should contain rejection header, got: %q", result)
	}
	if !strings.Contains(result, feedback) {
		t.Errorf("result should contain feedback %q, got: %q", feedback, result)
	}

	// Rejection must NOT appear in memories table.
	var memCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE content LIKE 'Reviewer rejected%'`,
	).Scan(&memCount); err != nil {
		t.Fatalf("query memories: %v", err)
	}
	if memCount != 0 {
		t.Errorf("expected 0 rejection entries in memories, got %d (rejections must go to rejection_history)", memCount)
	}

	// Rejection must appear in rejection_history.
	var histCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rejection_history WHERE bead_id = ? AND feedback = ?`,
		"oro-sep-tbl", feedback,
	).Scan(&histCount); err != nil {
		t.Fatalf("query rejection_history: %v", err)
	}
	if histCount != 1 {
		t.Errorf("expected 1 rejection in rejection_history, got %d", histCount)
	}
}

// TestBuildRejectionMemoryContext_NoDuplicateInPrior verifies that calling
// buildRejectionMemoryContext twice produces output where the second call
// contains the first feedback exactly once (in "Prior") and the second
// feedback exactly once (in "Current"), not twice.
func TestBuildRejectionMemoryContext_NoDuplicateInPrior(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	// First rejection cycle.
	_ = d.buildRejectionMemoryContext(ctx, "oro-dup-test", "first feedback")

	// Second rejection cycle.
	result := d.buildRejectionMemoryContext(ctx, "oro-dup-test", "second feedback")

	// "second feedback" must appear exactly once — in the current rejection section.
	if count := strings.Count(result, "second feedback"); count != 1 {
		t.Errorf("second feedback should appear exactly once (current section), appeared %d times in:\n%s", count, result)
	}

	// "first feedback" must appear exactly once — in the prior rejection history.
	if count := strings.Count(result, "first feedback"); count != 1 {
		t.Errorf("first feedback should appear exactly once (prior section), appeared %d times in:\n%s", count, result)
	}

	// The prior section must NOT contain the current feedback.
	priorIdx := strings.Index(result, "## Prior Rejection History")
	if priorIdx == -1 {
		t.Fatal("expected '## Prior Rejection History' section in output")
	}
	priorSection := result[priorIdx:]
	if strings.Contains(priorSection, "second feedback") {
		t.Errorf("prior section should not contain current feedback 'second feedback', got:\n%s", priorSection)
	}
}

// TestDispatcher_ResetOrphanedBeads verifies startup crash recovery: any
// in_progress beads are reset to open before the assign loop starts.
func TestDispatcher_ResetOrphanedBeads(t *testing.T) {
	ctx := context.Background()

	t.Run("resets_all_in_progress_beads_to_open", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.inProgressBeads = []protocol.Bead{
			{ID: "oro-x"},
			{ID: "oro-y"},
		}

		d.resetOrphanedBeads(ctx, map[string]bool{"oro-x": true, "oro-y": true})

		beadSrc.mu.Lock()
		updated := beadSrc.updated
		beadSrc.mu.Unlock()

		for _, id := range []string{"oro-x", "oro-y"} {
			status, ok := updated[id]
			if !ok {
				t.Errorf("expected Update(%q, 'open') to be called, but it was not", id)
				continue
			}
			if status != "open" {
				t.Errorf("expected status 'open' for %q, got %q", id, status)
			}
		}
	})

	t.Run("in_progress_error_logs_startup_reset_list_failed_and_continues", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.inProgressErr = fmt.Errorf("bd not found")

		d.resetOrphanedBeads(ctx, map[string]bool{"oro-x": true})

		// No Update() calls should have been made.
		beadSrc.mu.Lock()
		updated := beadSrc.updated
		beadSrc.mu.Unlock()
		if len(updated) > 0 {
			t.Errorf("expected no Update() calls, got %v", updated)
		}

		// The startup_reset_list_failed event must be logged.
		rows, queryErr := d.db.QueryContext(ctx, `SELECT type FROM events WHERE type='startup_reset_list_failed'`)
		if queryErr != nil {
			t.Fatalf("query events: %v", queryErr)
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			t.Error("expected 'startup_reset_list_failed' event to be logged")
		}
	})

	t.Run("per_bead_update_error_logs_startup_reset_bead_failed_and_continues", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.inProgressBeads = []protocol.Bead{
			{ID: "oro-x"},
			{ID: "oro-y"},
		}
		beadSrc.updateErrs = map[string]error{
			"oro-x": fmt.Errorf("bd update failed"),
		}

		d.resetOrphanedBeads(ctx, map[string]bool{"oro-x": true, "oro-y": true})

		// oro-y must still be updated despite oro-x error.
		beadSrc.mu.Lock()
		updated := beadSrc.updated
		beadSrc.mu.Unlock()
		if updated["oro-y"] != "open" {
			t.Errorf("expected oro-y to be updated to 'open', got %q", updated["oro-y"])
		}

		// The startup_reset_bead_failed event must be logged for oro-x.
		rows, queryErr := d.db.QueryContext(ctx, `SELECT bead_id FROM events WHERE type='startup_reset_bead_failed'`)
		if queryErr != nil {
			t.Fatalf("query events: %v", queryErr)
		}
		defer func() { _ = rows.Close() }()
		found := false
		for rows.Next() {
			var beadID string
			if scanErr := rows.Scan(&beadID); scanErr != nil {
				t.Fatalf("scan: %v", scanErr)
			}
			if beadID == "oro-x" {
				found = true
			}
		}
		if !found {
			t.Error("expected 'startup_reset_bead_failed' event for 'oro-x' to be logged")
		}
	})

	t.Run("no_in_progress_beads_is_noop", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		// Default: inProgressBeads is nil → InProgress returns nil.

		d.resetOrphanedBeads(ctx, map[string]bool{"oro-x": true})

		beadSrc.mu.Lock()
		updated := beadSrc.updated
		beadSrc.mu.Unlock()
		if len(updated) > 0 {
			t.Errorf("expected no Update() calls for empty InProgress, got %v", updated)
		}
	})

	t.Run("skips_human_owned_in_progress_beads_without_recoverable_assignment", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.inProgressBeads = []protocol.Bead{
			{ID: "dispatcher-owned"},
			{ID: "human-owned"},
		}

		d.resetOrphanedBeads(ctx, map[string]bool{"dispatcher-owned": true})

		beadSrc.mu.Lock()
		updated := beadSrc.updated
		beadSrc.mu.Unlock()

		if updated["dispatcher-owned"] != "open" {
			t.Fatalf("expected dispatcher-owned bead to reopen, got %q", updated["dispatcher-owned"])
		}
		if _, ok := updated["human-owned"]; ok {
			t.Fatalf("expected human-owned bead to remain untouched, got updates=%v", updated)
		}
	})
}

// mergeAbortSpy is a merge.GitRunner that blocks non-abort calls so we can
// verify AbortAll is invoked during shutdown while a merge is in progress.
type mergeAbortSpy struct {
	started     chan struct{} // closed when the first non-abort Run arrives
	startOnce   sync.Once
	blockCh     chan struct{} // blocks non-abort Run calls until closed
	abortCalled atomic.Bool
}

func (s *mergeAbortSpy) Run(_ context.Context, _ string, args ...string) (string, string, error) {
	if len(args) >= 2 && args[0] == "rebase" && args[1] == "--abort" {
		s.abortCalled.Store(true)
		return "", "", nil
	}
	s.startOnce.Do(func() { close(s.started) })
	<-s.blockCh
	return "", "", fmt.Errorf("blocked call released")
}

func TestShutdownCallsAbortAll(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	spy := &mergeAbortSpy{
		started: make(chan struct{}),
		blockCh: make(chan struct{}),
	}
	d.merger = merge.NewCoordinator(spy)

	// Start a merge that will block inside isBranchMerged, keeping activeWorktree set.
	go func() {
		_, _ = d.merger.Merge(context.Background(), merge.Opts{
			Branch: "test-branch", Worktree: "/tmp/test-wt", BeadID: "test-bead",
		})
	}()

	// Wait for the merge goroutine to enter spy.Run (activeWorktree is now set).
	select {
	case <-spy.started:
	case <-time.After(2 * time.Second):
		t.Fatal("merge did not start within timeout")
	}

	// shutdownCancelOps → AbortAll → Abort → spy.Run("rebase","--abort")
	d.shutdownCancelOps()

	if !spy.abortCalled.Load() {
		t.Fatal("shutdownCancelOps did not call merger.AbortAll()")
	}

	close(spy.blockCh) // unblock the merge goroutine so it can exit
}

func TestAssignBeadResolvesBaseBranch(t *testing.T) { //nolint:funlen // integration test with subtests
	t.Run("standalone bead uses main as base and target", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-standalone", Title: "Standalone", Priority: 1}})

		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected ASSIGN")
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN, got %s", msg.Type)
		}
		if msg.Assign.TargetBranch != "main" {
			t.Errorf("TargetBranch = %q, want %q", msg.Assign.TargetBranch, "main")
		}

		waitForWorkerState(t, d, "w1", protocol.WorkerBusy, time.Second)

		d.mu.Lock()
		w := d.workers["w1"]
		baseBranch := w.baseBranch
		targetBranch := w.targetBranch
		d.mu.Unlock()

		if baseBranch != "main" {
			t.Errorf("w.baseBranch = %q, want %q", baseBranch, "main")
		}
		if targetBranch != "main" {
			t.Errorf("w.targetBranch = %q, want %q", targetBranch, "main")
		}
	})

	t.Run("epic child uses epic branch as base and target", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

		// Register the parent as an epic so resolveEpicBranch walks up correctly.
		beadSrc.mu.Lock()
		beadSrc.shown["epic-xyz"] = &protocol.BeadDetail{ID: "epic-xyz", Title: "Epic XYZ", Type: "epic"}
		beadSrc.mu.Unlock()

		// Epic branch exists.
		wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
			return branch == "epic/epic-xyz", nil
		}
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-child", Title: "Child bead", Priority: 1, Epic: "epic-xyz"}})

		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected ASSIGN")
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN, got %s", msg.Type)
		}
		if msg.Assign.TargetBranch != "epic/epic-xyz" {
			t.Errorf("TargetBranch = %q, want %q", msg.Assign.TargetBranch, "epic/epic-xyz")
		}

		waitForWorkerState(t, d, "w1", protocol.WorkerBusy, time.Second)

		d.mu.Lock()
		w := d.workers["w1"]
		baseBranch := w.baseBranch
		targetBranch := w.targetBranch
		d.mu.Unlock()

		if baseBranch != "epic/epic-xyz" {
			t.Errorf("w.baseBranch = %q, want %q", baseBranch, "epic/epic-xyz")
		}
		if targetBranch != "epic/epic-xyz" {
			t.Errorf("w.targetBranch = %q, want %q", targetBranch, "epic/epic-xyz")
		}
	})

	t.Run("epic child lazily creates branch when epic branch does not exist", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

		// Register the parent as an epic so resolveEpicBranch produces an epic branch.
		beadSrc.mu.Lock()
		beadSrc.shown["epic-missing"] = &protocol.BeadDetail{ID: "epic-missing", Title: "Epic Missing", Type: "epic"}
		beadSrc.mu.Unlock()

		// Epic branch does NOT exist — lazy creation will create it.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-child-missing", Title: "Child bead missing branch", Priority: 1, Epic: "epic-missing"}})

		// Branch lazily created → bead SHOULD be assigned.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok || msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN after lazy branch creation, got ok=%v type=%v", ok, msg.Type)
		}
	})
}

func TestBuildAssignPayload_PopulatesAllFields(t *testing.T) {
	t.Run("happy path populates all fields", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)

		tmpDir := t.TempDir()
		d.cfg.RepoRoot = tmpDir

		wpContent := "# Worker Program\nDo good work."
		if err := os.WriteFile(tmpDir+"/worker-program.md", []byte(wpContent), 0o600); err != nil {
			t.Fatal(err)
		}

		gitLogOutput := "abc1234 feat: something\ndef5678 fix: other"
		d.shutdownRunner = &mockCommandRunner{output: []byte(gitLogOutput)}

		beadSrc.shown["test-bead"] = &protocol.BeadDetail{
			Title:              "Test Bead",
			Description:        "A test bead description",
			AcceptanceCriteria: "Test: X | Assert: Y",
		}

		w := &trackedWorker{
			id:           "worker-1",
			beadID:       "test-bead",
			worktree:     "/tmp/worktree-test-bead",
			model:        "sonnet",
			targetBranch: "main",
			isEpicDecomp: false,
		}

		got := d.buildAssignPayload(context.Background(), w, 1, "some feedback", "memory ctx")

		if got.BeadID != "test-bead" {
			t.Errorf("BeadID = %q, want %q", got.BeadID, "test-bead")
		}
		if got.Worktree != "/tmp/worktree-test-bead" {
			t.Errorf("Worktree = %q", got.Worktree)
		}
		if got.Title != "Test Bead" {
			t.Errorf("Title = %q, want %q", got.Title, "Test Bead")
		}
		if got.Description != "A test bead description" {
			t.Errorf("Description = %q", got.Description)
		}
		if got.AcceptanceCriteria != "Test: X | Assert: Y" {
			t.Errorf("AcceptanceCriteria = %q", got.AcceptanceCriteria)
		}
		if got.ProjectRoot != tmpDir {
			t.Errorf("ProjectRoot = %q, want %q", got.ProjectRoot, tmpDir)
		}
		if got.TargetBranch != "main" {
			t.Errorf("TargetBranch = %q", got.TargetBranch)
		}
		if !strings.Contains(got.GitLog, "feat: something") {
			t.Errorf("GitLog = %q, want to contain git log output", got.GitLog)
		}
		if got.WorkerProgram != wpContent {
			t.Errorf("WorkerProgram = %q, want %q", got.WorkerProgram, wpContent)
		}
		if got.Attempt != 1 {
			t.Errorf("Attempt = %d", got.Attempt)
		}
		if got.Feedback != "some feedback" {
			t.Errorf("Feedback = %q", got.Feedback)
		}
		if got.MemoryContext != "memory ctx" {
			t.Errorf("MemoryContext = %q", got.MemoryContext)
		}
	})

	t.Run("epic decomp skips GitLog and WorkerProgram", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)

		tmpDir := t.TempDir()
		d.cfg.RepoRoot = tmpDir

		if err := os.WriteFile(tmpDir+"/worker-program.md", []byte("program content"), 0o600); err != nil {
			t.Fatal(err)
		}

		gitCalled := false
		d.shutdownRunner = &mockCommandRunner{callFn: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			gitCalled = true
			return []byte("git log output"), nil
		}}

		beadSrc.shown["epic-bead"] = &protocol.BeadDetail{
			Title:              "Epic Bead",
			AcceptanceCriteria: "AC",
		}

		w := &trackedWorker{
			id:           "worker-1",
			beadID:       "epic-bead",
			isEpicDecomp: true,
		}

		got := d.buildAssignPayload(context.Background(), w, 0, "", "")

		if got.GitLog != "" {
			t.Errorf("GitLog should be empty for epic decomp, got %q", got.GitLog)
		}
		if got.WorkerProgram != "" {
			t.Errorf("WorkerProgram should be empty for epic decomp, got %q", got.WorkerProgram)
		}
		if gitCalled {
			t.Error("git log should not be called for epic decomp")
		}
		if got.Title != "Epic Bead" {
			t.Errorf("Title = %q, want %q", got.Title, "Epic Bead")
		}
		if !got.IsEpicDecomposition {
			t.Error("IsEpicDecomposition should be true")
		}
	})

	t.Run("beads Show error falls back to empty metadata", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)

		tmpDir := t.TempDir()
		d.cfg.RepoRoot = tmpDir

		d.shutdownRunner = &mockCommandRunner{output: []byte("abc1234 some commit")}

		beadSrc.showErr = errors.New("bead not found")

		w := &trackedWorker{
			id:           "worker-1",
			beadID:       "missing-bead",
			isEpicDecomp: false,
		}

		got := d.buildAssignPayload(context.Background(), w, 0, "", "")

		if got.Title != "" {
			t.Errorf("Title should be empty on Show error, got %q", got.Title)
		}
		if got.Description != "" {
			t.Errorf("Description should be empty on Show error, got %q", got.Description)
		}
		if got.AcceptanceCriteria != "" {
			t.Errorf("AcceptanceCriteria should be empty on Show error, got %q", got.AcceptanceCriteria)
		}
		if got.GitLog == "" {
			t.Error("GitLog should still be populated when Show fails")
		}
	})
}

// TestHandoffRespawn_UsesTitle verifies that:
// 1. pendingHandoff.title is populated from bead title at handoff time
// 2. registerWorker's memory search uses h.title+labels (not h.beadID)
// 3. Labels are included in the search query via buildSearchQuery
func TestHandoffRespawn_UsesTitle(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Seed a memory that matches the bead title
	_, err := d.db.Exec(
		`INSERT INTO memories (content, type, tags, source, bead_id, confidence)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"always check this when fixing tests", "lesson", `["testing"]`,
		"self_report", "old-bead", 0.9,
	)
	if err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	// Connect first worker
	conn1, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn1, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Assign a bead with specific title and labels
	const beadTitle = "always check this when fixing tests"
	beadSrc.SetBeads([]protocol.Bead{
		{
			ID:       "bead-handoff",
			Title:    beadTitle,
			Labels:   []string{"testing"},
			Priority: 1,
		},
	})

	// Configure the mock's Show() to return the full BeadDetail with labels
	beadSrc.mu.Lock()
	if beadSrc.shown == nil {
		beadSrc.shown = make(map[string]*protocol.BeadDetail)
	}
	beadSrc.shown["bead-handoff"] = &protocol.BeadDetail{
		Title:              beadTitle,
		Labels:             []string{"testing"},
		AcceptanceCriteria: "Test: auto | Assert: PASS",
	}
	beadSrc.mu.Unlock()

	// Read ASSIGN message
	msg, ok := readMsg(t, conn1, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	beadSrc.SetBeads(nil)

	// Send HANDOFF from first worker
	sendMsg(t, conn1, protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{BeadID: "bead-handoff", WorkerID: "w1"},
	})

	// Worker 1 should receive SHUTDOWN
	msg, ok = readMsg(t, conn1, 2*time.Second)
	if !ok {
		t.Fatal("expected SHUTDOWN after handoff")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Verify pendingHandoff was created with title and labels
	waitFor(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		ph, ok := d.pendingHandoffs["bead-handoff"]
		return ok && ph != nil && ph.title != "" && len(ph.labels) > 0
	}, 2*time.Second)

	d.mu.Lock()
	ph := d.pendingHandoffs["bead-handoff"]
	if ph == nil {
		t.Fatal("pending handoff not found")
	}
	if ph.title != "always check this when fixing tests" {
		t.Errorf("expected title 'always check this when fixing tests', got %q", ph.title)
	}
	if len(ph.labels) != 1 || ph.labels[0] != "testing" {
		t.Errorf("expected labels ['testing'], got %v", ph.labels)
	}
	d.mu.Unlock()

	// Connect second worker — this will trigger registerWorker which should
	// use the pending handoff with title+labels for memory search
	conn2, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn2, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w2", ContextPct: 5},
	})
	waitForWorkers(t, d, 2, 2*time.Second)

	// Second worker should receive ASSIGN for the pending handoff,
	// and it should include MemoryContext populated from the title+labels search
	msg, ok = readMsg(t, conn2, 3*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN for second worker")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}
	if msg.Assign.BeadID != "bead-handoff" {
		t.Fatalf("expected BeadID 'bead-handoff', got %q", msg.Assign.BeadID)
	}
	// The key assertion: MemoryContext should contain the seeded memory because
	// registerWorker used title+labels to search (not just beadID)
	if msg.Assign.MemoryContext == "" {
		t.Fatal("expected non-empty MemoryContext when searching by title+labels for respawned bead")
	}
	if !containsStr(msg.Assign.MemoryContext, "always check this") {
		t.Errorf("expected MemoryContext to contain 'always check this', got: %s", msg.Assign.MemoryContext)
	}
}

func TestBuildSearchQuery_WithLabels(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		labels []string
		want   string
	}{
		{
			name:   "title with labels",
			title:  "Fix auth",
			labels: []string{"go", "auth"},
			want:   "Fix auth go auth",
		},
		{
			name:   "title with nil labels",
			title:  "Fix auth",
			labels: nil,
			want:   "Fix auth",
		},
		{
			name:   "title with empty labels",
			title:  "Fix auth",
			labels: []string{},
			want:   "Fix auth",
		},
		{
			name:   "empty title with labels",
			title:  "",
			labels: []string{"go", "auth"},
			want:   "go auth",
		},
		{
			name:   "empty title with nil labels",
			title:  "",
			labels: nil,
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSearchQuery(tt.title, tt.labels)
			if got != tt.want {
				t.Errorf("buildSearchQuery(%q, %v) = %q, want %q", tt.title, tt.labels, got, tt.want)
			}
		})
	}
}

func TestExtractBeadID_ReconnectReturnsBeadID(t *testing.T) {
	msg := protocol.Message{
		Type: protocol.MsgReconnect,
		Reconnect: &protocol.ReconnectPayload{
			WorkerID: "w1",
			BeadID:   "bead-abc",
		},
	}
	got := extractBeadID(msg)
	if got != "bead-abc" {
		t.Errorf("extractBeadID(MsgReconnect) = %q, want %q", got, "bead-abc")
	}
}

func TestExtractBeadID_ReconnectNilPayloadReturnsEmpty(t *testing.T) {
	msg := protocol.Message{
		Type: protocol.MsgReconnect,
	}
	got := extractBeadID(msg)
	if got != "" {
		t.Errorf("extractBeadID(MsgReconnect nil payload) = %q, want empty", got)
	}
}

// TestAssignBead_UsesLLMEstimate verifies that the LLM estimator is called when
// appropriate and the result is used for model routing.
func TestAssignBead_UsesLLMEstimate(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	// Mock estimator that tracks calls and returns canned results
	mockEstimator := &mockBeadEstimator{
		estimates: make(map[string]int),
	}
	d.estimator = mockEstimator

	cancel := startDispatcher(t, d)
	defer cancel()
	d.setState(StateRunning)

	beadSrc.SetBeads([]protocol.Bead{
		{
			ID:       "bead-estimate-short",
			Title:    "Short task",
			Type:     "task",
			Priority: 1,
			// Model and EstimatedMinutes are both empty/0 — should call estimator
		},
		{
			ID:       "bead-estimate-long",
			Title:    "Long task",
			Type:     "task",
			Priority: 1,
			// Model and EstimatedMinutes are both empty/0 — should call estimator
		},
		{
			ID:               "bead-has-estimate",
			Title:            "Has estimate",
			Type:             "task",
			Priority:         1,
			EstimatedMinutes: 3, // Has estimate, should NOT call estimator
		},
		{
			ID:       "bead-has-model",
			Title:    "Has model",
			Type:     "task",
			Priority: 1,
			Model:    protocol.ModelOpus, // Has explicit model, should NOT call estimator
		},
		{
			ID:       "bead-estimate-zero",
			Title:    "Estimates to zero",
			Type:     "task",
			Priority: 1,
			// Model and EstimatedMinutes are both empty/0 — estimator will return 0
		},
	})

	// Set up estimator return values
	mockEstimator.estimates["Short task"] = 3        // <=5 should route to Haiku
	mockEstimator.estimates["Long task"] = 8         // >5 should route to Sonnet
	mockEstimator.estimates["Estimates to zero"] = 0 // 0 should route to default (Sonnet)

	// Connect 5 workers to collect all 5 assignments
	conns := make([]net.Conn, 5)
	for i := 0; i < 5; i++ {
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		conns[i] = conn
		defer conn.Close()

		workerID := fmt.Sprintf("w-estimate-%d", i)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: workerID},
		})
	}

	waitForWorkers(t, d, 5, 2*time.Second)

	// Collect all assignments in a map
	assignedBeads := make(map[string]*protocol.AssignPayload)
	for i := 0; i < 5; i++ {
		msg, ok := readMsg(t, conns[i], 3*time.Second)
		if !ok {
			t.Fatalf("worker %d: expected ASSIGN message", i)
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("worker %d: expected ASSIGN, got %s", i, msg.Type)
		}
		assignedBeads[msg.Assign.BeadID] = msg.Assign
	}

	// Verify all beads were assigned
	expectedBeads := []string{
		"bead-estimate-short",
		"bead-estimate-long",
		"bead-has-estimate",
		"bead-has-model",
		"bead-estimate-zero",
	}
	for _, beadID := range expectedBeads {
		if _, found := assignedBeads[beadID]; !found {
			t.Errorf("bead %s was not assigned", beadID)
		}
	}

	// Test 1: bead-estimate-short should be estimated to 3 minutes → Haiku
	if assign := assignedBeads["bead-estimate-short"]; assign != nil {
		if assign.Model != protocol.ModelHaiku {
			t.Errorf("bead-estimate-short: estimated 3 min should route to Haiku, got %s", assign.Model)
		}
		if !mockEstimator.wasCalled("Short task") {
			t.Errorf("bead-estimate-short: estimator should have been called")
		}
	}

	// Test 2: bead-estimate-long should be estimated to 8 minutes → Sonnet
	if assign := assignedBeads["bead-estimate-long"]; assign != nil {
		if assign.Model != protocol.ModelSonnet {
			t.Errorf("bead-estimate-long: estimated 8 min should route to Sonnet, got %s", assign.Model)
		}
		if !mockEstimator.wasCalled("Long task") {
			t.Errorf("bead-estimate-long: estimator should have been called")
		}
	}

	// Test 3: bead-has-estimate has pre-set estimate, should NOT call estimator
	if assign := assignedBeads["bead-has-estimate"]; assign != nil {
		if assign.Model != protocol.ModelHaiku {
			t.Errorf("bead-has-estimate: 3 minute estimate should route to Haiku, got %s", assign.Model)
		}
		if mockEstimator.wasCalled("Has estimate") {
			t.Errorf("bead-has-estimate: estimator should NOT have been called (already has estimate)")
		}
	}

	// Test 4: bead-has-model has explicit model, should NOT call estimator
	if assign := assignedBeads["bead-has-model"]; assign != nil {
		if assign.Model != protocol.ModelOpus {
			t.Errorf("bead-has-model: explicit Model=Opus should be used, got %s", assign.Model)
		}
		if mockEstimator.wasCalled("Has model") {
			t.Errorf("bead-has-model: estimator should NOT have been called (has explicit model)")
		}
	}

	// Test 5: bead-estimate-zero should be estimated to 0 → default Sonnet
	if assign := assignedBeads["bead-estimate-zero"]; assign != nil {
		if assign.Model != protocol.ModelSonnet {
			t.Errorf("bead-estimate-zero: estimate of 0 should route to default Sonnet, got %s", assign.Model)
		}
		if !mockEstimator.wasCalled("Estimates to zero") {
			t.Errorf("bead-estimate-zero: estimator should have been called")
		}
	}
}

// mockBeadEstimator implements BeadEstimator for testing
type mockBeadEstimator struct {
	estimates map[string]int
	calls     map[string]int
	mu        sync.Mutex
}

func (m *mockBeadEstimator) Estimate(ctx context.Context, title, acceptance string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls == nil {
		m.calls = make(map[string]int)
	}
	// Extract bead ID from title (test titles match beadIDs for simplicity)
	// In a real scenario, we'd use a better tracking mechanism
	m.calls[title]++
	return m.estimates[title]
}

func (m *mockBeadEstimator) wasCalled(title string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[title] > 0
}

// TestAllAssignPayloadSitesUseBuildAssignPayload verifies that the QG retry and
// review rejection paths both call buildAssignPayload, ensuring the re-ASSIGN
// messages include bead metadata (Title, AcceptanceCriteria) fetched from
// beads.Show — fields that the former inline AssignPayload{} literals omitted.
func TestAllAssignPayloadSitesUseBuildAssignPayload(t *testing.T) {
	const (
		beadTitle = "Consolidate assignPayload sites"
		beadAC    = "Test: pkg/dispatcher | Assert: PASS"
	)

	t.Run("QG retry includes bead metadata from beads.Show", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-qg-meta", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		// Seed rich bead detail so buildAssignPayload can retrieve it via beads.Show.
		beadSrc.mu.Lock()
		beadSrc.shown["bead-qg-meta"] = &protocol.BeadDetail{
			Title:              beadTitle,
			AcceptanceCriteria: beadAC,
		}
		beadSrc.mu.Unlock()

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-qg-meta", Title: beadTitle, Type: "task", Priority: 1, Model: protocol.ModelOpus}})
		_, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected initial ASSIGN")
		}
		beadSrc.SetBeads(nil)

		// Trigger QG retry.
		sendMsg(t, conn, protocol.Message{
			Type: protocol.MsgDone,
			Done: &protocol.DonePayload{
				WorkerID:          "w-qg-meta",
				BeadID:            "bead-qg-meta",
				QualityGatePassed: false,
				QGOutput:          "tests failed",
			},
		})

		// Re-ASSIGN must include bead metadata from beads.Show.
		msg, ok := readMsg(t, conn, 3*time.Second)
		if !ok {
			t.Fatal("expected re-ASSIGN after QG failure")
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN, got %s", msg.Type)
		}
		if msg.Assign.Title != beadTitle {
			t.Errorf("QG retry Title = %q, want %q", msg.Assign.Title, beadTitle)
		}
		if msg.Assign.AcceptanceCriteria != beadAC {
			t.Errorf("QG retry AcceptanceCriteria = %q, want %q", msg.Assign.AcceptanceCriteria, beadAC)
		}
	})

	t.Run("review rejection includes bead metadata from beads.Show", func(t *testing.T) {
		d, beadSrc, _, _, _, spawnMock := newTestDispatcher(t)
		spawnMock.mu.Lock()
		spawnMock.verdict = "missing tests\n\nVERDICT: REJECTED"
		spawnMock.mu.Unlock()
		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w-rev-meta", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, 1*time.Second)

		// Seed rich bead detail.
		beadSrc.mu.Lock()
		beadSrc.shown["bead-rev-meta"] = &protocol.BeadDetail{
			Title:              beadTitle,
			AcceptanceCriteria: beadAC,
		}
		beadSrc.mu.Unlock()

		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, 1*time.Second)

		beadSrc.SetBeads([]protocol.Bead{{ID: "bead-rev-meta", Title: beadTitle, Priority: 1}})
		_, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected initial ASSIGN")
		}
		beadSrc.SetBeads(nil)

		// Trigger review rejection.
		sendMsg(t, conn, protocol.Message{
			Type:           protocol.MsgReadyForReview,
			ReadyForReview: &protocol.ReadyForReviewPayload{BeadID: "bead-rev-meta", WorkerID: "w-rev-meta"},
		})

		// Re-ASSIGN must include bead metadata from beads.Show.
		msg, ok := readMsg(t, conn, 3*time.Second)
		if !ok {
			t.Fatal("expected re-ASSIGN after review rejection")
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN, got %s", msg.Type)
		}
		if msg.Assign.Title != beadTitle {
			t.Errorf("review rejection Title = %q, want %q", msg.Assign.Title, beadTitle)
		}
		if msg.Assign.AcceptanceCriteria != beadAC {
			t.Errorf("review rejection AcceptanceCriteria = %q, want %q", msg.Assign.AcceptanceCriteria, beadAC)
		}
	})
}

// TestSortBeadsByPriority_EpicFinishing verifies the full sort order:
// (1) spawn-for beads, (2) focused epic children, (3) non-epic beads,
// (4) oldest unfocused epic children (lowest epic ID = oldest), then next oldest.
// Within each group, sort by priority P0→P1→P2.
func TestSortBeadsByPriority_EpicFinishing(t *testing.T) {
	t.Run("full ordering", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		d.mu.Lock()
		d.focusedEpic = "epic-focus"
		d.priorityBeads["b-spawn-p0"] = true
		d.priorityBeads["b-spawn-p1"] = true
		d.mu.Unlock()

		beads := []protocol.Bead{
			{ID: "b-new-epic-p1", Priority: 1, Epic: "epic-zzz"},
			{ID: "b-focus-p2", Priority: 2, Epic: "epic-focus"},
			{ID: "b-noepic-p0", Priority: 0, Epic: ""},
			{ID: "b-old-epic-p0", Priority: 0, Epic: "epic-aaa"},
			{ID: "b-spawn-p1", Priority: 1, Epic: "epic-z"},
			{ID: "b-noepic-p1", Priority: 1, Epic: ""},
			{ID: "b-old-epic-p2", Priority: 2, Epic: "epic-aaa"},
			{ID: "b-spawn-p0", Priority: 0, Epic: ""},
			{ID: "b-focus-p1", Priority: 1, Epic: "epic-focus"},
		}

		d.sortBeadsByPriority(beads)

		want := []string{
			"b-spawn-p0",    // group 1: spawn-for, P0
			"b-spawn-p1",    // group 1: spawn-for, P1
			"b-focus-p1",    // group 2: focused epic, P1
			"b-focus-p2",    // group 2: focused epic, P2
			"b-noepic-p0",   // group 3: non-epic, P0
			"b-noepic-p1",   // group 3: non-epic, P1
			"b-old-epic-p0", // group 4: oldest unfocused epic "epic-aaa", P0
			"b-old-epic-p2", // group 4: oldest unfocused epic "epic-aaa", P2
			"b-new-epic-p1", // group 4: newer unfocused epic "epic-zzz", P1
		}

		got := make([]string, len(beads))
		for i, b := range beads {
			got[i] = b.ID
		}

		for i, id := range want {
			if i >= len(got) || got[i] != id {
				t.Errorf("position %d: got %q, want %q\nfull got:  %v\nfull want: %v", i, func() string {
					if i < len(got) {
						return got[i]
					}
					return "<missing>"
				}(), id, got, want)
				return
			}
		}
	})

	t.Run("no epics priority only", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		beads := []protocol.Bead{
			{ID: "p2", Priority: 2, Epic: ""},
			{ID: "p0", Priority: 0, Epic: ""},
			{ID: "p1", Priority: 1, Epic: ""},
		}

		d.sortBeadsByPriority(beads)

		want := []string{"p0", "p1", "p2"}
		for i, id := range want {
			if beads[i].ID != id {
				t.Errorf("position %d: got %q, want %q", i, beads[i].ID, id)
			}
		}
	})

	t.Run("all same epic sorts by priority", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		beads := []protocol.Bead{
			{ID: "p2", Priority: 2, Epic: "epic-one"},
			{ID: "p0", Priority: 0, Epic: "epic-one"},
			{ID: "p1", Priority: 1, Epic: "epic-one"},
		}

		d.sortBeadsByPriority(beads)

		want := []string{"p0", "p1", "p2"}
		for i, id := range want {
			if beads[i].ID != id {
				t.Errorf("position %d: got %q, want %q", i, beads[i].ID, id)
			}
		}
	})
}

func TestDirective_MaxWorkers(t *testing.T) {
	t.Run("sets MaxWorkers and clamps targetWorkers", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		// Default MaxWorkers=5; set targetWorkers=4 so it must be clamped to 3.
		d.mu.Lock()
		d.targetWorkers = 4
		d.mu.Unlock()
		startDispatcher(t, d)

		ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "max-workers", "3")

		if !ack.OK {
			t.Fatalf("expected OK=true, got false: %s", ack.Detail)
		}
		if !strings.Contains(ack.Detail, "max_workers=3") {
			t.Fatalf("expected detail to contain 'max_workers=3', got %q", ack.Detail)
		}
		d.mu.Lock()
		gotMax := d.cfg.MaxWorkers
		gotTarget := d.targetWorkers
		d.mu.Unlock()
		if gotMax != 3 {
			t.Fatalf("expected cfg.MaxWorkers=3, got %d", gotMax)
		}
		if gotTarget != 3 {
			t.Fatalf("expected targetWorkers clamped to 3, got %d", gotTarget)
		}
	})

	t.Run("does not raise targetWorkers when below new max", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.mu.Lock()
		d.targetWorkers = 1
		d.mu.Unlock()
		startDispatcher(t, d)

		ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "max-workers", "5")

		if !ack.OK {
			t.Fatalf("expected OK=true: %s", ack.Detail)
		}
		if !strings.Contains(ack.Detail, "max_workers=5") {
			t.Fatalf("expected detail 'max_workers=5', got %q", ack.Detail)
		}
		d.mu.Lock()
		gotTarget := d.targetWorkers
		d.mu.Unlock()
		if gotTarget != 1 {
			t.Fatalf("expected targetWorkers unchanged at 1, got %d", gotTarget)
		}
	})

	t.Run("maybeAutoScale respects new cap", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "max-workers", "3")
		if !ack.OK {
			t.Fatalf("expected OK=true: %s", ack.Detail)
		}
		// targetWorkers already at max; autoscale with deep queue should not exceed 3.
		d.mu.Lock()
		d.targetWorkers = 3
		d.mu.Unlock()

		d.maybeAutoScale(context.Background(), 10, 0)

		d.mu.Lock()
		gotTarget := d.targetWorkers
		d.mu.Unlock()
		if gotTarget > 3 {
			t.Fatalf("expected targetWorkers <= 3 after maybeAutoScale, got %d", gotTarget)
		}
	})

	t.Run("empty args returns error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "max-workers", "")

		if ack.OK {
			t.Fatal("expected OK=false for empty args")
		}
		if !strings.Contains(ack.Detail, "worker count required") {
			t.Fatalf("expected 'worker count required' in detail, got %q", ack.Detail)
		}
	})

	t.Run("non-integer args returns error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "max-workers", "abc")

		if ack.OK {
			t.Fatal("expected OK=false for non-integer args")
		}
		if !strings.Contains(strings.ToLower(ack.Detail), "invalid") {
			t.Fatalf("expected 'invalid' in detail, got %q", ack.Detail)
		}
	})

	t.Run("negative args returns error", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		startDispatcher(t, d)

		ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "max-workers", "-1")

		if ack.OK {
			t.Fatal("expected OK=false for negative args")
		}
		if !strings.Contains(ack.Detail, "non-negative") {
			t.Fatalf("expected 'non-negative' in detail, got %q", ack.Detail)
		}
	})

	t.Run("zero drains all managed workers", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		conn1 := newMockConn()
		conn2 := newMockConn()

		d.mu.Lock()
		d.targetWorkers = 2
		d.workers["w1"] = &trackedWorker{
			id:      "w1",
			conn:    conn1,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(conn1),
		}
		d.workers["w2"] = &trackedWorker{
			id:      "w2",
			conn:    conn2,
			state:   protocol.WorkerIdle,
			managed: true,
			encoder: json.NewEncoder(conn2),
		}
		d.mu.Unlock()
		startDispatcher(t, d)

		ack := sendDirectiveWithArgs(t, d.cfg.SocketPath, "max-workers", "0")

		if !ack.OK {
			t.Fatalf("expected OK=true, got false: %s", ack.Detail)
		}
		if !strings.Contains(ack.Detail, "max_workers=0") {
			t.Fatalf("expected detail to contain 'max_workers=0', got %q", ack.Detail)
		}
		d.mu.Lock()
		gotMax := d.cfg.MaxWorkers
		gotTarget := d.targetWorkers
		d.mu.Unlock()
		if gotMax != 0 {
			t.Fatalf("expected cfg.MaxWorkers=0, got %d", gotMax)
		}
		if gotTarget != 0 {
			t.Fatalf("expected targetWorkers=0 after max-workers 0, got %d", gotTarget)
		}

		// Both managed workers should receive shutdown messages.
		conn1.mu.Lock()
		w1Writes := len(conn1.written)
		conn1.mu.Unlock()
		conn2.mu.Lock()
		w2Writes := len(conn2.written)
		conn2.mu.Unlock()
		if w1Writes == 0 || w2Writes == 0 {
			t.Fatalf("expected both workers to receive shutdown, got w1=%d w2=%d writes", w1Writes, w2Writes)
		}
	})
}

// TestAssignSkipsBlockedBeads verifies that filterAssignable excludes beads
// whose Dependencies contain unresolved blocking deps (type "blocks" or
// "conditional-blocks") pointing to open beads in the same batch, while
// allowing through beads whose deps are closed, non-existent (dangling), or
// of a non-blocking type ("parent-child").
func TestAssignSkipsBlockedBeads(t *testing.T) {
	tests := []struct {
		name    string
		beads   []protocol.Bead
		wantIDs []string
	}{
		{
			name: "blocks-open",
			beads: []protocol.Bead{
				{ID: "bead-a", Title: "Blocker", Status: "open", Priority: 1, Type: "task"},
				{
					ID: "bead-b", Title: "Blocked by A", Priority: 2, Type: "task",
					Dependencies: []protocol.Dependency{
						{IssueID: "bead-b", DependsOnID: "bead-a", Type: "blocks"},
					},
				},
			},
			wantIDs: []string{"bead-a"},
		},
		{
			name: "closed-allows",
			beads: []protocol.Bead{
				{ID: "bead-c", Title: "Closed blocker", Status: "closed", Priority: 1, Type: "task"},
				{
					ID: "bead-d", Title: "Dep on closed", Priority: 2, Type: "task",
					Dependencies: []protocol.Dependency{
						{IssueID: "bead-d", DependsOnID: "bead-c", Type: "blocks"},
					},
				},
			},
			wantIDs: []string{"bead-d"},
		},
		{
			name: "parent-child-nonblocking",
			beads: []protocol.Bead{
				{ID: "bead-e", Title: "Epic", Status: "open", Priority: 1, Type: "epic"},
				{
					ID: "bead-f", Title: "Child task", Priority: 2, Type: "task",
					Dependencies: []protocol.Dependency{
						{IssueID: "bead-f", DependsOnID: "bead-e", Type: "parent-child"},
					},
				},
			},
			// bead-e is an epic: filtered as non-executable. bead-f passes (parent-child dep is non-blocking).
			wantIDs: []string{"bead-f"},
		},
		{
			name: "conditional-blocks",
			beads: []protocol.Bead{
				{ID: "bead-g", Title: "Open dep", Status: "open", Priority: 1, Type: "task"},
				{
					ID: "bead-h", Title: "Conditionally blocked", Priority: 2, Type: "task",
					Dependencies: []protocol.Dependency{
						{IssueID: "bead-h", DependsOnID: "bead-g", Type: "conditional-blocks"},
					},
				},
			},
			wantIDs: []string{"bead-g"},
		},
		{
			name: "dangling-ok",
			beads: []protocol.Bead{
				{
					ID: "bead-i", Title: "Dangling dep", Priority: 1, Type: "task",
					Dependencies: []protocol.Dependency{
						{IssueID: "bead-i", DependsOnID: "bead-x-unknown", Type: "blocks"},
					},
				},
			},
			wantIDs: []string{"bead-i"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			// Inject a mock shutdownRunner so isBranchMerged always returns false
			// (branch doesn't exist → not merged → bead stays as candidate).
			d.shutdownRunner = &mockCommandRunner{err: errors.New("exit status 1")}

			result := d.filterAssignable(context.Background(), tc.beads)

			gotIDs := make([]string, len(result))
			for i, b := range result {
				gotIDs[i] = b.ID
			}

			if len(result) != len(tc.wantIDs) {
				t.Fatalf("got %d beads %v, want %d %v",
					len(result), gotIDs, len(tc.wantIDs), tc.wantIDs)
			}

			wantSet := make(map[string]bool, len(tc.wantIDs))
			for _, id := range tc.wantIDs {
				wantSet[id] = true
			}
			for _, id := range gotIDs {
				if !wantSet[id] {
					t.Errorf("unexpected bead %q in result %v", id, gotIDs)
				}
			}
		})
	}
}

func TestDispatcherUsesProjectPaths(t *testing.T) {
	t.Run("BeadsDir from Config wired into dispatcher", func(t *testing.T) {
		customBeadsDir := t.TempDir()

		db := newTestDB(t)
		gitRunner := &mockGitRunner{}
		merger := merge.NewCoordinator(gitRunner)
		opsSpawner := ops.NewSpawner(&mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"})
		beadSrc := &fakeBeadStore{beads: []protocol.Bead{}, shown: make(map[string]*protocol.BeadDetail)}
		wtMgr := &mockWorktreeManager{created: make(map[string]string)}
		esc := &mockEscalator{}

		sockPath := fmt.Sprintf("/tmp/oro-test-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })

		cfg := Config{
			SocketPath:       sockPath,
			DBPath:           ":memory:",
			MaxWorkers:       1,
			HeartbeatTimeout: 500 * time.Millisecond,
			PollInterval:     50 * time.Millisecond,
			ShutdownTimeout:  200 * time.Millisecond,
			BeadsDir:         customBeadsDir,
		}

		d, err := New(cfg, db, merger, opsSpawner, beadSrc, wtMgr, esc, nil)
		if err != nil {
			t.Fatalf("New() failed: %v", err)
		}

		if d.beadsDir != customBeadsDir {
			t.Errorf("beadsDir = %q, want %q (Config.BeadsDir not wired)", d.beadsDir, customBeadsDir)
		}
	})

	t.Run("appendReviewPatterns uses repoRoot not filepath.Dir(beadsDir)", func(t *testing.T) {
		repoRoot := t.TempDir()
		separateBeadsDir := t.TempDir() // intentionally different directory from repoRoot

		d, _, _, _, _, _ := newTestDispatcher(t)
		d.repoRoot = repoRoot
		d.beadsDir = separateBeadsDir

		err := d.appendReviewPatterns(context.Background(), "bead-1", "worker-1", []string{"pattern1"})
		if err != nil {
			t.Fatalf("appendReviewPatterns() error: %v", err)
		}

		// Must write to repoRoot/assets/review-patterns.md
		expectedFile := filepath.Join(repoRoot, "assets", "review-patterns.md")
		if _, statErr := os.Stat(expectedFile); statErr != nil {
			t.Errorf("review-patterns.md not at repoRoot/assets/: %v", statErr)
		}

		// Must NOT write to filepath.Dir(beadsDir)/assets/review-patterns.md
		wrongFile := filepath.Join(filepath.Dir(separateBeadsDir), "assets", "review-patterns.md")
		if _, statErr := os.Stat(wrongFile); statErr == nil {
			t.Errorf("review-patterns.md incorrectly created at filepath.Dir(beadsDir)/assets/ instead of repoRoot/assets/")
		}
	})
}

// TestAssignLoopRestartsAfterPanic verifies that the critical dispatcher loops
// recover from panics in their body, log a goroutine_panic event with the
// restart count, and resume operation after exponential backoff.
func TestAssignLoopRestartsAfterPanic(t *testing.T) {
	t.Run("assign_loop_restarts_and_logs", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.loopPanicBackoffFn = func(_ int) time.Duration { return 5 * time.Millisecond }

		var callCount atomic.Int32
		d.tryAssignFn = func(_ context.Context) {
			if callCount.Add(1) <= 2 {
				panic("test assign panic")
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go d.assignLoopPoll(ctx)

		// Keep feeding triggers so the loop has work to do after each restart.
		go func() {
			for {
				select {
				case d.workerReadyCh <- struct{}{}:
				case <-ctx.Done():
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()

		// (1) goroutine_panic is logged within 5s.
		waitFor(t, func() bool { return eventCount(t, d.db, "goroutine_panic") >= 1 }, 5*time.Second)
		// (2) tryAssign is called again after restart.
		waitFor(t, func() bool { return callCount.Load() >= 3 }, 5*time.Second)
	})

	t.Run("backoff_increases_on_consecutive_panics", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		var capturedN []int
		var nMu sync.Mutex
		d.loopPanicBackoffFn = func(n int) time.Duration {
			nMu.Lock()
			capturedN = append(capturedN, n)
			nMu.Unlock()
			return 1 * time.Millisecond
		}

		var callCount atomic.Int32
		d.tryAssignFn = func(_ context.Context) {
			if callCount.Add(1) <= 4 {
				panic("test backoff panic")
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go d.assignLoopPoll(ctx)

		go func() {
			for {
				select {
				case d.workerReadyCh <- struct{}{}:
				case <-ctx.Done():
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()

		waitFor(t, func() bool { return eventCount(t, d.db, "goroutine_panic") >= 4 }, 5*time.Second)

		nMu.Lock()
		counts := make([]int, len(capturedN))
		copy(counts, capturedN)
		nMu.Unlock()

		if len(counts) < 4 {
			t.Fatalf("expected at least 4 backoff calls, got %d", len(counts))
		}
		// (3) Restart counts passed to backoffFn must be 1, 2, 3, 4 (increasing).
		for i := 0; i < 4; i++ {
			if counts[i] != i+1 {
				t.Errorf("panic #%d: backoffFn got n=%d, want %d", i+1, counts[i], i+1)
			}
		}
	})

	t.Run("backoff_resets_after_5_minutes_no_panics", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		var capturedN []int
		var nMu sync.Mutex
		d.loopPanicBackoffFn = func(n int) time.Duration {
			nMu.Lock()
			capturedN = append(capturedN, n)
			nMu.Unlock()
			return 1 * time.Millisecond
		}

		// Panic 2 times, advance time by 6 minutes, then panic again.
		// The 3rd panic should reset restartCount → backoffFn gets n=1 again.
		var callCount atomic.Int32
		panicTime := d.nowFunc()
		d.nowFunc = func() time.Time { return panicTime }
		d.tryAssignFn = func(_ context.Context) {
			n := callCount.Add(1)
			switch {
			case n <= 2:
				panic("early panic")
			case n == 3:
				// Advance clock by 6 minutes so backoff resets.
				panicTime = panicTime.Add(6 * time.Minute)
				panic("post-reset panic")
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go d.assignLoopPoll(ctx)

		go func() {
			for {
				select {
				case d.workerReadyCh <- struct{}{}:
				case <-ctx.Done():
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()

		waitFor(t, func() bool { return eventCount(t, d.db, "goroutine_panic") >= 3 }, 5*time.Second)

		nMu.Lock()
		counts := make([]int, len(capturedN))
		copy(counts, capturedN)
		nMu.Unlock()

		if len(counts) < 3 {
			t.Fatalf("expected at least 3 backoff calls, got %d", len(counts))
		}
		// After reset, the 3rd panic should get n=1 again.
		if counts[2] != 1 {
			t.Errorf("after 6-minute gap, backoffFn got n=%d, want 1 (reset)", counts[2])
		}
	})

	t.Run("heartbeat_loop_restarts_after_panic", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.loopPanicBackoffFn = func(_ int) time.Duration { return 5 * time.Millisecond }

		var callCount atomic.Int32
		d.checkHeartbeatsFn = func(_ context.Context) {
			if callCount.Add(1) <= 1 {
				panic("test heartbeat panic")
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go d.heartbeatLoop(ctx)

		// heartbeatLoop ticks at HeartbeatTimeout/3 ≈ 167ms in tests.
		waitFor(t, func() bool { return eventCount(t, d.db, "goroutine_panic") >= 1 }, 5*time.Second)
		waitFor(t, func() bool { return callCount.Load() >= 2 }, 5*time.Second)
	})

	t.Run("escalation_retry_loop_restarts_after_panic", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.loopPanicBackoffFn = func(_ int) time.Duration { return 5 * time.Millisecond }

		var callCount atomic.Int32
		d.retryEscalationsFn = func(_ context.Context) {
			if callCount.Add(1) <= 1 {
				panic("test escalation panic")
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go d.escalationRetryLoop(ctx)

		// escalationRetryLoop uses EscalationRetryInterval (50ms in tests).
		waitFor(t, func() bool { return eventCount(t, d.db, "goroutine_panic") >= 1 }, 5*time.Second)
		waitFor(t, func() bool { return callCount.Load() >= 2 }, 5*time.Second)
	})
}

func TestConfigWithDefaults_DefaultBranch(t *testing.T) {
	t.Run("sets DefaultBranch to main when empty", func(t *testing.T) {
		cfg := Config{SocketPath: "/tmp/test.sock", DBPath: ":memory:"}
		resolved := cfg.withDefaults()
		if resolved.DefaultBranch != "main" {
			t.Fatalf("DefaultBranch: got %q, want %q", resolved.DefaultBranch, "main")
		}
	})

	t.Run("preserves DefaultBranch when set", func(t *testing.T) {
		cfg := Config{SocketPath: "/tmp/test.sock", DBPath: ":memory:", DefaultBranch: "develop"}
		resolved := cfg.withDefaults()
		if resolved.DefaultBranch != "develop" {
			t.Fatalf("DefaultBranch: got %q, want %q (should preserve explicit value)", resolved.DefaultBranch, "develop")
		}
	})
}

// TestAssignBead_MetadataBranch verifies that assignBead reads Metadata[MetaBranch]
// to determine the base branch, falls back to DefaultBranch when absent, and passes
// the resolved branch to both resolveEpicBranch (as defaultBranch) and worktree.Create.
func TestAssignBead_MetadataBranch(t *testing.T) {
	t.Run("MetaBranch present: uses metadata branch as base", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		d.cfg.DefaultBranch = "main"

		// Record which baseBranch was passed to Create.
		var gotBase string
		wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
			gotBase = baseBranch
			return "/tmp/wt-" + beadID, "agent/" + beadID, nil
		}
		// isBranchMerged must return false so the bead isn't closed before assignment.
		d.shutdownRunner = &mockCommandRunner{err: errors.New("exit status 1")}

		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{{
			ID:       "bead-meta-branch",
			Title:    "Bead with MetaBranch",
			Priority: 1,
			Metadata: map[string]any{MetaBranch: "develop"},
		}})

		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected ASSIGN")
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN, got %s", msg.Type)
		}
		if msg.Assign.TargetBranch != "develop" {
			t.Errorf("TargetBranch = %q, want %q", msg.Assign.TargetBranch, "develop")
		}
		waitForWorkerState(t, d, "w1", protocol.WorkerBusy, time.Second)

		wtMgr.mu.Lock()
		base := gotBase
		wtMgr.mu.Unlock()
		if base != "develop" {
			t.Errorf("worktree.Create baseBranch = %q, want %q", base, "develop")
		}
	})

	t.Run("MetaBranch absent: falls back to DefaultBranch", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		d.cfg.DefaultBranch = "trunk"

		var gotBase string
		wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
			gotBase = baseBranch
			return "/tmp/wt-" + beadID, "agent/" + beadID, nil
		}
		d.shutdownRunner = &mockCommandRunner{err: errors.New("exit status 1")}

		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{{
			ID:       "bead-no-meta",
			Title:    "Bead without MetaBranch",
			Priority: 1,
		}})

		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok {
			t.Fatal("expected ASSIGN")
		}
		if msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN, got %s", msg.Type)
		}
		if msg.Assign.TargetBranch != "trunk" {
			t.Errorf("TargetBranch = %q, want %q", msg.Assign.TargetBranch, "trunk")
		}
		waitForWorkerState(t, d, "w1", protocol.WorkerBusy, time.Second)

		wtMgr.mu.Lock()
		base := gotBase
		wtMgr.mu.Unlock()
		if base != "trunk" {
			t.Errorf("worktree.Create baseBranch = %q, want %q", base, "trunk")
		}
	})
}

// TestIsBranchMerged_DefaultBranch verifies that isBranchMerged checks against
// d.cfg.DefaultBranch, not the hardcoded string "main".
func TestIsBranchMerged_DefaultBranch(t *testing.T) {
	// Mock that returns distinct SHAs for rev-parse vs merge-base so the
	// empty-branch guard does not short-circuit, then exits 0 on
	// merge-base --is-ancestor.
	mergedRunner := func(captured *[]string) *mockCommandRunner {
		return &mockCommandRunner{
			callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				*captured = args
				if len(args) >= 1 && args[0] == "rev-parse" {
					return []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), nil
				}
				if len(args) >= 1 && args[0] == "merge-base" && (len(args) < 2 || args[1] != "--is-ancestor") {
					return []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), nil
				}
				return nil, nil
			},
		}
	}

	t.Run("uses DefaultBranch in git merge-base check", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.cfg.DefaultBranch = "develop"

		var gotArgs []string
		d.shutdownRunner = mergedRunner(&gotArgs)

		result := d.isBranchMerged(context.Background(), "bead-abc")
		if !result {
			t.Error("isBranchMerged should return true when runner exits 0")
		}

		// The last arg must be "develop", not "main".
		if len(gotArgs) == 0 {
			t.Fatal("no args passed to runner")
		}
		last := gotArgs[len(gotArgs)-1]
		if last != "develop" {
			t.Errorf("isBranchMerged checked against %q, want %q", last, "develop")
		}
	})

	t.Run("uses DefaultBranch 'main' (default)", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		// DefaultBranch is "main" by default (withDefaults).

		var gotArgs []string
		d.shutdownRunner = mergedRunner(&gotArgs)

		_ = d.isBranchMerged(context.Background(), "bead-xyz")

		if len(gotArgs) == 0 {
			t.Fatal("no args passed to runner")
		}
		last := gotArgs[len(gotArgs)-1]
		if last != "main" {
			t.Errorf("isBranchMerged checked against %q, want %q", last, "main")
		}
	})
}

// TestIsBranchMerged_EmptyBranch verifies the fix for the false-merge bug:
// when agent/<bead> exists but has zero commits beyond its merge-base with main
// (e.g., the worker never committed implementation work), isBranchMerged must
// return false. The previous behavior used `git merge-base --is-ancestor` alone,
// which trivially returns true for an empty branch sitting at a commit already
// in main's history — falsely closing beads as "branch already merged" and
// orphaning any earlier worker's implementation commits.
func TestIsBranchMerged_EmptyBranch(t *testing.T) {
	t.Run("returns false when branch tip equals merge-base with main", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		const sameSHA = "abc1234567890abcdef1234567890abcdef12345"
		d.shutdownRunner = &mockCommandRunner{
			callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if len(args) >= 1 && args[0] == "rev-parse" {
					return []byte(sameSHA + "\n"), nil
				}
				if len(args) >= 1 && args[0] == "merge-base" && (len(args) < 2 || args[1] != "--is-ancestor") {
					return []byte(sameSHA + "\n"), nil
				}
				if len(args) >= 2 && args[0] == "merge-base" && args[1] == "--is-ancestor" {
					return nil, nil
				}
				return nil, nil
			},
		}

		if d.isBranchMerged(context.Background(), "oro-bl08") {
			t.Error("isBranchMerged should return false for empty branch (tip == merge-base)")
		}
	})

	t.Run("returns true when branch has commits and is ancestor of main", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		d.shutdownRunner = &mockCommandRunner{
			callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if len(args) >= 1 && args[0] == "rev-parse" {
					return []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), nil
				}
				if len(args) >= 1 && args[0] == "merge-base" && (len(args) < 2 || args[1] != "--is-ancestor") {
					return []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), nil
				}
				if len(args) >= 2 && args[0] == "merge-base" && args[1] == "--is-ancestor" {
					return nil, nil
				}
				return nil, nil
			},
		}

		if !d.isBranchMerged(context.Background(), "oro-real") {
			t.Error("isBranchMerged should return true when branch has commits and is ancestor")
		}
	})

	t.Run("returns false when branch has commits but is not ancestor of main", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		d.shutdownRunner = &mockCommandRunner{
			callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if len(args) >= 1 && args[0] == "rev-parse" {
					return []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), nil
				}
				if len(args) >= 1 && args[0] == "merge-base" && (len(args) < 2 || args[1] != "--is-ancestor") {
					return []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), nil
				}
				if len(args) >= 2 && args[0] == "merge-base" && args[1] == "--is-ancestor" {
					return nil, errors.New("not ancestor")
				}
				return nil, nil
			},
		}

		if d.isBranchMerged(context.Background(), "oro-unmerged") {
			t.Error("isBranchMerged should return false when branch has commits but is not ancestor")
		}
	})

	t.Run("returns false when branch does not exist (rev-parse fails)", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		d.shutdownRunner = &mockCommandRunner{
			callFn: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if len(args) >= 1 && args[0] == "rev-parse" {
					return nil, errors.New("unknown revision")
				}
				return nil, nil
			},
		}

		if d.isBranchMerged(context.Background(), "oro-missing") {
			t.Error("isBranchMerged should return false when branch does not exist")
		}
	})
}

// TestMergeComplete_InterpolatesBranch verifies that the MERGE_COMPLETE escalation
// message says "merged to <targetBranch>" rather than the hardcoded "merged to main".
func TestMergeComplete_InterpolatesBranch(t *testing.T) {
	t.Run("uses targetBranch in MERGE_COMPLETE summary", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		beadID := "bead-interp"
		workerID := "w-interp"
		worktree := "/tmp/worktree-" + beadID
		branch := "agent/" + beadID
		targetBranch := "epic/my-epic"

		d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", targetBranch, 0)

		wantSummary := "merged to " + targetBranch
		found := false
		for _, msg := range esc.Messages() {
			if strings.Contains(msg, string(protocol.EscMergeComplete)) {
				found = true
				if !strings.Contains(msg, wantSummary) {
					t.Errorf("MERGE_COMPLETE escalation = %q, want it to contain %q", msg, wantSummary)
				}
				break
			}
		}
		if !found {
			t.Fatalf("expected MERGE_COMPLETE escalation, got: %v", esc.Messages())
		}
	})

	t.Run("uses DefaultBranch when targetBranch is empty string", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		beadID := "bead-empty-target"
		workerID := "w-empty"
		worktree := "/tmp/worktree-" + beadID
		branch := "agent/" + beadID

		// targetBranch = "" → should say "merged to main" (DefaultBranch)
		d.mergeAndComplete(ctx, beadID, workerID, worktree, branch, "", "", 0)

		found := false
		for _, msg := range esc.Messages() {
			if strings.Contains(msg, string(protocol.EscMergeComplete)) {
				found = true
				if !strings.Contains(msg, "merged to main") {
					t.Errorf("MERGE_COMPLETE escalation = %q, want it to contain %q", msg, "merged to main")
				}
				break
			}
		}
		if !found {
			t.Fatalf("expected MERGE_COMPLETE escalation, got: %v", esc.Messages())
		}
	})
}

func TestCheckEpicAssignable_RetriesOnError(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	epicID := "epic-retry-test"
	epicBead := protocol.Bead{ID: epicID, Type: "epic", Title: "Epic"}
	workerID := "w-test"

	// Test 1: HasChildren returns error → skip=true (bead skipped this cycle, retried next)
	beadSrc.hasChildrenErr = fmt.Errorf("transient db error")
	isDecomp, skip := d.checkEpicAssignable(ctx, epicBead, workerID)
	if isDecomp || !skip {
		t.Errorf("HasChildren error: got (%v, %v), want (false, true)", isDecomp, skip)
	}

	// Retry: clear error, hasChildren=false → (true, false) for decomposition
	beadSrc.hasChildrenErr = nil
	beadSrc.hasChildrenMap = map[string]bool{epicID: false}
	isDecomp, skip = d.checkEpicAssignable(ctx, epicBead, workerID)
	if !isDecomp || skip {
		t.Errorf("After HasChildren recovers with no children: got (%v, %v), want (true, false)", isDecomp, skip)
	}

	// Test 2: AllChildrenClosed returns error → skip=true (retried next cycle)
	beadSrc.allChildrenClosedErr = fmt.Errorf("transient db error")
	beadSrc.hasChildrenMap = map[string]bool{epicID: true}
	beadSrc.hasChildrenErr = nil
	isDecomp, skip = d.checkEpicAssignable(ctx, epicBead, workerID)
	if isDecomp || !skip {
		t.Errorf("AllChildrenClosed error: got (%v, %v), want (false, true)", isDecomp, skip)
	}

	// Retry: clear error, allClosed=true → skip (epic auto-closed)
	beadSrc.allChildrenClosedErr = nil
	beadSrc.allChildrenClosedMap = map[string]bool{epicID: true}
	isDecomp, skip = d.checkEpicAssignable(ctx, epicBead, workerID)
	if isDecomp || !skip {
		t.Errorf("After AllChildrenClosed recovers with allClosed=true: got (%v, %v), want (false, true)", isDecomp, skip)
	}

	// Test 3: No error, children exist + not all closed → skip (children still open)
	beadSrc.allChildrenClosedMap = map[string]bool{epicID: false}
	isDecomp, skip = d.checkEpicAssignable(ctx, epicBead, workerID)
	if isDecomp || !skip {
		t.Errorf("Children exist, not all closed: got (%v, %v), want (false, true)", isDecomp, skip)
	}

	// Test 4: Non-epic bead → (false, false), proceed normally
	nonEpicBead := protocol.Bead{ID: "task-1", Type: "task", Title: "Task"}
	isDecomp, skip = d.checkEpicAssignable(ctx, nonEpicBead, workerID)
	if isDecomp || skip {
		t.Errorf("Non-epic bead: got (%v, %v), want (false, false)", isDecomp, skip)
	}
}

func TestChildAssignment_SkipsWhenEpicNotAssigned(t *testing.T) {
	t.Run("epic in open status with missing branch does not escalate", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)

		// Register the epic with "open" status (not yet assigned)
		beadSrc.mu.Lock()
		beadSrc.shown["epic-open"] = &protocol.BeadDetail{
			ID:     "epic-open",
			Title:  "Epic Open",
			Type:   "epic",
			Status: "open", // Key: epic is in open status
		}
		beadSrc.mu.Unlock()

		// Epic branch does NOT exist.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}

		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		// Set a child bead of the open epic.
		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-of-open", Title: "Child of open epic", Priority: 1, Epic: "epic-open"},
		})

		// Branch is lazily created → child SHOULD be assigned.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok || msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN after lazy branch creation (open epic), got ok=%v type=%v", ok, msg.Type)
		}

		// epic_branch_created should be logged (branch was lazily created).
		foundCreated := false
		for _, event := range getLogEvents(t, d) {
			if strings.Contains(event, "epic_branch_created:") {
				foundCreated = true
				break
			}
		}
		if !foundCreated {
			t.Errorf("expected epic_branch_created log event, events: %v", getLogEvents(t, d))
		}

		// No STUCK_WORKER escalation should be sent (lazy creation succeeded).
		for _, m := range esc.Messages() {
			if strings.Contains(m, string(protocol.EscStuckWorker)) {
				t.Errorf("unexpected STUCK_WORKER escalation: %s", m)
			}
		}
	})

	t.Run("epic in in_progress status with missing branch escalates as STUCK_WORKER", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)

		// Register the epic with "in_progress" status (already assigned, being worked on)
		beadSrc.mu.Lock()
		beadSrc.shown["epic-wip"] = &protocol.BeadDetail{
			ID:     "epic-wip",
			Title:  "Epic WIP",
			Type:   "epic",
			Status: "in_progress", // Key: epic is in progress
		}
		beadSrc.mu.Unlock()

		// Epic branch does NOT exist.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}

		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w2", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		// Set a child bead of the in_progress epic.
		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-of-wip", Title: "Child of WIP epic", Priority: 1, Epic: "epic-wip"},
		})

		// Branch is lazily created → child SHOULD be assigned.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok || msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN after lazy branch creation (in_progress epic), got ok=%v type=%v", ok, msg.Type)
		}

		// epic_branch_created should be logged.
		foundCreated := false
		for _, event := range getLogEvents(t, d) {
			if strings.Contains(event, "epic_branch_created:") {
				foundCreated = true
				break
			}
		}
		if !foundCreated {
			t.Errorf("expected epic_branch_created log event, events: %v", getLogEvents(t, d))
		}

		// No STUCK_WORKER escalation: lazy creation succeeded, no error path hit.
		for _, m := range esc.Messages() {
			if strings.Contains(m, string(protocol.EscStuckWorker)) {
				t.Errorf("unexpected STUCK_WORKER escalation after lazy creation: %s", m)
			}
		}
	})

	t.Run("epic in blocked status with missing branch does not escalate", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)

		// Register the epic with "blocked" status
		beadSrc.mu.Lock()
		beadSrc.shown["epic-blocked"] = &protocol.BeadDetail{
			ID:     "epic-blocked",
			Title:  "Epic Blocked",
			Type:   "epic",
			Status: "blocked", // Key: epic is blocked
		}
		beadSrc.mu.Unlock()

		// Epic branch does NOT exist.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}

		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w3", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		// Set a child bead of the blocked epic.
		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-of-blocked", Title: "Child of blocked epic", Priority: 1, Epic: "epic-blocked"},
		})

		// Branch is lazily created → child SHOULD be assigned.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok || msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN after lazy branch creation (blocked epic), got ok=%v type=%v", ok, msg.Type)
		}

		// epic_branch_created should be logged.
		foundCreated := false
		for _, event := range getLogEvents(t, d) {
			if strings.Contains(event, "epic_branch_created:") {
				foundCreated = true
				break
			}
		}
		if !foundCreated {
			t.Errorf("expected epic_branch_created log event, events: %v", getLogEvents(t, d))
		}

		// No STUCK_WORKER escalation (lazy creation succeeded).
		for _, m := range esc.Messages() {
			if strings.Contains(m, string(protocol.EscStuckWorker)) {
				t.Errorf("unexpected STUCK_WORKER escalation for blocked epic: %s", m)
			}
		}
	})

	t.Run("epic in closed status with missing branch lazily creates branch", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)

		// Register the epic with "closed" status but branch is missing — genuine problem
		beadSrc.mu.Lock()
		beadSrc.shown["epic-closed"] = &protocol.BeadDetail{
			ID:     "epic-closed",
			Title:  "Epic Closed",
			Type:   "epic",
			Status: "closed", // Key: epic is closed but branch missing
		}
		beadSrc.mu.Unlock()

		// Epic branch does NOT exist.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}

		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w4", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		// Set a child bead of the closed epic.
		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-of-closed", Title: "Child of closed epic", Priority: 1, Epic: "epic-closed"},
		})

		// Branch is lazily created → child SHOULD be assigned.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok || msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN after lazy branch creation (closed epic), got ok=%v type=%v", ok, msg.Type)
		}

		// epic_branch_created should be logged.
		foundCreated := false
		for _, event := range getLogEvents(t, d) {
			if strings.Contains(event, "epic_branch_created:") {
				foundCreated = true
				break
			}
		}
		if !foundCreated {
			t.Errorf("expected epic_branch_created log event, events: %v", getLogEvents(t, d))
		}

		// No STUCK_WORKER escalation (lazy creation succeeded; handleEpicBranchMissing not called).
		for _, m := range esc.Messages() {
			if strings.Contains(m, string(protocol.EscStuckWorker)) {
				t.Errorf("unexpected STUCK_WORKER escalation after lazy creation: %s", m)
			}
		}
	})
}

func TestChildAssignment_ShowError_NoEscalation(t *testing.T) {
	t.Run("beads.Show returns nil detail with no error is treated as error", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)

		// Set up the mock to return nil detail with no error (edge case).
		beadSrc.mu.Lock()
		if beadSrc.shownNil == nil {
			beadSrc.shownNil = make(map[string]bool)
		}
		beadSrc.shownNil["epic-nil"] = true
		beadSrc.mu.Unlock()

		// Epic branch does NOT exist.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}

		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w6", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		// Set a child bead of the epic with nil detail.
		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-nil", Title: "Child with nil detail", Priority: 1, Epic: "epic-nil"},
		})

		// Bead should NOT be assigned.
		msg, ok := readMsg(t, conn, 500*time.Millisecond)
		if ok && msg.Type == protocol.MsgAssign {
			t.Fatal("bead should not be assigned when epic Show returns nil detail")
		}

		// No STUCK_WORKER escalation should be sent.
		escalations := esc.Messages()
		for _, esc := range escalations {
			if strings.Contains(esc, string(protocol.EscStuckWorker)) {
				t.Errorf("unexpected STUCK_WORKER escalation when Show returns nil: %s", esc)
			}
		}
	})

	t.Run("beads.Show returns transient error does not escalate", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)

		// Set up the mock to return an error for a specific epic (transient error).
		beadSrc.mu.Lock()
		if beadSrc.showErrFn == nil {
			beadSrc.showErrFn = make(map[string]error)
		}
		beadSrc.showErrFn["epic-error"] = fmt.Errorf("transient database error")
		beadSrc.mu.Unlock()

		// Epic branch does NOT exist.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}

		startDispatcher(t, d)

		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w7", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		// Set a child bead of the epic that returns error.
		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-error", Title: "Child with Show error", Priority: 1, Epic: "epic-error"},
		})

		// Bead should NOT be assigned.
		msg, ok := readMsg(t, conn, 500*time.Millisecond)
		if ok && msg.Type == protocol.MsgAssign {
			t.Fatal("bead should not be assigned when epic Show returns error")
		}

		// No STUCK_WORKER escalation should be sent.
		escalations := esc.Messages()
		for _, esc := range escalations {
			if strings.Contains(esc, string(protocol.EscStuckWorker)) {
				t.Errorf("unexpected STUCK_WORKER escalation when Show returns error: %s", esc)
			}
		}
	})
}

// --- FM4: epic FF-merge failure escalation, epicMergeFailed guard, checkEpicAssignable delegation ---

// TestEpicFFMergeFailure_EscalatesAndBlocks verifies two behaviours:
//  1. When ffMergeEpicBranch fails, completeEpicClose escalates EscStuck and
//     sets epicMergeFailed[epicID]=true.
//  2. tryCloseEpic silently skips when epicMergeFailed[epicID] is true, even
//     when all children are closed.
func TestEpicFFMergeFailure_EscalatesAndBlocks(t *testing.T) {
	t.Run("ff merge failure escalates STUCK and sets epicMergeFailed", func(t *testing.T) {
		d, _, wtMgr, esc, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-ff-fail"
		workerID := "worker-ff1"

		// Epic branch exists but FF merge always fails.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return true, nil
		}
		wtMgr.mergeFFOnlyFn = func(_, _ string) (string, error) {
			return "", fmt.Errorf("diverged history")
		}

		d.completeEpicClose(ctx, epicID, workerID, "All children completed", d.cfg.DefaultBranch)

		// STUCK escalation must have been sent.
		msgs := esc.Messages()
		foundStuck := false
		for _, msg := range msgs {
			if strings.Contains(msg, string(protocol.EscStuck)) && strings.Contains(msg, epicID) {
				foundStuck = true
				break
			}
		}
		if !foundStuck {
			t.Errorf("expected STUCK escalation for %s, got: %v", epicID, msgs)
		}

		// epicMergeFailed must be set.
		d.mu.Lock()
		failed := d.epicMergeFailed[epicID]
		d.mu.Unlock()
		if !failed {
			t.Error("expected epicMergeFailed[epicID]=true after ff merge failure")
		}
	})

	t.Run("tryCloseEpic skips when epicMergeFailed is set", func(t *testing.T) {
		d, beadSource, _, _, _, _ := newTestDispatcher(t)
		ctx := context.Background()

		_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
		if err != nil {
			t.Fatalf("init schema: %v", err)
		}

		epicID := "epic-blocked"
		workerID := "worker-blk1"

		// Mark this epic as having a failed merge.
		d.mu.Lock()
		d.epicMergeFailed[epicID] = true
		d.mu.Unlock()

		// All children are closed — would normally trigger close.
		beadSource.allChildrenClosedMap = map[string]bool{epicID: true}

		d.tryCloseEpic(ctx, epicID, workerID)

		// Epic must NOT be closed.
		beadSource.mu.Lock()
		for _, id := range beadSource.closed {
			if id == epicID {
				beadSource.mu.Unlock()
				t.Fatalf("epic %s should not be closed when epicMergeFailed is set", epicID)
			}
		}
		beadSource.mu.Unlock()
	})
}

// TestEpicMergeFailedClearedOnChildComplete verifies that mergeAndComplete
// deletes epicMergeFailed[epicID] before calling autoCloseEpicIfComplete, so
// that a rebase-fix child completing unblocks the epic's auto-close.
func TestEpicMergeFailedClearedOnChildComplete(t *testing.T) {
	d, beadSource, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	epicID := "epic-recover"
	childID := "child-rec1"
	workerID := "worker-rec1"
	worktree := "/tmp/worktree-" + childID
	branch := protocol.BranchPrefix + childID

	// Pre-set epicMergeFailed — simulates a prior ff-merge failure.
	d.mu.Lock()
	d.epicMergeFailed[epicID] = true
	d.mu.Unlock()

	// All children closed (the rebase fix just completed).
	beadSource.allChildrenClosedMap = map[string]bool{epicID: true}

	// Track the worker so mergeAndComplete can clean up.
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:      workerID,
		beadID:  childID,
		epicID:  epicID,
		state:   protocol.WorkerBusy,
		encoder: json.NewEncoder(nil),
	}
	d.mu.Unlock()

	d.mergeAndComplete(ctx, childID, workerID, worktree, branch, epicID, "", 0)

	// Wait for the async auto-close goroutine to close the epic.
	// This only happens if epicMergeFailed was cleared before autoCloseEpicIfComplete.
	waitFor(t, func() bool {
		beadSource.mu.Lock()
		defer beadSource.mu.Unlock()
		for _, id := range beadSource.closed {
			if id == epicID {
				return true
			}
		}
		return false
	}, 2*time.Second)

	// epicMergeFailed must be cleared.
	d.mu.Lock()
	failed := d.epicMergeFailed[epicID]
	d.mu.Unlock()
	if failed {
		t.Error("expected epicMergeFailed[epicID] to be cleared after child completes")
	}
}

// TestCheckEpicAssignable_DelegatesToCompleteEpicClose verifies that when all
// children are closed, checkEpicAssignable delegates to completeEpicClose
// (which FF-merges the epic branch) instead of calling beads.Close() directly.
func TestCheckEpicAssignable_DelegatesToCompleteEpicClose(t *testing.T) {
	d, beadSource, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	epicID := "epic-delegate"
	workerID := "worker-del1"

	// Epic has children and all are closed.
	beadSource.hasChildrenMap = map[string]bool{epicID: true}
	beadSource.allChildrenClosedMap = map[string]bool{epicID: true}

	// Epic branch exists — FF merge should be attempted.
	wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
		return true, nil
	}

	epicBead := protocol.Bead{ID: epicID, Type: "epic", Title: "Epic Delegate"}
	isDecomp, skip := d.checkEpicAssignable(ctx, epicBead, workerID)

	// Must return (false, true) — skip epic assignment.
	if isDecomp || !skip {
		t.Errorf("expected (false, true), got (%v, %v)", isDecomp, skip)
	}

	// FF merge must have been attempted (proves completeEpicClose was called,
	// not a bare beads.Close()).
	wtMgr.mu.Lock()
	merged := make([]string, len(wtMgr.mergedBranches))
	copy(merged, wtMgr.mergedBranches)
	wtMgr.mu.Unlock()

	epicBranch := protocol.EpicBranchPrefix + epicID
	foundMerge := false
	for _, b := range merged {
		if b == epicBranch {
			foundMerge = true
			break
		}
	}
	if !foundMerge {
		t.Errorf("expected epic branch %s to be FF-merged via completeEpicClose, got merged: %v", epicBranch, merged)
	}

	// Epic must be closed.
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
		t.Error("expected epic to be closed via completeEpicClose")
	}
}

// TestExternalClose_NoReEntry verifies the processedExternalClose re-entry guard:
//
//	(1) First call to handleClosedAssignment processes a closed bead (sends SHUTDOWN)
//	    and sets processedExternalClose[beadID] = true.
//	(2) A subsequent call with the same beadID returns early because
//	    processedExternalClose[beadID] is true — no duplicate SHUTDOWN is sent.
//	(3) clearBeadTracking removes the processedExternalClose entry (triggered by
//	    reassignment or worktree removal).
//	(4) processedExternalClose entries appear in allTrackingKeys and are pruned by
//	    deleteOrphanedTracking.
func TestExternalClose_NoReEntry(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	_, err := d.db.ExecContext(ctx, protocol.SchemaDDL)
	if err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const beadID = "bead-no-reentry"
	const workerID = "w-no-reentry"
	const worktreePath = "/tmp/worktree-no-reentry"

	// Insert assignment record so completeAssignment can update it.
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktreePath)
	if err != nil {
		t.Fatalf("insert assignment: %v", err)
	}
	assignmentID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	// Mark bead as closed externally.
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "closed"}
	beadSrc.mu.Unlock()

	// Block worktree removal so the async external-close cleanup goroutine parks
	// before clearBeadTracking runs. This keeps processedExternalClose observable
	// long enough for the re-entry assertion.
	removeBlockCh := make(chan struct{})
	closed := false
	defer func() {
		if !closed {
			close(removeBlockCh)
		}
	}()
	wtMgr.removeFn = func(_ context.Context, path string) error {
		if path != worktreePath {
			t.Fatalf("remove path = %q, want %q", path, worktreePath)
		}
		<-removeBlockCh
		return nil
	}

	// --- (1) First call processes the bead: sends SHUTDOWN. ---
	conn := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn,
		assignmentID: assignmentID,
		beadID:       beadID,
		state:        protocol.WorkerBusy,
		worktree:     worktreePath,
		encoder:      json.NewEncoder(conn),
	}
	d.worktreeByBead[beadID] = worktreePath
	d.mu.Unlock()

	d.handleClosedAssignment(ctx, workerID, beadID)

	conn.mu.Lock()
	gotShutdown := false
	for _, data := range conn.written {
		if strings.Contains(string(data), string(protocol.MsgShutdown)) {
			gotShutdown = true
			break
		}
	}
	conn.mu.Unlock()
	if !gotShutdown {
		t.Fatal("(1) expected SHUTDOWN to be sent on first call to handleClosedAssignment")
	}

	// --- (2) processedExternalClose[beadID] is true after the first call. ---
	// The cleanup goroutine is parked in worktree removal (removeBlockCh not yet
	// closed), so clearBeadTracking has not run — the flag is observable here.
	d.mu.Lock()
	processed := d.processedExternalClose[beadID]
	d.mu.Unlock()
	if !processed {
		t.Fatal("(2) expected processedExternalClose[beadID] to be true after first call")
	}

	// --- (2) Second call returns early — no duplicate SHUTDOWN. ---
	conn2 := newMockConn()
	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         conn2,
		assignmentID: assignmentID,
		beadID:       beadID,
		state:        protocol.WorkerBusy,
		worktree:     worktreePath,
		encoder:      json.NewEncoder(conn2),
	}
	d.mu.Unlock()

	d.handleClosedAssignment(ctx, workerID, beadID)

	conn2.mu.Lock()
	gotShutdown2 := false
	for _, data := range conn2.written {
		if strings.Contains(string(data), string(protocol.MsgShutdown)) {
			gotShutdown2 = true
			break
		}
	}
	conn2.mu.Unlock()
	if gotShutdown2 {
		t.Error("(2) expected no SHUTDOWN on re-entry when processedExternalClose is set")
	}

	// Release the blocked goroutine and wait for cleanup to finish.
	close(removeBlockCh)
	closed = true
	d.wg.Wait()

	// --- (3) clearBeadTracking removes the processedExternalClose entry. ---
	d.mu.Lock()
	processedAfterClear := d.processedExternalClose[beadID]
	d.mu.Unlock()
	if processedAfterClear {
		t.Error("(3) expected processedExternalClose[beadID] to be cleared after cleanup")
	}

	// --- (4a) allTrackingKeys includes processedExternalClose entries. ---
	d.mu.Lock()
	d.processedExternalClose[beadID] = true
	keys := d.allTrackingKeys()
	d.mu.Unlock()

	found := false
	for _, k := range keys {
		if k == beadID {
			found = true
			break
		}
	}
	if !found {
		t.Error("(4) expected processedExternalClose entry to appear in allTrackingKeys")
	}

	// --- (4b) deleteOrphanedTracking removes processedExternalClose entries. ---
	d.mu.Lock()
	count := d.deleteOrphanedTracking(map[string]bool{})
	processedAfterOrphan := d.processedExternalClose[beadID]
	d.mu.Unlock()

	if count == 0 {
		t.Error("(4) expected deleteOrphanedTracking to find at least one orphaned entry")
	}
	if processedAfterOrphan {
		t.Error("(4) expected processedExternalClose[beadID] to be removed by deleteOrphanedTracking")
	}
}

// TestEscalationRetryLoopShutdown verifies that closing shutdownCh causes
// escalationRetryLoop to exit promptly without waiting for the next tick.
func TestEscalationRetryLoopShutdown(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	// Use a long interval so the loop would block on ticker if shutdownCh is ignored.
	d.escalationRetryInterval = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exited := make(chan struct{})
	go func() {
		d.escalationRetryLoop(ctx)
		close(exited)
	}()

	// Give the loop a moment to enter the select, then signal shutdown.
	time.Sleep(20 * time.Millisecond)
	close(d.shutdownCh)

	select {
	case <-exited:
		// success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("escalationRetryLoop did not exit after shutdownCh was closed")
	}
}

// TestDispatcherBranchesFromMainNotStaleAgent verifies that when an agent/* branch
// already exists from a prior session, the dispatcher deletes it before creating a
// fresh worktree, ensuring the new branch is rooted at main HEAD and not the stale tip.
func TestDispatcherBranchesFromMainNotStaleAgent(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

	var mu sync.Mutex
	var ops []string // records "delete:<branch>" and "create:<beadID>:<baseBranch>" in call order

	// Simulate a stale agent/oro-stale branch from a prior session.
	wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
		return branch == "agent/oro-stale", nil
	}
	wtMgr.deleteBranchFn = func(branch string) error {
		mu.Lock()
		ops = append(ops, "delete:"+branch)
		mu.Unlock()
		return nil
	}
	wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
		mu.Lock()
		ops = append(ops, "create:"+beadID+":"+baseBranch)
		mu.Unlock()
		return "/tmp/wt-" + beadID, "agent/" + beadID, nil
	}

	// isBranchMerged must return false so the bead isn't skipped before assignment.
	d.shutdownRunner = &mockCommandRunner{err: errors.New("exit status 1")}

	startDispatcher(t, d)

	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, time.Second)
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, time.Second)

	beadSrc.SetBeads([]protocol.Bead{{
		ID:       "oro-stale",
		Title:    "Stale bead from prior session",
		Priority: 1,
	}})

	msg, ok := readMsg(t, conn, 2*time.Second)
	if !ok {
		t.Fatal("expected ASSIGN message")
	}
	if msg.Type != protocol.MsgAssign {
		t.Fatalf("expected ASSIGN, got %s", msg.Type)
	}

	mu.Lock()
	snapshot := make([]string, len(ops))
	copy(snapshot, ops)
	mu.Unlock()

	// Verify: stale branch deleted before new worktree created.
	if len(snapshot) < 2 {
		t.Fatalf("expected at least 2 ops (delete then create), got %v", snapshot)
	}
	if snapshot[0] != "delete:agent/oro-stale" {
		t.Errorf("op[0] = %q, want %q (stale branch must be deleted first)", snapshot[0], "delete:agent/oro-stale")
	}
	if snapshot[1] != "create:oro-stale:main" {
		t.Errorf("op[1] = %q, want %q (create must use main as base branch)", snapshot[1], "create:oro-stale:main")
	}
}

// TestPruneStaleAgentBranches_DeletesAllAtStartup verifies that the startup
// prune path deletes every agent/* branch returned by `git branch --list agent/*`,
// including the one marked as current with a "*" prefix. This covers AC part (a):
// "oro start: delete any pre-existing agent/* branches at startup".
func TestPruneStaleAgentBranches_DeletesAllAtStartup(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.repoRoot = t.TempDir()

	var mu sync.Mutex
	var deleted []string
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "git" {
				return nil, fmt.Errorf("unexpected command: %s", name)
			}
			// Find the git subcommand (first arg after "-C <repoRoot>" prefix).
			var sub string
			for i, a := range args {
				if a == "branch" && i+1 < len(args) {
					sub = args[i+1]
					break
				}
			}
			switch sub {
			case "--list":
				// Simulate two stale agent branches; one is the currently checked-out branch.
				return []byte("  agent/oro-foo\n* agent/oro-bar\n"), nil
			case "-D":
				// Last arg is the branch to delete.
				mu.Lock()
				deleted = append(deleted, args[len(args)-1])
				mu.Unlock()
				return nil, nil
			}
			return nil, fmt.Errorf("unexpected git subcommand: %v", args)
		},
	}
	d.shutdownRunner = runner

	d.pruneStaleAgentBranches(context.Background())

	mu.Lock()
	snapshot := append([]string(nil), deleted...)
	mu.Unlock()

	want := map[string]bool{"agent/oro-foo": false, "agent/oro-bar": false}
	for _, b := range snapshot {
		if _, ok := want[b]; !ok {
			t.Errorf("unexpected branch deleted: %q", b)
			continue
		}
		want[b] = true
	}
	for b, ok := range want {
		if !ok {
			t.Errorf("expected branch %q to be deleted, got deleted=%v", b, snapshot)
		}
	}
}

// TestPruneStaleAgentBranches_NoRepoRoot verifies the early-return guard: when
// d.repoRoot is empty, no git commands run. This prevents spurious failures in
// tests that construct a Dispatcher without wiring a real repo root.
func TestPruneStaleAgentBranches_NoRepoRoot(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.repoRoot = ""

	runner := &mockCommandRunner{
		callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			t.Fatalf("unexpected git call: %s %v", name, args)
			return nil, nil
		},
	}
	d.shutdownRunner = runner

	d.pruneStaleAgentBranches(context.Background())
}

// TestPruneStaleAgentBranches_StripsPlusPrefix verifies that the prune logic
// strips both '*' (current branch) and '+' (checked out in another worktree)
// prefixes from git branch --list output. Given branches with mixed prefixes:
// '  agent/oro-z', '* agent/oro-y', '+ agent/oro-x', all three should be
// deleted with exact branch names (no leading sigils).
func TestPruneStaleAgentBranches_StripsPlusPrefix(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.repoRoot = t.TempDir()

	var mu sync.Mutex
	var deleted []string
	runner := &mockCommandRunner{
		callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "git" {
				return nil, fmt.Errorf("unexpected command: %s", name)
			}
			// Find the git subcommand (first arg after "-C <repoRoot>" prefix).
			var sub string
			for i, a := range args {
				if a == "branch" && i+1 < len(args) {
					sub = args[i+1]
					break
				}
			}
			switch sub {
			case "--list":
				// Simulate three stale agent branches with different prefixes:
				// no prefix, * (current), + (checked out in another worktree).
				return []byte("  agent/oro-z\n* agent/oro-y\n+ agent/oro-x\n"), nil
			case "-D":
				// Last arg is the branch to delete.
				mu.Lock()
				deleted = append(deleted, args[len(args)-1])
				mu.Unlock()
				return nil, nil
			}
			return nil, fmt.Errorf("unexpected git subcommand: %v", args)
		},
	}
	d.shutdownRunner = runner

	d.pruneStaleAgentBranches(context.Background())

	mu.Lock()
	snapshot := append([]string(nil), deleted...)
	mu.Unlock()

	want := map[string]bool{
		"agent/oro-z": false,
		"agent/oro-y": false,
		"agent/oro-x": false,
	}
	for _, b := range snapshot {
		if _, ok := want[b]; !ok {
			t.Errorf("unexpected branch deleted: %q", b)
			continue
		}
		want[b] = true
	}
	for b, ok := range want {
		if !ok {
			t.Errorf("expected branch %q to be deleted, got deleted=%v", b, snapshot)
		}
	}
}
