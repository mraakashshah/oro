package data

import (
	"testing"
)

func TestFilterIssues(t *testing.T) {
	issues := []Issue{
		{ID: "vv-001", Title: "Fix login bug", IssueType: TypeBug, Priority: PriorityCritical},
		{ID: "vv-002", Title: "Add search feature", IssueType: TypeFeature, Priority: PriorityHigh},
		{ID: "vv-003", Title: "Update documentation", IssueType: TypeChore, Priority: PriorityLow},
		{ID: "vv-004", Title: "Refactor auth flow", IssueType: TypeTask, Priority: PriorityMedium},
	}

	tests := []struct {
		name     string
		query    string
		expected []string // expected issue IDs
	}{
		{
			name:     "Empty query",
			query:    "",
			expected: []string{"vv-001", "vv-002", "vv-003", "vv-004"},
		},
		{
			name:     "Free text partial word",
			query:    "login",
			expected: []string{"vv-001"},
		},
		{
			name:     "Free text case insensitive",
			query:    "search",
			expected: []string{"vv-002"},
		},
		{
			name:     "Free text matches ID",
			query:    "vv-003",
			expected: []string{"vv-003"},
		},
		{
			name:     "Type filter exact",
			query:    "type:bug",
			expected: []string{"vv-001"},
		},
		{
			name:     "Type filter and free text",
			query:    "type:feature search",
			expected: []string{"vv-002"},
		},
		{
			name:     "Type filter no match",
			query:    "type:epic",
			expected: []string{},
		},
		{
			name:     "Priority short label (p0)",
			query:    "p0",
			expected: []string{"vv-001"},
		},
		{
			name:     "Priority short label (p1)",
			query:    "P1",
			expected: []string{"vv-002"},
		},
		{
			name:     "Priority explicit number",
			query:    "priority:3",
			expected: []string{"vv-003"},
		},
		{
			name:     "Priority explicit name",
			query:    "priority:medium",
			expected: []string{"vv-004"},
		},
		{
			name:     "Multiple structured tokens combined",
			query:    "type:feature p1",
			expected: []string{"vv-002"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterIssues(issues, tt.query)
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d results, got %d", len(tt.expected), len(result))
			}

			// Verify IDs
			resMap := make(map[string]bool)
			for _, r := range result {
				resMap[r.ID] = true
			}
			for _, exp := range tt.expected {
				if !resMap[exp] {
					t.Errorf("expected issue %s to be in result, but it was not", exp)
				}
			}
		})
	}
}

func TestFilterIssuesFuzzy(t *testing.T) {
	issues := []Issue{
		{ID: "vv-001", Title: "Login token expiry bug", IssueType: TypeBug, Priority: PriorityCritical},
		{ID: "vv-002", Title: "Add search feature", IssueType: TypeFeature, Priority: PriorityHigh},
		{ID: "vv-003", Title: "Update documentation", IssueType: TypeChore, Priority: PriorityLow},
	}

	// Fuzzy matching: "lgn tkn" should match "Login token expiry bug"
	result := FilterIssues(issues, "lgn tkn")
	found := false
	for _, r := range result {
		if r.ID == "vv-001" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("fuzzy query 'lgn tkn' should match 'Login token expiry bug', got %d results", len(result))
	}
}

func TestFilterIssuesWithHighlights(t *testing.T) {
	issues := []Issue{
		{ID: "oro-001", Title: "Fix login flow", IssueType: TypeBug, Priority: PriorityCritical},
		{ID: "oro-002", Title: "Add search", IssueType: TypeFeature, Priority: PriorityHigh},
	}

	tests := []struct {
		name          string
		query         string
		wantIDs       []string
		wantHighlight string
	}{
		{
			name:    "empty query returns original issues without highlights",
			query:   " ",
			wantIDs: []string{"oro-001", "oro-002"},
		},
		{
			name:    "structured query returns matches without highlights",
			query:   "type:bug p0",
			wantIDs: []string{"oro-001"},
		},
		{
			name:          "free text returns title highlights",
			query:         "login",
			wantIDs:       []string{"oro-001"},
			wantHighlight: "oro-001",
		},
		{
			name:    "structured query with no candidates returns no result",
			query:   "type:chore login",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, highlights := FilterIssuesWithHighlights(issues, tt.query)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("FilterIssuesWithHighlights(%q) returned %d issues, want %d", tt.query, len(got), len(tt.wantIDs))
			}
			for i, wantID := range tt.wantIDs {
				if got[i].ID != wantID {
					t.Fatalf("FilterIssuesWithHighlights(%q)[%d] = %q, want %q", tt.query, i, got[i].ID, wantID)
				}
			}
			if tt.wantHighlight == "" {
				if len(highlights) != 0 {
					t.Fatalf("FilterIssuesWithHighlights(%q) highlights = %#v, want none", tt.query, highlights)
				}
				return
			}
			if len(highlights[tt.wantHighlight]) == 0 {
				t.Fatalf("FilterIssuesWithHighlights(%q) highlights = %#v, want title indices for %q", tt.query, highlights, tt.wantHighlight)
			}
		})
	}
}

func TestIsStructuredToken(t *testing.T) {
	tests := []struct {
		token    string
		expected bool
	}{
		{"type:bug", true},
		{"priority:high", true},
		{"p0", true},
		{"p4", true},
		{"p5", false},
		{"login", false},
		{"type", false},
	}

	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			if got := isStructuredToken(tt.token); got != tt.expected {
				t.Errorf("isStructuredToken(%q) = %v, want %v", tt.token, got, tt.expected)
			}
		})
	}
}
