package main

import "oro/pkg/protocol"

// GetEpicProgress calculates the completion percentage for a focused epic.
// Returns (percentage, total_child_beads, done_child_beads).
// Returns (0, 0, 0) if focusedEpic is empty or has no child beads.
func GetEpicProgress(focusedEpic string, beads []protocol.Bead) (pct, total, done int) {
	if focusedEpic == "" {
		return 0, 0, 0
	}

	for _, b := range beads {
		// Count beads that belong to the focused epic (excluding the epic itself)
		if b.Epic == focusedEpic && b.ID != focusedEpic {
			total++
			if b.Status == "done" {
				done++
			}
		}
	}

	if total == 0 {
		return 0, 0, 0
	}

	pct = (done * 100) / total
	return pct, total, done
}
