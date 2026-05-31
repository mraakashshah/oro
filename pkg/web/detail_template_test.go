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

type detailTemplateData struct {
	*protocol.BeadDetail
	Children []protocol.Bead
}

// TestDetailTemplate validates that the templates/detail.html template renders
// *protocol.BeadDetail data with correct structure and conditional sections.
func TestDetailTemplate(t *testing.T) {
	tmpl, err := template.New("").Funcs(web.TemplateFuncMap()).ParseFS(os.DirFS("templates"), "detail.html")
	if err != nil {
		t.Fatalf("parse templates/detail.html: %v", err)
	}

	render := func(t *testing.T, detail *protocol.BeadDetail) string {
		t.Helper()
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "detail.html", detail); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		return buf.String()
	}

	t.Run("full bead with all fields", func(t *testing.T) {
		detail := &protocol.BeadDetail{
			ID:                 "oro-x1",
			Title:              "Test bead",
			Status:             "in_progress",
			Type:               "task",
			Epic:               "oro-epic1",
			Model:              "sonnet",
			Description:        "desc",
			AcceptanceCriteria: "Test: foo",
			Dependencies: []protocol.Dependency{
				{IssueID: "oro-x1", DependsOnID: "oro-dep1", Type: "blocks"},
			},
			WorkerID:       "w1",
			ContextPercent: 42,
		}
		body := render(t, detail)

		// .ID in outer div
		assertContains(t, body, `id="oro-x1"`)
		// .Title in <h2>
		assertContains(t, body, "Test bead</h2>")
		// .Status as a human-readable label
		assertContains(t, body, "In progress")
		assertContains(t, body, "detail-meta")
		// .Description in prose block
		assertContains(t, body, `detail-description`)
		assertContains(t, body, "desc")
		// .AcceptanceCriteria in <pre> block
		assertContains(t, body, `detail-ac`)
		assertContains(t, body, "Test: foo")
		// .Dependencies listed (non-empty)
		assertContains(t, body, `detail-deps`)
		assertContains(t, body, "oro-dep1")
		// .WorkerID shown (non-empty)
		assertContains(t, body, `detail-worker`)
		assertContains(t, body, "w1")
		// .ContextPercent shown (>0)
		assertContains(t, body, "42")
	})

	t.Run("empty optional fields not rendered", func(t *testing.T) {
		detail := &protocol.BeadDetail{
			ID:                 "oro-x2",
			Title:              "Minimal bead",
			Status:             "open",
			Description:        "",
			AcceptanceCriteria: "",
			Dependencies:       nil,
			WorkerID:           "",
			ContextPercent:     0,
		}
		body := render(t, detail)

		// Required fields still present
		assertContains(t, body, `id="oro-x2"`)
		assertContains(t, body, "Minimal bead</h2>")
		assertContains(t, body, "Open")

		// Description section hidden when empty
		assertNotContains(t, body, "detail-description")
		// Dependencies section hidden when nil
		assertNotContains(t, body, "detail-deps")
		// Worker section hidden when WorkerID empty
		assertNotContains(t, body, "detail-worker")
		// AcceptanceCriteria section hidden when empty
		assertNotContains(t, body, "detail-ac")
	})
}

func TestDetailWrapsLongAC(t *testing.T) {
	tmpl, err := template.New("").Funcs(web.TemplateFuncMap()).ParseFS(os.DirFS("templates"), "detail.html")
	if err != nil {
		t.Fatalf("parse templates/detail.html: %v", err)
	}

	render := func(t *testing.T, detail detailTemplateData) string {
		t.Helper()
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "detail.html", detail); err != nil {
			t.Fatalf("execute template: %v", err)
		}
		return buf.String()
	}

	longAC := strings.Repeat("x", 400)
	body := render(t, detailTemplateData{
		BeadDetail: &protocol.BeadDetail{
			ID:                 "oro-long-ac",
			Title:              "Long acceptance criteria",
			Status:             "in_progress",
			Type:               "task",
			Description:        "single token AC should wrap",
			AcceptanceCriteria: longAC,
		},
	})

	assertContains(t, body, `class="bead-detail__title"`)
	assertContains(t, body, "Long acceptance criteria</h2>")
	assertContains(t, body, "In progress")
	assertContains(t, body, `detail-ac`)
	assertContains(t, body, "pre-wrap")
	assertContains(t, body, "overflow-wrap")
	assertContains(t, body, longAC)
	assertNotContains(t, body, "<pre")

	epicBody := render(t, detailTemplateData{
		BeadDetail: &protocol.BeadDetail{
			ID:                 "oro-epic",
			Title:              "Epic bead",
			Status:             "open",
			Type:               "epic",
			AcceptanceCriteria: "Do not render this for epics with children",
		},
		Children: []protocol.Bead{
			{ID: "oro-child-1", Title: "First child", Status: "closed"},
			{ID: "oro-child-2", Title: "Second child", Status: "in_progress"},
		},
	})

	assertContains(t, epicBody, `id="children"`)
	assertContains(t, epicBody, "First child")
	assertContains(t, epicBody, "Closed")
	assertContains(t, epicBody, "Second child")
	assertContains(t, epicBody, "In progress")
	assertNotContains(t, epicBody, "Do not render this")
}

func assertContains(t *testing.T, body, substr string) {
	t.Helper()
	if !strings.Contains(body, substr) {
		t.Errorf("body missing %q;\ngot:\n%s", substr, body)
	}
}

func assertNotContains(t *testing.T, body, substr string) {
	t.Helper()
	if strings.Contains(body, substr) {
		t.Errorf("body should not contain %q;\ngot:\n%s", substr, body)
	}
}
