package dispatcher

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// recoverDoltBackoff returns the exponential backoff duration for the n-th
// consecutive dolt recovery failure. The sequence is 1s, 2s, 4s, ... capped at 30s.
// recoverDoltBackoffFn may be set by tests to override this behaviour.
func (d *Dispatcher) recoverDoltBackoff(n int) time.Duration {
	if d.recoverDoltBackoffFn != nil {
		return d.recoverDoltBackoffFn(n)
	}
	backoff := time.Duration(1<<uint(n-1)) * time.Second
	if backoff > 30*time.Second {
		return 30 * time.Second
	}
	return backoff
}

// recoverDolt attempts to restart the dolt bead store and reimport state from
// the most recent backup. It sets doltRecovering=true, runs the legacy dolt
// start/import recovery commands, and clears doltRecovering on success.
// On failure it increments doltRecoveryAttempts and applies exponential backoff.
// After 3 consecutive failures it escalates to the manager via d.escalator.
func (d *Dispatcher) recoverDolt(ctx context.Context) {
	d.doltRecovering.Store(true)
	d.lastRecoveryTime = d.nowFunc()

	_ = d.logEvent(ctx, "dolt_recovery_started", "dispatcher", "", "",
		fmt.Sprintf(`{"attempt":%d}`, d.doltRecoveryAttempts+1))

	backupPath := filepath.Join(d.beadsDir, "backup", "full-state.jsonl")

	if _, err := d.shutdownRunner.Run(ctx, "bd", "dolt", "start"); err != nil {
		d.onRecoveryFailure(ctx, "dolt_start", err)
		return
	}

	if _, err := d.shutdownRunner.Run(ctx, "bd", "import", backupPath); err != nil {
		d.onRecoveryFailure(ctx, "import", err)
		return
	}

	d.doltRecoveryAttempts = 0
	d.doltRecovering.Store(false)
	_ = d.logEvent(ctx, "dolt_recovery_succeeded", "dispatcher", "", "", "")
}

// onRecoveryFailure handles a failed step inside recoverDolt: increments the
// attempt counter, logs the failure, applies backoff sleep, and escalates after
// 3 consecutive failures.
func (d *Dispatcher) onRecoveryFailure(ctx context.Context, step string, err error) {
	d.doltRecoveryAttempts++
	_ = d.logEvent(ctx, "dolt_recovery_failed", "dispatcher", "", "",
		fmt.Sprintf(`{"step":%q,"error":%q,"attempt":%d}`, step, err.Error(), d.doltRecoveryAttempts))

	backoff := d.recoverDoltBackoff(d.doltRecoveryAttempts)
	if backoff > 0 {
		timer := time.NewTimer(backoff)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-d.shutdownCh:
		case <-timer.C:
		}
	}

	if d.doltRecoveryAttempts >= 3 {
		msg := fmt.Sprintf("[ORO-DISPATCH] DOLT_RECOVERY_FAILED: dolt failed to recover after %d consecutive attempts", d.doltRecoveryAttempts)
		_ = d.escalator.Escalate(ctx, msg)
	}
}
