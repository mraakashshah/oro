package main

import (
	"testing"
)

func TestBottlenecksSortedByBlockedCountDescending(t *testing.T) {
	// B blocks 3 beads, A blocks 2, C and D each block 1.
	// Tiebreak: C comes before D alphabetically.
	beads := []BeadWithDeps{
		{ID: "bead-A", DependsOn: []string{"bead-B"}},
		{ID: "bead-C", DependsOn: []string{"bead-B", "bead-A"}},
		{ID: "bead-D", DependsOn: []string{"bead-B", "bead-A", "bead-E"}},
		{ID: "bead-E", DependsOn: []string{"bead-C"}},
		{ID: "bead-B"},
	}

	g := NewDependencyGraph(beads)
	got := g.Bottlenecks()

	// Expected order (BlockedCount desc, then BeadID asc on ties):
	// bead-B: 3 (blocked by A, C, D)
	// bead-A: 2 (blocked by C, D)
	// bead-C: 1 (blocked by E)
	// bead-E: 1 (blocked by D) — tiebreak: C < E
	want := []Bottleneck{
		{BeadID: "bead-B", BlockedCount: 3},
		{BeadID: "bead-A", BlockedCount: 2},
		{BeadID: "bead-C", BlockedCount: 1},
		{BeadID: "bead-E", BlockedCount: 1},
	}

	if len(got) != len(want) {
		t.Fatalf("Bottlenecks() len = %d, want %d; got %v", len(got), len(want), got)
	}

	for i, w := range want {
		g := got[i]
		if g.BeadID != w.BeadID || g.BlockedCount != w.BlockedCount {
			t.Errorf("Bottlenecks()[%d] = {%s, %d}, want {%s, %d}",
				i, g.BeadID, g.BlockedCount, w.BeadID, w.BlockedCount)
		}
	}
}
