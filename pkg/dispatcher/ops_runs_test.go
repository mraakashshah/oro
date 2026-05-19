package dispatcher //nolint:testpackage // exercises dispatcher-owned DB helpers

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

func TestOpsRunPersistenceLifecycle(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	if _, _, err := CreateOpsRun(ctx, nil, OpsRunRecord{Type: "decompose", BeadID: "oro-nil"}); err == nil {
		t.Fatal("CreateOpsRun nil db error = nil, want error")
	}

	rec := OpsRunRecord{
		Type:          "decompose",
		BeadID:        "oro-life",
		WorkerID:      "worker-1",
		DispatcherPID: 101,
		ProcessPID:    202,
		Runtime:       "codex",
		Model:         "gpt-5.5",
	}
	created, wasCreated, err := CreateOpsRun(ctx, db, rec)
	if err != nil {
		t.Fatalf("CreateOpsRun first: %v", err)
	}
	if !wasCreated {
		t.Fatal("CreateOpsRun first created = false, want true")
	}
	if created.ID == 0 {
		t.Fatal("CreateOpsRun first ID = 0, want persisted ID")
	}
	if created.Status != "running" {
		t.Fatalf("CreateOpsRun first status = %q, want running", created.Status)
	}

	duplicate, wasCreated, err := CreateOpsRun(ctx, db, OpsRunRecord{
		Type:     "decompose",
		BeadID:   "oro-life",
		WorkerID: "worker-2",
	})
	if err != nil {
		t.Fatalf("CreateOpsRun duplicate: %v", err)
	}
	if wasCreated {
		t.Fatal("CreateOpsRun duplicate created = true, want existing blocking row")
	}
	if duplicate.ID != created.ID {
		t.Fatalf("duplicate ID = %d, want existing ID %d", duplicate.ID, created.ID)
	}
	if duplicate.WorkerID != "worker-1" {
		t.Fatalf("duplicate WorkerID = %q, want original record", duplicate.WorkerID)
	}

	if err := CompleteOpsRun(ctx, db, created.ID, "resolved", "ok", "fixed", ""); err != nil {
		t.Fatalf("CompleteOpsRun resolved: %v", err)
	}
	if err := CompleteOpsRun(ctx, db, created.ID, "resolved", "ok", "fixed", ""); err != nil {
		t.Fatalf("CompleteOpsRun repeated resolved: %v", err)
	}
	blocking, err := FindBlockingOpsRun(ctx, db, "decompose", "oro-life")
	if err != nil {
		t.Fatalf("FindBlockingOpsRun after resolved: %v", err)
	}
	if blocking != nil {
		t.Fatalf("FindBlockingOpsRun after resolved = %#v, want nil", blocking)
	}

	next, wasCreated, err := CreateOpsRun(ctx, db, rec)
	if err != nil {
		t.Fatalf("CreateOpsRun after resolved: %v", err)
	}
	if !wasCreated {
		t.Fatal("CreateOpsRun after resolved created = false, want true")
	}
	if next.ID == created.ID {
		t.Fatalf("CreateOpsRun after resolved reused ID %d, want new row", next.ID)
	}
}

func TestFindBlockingOpsRun(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	if _, err := FindBlockingOpsRun(ctx, nil, "decompose", "oro-nil"); err == nil {
		t.Fatal("FindBlockingOpsRun nil db error = nil, want error")
	}

	for _, status := range []string{"running", "failed", "stale"} {
		rec, _, err := CreateOpsRun(ctx, db, OpsRunRecord{
			Type:   "decompose",
			BeadID: "oro-" + status,
			Status: status,
		})
		if err != nil {
			t.Fatalf("CreateOpsRun %s: %v", status, err)
		}
		blocking, err := FindBlockingOpsRun(ctx, db, "decompose", "oro-"+status)
		if err != nil {
			t.Fatalf("FindBlockingOpsRun %s: %v", status, err)
		}
		if blocking == nil {
			t.Fatalf("FindBlockingOpsRun %s = nil, want row", status)
		}
		if blocking.ID != rec.ID || blocking.Status != status {
			t.Fatalf("FindBlockingOpsRun %s = %#v, want ID %d status %q", status, blocking, rec.ID, status)
		}
	}

	for _, status := range []string{"resolved", "superseded"} {
		if _, _, err := CreateOpsRun(ctx, db, OpsRunRecord{
			Type:   "decompose",
			BeadID: "oro-" + status,
			Status: status,
		}); err != nil {
			t.Fatalf("CreateOpsRun %s: %v", status, err)
		}
		blocking, err := FindBlockingOpsRun(ctx, db, "decompose", "oro-"+status)
		if err != nil {
			t.Fatalf("FindBlockingOpsRun %s: %v", status, err)
		}
		if blocking != nil {
			t.Fatalf("FindBlockingOpsRun %s = %#v, want nil", status, blocking)
		}
	}
}

func TestOpsRunCompletionStates(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	if err := CompleteOpsRun(ctx, nil, 1, "resolved", "", "", ""); err == nil {
		t.Fatal("CompleteOpsRun nil db error = nil, want error")
	}

	for _, status := range []string{"failed", "stale", "resolved", "superseded"} {
		rec, _, err := CreateOpsRun(ctx, db, OpsRunRecord{
			Type:   "decompose",
			BeadID: "oro-complete-" + status,
		})
		if err != nil {
			t.Fatalf("CreateOpsRun %s: %v", status, err)
		}
		if err := CompleteOpsRun(ctx, db, rec.ID, status, "verdict-"+status, "feedback", "error"); err != nil {
			t.Fatalf("CompleteOpsRun %s: %v", status, err)
		}
		got := fetchOpsRunForTest(t, db, rec.ID)
		if got.Status != status {
			t.Fatalf("status after CompleteOpsRun(%s) = %q", status, got.Status)
		}
		if got.CompletedAt == "" {
			t.Fatalf("CompletedAt after CompleteOpsRun(%s) is empty", status)
		}

		blocking, err := FindBlockingOpsRun(ctx, db, "decompose", "oro-complete-"+status)
		if err != nil {
			t.Fatalf("FindBlockingOpsRun %s: %v", status, err)
		}
		wantBlocking := status == "failed" || status == "stale"
		if (blocking != nil) != wantBlocking {
			t.Fatalf("FindBlockingOpsRun %s present = %v, want %v", status, blocking != nil, wantBlocking)
		}
	}

	rec, _, err := CreateOpsRun(ctx, db, OpsRunRecord{Type: "decompose", BeadID: "oro-invalid-status"})
	if err != nil {
		t.Fatalf("CreateOpsRun invalid-status fixture: %v", err)
	}
	if err := CompleteOpsRun(ctx, db, rec.ID, "wat", "", "", ""); err == nil {
		t.Fatal("CompleteOpsRun invalid status error = nil, want error")
	}
}

func TestDispatcherStartupMarksOrphanedOpsRunsStale(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, spawnMock := newTestDispatcher(t)

	live, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          "decompose",
		BeadID:        "oro-live-ops",
		DispatcherPID: 999999,
		ProcessPID:    os.Getpid(),
	})
	if err != nil {
		t.Fatalf("CreateOpsRun live orphan: %v", err)
	}
	dead, _, err := CreateOpsRun(ctx, d.db, OpsRunRecord{
		Type:          "decompose",
		BeadID:        "oro-dead-ops",
		DispatcherPID: 999999,
		ProcessPID:    -1,
	})
	if err != nil {
		t.Fatalf("CreateOpsRun dead orphan: %v", err)
	}

	if err := d.startupRecovery(ctx); err != nil {
		t.Fatalf("startupRecovery: %v", err)
	}

	liveAfter := fetchOpsRunForTest(t, d.db, live.ID)
	if liveAfter.Status != "stale" {
		t.Fatalf("live orphan status = %q, want stale", liveAfter.Status)
	}

	deadAfter := fetchOpsRunForTest(t, d.db, dead.ID)
	if deadAfter.Status != "superseded" {
		t.Fatalf("dead orphan status = %q, want superseded", deadAfter.Status)
	}

	blocking, err := FindBlockingOpsRun(ctx, d.db, "decompose", "oro-dead-ops")
	if err != nil {
		t.Fatalf("FindBlockingOpsRun rerouted dead orphan: %v", err)
	}
	if blocking == nil {
		t.Fatal("FindBlockingOpsRun rerouted dead orphan = nil, want new running row")
	}
	if blocking.ID == dead.ID {
		t.Fatalf("rerouted blocking row reused superseded ID %d", blocking.ID)
	}
	if blocking.Status != "running" {
		t.Fatalf("rerouted status = %q, want running", blocking.Status)
	}

	waitFor(t, func() bool {
		return spawnMock.SpawnCount() > 0
	}, time.Second)
}

func fetchOpsRunForTest(t *testing.T, db *sql.DB, id int64) OpsRunRecord {
	t.Helper()
	rec, err := scanOpsRun(db.QueryRowContext(context.Background(), `
SELECT id, escalation_id, type, bead_id, worker_id, dispatcher_pid, process_pid, runtime, model, status, verdict, feedback, error, started_at, completed_at
FROM ops_runs
WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("ops run %d not found", id)
		}
		t.Fatalf("scan ops run %d: %v", id, err)
	}
	return rec
}
