package dispatcher

import (
	"context"
	"fmt"

	"oro/pkg/protocol"
)

// resolveEpicBranch walks the parent chain starting from parentID to find the
// nearest epic-type ancestor. Returns ("epic/<id>", id, nil) if an epic is
// found, ("main", "", nil) if no epic ancestor exists, or ("main", "", err)
// on I/O failure.
//
// parentID is the raw bead.Epic value, which maps to the JSON "parent" field
// and may point to any bead type — not necessarily an epic.
func resolveEpicBranch(ctx context.Context, beads BeadSource, parentID string) (branch, epicID string, err error) {
	if parentID == "" {
		return "main", "", nil
	}

	visited := make(map[string]bool)
	current := parentID

	for current != "" {
		if visited[current] {
			// Cycle in the parent chain — bail out safely.
			return "main", "", fmt.Errorf("resolveEpicBranch: cycle detected at bead %q", current)
		}
		visited[current] = true

		detail, showErr := beads.Show(ctx, current)
		if showErr != nil {
			return "main", "", fmt.Errorf("resolveEpicBranch: show %q: %w", current, showErr)
		}

		if detail.Type == "epic" {
			return protocol.EpicBranchPrefix + current, current, nil
		}

		current = detail.Epic // walk up to parent
	}

	return "main", "", nil
}
