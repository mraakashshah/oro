package web_test

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"

	"oro/pkg/protocol"
	"oro/pkg/web"
)

func TestEventSummaryUsesTitle(t *testing.T) {
	fsys := os.DirFS("templates")
	tmpl, err := template.New("").Funcs(web.TemplateFuncMap()).ParseFS(fsys, "events.html")
	if err != nil {
		t.Fatalf("parse events.html: %v", err)
	}

	render := func(t *testing.T, titles map[string]string) string {
		t.Helper()
		var buf bytes.Buffer
		data := web.EventsData{
			Events: []protocol.Event{{Type: "merged", BeadID: "oro-x"}},
			Titles: titles,
		}
		if err := tmpl.ExecuteTemplate(&buf, "events.html", data); err != nil {
			t.Fatalf("execute events.html: %v", err)
		}
		return buf.String()
	}

	t.Run("known id uses title", func(t *testing.T) {
		html := render(t, map[string]string{"oro-x": "Add cards show"})
		if !strings.Contains(html, "Add cards show") {
			t.Fatalf("event summary missing title; html: %s", html)
		}
	})

	t.Run("unknown id falls back to id", func(t *testing.T) {
		html := render(t, nil)
		if !strings.Contains(html, "oro-x") {
			t.Fatalf("event summary missing fallback id; html: %s", html)
		}
	})
}
