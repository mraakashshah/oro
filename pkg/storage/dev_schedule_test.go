package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/storage"
)

// TestDevCacheSweepTriggersOnSizeThreshold covers the size trigger: a cache
// that has grown past the threshold is swept even when the calendar says the
// next sweep is not due. The weekly-only trigger let the Go build cache reach
// 50 GB between sweeps, because a continuously running daemon never reached
// the startup check that evaluates the due date.
func TestDevCacheSweepTriggersOnSizeThreshold(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)

	// A cache directory holding 2 KiB of data.
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, "blob"), make([]byte, 2048), 0o600); err != nil {
		t.Fatalf("seed cache dir: %v", err)
	}

	newRequest := func(t *testing.T, threshold int64, calls *int) storage.WeeklyDevCacheSweepRequest {
		t.Helper()
		catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
		if err != nil {
			t.Fatalf("open catalog: %v", err)
		}
		t.Cleanup(func() { _ = catalog.Close() })
		// Schedule is explicitly NOT due: next sweep is a day away.
		if _, err := catalog.DB().ExecContext(ctx, `
INSERT INTO weekly_dev_cache_schedule (id, due_at, updated_at) VALUES ('weekly-dev-cache', ?, ?)`,
			now.Add(24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
			t.Fatalf("seed future schedule: %v", err)
		}
		return storage.WeeklyDevCacheSweepRequest{
			Catalog:       catalog,
			LockPath:      filepath.Join(t.TempDir(), "maintenance.lock"),
			Now:           func() time.Time { return now },
			SizeThreshold: threshold,
			Providers: []storage.CacheProvider{{
				ID:          "probe",
				Variables:   []string{"PROBE_CACHE"},
				DefaultPath: func() string { return cacheDir },
				Scope:       storage.UserScope,
				Concurrency: storage.Serialized,
				Ownership:   storage.ToolNative,
				Cleaner:     storage.CleanerDescriptor{Executable: "probe", Trusted: true},
			}},
			Run: func(_ context.Context, m storage.ProviderMaintenance) (storage.MaintenanceEvidence, error) {
				*calls++
				return storage.MaintenanceEvidence{ProviderID: m.Provider.ID, ExitCode: 0}, nil
			},
		}
	}

	t.Run("over threshold sweeps despite not being due", func(t *testing.T) {
		calls := 0
		result, err := storage.RunWeeklyDevCacheSweep(ctx, newRequest(t, 1024, &calls))
		if err != nil {
			t.Fatalf("size-triggered sweep: %v", err)
		}
		if !result.Ran || calls != 1 {
			t.Fatalf("result = %+v, calls = %d; want a sweep triggered by the 1 KiB threshold against a 2 KiB cache", result, calls)
		}
	})

	t.Run("under threshold respects the schedule", func(t *testing.T) {
		calls := 0
		result, err := storage.RunWeeklyDevCacheSweep(ctx, newRequest(t, 1<<20, &calls))
		if err != nil {
			t.Fatalf("under-threshold sweep: %v", err)
		}
		if result.Ran || calls != 0 {
			t.Fatalf("result = %+v, calls = %d; want no sweep — cache is under the 1 MiB threshold and the schedule is not due", result, calls)
		}
	})
}

func TestWeeklyDevCacheDueAndCatchup(t *testing.T) {
	ctx := context.Background()
	catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.DB().ExecContext(ctx, `
INSERT INTO weekly_dev_cache_schedule (id, due_at, updated_at)
VALUES ('weekly-dev-cache', ?, ?)
`, now.Add(-time.Hour).Format(time.RFC3339), now.Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("seed overdue schedule: %v", err)
	}

	provider := storage.CacheProvider{
		ID:          "probe",
		Variables:   []string{"PROBE_CACHE"},
		DefaultPath: t.TempDir,
		Scope:       storage.UserScope,
		Concurrency: storage.Serialized,
		Ownership:   storage.ToolNative,
		Cleaner:     storage.CleanerDescriptor{Executable: "probe", Trusted: true},
	}
	calls := 0
	runner := func(_ context.Context, maintenance storage.ProviderMaintenance) (storage.MaintenanceEvidence, error) {
		calls++
		evidence := storage.MaintenanceEvidence{ProviderID: maintenance.Provider.ID, ExitCode: 0}
		if calls == 2 {
			evidence.ExitCode = 19
			return evidence, fmt.Errorf("provider failed")
		}
		return evidence, nil
	}
	request := storage.WeeklyDevCacheSweepRequest{
		Catalog:   catalog,
		LockPath:  filepath.Join(t.TempDir(), "maintenance.lock"),
		Now:       func() time.Time { return now },
		Providers: []storage.CacheProvider{provider},
		Run:       runner,
	}

	first, err := storage.RunWeeklyDevCacheSweep(ctx, request)
	if err != nil {
		t.Fatalf("startup catch-up: %v", err)
	}
	if !first.Ran || calls != 1 {
		t.Fatalf("startup result = %+v, calls = %d; want one overdue sweep", first, calls)
	}
	if want := now.Add(storage.WeeklyDevCacheSweepInterval); !first.NextDue.Equal(want) {
		t.Fatalf("startup next due = %s, want %s", first.NextDue, want)
	}

	now = now.Add(time.Hour)
	second, err := storage.RunWeeklyDevCacheSweep(ctx, request)
	if err != nil {
		t.Fatalf("same interval sweep: %v", err)
	}
	if second.Ran || calls != 1 {
		t.Fatalf("same interval result = %+v, calls = %d; want coalesced sweep", second, calls)
	}

	now = now.Add(storage.WeeklyDevCacheSweepInterval)
	third, err := storage.RunWeeklyDevCacheSweep(ctx, request)
	if err == nil {
		t.Fatal("failed provider sweep error = nil, want provider failure")
	}
	if !third.Ran || calls != 2 {
		t.Fatalf("next interval result = %+v, calls = %d; want one failed sweep", third, calls)
	}

	var failedEvidence int
	if err := catalog.DB().QueryRowContext(ctx, `
SELECT COUNT(*) FROM evidence WHERE kind = 'weekly_dev_cache_provider' AND payload LIKE '%"exit_code":19%'
`).Scan(&failedEvidence); err != nil {
		t.Fatalf("count failed evidence: %v", err)
	}
	if failedEvidence != 1 {
		t.Fatalf("failed provider evidence count = %d, want 1", failedEvidence)
	}
}

func TestWeeklyDevCacheSweepReconcilesInterruptedRun(t *testing.T) {
	ctx := context.Background()
	fixture := newInterruptedWeeklySweepFixture(ctx, t)
	calls := 0
	request := fixture.request(t, &calls)
	request.GlobalDrain.Protocol = storage.NewPauseEpochProtocol(fixture.catalog, func(int) (storage.ProcessIdentity, error) {
		return storage.ProcessIdentity{}, errors.New("process exited")
	})

	result, err := storage.RunWeeklyDevCacheSweep(ctx, request)
	if err != nil {
		t.Fatalf("reconcile interrupted weekly sweep: %v", err)
	}
	if result.Ran || calls != 0 || !result.NextDue.Equal(fixture.nextDue) {
		t.Fatalf("result = %+v, provider calls = %d; want reconciliation without rerunning before %s", result, calls, fixture.nextDue)
	}

	var status string
	var finishedAt sql.NullString
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT status, finished_at FROM sweeps WHERE id=?`, fixture.sweepID).Scan(&status, &finishedAt); err != nil {
		t.Fatalf("load reconciled sweep: %v", err)
	}
	if status != "failed" || !finishedAt.Valid {
		t.Fatalf("reconciled sweep status/finished_at = %q/%q, want failed/non-null", status, finishedAt.String)
	}
	var evidenceCount int
	var evidencePayload string
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(payload), '') FROM evidence WHERE sweep_id=?`, fixture.sweepID).Scan(&evidenceCount, &evidencePayload); err != nil {
		t.Fatalf("load interrupted evidence: %v", err)
	}
	if evidenceCount != 1 || !strings.Contains(evidencePayload, "interrupted") {
		t.Fatalf("interrupted evidence count/payload = %d/%q, want one durable interrupted record", evidenceCount, evidencePayload)
	}
	var pauseState string
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT state FROM runtime_pause_epochs WHERE epoch=?`, fixture.pauseEpoch).Scan(&pauseState); err != nil {
		t.Fatalf("load reconciled pause epoch: %v", err)
	}
	if pauseState != string(storage.Open) {
		t.Fatalf("reconciled pause state = %q, want %q", pauseState, storage.Open)
	}
	var unrelatedStatus string
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT status FROM sweeps WHERE id=?`, fixture.unrelatedSweepID).Scan(&unrelatedStatus); err != nil {
		t.Fatalf("load unrelated sweep: %v", err)
	}
	if unrelatedStatus != "running" {
		t.Fatalf("unrelated sweep status = %q, want running", unrelatedStatus)
	}

	second, err := storage.RunWeeklyDevCacheSweep(ctx, request)
	if err != nil {
		t.Fatalf("repeat interrupted reconciliation: %v", err)
	}
	if second.Ran || calls != 0 || !second.NextDue.Equal(fixture.nextDue) {
		t.Fatalf("repeat result = %+v, provider calls = %d; want idempotent no-op", second, calls)
	}
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence WHERE sweep_id=?`, fixture.sweepID).Scan(&evidenceCount); err != nil {
		t.Fatalf("count repeated evidence: %v", err)
	}
	if evidenceCount != 1 {
		t.Fatalf("evidence count after repeat = %d, want exactly 1", evidenceCount)
	}
}

func TestWeeklyDevCacheSweepReconciliationRollsBackOnEvidenceCollision(t *testing.T) {
	ctx := context.Background()
	fixture := newInterruptedWeeklySweepFixture(ctx, t)
	collisionID := fixture.sweepID + "-evidence"
	collisionPayload := `{"owner":"unrelated"}`
	if _, err := fixture.catalog.DB().ExecContext(ctx, `
INSERT INTO evidence (id, sweep_id, kind, payload, created_at)
VALUES (?, ?, 'collision', ?, ?)`, collisionID, fixture.unrelatedSweepID, collisionPayload, fixture.now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed evidence collision: %v", err)
	}
	calls := 0
	request := fixture.request(t, &calls)
	request.GlobalDrain.Protocol = storage.NewPauseEpochProtocol(fixture.catalog, func(int) (storage.ProcessIdentity, error) {
		return storage.ProcessIdentity{}, errors.New("process exited")
	})

	if _, err := storage.RunWeeklyDevCacheSweep(ctx, request); err == nil {
		t.Fatal("evidence collision error = nil, want reconciliation failure")
	} else if !strings.Contains(err.Error(), "record interrupted weekly dev cache sweep") {
		t.Fatalf("evidence collision error = %v, want interrupted evidence failure", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls after evidence collision = %d, want 0", calls)
	}
	var status string
	var finishedAt sql.NullString
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT status, finished_at FROM sweeps WHERE id=?`, fixture.sweepID).Scan(&status, &finishedAt); err != nil {
		t.Fatalf("load collision target sweep: %v", err)
	}
	if status != "running" || finishedAt.Valid {
		t.Fatalf("collision target status/finished_at = %q/%q, want running/null rollback", status, finishedAt.String)
	}
	pause, err := fixture.catalog.PauseEpoch(ctx, fixture.pauseEpoch)
	if err != nil {
		t.Fatalf("load collision target pause: %v", err)
	}
	if pause.State != storage.PauseRequested {
		t.Fatalf("collision target pause = %q, want unchanged %q", pause.State, storage.PauseRequested)
	}
	var targetEvidence int
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence WHERE sweep_id=? AND kind='weekly_dev_cache_provider'`, fixture.sweepID).Scan(&targetEvidence); err != nil {
		t.Fatalf("count target evidence after collision: %v", err)
	}
	if targetEvidence != 0 {
		t.Fatalf("target valid evidence count = %d, want 0", targetEvidence)
	}
	var collisionSweep, gotPayload string
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT sweep_id, payload FROM evidence WHERE id=?`, collisionID).Scan(&collisionSweep, &gotPayload); err != nil {
		t.Fatalf("load unrelated collision evidence: %v", err)
	}
	if collisionSweep != fixture.unrelatedSweepID || gotPayload != collisionPayload {
		t.Fatalf("collision evidence = %q/%q, want unchanged %q/%q", collisionSweep, gotPayload, fixture.unrelatedSweepID, collisionPayload)
	}
}

func TestWeeklyDevCacheSweepReconciliationFailsClosedForLiveOwnership(t *testing.T) {
	for _, test := range []struct {
		name           string
		seedOwnership  func(context.Context, *testing.T, *interruptedWeeklySweepFixture)
		inspectProcess storage.ProcessInspector
	}{
		{
			name: "matching live controller",
			seedOwnership: func(ctx context.Context, t *testing.T, fixture *interruptedWeeklySweepFixture) {
				t.Helper()
				controller := interruptedSweepController(fixture.now)
				if err := fixture.catalog.UpsertController(ctx, controller); err != nil {
					t.Fatalf("seed live controller: %v", err)
				}
			},
			inspectProcess: func(pid int) (storage.ProcessIdentity, error) {
				controller := interruptedSweepController(time.Time{})
				if pid != controller.PID {
					return storage.ProcessIdentity{}, errors.New("unexpected process")
				}
				return controller.Identity, nil
			},
		},
		{
			name: "active lease",
			seedOwnership: func(ctx context.Context, t *testing.T, fixture *interruptedWeeklySweepFixture) {
				t.Helper()
				if _, err := fixture.catalog.AcquireLease(ctx, storage.LeaseRequest{
					ID: "active", Namespace: "repo/worktree", ControllerID: "controller", OwnerID: "owner", PID: 202,
					ProcessStart: fixture.now.Add(-time.Hour), AcquiredAt: fixture.now, HeartbeatAt: fixture.now,
				}); err != nil {
					t.Fatalf("seed active lease: %v", err)
				}
			},
			inspectProcess: func(int) (storage.ProcessIdentity, error) {
				return storage.ProcessIdentity{}, errors.New("process exited")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newInterruptedWeeklySweepFixture(ctx, t)
			test.seedOwnership(ctx, t, fixture)
			calls := 0
			request := fixture.request(t, &calls)
			request.GlobalDrain.Protocol = storage.NewPauseEpochProtocol(fixture.catalog, test.inspectProcess)

			result, err := storage.RunWeeklyDevCacheSweep(ctx, request)
			if err != nil {
				t.Fatalf("run ownership-protected reconciliation: %v", err)
			}
			if result.Ran || calls != 0 || !result.NextDue.Equal(fixture.nextDue) {
				t.Fatalf("result = %+v, calls = %d; want schedule-preserving no-op", result, calls)
			}
			var status string
			var finishedAt sql.NullString
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT status, finished_at FROM sweeps WHERE id=?`, fixture.sweepID).Scan(&status, &finishedAt); err != nil {
				t.Fatalf("load protected sweep: %v", err)
			}
			if status != "running" || finishedAt.Valid {
				t.Fatalf("protected sweep status/finished_at = %q/%q, want running/null", status, finishedAt.String)
			}
			var pauseState string
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT state FROM runtime_pause_epochs WHERE epoch=?`, fixture.pauseEpoch).Scan(&pauseState); err != nil {
				t.Fatalf("load protected pause: %v", err)
			}
			if pauseState != string(storage.PauseRequested) {
				t.Fatalf("protected pause state = %q, want %q", pauseState, storage.PauseRequested)
			}
			var evidenceCount int
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence WHERE sweep_id=?`, fixture.sweepID).Scan(&evidenceCount); err != nil {
				t.Fatalf("count protected evidence: %v", err)
			}
			if evidenceCount != 0 {
				t.Fatalf("protected evidence count = %d, want 0", evidenceCount)
			}
		})
	}
}

func TestWeeklyDevCacheSweepReconciliationLeavesLaterPauseEpochUnchanged(t *testing.T) {
	ctx := context.Background()
	fixture := newInterruptedWeeklySweepFixture(ctx, t)
	const laterEpoch = 8
	if err := fixture.catalog.RecordPauseEpoch(ctx, storage.PauseEpoch{
		Epoch: laterEpoch, State: storage.PauseRequested, CreatedAt: fixture.now,
	}); err != nil {
		t.Fatalf("seed later operator pause: %v", err)
	}
	calls := 0
	request := fixture.request(t, &calls)
	request.GlobalDrain.Protocol = storage.NewPauseEpochProtocol(fixture.catalog, func(int) (storage.ProcessIdentity, error) {
		return storage.ProcessIdentity{}, errors.New("process exited")
	})

	result, err := storage.RunWeeklyDevCacheSweep(ctx, request)
	if err != nil {
		t.Fatalf("reconcile with later operator pause: %v", err)
	}
	if result.Ran || calls != 0 || !result.NextDue.Equal(fixture.nextDue) {
		t.Fatalf("result = %+v, calls = %d; want schedule-preserving reconciliation", result, calls)
	}
	operatorPause, err := fixture.catalog.PauseEpoch(ctx, laterEpoch)
	if err != nil {
		t.Fatalf("load later operator pause: %v", err)
	}
	if operatorPause.State != storage.PauseRequested {
		t.Fatalf("later operator pause state = %q, want unchanged %q", operatorPause.State, storage.PauseRequested)
	}
	cleanupPause, err := fixture.catalog.PauseEpoch(ctx, fixture.pauseEpoch)
	if err != nil {
		t.Fatalf("load cleanup pause: %v", err)
	}
	if cleanupPause.State != storage.Open {
		t.Fatalf("cleanup pause state = %q, want %q", cleanupPause.State, storage.Open)
	}
	var status string
	if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT status FROM sweeps WHERE id=?`, fixture.sweepID).Scan(&status); err != nil {
		t.Fatalf("load correlated interrupted sweep: %v", err)
	}
	if status != "failed" {
		t.Fatalf("correlated interrupted sweep status = %q, want failed", status)
	}
}

func TestWeeklyDevCacheSweepReconciliationRequiresUniquePauseCorrelation(t *testing.T) {
	for _, test := range []struct {
		name string
		seed func(context.Context, *testing.T, *interruptedWeeklySweepFixture)
	}{
		{
			name: "missing correlation",
			seed: func(ctx context.Context, t *testing.T, fixture *interruptedWeeklySweepFixture) {
				t.Helper()
				if _, err := fixture.catalog.DB().ExecContext(ctx, `UPDATE runtime_pause_epochs SET created_at=? WHERE epoch=?`, fixture.now.Add(-2*time.Hour).Format(time.RFC3339Nano), fixture.pauseEpoch); err != nil {
					t.Fatalf("break cleanup pause correlation: %v", err)
				}
			},
		},
		{
			name: "ambiguous correlation",
			seed: func(ctx context.Context, t *testing.T, fixture *interruptedWeeklySweepFixture) {
				t.Helper()
				if err := fixture.catalog.RecordPauseEpoch(ctx, storage.PauseEpoch{
					Epoch: 6, State: storage.PauseRequested, CreatedAt: fixture.now.Add(-time.Hour),
				}); err != nil {
					t.Fatalf("seed ambiguous cleanup pause: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newInterruptedWeeklySweepFixture(ctx, t)
			test.seed(ctx, t, fixture)
			blockedDue := fixture.now.Add(-time.Hour)
			if _, err := fixture.catalog.DB().ExecContext(ctx, `UPDATE weekly_dev_cache_schedule SET due_at=? WHERE id='weekly-dev-cache'`, blockedDue.Format(time.RFC3339Nano)); err != nil {
				t.Fatalf("make unsafe reconciliation schedule due: %v", err)
			}
			var pauseCountBefore, sweepCountBefore, evidenceCountBefore, providerCountBefore int
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_pause_epochs`).Scan(&pauseCountBefore); err != nil {
				t.Fatalf("count pauses before unsafe reconciliation: %v", err)
			}
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sweeps`).Scan(&sweepCountBefore); err != nil {
				t.Fatalf("count sweeps before unsafe reconciliation: %v", err)
			}
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence`).Scan(&evidenceCountBefore); err != nil {
				t.Fatalf("count evidence before unsafe reconciliation: %v", err)
			}
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM providers`).Scan(&providerCountBefore); err != nil {
				t.Fatalf("count providers before unsafe reconciliation: %v", err)
			}
			calls := 0
			request := fixture.request(t, &calls)
			request.GlobalDrain.Protocol = storage.NewPauseEpochProtocol(fixture.catalog, func(int) (storage.ProcessIdentity, error) {
				return storage.ProcessIdentity{}, errors.New("process exited")
			})

			if _, err := storage.RunWeeklyDevCacheSweep(ctx, request); err == nil {
				t.Fatal("unsafe correlation error = nil, want blocked reconciliation")
			} else if !strings.Contains(err.Error(), "unsafe interrupted weekly dev cache sweep correlation") {
				t.Fatalf("unsafe correlation error = %v, want explicit unsafe-correlation failure", err)
			}
			if calls != 0 {
				t.Fatalf("provider calls after unsafe correlation = %d, want 0", calls)
			}
			var status string
			var finishedAt sql.NullString
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT status, finished_at FROM sweeps WHERE id=?`, fixture.sweepID).Scan(&status, &finishedAt); err != nil {
				t.Fatalf("load uncorrelated sweep: %v", err)
			}
			if status != "running" || finishedAt.Valid {
				t.Fatalf("uncorrelated sweep status/finished_at = %q/%q, want running/null", status, finishedAt.String)
			}
			var evidenceCount int
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence WHERE sweep_id=?`, fixture.sweepID).Scan(&evidenceCount); err != nil {
				t.Fatalf("count uncorrelated evidence: %v", err)
			}
			if evidenceCount != 0 {
				t.Fatalf("uncorrelated evidence count = %d, want 0", evidenceCount)
			}
			pause, err := fixture.catalog.PauseEpoch(ctx, fixture.pauseEpoch)
			if err != nil {
				t.Fatalf("load uncorrelated pause: %v", err)
			}
			if pause.State != storage.PauseRequested {
				t.Fatalf("uncorrelated pause state = %q, want unchanged %q", pause.State, storage.PauseRequested)
			}
			var gotDue string
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT due_at FROM weekly_dev_cache_schedule WHERE id='weekly-dev-cache'`).Scan(&gotDue); err != nil {
				t.Fatalf("load due time after unsafe correlation: %v", err)
			}
			if gotDue != blockedDue.Format(time.RFC3339Nano) {
				t.Fatalf("due time after unsafe correlation = %q, want unchanged %q", gotDue, blockedDue.Format(time.RFC3339Nano))
			}
			var pauseCountAfter, sweepCountAfter, evidenceCountAfter, providerCountAfter int
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_pause_epochs`).Scan(&pauseCountAfter); err != nil {
				t.Fatalf("count pauses after unsafe reconciliation: %v", err)
			}
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sweeps`).Scan(&sweepCountAfter); err != nil {
				t.Fatalf("count sweeps after unsafe reconciliation: %v", err)
			}
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence`).Scan(&evidenceCountAfter); err != nil {
				t.Fatalf("count evidence after unsafe reconciliation: %v", err)
			}
			if err := fixture.catalog.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM providers`).Scan(&providerCountAfter); err != nil {
				t.Fatalf("count providers after unsafe reconciliation: %v", err)
			}
			if pauseCountAfter != pauseCountBefore || sweepCountAfter != sweepCountBefore || evidenceCountAfter != evidenceCountBefore || providerCountAfter != providerCountBefore {
				t.Fatalf("catalog counts after unsafe correlation = pause:%d sweep:%d evidence:%d provider:%d, want unchanged %d/%d/%d/%d", pauseCountAfter, sweepCountAfter, evidenceCountAfter, providerCountAfter, pauseCountBefore, sweepCountBefore, evidenceCountBefore, providerCountBefore)
			}
		})
	}
}

type interruptedWeeklySweepFixture struct {
	catalog          *storage.Catalog
	now, nextDue     time.Time
	pauseEpoch       int64
	sweepID          string
	unrelatedSweepID string
	provider         storage.CacheProvider
}

func newInterruptedWeeklySweepFixture(ctx context.Context, t *testing.T) *interruptedWeeklySweepFixture {
	t.Helper()
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	nextDue := now.Add(storage.WeeklyDevCacheSweepInterval)
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	catalog, err := storage.OpenCatalog(ctx, path)
	if err != nil {
		t.Fatalf("open seed catalog: %v", err)
	}
	provider := storage.CacheProvider{
		ID: "probe", Variables: []string{"PROBE_CACHE"}, DefaultPath: t.TempDir,
		Scope: storage.UserScope, Concurrency: storage.Serialized, Ownership: storage.ToolNative,
		Cleaner: storage.CleanerDescriptor{Executable: "probe", Trusted: true},
	}
	sweepID := "weekly-dev-cache-20260806T110000.000000000Z-probe"
	unrelatedSweepID := "manual-sweep"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO weekly_dev_cache_schedule (id, due_at, updated_at) VALUES ('weekly-dev-cache', ?, ?)`, []any{nextDue.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)}},
		{`INSERT INTO providers (id, created_at, updated_at) VALUES (?, ?, ?)`, []any{provider.ID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)}},
		{`INSERT INTO sweeps (id, provider_id, started_at, status) VALUES (?, ?, ?, 'running')`, []any{sweepID, provider.ID, now.Add(-time.Hour).Format(time.RFC3339Nano)}},
		{`INSERT INTO sweeps (id, provider_id, started_at, status) VALUES (?, ?, ?, 'running')`, []any{unrelatedSweepID, provider.ID, now.Add(-time.Hour).Format(time.RFC3339Nano)}},
		{`INSERT INTO runtime_pause_epochs (epoch, state, created_at) VALUES (7, 'pause_requested', ?)`, []any{now.Add(-time.Hour).Format(time.RFC3339Nano)}},
	}
	for _, statement := range statements {
		if _, err := catalog.DB().ExecContext(ctx, statement.query, statement.args...); err != nil {
			_ = catalog.Close()
			t.Fatalf("seed interrupted sweep: %v", err)
		}
	}
	if err := catalog.Close(); err != nil {
		t.Fatalf("close seed catalog: %v", err)
	}
	catalog, err = storage.OpenCatalog(ctx, path)
	if err != nil {
		t.Fatalf("reopen interrupted catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	return &interruptedWeeklySweepFixture{
		catalog: catalog, now: now, nextDue: nextDue, pauseEpoch: 7,
		sweepID: sweepID, unrelatedSweepID: unrelatedSweepID, provider: provider,
	}
}

func (fixture *interruptedWeeklySweepFixture) request(t *testing.T, calls *int) storage.WeeklyDevCacheSweepRequest {
	t.Helper()
	return storage.WeeklyDevCacheSweepRequest{
		Catalog: fixture.catalog, LockPath: filepath.Join(t.TempDir(), "maintenance.lock"),
		Now: func() time.Time { return fixture.now }, Providers: []storage.CacheProvider{fixture.provider}, SizeThreshold: -1,
		Run: func(context.Context, storage.ProviderMaintenance) (storage.MaintenanceEvidence, error) {
			*calls++
			return storage.MaintenanceEvidence{ProviderID: fixture.provider.ID}, nil
		},
	}
}

func interruptedSweepController(now time.Time) storage.Controller {
	return storage.Controller{
		ID: "controller", OwnerID: "owner", PID: 101, ProcessStart: now.Add(-time.Hour), HeartbeatAt: now,
		Identity:      storage.ProcessIdentity{PID: 101, StartMarker: "start-controller", Executable: "oro", ProcessGroup: 101},
		ObservedEpoch: 7,
	}
}
