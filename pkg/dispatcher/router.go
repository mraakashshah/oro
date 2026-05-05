package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// premortemVerdictPayload is the structured output a premortem agent emits at
// completion. The dispatcher's ClosePremortemBead consumes this payload and
// persists the verdict on the bead.
type premortemVerdictPayload struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// isValidPremortemVerdict reports whether v is in the closed verdict set per
// §11.4: proceed, block, replan.
func isValidPremortemVerdict(v string) bool {
	switch v {
	case "proceed", "block", "replan":
		return true
	default:
		return false
	}
}

// parsePremortemVerdict normalizes an emitted payload into (verdict, reason,
// invalid). When the payload is empty, malformed, or carries a verdict outside
// the closed set, the verdict defaults to "replan" and invalid=true so the
// caller can log a fail-safe warning. Reason text is preserved verbatim when
// extractable, even if the verdict itself is invalid.
func parsePremortemVerdict(payload []byte) (verdict, reason string, invalid bool) {
	if len(payload) == 0 {
		return "replan", "", true
	}
	var p premortemVerdictPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "replan", "", true
	}
	if !isValidPremortemVerdict(p.Verdict) {
		return "replan", p.Reason, true
	}
	return p.Verdict, p.Reason, false
}

// BuildPrompt selects the correct prompt assembler based on bead.Type (§10.2).
//
// Executable types: task, bug, chore → worker prompt; research → oracle stub;
// premortem → premortem stub. Non-executable types (epic, review) return an
// error. Full oracle and premortem assemblers are Phase B.3 deliverables.
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
	case "premortem":
		return worker.AssemblePremortemPrompt(worker.PremortemPromptParams{
			BeadID:            b.ID,
			TargetBeadID:      b.Epic,
			TargetTitle:       b.Title,
			TargetDescription: b.Description,
		}), nil
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
	return nil
}

// ClosePremortemBead closes a premortem-type bead and persists its verdict
// payload to bead.Metadata (§11.4). The verdict is one of {proceed, block,
// replan}; reason is preserved verbatim. When the payload is missing or
// malformed (or carries an unknown verdict value), the bead still closes but
// with verdict="replan" as the fail-safe default and a
// premortem_verdict_invalid event is recorded for observability.
//
// After persisting the verdict, the parent epic's gate_state is transitioned
// per the verdict: proceed→satisfied, block→blocked, replan→replan (+ cycle
// count increment). ErrStaleGate is treated as non-fatal (gate already moved).
func (d *Dispatcher) ClosePremortemBead(ctx context.Context, beadID string, payload []byte) error {
	verdict, reason, invalid := parsePremortemVerdict(payload)
	if invalid {
		d.warnInvalidPremortemVerdict(ctx, beadID, payload)
	}
	if err := d.beads.SetPremortemVerdict(ctx, beadID, verdict, reason); err != nil {
		return fmt.Errorf("Store.SetPremortemVerdict(%s): %w", beadID, err)
	}
	if err := d.applyPremortemVerdict(ctx, beadID, verdict); err != nil {
		return fmt.Errorf("apply premortem verdict for %s: %w", beadID, err)
	}
	closeReason := fmt.Sprintf("premortem verdict=%s", verdict)
	return d.CloseBead(ctx, beadID, closeReason)
}

// applyPremortemVerdict transitions the parent bead's gate_state based on the
// premortem verdict (§11.4). ErrStaleGate is non-fatal — it means the gate
// already moved (concurrent write or test setup without a matching initial state).
func (d *Dispatcher) applyPremortemVerdict(ctx context.Context, premortemID, verdict string) error {
	bead, err := d.beads.Show(ctx, premortemID)
	if err != nil {
		return fmt.Errorf("show premortem bead: %w", err)
	}
	if bead == nil || bead.Epic == "" {
		return nil
	}
	parentID := bead.Epic
	var gateErr error
	switch verdict {
	case "proceed":
		gateErr = d.beads.SetGateState(ctx, parentID, beadstore.GateEligible, beadstore.GateSatisfied, "premortem_verdict_proceed")
	case "block":
		gateErr = d.beads.SetGateState(ctx, parentID, beadstore.GateEligible, beadstore.GateBlocked, "premortem_verdict_block")
	case "replan":
		gateErr = d.beads.SetGateState(ctx, parentID, beadstore.GateEligible, beadstore.GateReplan, "premortem_verdict_replan")
		if gateErr == nil {
			if err := d.beads.IncrPremortCycleCount(ctx, parentID); err != nil {
				return fmt.Errorf("increment premortem cycle count for %s: %w", parentID, err)
			}
			return nil
		}
	default:
		return nil
	}
	if errors.Is(gateErr, beadstore.ErrStaleGate) {
		return nil
	}
	if gateErr != nil {
		return fmt.Errorf("set gate state for %s: %w", parentID, gateErr)
	}
	return nil
}

// warnInvalidPremortemVerdict records a fail-safe warning when a premortem
// bead closes without a parsable verdict. The bead still closes (with the
// "replan" default), but downstream observers need a signal that the agent
// did not produce a clean verdict.
func (d *Dispatcher) warnInvalidPremortemVerdict(ctx context.Context, beadID string, payload []byte) {
	if d.db == nil {
		fmt.Fprintf(os.Stderr, "warn: dispatcher.ClosePremortemBead: invalid verdict payload for %s; defaulting to replan\n", beadID)
		return
	}
	_ = d.logEvent(ctx, "premortem_verdict_invalid", "dispatcher", beadID, "", string(payload))
}
