package main

import (
	"testing"

	"oro/pkg/protocol"
)

func TestFuzzyMatchToleratesTypos(t *testing.T) {
	t.Run("levenshtein edge cases", func(t *testing.T) {
		if got := levenshtein("", "abc"); got != 3 {
			t.Errorf("levenshtein('', 'abc') = %d, want 3", got)
		}
		if got := levenshtein("abc", ""); got != 3 {
			t.Errorf("levenshtein('abc', '') = %d, want 3", got)
		}
		if got := levenshtein("", ""); got != 0 {
			t.Errorf("levenshtein('', '') = %d, want 0", got)
		}
		if got := levenshtein("dispatcer", "dispatcher"); got != 1 {
			t.Errorf("levenshtein('dispatcer', 'dispatcher') = %d, want 1", got)
		}
		if got := levenshtein("heartbeet", "heartbeat"); got != 1 {
			t.Errorf("levenshtein('heartbeet', 'heartbeat') = %d, want 1", got)
		}
	})

	t.Run("minEditDistance edge cases", func(t *testing.T) {
		if got := minEditDistance("", "anything"); got != 0 {
			t.Errorf("minEditDistance('', 'anything') = %d, want 0", got)
		}
		if got := minEditDistance("abc", ""); got != 3 {
			t.Errorf("minEditDistance('abc', '') = %d, want 3", got)
		}
		if got := minEditDistance("dispatcer", "fix dispatcher timeout"); got != 1 {
			t.Errorf("minEditDistance('dispatcer', 'fix dispatcher timeout') = %d, want 1", got)
		}
		if got := minEditDistance("heartbeet", "improve heartbeat monitoring"); got != 1 {
			t.Errorf("minEditDistance('heartbeet', 'improve heartbeat monitoring') = %d, want 1", got)
		}
	})

	t.Run("fuzzyMaxDistance thresholds", func(t *testing.T) {
		// 1-4 char terms: exact substring only (no false positives on short terms)
		for _, termLen := range []int{1, 2, 3, 4} {
			if got := fuzzyMaxDistance(termLen); got != 0 {
				t.Errorf("fuzzyMaxDistance(%d) = %d, want 0 (exact only)", termLen, got)
			}
		}
		// 5+ char terms: allow 1 edit
		for _, termLen := range []int{5, 9, 12} {
			if got := fuzzyMaxDistance(termLen); got < 1 {
				t.Errorf("fuzzyMaxDistance(%d) = %d, want >= 1", termLen, got)
			}
		}
	})

	testBeads := []protocol.Bead{
		{ID: "oro-aaa.1", Title: "Fix dispatcher timeout", Status: "open", Priority: 0, Type: "bug"},
		{ID: "oro-bbb.2", Title: "Improve heartbeat monitoring", Status: "open", Priority: 1, Type: "feature"},
		{ID: "oro-ccc.3", Title: "Update configuration file", Status: "open", Priority: 2, Type: "task"},
	}
	sm := SearchModel{}

	t.Run("dispatcer matches dispatcher (1 typo)", func(t *testing.T) {
		result := sm.Filter(testBeads, "dispatcer")
		if len(result) == 0 {
			t.Error("Filter('dispatcer') returned no results, want oro-aaa.1")
			return
		}
		found := false
		for _, b := range result {
			if b.ID == "oro-aaa.1" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Filter('dispatcer') missing expected bead oro-aaa.1, got %v", result)
		}
	})

	t.Run("heartbeet matches heartbeat (1 typo)", func(t *testing.T) {
		result := sm.Filter(testBeads, "heartbeet")
		if len(result) == 0 {
			t.Error("Filter('heartbeet') returned no results, want oro-bbb.2")
			return
		}
		found := false
		for _, b := range result {
			if b.ID == "oro-bbb.2" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Filter('heartbeet') missing expected bead oro-bbb.2, got %v", result)
		}
	})

	t.Run("zzzzz matches nothing", func(t *testing.T) {
		result := sm.Filter(testBeads, "zzzzz")
		if len(result) != 0 {
			t.Errorf("Filter('zzzzz') returned %d results, want 0: %v", len(result), result)
		}
	})

	t.Run("1-char term uses exact substring (no false positives)", func(t *testing.T) {
		// 'z' does not appear in any test bead
		result := sm.Filter(testBeads, "z")
		if len(result) != 0 {
			t.Errorf("Filter('z') returned %d results, want 0", len(result))
		}
	})

	t.Run("2-char term uses exact substring (no false positives)", func(t *testing.T) {
		// 'zz' does not appear in any test bead
		result := sm.Filter(testBeads, "zz")
		if len(result) != 0 {
			t.Errorf("Filter('zz') returned %d results, want 0", len(result))
		}
	})
}

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
