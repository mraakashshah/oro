package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"oro/pkg/protocol"
	"oro/pkg/web"
)

// mockDashboard implements web.DashboardData for tests.
type mockDashboard struct {
	ready      []protocol.Bead
	inProgress []protocol.Bead
	blocked    []protocol.Bead
	closed     []protocol.Bead
	detail     map[string]*protocol.BeadDetail
	workers    []web.WorkerInfo
}

func (m *mockDashboard) ReadyBeads(_ context.Context) ([]protocol.Bead, error) {
	return m.ready, nil
}

func (m *mockDashboard) InProgressBeads(_ context.Context) ([]protocol.Bead, error) {
	return m.inProgress, nil
}

func (m *mockDashboard) BlockedBeads(_ context.Context) ([]protocol.Bead, error) {
	return m.blocked, nil
}

func (m *mockDashboard) ClosedBeads(_ context.Context, _ int) ([]protocol.Bead, error) {
	return m.closed, nil
}

func (m *mockDashboard) ShowBead(_ context.Context, id string) (*protocol.BeadDetail, error) {
	if m.detail != nil {
		if d, ok := m.detail[id]; ok {
			return d, nil
		}
	}
	return nil, fmt.Errorf("bead not found: %s", id)
}

func (m *mockDashboard) Workers(_ context.Context) ([]web.WorkerInfo, error) {
	return m.workers, nil
}

func (m *mockDashboard) SubscribeSSE() chan string {
	ch := make(chan string, 1)
	return ch
}

func (m *mockDashboard) UnsubscribeSSE(_ chan string) {}

// testTemplates returns a minimal fs.FS with all required templates.
func testTemplates() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html>
<html>
<body>
<div id="parade">{{template "parade-content" .}}</div>
<div id="sidebar">sidebar</div>
</body>
</html>`),
		},
		"parade.html": &fstest.MapFile{
			Data: []byte(`{{define "parade-content"}}
<section id="queued-up"><h2>Queued Up</h2>{{range .Ready}}<div class="bead">{{.ID}}</div>{{end}}</section>
<section id="rolling"><h2>Rolling</h2>{{range .InProgress}}<div class="bead">{{.ID}}</div>{{end}}</section>
<section id="stalled"><h2>Stalled</h2>{{range .Blocked}}<div class="bead">{{.ID}}</div>{{end}}</section>
<section id="finished"><h2>Finished</h2>{{range .Closed}}<div class="bead">{{.ID}}</div>{{end}}</section>
{{end}}`),
		},
		"workers.html": &fstest.MapFile{
			Data: []byte(`{{range .}}<div class="worker-row" data-id="{{.ID}}">
<span class="state">{{.State}}</span>
<span class="context-pct">{{.ContextPct}}%</span>
<span class="bead-id">{{.BeadID}}</span>
</div>{{end}}`),
		},
		"detail.html": &fstest.MapFile{
			Data: []byte(`<div class="bead-detail" id="{{.ID}}">
<h1>{{.Title}}</h1>
<p class="status">{{.Status}}</p>
<p class="description">{{.Description}}</p>
</div>`),
		},
	}
}

func TestFullPageRender(t *testing.T) {
	data := &mockDashboard{
		ready:      []protocol.Bead{{ID: "oro-r1", Title: "Ready bead"}},
		inProgress: []protocol.Bead{{ID: "oro-ip1", Title: "In progress"}},
	}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "parade") {
		t.Errorf("GET / body missing 'parade'; got: %q", body)
	}
	if !strings.Contains(body, "sidebar") {
		t.Errorf("GET / body missing 'sidebar'; got: %q", body)
	}
}

func TestFragmentParade(t *testing.T) {
	data := &mockDashboard{
		ready:      []protocol.Bead{{ID: "oro-r1", Title: "Ready bead"}},
		inProgress: []protocol.Bead{{ID: "oro-ip1", Title: "Rolling bead"}},
		blocked:    []protocol.Bead{{ID: "oro-b1", Title: "Blocked bead"}},
		closed:     []protocol.Bead{{ID: "oro-c1", Title: "Closed bead"}},
	}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/fragments/parade", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fragments/parade status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"Queued Up", "Rolling", "Stalled", "Finished",
		"oro-r1", "oro-ip1", "oro-b1", "oro-c1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /fragments/parade body missing %q; body: %q", want, body)
		}
	}
}

func TestFragmentWorkers(t *testing.T) {
	data := &mockDashboard{
		workers: []web.WorkerInfo{
			{ID: "worker-1", State: "busy", BeadID: "oro-ip1", ContextPct: 42},
			{ID: "worker-2", State: "idle", BeadID: "", ContextPct: 0},
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

	for _, want := range []string{"worker-1", "busy", "42%", "worker-2", "idle"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /fragments/workers body missing %q; body: %q", want, body)
		}
	}
}

func TestFragmentDetailFound(t *testing.T) {
	data := &mockDashboard{
		detail: map[string]*protocol.BeadDetail{
			"oro-x1": {ID: "oro-x1", Title: "Detail bead", Status: "in_progress", Description: "desc here"},
		},
	}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/fragments/detail/oro-x1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fragments/detail/oro-x1 status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"oro-x1", "Detail bead", "in_progress"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /fragments/detail/oro-x1 body missing %q; body: %q", want, body)
		}
	}
}

func TestFragmentDetailNotFound(t *testing.T) {
	data := &mockDashboard{}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/fragments/detail/no-such-id", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /fragments/detail/no-such-id status = %d, want 404", rec.Code)
	}
}

func TestSSEEndpoint(t *testing.T) {
	data := &mockDashboard{}
	h := web.NewHandler(data, testTemplates())

	// Use a cancelable request so the SSE handler exits.
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	cancel() // cancel immediately so handler returns

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("GET /events Content-Type = %q, want text/event-stream", ct)
	}
}
