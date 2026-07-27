package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/storage"
)

func TestOverdueDevCleanupRequiresGlobalDrain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	catalog, err := storage.OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	if _, err := catalog.DB().ExecContext(ctx, `
INSERT INTO weekly_dev_cache_schedule (id, due_at, updated_at)
VALUES ('weekly-dev-cache', ?, ?)
`, now.Add(-25*time.Hour).Format(time.RFC3339), now.Add(-25*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("seed overdue schedule: %v", err)
	}

	controllers := []storage.Controller{
		devDrainController("one", 101, now),
		devDrainController("two", 202, now),
		devDrainController("stale", 303, now),
	}
	for _, controller := range controllers {
		if err := catalog.UpsertController(ctx, controller); err != nil {
			t.Fatalf("upsert controller %s: %v", controller.ID, err)
		}
	}
	for _, controller := range controllers[:2] {
		if _, err := catalog.AcquireLease(ctx, storage.LeaseRequest{
			ID:           storage.LeaseID("lease-" + controller.ID),
			Namespace:    "namespace-" + controller.ID,
			ControllerID: controller.ID,
			OwnerID:      controller.OwnerID,
			PID:          controller.PID,
			ProcessStart: controller.ProcessStart,
			AcquiredAt:   now,
			HeartbeatAt:  now,
		}); err != nil {
			t.Fatalf("acquire %s lease: %v", controller.ID, err)
		}
	}

	inspect := func(pid int) (storage.ProcessIdentity, error) {
		for _, controller := range controllers[:2] {
			if controller.PID == pid {
				return controller.Identity, nil
			}
		}
		return storage.ProcessIdentity{}, errors.New("process exited")
	}
	protocol := storage.NewPauseEpochProtocol(catalog, inspect)
	var starts atomic.Int32
	started := make(chan struct{}, 1)
	request := storage.WeeklyDevCacheSweepRequest{
		Catalog:   catalog,
		LockPath:  filepath.Join(t.TempDir(), "maintenance.lock"),
		Now:       func() time.Time { return now },
		Providers: []storage.CacheProvider{devDrainProvider(t)},
		GlobalDrain: storage.GlobalDrainRequest{
			Protocol:     protocol,
			PollInterval: time.Millisecond,
		},
		Run: func(_ context.Context, _ storage.ProviderMaintenance) (storage.MaintenanceEvidence, error) {
			starts.Add(1)
			started <- struct{}{}
			return storage.MaintenanceEvidence{}, nil
		},
	}

	resultCh := make(chan error, 1)
	go func() {
		_, runErr := storage.RunWeeklyDevCacheSweep(ctx, request)
		resultCh <- runErr
	}()

	epoch := waitForDevDrainEpoch(t, ctx, catalog)
	one := devDrainRuntimeController(t, catalog, controllers[0], func(context.Context) error {
		return catalog.ReleaseLease(ctx, "lease-one")
	})
	if err := one.Observe(ctx, now); err != nil {
		t.Fatalf("observe first controller: %v", err)
	}
	if one.Admit() {
		t.Fatal("first controller admitted a new start during global pause")
	}
	newController := devDrainRuntimeController(t, catalog, controllers[0], func(context.Context) error { return nil })
	if newController.Admit() {
		t.Fatal("new controller admitted a new start during global pause")
	}
	if _, err := catalog.PauseAcknowledgement(ctx, epoch, controllers[2].ID); err == nil {
		t.Fatal("stale controller unexpectedly acknowledged pause")
	}
	select {
	case <-started:
		t.Fatal("provider maintenance started before every live controller acknowledged and leases drained")
	case <-time.After(25 * time.Millisecond):
	}

	two := devDrainRuntimeController(t, catalog, controllers[1], func(context.Context) error {
		return catalog.ReleaseLease(ctx, "lease-two")
	})
	if err := two.Observe(ctx, now); err != nil {
		t.Fatalf("observe second controller: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider maintenance did not start after global drain")
	}
	if err := <-resultCh; err != nil {
		t.Fatalf("run overdue sweep: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("provider starts = %d, want 1", starts.Load())
	}
}

func devDrainController(id string, pid int, now time.Time) storage.Controller {
	return storage.Controller{
		ID:           id,
		OwnerID:      "owner-" + id,
		PID:          pid,
		ProcessStart: now.Add(-time.Minute),
		HeartbeatAt:  now,
		Identity: storage.ProcessIdentity{
			PID:          pid,
			StartMarker:  "start-" + id,
			Executable:   "oro",
			ProcessGroup: pid,
		},
	}
}

func devDrainRuntimeController(t *testing.T, catalog *storage.Catalog, controller storage.Controller, drain func(context.Context) error) *storage.Controller {
	t.Helper()
	runtime, err := storage.NewController(storage.ControllerConfig{
		Catalog: catalog,
		ID:      controller.ID,
		Drain:   drain,
	})
	if err != nil {
		t.Fatalf("new controller %s: %v", controller.ID, err)
	}
	return runtime
}

func devDrainProvider(t *testing.T) storage.CacheProvider {
	t.Helper()
	return storage.CacheProvider{
		ID:          "probe",
		Variables:   []string{"PROBE_CACHE"},
		DefaultPath: t.TempDir,
		Scope:       storage.UserScope,
		Concurrency: storage.Serialized,
		Ownership:   storage.ToolNative,
		Cleaner:     storage.CleanerDescriptor{Executable: "probe", Trusted: true},
	}
}

func waitForDevDrainEpoch(t *testing.T, ctx context.Context, catalog *storage.Catalog) int64 {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var epoch int64
		err := catalog.DB().QueryRowContext(ctx, `SELECT epoch FROM runtime_pause_epochs ORDER BY epoch DESC LIMIT 1`).Scan(&epoch)
		if err == nil {
			return epoch
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("global pause epoch was not requested")
	return 0
}
