package dispatcher

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// ErrReplanLoopExhausted is returned by OnReplanChildrenClosed when the parent
// is already at gate_state=escalated, indicating the replan cycle cap has been
// reached and no further replan children should be created.
var ErrReplanLoopExhausted = errors.New("dispatcher: replan loop exhausted")

const tagAwaitsParentClose = "awaits_parent_close"

// defaultMaxPremortemCycles is the default cap on replan cycles before escalation (§11.4).
const defaultMaxPremortemCycles = 5

// nopPremortCounter satisfies PremortCounter with no-op SetPremortCycleCount.
// Used as a stub until the v3 beadstore gains a persistent cycle-count writer.
type nopPremortCounter struct{}

// SetPremortCycleCount is a no-op implementation of PremortCounter.
func (nopPremortCounter) SetPremortCycleCount(_ context.Context, _ string, _ int) error {
	return nil
}

// parseReplanCycleNum extracts the N from the first "replan_cycle:N" tag in tags.
// Returns -1 when no such tag is present or when N cannot be parsed.
func parseReplanCycleNum(tags []string) int {
	for _, tag := range tags {
		if len(tag) > len("replan_cycle:") && tag[:len("replan_cycle:")] == "replan_cycle:" {
			n := 0
			rest := tag[len("replan_cycle:"):]
			for _, c := range rest {
				if c < '0' || c > '9' {
					return -1
				}
				n = n*10 + int(c-'0')
			}
			return n
		}
	}
	return -1
}

// PremortCounter records a bead's completed premortem cycle count.
// It is satisfied by any type that exposes SetPremortCycleCount, including
// beadstore.SQLiteStore after v3 migration.
type PremortCounter interface {
	SetPremortCycleCount(ctx context.Context, beadID string, n int) error
}

// PromoteChildrenOnParentClose removes the awaits_parent_close tag from every
// child of parentID and appends a parent_closed_promoted journey event on each.
//
// Gate: only acts when the parent's type is "research"; other parent types'
// children do not carry awaits_parent_close in normal usage.
//
// Idempotent: a second call for the same parent finds no tagged children and is
// a no-op. Errors from individual child updates are returned immediately so the
// caller can decide whether to retry (the periodic PromoteClosedParentChildren
// sweep will do so on the next tick).
func PromoteChildrenOnParentClose(ctx context.Context, store beadstore.Store, parentID string) error {
	parent, err := store.Show(ctx, parentID)
	if err != nil {
		return fmt.Errorf("sweep: look up parent %s: %w", parentID, err)
	}
	if parent == nil || parent.Type != "research" {
		return nil
	}

	children, err := store.FindByParentAndTag(ctx, parentID, tagAwaitsParentClose)
	if err != nil {
		return fmt.Errorf("sweep: find children of %s: %w", parentID, err)
	}

	for _, child := range children {
		newTags := slices.DeleteFunc(slices.Clone(child.Tags), func(t string) bool {
			return t == tagAwaitsParentClose
		})
		if err := store.Update(ctx, child.ID, beadstore.UpdateParams{Tags: &newTags}); err != nil {
			return fmt.Errorf("sweep: remove tag from child %s: %w", child.ID, err)
		}
		payload := fmt.Sprintf(`{"parent_id":%q,"child_id":%q}`, parentID, child.ID)
		if err := store.AppendJourney(ctx, child.ID, beadstore.JourneyEvent{
			BeadID:  child.ID,
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
			Actor:   "dispatcher",
			Event:   "parent_closed_promoted",
			Payload: payload,
		}); err != nil {
			return fmt.Errorf("sweep: append journey for child %s: %w", child.ID, err)
		}
	}
	return nil
}

// PromoteClosedParentChildren is the periodic retry path for PromoteChildrenOnParentClose.
// It scans all beads for children with the awaits_parent_close tag whose parent is alive
// AND closed, then runs per-child promotion. Runs every 5 min.
func PromoteClosedParentChildren(ctx context.Context, store beadstore.Store) error {
	beads, err := exportBeads(ctx, store)
	if err != nil {
		return err
	}

	seen := make(map[string]struct{})
	for _, b := range beads {
		if hasBeadTag(b, tagAwaitsParentClose) && b.Epic != "" {
			seen[b.Epic] = struct{}{}
		}
	}

	for parentID := range seen {
		parent, err := store.Show(ctx, parentID)
		if err != nil {
			return fmt.Errorf("sweep: show parent %s: %w", parentID, err)
		}
		if parent == nil || parent.Status != "closed" {
			continue
		}
		if err := PromoteChildrenOnParentClose(ctx, store, parentID); err != nil {
			return err
		}
	}
	return nil
}

// ReapDeletedParentChildren finds children with awaits_parent_close whose parent
// is soft-deleted (Show returns nil). For each, it appends an escalated journey
// event and defers the child for human action. Runs every 5 min.
func ReapDeletedParentChildren(ctx context.Context, store DeferredStore) error {
	beads, err := exportBeads(ctx, store)
	if err != nil {
		return err
	}

	for _, child := range beads {
		if !hasBeadTag(child, tagAwaitsParentClose) || child.Epic == "" {
			continue
		}

		parent, err := store.Show(ctx, child.Epic)
		if err != nil {
			return fmt.Errorf("sweep: show parent %s: %w", child.Epic, err)
		}
		if parent != nil {
			continue // parent alive — not our job
		}

		// Idempotency: already deferred means we already escalated.
		if child.DeferUntil != "" {
			continue
		}

		payload := fmt.Sprintf(`{"kind":"parent_deleted","parent_id":%q,"child_id":%q}`, child.Epic, child.ID)
		if err := store.AppendJourney(ctx, child.ID, beadstore.JourneyEvent{
			BeadID:  child.ID,
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
			Actor:   "dispatcher",
			Event:   "escalated",
			Payload: payload,
		}); err != nil {
			return fmt.Errorf("sweep: append escalated for child %s: %w", child.ID, err)
		}

		if err := store.Defer(ctx, child.ID, zombieDeferredUntil); err != nil {
			return fmt.Errorf("sweep: defer child %s: %w", child.ID, err)
		}
	}
	return nil
}

// OnReplanChildrenClosed is called when a replan-cycle child of parentID closes.
// When all open children tagged replan_cycle:<cycleNum> are gone, it transitions
// the parent's gate_state from replan → eligible and records the cycle count.
// If cycleNum >= maxCycles, it emits an escalated event instead.
func OnReplanChildrenClosed(ctx context.Context, store beadstore.Store, counter PremortCounter, parentID string, cycleNum, maxCycles int) error {
	gs, err := store.GateState(ctx, parentID)
	if err != nil {
		return fmt.Errorf("sweep: gate state for %s: %w", parentID, err)
	}
	if gs == beadstore.GateEscalated {
		return ErrReplanLoopExhausted
	}

	beads, err := exportBeads(ctx, store)
	if err != nil {
		return err
	}

	replanTag := fmt.Sprintf("replan_cycle:%d", cycleNum)
	for _, b := range beads {
		if b.Epic == parentID && hasBeadTag(b, replanTag) && b.Status != "closed" {
			return nil // open replan children remain
		}
	}

	if cycleNum >= maxCycles {
		payload := fmt.Sprintf(
			`{"kind":"premortem_loop","parent_id":%q,"cycle_count":%d,"max_cycles":%d}`,
			parentID, cycleNum, maxCycles)
		if err := store.AppendJourney(ctx, parentID, beadstore.JourneyEvent{
			BeadID:  parentID,
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
			Actor:   "dispatcher",
			Event:   "escalated",
			Payload: payload,
		}); err != nil {
			return fmt.Errorf("sweep: escalate at max cycles for %s: %w", parentID, err)
		}
		return nil
	}

	reason := fmt.Sprintf("replan_cycle_%d_complete", cycleNum)
	if err := store.SetGateState(ctx, parentID, beadstore.GateReplan, beadstore.GateEligible, reason); err != nil {
		return fmt.Errorf("sweep: gate transition for %s: %w", parentID, err)
	}
	if err := counter.SetPremortCycleCount(ctx, parentID, cycleNum); err != nil {
		return fmt.Errorf("sweep: set premortem cycle count for %s: %w", parentID, err)
	}
	return nil
}

// ExpireReviewQueueSLA auto-rejects bead_learnings_pending rows whose
// queued_for_review_at is older than slaDays. Returns the number of rows rejected.
func ExpireReviewQueueSLA(ctx context.Context, db *sql.DB, slaDays int) (int64, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE bead_learnings_pending
		   SET rejected_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		       reason      = 'review_queue_sla_expired'
		 WHERE queued_for_review_at IS NOT NULL
		   AND promoted_to IS NULL
		   AND rejected_at IS NULL
		   AND datetime(queued_for_review_at) < datetime('now', printf('-%d days', ?))`,
		slaDays)
	if err != nil {
		return 0, fmt.Errorf("sweep: expire review queue SLA: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep: expire review queue SLA rows affected: %w", err)
	}
	return n, nil
}

// SweepDeletedBeadLearnings rejects pending bead_learnings_pending rows whose
// associated bead is soft-deleted (beads.deleted=1). Returns the number of rows rejected.
func SweepDeletedBeadLearnings(ctx context.Context, db *sql.DB) (int64, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE bead_learnings_pending
		   SET rejected_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		       reason      = 'parent_bead_deleted'
		 WHERE promoted_to IS NULL
		   AND rejected_at IS NULL
		   AND bead_id IN (SELECT id FROM beads WHERE deleted = 1)`)
	if err != nil {
		return 0, fmt.Errorf("sweep: sweep deleted bead learnings: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep: sweep deleted bead learnings rows affected: %w", err)
	}
	return n, nil
}

// exportBeads calls store.Export and decodes the JSONL result.
func exportBeads(ctx context.Context, store beadstore.Store) ([]protocol.Bead, error) {
	out, err := store.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("sweep: export beads: %w", err)
	}
	var beads []protocol.Bead
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var bead protocol.Bead
		if err := json.Unmarshal([]byte(line), &bead); err != nil {
			continue
		}
		beads = append(beads, bead)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("sweep: scan exported beads: %w", err)
	}
	return beads, nil
}

// hasBeadTag reports whether bead has the given tag.
func hasBeadTag(bead protocol.Bead, tag string) bool {
	for _, t := range bead.Tags {
		if t == tag {
			return true
		}
	}
	return false
}
