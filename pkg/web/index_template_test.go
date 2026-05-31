package web_test

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// indexTemplateData mirrors indexData for use in external test package.
// Go templates access fields by name via reflection, so field names must match.
type indexTemplateData struct {
	HealthErr  string
	Parade     struct{}
	Epics      struct{}
	Workers    []struct{}
	Events     []struct{}
	Throughput struct {
		BeadsPerHour  int
		ActiveWorkers int
		TotalWorkers  int
		Uptime        string
		CostPerHour   string
	}
}

// TestIndexTemplate validates that templates/index.html is a complete page shell
// with SSE and htmx wiring per the acceptance criteria.
//
// Uses fstest.MapFS with the real index.html content + stub partials so the
// test exercises the actual template file without pulling in full fragments.
func TestIndexTemplate(t *testing.T) {
	// Read the real index.html from the templates directory.
	indexContent, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read templates/index.html: %v", err)
	}

	tFS := fstest.MapFS{
		"index.html":      {Data: indexContent},
		"epics.html":      {Data: []byte(`{{define "epics.html"}}EPICS-STUB{{end}}`)},
		"workers.html":    {Data: []byte(`{{define "workers.html"}}WORKERS-STUB{{end}}`)},
		"events.html":     {Data: []byte(`{{define "events.html"}}EVENTS-STUB{{end}}`)},
		"needs-you.html":  {Data: []byte(`{{define "needs-you.html"}}{{if .HealthErr}}<div id="needs-you">Needs you</div><div id="health-error">{{.HealthErr}}</div>{{else}}<div id="needs-you">Healthy</div>{{end}}{{end}}`)},
		"throughput.html": {Data: []byte(`{{define "throughput.html"}}THROUGHPUT-STUB{{end}}`)},
	}

	// Parse without a custom FuncMap — index.html must not use FuncMap helpers.
	tmpl, err := template.New("").ParseFS(tFS, "index.html", "epics.html", "workers.html", "events.html", "needs-you.html", "throughput.html")
	if err != nil {
		t.Fatalf("template.ParseFS: %v", err)
	}

	render := func(t *testing.T, data indexTemplateData) string {
		t.Helper()
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "index.html", data); err != nil {
			t.Fatalf("execute index.html: %v", err)
		}
		return buf.String()
	}

	t.Run("structural elements", func(t *testing.T) {
		body := render(t, indexTemplateData{
			HealthErr: "",
			Parade:    struct{}{},
			Throughput: struct {
				BeadsPerHour  int
				ActiveWorkers int
				TotalWorkers  int
				Uptime        string
				CostPerHour   string
			}{
				BeadsPerHour:  4,
				ActiveWorkers: 2,
				TotalWorkers:  3,
				Uptime:        "1h",
				CostPerHour:   "$0.50",
			},
		})

		wants := []string{
			"<!DOCTYPE html>",
			"<html",
			"/static/style.css",
			"/static/htmx.min.js",
			"/static/dash.js",
			`class="dashboard-header"`,
			"Healthy",
			"4",
			"beads/hr",
			"$0.50",
			"cost/hr",
			"2/3",
			"workers",
			"1h",
			"uptime",
			`id="epics"`,
			`id="sidebar"`,
			"EPICS-STUB",
			"WORKERS-STUB",
			"EVENTS-STUB",
			"/events", // SSE endpoint wired
			"EventSource",
			"parade-update",
			"worker-update",
			"new-event",
			"throughput-update",
			`data-dashboard-search`,
		}
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q;\ngot:\n%s", want, body)
			}
		}
	})

	t.Run("empty HealthErr no error banner", func(t *testing.T) {
		body := render(t, indexTemplateData{HealthErr: ""})
		if strings.Contains(body, "health-error") {
			t.Errorf("empty HealthErr should not render error banner;\ngot:\n%s", body)
		}
	})

	t.Run("health error banner shows message", func(t *testing.T) {
		body := render(t, indexTemplateData{HealthErr: "db down"})
		if !strings.Contains(body, "db down") {
			t.Errorf("HealthErr='db down' missing from output;\ngot:\n%s", body)
		}
		if !strings.Contains(body, "health-error") {
			t.Errorf("health-error banner element missing;\ngot:\n%s", body)
		}
	})
}

func TestDetailSlideOverOutsideSwapTarget(t *testing.T) {
	indexContent, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read templates/index.html: %v", err)
	}

	tFS := fstest.MapFS{
		"index.html":      {Data: indexContent},
		"epics.html":      {Data: []byte(`{{define "epics.html"}}EPICS-STUB{{end}}`)},
		"workers.html":    {Data: []byte(`{{define "workers.html"}}WORKERS-STUB{{end}}`)},
		"events.html":     {Data: []byte(`{{define "events.html"}}EVENTS-STUB{{end}}`)},
		"needs-you.html":  {Data: []byte(`{{define "needs-you.html"}}{{if .HealthErr}}<div id="needs-you">Needs you</div><div id="health-error">{{.HealthErr}}</div>{{else}}<div id="needs-you">Healthy</div>{{end}}{{end}}`)},
		"throughput.html": {Data: []byte(`{{define "throughput.html"}}THROUGHPUT-STUB{{end}}`)},
	}
	tmpl, err := template.New("").ParseFS(tFS, "index.html", "epics.html", "workers.html", "events.html", "needs-you.html", "throughput.html")
	if err != nil {
		t.Fatalf("template.ParseFS: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "index.html", indexTemplateData{}); err != nil {
		t.Fatalf("execute index.html: %v", err)
	}
	body := buf.String()

	epicsStart := strings.Index(body, `id="epics"`)
	if epicsStart < 0 {
		t.Fatalf("rendered index missing #epics; body:\n%s", body)
	}
	epicsClose := strings.Index(body[epicsStart:], "</div>")
	if epicsClose < 0 {
		t.Fatalf("rendered index missing #epics closing div; body:\n%s", body)
	}
	epicsClose += epicsStart

	detailStart := strings.Index(body, `id="detail"`)
	if detailStart < 0 {
		t.Fatalf("rendered index missing #detail slide-over host; body:\n%s", body)
	}
	if detailStart < epicsClose {
		t.Fatalf("#detail is inside the SSE-swapped #epics container; body:\n%s", body)
	}
	if strings.Contains(body[epicsStart:epicsClose], `id="detail"`) {
		t.Fatalf("#detail must not be a descendant of #epics; body:\n%s", body)
	}
}
