package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/dbutil"
	"oro/pkg/storage"
)

type runtimeLeaseCatalog interface {
	AcquireLease(context.Context, storage.LeaseRequest) (storage.Lease, error)
	ReleaseLease(context.Context, storage.LeaseID) error
}

var _ runtimeLeaseCatalog = (*storage.Catalog)(nil)

func TestCatalogPreservesRetiredRuntimeReservations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "catalog.db")
	catalog, err := storage.OpenCatalog(ctx, path)
	if err != nil {
		t.Fatalf("create legacy catalog: %v", err)
	}
	t.Cleanup(func() {
		if catalog != nil {
			_ = catalog.Close()
		}
	})

	artifacts := make(map[string]string)
	for index, state := range []string{"active", "interrupted"} {
		workdir := filepath.Join(root, state)
		manifestPath := filepath.Join(workdir, "runtime-manifest.json")
		if err := os.MkdirAll(workdir, 0o750); err != nil {
			t.Fatalf("create legacy workdir: %v", err)
		}
		artifacts[manifestPath] = `{"schema_version":1,"state":"` + state + `"}`
		if _, err := catalog.DB().ExecContext(ctx, `INSERT INTO runtime_reservations
 (id, lease_id, task_id, run_id, bead_id, worker_id, assignment_id, generation,
 pid, process_start_marker, executable, process_group, manifest_path, workdir,
 state, retention_until, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"reservation-"+state, "lease-"+state, "task-"+state, "run-"+state,
			"bead-"+state, "worker-"+state, index+1, index+2, index+101,
			"start-"+state, "/legacy/bin/oro", index+101, manifestPath, workdir,
			state, "2026-10-25T12:00:00Z", "2026-10-23T12:00:00Z", "2026-10-24T12:00:00Z"); err != nil {
			t.Fatalf("seed legacy reservation: %v", err)
		}
		for _, managedRoot := range []struct {
			class, disposition string
		}{
			{"cache", "shared"},
			{"temp", "disposable"},
			{"evidence", "durable"},
		} {
			rootPath := filepath.Join(workdir, managedRoot.class)
			if err := os.Mkdir(rootPath, 0o750); err != nil {
				t.Fatalf("create legacy root: %v", err)
			}
			artifacts[filepath.Join(rootPath, "retained.txt")] = state + ":" + managedRoot.class
			if _, err := catalog.DB().ExecContext(ctx, `INSERT INTO runtime_reservation_roots
 (reservation_id, path, class, disposition) VALUES (?, ?, ?, ?)`,
				"reservation-"+state, rootPath, managedRoot.class, managedRoot.disposition); err != nil {
				t.Fatalf("seed legacy reservation root: %v", err)
			}
		}
	}
	for path, contents := range artifacts {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write legacy artifact: %v", err)
		}
	}

	queries := []struct {
		name, query string
	}{
		{"schema", `SELECT json_group_array(json_array(type, name, tbl_name, sql))
 FROM (SELECT * FROM sqlite_schema ORDER BY type, name)`},
		{"reservations", `SELECT json_group_array(json_array(id, lease_id, task_id, run_id,
 bead_id, worker_id, assignment_id, generation, pid, process_start_marker, executable,
 process_group, manifest_path, workdir, state, retention_until, created_at, updated_at))
 FROM (SELECT * FROM runtime_reservations ORDER BY id)`},
		{"roots", `SELECT json_group_array(json_array(reservation_id, path, class, disposition))
 FROM (SELECT * FROM runtime_reservation_roots ORDER BY path)`},
	}
	before := make(map[string]string)
	for _, query := range queries {
		var snapshot string
		if err := catalog.DB().QueryRowContext(ctx, query.query).Scan(&snapshot); err != nil {
			t.Fatalf("snapshot legacy %s: %v", query.name, err)
		}
		before[query.name] = snapshot
	}
	for range 2 {
		if err := catalog.Close(); err != nil {
			t.Fatalf("close legacy catalog: %v", err)
		}
		catalog, err = storage.OpenCatalog(ctx, path)
		if err != nil {
			t.Fatalf("reopen legacy catalog: %v", err)
		}
		var version int
		if err := catalog.DB().QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatalf("read catalog version: %v", err)
		}
		if version != 5 || storage.CatalogSchemaVersion != 5 {
			t.Fatalf("retirement changed catalog version: disk=%d binary=%d, want 5", version, storage.CatalogSchemaVersion)
		}
		for _, query := range queries {
			var after string
			if err := catalog.DB().QueryRowContext(ctx, query.query).Scan(&after); err != nil {
				t.Fatalf("read reopened %s: %v", query.name, err)
			}
			if after != before[query.name] {
				t.Errorf("catalog reopen changed %s:\nbefore: %s\nafter: %s", query.name, before[query.name], after)
			}
		}
		for path, want := range artifacts {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Errorf("legacy artifact %s changed: got %q, error %v; want %q", path, got, err, want)
			}
		}
	}
}

func TestCatalogRuntimeLeaseLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE runtime_leases (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seed stale schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close stale catalog fixture: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, path)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	db = catalog.DB()

	var foundationTable string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='providers'`).Scan(&foundationTable); err != nil {
		t.Fatalf("foundational catalog schema was not preserved: %v", err)
	}

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	lease, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	})
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if lease.ID != "lease-1" || lease.ReleasedAt != nil {
		t.Fatalf("unexpected acquired lease: %+v", lease)
	}
	if err := catalog.UpsertController(ctx, storage.Controller{
		ID: "controller-1", OwnerID: "owner-1", PID: 101, ProcessStart: now.Add(-time.Minute), ObservedEpoch: 7, HeartbeatAt: now,
		Identity: storage.ProcessIdentity{PID: 101, StartMarker: "start-controller-1", Executable: "oro", ProcessGroup: 101},
	}); err != nil {
		t.Fatalf("upsert controller: %v", err)
	}
	if err := catalog.RecordPauseEpoch(ctx, storage.PauseEpoch{Epoch: 7, State: storage.PauseRequested, CreatedAt: now}); err != nil {
		t.Fatalf("record pause epoch: %v", err)
	}
	if err := catalog.AcknowledgePauseEpoch(ctx, storage.PauseAcknowledgement{Epoch: 7, ControllerID: "controller-1", State: storage.Paused, AcknowledgedAt: now}); err != nil {
		t.Fatalf("acknowledge pause epoch: %v", err)
	}
	if err := catalog.UpsertTombstone(ctx, storage.Tombstone{ID: "tombstone-1", Namespace: "repo-a/worktree-a", Reason: "merged", State: "pending", RetiredAt: now}); err != nil {
		t.Fatalf("upsert tombstone: %v", err)
	}
	if err := catalog.SaveReconciliationCursor(ctx, storage.ReconciliationCursor{Name: "legacy-temp", Cursor: "token-99", Proof: "safe", UpdatedAt: now}); err != nil {
		t.Fatalf("save reconciliation cursor: %v", err)
	}
	if err := catalog.ReleaseLease(ctx, lease.ID); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	gotLease, err := catalog.Lease(ctx, lease.ID)
	if err != nil {
		t.Fatalf("load lease: %v", err)
	}
	if gotLease.ReleasedAt == nil || gotLease.Namespace != lease.Namespace || gotLease.OwnerID != lease.OwnerID {
		t.Fatalf("lease did not round-trip: %+v", gotLease)
	}
	if got, err := catalog.Controller(ctx, "controller-1"); err != nil || got.ObservedEpoch != 7 {
		t.Fatalf("controller did not round-trip: %+v, %v", got, err)
	}
	if got, err := catalog.PauseEpoch(ctx, 7); err != nil || got.State != storage.PauseRequested {
		t.Fatalf("pause epoch did not round-trip: %+v, %v", got, err)
	}
	if got, err := catalog.PauseAcknowledgement(ctx, 7, "controller-1"); err != nil || got.State != storage.Paused {
		t.Fatalf("pause acknowledgement did not round-trip: %+v, %v", got, err)
	}
	if got, err := catalog.Tombstone(ctx, "tombstone-1"); err != nil || got.Reason != "merged" {
		t.Fatalf("tombstone did not round-trip: %+v, %v", got, err)
	}
	if got, err := catalog.ReconciliationCursor(ctx, "legacy-temp"); err != nil || got.Cursor != "token-99" || got.Proof != "safe" {
		t.Fatalf("reconciliation cursor did not round-trip: %+v, %v", got, err)
	}

	var columns int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('runtime_leases')`).Scan(&columns); err != nil {
		t.Fatalf("inspect migrated lease schema: %v", err)
	}
	if columns < 9 {
		t.Fatalf("stale runtime_leases schema was not rebuilt, got %d columns", columns)
	}
}

func TestCatalogMigrationRebuildsIncompatibleLeaseSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open catalog db: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
CREATE TABLE runtime_leases (
 id TEXT, namespace TEXT NOT NULL, controller_id TEXT NOT NULL, owner_id TEXT NOT NULL,
 pid INTEGER NOT NULL, process_start TEXT NOT NULL, acquired_at TEXT NOT NULL,
 heartbeat_at TEXT NOT NULL, released_at TEXT
)`); err != nil {
		t.Fatalf("seed incompatible schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close incompatible catalog fixture: %v", err)
	}
	catalog, err := storage.OpenCatalog(ctx, path)
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
		ID:           "lease-1",
		Namespace:    "repo-a/worktree-a",
		ControllerID: "controller-1",
		OwnerID:      "owner-1",
		PID:          101,
		ProcessStart: now.Add(-time.Minute),
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}); err != nil {
		t.Fatalf("acquire lease after migration: %v", err)
	}
	if err := storage.MigrateCatalog(ctx, catalog.DB()); err != nil {
		t.Fatalf("repeat catalog migration: %v", err)
	}
}
