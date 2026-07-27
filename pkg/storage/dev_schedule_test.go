package storage_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/storage"
)

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
