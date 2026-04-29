package data

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

func TestSourceLabelJSONL(t *testing.T) {
	tests := []struct {
		name string
		src  Source
		want string
	}{
		{
			name: "JSONL with path",
			src:  Source{Mode: SourceJSONL, Path: "/foo/.beads/issues.jsonl"},
			want: "issues.jsonl",
		},
		{
			name: "JSONL empty path",
			src:  Source{Mode: SourceJSONL},
			want: "issues.jsonl",
		},
		{
			name: "CLI mode",
			src:  Source{Mode: SourceCLI},
			want: "bead store",
		},
		{
			name: "CLI mode ignores path",
			src:  Source{Mode: SourceCLI, Path: "/foo/bar"},
			want: "bead store",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.src.Label()
			if got != tt.want {
				t.Errorf("Source.Label() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckBdVersionKnownBroken(t *testing.T) {
	got := parseBdVersionWarning("bd version 0.59.0")
	if got == "" {
		t.Fatal("expected warning for v0.59.0, got empty string")
	}
	if got != "bd v0.59.0 has a known bug where --json is ignored; upgrade to v0.60.0+" {
		t.Errorf("unexpected warning: %q", got)
	}
}

func TestCheckBdVersionOK(t *testing.T) {
	got := parseBdVersionWarning("bd version 0.58.0")
	if got != "" {
		t.Errorf("expected no warning for v0.58.0, got %q", got)
	}
}

func TestCheckBdVersionUnparseable(t *testing.T) {
	cases := []string{
		"",
		"garbled output here",
		"bd",
		"\x00\xff",
	}
	for _, input := range cases {
		got := parseBdVersionWarning(input)
		if got != "" {
			t.Errorf("parseBdVersionWarning(%q) = %q, want empty", input, got)
		}
	}
}

func TestBdListArgs(t *testing.T) {
	args := bdListArgs()
	got := strings.Join(args, " ")
	want := "list --json --limit 0 --all"
	if got != want {
		t.Fatalf("bdListArgs() = %q, want %q", got, want)
	}
}

func TestSourceFetchActiveIssuesUsesFakeStore(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "mg-2", Title: "open task", Status: "open", Priority: 2, Type: "task", UpdatedAt: "2026-03-02T00:00:00Z"},
		protocol.Bead{ID: "mg-1", Title: "in flight", Status: "in_progress", Priority: 1, Type: "bug", Epic: "mg-parent", UpdatedAt: "2026-03-03T00:00:00.123456Z"},
		protocol.Bead{ID: "mg-3", Title: "blocked task", Status: "blocked", Priority: 3, Type: "feature", UpdatedAt: "2026-03-01T00:00:00Z"},
		protocol.Bead{ID: "mg-5", Title: "deferred task", Status: "open", Priority: 4, Type: "task", DeferUntil: "2026-04-30T00:00:00Z", UpdatedAt: "2026-03-05T00:00:00Z"},
		protocol.Bead{ID: "mg-4", Title: "closed task", Status: "closed", Priority: 0, Type: "task", UpdatedAt: "2026-03-04T00:00:00Z", ClosedAt: "2026-03-04T00:00:00Z"},
	)

	issues, err := FetchActiveIssues(store)
	if err != nil {
		t.Fatalf("FetchActiveIssues() error = %v", err)
	}
	gotIDs := sourceIssueIDs(issues)
	wantIDs := []string{"mg-1", "mg-2", "mg-3", "mg-5"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("issue IDs = %v, want %v", gotIDs, wantIDs)
	}
	if issues[0].IssueType != TypeBug {
		t.Fatalf("IssueType = %q, want %q", issues[0].IssueType, TypeBug)
	}
	if issues[0].ParentIDValue != "mg-parent" || issues[0].ParentID() != "mg-parent" {
		t.Fatalf("parent = field %q accessor %q, want mg-parent", issues[0].ParentIDValue, issues[0].ParentID())
	}
}

func TestSourceFetchIssuesUsesFakeStoreExportForAllIssues(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "mg-1", Title: "open task", Status: "open", Priority: 2, Type: "task", UpdatedAt: "2026-03-01T00:00:00Z"},
		protocol.Bead{ID: "mg-2", Title: "closed task", Status: "closed", Priority: 1, Type: "bug", UpdatedAt: "2026-03-02T00:00:00Z", ClosedAt: "2026-03-02T00:00:00Z"},
	)

	issues, err := FetchIssues(store)
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}
	gotIDs := sourceIssueIDs(issues)
	wantIDs := []string{"mg-1", "mg-2"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("issue IDs = %v, want %v", gotIDs, wantIDs)
	}
}

func TestSourceFetchIssuesAcceptsNativeExportShape(t *testing.T) {
	store := rawExportStore{
		FakeStore: beadstore.NewFakeStore(),
		export:    []byte(`{"id":"child-flat","title":"child","status":"open","priority":1,"type":"task","parent_id":"parent-1","updated_at":"2026-03-01T00:00:00Z"}` + "\n"),
	}

	issues, err := FetchIssues(store)
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d, want 1", len(issues))
	}
	if issues[0].IssueType != TypeTask {
		t.Fatalf("IssueType = %q, want %q", issues[0].IssueType, TypeTask)
	}
	if issues[0].ParentIDValue != "parent-1" || issues[0].ParentID() != "parent-1" {
		t.Fatalf("parent = field %q accessor %q, want parent-1", issues[0].ParentIDValue, issues[0].ParentID())
	}
}

func TestFetchIssueDetailUsesStoreShow(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:                 "mg-1",
		Title:              "detail",
		Status:             "open",
		Priority:           1,
		Type:               "task",
		Epic:               "mg-parent",
		UpdatedAt:          "2026-03-01T00:00:00Z",
		Notes:              "store notes",
		AcceptanceCriteria: "store acceptance",
	})

	issue, err := FetchIssueDetail(store, "mg-1")
	if err != nil {
		t.Fatalf("FetchIssueDetail() error = %v", err)
	}
	if issue.Notes != "store notes" || issue.AcceptanceCriteria != "store acceptance" {
		t.Fatalf("detail = notes %q acceptance %q, want store fields", issue.Notes, issue.AcceptanceCriteria)
	}
	if issue.ParentID() != "mg-parent" {
		t.Fatalf("ParentID() = %q, want mg-parent", issue.ParentID())
	}
}

func TestParseIssuesCLIOutputRejectsWrongSinglePrefix(t *testing.T) {
	out := mustMarshalIssues(t, []Issue{
		{ID: "vv-12", Title: "wrong project", Status: StatusOpen, Priority: PriorityMedium, IssueType: TypeBug},
	})

	_, err := parseIssuesCLIOutput(out, "mg")
	if err == nil {
		t.Fatal("expected prefix validation error, got nil")
	}
	if !strings.Contains(err.Error(), `expects "mg"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), `"vv" issues`) {
		t.Fatalf("expected wrong prefix in error, got %v", err)
	}
}

func TestParseIssuesCLIOutputAllowsExpectedAndHQPrefixes(t *testing.T) {
	out := mustMarshalIssues(t, []Issue{
		{ID: "hq-1", Title: "hq item", Status: StatusOpen, Priority: PriorityLow, IssueType: TypeTask},
		{ID: "mg-2", Title: "local item", Status: StatusOpen, Priority: PriorityMedium, IssueType: TypeBug},
	})

	issues, err := parseIssuesCLIOutput(out, "mg")
	if err != nil {
		t.Fatalf("parseIssuesCLIOutput() error = %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(issues))
	}
}

func TestSourceUsesProjectPaths(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up a custom beads dir (not the default .beads/)
	customBeadsDir := filepath.Join(tmpDir, "custom-beads")
	if err := os.MkdirAll(customBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customBeadsDir, "config.yaml"),
		[]byte("issue-prefix: pp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := beadstore.NewFakeStore()
	src := NewSource(store, tmpDir)
	if src.Store != store {
		t.Errorf("Store = %p, want %p", src.Store, store)
	}
	if src.ProjectDir != tmpDir {
		t.Errorf("ProjectDir = %q, want %q", src.ProjectDir, tmpDir)
	}
	if src.Mode != SourceCLI {
		t.Errorf("Mode = %v, want SourceCLI", src.Mode)
	}

	// LoadIssuePrefix uses configurable beads dir (not hardcoded .beads)
	prefix := LoadIssuePrefix(tmpDir, customBeadsDir)
	if prefix != "pp" {
		t.Errorf("LoadIssuePrefix(tmpDir, customBeadsDir) = %q, want %q", prefix, "pp")
	}

	// LoadMetadataSchema uses configurable beads dir
	schema := LoadMetadataSchema(tmpDir, customBeadsDir)
	if schema != nil {
		t.Errorf("LoadMetadataSchema(tmpDir, customBeadsDir) = %v, want nil (no metadata section)", schema)
	}
}

func sourceIssueIDs(issues []Issue) []string {
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	return ids
}

func mustMarshalIssues(t *testing.T, issues []Issue) []byte {
	t.Helper()
	out, err := json.Marshal(issues)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return out
}

type rawExportStore struct {
	*beadstore.FakeStore
	export []byte
}

func (s rawExportStore) Export(ctx context.Context) ([]byte, error) {
	return s.export, nil
}
