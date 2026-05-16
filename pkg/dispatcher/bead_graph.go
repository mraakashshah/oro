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
