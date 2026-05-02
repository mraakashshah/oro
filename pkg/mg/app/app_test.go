package app

import (
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/mg/data"
	"oro/pkg/protocol"

	tea "charm.land/bubbletea/v2"
)

func testIssue(id string, status data.Status) data.Issue {
	now := time.Now()
	return data.Issue{
		ID:        id,
		Title:     id,
		Status:    status,
		Priority:  data.PriorityMedium,
		IssueType: data.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestFileChangedMsgPreservesSelectionAndClosedState(t *testing.T) {
	issues := []data.Issue{
		testIssue("open-1", data.StatusOpen),
		testIssue("open-2", data.StatusOpen),
		testIssue("closed-1", data.StatusClosed),
	}

	m := New(issues, data.Source{}, data.DefaultBlockingTypes)
	m.startedAt = time.Now().Add(-time.Second) // bypass startup guard
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	got := model.(Model)

	// Move selection to second open issue.
	model, _ = got.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	got = model.(Model)
	if got.parade.SelectedIssue == nil || got.parade.SelectedIssue.ID != "open-2" {
		t.Fatalf("expected selected issue open-2 before refresh, got %+v", got.parade.SelectedIssue)
	}

	// Expand closed section.
	model, _ = got.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	got = model.(Model)
	if !got.parade.ShowClosed {
		t.Fatal("expected closed section expanded before refresh")
	}

	// Simulate file refresh with same issues.
	model, _ = got.Update(data.FileChangedMsg{Issues: issues})
	got = model.(Model)

	if !got.parade.ShowClosed {
		t.Fatal("expected closed section to remain expanded after refresh")
	}
	if got.parade.SelectedIssue == nil || got.parade.SelectedIssue.ID != "open-2" {
		t.Fatalf("expected selected issue open-2 after refresh, got %+v", got.parade.SelectedIssue)
	}
}

func TestFilteringModeAcceptsTypedInput(t *testing.T) {
	issues := []data.Issue{
		testIssue("alpha-1", data.StatusOpen),
		testIssue("beta-1", data.StatusOpen),
	}

	m := New(issues, data.Source{}, data.DefaultBlockingTypes)
	m.startedAt = time.Now().Add(-time.Second) // bypass startup guard
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	got := model.(Model)

	model, _ = got.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	got = model.(Model)
	if !got.filtering {
		t.Fatal("expected filtering mode to be active after pressing /")
	}

	model, _ = got.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	got = model.(Model)
	if got.filterInput.Value() != "b" {
		t.Fatalf("expected filter input value %q, got %q", "b", got.filterInput.Value())
	}
}

func TestFilteringModeQStillQuits(t *testing.T) {
	issues := []data.Issue{
		testIssue("alpha-1", data.StatusOpen),
	}

	m := New(issues, data.Source{}, data.DefaultBlockingTypes)
	m.startedAt = time.Now().Add(-time.Second) // bypass startup guard
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	got := model.(Model)

	model, _ = got.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	got = model.(Model)
	if !got.filtering {
		t.Fatal("expected filtering mode to be active after pressing /")
	}

	_, cmd := got.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("expected quit command when pressing q in filtering mode")
	}

	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from quit command, got %T", msg)
	}
}

func TestHelpCanOpenFromFilteringMode(t *testing.T) {
	issues := []data.Issue{
		testIssue("alpha-1", data.StatusOpen),
	}

	m := New(issues, data.Source{}, data.DefaultBlockingTypes)
	m.startedAt = time.Now().Add(-time.Second) // bypass startup guard
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	got := model.(Model)

	model, _ = got.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	got = model.(Model)
	if !got.filtering {
		t.Fatal("expected filtering mode to be active after pressing /")
	}

	model, _ = got.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	got = model.(Model)
	if !got.showHelp {
		t.Fatal("expected help overlay to open from filtering mode")
	}
	if !got.filtering {
		t.Fatal("expected filtering mode state to be preserved while help is open")
	}

	// Closing help should return to prior mode.
	model, _ = got.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	got = model.(Model)
	if got.showHelp {
		t.Fatal("expected help overlay to close on esc")
	}
	if !got.filtering {
		t.Fatal("expected filtering mode to resume after closing help")
	}
}

func TestFetchIssueDetailJSONLUsesLoadedIssuesWithoutBd(t *testing.T) {
	t.Setenv("PATH", "")
	issue := testIssue("mg-1", data.StatusOpen)
	issue.Notes = "loaded notes"
	issue.AcceptanceCriteria = "loaded acceptance"

	m := New([]data.Issue{issue}, data.Source{Mode: data.SourceJSONL}, data.DefaultBlockingTypes)
	msg := m.fetchIssueDetail("mg-1")()

	got, ok := msg.(issueDetailMsg)
	if !ok {
		t.Fatalf("message = %T, want issueDetailMsg", msg)
	}
	if got.err != nil {
		t.Fatalf("fetchIssueDetail returned error with bd absent: %v", got.err)
	}
	if got.issue == nil {
		t.Fatal("fetchIssueDetail returned nil issue")
	}
	if got.issue.Notes != "loaded notes" || got.issue.AcceptanceCriteria != "loaded acceptance" {
		t.Fatalf("detail = notes %q acceptance %q, want loaded issue fields", got.issue.Notes, got.issue.AcceptanceCriteria)
	}

	msg = m.fetchIssueDetail("missing")()
	got, ok = msg.(issueDetailMsg)
	if !ok {
		t.Fatalf("message = %T, want issueDetailMsg", msg)
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "issue missing not found") {
		t.Fatalf("missing issue error = %v, want not found", got.err)
	}
}

func TestIssueDetailMsgIgnoresStaleJSONLSnapshot(t *testing.T) {
	oldIssue := testIssue("mg-1", data.StatusOpen)
	oldIssue.Notes = "old notes"
	newIssue := testIssue("mg-1", data.StatusOpen)
	newIssue.Notes = "fresh notes"

	m := New([]data.Issue{oldIssue}, data.Source{Mode: data.SourceJSONL}, data.DefaultBlockingTypes)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	got := model.(Model)
	if got.detail.Issue == nil || got.detail.Issue.ID != "mg-1" {
		t.Fatalf("selected detail = %+v, want mg-1", got.detail.Issue)
	}

	staleCmd := got.fetchIssueDetail("mg-1")

	model, _ = got.Update(data.FileChangedMsg{Issues: []data.Issue{newIssue}})
	got = model.(Model)
	if got.detail.Issue == nil || got.detail.Issue.Notes != "fresh notes" {
		t.Fatalf("detail after refresh = %+v, want fresh notes", got.detail.Issue)
	}

	model, _ = got.Update(staleCmd())
	got = model.(Model)
	if got.detail.Issue == nil || got.detail.Issue.Notes != "fresh notes" {
		t.Fatalf("stale detail applied: %+v", got.detail.Issue)
	}
	if got.detail.RichIssueID != "" {
		t.Fatalf("stale detail should not mark rich detail loaded, got %q", got.detail.RichIssueID)
	}
}

func TestIssueDetailMsgAllowsFreshStoreDetailAfterRefresh(t *testing.T) {
	issue := testIssue("mg-1", data.StatusOpen)
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:        "mg-1",
		Title:     "mg-1",
		Status:    "open",
		Priority:  2,
		Type:      "task",
		Notes:     "store notes",
		CreatedAt: issue.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt: issue.UpdatedAt.Format(time.RFC3339Nano),
	})

	m := New([]data.Issue{issue}, data.Source{Mode: data.SourceCLI, Store: store}, data.DefaultBlockingTypes)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	got := model.(Model)
	detailCmd := got.fetchIssueDetail("mg-1")

	model, _ = got.Update(data.ClosedIssuesMsg{Issues: []data.Issue{testIssue("closed-1", data.StatusClosed)}})
	got = model.(Model)
	if got.sourceVersion == 0 {
		t.Fatal("expected refresh to advance sourceVersion")
	}

	model, _ = got.Update(detailCmd())
	got = model.(Model)
	if got.detail.Issue == nil || got.detail.Issue.Notes != "store notes" {
		t.Fatalf("store detail was incorrectly dropped after refresh: %+v", got.detail.Issue)
	}
	if got.detail.RichIssueID != "mg-1" {
		t.Fatalf("RichIssueID = %q, want mg-1", got.detail.RichIssueID)
	}
}
