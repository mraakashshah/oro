package dispatcher

import (
	"context"
	"fmt"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// CreateBeadGraph creates child beads under parentID and returns the stored
// children in input order. ParentID on each child is forced to parentID.
func CreateBeadGraph(ctx context.Context, store beadstore.Store, parentID string, children []beadstore.CreateParams) ([]protocol.Bead, error) {
	created := make([]protocol.Bead, 0, len(children))
	for _, child := range children {
		child.ParentID = parentID
		child = inheritCreateParamsTier(ctx, store, child)
		bead, err := store.Create(ctx, child)
		if err != nil {
			return nil, fmt.Errorf("create graph child under %s: %w", parentID, err)
		}
		if bead == nil {
			return nil, fmt.Errorf("create graph child under %s: nil bead returned", parentID)
		}
		created = append(created, *bead)
	}
	return created, nil
}

func inheritCreateParamsTier(ctx context.Context, store beadstore.Store, params beadstore.CreateParams) beadstore.CreateParams {
	if params.Tier != "" || params.ParentID == "" {
		return params
	}
	params.Tier = parentTierForCreate(ctx, store, params.ParentID)
	return params
}

func parentTierForCreate(ctx context.Context, store beadstore.Store, parentID string) string {
	if parentID == "" {
		return ""
	}
	parent, err := store.Show(ctx, parentID)
	if err != nil || parent == nil {
		return ""
	}
	return string(parent.Tier)
}
