package web_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"oro/pkg/protocol"
	"oro/pkg/web"
)

// TestDetailTemplate validates that detail.html template renders all BeadDetail fields correctly.
func TestDetailTemplate(t *testing.T) {
	testCases := []struct {
		name      string
		detail    *protocol.BeadDetail
		wantStrs  []string
		noWantStr []string // strings that should NOT appear
	}{
		{
			name: "full bead with all fields",
			detail: &protocol.BeadDetail{
				ID:                 "oro-x1",
				Title:              "Test bead",
				Status:             "in_progress",
				Description:        "This is a test description",
				AcceptanceCriteria: "Test: foo\nAssert: bar",
				Dependencies: []protocol.Dependency{
					{IssueID: "oro-x1", DependsOnID: "oro-x2", Type: "blocks"},
				},
				WorkerID:       "w1",
				ContextPercent: 42,
			},
			wantStrs: []string{
				"oro-x1",                     // ID
				"Test bead",                  // Title
				"in_progress",                // Status
				"This is a test description", // Description
				"Test: foo",                  // AcceptanceCriteria (at least part of it)
				"oro-x2",                     // Dependencies
				"w1",                         // WorkerID
				"42",                         // ContextPercent
			},
		},
		{
			name: "minimal bead with empty optional fields",
			detail: &protocol.BeadDetail{
				ID:                 "oro-x2",
				Title:              "Minimal bead",
				Status:             "open",
				Description:        "",
				AcceptanceCriteria: "Test: required",
				WorkerID:           "",
				ContextPercent:     0,
			},
			wantStrs: []string{
				"oro-x2",         // ID
				"Minimal bead",   // Title
				"open",           // Status
				"Test: required", // AcceptanceCriteria
			},
			noWantStr: []string{
				"Worker:", // Worker section should not appear
			},
		},
		{
			name: "bead without dependencies",
			detail: &protocol.BeadDetail{
				ID:                 "oro-x3",
				Title:              "No deps bead",
				Status:             "blocked",
				Description:        "No dependencies",
				AcceptanceCriteria: "Test: x",
				Dependencies:       nil,
				WorkerID:           "",
				ContextPercent:     0,
			},
			wantStrs: []string{
				"oro-x3",
				"No deps bead",
				"blocked",
				"No dependencies",
				"Test: x",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Use testTemplates() which includes the full detail.html template
			data := &mockDashboard{
				detail: map[string]*protocol.BeadDetail{
					tc.detail.ID: tc.detail,
				},
			}
			h := web.NewHandler(data, testTemplates())

			// Test the detail fragment endpoint
			req := httptest.NewRequest("GET", "/fragments/detail/"+tc.detail.ID, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != 200 {
				t.Fatalf("GET /fragments/detail/%s status = %d, want 200", tc.detail.ID, rec.Code)
			}

			body := rec.Body.String()

			// Check for required strings
			for _, want := range tc.wantStrs {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q; got: %q", want, body)
				}
			}

			// Check that unwanted strings are NOT present
			for _, noWant := range tc.noWantStr {
				if strings.Contains(body, noWant) {
					t.Errorf("body should not contain %q; got: %q", noWant, body)
				}
			}
		})
	}
}
