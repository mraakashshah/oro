package data_test

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io/fs"
	"strings"
	"testing"

	"oro/pkg/mg"
	"oro/pkg/mg/data"
	"oro/pkg/mg/views"
	"oro/pkg/protocol"
	"oro/pkg/web"

	"github.com/charmbracelet/x/ansi"
	"github.com/google/go-cmp/cmp"
)

func TestDashboardSnapshot(t *testing.T) {
	const width, height = 100, 24

	beforeIssues := parseDashboardSnapshotIssues(t, legacyDashboardSnapshotJSON)
	afterIssues := parseDashboardSnapshotIssues(t, nativeDashboardSnapshotJSON)

	beforeParade := views.NewParade(beforeIssues, width, height, data.DefaultBlockingTypes)
	before := beforeParade.View()

	afterParade := views.NewParade(afterIssues, width, height, data.DefaultBlockingTypes)
	after := afterParade.View()

	if diff := cmp.Diff(before, after); diff != "" {
		t.Fatalf("dashboard snapshot changed between legacy and native issue JSON (-legacy +native):\n%s", diff)
	}

	stripped := ansi.Strip(after)
	for _, want := range []string{"Rolling", "Lined Up", "Stalled", "abc-1", "abc-1.1"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("dashboard snapshot missing %q:\n%s", want, stripped)
		}
	}
	if !strings.Contains(stripped, "    "+mg.SymLinedUp+" abc-1.1") {
		t.Fatalf("explicit parent child should render indented in parade snapshot:\n%s", stripped)
	}

	beforeHTML := renderDashboardSnapshotHTML(t, beforeIssues)
	afterHTML := renderDashboardSnapshotHTML(t, afterIssues)
	if diff := cmp.Diff(beforeHTML, afterHTML); diff != "" {
		t.Fatalf("dashboard HTML snapshot changed between legacy and native issue JSON (-legacy +native):\n%s", diff)
	}

	flatNativeIssues := parseDashboardSnapshotIssues(t, flatNativeDashboardSnapshotJSON)
	flatNativeParade := views.NewParade(flatNativeIssues, width, height, data.DefaultBlockingTypes)
	flatNative := ansi.Strip(flatNativeParade.View())
	if !strings.Contains(flatNative, "    "+mg.SymLinedUp+" xyz-3") {
		t.Fatalf("flat native child with explicit parent should render indented in parade snapshot:\n%s", flatNative)
	}
}

func parseDashboardSnapshotIssues(t *testing.T, raw string) []data.Issue {
	t.Helper()

	var issues []data.Issue
	if err := json.Unmarshal([]byte(raw), &issues); err != nil {
		t.Fatalf("parse dashboard snapshot issues: %v", err)
	}
	return issues
}

func renderDashboardSnapshotHTML(t *testing.T, issues []data.Issue) string {
	t.Helper()

	templates, err := fs.Sub(web.Content, "templates")
	if err != nil {
		t.Fatalf("load web templates: %v", err)
	}
	tmpl, err := template.New("").Funcs(web.TemplateFuncMap()).ParseFS(templates, "parade.html")
	if err != nil {
		t.Fatalf("parse parade template: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "parade-content", dashboardSnapshotParadeData(issues)); err != nil {
		t.Fatalf("render dashboard HTML: %v", err)
	}
	return buf.String()
}

func dashboardSnapshotParadeData(issues []data.Issue) web.ParadeData {
	groups := data.GroupByParade(issues, data.DefaultBlockingTypes)
	return web.ParadeData{
		Ready:      dashboardSnapshotBeads(groups[data.ParadeLinedUp]),
		InProgress: dashboardSnapshotBeads(groups[data.ParadeRolling]),
		Blocked:    dashboardSnapshotBeads(groups[data.ParadeStalled]),
		Closed:     dashboardSnapshotBeads(groups[data.ParadePastTheStand]),
	}
}

func dashboardSnapshotBeads(issues []data.Issue) []protocol.Bead {
	beads := make([]protocol.Bead, 0, len(issues))
	for _, issue := range issues {
		bead := protocol.Bead{
			ID:        issue.ID,
			Title:     issue.Title,
			Status:    string(issue.Status),
			Priority:  int(issue.Priority),
			Type:      string(issue.IssueType),
			Owner:     issue.Owner,
			CreatedAt: issue.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt: issue.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
		if issue.ParentID() != "" {
			bead.Epic = issue.ParentID()
		}
		for _, dep := range issue.Dependencies {
			bead.Dependencies = append(bead.Dependencies, protocol.Dependency{
				IssueID:     dep.IssueID,
				DependsOnID: dep.DependsOnID,
				Type:        dep.Type,
			})
		}
		beads = append(beads, bead)
	}
	return beads
}

const legacyDashboardSnapshotJSON = `[
  {
    "id": "roll-1",
    "title": "Worker is moving",
    "status": "in_progress",
    "priority": 1,
    "issue_type": "task",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "abc-1",
    "title": "Epic parent",
    "status": "open",
    "priority": 2,
    "issue_type": "epic",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "abc-1.1",
    "title": "Child with preserved parent",
    "status": "open",
    "priority": 2,
    "issue_type": "task",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "stall-1",
    "title": "Blocked on missing input",
    "status": "open",
    "priority": 0,
    "issue_type": "bug",
    "dependencies": [{"issue_id":"stall-1","depends_on_id":"missing-1","type":"blocks"}],
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "done-1",
    "title": "Already done",
    "status": "closed",
    "priority": 3,
    "issue_type": "chore",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  }
]`

const nativeDashboardSnapshotJSON = `[
  {
    "id": "roll-1",
    "title": "Worker is moving",
    "status": "in_progress",
    "priority": 1,
    "type": "task",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "abc-1",
    "title": "Epic parent",
    "status": "open",
    "priority": 2,
    "type": "epic",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "abc-1.1",
    "title": "Child with preserved parent",
    "status": "open",
    "priority": 2,
    "type": "task",
    "parent_id": "abc-1",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "stall-1",
    "title": "Blocked on missing input",
    "status": "open",
    "priority": 0,
    "type": "bug",
    "dependencies": [{"issue_id":"stall-1","depends_on_id":"missing-1","type":"blocks"}],
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "done-1",
    "title": "Already done",
    "status": "closed",
    "priority": 3,
    "type": "chore",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  }
]`

const flatNativeDashboardSnapshotJSON = `[
  {
    "id": "abc-1",
    "title": "Epic parent",
    "status": "open",
    "priority": 2,
    "type": "epic",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  },
  {
    "id": "xyz-3",
    "title": "Flat child with explicit parent",
    "status": "open",
    "priority": 2,
    "type": "task",
    "parent_id": "abc-1",
    "created_at": "2026-04-27T12:00:00Z",
    "updated_at": "2026-04-27T12:00:00Z",
    "labels": [],
    "metadata": {},
    "tags": []
  }
]`
