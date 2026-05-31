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
	Workers    []struct{}
	Events     []struct{}
	Throughput struct{}
}

// TestIndexTemplate validates that templates/index.html is a complete page shell
// with SSE and htmx wiring per the acceptance criteria.
//
// Uses fstest.MapFS with the real index.html content + a stub parade.html so the
// test exercises the actual template file without pulling in the full parade template.
func TestIndexTemplate(t *testing.T) {
	// Read the real index.html from the templates directory.
	indexContent, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read templates/index.html: %v", err)
	}

	// Build a MapFS: real index.html + stub parade.html defining "parade-content".
	tFS := fstest.MapFS{
		"index.html":      {Data: indexContent},
		"parade.html":     {Data: []byte(`{{define "parade-content"}}PARADE-STUB{{end}}`)},
		"workers.html":    {Data: []byte(`{{define "workers.html"}}WORKERS-STUB{{end}}`)},
		"events.html":     {Data: []byte(`{{define "events.html"}}EVENTS-STUB{{end}}`)},
		"throughput.html": {Data: []byte(`{{define "throughput.html"}}THROUGHPUT-STUB{{end}}`)},
	}

	// Parse without a custom FuncMap — index.html must not use FuncMap helpers.
	tmpl, err := template.New("").ParseFS(tFS, "index.html", "parade.html", "workers.html", "events.html", "throughput.html")
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
		body := render(t, indexTemplateData{HealthErr: "", Parade: struct{}{}})

		wants := []string{
			"<!DOCTYPE html>",
			"<html",
			"/static/style.css",
			"/static/htmx.min.js",
			`id="parade"`,
			`id="sidebar"`,
			"PARADE-STUB", // stub "parade-content" rendered inside #parade
			"WORKERS-STUB",
			"EVENTS-STUB",
			"THROUGHPUT-STUB",
			"/events", // SSE endpoint wired
			"dashboard-parade",
			"dashboard-workers",
			"dashboard-events",
			"dashboard-throughput",
			"EventSource",
			"parade-update",
			"worker-update",
			"new-event",
			"throughput-update",
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
		"parade.html":     {Data: []byte(`{{define "parade-content"}}PARADE-STUB{{end}}`)},
		"workers.html":    {Data: []byte(`{{define "workers.html"}}WORKERS-STUB{{end}}`)},
		"events.html":     {Data: []byte(`{{define "events.html"}}EVENTS-STUB{{end}}`)},
		"throughput.html": {Data: []byte(`{{define "throughput.html"}}THROUGHPUT-STUB{{end}}`)},
	}
	tmpl, err := template.New("").ParseFS(tFS, "index.html", "parade.html", "workers.html", "events.html", "throughput.html")
	if err != nil {
		t.Fatalf("template.ParseFS: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "index.html", indexTemplateData{}); err != nil {
		t.Fatalf("execute index.html: %v", err)
	}
	body := buf.String()

	paradeStart := strings.Index(body, `id="parade"`)
	if paradeStart < 0 {
		t.Fatalf("rendered index missing #parade; body:\n%s", body)
	}
	paradeClose := strings.Index(body[paradeStart:], "</div>")
	if paradeClose < 0 {
		t.Fatalf("rendered index missing #parade closing div; body:\n%s", body)
	}
	paradeClose += paradeStart

	detailStart := strings.Index(body, `id="detail"`)
	if detailStart < 0 {
		t.Fatalf("rendered index missing #detail slide-over host; body:\n%s", body)
	}
	if detailStart < paradeClose {
		t.Fatalf("#detail is inside the SSE-swapped #parade container; body:\n%s", body)
	}
	if strings.Contains(body[paradeStart:paradeClose], `id="detail"`) {
		t.Fatalf("#detail must not be a descendant of #parade; body:\n%s", body)
	}
}
