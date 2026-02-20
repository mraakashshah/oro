package main

import (
	"testing"

	"oro/pkg/protocol"
)

func TestSearchModel_Filter(t *testing.T) {
	testBeads := []protocol.Bead{
		{ID: "oro-abc.1", Title: "Fix authentication bug", Status: "open", Priority: 0, Type: "bug"},
		{ID: "oro-abc.2", Title: "Add user dashboard", Status: "in_progress", Priority: 1, Type: "feature"},
		{ID: "oro-xyz.3", Title: "Refactor auth module", Status: "open", Priority: 2, Type: "task"},
		{ID: "oro-xyz.4", Title: "Database migration script", Status: "done", Priority: 3, Type: "task"},
	}

	tests := []struct {
		name     string
		query    string
		expected []string // expected IDs in result
	}{
		{
			name:     "empty query shows all beads",
			query:    "",
			expected: []string{"oro-abc.1", "oro-abc.2", "oro-xyz.3", "oro-xyz.4"},
		},
		{
			name:     "fuzzy match on ID",
			query:    "abc",
			expected: []string{"oro-abc.1", "oro-abc.2"},
		},
		{
			name:     "fuzzy match on title",
			query:    "auth",
			expected: []string{"oro-abc.1", "oro-xyz.3"},
		},
		{
			name:     "fuzzy match case insensitive",
			query:    "AUTH",
			expected: []string{"oro-abc.1", "oro-xyz.3"},
		},
		{
			name:     "filter by priority p:0",
			query:    "p:0",
			expected: []string{"oro-abc.1"},
		},
		{
			name:     "filter by status s:open",
			query:    "s:open",
			expected: []string{"oro-abc.1", "oro-xyz.3"},
		},
		{
			name:     "filter by type t:bug",
			query:    "t:bug",
			expected: []string{"oro-abc.1"},
		},
		{
			name:     "filter by type t:task",
			query:    "t:task",
			expected: []string{"oro-xyz.3", "oro-xyz.4"},
		},
		{
			name:     "combine filter and fuzzy: p:0 + auth",
			query:    "p:0 auth",
			expected: []string{"oro-abc.1"},
		},
		{
			name:     "combine filter and fuzzy: s:in_progress + dash",
			query:    "s:in_progress dash",
			expected: []string{"oro-abc.2"},
		},
		{
			name:     "no matches returns empty",
			query:    "nonexistent",
			expected: []string{},
		},
		{
			name:     "multiple filters: s:open t:bug",
			query:    "s:open t:bug",
			expected: []string{"oro-abc.1"},
		},
	}

	sm := SearchModel{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sm.Filter(testBeads, tt.query)

			// Check length
			if len(result) != len(tt.expected) {
				t.Errorf("Filter() returned %d beads, want %d", len(result), len(tt.expected))
			}

			// Check IDs match expected
			resultIDs := make(map[string]bool)
			for _, bead := range result {
				resultIDs[bead.ID] = true
			}

			for _, expectedID := range tt.expected {
				if !resultIDs[expectedID] {
					t.Errorf("Filter() missing expected bead %s", expectedID)
				}
			}
		})
	}
}
