package views

import (
	"strings"
	"testing"
	"time"

	"oro/pkg/mg/data"
)

func TestParadeLabel(t *testing.T) {
	issues := []data.Issue{
		{ID: "mg-001", Title: "Blocker", Status: data.StatusOpen, Priority: data.PriorityHigh, IssueType: data.TypeTask},
		{
			ID: "mg-002", Title: "Blocked", Status: data.StatusOpen, Priority: data.PriorityMedium, IssueType: data.TypeTask,
			Dependencies: []data.Dependency{{IssueID: "mg-002", DependsOnID: "mg-001", Type: "blocks"}},
		},
		{ID: "mg-003", Title: "Rolling", Status: data.StatusInProgress, Priority: data.PriorityHigh, IssueType: data.TypeTask},
		{ID: "mg-004", Title: "Closed", Status: data.StatusClosed, Priority: data.PriorityMedium, IssueType: data.TypeTask},
	}
	issueMap := data.BuildIssueMap(issues)
	bt := data.DefaultBlockingTypes

	tests := []struct {
		name   string
		issue  *data.Issue
		expect string
	}{
		{name: "open unblocked", issue: issueMap["mg-001"], expect: "Lined Up"},
		{name: "open blocked", issue: issueMap["mg-002"], expect: "Stalled"},
		{name: "in_progress", issue: issueMap["mg-003"], expect: "Rolling"},
		{name: "closed", issue: issueMap["mg-004"], expect: "Past the Stand"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := paradeLabel(tc.issue, issueMap, bt)
			if got != tc.expect {
				t.Fatalf("paradeLabel(%s) = %q, want %q", tc.issue.ID, got, tc.expect)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		expect string
	}{
		{name: "short string", input: "hello", maxLen: 10, expect: "hello"},
		{name: "exact fit", input: "hello", maxLen: 5, expect: "hello"},
		{name: "needs truncation", input: "hello world", maxLen: 8, expect: "hello..."},
		{name: "very short max", input: "hello", maxLen: 2, expect: "he"},
		{name: "max 3", input: "hello", maxLen: 3, expect: "hel"},
		{name: "empty string", input: "", maxLen: 5, expect: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.input, tc.maxLen)
			if got != tc.expect {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.expect)
			}
		})
	}
}

func TestWordWrap(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		width  int
		expect string
	}{
		{name: "no wrap needed", input: "short text", width: 20, expect: "short text"},
		{name: "wraps at word boundary", input: "hello world foo bar", width: 11, expect: "hello world\nfoo bar"},
		{name: "single long word", input: "superlongword", width: 5, expect: "superlongword"},
		{name: "empty string", input: "", width: 10, expect: ""},
		{name: "zero width", input: "hello", width: 0, expect: "hello"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := wordWrap(tc.input, tc.width)
			if got != tc.expect {
				t.Fatalf("wordWrap(%q, %d) = %q, want %q", tc.input, tc.width, got, tc.expect)
			}
		})
	}
}

func TestSetIssueUpdatesContent(t *testing.T) {
	issues := []data.Issue{
		{ID: "mg-001", Title: "Test Issue Title", Status: data.StatusOpen, Priority: data.PriorityMedium, IssueType: data.TypeTask},
	}
	d := NewDetail(60, 20, issues)
	d.SetIssue(&issues[0])

	content := d.Viewport.View()
	if !strings.Contains(content, "Test Issue Title") {
		t.Fatalf("viewport content should contain issue title, got: %s", content)
	}
}

func TestSetSizeUpdatesDimensions(t *testing.T) {
	issues := []data.Issue{
		{ID: "mg-001", Title: "Test", Status: data.StatusOpen, Priority: data.PriorityMedium, IssueType: data.TypeTask},
	}
	d := NewDetail(60, 20, issues)

	d.SetSize(100, 30)
	if d.Width != 100 {
		t.Fatalf("Width = %d, want 100", d.Width)
	}
	if d.Height != 30 {
		t.Fatalf("Height = %d, want 30", d.Height)
	}
	if d.Viewport.Width() != 98 {
		t.Fatalf("Viewport.Width = %d, want 98 (width-2)", d.Viewport.Width())
	}
	if d.Viewport.Height() != 30 {
		t.Fatalf("Viewport.Height = %d, want 30", d.Viewport.Height())
	}
}

func TestEpicProgressUsesDirectChildren(t *testing.T) {
	issues := []data.Issue{
		{ID: "mg-100", Title: "Platform migration", Status: data.StatusOpen, Priority: data.PriorityHigh, IssueType: data.TypeEpic, CreatedAt: time.Now()},
		{ID: "mg-100.1", Title: "Auth", Status: data.StatusClosed, Priority: data.PriorityMedium, IssueType: data.TypeTask, CreatedAt: time.Now()},
		{ID: "mg-100.2", Title: "Billing", Status: data.StatusOpen, Priority: data.PriorityMedium, IssueType: data.TypeTask, CreatedAt: time.Now()},
		{ID: "mg-100.2.1", Title: "Billing schema", Status: data.StatusClosed, Priority: data.PriorityLow, IssueType: data.TypeTask, CreatedAt: time.Now()},
	}

	d := NewDetail(80, 30, issues)
	progress, ok := d.epicProgress(&issues[0])
	if !ok {
		t.Fatal("expected epic progress to be available")
	}
	if progress.Done != 1 || progress.Total != 2 {
		t.Fatalf("epicProgress() = %+v, want done=1 total=2", progress)
	}
	if progress.Label() != "1/2 (50%)" {
		t.Fatalf("progress.Label() = %q, want %q", progress.Label(), "1/2 (50%)")
	}
}

func TestEpicProgressRenderingInContent(t *testing.T) {
	issues := []data.Issue{
		{ID: "mg-100", Title: "Platform migration", Status: data.StatusOpen, Priority: data.PriorityHigh, IssueType: data.TypeEpic, CreatedAt: time.Now()},
		{ID: "mg-100.1", Title: "Auth", Status: data.StatusClosed, Priority: data.PriorityMedium, IssueType: data.TypeTask, CreatedAt: time.Now()},
		{ID: "mg-100.2", Title: "Billing", Status: data.StatusOpen, Priority: data.PriorityMedium, IssueType: data.TypeTask, CreatedAt: time.Now()},
	}

	d := NewDetail(80, 30, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()
	if !strings.Contains(content, "Progress:") {
		t.Fatalf("content should contain Progress row, got: %s", content)
	}
	if !strings.Contains(content, "1/2 (50%)") {
		t.Fatalf("content should contain 1/2 progress label, got: %s", content)
	}
}

func TestActivityRenderingInContent(t *testing.T) {
	created := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2025, 1, 16, 14, 30, 0, 0, time.UTC)
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "Test Issue", Status: data.StatusInProgress,
			Priority: data.PriorityMedium, IssueType: data.TypeTask,
			CreatedAt: created, UpdatedAt: updated,
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "ACTIVITY") {
		t.Error("content should contain ACTIVITY section")
	}
	if !strings.Contains(content, "Created") {
		t.Error("content should contain 'Created' event")
	}
	if !strings.Contains(content, "Updated") {
		t.Error("content should contain 'Updated' event")
	}
}

func TestActivityWithWorker(t *testing.T) {
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "Test Issue", Status: data.StatusInProgress,
			Priority: data.PriorityMedium, IssueType: data.TypeTask,
			CreatedAt: time.Now(),
		},
	}
	d := NewDetail(80, 40, issues)
	d.ActiveWorkers = map[string]string{"mg-001": "pane-1"}
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "worker active") {
		t.Error("content should show worker active in activity section")
	}
}

func TestActivityWithClosedIssue(t *testing.T) {
	created := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	closed := time.Date(2025, 1, 17, 9, 0, 0, 0, time.UTC)
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "Test Issue", Status: data.StatusClosed,
			Priority: data.PriorityMedium, IssueType: data.TypeTask,
			CreatedAt: created, ClosedAt: &closed,
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "Closed") {
		t.Error("content should contain 'Closed' event")
	}
}

func TestMoleculeProgressBar(t *testing.T) {
	bar := moleculeProgressBar(3, 10, 20)
	if bar == "" {
		t.Fatal("progress bar should not be empty")
	}
	if len([]rune(bar)) == 0 {
		t.Fatal("progress bar should have characters")
	}

	// Edge cases
	emptyBar := moleculeProgressBar(0, 0, 10)
	if emptyBar == "" {
		t.Fatal("zero-total bar should not be empty")
	}
}

func TestFormatTime(t *testing.T) {
	ts := time.Date(2025, 2, 15, 14, 30, 0, 0, time.UTC)
	got := formatTime(ts)
	if !strings.Contains(got, "Feb 15") {
		t.Errorf("formatTime should contain date, got %q", got)
	}

	// Zero time
	zero := formatTime(time.Time{})
	if strings.TrimSpace(zero) != "" {
		t.Errorf("zero time should be blank, got %q", zero)
	}
}

func TestQualitySectionRendered(t *testing.T) {
	score := float32(0.85)
	cryst := true
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "HOP Issue", Status: data.StatusClosed,
			Priority:     data.PriorityMedium,
			IssueType:    data.TypeTask,
			CreatedAt:    time.Now(),
			QualityScore: &score,
			Crystallizes: &cryst,
			Creator: &data.EntityRef{
				Name:     "polecat-alpha",
				Platform: "gastown",
				URI:      "hop://gastown/mardi_gras/polecat-alpha",
			},
			Validations: []data.Validation{
				{
					Validator:    data.EntityRef{Name: "witness", Platform: "gastown"},
					Outcome:      data.OutcomeAccepted,
					QualityScore: 0.9,
				},
				{
					Validator:    data.EntityRef{Name: "refinery", Platform: "gastown"},
					Outcome:      data.OutcomeAccepted,
					QualityScore: 0.8,
					Comment:      "Clean implementation",
				},
			},
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "QUALITY") {
		t.Error("content should contain QUALITY section")
	}
	if !strings.Contains(content, "0.85") {
		t.Error("content should contain quality score")
	}
	if !strings.Contains(content, "good") {
		t.Error("content should contain quality label 'good'")
	}
	if !strings.Contains(content, "polecat-alpha") {
		t.Error("content should contain creator name")
	}
	if !strings.Contains(content, "witness") {
		t.Error("content should contain validator name")
	}
	if !strings.Contains(content, "refinery") {
		t.Error("content should contain second validator name")
	}
	if !strings.Contains(content, "crystallizes") {
		t.Error("content should contain crystallization indicator")
	}
}

func TestQualitySectionNotRenderedWithoutScore(t *testing.T) {
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "No HOP", Status: data.StatusOpen,
			Priority:  data.PriorityMedium,
			IssueType: data.TypeTask,
			CreatedAt: time.Now(),
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if strings.Contains(content, "QUALITY") {
		t.Error("content should not contain QUALITY section when no quality score")
	}
}

func TestCrossRigDepsRendered(t *testing.T) {
	issues := []data.Issue{
		{
			ID: "bd-001", Title: "Fix token validation", Status: data.StatusOpen,
			Priority: data.PriorityMedium, IssueType: data.TypeBug, CreatedAt: time.Now(),
			Dependencies: []data.Dependency{
				{IssueID: "bd-001", DependsOnID: "external:gastown:gt-c3f2", Type: "blocks"},
				{IssueID: "bd-001", DependsOnID: "external:wyvern:wy-e5f6", Type: "related"},
			},
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "CROSS-RIG") {
		t.Error("content should contain CROSS-RIG section")
	}
	if !strings.Contains(content, "gastown") {
		t.Error("content should contain rig name 'gastown'")
	}
	if !strings.Contains(content, "wyvern") {
		t.Error("content should contain rig name 'wyvern'")
	}
	if !strings.Contains(content, "gt-c3f2") {
		t.Error("content should contain external issue ID")
	}
}

func TestCrossRigDepsNotRenderedForLocalDeps(t *testing.T) {
	issues := []data.Issue{
		{
			ID: "bd-001", Title: "Local issue", Status: data.StatusOpen,
			Priority: data.PriorityMedium, IssueType: data.TypeTask, CreatedAt: time.Now(),
			Dependencies: []data.Dependency{
				{IssueID: "bd-001", DependsOnID: "bd-002", Type: "blocks"},
			},
		},
		{
			ID: "bd-002", Title: "Another local", Status: data.StatusOpen,
			Priority: data.PriorityMedium, IssueType: data.TypeTask, CreatedAt: time.Now(),
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if strings.Contains(content, "CROSS-RIG") {
		t.Error("content should not contain CROSS-RIG section for local-only deps")
	}
}

func TestQualityRejectedValidation(t *testing.T) {
	score := float32(0.3)
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "Rejected Issue", Status: data.StatusInProgress,
			Priority:     data.PriorityMedium,
			IssueType:    data.TypeTask,
			CreatedAt:    time.Now(),
			QualityScore: &score,
			Validations: []data.Validation{
				{
					Validator:    data.EntityRef{Name: "witness"},
					Outcome:      data.OutcomeRejected,
					QualityScore: 0.2,
				},
			},
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "QUALITY") {
		t.Error("content should contain QUALITY section")
	}
	if !strings.Contains(content, "poor") {
		t.Error("content should contain quality label 'poor'")
	}
	if !strings.Contains(content, "rejected") {
		t.Error("content should contain 'rejected' outcome")
	}
}

func TestMetadataSchemaRendered(t *testing.T) {
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "Test Issue", Status: data.StatusOpen,
			Priority: data.PriorityMedium, IssueType: data.TypeTask,
			CreatedAt: time.Now(),
		},
	}
	d := NewDetail(80, 40, issues)
	min0 := 0.0
	max100 := 100.0
	d.MetadataSchema = &data.MetadataSchema{
		Mode: "warn",
		Fields: map[string]data.MetadataFieldSchema{
			"team": {
				Type:     data.MetaEnum,
				Required: true,
				Values:   []string{"platform", "frontend", "backend"},
			},
			"priority_score": {
				Type: data.MetaInt,
				Min:  &min0,
				Max:  &max100,
			},
		},
	}
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "METADATA") {
		t.Error("content should contain METADATA section")
	}
	if !strings.Contains(content, "warn") {
		t.Error("content should contain mode 'warn'")
	}
	if !strings.Contains(content, "team") {
		t.Error("content should contain field name 'team'")
	}
	if !strings.Contains(content, "enum") {
		t.Error("content should contain field type 'enum'")
	}
	if !strings.Contains(content, "priority_score") {
		t.Error("content should contain field name 'priority_score'")
	}
}

func TestMetadataNotRenderedWithoutSchema(t *testing.T) {
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "No Metadata", Status: data.StatusOpen,
			Priority: data.PriorityMedium, IssueType: data.TypeTask,
			CreatedAt: time.Now(),
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if strings.Contains(content, "METADATA") {
		t.Error("content should not contain METADATA section without schema")
	}
}

func TestMetadataWithIssueValues(t *testing.T) {
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "With Metadata", Status: data.StatusOpen,
			Priority: data.PriorityMedium, IssueType: data.TypeTask,
			CreatedAt: time.Now(),
			Metadata: map[string]any{
				"team":   "frontend",
				"urgent": true,
			},
		},
	}
	d := NewDetail(80, 40, issues)
	d.MetadataSchema = &data.MetadataSchema{
		Mode: "warn",
		Fields: map[string]data.MetadataFieldSchema{
			"team": {
				Type:     data.MetaEnum,
				Required: true,
				Values:   []string{"platform", "frontend", "backend"},
			},
			"urgent": {
				Type: data.MetaBool,
			},
		},
	}
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "METADATA") {
		t.Error("content should contain METADATA section")
	}
	if !strings.Contains(content, "frontend") {
		t.Error("content should contain metadata value 'frontend'")
	}
	if !strings.Contains(content, "true") {
		t.Error("content should contain metadata value 'true'")
	}
}

func TestMetadataRawValuesWithoutSchema(t *testing.T) {
	issues := []data.Issue{
		{
			ID: "mg-001", Title: "Raw Metadata", Status: data.StatusOpen,
			Priority: data.PriorityMedium, IssueType: data.TypeTask,
			CreatedAt: time.Now(),
			Metadata: map[string]any{
				"custom_field": "value123",
			},
		},
	}
	d := NewDetail(80, 40, issues)
	d.SetIssue(&issues[0])

	content := d.renderContent()

	if !strings.Contains(content, "METADATA") {
		t.Error("content should contain METADATA section for raw metadata")
	}
	if !strings.Contains(content, "custom_field") {
		t.Error("content should contain raw metadata key")
	}
	if !strings.Contains(content, "value123") {
		t.Error("content should contain raw metadata value")
	}
}
