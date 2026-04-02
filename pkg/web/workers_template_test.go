package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"oro/pkg/web"
)

// TestWorkersTemplate verifies that workers.html template renders []web.WorkerInfo
// with state indicators, context percentage bar, and bead IDs.
func TestWorkersTemplate(t *testing.T) {
	// Test data: one busy worker with a bead, one idle worker
	data := &mockDashboard{
		workers: []web.WorkerInfo{
			{
				ID:         "worker-1",
				State:      "busy",
				BeadID:     "oro-ip1",
				ContextPct: 42,
			},
			{
				ID:         "worker-2",
				State:      "idle",
				BeadID:     "",
				ContextPct: 0,
			},
		},
	}

	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/fragments/workers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fragments/workers status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()

	// Assertions per acceptance criteria:
	// Each worker row contains: .ID, state indicator, .ContextPct with %, .BeadID when non-empty
	assertions := []struct {
		name string
		want string
	}{
		// Worker IDs
		{"busy worker ID", "worker-1"},
		{"idle worker ID", "worker-2"},
		// State indicators (● for busy, ○ for idle)
		{"busy indicator bullet", "●"},
		{"idle indicator circle", "○"},
		// Context percentages with % suffix
		{"busy context pct", "42%"},
		{"idle context pct", "0%"},
		// BeadID for busy worker
		{"busy bead ID", "oro-ip1"},
		// "idle" text for worker with no BeadID (instead of empty)
		{"idle text when no bead", "idle"},
		// State class on worker-row
		{"busy state class", `state-busy`},
		{"idle state class", `state-idle`},
		// Context bar with width style
		{"busy context bar width", `style="width:42%"`},
		{"idle context bar width", `style="width:0%"`},
	}

	for _, a := range assertions {
		if !strings.Contains(body, a.want) {
			t.Errorf("assertion %q failed: body missing %q\nGot:\n%s", a.name, a.want, body)
		}
	}
}
