package main

import (
	"reflect"
	"testing"
)

func TestInsightsModel_CriticalPath(t *testing.T) {
	tests := []struct {
		name     string
		beads    []BeadWithDeps
		wantPath []string
		wantErr  bool
	}{
		{
			name:     "no dependencies returns empty critical path",
			beads:    []BeadWithDeps{{ID: "bead-1"}},
			wantPath: []string{},
			wantErr:  false,
		},
		{
			name: "simple chain returns correct critical path",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-2"}},
				{ID: "bead-2", DependsOn: []string{"bead-3"}},
				{ID: "bead-3"},
			},
			wantPath: []string{"bead-3", "bead-2", "bead-1"},
			wantErr:  false,
		},
		{
			name: "multiple chains returns longest path",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-2"}},
				{ID: "bead-2", DependsOn: []string{"bead-3"}},
				{ID: "bead-3"},
				{ID: "bead-4", DependsOn: []string{"bead-5"}},
				{ID: "bead-5"},
			},
			wantPath: []string{"bead-3", "bead-2", "bead-1"},
			wantErr:  false,
		},
		{
			name: "circular dependency returns error",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-2"}},
				{ID: "bead-2", DependsOn: []string{"bead-1"}},
			},
			wantPath: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := NewDependencyGraph(tt.beads)
			gotPath, err := graph.CriticalPath()

			if tt.wantErr {
				assertError(t, err, gotPath)
				return
			}

			assertNoError(t, err)
			assertPathEquals(t, gotPath, tt.wantPath)
		})
	}
}

func TestInsightsModel_Bottlenecks(t *testing.T) {
	tests := []struct {
		name            string
		beads           []BeadWithDeps
		wantBottlenecks []Bottleneck
	}{
		{
			name:            "no dependencies returns no bottlenecks",
			beads:           []BeadWithDeps{{ID: "bead-1"}},
			wantBottlenecks: []Bottleneck{},
		},
		{
			name: "single blocker with multiple blockers",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-3"}},
				{ID: "bead-2", DependsOn: []string{"bead-3"}},
				{ID: "bead-3"},
			},
			wantBottlenecks: []Bottleneck{
				{BeadID: "bead-3", BlockedCount: 2},
			},
		},
		{
			name: "multiple blockers with different counts",
			beads: []BeadWithDeps{
				{ID: "bead-1", DependsOn: []string{"bead-4"}},
				{ID: "bead-2", DependsOn: []string{"bead-4"}},
				{ID: "bead-3", DependsOn: []string{"bead-4"}},
				{ID: "bead-4"},
				{ID: "bead-5", DependsOn: []string{"bead-6"}},
				{ID: "bead-6"},
			},
			wantBottlenecks: []Bottleneck{
				{BeadID: "bead-4", BlockedCount: 3},
				{BeadID: "bead-6", BlockedCount: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := NewDependencyGraph(tt.beads)
			got := graph.Bottlenecks()
			if len(got) != len(tt.wantBottlenecks) {
				t.Errorf("Bottlenecks() count = %d, want %d", len(got), len(tt.wantBottlenecks))
				return
			}
			// Check that all expected bottlenecks are present
			for _, want := range tt.wantBottlenecks {
				found := false
				for _, g := range got {
					if g.BeadID == want.BeadID && g.BlockedCount == want.BlockedCount {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Bottlenecks() missing expected bottleneck %+v", want)
				}
			}
		})
	}
}

func TestInsightsModel_TriageFlags(t *testing.T) {
	tests := []struct {
		name      string
		beads     []BeadWithDeps
		wantFlags []TriageFlag
	}{
		{
			name:      "no beads returns no flags",
			beads:     []BeadWithDeps{},
			wantFlags: []TriageFlag{},
		},
		{
			name: "stale P0 bead gets flagged",
			beads: []BeadWithDeps{
				{ID: "bead-1", Priority: 0, DaysSinceUpdate: 8},
			},
			wantFlags: []TriageFlag{
				{BeadID: "bead-1", Reason: "stale P0 (8 days)", Severity: "high"},
			},
		},
		{
			name: "misprioritized bug gets flagged",
			beads: []BeadWithDeps{
				{ID: "bead-1", Type: "bug", Priority: 4},
			},
			wantFlags: []TriageFlag{
				{BeadID: "bead-1", Reason: "bug with low priority (P4)", Severity: "medium"},
			},
		},
		{
			name: "normal beads have no flags",
			beads: []BeadWithDeps{
				{ID: "bead-1", Priority: 2, DaysSinceUpdate: 3},
				{ID: "bead-2", Type: "task", Priority: 2},
			},
			wantFlags: []TriageFlag{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph := NewDependencyGraph(tt.beads)
			got := graph.TriageFlags()
			if len(got) != len(tt.wantFlags) {
				t.Errorf("TriageFlags() count = %d, want %d", len(got), len(tt.wantFlags))
				return
			}
			// Check that all expected flags are present
			for _, want := range tt.wantFlags {
				found := false
				for _, g := range got {
					if g.BeadID == want.BeadID && g.Reason == want.Reason && g.Severity == want.Severity {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("TriageFlags() missing expected flag %+v", want)
				}
			}
		})
	}
}

// Test helpers
func assertError(t *testing.T, err error, gotPath []string) {
	t.Helper()
	if err == nil {
		t.Errorf("CriticalPath() expected error, got nil")
	}
	if gotPath != nil {
		t.Errorf("CriticalPath() expected nil path on error, got %v", gotPath)
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("CriticalPath() unexpected error: %v", err)
	}
}

func assertPathEquals(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CriticalPath() = %v, want %v", got, want)
	}
}
