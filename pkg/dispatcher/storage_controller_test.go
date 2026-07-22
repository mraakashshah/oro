package dispatcher //nolint:testpackage // internal pause admission wiring needs dispatcher state access

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/storage"
)

func TestDispatcherStoragePauseStopsAdmissions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	d, beads, _, _, _, spawnMock := newTestDispatcher(t)
	d.setState(StateRunning)

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open storage catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	if err := catalog.UpsertController(ctx, storage.Controller{
		ID: "dispatcher", OwnerID: "test", PID: 101, ProcessStart: now.Add(-time.Minute), HeartbeatAt: now,
		Identity: storage.ProcessIdentity{PID: 101, StartMarker: "start", Executable: "oro", ProcessGroup: 101},
	}); err != nil {
		t.Fatalf("register controller: %v", err)
	}

	drainStarted := make(chan struct{})
	drainRelease := make(chan struct{})
	controller, err := storage.NewController(storage.ControllerConfig{
		Catalog: catalog,
		ID:      "dispatcher",
		Drain: func(context.Context) error {
			close(drainStarted)
			<-drainRelease
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new storage controller: %v", err)
	}
	d.cfg.StorageController = controller

	pause, err := storage.NewPauseEpochProtocol(catalog, nil).RequestPause(ctx, now)
	if err != nil {
		t.Fatalf("request storage pause: %v", err)
	}

	observeDone := make(chan error, 1)
	go func() { observeDone <- d.observeStorageController(ctx) }()
	select {
	case <-drainStarted:
	case <-time.After(time.Second):
		t.Fatal("storage pause did not invoke dispatcher drain callback")
	}

	beads.beads = []protocol.Bead{{ID: "oro-pause-test", Title: "blocked assignment", Status: "open"}}
	d.mu.Lock()
	d.workers["idle"] = &trackedWorker{id: "idle", conn: newMockConn(), state: protocol.WorkerIdle}
	d.mu.Unlock()
	assignDone := make(chan struct{})
	go func() {
		d.tryAssign(ctx)
		close(assignDone)
	}()

	select {
	case <-assignDone:
		t.Fatal("tryAssign returned before the storage drain completed")
	case <-time.After(50 * time.Millisecond):
	}

	if got := beads.readyCalled; got != 0 {
		t.Fatalf("Ready calls while storage drain is active = %d, want 0", got)
	}

	close(drainRelease)
	if err := <-observeDone; err != nil {
		t.Fatalf("observe storage controller: %v", err)
	}
	select {
	case <-assignDone:
	case <-time.After(time.Second):
		t.Fatal("tryAssign did not return after the storage drain completed")
	}
	if _, err := catalog.PauseAcknowledgement(ctx, pause.Epoch, "dispatcher"); err != nil {
		t.Fatalf("pause acknowledgement after drain: %v", err)
	}

	d.mu.Lock()
	d.workers["reviewer"] = &trackedWorker{
		id: "reviewer", state: protocol.WorkerReviewing, beadID: "oro-pause-test", worktree: t.TempDir(), targetBranch: "main",
	}
	d.mu.Unlock()
	if d.routeReviewOpsRun(ctx, OpsRunRecord{BeadID: "oro-pause-test", WorkerID: "reviewer"}) {
		t.Fatal("storage-paused dispatcher started a persisted review")
	}
	if got := spawnMock.SpawnCount(); got != 0 {
		t.Fatalf("review starts while storage paused = %d, want 0", got)
	}

	qg := &mockQGRunner{passed: true}
	d.qgRunner = qg
	if err := d.runPreMergeQG(ctx, "oro-pause-test", "reviewer", t.TempDir(), 1, "main"); !errors.Is(err, errStorageAdmissionPaused) {
		t.Fatalf("runPreMergeQG() error = %v, want %v", err, errStorageAdmissionPaused)
	}
	qg.mu.Lock()
	qgCalls := len(qg.calls)
	qg.mu.Unlock()
	if qgCalls != 0 {
		t.Fatalf("quality-gate starts while storage paused = %d, want 0", qgCalls)
	}
}
