package main

import (
	"testing"

	"oro/pkg/protocol"
)

// TestEpicProgress verifies that GetEpicProgress correctly calculates
// epic completion percentage based on child bead statuses.
func TestEpicProgress(t *testing.T) {
	tests := []struct {
		name        string
		focusedEpic string
		beads       []protocol.Bead
		wantPct     int
		wantTotal   int
		wantDone    int
	}{
		{
			name:        "no focused epic",
			focusedEpic: "",
			beads:       []protocol.Bead{},
			wantPct:     0,
			wantTotal:   0,
			wantDone:    0,
		},
		{
			name:        "focused epic with no child beads",
			focusedEpic: "epic-123",
			beads: []protocol.Bead{
				{ID: "epic-123", Type: "epic", Status: "open"},
			},
			wantPct:   0,
			wantTotal: 0,
			wantDone:  0,
		},
		{
			name:        "focused epic with all children done",
			focusedEpic: "epic-123",
			beads: []protocol.Bead{
				{ID: "epic-123", Type: "epic", Status: "open"},
				{ID: "bead-1", Type: "task", Status: "done", Epic: "epic-123"},
				{ID: "bead-2", Type: "bug", Status: "done", Epic: "epic-123"},
				{ID: "bead-3", Type: "feature", Status: "done", Epic: "epic-123"},
			},
			wantPct:   100,
			wantTotal: 3,
			wantDone:  3,
		},
		{
			name:        "focused epic with partial completion",
			focusedEpic: "epic-456",
			beads: []protocol.Bead{
				{ID: "epic-456", Type: "epic", Status: "in_progress"},
				{ID: "task-1", Type: "task", Status: "done", Epic: "epic-456"},
				{ID: "task-2", Type: "task", Status: "in_progress", Epic: "epic-456"},
				{ID: "task-3", Type: "task", Status: "open", Epic: "epic-456"},
				{ID: "task-4", Type: "task", Status: "blocked", Epic: "epic-456"},
			},
			wantPct:   25, // 1 of 4 done
			wantTotal: 4,
			wantDone:  1,
		},
		{
			name:        "ignores beads from other epics",
			focusedEpic: "epic-789",
			beads: []protocol.Bead{
				{ID: "epic-789", Type: "epic", Status: "in_progress"},
				{ID: "task-a", Type: "task", Status: "done", Epic: "epic-789"},
				{ID: "task-b", Type: "task", Status: "open", Epic: "epic-789"},
				{ID: "task-other", Type: "task", Status: "done", Epic: "epic-other"},
			},
			wantPct:   50, // 1 of 2 for epic-789
			wantTotal: 2,
			wantDone:  1,
		},
		{
			name:        "zero division safety when no child beads",
			focusedEpic: "epic-empty",
			beads: []protocol.Bead{
				{ID: "epic-empty", Type: "epic", Status: "open"},
			},
			wantPct:   0,
			wantTotal: 0,
			wantDone:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pct, total, done := GetEpicProgress(tt.focusedEpic, tt.beads)
			if pct != tt.wantPct {
				t.Errorf("GetEpicProgress() pct = %d, want %d", pct, tt.wantPct)
			}
			if total != tt.wantTotal {
				t.Errorf("GetEpicProgress() total = %d, want %d", total, tt.wantTotal)
			}
			if done != tt.wantDone {
				t.Errorf("GetEpicProgress() done = %d, want %d", done, tt.wantDone)
			}
		})
	}
}
