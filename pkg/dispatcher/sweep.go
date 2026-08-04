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

const tagAwaitsParentClose = "awaits_parent_close"

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
		payload := fmt.Sprintf(`{"parent_id":%q,"child_id":%q}`, parentID, child.ID)
		journey := beadstore.JourneyEvent{
			BeadID:  child.ID,
			Ts:      time.Now().UTC().Format(time.RFC3339Nano),
			Actor:   "dispatcher",
			Event:   "parent_closed_promoted",
			Payload: payload,
		}
		params := beadstore.UpdateParams{Tags: &newTags}
		if atomicStore, ok := store.(interface {
			UpdateWithJourney(context.Context, string, beadstore.UpdateParams, beadstore.JourneyEvent) error
		}); ok {
			if err := atomicStore.UpdateWithJourney(ctx, child.ID, params, journey); err != nil {
				return fmt.Errorf("sweep: promote child %s atomically: %w", child.ID, err)
			}
			continue
		}
		if err := store.Update(ctx, child.ID, params); err != nil {
			return fmt.Errorf("sweep: remove tag from child %s: %w", child.ID, err)
		}
		if err := store.AppendJourney(ctx, child.ID, journey); err != nil {
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

// SweepDeletedBeadLearnings rejects pending bead_learnings_pending rows whose
// associated bead is soft-deleted (beads.deleted=1). Returns the number of rows rejected.
func SweepDeletedBeadLearnings(ctx context.Context, db *sql.DB) (int64, error) {
	ok, err := tableExists(ctx, db, "bead_learnings_pending")
	if err != nil {
		return 0, fmt.Errorf("sweep: inspect deleted bead learnings table: %w", err)
	}
	if !ok {
		return 0, nil
	}
	ok, err = tableExists(ctx, db, "beads")
	if err != nil {
		return 0, fmt.Errorf("sweep: inspect beads table: %w", err)
	}
	if !ok {
		return 0, nil
	}
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

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `
		SELECT 1
		  FROM sqlite_master
		 WHERE type = 'table'
		   AND name = ?
		 LIMIT 1`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect sqlite table %s: %w", name, err)
	}
	return true, nil
}

// PruneEvents removes durable dispatcher events older than retain.
// A non-positive retain duration is a no-op so a missing config cannot delete
// the full event log on the first sweep.
func PruneEvents(ctx context.Context, db *sql.DB, retain time.Duration) (int64, error) {
	if db == nil || retain <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-retain).Format("2006-01-02 15:04:05")
	res, err := db.ExecContext(ctx, `
		DELETE FROM events
		 WHERE datetime(created_at) < datetime(?)`,
		cutoff)
	if err != nil {
		return 0, fmt.Errorf("sweep: prune events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sweep: prune events rows affected: %w", err)
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
