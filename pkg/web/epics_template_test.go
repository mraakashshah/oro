package web_test

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"

	"oro/pkg/web"
)

func TestEpicsRender(t *testing.T) {
	fsys := os.DirFS("templates")
	tmpl, err := template.New("").Funcs(web.TemplateFuncMap()).ParseFS(fsys, "epics.html")
	if err != nil {
		t.Fatalf("parse epics.html: %v", err)
	}

	data := web.EpicsData{
		InProgress: []web.EpicSummary{
			{
				ID:               "oro-epic-active",
				Title:            "Ship dashboard epics",
				ClosedChildren:   7,
				TotalChildren:    9,
				ActiveChildTitle: "Render the active child",
			},
		},
		Next: []web.EpicSummary{
			{
				ID:     "oro-epic-ready",
				Title:  "Ready follow-up epic",
				Status: "open",
			},
			{
				ID:                "oro-epic-blocked",
				Title:             "Blocked follow-up epic",
				Status:            "blocked",
				FirstBlockerTitle: "Finish dependency setup",
			},
		},
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "epics.html", data); err != nil {
		t.Fatalf("execute epics.html: %v", err)
	}
	html := buf.String()

	for _, want := range []string{
		"Ship dashboard epics",
		"7 / 9",
		"Render the active child",
		"next -&gt;",
		"NEXT EPICS",
		"Ready follow-up epic",
		"Blocked follow-up epic",
		"ready",
		"blocked",
		"first needs Finish dependency setup",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered epics missing %q; html:\n%s", want, html)
		}
	}
	if !strings.Contains(html, "epic-progress__bar") {
		t.Errorf("rendered epics missing progress bar; html:\n%s", html)
	}
	if strings.Index(html, "Ship dashboard epics") > strings.Index(html, `class="epic-card__id">oro-epic-active`) {
		t.Errorf("epic title should lead before demoted id; html:\n%s", html)
	}
	if strings.Index(html, "Ready follow-up epic") > strings.Index(html, `class="epic-card__id">oro-epic-ready`) {
		t.Errorf("next epic title should lead before demoted id; html:\n%s", html)
	}

	t.Run("empty in progress still renders next epics", func(t *testing.T) {
		buf.Reset()
		emptyActive := web.EpicsData{
			Next: []web.EpicSummary{{ID: "oro-epic-next", Title: "Only next epic", Status: "open"}},
		}
		if err := tmpl.ExecuteTemplate(&buf, "epics.html", emptyActive); err != nil {
			t.Fatalf("execute epics.html: %v", err)
		}
		html := buf.String()
		if !strings.Contains(html, "NEXT EPICS") || !strings.Contains(html, "Only next epic") {
			t.Errorf("empty in-progress epics should still render next lane; html:\n%s", html)
		}
	})
}
