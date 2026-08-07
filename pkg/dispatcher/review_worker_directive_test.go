package dispatcher //nolint:testpackage // white-box lifecycle edge assertions

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"oro/pkg/protocol"
)

func TestReviewWorkerDirectivesDurablyReleaseCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause ReviewReleaseCause
		apply func(*Dispatcher, string) (string, error)
	}{
		{name: "kill", cause: ReviewReleaseCauseKilled, apply: (*Dispatcher).applyKillWorker},
		{name: "restart", cause: ReviewReleaseCauseRestarted, apply: (*Dispatcher).applyRestartWorker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, beadSrc, _, _, _, _ := newTestDispatcher(t)
			pm := &mockProcessManager{}
			d.procMgr = pm
			d.targetWorkers = 3
			checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "directive-"+tc.name, ReviewCheckpointStateCorrectionAssigned, "active")
			worker.managed = true
			drainCheckpointReleaseWakes(d)

			detail, err := tc.apply(d, worker.id)
			if err != nil {
				t.Fatalf("%s review worker: %v", tc.name, err)
			}
			if !strings.Contains(detail, worker.id) {
				t.Fatalf("directive detail = %q, want worker ID", detail)
			}
			assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateCorrectionAssigned)
			if got := trackedReleaseWorker(d, worker.id); got != nil {
				t.Fatalf("released directive worker remains tracked: %p", got)
			}
			assertCheckpointReleaseEvent(t, d, worker.beadID, worker.id, tc.cause)
			assertOneCheckpointReleaseWake(t, d)
			beadSrc.mu.Lock()
			updated := beadSrc.updated[worker.beadID]
			beadSrc.mu.Unlock()
			if updated != "" {
				t.Fatalf("checkpoint-owned bead status changed to %q", updated)
			}

			switch tc.name {
			case "kill":
				if d.targetWorkers != 2 {
					t.Fatalf("targetWorkers = %d, want 2 after managed kill", d.targetWorkers)
				}
				if got := pm.SpawnedIDs(); len(got) != 0 {
					t.Fatalf("kill spawned workers: %v", got)
				}
			case "restart":
				if d.targetWorkers != 3 {
					t.Fatalf("targetWorkers = %d, want unchanged 3 after restart", d.targetWorkers)
				}
				d.mu.Lock()
				pending := d.pendingManagedIDs[worker.id]
				_, hasSince := d.pendingManagedSince[worker.id]
				d.mu.Unlock()
				if !pending || !hasSince {
					t.Fatalf("restart managed bookkeeping = pending %t since %t, want true/true", pending, hasSince)
				}
				if got := pm.KilledIDs(); len(got) != 1 || got[0] != worker.id {
					t.Fatalf("restart killed IDs = %v, want [%s]", got, worker.id)
				}
				if got := pm.SpawnedIDs(); len(got) != 1 || got[0] != worker.id {
					t.Fatalf("restart spawned IDs = %v, want [%s]", got, worker.id)
				}
			}
		})
	}
}

func TestReviewWorkerDirectiveReleaseFailureDoesNotFallBack(t *testing.T) {
	for _, directive := range []struct {
		name  string
		apply func(*Dispatcher, string) (string, error)
	}{
		{name: "kill", apply: (*Dispatcher).applyKillWorker},
		{name: "restart", apply: (*Dispatcher).applyRestartWorker},
	} {
		for _, release := range []struct {
			name             string
			assignmentStatus string
			terminal         bool
		}{
			{name: "conflict", assignmentStatus: "requeued"},
			{name: "no-op", assignmentStatus: "active", terminal: true},
		} {
			t.Run(directive.name+"/"+release.name, func(t *testing.T) {
				d, beadSrc, _, _, _, _ := newTestDispatcher(t)
				pm := &mockProcessManager{}
				d.procMgr = pm
				d.targetWorkers = 3
				checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "directive-fail-"+directive.name+"-"+release.name, ReviewCheckpointStateReviewRunning, release.assignmentStatus)
				worker.managed = true
				if release.terminal {
					mustExec(t, d, `UPDATE review_checkpoints SET state='integrated' WHERE id=?`, checkpointID)
				}
				drainCheckpointReleaseWakes(d)

				_, err := directive.apply(d, worker.id)

				if err == nil {
					t.Fatal("directive error = nil, want durable release failure")
				}
				if got := trackedReleaseWorker(d, worker.id); got != worker {
					t.Fatalf("failed release changed worker: got %p, want %p", got, worker)
				}
				if d.targetWorkers != 3 {
					t.Fatalf("failed release changed targetWorkers to %d", d.targetWorkers)
				}
				d.mu.Lock()
				pending := d.pendingManagedIDs[worker.id]
				d.mu.Unlock()
				if pending || len(pm.KilledIDs()) != 0 || len(pm.SpawnedIDs()) != 0 {
					t.Fatalf("failed release entered managed fallback: pending=%t killed=%v spawned=%v", pending, pm.KilledIDs(), pm.SpawnedIDs())
				}
				var checkpointWorker, assignmentStatus string
				if err := d.db.QueryRow(`SELECT COALESCE(worker_id, '') FROM review_checkpoints WHERE id=?`, checkpointID).Scan(&checkpointWorker); err != nil {
					t.Fatalf("load checkpoint worker: %v", err)
				}
				if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
					t.Fatalf("load assignment: %v", err)
				}
				if checkpointWorker != worker.id || assignmentStatus != release.assignmentStatus {
					t.Fatalf("durable state changed: worker/status=%q/%q, want %q/%q", checkpointWorker, assignmentStatus, worker.id, release.assignmentStatus)
				}
				beadSrc.mu.Lock()
				updated := beadSrc.updated[worker.beadID]
				beadSrc.mu.Unlock()
				if updated != "" {
					t.Fatalf("failed release changed bead status to %q", updated)
				}
				assertCheckpointReleaseEventCount(t, d, 0)
				assertNoCheckpointReleaseWake(t, d)
			})
		}
	}
}

func TestOrdinaryWorkerDirectivesRetainExistingBehavior(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*Dispatcher, string) (string, error)
	}{
		{name: "kill", apply: (*Dispatcher).applyKillWorker},
		{name: "restart", apply: (*Dispatcher).applyRestartWorker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			worker := &trackedWorker{id: "ordinary-" + tc.name, conn: newMockConn(), state: protocol.WorkerIdle}
			d.mu.Lock()
			d.workers[worker.id] = worker
			d.mu.Unlock()

			if _, err := tc.apply(d, worker.id); err != nil {
				t.Fatalf("ordinary %s: %v", tc.name, err)
			}
			if got := trackedReleaseWorker(d, worker.id); got != nil {
				t.Fatalf("ordinary %s worker remains tracked: %p", tc.name, got)
			}
		})
	}
}

func TestReviewWorkerKillFenceRejectsReconnectUntilFinalized(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.targetWorkers = 2
	checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "kill-fence", ReviewCheckpointStateReviewRunning, "active")
	worker.managed = true
	observedConn := worker.conn
	storeStarted := make(chan struct{})
	releaseStore := make(chan struct{})
	done := make(chan error, 1)
	drainCheckpointReleaseWakes(d)

	go func() {
		_, err := d.killCheckpointOwnedWorkerUsing(ctx, worker, observedConn,
			func(ctx context.Context, beadID, workerID string) (bool, error) {
				close(storeStarted)
				<-releaseStore
				return NewReviewCheckpointStore(d.db).ReleaseWorker(ctx, beadID, workerID)
			})
		done <- err
	}()
	<-storeStarted
	if accepted := d.registerWorkerWithProtocol(worker.id, newMockConn(), false); accepted {
		t.Fatal("kill fence accepted reconnect while Store blocked")
	}
	close(releaseStore)
	if err := <-done; err != nil {
		t.Fatalf("kill fenced review worker: %v", err)
	}

	assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateReviewRunning)
	assertCheckpointReleaseEvent(t, d, worker.beadID, worker.id, ReviewReleaseCauseKilled)
	assertOneCheckpointReleaseWake(t, d)
	if got := trackedReleaseWorker(d, worker.id); got != nil {
		t.Fatalf("killed review worker remains tracked: %p", got)
	}
	if d.targetWorkers != 1 {
		t.Fatalf("targetWorkers = %d, want 1", d.targetWorkers)
	}
	assertShutdownWrittenToConn(t, observedConn)
	if accepted := d.registerWorkerWithProtocol(worker.id, newMockConn(), false); !accepted {
		t.Fatal("kill fence rejected C3 after finalization")
	}
}

func TestReviewWorkerRestartFenceSpansStoreKillAndSpawn(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	pm := newBlockingDirectiveProcessManager()
	d.procMgr = pm
	checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "restart-fence", ReviewCheckpointStateReviewRunning, "active")
	worker.managed = true
	observedConn := worker.conn
	storeStarted := make(chan struct{})
	releaseStore := make(chan struct{})
	done := make(chan error, 1)
	drainCheckpointReleaseWakes(d)

	go func() {
		_, err := d.restartCheckpointOwnedWorkerUsing(ctx, worker, observedConn,
			func(ctx context.Context, beadID, workerID string) (bool, error) {
				close(storeStarted)
				<-releaseStore
				return NewReviewCheckpointStore(d.db).ReleaseWorker(ctx, beadID, workerID)
			})
		done <- err
	}()
	<-storeStarted
	assertReviewReconnectRejected(t, d, worker.id, "Store")
	close(releaseStore)
	<-pm.killStarted
	assertReviewReconnectRejected(t, d, worker.id, "Kill")
	close(pm.releaseKill)
	<-pm.spawnStarted
	d.mu.Lock()
	pendingBefore := d.pendingManagedIDs[worker.id]
	d.mu.Unlock()
	if !pendingBefore {
		t.Fatal("restart did not install pending-managed state before Spawn")
	}
	assertReviewReconnectRejected(t, d, worker.id, "Spawn")
	d.mu.Lock()
	pendingAfter := d.pendingManagedIDs[worker.id]
	d.mu.Unlock()
	if !pendingAfter {
		t.Fatal("rejected Spawn-phase reconnect consumed pending-managed state")
	}
	close(pm.releaseSpawn)
	if err := <-done; err != nil {
		t.Fatalf("restart fenced review worker: %v", err)
	}

	assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateReviewRunning)
	assertCheckpointReleaseEvent(t, d, worker.beadID, worker.id, ReviewReleaseCauseRestarted)
	assertOneCheckpointReleaseWake(t, d)
	assertShutdownWrittenToConn(t, observedConn)
	if got := trackedReleaseWorker(d, worker.id); got != nil {
		t.Fatalf("restarted review worker remains tracked: %p", got)
	}
	if accepted := d.registerWorkerWithProtocol(worker.id, newMockConn(), false); !accepted {
		t.Fatal("restart fence rejected C3 after finalization")
	}
}

func TestReviewWorkerRestartActionErrorsStillFinalizeDurableRelease(t *testing.T) {
	for _, tc := range []struct {
		name     string
		killErr  error
		spawnErr error
	}{
		{name: "kill error", killErr: errors.New("injected kill failure")},
		{name: "spawn error", spawnErr: errors.New("injected spawn failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			pm := &mockProcessManager{killErr: tc.killErr, spawnErr: tc.spawnErr}
			d.procMgr = pm
			checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(t, d, "restart-action-"+tc.name, ReviewCheckpointStateReviewRunning, "active")
			worker.managed = true
			observedConn := worker.conn
			drainCheckpointReleaseWakes(d)

			_, err := d.restartCheckpointOwnedWorkerUsing(context.Background(), worker, observedConn,
				NewReviewCheckpointStore(d.db).ReleaseWorker)

			if err == nil {
				t.Fatal("restart action error = nil")
			}
			assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateReviewRunning)
			assertCheckpointReleaseEvent(t, d, worker.beadID, worker.id, ReviewReleaseCauseRestarted)
			assertOneCheckpointReleaseWake(t, d)
			if got := trackedReleaseWorker(d, worker.id); got != nil {
				t.Fatalf("action-error review worker remains tracked: %p", got)
			}
			d.mu.Lock()
			pending := d.pendingManagedIDs[worker.id]
			d.mu.Unlock()
			if tc.killErr != nil && (pending || len(pm.SpawnedIDs()) != 0) {
				t.Fatalf("kill error bookkeeping = pending %t spawned %v, want false/none", pending, pm.SpawnedIDs())
			}
			if tc.spawnErr != nil && !pending {
				t.Fatal("spawn error did not preserve pending-managed retry state")
			}
			if accepted := d.registerWorkerWithProtocol(worker.id, newMockConn(), false); !accepted {
				t.Fatal("action error retained restart fence")
			}
		})
	}
}

type blockingDirectiveProcessManager struct {
	killStarted  chan struct{}
	releaseKill  chan struct{}
	spawnStarted chan struct{}
	releaseSpawn chan struct{}
	mu           sync.Mutex
	killed       []string
	spawned      []string
}

func newBlockingDirectiveProcessManager() *blockingDirectiveProcessManager {
	return &blockingDirectiveProcessManager{
		killStarted:  make(chan struct{}),
		releaseKill:  make(chan struct{}),
		spawnStarted: make(chan struct{}),
		releaseSpawn: make(chan struct{}),
	}
}

func (p *blockingDirectiveProcessManager) Kill(id string) error {
	p.mu.Lock()
	p.killed = append(p.killed, id)
	p.mu.Unlock()
	close(p.killStarted)
	<-p.releaseKill
	return nil
}

func (p *blockingDirectiveProcessManager) Spawn(id string) (*os.Process, error) {
	p.mu.Lock()
	p.spawned = append(p.spawned, id)
	p.mu.Unlock()
	close(p.spawnStarted)
	<-p.releaseSpawn
	return nil, nil
}

func (*blockingDirectiveProcessManager) IsAlive(string) bool { return true }

func assertReviewReconnectRejected(t *testing.T, d *Dispatcher, workerID, phase string) {
	t.Helper()
	conn := newMockConn()
	if accepted := d.registerWorkerWithProtocol(workerID, conn, false); accepted {
		t.Fatalf("restart fence accepted reconnect during %s", phase)
	}
	if !conn.closed {
		t.Fatalf("restart fence left %s reconnect open", phase)
	}
}

func assertShutdownWrittenToConn(t *testing.T, conn any) {
	t.Helper()
	mock, ok := conn.(*mockConn)
	if !ok {
		t.Fatalf("connection = %T, want *mockConn", conn)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.written) != 1 || !strings.Contains(string(mock.written[0]), string(protocol.MsgShutdown)) {
		t.Fatalf("shutdown writes = %q, want one SHUTDOWN", mock.written)
	}
}
