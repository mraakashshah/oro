package dispatcher //nolint:testpackage // white-box test verifies internal recovery gate and assignment state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

type failingDoltRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *failingDoltRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+fmt.Sprint(args))
	return nil, errors.New("bd unavailable")
}

func (r *failingDoltRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *failingDoltRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

type sqliteSentinelProcess struct {
	done chan struct{}
}

func newSQLiteSentinelProcess() *sqliteSentinelProcess {
	return &sqliteSentinelProcess{done: make(chan struct{})}
}

func (p *sqliteSentinelProcess) Wait() error {
	<-p.done
	return nil
}

func (p *sqliteSentinelProcess) Kill() error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}

type sqliteSentinelSpawner struct {
	t        *testing.T
	mainRoot string
	process  *sqliteSentinelProcess
}

func (s *sqliteSentinelSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatLineText
}

func (s *sqliteSentinelSpawner) Spawn(_ context.Context, _ string, _ string, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	if err := os.WriteFile(filepath.Join(workdir, "sentinel.txt"), []byte("assigned worktree\n"), 0o600); err != nil {
		return nil, nil, nil, err
	}
	if _, err := os.Stat(filepath.Join(s.mainRoot, "sentinel.txt")); !os.IsNotExist(err) {
		s.t.Fatalf("sentinel leaked into main root during spawn, stat err: %v", err)
	}
	return s.process, io.NopCloser(strings.NewReader("")), nil, nil
}

func TestSQLiteModeSkipsDoltRecoveryAndAssignsReadyBead(t *testing.T) {
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")

	db := newTestDB(t)
	wt := &mockWorktreeManager{
		created: make(map[string]string),
		branchExistsFn: func(context.Context, string) (bool, error) {
			return false, nil
		},
	}
	runner := &failingDoltRunner{}
	sockPath := fmt.Sprintf("/tmp/oro-sqlite-dolt-health-%d.sock", time.Now().UnixNano())

	d, err := New(
		Config{
			SocketPath:           sockPath,
			DBPath:               ":memory:",
			MaxWorkers:           1,
			InitialWorkers:       1,
			HeartbeatTimeout:     30 * time.Millisecond,
			DoltHealthInterval:   time.Millisecond,
			PollInterval:         time.Hour,
			FallbackPollInterval: time.Hour,
			BackupInterval:       time.Hour,
		},
		db,
		merge.NewCoordinator(&mockGitRunner{}),
		ops.NewSpawner(&mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}),
		beadstore.NewFakeStore(),
		wt,
		&mockEscalator{},
		nil,
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.shutdownRunner = runner

	d.maybeRecoverDolt(context.Background())

	if got := runner.count(); got != 0 {
		t.Fatalf("sqlite mode must not probe or recover dolt; got %d calls: %v", got, runner.snapshot())
	}
	if d.doltRecovering.Load() {
		t.Fatal("sqlite mode must not enter dolt recovery")
	}

	const beadID = "oro-sqlite-assign"
	if _, err := d.beads.Create(context.Background(), beadstore.CreateParams{
		ID:                 beadID,
		Title:              "SQLite assignment smoke",
		Type:               "task",
		Priority:           0,
		Description:        "Prove sqlite ready beads still assign when bd is absent.",
		AcceptanceCriteria: "Test: pkg/dispatcher TestSQLiteModeSkipsDoltRecoveryAndAssignsReadyBead | Assert: worker receives ASSIGN",
	}); err != nil {
		t.Fatalf("create sqlite bead: %v", err)
	}

	conn := newMockConn()
	d.mu.Lock()
	d.state = StateRunning
	d.workers["sqlite-worker"] = &trackedWorker{
		id:       "sqlite-worker",
		conn:     conn,
		state:    protocol.WorkerIdle,
		encoder:  json.NewEncoder(conn),
		lastSeen: d.nowFunc(),
	}
	d.mu.Unlock()

	d.tryAssign(context.Background())

	st, assigned, ok := d.WorkerInfo("sqlite-worker")
	if !ok || st != protocol.WorkerBusy || assigned != beadID {
		t.Fatalf("sqlite ready bead not assigned: ok=%v state=%s bead=%q", ok, st, assigned)
	}
}

func TestSQLiteModeControlledWorkerAssignmentKeepsSentinelInAssignedWorktree(t *testing.T) {
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")

	mainRoot := t.TempDir()
	assignedWorktree := t.TempDir()
	t.Setenv("PWD", mainRoot)
	t.Setenv("GIT_DIR", filepath.Join(mainRoot, ".git"))
	t.Setenv("GIT_WORK_TREE", mainRoot)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(mainRoot, ".git", "index"))

	db := newTestDB(t)
	wt := &mockWorktreeManager{
		created: make(map[string]string),
		createFn: func(_ context.Context, beadID, _ string) (string, string, error) {
			if beadID != "oro-sqlite-sentinel" {
				t.Fatalf("unexpected bead id %q", beadID)
			}
			return assignedWorktree, "agent/" + beadID, nil
		},
		branchExistsFn: func(context.Context, string) (bool, error) {
			return false, nil
		},
	}

	d, err := New(
		Config{
			SocketPath:           shortSockPath(t, "sqlite-sentinel"),
			DBPath:               ":memory:",
			MaxWorkers:           1,
			InitialWorkers:       1,
			HeartbeatTimeout:     time.Second,
			PollInterval:         time.Hour,
			FallbackPollInterval: time.Hour,
			BackupInterval:       time.Hour,
		},
		db,
		merge.NewCoordinator(&mockGitRunner{}),
		ops.NewSpawner(&mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}),
		beadstore.NewFakeStore(),
		wt,
		&mockEscalator{},
		nil,
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const beadID = "oro-sqlite-sentinel"
	if _, err := d.beads.Create(context.Background(), beadstore.CreateParams{
		ID:                 beadID,
		Title:              "SQLite sentinel assignment",
		Type:               "task",
		Priority:           0,
		Description:        "Prove sqlite dispatcher assignment keeps worker edits in assigned worktree.",
		AcceptanceCriteria: "Test: pkg/dispatcher TestSQLiteModeControlledWorkerAssignmentKeepsSentinelInAssignedWorktree | Assert: sentinel only appears in assigned worktree",
	}); err != nil {
		t.Fatalf("create sqlite bead: %v", err)
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	process := newSQLiteSentinelProcess()
	w := worker.NewWithConn("sqlite-sentinel-worker", workerConn, &sqliteSentinelSpawner{
		t:        t,
		mainRoot: mainRoot,
		process:  process,
	})
	w.SetHeartbeatInterval(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = process.Kill()
		_ = dispatcherConn.Close()
		select {
		case <-errCh:
		case <-time.After(time.Second):
			t.Fatal("worker did not exit")
		}
	})

	if msg, ok := readMsg(t, dispatcherConn, time.Second); !ok || msg.Type != protocol.MsgHeartbeat {
		t.Fatalf("expected initial worker heartbeat, ok=%v msg=%+v", ok, msg)
	}

	d.mu.Lock()
	d.state = StateRunning
	d.workers["sqlite-sentinel-worker"] = &trackedWorker{
		id:       "sqlite-sentinel-worker",
		conn:     dispatcherConn,
		state:    protocol.WorkerIdle,
		encoder:  json.NewEncoder(dispatcherConn),
		lastSeen: d.nowFunc(),
	}
	d.mu.Unlock()

	d.tryAssign(context.Background())

	if msg, ok := readMsg(t, dispatcherConn, time.Second); !ok || msg.Type != protocol.MsgStatus {
		t.Fatalf("expected running worker status after assignment, ok=%v msg=%+v", ok, msg)
	}
	if _, err := os.Stat(filepath.Join(assignedWorktree, "sentinel.txt")); err != nil {
		t.Fatalf("sentinel missing from assigned worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainRoot, "sentinel.txt")); !os.IsNotExist(err) {
		t.Fatalf("sentinel leaked into main root, stat err: %v", err)
	}
}

func TestSQLitePrimaryDefaultsToSQLiteMode(t *testing.T) {
	previous, hadPrevious := os.LookupEnv("ORO_BEADSOURCE_MODE")
	if err := os.Unsetenv("ORO_BEADSOURCE_MODE"); err != nil {
		t.Fatalf("unset mode: %v", err)
	}
	t.Cleanup(func() {
		if hadPrevious {
			_ = os.Setenv("ORO_BEADSOURCE_MODE", previous)
			return
		}
		_ = os.Unsetenv("ORO_BEADSOURCE_MODE")
	})

	db := newTestDB(t)
	runner := &failingDoltRunner{}
	sockPath := fmt.Sprintf("/tmp/oro-sqlite-primary-default-%d.sock", time.Now().UnixNano())
	d, err := New(
		Config{
			SocketPath:         sockPath,
			DBPath:             ":memory:",
			MaxWorkers:         1,
			DoltHealthInterval: time.Millisecond,
			BackupInterval:     time.Hour,
		},
		db,
		merge.NewCoordinator(&mockGitRunner{}),
		ops.NewSpawner(&mockBatchSpawner{verdict: "looks good\n\nVERDICT: APPROVED"}),
		beadstore.NewSQLiteStore(db),
		&mockWorktreeManager{created: make(map[string]string)},
		&mockEscalator{},
		nil,
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.shutdownRunner = runner

	if d.beadSourceMode != "sqlite" {
		t.Fatalf("beadSourceMode = %q, want sqlite", d.beadSourceMode)
	}
	d.maybeRecoverDolt(context.Background())
	if got := runner.count(); got != 0 {
		t.Fatalf("sqlite primary default must not probe or recover dolt; got %d calls: %v", got, runner.snapshot())
	}
}
