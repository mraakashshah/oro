package dispatcher

import (
	"context"
	"fmt"
	"os"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// BuildPrompt selects the correct prompt assembler based on bead.Type (§10.2).
//
// Executable types: task, bug, chore → worker prompt; research → oracle stub;
// non-executable types (epic, review) return an error. Full oracle assemblers
// are Phase B.3 deliverables.
//
// Deviation from §10.3 sketch: the spec sketches BuildPrompt(ctx, store,
// cards, b) because real assemblers will fetch journey/cards via the store.
// Until B.3 introduces those fetches, the store and cards parameters are
// dropped here rather than accepted-and-ignored — keeping a stub signature
// that future callers rely on without exercising the dependency is a worse
// trap than expanding the signature when the work actually lands.
func BuildPrompt(_ context.Context, b protocol.Bead) (string, error) {
	switch b.Type {
	case "task", "bug", "chore":
		return worker.AssemblePrompt(worker.PromptParams{
			BeadID:             b.ID,
			Title:              b.Title,
			Description:        b.Description,
			AcceptanceCriteria: b.AcceptanceCriteria,
		}), nil
	case "research":
		// Phase B.3 will replace this stub with AssembleOraclePrompt.
		return fmt.Sprintf("# Oracle prompt\n\n## Bead\n\n%s: %s\n", b.ID, b.Title), nil
	case "epic", "review":
		return "", fmt.Errorf("bead type %q is not directly executable; routed via decomposition or ops review", b.Type)
	default:
		return "", fmt.Errorf("unknown bead type %q", b.Type)
	}
}

// warnSweepFailure records a non-fatal sweep failure. Prefers the dispatcher's
// structured event log; falls back to stderr when d.db is unavailable (unit
// tests that build Dispatcher directly without a backing DB).
func (d *Dispatcher) warnSweepFailure(ctx context.Context, beadID string, err error) {
	if d.db != nil {
		_ = d.logEvent(ctx, "close_bead_sweep_failed", "dispatcher", beadID, "", err.Error())
		return
	}
	fmt.Fprintf(os.Stderr, "warn: dispatcher.CloseBead sweeper failed for %s: %v\n", beadID, err)
}

// CloseBead closes beadID with reason and immediately runs
// PromoteChildrenOnParentClose to unblock waiting children (§10.4).
//
// Sweep failure is non-fatal: the bead remains closed and the periodic
// PromoteClosedParentChildren sweep will retry on the next tick. The failure
// is recorded via the dispatcher's structured event log (matching surrounding
// d.logEvent usage); when d.db is nil (unit tests that build Dispatcher
// directly without a backing DB) we fall back to stderr so the warning is
// still observable.
func (d *Dispatcher) CloseBead(ctx context.Context, beadID, reason string) error {
	if err := d.beads.Close(ctx, beadID, reason); err != nil {
		return fmt.Errorf("Store.Close(%s): %w", beadID, err)
	}
	if err := PromoteChildrenOnParentClose(ctx, d.beads, beadID); err != nil {
		d.warnSweepFailure(ctx, beadID, err)
	}
	if err := d.runLearningPromotion(ctx, beadID, promotionVerdictFromCloseReason(reason)); err != nil {
		return fmt.Errorf("run learning promotion for %s: %w", beadID, err)
	}
	return nil
}
