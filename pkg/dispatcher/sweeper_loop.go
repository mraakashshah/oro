package dispatcher

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// SweepConfig controls tick intervals for the sweep loop.
// Zero values are replaced by production defaults via withDefaults.
type SweepConfig struct {
	Interval5m     time.Duration // 5-min sweeper tick interval (default 5 minutes)
	Interval60m    time.Duration // 60-min sweeper tick interval (default 60 minutes)
	EventRetention time.Duration // event-log retention window for PruneEvents (default 7 days)
	SLADays        int           // review-queue SLA in days for ExpireReviewQueueSLA (default 60)
}

func (c SweepConfig) withDefaults() SweepConfig {
	if c.Interval5m == 0 {
		c.Interval5m = 5 * time.Minute
	}
	if c.Interval60m == 0 {
		c.Interval60m = 60 * time.Minute
	}
	if c.EventRetention == 0 {
		c.EventRetention = 7 * 24 * time.Hour
	}
	if c.SLADays == 0 {
		c.SLADays = 60
	}
	return c
}

// RunSweepLoop runs the dispatcher sweep ticker until ctx is cancelled.
// Sweepers run sequentially within each tick to limit concurrent SQLite writers.
//
// Every Interval5m:  PromoteClosedParentChildren, ReapDeletedParentChildren,
//
//	SweepDeletedBeadLearnings (when db != nil)
//
// Every Interval60m: PruneEvents and ExpireReviewQueueSLA (when db != nil)
func RunSweepLoop(ctx context.Context, store DeferredStore, db *sql.DB, cfg SweepConfig) {
	cfg = cfg.withDefaults()

	t5 := time.NewTicker(cfg.Interval5m)
	t60 := time.NewTicker(cfg.Interval60m)
	defer t5.Stop()
	defer t60.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t5.C:
			run5MinSweepers(ctx, store, db)
		case <-t60.C:
			run60MinSweepers(ctx, db, cfg.EventRetention, cfg.SLADays)
		}
	}
}

// runSweepLoop runs the dispatcher-owned sweep loop. Grade draining needs the
// dispatcher because it owns both the card store and the ops spawner.
func (d *Dispatcher) runSweepLoop(ctx context.Context, cfg SweepConfig) {
	cfg = cfg.withDefaults()

	t5 := time.NewTicker(cfg.Interval5m)
	t60 := time.NewTicker(cfg.Interval60m)
	defer t5.Stop()
	defer t60.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t5.C:
			d.run5MinSweepers(ctx)
		case <-t60.C:
			run60MinSweepers(ctx, d.db, cfg.EventRetention, cfg.SLADays)
		}
	}
}

func (d *Dispatcher) run5MinSweepers(ctx context.Context) {
	run5MinSweepers(ctx, d.beads, d.db)
	if err := d.drainGradeProposals(ctx); err != nil {
		slog.WarnContext(ctx, "sweep: drain grade proposals failed", "err", err)
	}
}

func run5MinSweepers(ctx context.Context, store DeferredStore, db *sql.DB) {
	if err := PromoteClosedParentChildren(ctx, store); err != nil {
		slog.WarnContext(ctx, "sweep: PromoteClosedParentChildren failed", "err", err)
	}
	if err := ReapDeletedParentChildren(ctx, store); err != nil {
		slog.WarnContext(ctx, "sweep: ReapDeletedParentChildren failed", "err", err)
	}
	if db != nil {
		if _, err := SweepDeletedBeadLearnings(ctx, db); err != nil {
			slog.WarnContext(ctx, "sweep: SweepDeletedBeadLearnings failed", "err", err)
		}
	}
}

func run60MinSweepers(ctx context.Context, db *sql.DB, eventRetention time.Duration, slaDays int) {
	if db == nil {
		return
	}
	if _, err := PruneEvents(ctx, db, eventRetention); err != nil {
		slog.WarnContext(ctx, "sweep: PruneEvents failed", "err", err)
	}
	if _, err := ExpireReviewQueueSLA(ctx, db, slaDays); err != nil {
		slog.WarnContext(ctx, "sweep: ExpireReviewQueueSLA failed", "err", err)
	}
}
