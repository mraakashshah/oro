package web_test

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	healthErr  error
	events     []protocol.Event
	throughput *web.ThroughputData
	epics      *web.EpicsData
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
	return nil, &protocol.BeadNotFoundError{BeadID: id}
}

func (m *mockDashboard) HealthError() error {
	return m.healthErr
}

func (m *mockDashboard) Workers(_ context.Context) ([]web.WorkerInfo, error) {
	return m.workers, nil
}

func (m *mockDashboard) RecentEvents(_ context.Context, _ int) ([]protocol.Event, error) {
	return m.events, nil
}

func (m *mockDashboard) SubscribeSSE() chan string {
	ch := make(chan string, 1)
	return ch
}

func (m *mockDashboard) UnsubscribeSSE(_ chan string) {}

func (m *mockDashboard) Throughput(_ context.Context) (*web.ThroughputData, error) {
	return m.throughput, nil
}

func (m *mockDashboard) Epics(_ context.Context) (*web.EpicsData, error) {
	return m.epics, nil
}

// testTemplates returns a minimal fs.FS with all required templates.
func testTemplates() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html>
<html>
<body>
<header class="dashboard-header">{{if .HealthErr}}<span>Needs you</span><span id="health-error">{{.HealthErr}}</span>{{else}}<span>Healthy</span><span>{{.Throughput.BeadsPerHour}} beads/hr</span><span>{{.Throughput.CostPerHour}} cost/hr</span><span>{{.Throughput.ActiveWorkers}}/{{.Throughput.TotalWorkers}} workers</span><span>{{.Throughput.Uptime}} uptime</span>{{end}}</header>
<div id="epics">{{template "epics.html" .Epics}}</div>
<div id="title-map">{{index .Titles "oro-r1"}}{{index .Titles "oro-ip1"}}</div>
<div id="sidebar">{{template "workers.html" .Workers}}{{template "events.html" .Events}}</div>
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
		"detail.html": &fstest.MapFile{
			Data: []byte(`<div class="bead-detail" id="{{.ID}}">
<h2>{{.Title}}</h2>
<div class="detail-meta">
<span class="status">Status: {{.Status}}</span>
</div>
{{if .Description}}<div class="detail-description">{{.Description}}</div>{{end}}
{{if .AcceptanceCriteria}}<pre class="detail-ac">{{.AcceptanceCriteria}}</pre>{{end}}
{{if .Dependencies}}<div class="detail-deps">
Dependencies:
{{range .Dependencies}}<div class="dep-item">{{.DependsOnID}}</div>{{end}}
</div>{{end}}
{{if .WorkerID}}<div class="detail-worker">Worker: {{.WorkerID}} ({{.ContextPercent}}%)</div>{{end}}
</div>`),
		},
		"events.html": &fstest.MapFile{
			Data: []byte(`{{define "events.html"}}<div class="event-feed">{{range .Events}}<div class="event-feed__item"><span class="event-feed__time">{{if gt (len .CreatedAt) 15}}{{slice .CreatedAt 11 16}}{{else}}{{.CreatedAt}}{{end}}</span><span class="event-feed__symbol">{{.Type}}</span>{{if .BeadID}}<span class="event-feed__text">{{.BeadID}}</span>{{end}}</div>{{end}}</div>{{end}}`),
		},
		"throughput.html": &fstest.MapFile{
			Data: []byte(`{{define "throughput.html"}}<div class="throughput"><div class="throughput__stat"><div class="throughput__value">{{.BeadsPerHour}}</div><div class="throughput__label">Beads / hour</div></div><div class="throughput__stat"><div class="throughput__value">{{.CostPerHour}}</div><div class="throughput__label">Cost / hour</div></div><div class="throughput__stat"><div class="throughput__value">{{.ActiveWorkers}}/{{.TotalWorkers}}</div><div class="throughput__label">Workers active</div></div><div class="throughput__stat"><div class="throughput__value">{{.Uptime}}</div><div class="throughput__label">Uptime</div></div></div>{{end}}`),
		},
		"needs-you.html": &fstest.MapFile{
			Data: []byte(`{{define "needs-you.html"}}{{if .HealthErr}}<div id="needs-you">Needs you {{.HealthErr}}</div>{{else}}<div id="needs-you">Healthy</div>{{end}}{{end}}`),
		},
		"epics.html": &fstest.MapFile{
			Data: []byte(`{{define "epics.html"}}<section class="epics">{{range .InProgress}}<article class="epic-card" data-id="{{.ID}}"><h2>{{.Title}}</h2>{{.ActiveChildTitle}}</article>{{end}}{{range .Next}}<article class="epic-card epic-card--next" data-id="{{.ID}}"><h2>{{.Title}}</h2>{{.FirstBlockerTitle}}</article>{{end}}</section>{{end}}`),
		},
		"workers.html": &fstest.MapFile{
			Data: []byte(`{{define "workers.html"}}{{range .Workers}}<div class="worker-row state-{{.State}}" data-id="{{.ID}}">
<span class="worker-row__dot worker-row__dot--{{.State}}"></span>
<span class="worker-row__id">{{.ID}}</span>
<span class="worker-row__bead">{{if ne .BeadID ""}}{{titleFor $.Titles .BeadID}}{{else}}idle{{end}}</span>
<span class="worker-row__context">{{.ContextPct}}%</span>
<span class="worker-row__heartbeat">{{printf "%.0fs ago" .LastHeartbeatSecs}}</span>
</div>{{end}}{{end}}`),
		},
	}
}

func TestFullPageRender(t *testing.T) {
	data := &mockDashboard{
		ready:      []protocol.Bead{{ID: "oro-r1", Title: "Ready bead"}},
		inProgress: []protocol.Bead{{ID: "oro-ip1", Title: "In progress"}},
		workers:    []web.WorkerInfo{{ID: "worker-1", State: "busy", BeadID: "oro-ip1", ContextPct: 42}},
		events:     []protocol.Event{{Type: "merged", BeadID: "oro-ip1", CreatedAt: "2026-04-22T12:01:00Z"}},
		throughput: &web.ThroughputData{BeadsPerHour: 3, CostPerHour: "—", ActiveWorkers: 1, TotalWorkers: 2, Uptime: "5m"},
		epics:      &web.EpicsData{InProgress: []web.EpicSummary{{ID: "oro-epic-active", Title: "Active epic"}}},
	}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "sidebar") {
		t.Errorf("GET / body missing 'sidebar'; got: %q", body)
	}
	for _, want := range []string{"worker-1", "oro-ip1", "Healthy", "3 beads/hr", "Active epic", "Ready bead", "In progress"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / body missing %q; got: %q", want, body)
		}
	}
}

func TestPageLevelEpicLayout(t *testing.T) {
	data := &mockDashboard{
		ready: []protocol.Bead{{
			ID:     "oro-orphan",
			Title:  "Loose orphan bead",
			Status: "open",
		}},
		inProgress: []protocol.Bead{{
			ID:     "oro-child-active",
			Title:  "Active epic child",
			Status: "in_progress",
			Epic:   "oro-epic-active",
		}},
		throughput: &web.ThroughputData{},
		epics: &web.EpicsData{
			InProgress: []web.EpicSummary{{
				ID:               "oro-epic-active",
				Title:            "Ship page-level epics",
				Status:           "in_progress",
				ClosedChildren:   2,
				TotalChildren:    5,
				ActiveChildTitle: "Active epic child",
			}},
			Next: []web.EpicSummary{{
				ID:                "oro-epic-next",
				Title:             "Prepare next epic",
				Status:            "open",
				FirstBlockerTitle: "Resolve first blocker",
			}},
		},
	}
	h := web.NewHandler(data, web.Content)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"NEXT EPICS", "Ship page-level epics", "Prepare next epic"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / body missing %q; body:\n%s", want, body)
		}
	}
	if !regexp.MustCompile(`[0-9]+ / [0-9]+`).MatchString(body) {
		t.Errorf("GET / body missing epic rollup; body:\n%s", body)
	}
	for _, banned := range []string{"Queued Up", "Rolling", "Stalled", "bead-string", "shimmer"} {
		if strings.Contains(body, banned) {
			t.Errorf("GET / body contains old parade marker %q; body:\n%s", banned, body)
		}
	}
	epicsTag := regexp.MustCompile(`(?s)<div id="epics"[^>]*>`).FindString(body)
	if epicsTag == "" {
		t.Fatalf("GET / body missing #epics container; body:\n%s", body)
	}
	if !strings.Contains(epicsTag, `hx-trigger="parade-update from:body"`) {
		t.Fatalf("#epics hx-trigger must reuse dashboard SSE event name; tag: %s", epicsTag)
	}
}

func TestEpicsFragment(t *testing.T) {
	t.Run("returns epics html from dashboard data", func(t *testing.T) {
		data := &mockDashboard{
			epics: &web.EpicsData{
				InProgress: []web.EpicSummary{{
					ID:               "oro-epic-active",
					Title:            "Ship epics panel",
					ActiveChildTitle: "Wire handler",
				}},
				Next: []web.EpicSummary{{
					ID:                "oro-epic-next",
					Title:             "Next epic",
					Status:            "blocked",
					FirstBlockerTitle: "Finish blocker",
				}},
			},
		}
		h := web.NewHandler(data, testTemplates())

		req := httptest.NewRequest(http.MethodGet, "/fragments/epics", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /fragments/epics status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"epics", "Ship epics panel", "Wire handler", "Next epic", "Finish blocker"} {
			if !strings.Contains(body, want) {
				t.Errorf("GET /fragments/epics body missing %q; body: %q", want, body)
			}
		}
	})

	t.Run("nil epics renders empty epics html", func(t *testing.T) {
		h := web.NewHandler(&mockDashboard{}, testTemplates())

		req := httptest.NewRequest(http.MethodGet, "/fragments/epics", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /fragments/epics status = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "epics") {
			t.Errorf("GET /fragments/epics body missing epics container; body: %q", rec.Body.String())
		}
	})
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

func TestFragmentDetailNilShowsNotFound(t *testing.T) {
	data := &mockDashboard{
		detail: map[string]*protocol.BeadDetail{
			"oro-nil": nil,
		},
	}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/fragments/detail/oro-nil", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /fragments/detail/oro-nil status = %d, want 404", rec.Code)
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

func TestFragmentDetailSystemError(t *testing.T) {
	// ShowBead returns a system error (not BeadNotFoundError) → expect 500.
	data := &mockDashboard{
		detail: map[string]*protocol.BeadDetail{}, // empty, but we override ShowBead below
	}
	// Use a wrapper that returns a system error for a specific ID.
	h := web.NewHandler(&systemErrorDashboard{mockDashboard: data}, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/fragments/detail/oro-sys-err", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /fragments/detail/oro-sys-err status = %d, want 500", rec.Code)
	}
}

// systemErrorDashboard wraps mockDashboard but returns a system error from ShowBead.
type systemErrorDashboard struct {
	*mockDashboard
}

func (s *systemErrorDashboard) ShowBead(_ context.Context, _ string) (*protocol.BeadDetail, error) {
	return nil, errors.New("database connection failed")
}

// eventsErrorDashboard wraps mockDashboard but returns a system error from RecentEvents.
type eventsErrorDashboard struct {
	*mockDashboard
}

func (e *eventsErrorDashboard) RecentEvents(_ context.Context, _ int) ([]protocol.Event, error) {
	return nil, errors.New("event store unavailable")
}

func TestIndexPathGuard(t *testing.T) {
	data := &mockDashboard{}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nonexistent status = %d, want 404", rec.Code)
	}
}

func TestIndexHealthError(t *testing.T) {
	data := &mockDashboard{
		healthErr: errors.New("database unreachable"),
	}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "database unreachable") {
		t.Errorf("GET / with health error should contain error message; got: %q", body)
	}
}

func TestFragmentThroughput(t *testing.T) {
	data := &mockDashboard{
		throughput: &web.ThroughputData{
			BeadsPerHour:  3,
			ActiveWorkers: 2,
			TotalWorkers:  4,
			Uptime:        "2h 14m",
			CostPerHour:   "—",
		},
	}
	h := web.NewHandler(data, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/fragments/throughput", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /fragments/throughput status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"Beads / hour", ">3<", ">2/4<", "2h 14m", "—"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; body: %q", want, body)
		}
	}
}

// throughputErrorDashboard wraps mockDashboard but returns a system error from Throughput.
type throughputErrorDashboard struct {
	*mockDashboard
}

func (d *throughputErrorDashboard) Throughput(_ context.Context) (*web.ThroughputData, error) {
	return nil, errors.New("metrics unavailable")
}

func TestFragmentThroughputError(t *testing.T) {
	data := &mockDashboard{}
	h := web.NewHandler(&throughputErrorDashboard{mockDashboard: data}, testTemplates())

	req := httptest.NewRequest(http.MethodGet, "/fragments/throughput", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /fragments/throughput status = %d, want 500", rec.Code)
	}
}

func TestTemplateErrorReturnsClean500(t *testing.T) {
	// Build a template that writes partial HTML then returns an error.
	// Before the buffer fix: ExecuteTemplate streams to ResponseWriter, so
	// the partial bytes land with implicit status 200; http.Error can no
	// longer change the status code.  After the fix: execution goes to a
	// bytes.Buffer and the ResponseWriter is never touched on error.
	brokenTmpl := template.Must(
		template.New("broken.html").Funcs(template.FuncMap{
			"failHere": func() (string, error) {
				return "", errors.New("injected mid-render error")
			},
		}).Parse(`<html><body>partial content {{failHere}}</body></html>`),
	)

	data := &mockDashboard{}
	h := web.HandlerForTemplate(data, brokenTmpl, "broken.html")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html>") {
		t.Errorf("response body contains partial HTML after template error: %q", body)
	}
}

func TestFragmentEvents(t *testing.T) {
	t.Run("returns 200 with event data", func(t *testing.T) {
		data := &mockDashboard{
			events: []protocol.Event{
				{Type: "merged", BeadID: "oro-a1", CreatedAt: "2025-01-15T14:30:00Z"},
				{Type: "quality_gate_rejected", BeadID: "oro-b2", CreatedAt: "2025-01-15T09:05:00Z"},
				{Type: "escalation", BeadID: "", CreatedAt: "2025-01-15T22:00:00Z"},
			},
		}
		h := web.NewHandler(data, testTemplates())

		req := httptest.NewRequest(http.MethodGet, "/fragments/events", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /fragments/events status = %d, want 200", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", ct)
		}
		body := rec.Body.String()
		for _, want := range []string{"14:30", "09:05", "22:00", "merged", "quality_gate_rejected", "escalation", "oro-a1", "oro-b2"} {
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q; body: %q", want, body)
			}
		}
	})

	t.Run("empty events renders container", func(t *testing.T) {
		data := &mockDashboard{events: []protocol.Event{}}
		h := web.NewHandler(data, testTemplates())

		req := httptest.NewRequest(http.MethodGet, "/fragments/events", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /fragments/events status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "event-feed") {
			t.Errorf("body missing events container; got: %q", body)
		}
	})

	t.Run("RecentEvents error returns 500", func(t *testing.T) {
		data := &mockDashboard{}
		h := web.NewHandler(&eventsErrorDashboard{mockDashboard: data}, testTemplates())

		req := httptest.NewRequest(http.MethodGet, "/fragments/events", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("GET /fragments/events status = %d, want 500", rec.Code)
		}
	})
}
