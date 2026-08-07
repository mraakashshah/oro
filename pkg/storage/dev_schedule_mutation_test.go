//nolint:testpackage // mutation owners exercise package-private transaction boundaries.
package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWeeklyDevCacheSweepMutationNoSweepReleasesTransaction(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	if err := reconcileInterruptedWeeklyDevCacheSweep(ctx, catalog, nil, time.Now().UTC()); err != nil {
		t.Fatalf("reconcile empty catalog: %v", err)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if _, err := catalog.DB().ExecContext(writeCtx, `
INSERT INTO providers (id, created_at, updated_at) VALUES ('probe', ?, ?)`,
		formatTime(time.Now().UTC()), formatTime(time.Now().UTC())); err != nil {
		t.Fatalf("write after no-sweep reconciliation: %v", err)
	}
}

func TestWeeklyDevCacheSweepMutationRejectsMissingSweepCAS(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	tx, err := catalog.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	err = failInterruptedWeeklyDevCacheSweeps(ctx, tx, []interruptedWeeklySweep{{
		id: "missing-sweep", providerID: "missing-provider",
	}}, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "fail interrupted weekly dev cache sweep missing-sweep: changed 0 rows") {
		t.Fatalf("missing sweep CAS error = %v, want changed-zero rejection", err)
	}
}

func TestWeeklyDevCacheSweepMutationRejectsMissingPauseCAS(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	tx, err := catalog.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	err = openInterruptedWeeklyDevCachePauses(ctx, tx, []interruptedWeeklySweep{{pauseEpoch: 404}})
	if err == nil || !strings.Contains(err.Error(), "open interrupted weekly dev cache pause 404: changed 0 rows") {
		t.Fatalf("missing pause CAS error = %v, want changed-zero rejection", err)
	}
}

func TestWeeklyDevCacheSweepMutationReportsControllerQueryFailure(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	tx, err := catalog.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DROP TABLE runtime_controllers`); err != nil {
		t.Fatalf("remove controller table fixture: %v", err)
	}

	live, err := interruptedSweepHasLiveController(ctx, tx, func(int) (ProcessIdentity, error) {
		return ProcessIdentity{}, nil
	})
	if live || err == nil || !strings.Contains(err.Error(), "list interrupted weekly dev cache controllers") {
		t.Fatalf("controller query result = %t/%v, want false/wrapped query error", live, err)
	}
}

func TestWeeklyDevCacheSweepMutationReportsSweepQueryFailure(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	tx, err := catalog.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DROP TABLE sweeps`); err != nil {
		t.Fatalf("remove sweeps table fixture: %v", err)
	}

	sweeps, err := interruptedWeeklyDevCacheSweeps(ctx, tx)
	if sweeps != nil || err == nil || !strings.Contains(err.Error(), "list interrupted weekly dev cache sweeps") {
		t.Fatalf("interrupted sweep query result = %#v/%v, want nil/wrapped query error", sweeps, err)
	}
}

func TestWeeklyDevCacheSweepMutationRejectsInvalidRequest(t *testing.T) {
	result, err := RunWeeklyDevCacheSweep(context.Background(), WeeklyDevCacheSweepRequest{
		LockPath: filepath.Join(t.TempDir(), "maintenance.lock"),
	})
	if err == nil || !strings.Contains(err.Error(), "weekly dev cache sweep catalog is nil") {
		t.Fatalf("invalid request result = %+v/%v, want nil-catalog validation error", result, err)
	}
}

func TestWeeklyDevCacheSweepMutationUsesDefaultProviderRunner(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	provider := CacheProvider{
		ID: "default-runner", Variables: []string{"DEFAULT_RUNNER_CACHE"},
		DefaultPath: t.TempDir, Scope: UserScope, Concurrency: Serialized, Ownership: ToolNative,
		Cleaner: CleanerDescriptor{Executable: "/usr/bin/true", Trusted: true},
	}

	if err := runWeeklyDevCacheProviders(ctx, catalog, []CacheProvider{provider}, now, nil); err != nil {
		t.Fatalf("run provider with default runner: %v", err)
	}
	var status string
	if err := catalog.DB().QueryRowContext(ctx, `SELECT status FROM sweeps WHERE provider_id=?`, provider.ID).Scan(&status); err != nil {
		t.Fatalf("load default-runner sweep: %v", err)
	}
	if status != "completed" {
		t.Fatalf("default-runner sweep status = %q, want completed", status)
	}
}

type mutationInterruptedSweepFixture struct {
	catalog    *Catalog
	now        time.Time
	sweepID    string
	pauseEpoch int64
}

func cleanupMutationCatalogBounded(t *testing.T, catalog *Catalog) {
	t.Helper()
	t.Cleanup(func() {
		closed := make(chan error, 1)
		go func() { closed <- catalog.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Errorf("close mutation catalog: %v", err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Error("close mutation catalog exceeded 250ms bound")
		}
	})
}

func newMutationInterruptedSweepFixture(t *testing.T) mutationInterruptedSweepFixture {
	t.Helper()
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open interrupted-sweep catalog: %v", err)
	}
	cleanupMutationCatalogBounded(t, catalog)
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	startedAt := now.Add(-time.Hour)
	const sweepID = "weekly-dev-cache-mutation-probe"
	const pauseEpoch = 7
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO providers (id, created_at, updated_at) VALUES ('probe', ?, ?)`, []any{formatTime(now), formatTime(now)}},
		{`INSERT INTO sweeps (id, provider_id, started_at, status) VALUES (?, 'probe', ?, 'running')`, []any{sweepID, formatTime(startedAt)}},
		{`INSERT INTO runtime_pause_epochs (epoch, state, created_at) VALUES (?, ?, ?)`, []any{pauseEpoch, PauseRequested, formatTime(startedAt)}},
	}
	for _, statement := range statements {
		if _, err := catalog.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed interrupted-sweep catalog: %v", err)
		}
	}
	return mutationInterruptedSweepFixture{catalog: catalog, now: now, sweepID: sweepID, pauseEpoch: pauseEpoch}
}

func (fixture mutationInterruptedSweepFixture) requireRunning(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var status string
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT status FROM sweeps WHERE id=?`, fixture.sweepID).Scan(&status); err != nil {
		t.Fatalf("load interrupted sweep: %v", err)
	}
	if status != "running" {
		t.Fatalf("interrupted sweep status = %q, want running", status)
	}
}

func TestWeeklyDevCacheSweepMutationReconciliationBoundaries(t *testing.T) {
	t.Run("uses default protocol", func(t *testing.T) {
		fixture := newMutationInterruptedSweepFixture(t)
		if err := reconcileInterruptedWeeklyDevCacheSweep(context.Background(), fixture.catalog, nil, fixture.now); err != nil {
			t.Fatalf("reconcile with default protocol: %v", err)
		}
		var status string
		if err := fixture.catalog.DB().QueryRow(`SELECT status FROM sweeps WHERE id=?`, fixture.sweepID).Scan(&status); err != nil {
			t.Fatalf("load reconciled sweep: %v", err)
		}
		if status != "failed" {
			t.Fatalf("reconciled sweep status = %q, want failed", status)
		}
	})

	for _, ownership := range []string{"live controller", "active lease"} {
		t.Run("preserves "+ownership, func(t *testing.T) {
			ctx := context.Background()
			fixture := newMutationInterruptedSweepFixture(t)
			protocol := NewPauseEpochProtocol(fixture.catalog, func(int) (ProcessIdentity, error) {
				return ProcessIdentity{}, context.Canceled
			})
			switch ownership {
			case "live controller":
				controller := Controller{
					ID: "live", OwnerID: "owner", PID: 202, ProcessStart: fixture.now.Add(-time.Hour), HeartbeatAt: fixture.now,
					Identity: ProcessIdentity{PID: 202, StartMarker: "live-start", Executable: "oro", ProcessGroup: 202},
				}
				if err := fixture.catalog.UpsertController(ctx, controller); err != nil {
					t.Fatalf("seed live controller: %v", err)
				}
				protocol = NewPauseEpochProtocol(fixture.catalog, func(pid int) (ProcessIdentity, error) {
					if pid != controller.PID {
						return ProcessIdentity{}, context.Canceled
					}
					return controller.Identity, nil
				})
			case "active lease":
				if _, err := fixture.catalog.DB().ExecContext(ctx, `
INSERT INTO runtime_leases (id, namespace, controller_id, owner_id, pid, process_start, acquired_at, heartbeat_at)
VALUES ('active', 'repo/worktree', 'controller', 'owner', 303, ?, ?, ?)`,
					formatTime(fixture.now.Add(-time.Hour)), formatTime(fixture.now), formatTime(fixture.now)); err != nil {
					t.Fatalf("seed active lease: %v", err)
				}
			}
			if err := reconcileInterruptedWeeklyDevCacheSweep(ctx, fixture.catalog, protocol, fixture.now); err != nil {
				t.Fatalf("reconcile with %s: %v", ownership, err)
			}
			fixture.requireRunning(t)
		})
	}

	for _, failure := range []string{
		"begin transaction", "list sweeps", "list controllers", "count leases",
		"fail sweep", "open pause", "commit transaction",
	} {
		t.Run("propagates "+failure, func(t *testing.T) {
			ctx := context.Background()
			var catalog *Catalog
			var now time.Time
			var want string
			//nolint:nestif // each branch injects a distinct package-private database failure seam.
			if failure == "begin transaction" || failure == "list sweeps" {
				var err error
				catalog, err = OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
				if err != nil {
					t.Fatalf("open catalog: %v", err)
				}
				cleanupMutationCatalogBounded(t, catalog)
				now = time.Now().UTC()
				if failure == "begin transaction" {
					want = "begin interrupted weekly dev cache reconciliation"
					if err := catalog.Close(); err != nil {
						t.Fatalf("close catalog fixture: %v", err)
					}
				} else {
					want = "list interrupted weekly dev cache sweeps"
					if _, err := catalog.DB().ExecContext(ctx, `DROP TABLE sweeps`); err != nil {
						t.Fatalf("remove sweeps table fixture: %v", err)
					}
				}
			} else {
				fixture := newMutationInterruptedSweepFixture(t)
				catalog, now = fixture.catalog, fixture.now
				switch failure {
				case "list controllers":
					want = "list interrupted weekly dev cache controllers"
					_, _ = catalog.DB().ExecContext(ctx, `DROP TABLE runtime_controllers`)
				case "count leases":
					want = "count interrupted weekly dev cache leases"
					_, _ = catalog.DB().ExecContext(ctx, `DROP TABLE runtime_leases`)
				case "fail sweep":
					want = "record interrupted weekly dev cache sweep"
					_, _ = catalog.DB().ExecContext(ctx, `INSERT INTO evidence (id, sweep_id, kind, payload, created_at) VALUES (?, ?, 'collision', '{}', ?)`, fixture.sweepID+"-evidence", fixture.sweepID, formatTime(now))
				case "open pause":
					want = "open interrupted weekly dev cache pause"
					_, _ = catalog.DB().ExecContext(ctx, `CREATE TRIGGER mutation_ignore_pause BEFORE UPDATE ON runtime_pause_epochs BEGIN SELECT RAISE(IGNORE); END`)
				case "commit transaction":
					want = "commit interrupted weekly dev cache reconciliation"
					catalog.DB().SetMaxOpenConns(1)
					if _, err := catalog.DB().ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
						t.Fatalf("enable commit failure foreign keys: %v", err)
					}
					statements := []string{
						`CREATE TABLE mutation_commit_parent (id INTEGER PRIMARY KEY)`,
						`CREATE TABLE mutation_commit_child (parent_id INTEGER REFERENCES mutation_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
						`CREATE TRIGGER mutation_fail_commit AFTER UPDATE OF status ON sweeps BEGIN INSERT INTO mutation_commit_child(parent_id) VALUES (404); END`,
					}
					for _, statement := range statements {
						if _, err := catalog.DB().ExecContext(ctx, statement); err != nil {
							t.Fatalf("seed commit failure fixture: %v", err)
						}
					}
				}
			}
			protocol := NewPauseEpochProtocol(catalog, func(int) (ProcessIdentity, error) {
				return ProcessIdentity{}, context.Canceled
			})
			err := reconcileInterruptedWeeklyDevCacheSweep(ctx, catalog, protocol, now)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s result = %v, want error containing %q", failure, err, want)
			}
		})
	}

	// Keep the leaked-transaction mutation last so its bounded failure cannot
	// prevent any other reconciliation contract from running.
	t.Run("no sweep releases transaction", func(t *testing.T) {
		testMutationNoSweepReleasesTransaction(t)
	})
}

func testMutationNoSweepReleasesTransaction(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	cleanupMutationCatalogBounded(t, catalog)
	controller := Controller{
		ID: "probe-consolidated", OwnerID: "owner", PID: 101, ProcessStart: time.Now().Add(-time.Hour), HeartbeatAt: time.Now(),
		Identity: ProcessIdentity{PID: 101, StartMarker: "start", Executable: "oro", ProcessGroup: 101},
	}
	if err := catalog.UpsertController(ctx, controller); err != nil {
		t.Fatalf("seed no-sweep controller: %v", err)
	}
	inspections := 0
	protocol := NewPauseEpochProtocol(catalog, func(int) (ProcessIdentity, error) {
		inspections++
		return controller.Identity, nil
	})
	if err := reconcileInterruptedWeeklyDevCacheSweep(ctx, catalog, protocol, time.Now().UTC()); err != nil {
		t.Fatalf("reconcile empty catalog: %v", err)
	}
	if inspections != 0 {
		t.Fatalf("controller inspections without interrupted sweeps = %d, want 0", inspections)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if _, err := catalog.DB().ExecContext(writeCtx, `
INSERT INTO providers (id, created_at, updated_at) VALUES ('probe-consolidated', ?, ?)`,
		formatTime(time.Now().UTC()), formatTime(time.Now().UTC())); err != nil {
		t.Fatalf("write after no-sweep reconciliation: %v", err)
	}
}

func TestWeeklyDevCacheSweepMutationRunBoundaries(t *testing.T) {
	newCatalog := func(t *testing.T) *Catalog {
		t.Helper()
		catalog, err := OpenCatalog(context.Background(), filepath.Join(t.TempDir(), "catalog.db"))
		if err != nil {
			t.Fatalf("open catalog: %v", err)
		}
		t.Cleanup(func() { _ = catalog.Close() })
		return catalog
	}
	requestFor := func(t *testing.T, catalog *Catalog, now time.Time) WeeklyDevCacheSweepRequest {
		t.Helper()
		return WeeklyDevCacheSweepRequest{
			Catalog: catalog, LockPath: filepath.Join(t.TempDir(), "maintenance.lock"),
			Now: func() time.Time { return now }, SizeThreshold: -1,
		}
	}

	t.Run("reports lock failure", func(t *testing.T) {
		catalog := newCatalog(t)
		request := requestFor(t, catalog, time.Now().UTC())
		blockedParent := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(blockedParent, []byte("blocked"), 0o600); err != nil {
			t.Fatalf("seed invalid lock parent: %v", err)
		}
		request.LockPath = filepath.Join(blockedParent, "maintenance.lock")
		result, err := RunWeeklyDevCacheSweep(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "acquire weekly dev cache sweep lock") {
			t.Fatalf("lock failure result = %+v/%v, want wrapped acquisition error", result, err)
		}
	})

	t.Run("treats busy lock as no-op", func(t *testing.T) {
		ctx := context.Background()
		catalog := newCatalog(t)
		request := requestFor(t, catalog, time.Now().UTC())
		lock, err := AcquireMaintenanceLock(ctx, request.LockPath)
		if err != nil {
			t.Fatalf("hold maintenance lock: %v", err)
		}
		defer func() { _ = lock.Close() }()
		result, err := RunWeeklyDevCacheSweep(ctx, request)
		if err != nil || result != (WeeklyDevCacheSweepResult{}) {
			t.Fatalf("busy lock result = %+v/%v, want zero-result no-op", result, err)
		}
	})

	for _, failure := range []string{"reconciliation", "due load"} {
		t.Run("propagates "+failure+" failure", func(t *testing.T) {
			ctx := context.Background()
			catalog := newCatalog(t)
			request := requestFor(t, catalog, time.Now().UTC())
			var statement, want string
			if failure == "reconciliation" {
				statement, want = `DROP TABLE sweeps`, "list interrupted weekly dev cache sweeps"
			} else {
				statement, want = `DROP TABLE weekly_dev_cache_schedule`, "load weekly dev cache due time"
			}
			if _, err := catalog.DB().ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed %s failure: %v", failure, err)
			}
			result, err := RunWeeklyDevCacheSweep(ctx, request)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s result = %+v/%v, want error containing %q", failure, result, err, want)
			}
		})
	}

	t.Run("requires overdue drain", func(t *testing.T) {
		ctx := context.Background()
		catalog := newCatalog(t)
		now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
		due := now.Add(-25 * time.Hour)
		if _, err := catalog.DB().ExecContext(ctx, `
INSERT INTO weekly_dev_cache_schedule (id, due_at, updated_at) VALUES (?, ?, ?)`,
			weeklyDevCacheScheduleID, formatTime(due), formatTime(due)); err != nil {
			t.Fatalf("seed overdue schedule: %v", err)
		}
		if _, err := catalog.DB().ExecContext(ctx, `
INSERT INTO runtime_leases (id, namespace, controller_id, owner_id, pid, process_start, acquired_at, heartbeat_at)
VALUES ('active', 'repo/worktree', 'controller', 'owner', 404, ?, ?, ?)`,
			formatTime(due), formatTime(now), formatTime(now)); err != nil {
			t.Fatalf("seed active lease: %v", err)
		}
		request := requestFor(t, catalog, now)
		request.GlobalDrain.PollInterval = time.Millisecond
		drainCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		defer cancel()
		result, err := RunWeeklyDevCacheSweep(drainCtx, request)
		if err == nil || !strings.Contains(err.Error(), "wait for overdue dev cleanup drain") {
			t.Fatalf("overdue drain result = %+v/%v, want bounded drain error", result, err)
		}
	})

	t.Run("propagates schedule save failure", func(t *testing.T) {
		ctx := context.Background()
		catalog := newCatalog(t)
		now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
		due := now.Add(-time.Hour)
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO weekly_dev_cache_schedule (id, due_at, updated_at) VALUES (?, ?, ?)`, []any{weeklyDevCacheScheduleID, formatTime(due), formatTime(due)}},
			{`CREATE TRIGGER mutation_reject_schedule_update BEFORE UPDATE ON weekly_dev_cache_schedule BEGIN SELECT RAISE(FAIL, 'blocked schedule save'); END`, nil},
		}
		for _, statement := range statements {
			if _, err := catalog.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
				t.Fatalf("seed schedule save failure: %v", err)
			}
		}
		result, err := RunWeeklyDevCacheSweep(ctx, requestFor(t, catalog, now))
		if err == nil || !strings.Contains(err.Error(), "save weekly dev cache due time") {
			t.Fatalf("schedule save result = %+v/%v, want wrapped save error", result, err)
		}
	})
}

func TestWeeklyDevCacheSweepMutationSkipsIneligibleProviders(t *testing.T) {
	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	providers := []CacheProvider{
		{ID: "no-maintenance", Concurrency: NoMaintenance, Cleaner: CleanerDescriptor{Executable: "/usr/bin/true", Trusted: true}},
		{ID: "no-cleaner", Concurrency: Serialized},
	}
	calls := 0
	err = runWeeklyDevCacheProviders(ctx, catalog, providers, time.Now().UTC(), func(context.Context, ProviderMaintenance) (MaintenanceEvidence, error) {
		calls++
		return MaintenanceEvidence{}, nil
	})
	if err != nil {
		t.Fatalf("skip ineligible providers: %v", err)
	}
	var recorded int
	if err := catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM providers`).Scan(&recorded); err != nil {
		t.Fatalf("count recorded providers: %v", err)
	}
	if calls != 0 || recorded != 0 {
		t.Fatalf("ineligible provider calls/records = %d/%d, want 0/0", calls, recorded)
	}
}
