package dispatcher //nolint:testpackage // white-box test verifies internal recovery gate and assignment state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"
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
		NewCLIStore(runner),
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
