package data

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
			name: "JSONL with custom path",
			src:  Source{Mode: SourceJSONL, Path: "/foo/custom-export.jsonl"},
			want: "custom-export.jsonl",
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

func TestFetchAllClosedCmdReturnsIssuesAndErrors(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "open", Status: "open", UpdatedAt: "2026-01-01T00:00:00Z"},
		protocol.Bead{ID: "closed", Status: "closed", UpdatedAt: "2026-01-01T00:00:00Z"},
	)
	msg := FetchAllClosedCmd(store)()
	closed, ok := msg.(ClosedIssuesMsg)
	if !ok {
		t.Fatalf("FetchAllClosedCmd() message = %T, want ClosedIssuesMsg", msg)
	}
	if closed.Err != nil || len(closed.Issues) != 1 || closed.Issues[0].ID != "closed" {
		t.Fatalf("FetchAllClosedCmd() = %#v, want only the closed issue", closed)
	}

	msg = FetchAllClosedCmd(nil)()
	closed, ok = msg.(ClosedIssuesMsg)
	if !ok || closed.Err == nil {
		t.Fatalf("FetchAllClosedCmd(nil) = %#v, want ClosedIssuesMsg with error", msg)
	}
}

func TestSourceFetchActiveIssuesRejectsNilStore(t *testing.T) {
	_, err := FetchActiveIssues(nil)
	if err == nil {
		t.Fatal("FetchActiveIssues(nil) error = nil, want bead store is nil")
	}
	if !strings.Contains(err.Error(), "bead store is nil") {
		t.Fatalf("FetchActiveIssues(nil) error = %v, want bead store is nil", err)
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

func TestSourceFetchIssuesRejectsNilStore(t *testing.T) {
	_, err := FetchIssues(nil)
	if err == nil {
		t.Fatal("FetchIssues(nil) error = nil, want bead store is nil")
	}
	if !strings.Contains(err.Error(), "bead store is nil") {
		t.Fatalf("FetchIssues(nil) error = %v, want bead store is nil", err)
	}
}

func TestSourceFetchIssuesWrapsExportError(t *testing.T) {
	store := rawExportStore{
		FakeStore: beadstore.NewFakeStore(),
		err:       errors.New("disk unavailable"),
	}

	_, err := FetchIssues(store)
	if err == nil {
		t.Fatal("FetchIssues() error = nil, want export error")
	}
	if !strings.Contains(err.Error(), "export beads: disk unavailable") {
		t.Fatalf("FetchIssues() error = %v, want wrapped export error", err)
	}
}

func TestSourceFetchIssuesRejectsInvalidExportJSONL(t *testing.T) {
	store := rawExportStore{
		FakeStore: beadstore.NewFakeStore(),
		export:    []byte("{not-json}\n"),
	}

	_, err := FetchIssues(store)
	if err == nil {
		t.Fatal("FetchIssues() error = nil, want parse error")
	}
	if !strings.Contains(err.Error(), "parse bead export") {
		t.Fatalf("FetchIssues() error = %v, want parse bead export error", err)
	}
}

func TestSourceFetchIssuesIgnoresBlankExportLines(t *testing.T) {
	export := "\n" +
		`{"id":"mg-1","title":"first","status":"open","priority":1,"type":"task","updated_at":"2026-03-01T00:00:00Z"}` + "\n" +
		"   \n" +
		`{"id":"mg-2","title":"second","status":"closed","priority":2,"type":"bug","updated_at":"2026-03-02T00:00:00Z","closed_at":"2026-03-02T00:00:00Z"}` + "\n"
	store := rawExportStore{
		FakeStore: beadstore.NewFakeStore(),
		export:    []byte(export),
	}

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

func TestSourceFetchRecentClosedRejectsNilStore(t *testing.T) {
	_, err := fetchRecentClosed(nil, 5)
	if err == nil {
		t.Fatal("fetchRecentClosed(nil) error = nil, want bead store is nil")
	}
	if !strings.Contains(err.Error(), "bead store is nil") {
		t.Fatalf("fetchRecentClosed(nil) error = %v, want bead store is nil", err)
	}
}

func TestSourceFetchRecentClosedWrapsStoreError(t *testing.T) {
	store := closedErrorStore{
		FakeStore: beadstore.NewFakeStore(),
		err:       errors.New("closed query failed"),
	}

	_, err := fetchRecentClosed(store, 5)
	if err == nil {
		t.Fatal("fetchRecentClosed() error = nil, want closed query error")
	}
	if !strings.Contains(err.Error(), "fetch closed beads: closed query failed") {
		t.Fatalf("fetchRecentClosed() error = %v, want wrapped closed query error", err)
	}
}

func TestSourceFetchAllClosedPropagatesFetchIssuesError(t *testing.T) {
	store := rawExportStore{
		FakeStore: beadstore.NewFakeStore(),
		err:       errors.New("export unavailable"),
	}

	_, err := FetchAllClosed(store)
	if err == nil {
		t.Fatal("FetchAllClosed() error = nil, want fetch issues error")
	}
	if !strings.Contains(err.Error(), "export beads: export unavailable") {
		t.Fatalf("FetchAllClosed() error = %v, want wrapped export error", err)
	}
}

func TestSourceFetchAllClosedFiltersClosedIssues(t *testing.T) {
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "mg-open", Title: "open task", Status: "open", Priority: 2, Type: "task", UpdatedAt: "2026-03-01T00:00:00Z"},
		protocol.Bead{ID: "mg-closed", Title: "closed task", Status: "closed", Priority: 1, Type: "bug", UpdatedAt: "2026-03-02T00:00:00Z", ClosedAt: "2026-03-02T00:00:00Z"},
	)

	issues, err := FetchAllClosed(store)
	if err != nil {
		t.Fatalf("FetchAllClosed() error = %v", err)
	}
	gotIDs := sourceIssueIDs(issues)
	wantIDs := []string{"mg-closed"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("issue IDs = %v, want %v", gotIDs, wantIDs)
	}
}

func TestSourceStorePreservesTags(t *testing.T) {
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:        "mg-1",
		Title:     "tagged task",
		Status:    "open",
		Priority:  2,
		Type:      "task",
		UpdatedAt: "2026-03-01T00:00:00Z",
		Tags:      []string{"phase-5", "mg"},
	})

	issues, err := FetchIssues(store)
	if err != nil {
		t.Fatalf("FetchIssues() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d, want 1", len(issues))
	}
	if !slices.Equal(issues[0].Tags, []string{"phase-5", "mg"}) {
		t.Fatalf("Tags = %v, want %v", issues[0].Tags, []string{"phase-5", "mg"})
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

func TestFetchIssueDetailFromIssuesUsesLoadedJSONL(t *testing.T) {
	issues := []Issue{
		{
			ID:                 "mg-1",
			Title:              "detail",
			Status:             StatusOpen,
			Priority:           PriorityHigh,
			IssueType:          TypeTask,
			Notes:              "jsonl notes",
			AcceptanceCriteria: "jsonl acceptance",
		},
	}

	issue, err := FetchIssueDetailFromIssues(issues, "mg-1")
	if err != nil {
		t.Fatalf("FetchIssueDetailFromIssues() error = %v", err)
	}
	if issue.Notes != "jsonl notes" || issue.AcceptanceCriteria != "jsonl acceptance" {
		t.Fatalf("detail = notes %q acceptance %q, want JSONL fields", issue.Notes, issue.AcceptanceCriteria)
	}

	_, err = FetchIssueDetailFromIssues(issues, "missing")
	if err == nil {
		t.Fatal("FetchIssueDetailFromIssues() error = nil, want missing issue error")
	}
	if !strings.Contains(err.Error(), "issue missing not found") {
		t.Fatalf("FetchIssueDetailFromIssues() error = %v, want missing issue error", err)
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

func TestParseIssuesCLIOutputRejectsWrongPrefixAlongsideHQ(t *testing.T) {
	out := mustMarshalIssues(t, []Issue{
		{ID: "hq-1", Title: "hq item", Status: StatusOpen, Priority: PriorityLow, IssueType: TypeTask},
		{ID: "vv-12", Title: "wrong project", Status: StatusOpen, Priority: PriorityMedium, IssueType: TypeBug},
	})

	_, err := parseIssuesCLIOutput(out, "mg")
	if err == nil {
		t.Fatal("expected wrong-prefix validation error, got nil")
	}
	if !strings.Contains(err.Error(), `"vv" issues`) {
		t.Fatalf("expected wrong vv prefix only, got %v", err)
	}
	if strings.Contains(err.Error(), `"hq" issues`) {
		t.Fatalf("hq prefix should be ignored, got %v", err)
	}
}

func TestParseIssuesCLIOutputAllowsExpectedPrefixAlongsideWrongPrefix(t *testing.T) {
	out := mustMarshalIssues(t, []Issue{
		{ID: "mg-1", Title: "expected project", Status: StatusOpen, Priority: PriorityLow, IssueType: TypeTask},
		{ID: "vv-12", Title: "wrong project", Status: StatusOpen, Priority: PriorityMedium, IssueType: TypeBug},
	})

	if _, err := parseIssuesCLIOutput(out, "mg"); err != nil {
		t.Fatalf("parseIssuesCLIOutput() error = %v, want nil when expected prefix is present", err)
	}
}

func TestParseIssuesCLIOutputAllowsMultipleWrongPrefixes(t *testing.T) {
	out := mustMarshalIssues(t, []Issue{
		{ID: "vv-12", Title: "wrong project", Status: StatusOpen, Priority: PriorityMedium, IssueType: TypeBug},
		{ID: "zz-99", Title: "another project", Status: StatusOpen, Priority: PriorityLow, IssueType: TypeTask},
	})

	if _, err := parseIssuesCLIOutput(out, "mg"); err != nil {
		t.Fatalf("parseIssuesCLIOutput() error = %v, want nil for ambiguous multi-prefix output", err)
	}
}

func TestParseIssuesCLIOutputIgnoresIDsWithoutPrefixes(t *testing.T) {
	out := mustMarshalIssues(t, []Issue{
		{ID: "orphan", Title: "legacy id", Status: StatusOpen, Priority: PriorityLow, IssueType: TypeTask},
		{ID: "-missing", Title: "empty prefix", Status: StatusOpen, Priority: PriorityLow, IssueType: TypeTask},
		{ID: "vv-12", Title: "wrong project", Status: StatusOpen, Priority: PriorityMedium, IssueType: TypeBug},
	})

	_, err := parseIssuesCLIOutput(out, "mg")
	if err == nil {
		t.Fatal("expected single wrong-prefix validation error, got nil")
	}
	if !strings.Contains(err.Error(), `"vv" issues`) {
		t.Fatalf("expected wrong vv prefix only, got %v", err)
	}
}

func TestSourceUsesProjectPaths(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up a custom task metadata dir.
	customBeadsDir := filepath.Join(tmpDir, "custom-beads")
	if err := os.MkdirAll(customBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customBeadsDir, "config.yaml"),
		[]byte("issue-prefix: pp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := beadstore.NewFakeStore()
	src := newSource(store, tmpDir)
	if src.Store != store {
		t.Errorf("Store = %p, want %p", src.Store, store)
	}
	if src.ProjectDir != tmpDir {
		t.Errorf("ProjectDir = %q, want %q", src.ProjectDir, tmpDir)
	}
	if src.Mode != SourceCLI {
		t.Errorf("Mode = %v, want SourceCLI", src.Mode)
	}

	// LoadIssuePrefix uses configurable task metadata dir.
	prefix := LoadIssuePrefix(tmpDir, customBeadsDir)
	if prefix != "pp" {
		t.Errorf("LoadIssuePrefix(tmpDir, customBeadsDir) = %q, want %q", prefix, "pp")
	}

	// LoadMetadataSchema uses configurable task metadata dir.
	schema := LoadMetadataSchema(tmpDir, customBeadsDir)
	if schema != nil {
		t.Errorf("LoadMetadataSchema(tmpDir, customBeadsDir) = %v, want nil (no metadata section)", schema)
	}
}

func TestIssuePrefixFromID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{
			name: "standard bead id",
			id:   "oro-123",
			want: "oro",
		},
		{
			name: "trim whitespace",
			id:   "  mg-456  ",
			want: "mg",
		},
		{
			name: "missing dash",
			id:   "orphan",
			want: "",
		},
		{
			name: "empty prefix",
			id:   "-123",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := issuePrefixFromID(tt.id); got != tt.want {
				t.Fatalf("issuePrefixFromID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
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
	err    error
}

func (s rawExportStore) Export(ctx context.Context) ([]byte, error) {
	return s.export, s.err
}

type closedErrorStore struct {
	*beadstore.FakeStore
	err error
}

func (s closedErrorStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	return nil, s.err
}
