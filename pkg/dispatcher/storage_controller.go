package dispatcher

import (
	"context"
	"fmt"
	"time"
)

// observeStorageController applies the latest durable storage pause epoch.
// The storage controller closes admission before starting its drain and only
// acknowledges an epoch after that drain succeeds.
func (d *Dispatcher) observeStorageController(ctx context.Context) error {
	if d == nil || d.cfg.StorageController == nil {
		return nil
	}
	if err := d.cfg.StorageController.Observe(ctx, d.nowFunc()); err != nil {
		return fmt.Errorf("observe storage controller: %w", err)
	}
	return nil
}

func (d *Dispatcher) storageAdmissionAllowed() bool {
	return d == nil || d.cfg.StorageController == nil || d.cfg.StorageController.Admit()
}

func (d *Dispatcher) storageControllerLoop(ctx context.Context) {
	if d.cfg.StorageController == nil {
		return
	}
	interval := d.cfg.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.observeStorageController(ctx); err != nil {
			_ = d.logEvent(ctx, "storage_controller_observe_failed", "dispatcher", "", "", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
