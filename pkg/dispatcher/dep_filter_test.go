package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"testing"

	"oro/pkg/protocol"
)

// TestHasUnresolvedBlockingDep verifies the hasUnresolvedBlockingDep helper.
//
// Acceptance criteria (oro-ljun):
//   - Returns true for "blocks" and "conditional-blocks" deps on non-closed beads
//   - Returns false for "parent-child" deps (non-blocking by type)
//   - Returns false for dangling deps (DependsOnID not in openBeadIDs)
//   - Returns false when bead has no dependencies
func TestHasUnresolvedBlockingDep(t *testing.T) {
	t.Parallel()

	openSet := map[string]bool{
		"dep-open": true,
	}

	tests := []struct {
		name string
		bead protocol.Bead
		want bool
	}{
		{
			name: "blocks dep on open bead returns true",
			bead: protocol.Bead{
				ID: "b1",
				Dependencies: []protocol.Dependency{
					{IssueID: "b1", DependsOnID: "dep-open", Type: "blocks"},
				},
			},
			want: true,
		},
		{
			name: "conditional-blocks dep on open bead returns true",
			bead: protocol.Bead{
				ID: "b2",
				Dependencies: []protocol.Dependency{
					{IssueID: "b2", DependsOnID: "dep-open", Type: "conditional-blocks"},
				},
			},
			want: true,
		},
		{
			name: "parent-child dep is non-blocking, returns false",
			bead: protocol.Bead{
				ID: "b3",
				Dependencies: []protocol.Dependency{
					{IssueID: "b3", DependsOnID: "dep-open", Type: "parent-child"},
				},
			},
			want: false,
		},
		{
			name: "blocks dep on missing/dangling bead returns false",
			bead: protocol.Bead{
				ID: "b4",
				Dependencies: []protocol.Dependency{
					{IssueID: "b4", DependsOnID: "dep-closed-or-absent", Type: "blocks"},
				},
			},
			want: false,
		},
		{
			name: "no dependencies returns false",
			bead: protocol.Bead{ID: "b5"},
			want: false,
		},
		{
			name: "mixed: blocks on open + parent-child on open = true (blocking dep wins)",
			bead: protocol.Bead{
				ID: "b6",
				Dependencies: []protocol.Dependency{
					{IssueID: "b6", DependsOnID: "dep-open", Type: "parent-child"},
					{IssueID: "b6", DependsOnID: "dep-open", Type: "blocks"},
				},
			},
			want: true,
		},
		{
			name: "related dep on open bead is non-blocking, returns false",
			bead: protocol.Bead{
				ID: "b7",
				Dependencies: []protocol.Dependency{
					{IssueID: "b7", DependsOnID: "dep-open", Type: "related"},
				},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := hasUnresolvedBlockingDep(tc.bead, openSet)
			if got != tc.want {
				t.Errorf("hasUnresolvedBlockingDep(%q, openSet) = %v, want %v", tc.bead.ID, got, tc.want)
			}
		})
	}
}
