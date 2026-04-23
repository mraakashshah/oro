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

func TestParadeTemplate(t *testing.T) {
	fsys := os.DirFS("templates")
	tmpl, err := template.New("").Funcs(web.TemplateFuncMap()).ParseFS(fsys, "parade.html")
	if err != nil {
		t.Fatalf("parse parade.html: %v", err)
	}

	if tmpl.Lookup("parade-content") == nil {
		t.Fatal("parade.html must define a 'parade-content' block")
	}

	t.Run("four sections with all bead IDs", func(t *testing.T) {
		data := web.ParadeData{
			Ready: []protocol.Bead{
				{ID: "oro-r1", Title: "Ready bead", Status: "open", Priority: 1},
			},
			InProgress: []protocol.Bead{
				{ID: "oro-ip1", Title: "Rolling bead", Status: "in_progress", Priority: 2, Owner: "worker-7"},
			},
			Blocked: []protocol.Bead{
				{
					ID: "oro-b1", Title: "Blocked bead", Status: "blocked", Priority: 0,
					Dependencies: []protocol.Dependency{{IssueID: "oro-b1", DependsOnID: "oro-dep1"}},
				},
			},
			Closed: []protocol.Bead{
				{ID: "oro-c1", Title: "Closed bead", Status: "closed", Priority: 3},
			},
		}

		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "parade-content", data); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		html := buf.String()

		// Section headers with status symbols
		for _, want := range []string{
			"Queued Up", "♪",
			"Rolling", "●",
			"Stalled", "⊘",
			"Finished", "✓",
		} {
			if !strings.Contains(html, want) {
				t.Errorf("missing %q in output; html: %s", want, html)
			}
		}

		// All 4 bead IDs present
		for _, id := range []string{"oro-r1", "oro-ip1", "oro-b1", "oro-c1"} {
			if !strings.Contains(html, id) {
				t.Errorf("missing bead ID %q in output; html: %s", id, html)
			}
		}

		// data-id attributes on bead cards
		for _, id := range []string{"oro-r1", "oro-ip1", "oro-b1", "oro-c1"} {
			if !strings.Contains(html, `data-id="`+id+`"`) {
				t.Errorf("missing data-id=%q attribute; html: %s", id, html)
			}
		}

		// Worker badge shown for Rolling bead (has Owner)
		if !strings.Contains(html, "worker-7") {
			t.Errorf("missing worker badge for oro-ip1 (Owner=worker-7); html: %s", html)
		}

		// Blocker hint shown for Stalled bead (has Dependencies)
		if !strings.Contains(html, "oro-dep1") {
			t.Errorf("missing blocker hint (DependsOnID=oro-dep1); html: %s", html)
		}
	})

	t.Run("bead card has hx-get for click-to-expand", func(t *testing.T) {
		data := web.ParadeData{
			Ready: []protocol.Bead{{ID: "oro-r1", Title: "Ready bead"}},
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "parade-content", data); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		html := buf.String()
		if !strings.Contains(html, `hx-get="/fragments/detail/oro-r1"`) {
			t.Errorf("missing hx-get attribute on bead card; html: %s", html)
		}
		if !strings.Contains(html, "bead-detail-slot") {
			t.Errorf("missing bead-detail-slot; html: %s", html)
		}
	})

	t.Run("priority shown as badge", func(t *testing.T) {
		data := web.ParadeData{
			Ready: []protocol.Bead{{ID: "oro-r1", Title: "Ready bead", Priority: 5}},
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "parade-content", data); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		html := buf.String()
		if !strings.Contains(html, "badge--priority") {
			t.Errorf("missing priority badge class; html: %s", html)
		}
		if !strings.Contains(html, "5") {
			t.Errorf("priority value not shown; html: %s", html)
		}
	})

	t.Run("no worker badge when Owner is empty", func(t *testing.T) {
		data := web.ParadeData{
			Ready: []protocol.Bead{{ID: "oro-r1", Title: "Ready bead", Owner: ""}},
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "parade-content", data); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		html := buf.String()
		if strings.Contains(html, "badge--worker") {
			t.Errorf("worker badge rendered for bead with empty Owner; html: %s", html)
		}
	})

	t.Run("empty sections show headers but no bead cards", func(t *testing.T) {
		data := web.ParadeData{} // all slices nil/empty
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "parade-content", data); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		html := buf.String()

		// Section headers still present
		for _, want := range []string{"Queued Up", "Rolling", "Stalled", "Finished"} {
			if !strings.Contains(html, want) {
				t.Errorf("empty data missing section header %q; html: %s", want, html)
			}
		}
		// No bead cards rendered
		if strings.Contains(html, "bead-card") {
			t.Errorf("empty data rendered bead cards unexpectedly; html: %s", html)
		}
	})

	t.Run("no blocker hint when bead has no dependencies", func(t *testing.T) {
		data := web.ParadeData{
			Blocked: []protocol.Bead{{ID: "oro-b1", Title: "Blocked bead", Dependencies: nil}},
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "parade-content", data); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		html := buf.String()
		if strings.Contains(html, "badge--blocker") {
			t.Errorf("blocker hint rendered for bead with no dependencies; html: %s", html)
		}
	})
}
