package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// WeeklyDevCacheSweepInterval is the cadence for developer-tool cache maintenance.
const WeeklyDevCacheSweepInterval = 7 * 24 * time.Hour

// DevCacheSweepSizeThreshold is the size at which a developer-tool cache is
// swept regardless of the calendar. The weekly interval alone is not a
// sufficient trigger: it is only evaluated at `oro start`, so a continuously
// running daemon never reaches the check, and a factory building many
// worktrees fills the Go build cache far faster than weekly. Observed
// 2026-07-29: 50 GB of GOCACHE accumulated in five days across 57 worktrees,
// every entry younger than Go's own five-day trim horizon.
const DevCacheSweepSizeThreshold int64 = 24 << 30 // 24 GiB

const weeklyDevCacheScheduleID = "weekly-dev-cache"

// DevCacheMaintenanceRunner runs one provider maintenance operation.
type DevCacheMaintenanceRunner func(context.Context, ProviderMaintenance) (MaintenanceEvidence, error)

// WeeklyDevCacheSweepRequest supplies the dependencies for one scheduled sweep.
type WeeklyDevCacheSweepRequest struct {
	Catalog     *Catalog
	LockPath    string
	Now         func() time.Time
	Interval    time.Duration
	Providers   []CacheProvider
	Run         DevCacheMaintenanceRunner
	GlobalDrain GlobalDrainRequest
	// SizeThreshold forces a sweep once any provider's cache reaches this many
	// bytes, even when the scheduled sweep is not yet due. Zero selects
	// DevCacheSweepSizeThreshold; negative disables the size trigger.
	SizeThreshold int64
}

// WeeklyDevCacheSweepResult describes whether a due sweep ran and when it is next due.
type WeeklyDevCacheSweepResult struct {
	Ran     bool
	NextDue time.Time
}

// RunWeeklyDevCacheSweep catches up one overdue weekly developer-cache sweep.
// It serializes every trigger with the host-wide maintenance lock and advances
// the persisted due time before returning provider failures, preventing repeated
// sweeps for the same due interval.
func RunWeeklyDevCacheSweep(ctx context.Context, request WeeklyDevCacheSweepRequest) (WeeklyDevCacheSweepResult, error) {
	if err := ctx.Err(); err != nil {
		return WeeklyDevCacheSweepResult{}, fmt.Errorf("weekly dev cache sweep context: %w", err)
	}
	if request.Catalog == nil {
		return WeeklyDevCacheSweepResult{}, fmt.Errorf("weekly dev cache sweep catalog is nil")
	}
	if strings.TrimSpace(request.LockPath) == "" {
		return WeeklyDevCacheSweepResult{}, fmt.Errorf("weekly dev cache sweep lock path is empty")
	}

	lock, err := AcquireMaintenanceLock(ctx, request.LockPath)
	if err != nil {
		if errors.Is(err, ErrMaintenanceBusy) {
			return WeeklyDevCacheSweepResult{}, nil
		}
		return WeeklyDevCacheSweepResult{}, fmt.Errorf("acquire weekly dev cache sweep lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	now := weeklySweepNow(request.Now)
	interval := weeklySweepInterval(request.Interval)
	due, err := loadWeeklyDevCacheDue(ctx, request.Catalog, now)
	if err != nil {
		return WeeklyDevCacheSweepResult{}, err
	}
	if now.Before(due) && !devCacheOverSizeThreshold(request.Providers, request.SizeThreshold) {
		return WeeklyDevCacheSweepResult{NextDue: due}, nil
	}
	if overdueDevCleanupRequiresGlobalDrain(now, due) {
		if err := request.GlobalDrain.wait(ctx, request.Catalog, now); err != nil {
			return WeeklyDevCacheSweepResult{}, err
		}
	}

	nextDue := now.Add(interval)
	if err := saveWeeklyDevCacheDue(ctx, request.Catalog, nextDue, now); err != nil {
		return WeeklyDevCacheSweepResult{}, err
	}

	runner := request.Run
	if runner == nil {
		runner = RunProviderMaintenance
	}
	var runErrs []error
	for _, provider := range request.Providers {
		if provider.Concurrency == NoMaintenance || !provider.Cleaner.present() {
			continue
		}
		if err := recordWeeklyDevCacheProvider(ctx, request.Catalog, provider, now, runner); err != nil {
			runErrs = append(runErrs, err)
		}
	}
	return WeeklyDevCacheSweepResult{Ran: true, NextDue: nextDue}, errors.Join(runErrs...)
}

// devCacheOverSizeThreshold reports whether any provider's cache has reached
// the size at which maintenance is warranted regardless of the schedule. A
// provider whose path cannot be measured — most often because the tool has
// never run and the directory is absent — is treated as under threshold, so a
// measurement failure can never force a sweep.
func devCacheOverSizeThreshold(providers []CacheProvider, threshold int64) bool {
	if threshold < 0 {
		return false
	}
	if threshold == 0 {
		threshold = DevCacheSweepSizeThreshold
	}
	for _, provider := range providers {
		if provider.DefaultPath == nil || !provider.Cleaner.present() {
			continue
		}
		bytes, err := scratchPathBytes(provider.DefaultPath())
		if err != nil {
			continue
		}
		if bytes >= threshold {
			return true
		}
	}
	return false
}

func weeklySweepNow(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}

func weeklySweepInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return WeeklyDevCacheSweepInterval
	}
	return interval
}

func loadWeeklyDevCacheDue(ctx context.Context, catalog *Catalog, now time.Time) (time.Time, error) {
	var dueAt string
	err := catalog.db.QueryRowContext(ctx, `SELECT due_at FROM weekly_dev_cache_schedule WHERE id=?`, weeklyDevCacheScheduleID).Scan(&dueAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return now, nil
		}
		return time.Time{}, fmt.Errorf("load weekly dev cache due time: %w", err)
	}
	due, err := parseTime(dueAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse weekly dev cache due time: %w", err)
	}
	return due, nil
}

func saveWeeklyDevCacheDue(ctx context.Context, catalog *Catalog, due, updated time.Time) error {
	_, err := catalog.db.ExecContext(ctx, `
INSERT INTO weekly_dev_cache_schedule (id, due_at, updated_at) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET due_at=excluded.due_at, updated_at=excluded.updated_at`,
		weeklyDevCacheScheduleID, formatTime(due), formatTime(updated))
	if err != nil {
		return fmt.Errorf("save weekly dev cache due time: %w", err)
	}
	return nil
}

func recordWeeklyDevCacheProvider(ctx context.Context, catalog *Catalog, provider CacheProvider, now time.Time, runner DevCacheMaintenanceRunner) error {
	if _, err := catalog.db.ExecContext(ctx, `
INSERT INTO providers (id, created_at, updated_at) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at`, provider.ID, formatTime(now), formatTime(now)); err != nil {
		return fmt.Errorf("record weekly dev cache provider %q: %w", provider.ID, err)
	}

	sweepID := weeklySweepID(now, provider.ID)
	if _, err := catalog.db.ExecContext(ctx, `INSERT INTO sweeps (id, provider_id, started_at, status) VALUES (?, ?, ?, 'running')`, sweepID, provider.ID, formatTime(now)); err != nil {
		return fmt.Errorf("start weekly dev cache provider %q: %w", provider.ID, err)
	}
	evidence, runErr := runner(ctx, ProviderMaintenance{Provider: provider})
	payload, marshalErr := json.Marshal(evidence)
	if marshalErr != nil {
		return fmt.Errorf("encode weekly dev cache provider %q evidence: %w", provider.ID, marshalErr)
	}
	status := "completed"
	if runErr != nil {
		status = "failed"
	}
	if _, err := catalog.db.ExecContext(ctx, `UPDATE sweeps SET finished_at=?, status=? WHERE id=?`, formatTime(now), status, sweepID); err != nil {
		return fmt.Errorf("finish weekly dev cache provider %q: %w", provider.ID, err)
	}
	if _, err := catalog.db.ExecContext(ctx, `INSERT INTO evidence (id, sweep_id, kind, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		sweepID+"-evidence", sweepID, "weekly_dev_cache_provider", string(payload), formatTime(now)); err != nil {
		return fmt.Errorf("record weekly dev cache provider %q evidence: %w", provider.ID, err)
	}
	if runErr != nil {
		return fmt.Errorf("run weekly dev cache provider %q: %w", provider.ID, runErr)
	}
	return nil
}

func weeklySweepID(now time.Time, providerID string) string {
	return "weekly-dev-cache-" + now.Format("20060102T150405.000000000Z") + "-" + filepath.Base(providerID)
}
