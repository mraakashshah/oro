package data

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestParentIDAccessor(t *testing.T) {
	t.Run("explicit parent field wins", func(t *testing.T) {
		iss := Issue{ID: "mg-007.2.1", ParentIDValue: "oro-parent"}

		if got := iss.ParentID(); got != "oro-parent" {
			t.Fatalf("ParentID() = %q, want explicit parent field", got)
		}
	})

	t.Run("dotted id fallback", func(t *testing.T) {
		iss := Issue{ID: "mg-007.2.1"}

		if got := iss.ParentID(); got != "mg-007.2" {
			t.Fatalf("ParentID() = %q, want dotted ID parent", got)
		}
	})

	t.Run("field and method do not collide", func(t *testing.T) {
		iss := Issue{ID: "mg-007.2", ParentIDValue: "mg-root"}

		if iss.ParentIDValue != "mg-root" {
			t.Fatalf("ParentIDValue = %q, want stored field", iss.ParentIDValue)
		}
		if got := iss.ParentID(); got != "mg-root" {
			t.Fatalf("ParentID() = %q, want accessor to read stored field", got)
		}
	})

	t.Run("oro native json type and parent id", func(t *testing.T) {
		var iss Issue
		if err := json.Unmarshal([]byte(`{"id":"oro-child","title":"Child","status":"open","priority":2,"type":"task","parent_id":"oro-parent"}`), &iss); err != nil {
			t.Fatalf("UnmarshalJSON() error = %v", err)
		}
		if iss.IssueType != TypeTask {
			t.Fatalf("IssueType = %q, want %q", iss.IssueType, TypeTask)
		}
		if got := iss.ParentID(); got != "oro-parent" {
			t.Fatalf("ParentID() = %q, want explicit JSON parent", got)
		}

		got, err := json.Marshal(iss)
		if err != nil {
			t.Fatalf("MarshalJSON() error = %v", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(got, &fields); err != nil {
			t.Fatalf("marshaled JSON did not decode: %v", err)
		}
		if _, ok := fields["type"]; !ok {
			t.Fatalf("marshaled JSON missing type field: %s", got)
		}
		if _, ok := fields["issue_type"]; ok {
			t.Fatalf("marshaled JSON included legacy issue_type field: %s", got)
		}
		if _, ok := fields["parent_id"]; !ok {
			t.Fatalf("marshaled JSON missing parent_id field: %s", got)
		}
	})

	t.Run("legacy issue type input", func(t *testing.T) {
		var iss Issue
		if err := json.Unmarshal([]byte(`{"id":"legacy-child","title":"Child","status":"open","priority":2,"issue_type":"bug"}`), &iss); err != nil {
			t.Fatalf("UnmarshalJSON() error = %v", err)
		}
		if iss.IssueType != TypeBug {
			t.Fatalf("IssueType = %q, want legacy issue_type value %q", iss.IssueType, TypeBug)
		}
	})
}

func TestEmptyVsNilLabelsMetadataTagsOroNativeJSON(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		wantNil      bool
		wantLabels   []string
		wantMetadata map[string]any
		wantTags     []string
	}{
		{
			name: "missing fields parse as nil and render null",
			json: `{"id":"oro-missing","title":"Missing","status":"open","priority":2,"type":"task",
				"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`,
			wantNil: true,
		},
		{
			name: "null fields parse as nil and render null",
			json: `{"id":"oro-null","title":"Null","status":"open","priority":2,"type":"task",
				"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z",
				"labels":null,"metadata":null,"tags":null}`,
			wantNil: true,
		},
		{
			name: "empty fields remain non nil and render empty",
			json: `{"id":"oro-empty","title":"Empty","status":"open","priority":2,"type":"task",
				"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z",
				"labels":[],"metadata":{},"tags":[]}`,
			wantLabels:   []string{},
			wantMetadata: map[string]any{},
			wantTags:     []string{},
		},
		{
			name: "populated fields round trip",
			json: `{"id":"oro-populated","title":"Populated","status":"open","priority":2,"type":"task",
				"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z",
				"labels":["backend","security"],"metadata":{"component":"api","effort":5,"reviewed":true},"tags":["phase-5","mg"]}`,
			wantLabels: []string{"backend", "security"},
			wantMetadata: map[string]any{
				"component": "api",
				"effort":    float64(5),
				"reviewed":  true,
			},
			wantTags: []string{"phase-5", "mg"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var iss Issue
			if err := json.Unmarshal([]byte(tc.json), &iss); err != nil {
				t.Fatalf("UnmarshalJSON() error = %v", err)
			}

			if tc.wantNil {
				if iss.Labels != nil {
					t.Fatalf("Labels = %#v, want nil", iss.Labels)
				}
				if iss.Metadata != nil {
					t.Fatalf("Metadata = %#v, want nil", iss.Metadata)
				}
				if iss.Tags != nil {
					t.Fatalf("Tags = %#v, want nil", iss.Tags)
				}
			} else {
				assertStringSliceEqual(t, "Labels", iss.Labels, tc.wantLabels)
				assertMetadataEqual(t, iss.Metadata, tc.wantMetadata)
				assertStringSliceEqual(t, "Tags", iss.Tags, tc.wantTags)
			}

			rendered, err := json.Marshal(iss)
			if err != nil {
				t.Fatalf("MarshalJSON() error = %v", err)
			}

			var fields map[string]json.RawMessage
			if err := json.Unmarshal(rendered, &fields); err != nil {
				t.Fatalf("rendered JSON did not decode: %v", err)
			}
			assertRenderedJSONField(t, fields, "labels", tc.wantNil, tc.wantLabels)
			assertRenderedJSONField(t, fields, "metadata", tc.wantNil, tc.wantMetadata)
			assertRenderedJSONField(t, fields, "tags", tc.wantNil, tc.wantTags)
		})
	}
}

func TestParentID(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"mg-007", ""},
		{"mg-007.1", "mg-007"},
		{"mg-007.2.1", "mg-007.2"},
		{"bd-a3f8.1.1", "bd-a3f8.1"},
		{"simple", ""},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			iss := Issue{ID: tc.id}
			if got := iss.ParentID(); got != tc.want {
				t.Errorf("ParentID(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

func TestNestingDepth(t *testing.T) {
	tests := []struct {
		id   string
		want int
	}{
		{"mg-007", 0},
		{"mg-007.1", 1},
		{"mg-007.2.1", 2},
		{"a.b.c.d", 3},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			iss := Issue{ID: tc.id}
			if got := iss.NestingDepth(); got != tc.want {
				t.Errorf("NestingDepth(%q) = %d, want %d", tc.id, got, tc.want)
			}
		})
	}
}

func TestHierarchyParentIDValue(t *testing.T) {
	parent := Issue{ID: "abc-1", IssueType: TypeEpic, Status: StatusOpen}
	child := Issue{ID: "xyz-3", ParentIDValue: "abc-1", IssueType: TypeTask, Status: StatusOpen}
	issues := []Issue{parent, child}

	issueMap := BuildIssueMap(issues)
	gotChild := issueMap["xyz-3"]
	if gotChild == nil {
		t.Fatal("child missing from issue map")
	}

	if got := gotChild.ParentID(); got != "abc-1" {
		t.Fatalf("ParentID() = %q, want explicit parent abc-1", got)
	}
	if _, ok := issueMap[gotChild.ParentID()]; !ok {
		t.Fatalf("child parent %q missing from issue map", gotChild.ParentID())
	}
	if got := gotChild.NestingDepth(); got != 1 {
		t.Fatalf("NestingDepth() = %d, want 1 so flat explicit-parent child is not rendered as an orphan", got)
	}
}

func assertStringSliceEqual(t *testing.T, field string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", field, got, want)
	}
}

func assertMetadataEqual(t *testing.T, got, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Metadata = %#v, want %#v", got, want)
	}
}

func assertRenderedJSONField(t *testing.T, fields map[string]json.RawMessage, name string, wantNull bool, want any) {
	t.Helper()

	got, ok := fields[name]
	if !ok {
		t.Fatalf("rendered JSON missing %q field", name)
	}
	if wantNull {
		if string(got) != "null" {
			t.Fatalf("rendered %s = %s, want null", name, got)
		}
		return
	}

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal expected %s field: %v", name, err)
	}
	if !reflect.DeepEqual(json.RawMessage(got), json.RawMessage(wantJSON)) {
		t.Fatalf("rendered %s = %s, want %s", name, got, wantJSON)
	}
}

func TestIsOverdue(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour)
	future := time.Now().Add(48 * time.Hour)

	tests := []struct {
		name   string
		dueAt  *time.Time
		status Status
		want   bool
	}{
		{"nil due", nil, StatusOpen, false},
		{"past due, open", &past, StatusOpen, true},
		{"past due, closed", &past, StatusClosed, false},
		{"future due, open", &future, StatusOpen, false},
		{"past due, in_progress", &past, StatusInProgress, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			iss := Issue{DueAt: tc.dueAt, Status: tc.status}
			if got := iss.IsOverdue(); got != tc.want {
				t.Errorf("IsOverdue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsDeferred(t *testing.T) {
	past := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(5 * 24 * time.Hour)

	tests := []struct {
		name       string
		deferUntil *time.Time
		want       bool
	}{
		{"nil", nil, false},
		{"past", &past, false},
		{"future", &future, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			iss := Issue{DeferUntil: tc.deferUntil}
			if got := iss.IsDeferred(); got != tc.want {
				t.Errorf("IsDeferred() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDueLabel(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"3 days overdue", -3 * 24 * time.Hour, "3d overdue"},
		{"due today (slightly past)", -2 * time.Hour, "due today"},
		{"due today (slightly future)", 6 * time.Hour, "due today"},
		{"1 day left", 36 * time.Hour, "1d left"},
		{"5 days left", 5*24*time.Hour + 12*time.Hour, "5d left"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			due := time.Now().Add(tc.offset)
			iss := Issue{DueAt: &due}
			got := iss.DueLabel()
			if got != tc.want {
				t.Errorf("DueLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDueLabelNil(t *testing.T) {
	iss := Issue{}
	if got := iss.DueLabel(); got != "" {
		t.Errorf("DueLabel() with nil DueAt = %q, want empty", got)
	}
}

func TestDeferLabel(t *testing.T) {
	future := time.Now().Add(5*24*time.Hour + 12*time.Hour)
	iss := Issue{DeferUntil: &future}
	got := iss.DeferLabel()
	if got != "deferred 5d" {
		t.Errorf("DeferLabel() = %q, want %q", got, "deferred 5d")
	}
}

func TestDeferLabelNil(t *testing.T) {
	iss := Issue{}
	if got := iss.DeferLabel(); got != "" {
		t.Errorf("DeferLabel() with nil DeferUntil = %q, want empty", got)
	}
}

func TestDeferLabelPast(t *testing.T) {
	past := time.Now().Add(-24 * time.Hour)
	iss := Issue{DeferUntil: &past}
	if got := iss.DeferLabel(); got != "" {
		t.Errorf("DeferLabel() with past DeferUntil = %q, want empty", got)
	}
}
