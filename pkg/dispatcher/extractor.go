package dispatcher

import (
	"context"
	"log/slog"
)

// extractAndStoreLearnings is a no-op; worker-side extraction is authoritative.
// Retained as a method to avoid call-site churn while the intake pipeline is
// being migrated.
func (d *Dispatcher) extractAndStoreLearnings(_ context.Context, beadID string) {
	slog.Debug("skipping dispatcher-side extraction (worker-side authoritative)", "bead_id", beadID)
}
