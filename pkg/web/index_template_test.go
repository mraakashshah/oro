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
	HealthErr string
	Parade    struct{}
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
		"index.html":  {Data: indexContent},
		"parade.html": {Data: []byte(`{{define "parade-content"}}PARADE-STUB{{end}}`)},
	}

	// Parse without a custom FuncMap — index.html must not use FuncMap helpers.
	tmpl, err := template.New("").ParseFS(tFS, "index.html", "parade.html")
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
			"PARADE-STUB",   // stub "parade-content" rendered inside #parade
			"/events",       // SSE endpoint wired
			"parade-update", // SSE trigger for parade panel
			"worker-update", // SSE trigger for worker grid
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
