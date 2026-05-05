package dispatcher

import (
	"context"
	"fmt"
	"slices"
	"time"

	"oro/pkg/beadstore"
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
