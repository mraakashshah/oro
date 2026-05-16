package dispatcher

import (
	"context"
	"fmt"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// ResolveEpicBranch is the exported form of resolveEpicBranch, for use by
// packages outside the dispatcher (e.g. cmd/oro work command) that need the
// same parent-chain-walking logic.
func ResolveEpicBranch(ctx context.Context, beads beadstore.Store, parentID, defaultBranch string) (branch, epicID string, err error) {
	return resolveEpicBranch(ctx, beads, parentID, defaultBranch)
}

// resolveEpicBranch walks the parent chain starting from parentID to find the
// nearest epic-type ancestor. Returns ("epic/<id>", id, nil) if an epic is
// found, (defaultBranch, "", nil) if no epic ancestor exists, or
// (defaultBranch, "", err) on I/O failure.
//
// parentID is the raw bead.Epic value, which maps to the JSON "parent" field
// and may point to any bead type — not necessarily an epic.
func resolveEpicBranch(ctx context.Context, beads beadstore.Store, parentID, defaultBranch string) (branch, epicID string, err error) {
	if parentID == "" {
		return defaultBranch, "", nil
	}

	visited := make(map[string]bool)
	current := parentID

	for current != "" {
		if visited[current] {
			// Cycle in the parent chain — bail out safely.
			return defaultBranch, "", fmt.Errorf("resolveEpicBranch: cycle detected at bead %q", current)
		}
		visited[current] = true

		detail, showErr := beads.Show(ctx, current)
		if showErr != nil {
			return defaultBranch, "", fmt.Errorf("resolveEpicBranch: show %q: %w", current, showErr)
		}
		if detail == nil {
			return defaultBranch, "", fmt.Errorf("resolveEpicBranch: show %q returned nil bead", current)
		}

		if detail.Type == "epic" {
			return protocol.EpicBranchPrefix + current, current, nil
		}

		current = detail.Epic // walk up to parent
	}

	return defaultBranch, "", nil
}
