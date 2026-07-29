package storage_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
