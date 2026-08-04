package dispatcher //nolint:testpackage // white-box scheduler wake-up regression

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/beadstore/migrations"
	"oro/pkg/protocol"
)

func TestNativeSchedulerSingleTriggerRefillsTopEpicWithoutPollDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if err := migrations.MigrateToV3(ctx, d.db); err != nil {
		t.Fatalf("migrate native beadstore: %v", err)
	}
	d.beads = beadstore.NewSQLiteStore(d.db)
	d.beadSourceMode = "sqlite"
	d.cfg.PollInterval = 10 * time.Second
	d.cfg.FallbackPollInterval = 10 * time.Second
	d.setState(StateRunning)

	const topEpic = "epic-refill-top"
	if _, err := d.beads.Create(ctx, beadstore.CreateParams{
		ID: topEpic, Title: "Top epic", Type: "epic", Priority: 0,
	}); err != nil {
		t.Fatalf("create top epic: %v", err)
	}
	for i, beadID := range []string{"top-child-a", "top-child-b"} {
		if _, err := d.beads.Create(ctx, beadstore.CreateParams{
			ID: beadID, Title: beadID, Type: "task", Priority: i, ParentID: topEpic,
			AcceptanceCriteria: "Test: scheduler refill | Cmd: go test ./pkg/dispatcher | Assert: assigned",
		}); err != nil {
			t.Fatalf("create top epic child %s: %v", beadID, err)
		}
	}
	if _, err := d.beads.Create(ctx, beadstore.CreateParams{
		ID: "epic-refill-lower", Title: "Lower epic", Type: "epic", Priority: 1,
	}); err != nil {
		t.Fatalf("create lower epic: %v", err)
	}
	if _, err := d.beads.Create(ctx, beadstore.CreateParams{
		ID: "lower-child", Title: "lower-child", Type: "task", ParentID: "epic-refill-lower",
		AcceptanceCriteria: "Test: lower epic | Cmd: go test ./pkg/dispatcher | Assert: deferred",
	}); err != nil {
		t.Fatalf("create lower epic child: %v", err)
	}

	d.mu.Lock()
	d.targetWorkers = 2
	for i := range 2 {
		workerID := fmt.Sprintf("w-native-refill-%d", i)
		d.workers[workerID] = &trackedWorker{
			id: workerID, conn: newMockConn(), state: protocol.WorkerIdle, managed: true,
		}
	}
	d.mu.Unlock()

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		d.assignLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-loopDone:
		case <-time.After(time.Second):
			t.Error("native assignment loop did not stop")
		}
	})

	started := time.Now()
	d.notifyAssignLoop()
	waitFor(t, func() bool {
		var count int
		return d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status='active'`).Scan(&count) == nil && count == 2
	}, 2*time.Second)
	if elapsed := time.Since(started); elapsed >= d.cfg.PollInterval {
		t.Fatalf("top epic refill took %v, want before %v poll interval", elapsed, d.cfg.PollInterval)
	}

	got := assignedBeadIDsSorted(t, d.db)
	want := []string{"top-child-a", "top-child-b"}
	if !slices.Equal(got, want) {
		t.Fatalf("single-trigger assignments = %v, want top epic refill %v", got, want)
	}
}

func TestEpicSchedulerDoesNotRefillAfterClaimedSetupCleansUp(t *testing.T) {
	d, beadSrc, workers := setupTryAssignSchedulingTest(t, 2)
	seedTryAssignEpic(t, beadSrc, "epic-no-refill-after-cleanup", 0, "2026-08-03T00:00:00Z")
	for priority, beadID := range []string{"cleanup-child-a", "cleanup-child-b"} {
		seedTryAssignBead(t, beadSrc, protocol.Bead{
			ID: beadID, Priority: priority, Epic: "epic-no-refill-after-cleanup",
		})
	}
	beadSrc.SetBeads([]protocol.Bead{
		{ID: "cleanup-child-a", Priority: 0, Epic: "epic-no-refill-after-cleanup"},
		{ID: "cleanup-child-b", Priority: 1, Epic: "epic-no-refill-after-cleanup"},
	})
	if _, err := d.db.Exec(`
CREATE TRIGGER fail_scheduler_assignment_capability
BEFORE INSERT ON assignment_capabilities
BEGIN
  SELECT RAISE(ABORT, 'injected capability persistence failure');
END`); err != nil {
		t.Fatalf("install capability failure trigger: %v", err)
	}
	for {
		select {
		case <-d.workerReadyCh:
		default:
			goto drained
		}
	}

drained:
	handles := d.tryAssignBatch(context.Background())
	waitForSetup(t, handles)

	notifyCount := 0
	quiet := time.NewTimer(100 * time.Millisecond)
	defer quiet.Stop()
	for {
		select {
		case <-d.workerReadyCh:
			notifyCount++
		case <-quiet.C:
			if notifyCount != 0 {
				t.Fatalf("immediate refill notifications after cleaned-up setup = %d, want 0", notifyCount)
			}
			assertMockWorkerAssignCount(t, workers, 0)
			var active int
			if err := d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE status='active'`).Scan(&active); err != nil {
				t.Fatalf("count active assignments: %v", err)
			}
			if active != 0 {
				t.Fatalf("active assignments after capability cleanup = %d, want 0", active)
			}
			return
		}
	}
}
