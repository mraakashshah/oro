package storage

import (
	"context"
	"fmt"
	"time"
)

const overdueDevCleanupDrainAfter = 24 * time.Hour

// GlobalDrainRequest configures the pause-and-drain boundary required before
// an overdue developer-cache cleanup may start provider maintenance.
type GlobalDrainRequest struct {
	Protocol     *PauseEpochProtocol
	PollInterval time.Duration
}

func overdueDevCleanupRequiresGlobalDrain(now, due time.Time) bool {
	return !now.Before(due.Add(overdueDevCleanupDrainAfter))
}

func (request GlobalDrainRequest) wait(ctx context.Context, catalog *Catalog, now time.Time) error {
	protocol := request.Protocol
	if protocol == nil {
		protocol = NewPauseEpochProtocol(catalog, nil)
	}
	epoch, err := protocol.RequestPause(ctx, now)
	if err != nil {
		return fmt.Errorf("request overdue dev cleanup pause: %w", err)
	}
	for {
		acknowledged, err := protocol.Acknowledged(ctx, epoch.Epoch, now)
		if err != nil {
			return fmt.Errorf("check overdue dev cleanup acknowledgements: %w", err)
		}
		leasesDrained, err := catalog.activeLeasesDrained(ctx)
		if err != nil {
			return fmt.Errorf("check overdue dev cleanup leases: %w", err)
		}
		if acknowledged && leasesDrained {
			return nil
		}
		if err := waitForDrainPoll(ctx, request.PollInterval); err != nil {
			return fmt.Errorf("wait for overdue dev cleanup drain: %w", err)
		}
	}
}

func (c *Catalog) activeLeasesDrained(ctx context.Context) (bool, error) {
	var count int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_leases WHERE released_at IS NULL`).Scan(&count); err != nil {
		return false, fmt.Errorf("count active runtime leases: %w", err)
	}
	return count == 0, nil
}

func waitForDrainPoll(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for drain poll: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
