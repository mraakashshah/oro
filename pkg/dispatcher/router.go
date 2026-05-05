package dispatcher

import (
	"context"
	"fmt"
	"os"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// BuildPrompt selects the correct prompt assembler based on bead.Type (§10.2).
//
// Executable types: task, bug, chore → worker prompt; research → oracle stub;
// premortem → premortem stub. Non-executable types (epic, review) return an
// error. Full oracle and premortem assemblers are Phase B.3 deliverables.
func BuildPrompt(_ context.Context, _ beadstore.Store, b protocol.Bead) (string, error) {
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
	case "premortem":
		// Phase B.3 will replace this stub with AssemblePremortemPrompt.
		return fmt.Sprintf("# Premortem prompt\n\n## Target\n\n%s: %s\n", b.ID, b.Title), nil
	case "epic", "review":
		return "", fmt.Errorf("bead type %q is not directly executable; routed via decomposition or ops review", b.Type)
	default:
		return "", fmt.Errorf("unknown bead type %q", b.Type)
	}
}

// CloseBead closes beadID with reason and immediately runs
// PromoteChildrenOnParentClose to unblock waiting children (§10.4).
//
// Sweep failure is non-fatal: the bead remains closed and the periodic
// PromoteClosedParentChildren sweep will retry on the next tick.
func (d *Dispatcher) CloseBead(ctx context.Context, beadID, reason string) error {
	if err := d.beads.Close(ctx, beadID, reason); err != nil {
		return fmt.Errorf("Store.Close(%s): %w", beadID, err)
	}
	if err := PromoteChildrenOnParentClose(ctx, d.beads, beadID); err != nil {
		fmt.Fprintf(os.Stderr, "warn: dispatcher.CloseBead sweeper failed for %s: %v\n", beadID, err)
	}
	return nil
}
