package data

import (
	"context"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

func TestMutateSetStatusUsesStore(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	store := beadstore.NewFakeStore(protocol.Bead{ID: "oro-1", Title: "Issue", Status: "open"})

	if err := SetStatus(store, "oro-1", StatusInProgress); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	got := mustShowBead(t, store, "oro-1")
	if got.Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress", got.Status)
	}
}

func TestMutateClaimIssueUsesStore(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("USER", "alice")
	store := beadstore.NewFakeStore(protocol.Bead{ID: "oro-claim", Title: "Issue", Status: "open"})

	if err := ClaimIssue(store, "oro-claim"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}

	got := mustShowBead(t, store, "oro-claim")
	if got.Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress", got.Status)
	}
	if got.Owner != "alice" {
		t.Fatalf("owner = %q, want alice", got.Owner)
	}
}

func TestMutateClaimIssueRejectsOtherOwner(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("USER", "alice")
	store := beadstore.NewFakeStore(protocol.Bead{ID: "oro-owned", Title: "Issue", Status: "open", Owner: "bob"})

	if err := ClaimIssue(store, "oro-owned"); err == nil {
		t.Fatal("ClaimIssue succeeded for bead owned by another user, want error")
	}

	got := mustShowBead(t, store, "oro-owned")
	if got.Status != "open" || got.Owner != "bob" {
		t.Fatalf("bead changed after rejected claim: %#v", got)
	}
}

func TestMutateCloseIssueUsesStore(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	store := beadstore.NewFakeStore(protocol.Bead{ID: "oro-close", Title: "Issue", Status: "open"})

	if err := CloseIssue(store, "oro-close"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	got := mustShowBead(t, store, "oro-close")
	if got.Status != "closed" {
		t.Fatalf("status = %q, want closed", got.Status)
	}
	if got.ClosedAt == "" {
		t.Fatal("ClosedAt is empty")
	}
}

func TestMutateSetPriorityUsesStore(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	store := beadstore.NewFakeStore(protocol.Bead{ID: "oro-priority", Title: "Issue", Status: "open", Priority: 2})

	if err := SetPriority(store, "oro-priority", PriorityHigh); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}

	got := mustShowBead(t, store, "oro-priority")
	if got.Priority != int(PriorityHigh) {
		t.Fatalf("priority = %d, want %d", got.Priority, PriorityHigh)
	}
}

func TestMutateCreateIssueUsesStore(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	store := beadstore.NewFakeStore()

	id, err := CreateIssue(store, "New issue", TypeBug, PriorityLow)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	got := mustShowBead(t, store, id)
	if got.Title != "New issue" {
		t.Fatalf("title = %q, want New issue", got.Title)
	}
	if got.Type != "bug" {
		t.Fatalf("type = %q, want bug", got.Type)
	}
	if got.Priority != int(PriorityLow) {
		t.Fatalf("priority = %d, want %d", got.Priority, PriorityLow)
	}
}

func TestMutateNilStoreReturnsError(t *testing.T) {
	if err := SetStatus(nil, "oro-1", StatusOpen); err == nil {
		t.Fatal("SetStatus nil store error = nil, want error")
	}
	if err := ClaimIssue(nil, "oro-1"); err == nil {
		t.Fatal("ClaimIssue nil store error = nil, want error")
	}
	if err := CloseIssue(nil, "oro-1"); err == nil {
		t.Fatal("CloseIssue nil store error = nil, want error")
	}
	if err := SetPriority(nil, "oro-1", PriorityHigh); err == nil {
		t.Fatal("SetPriority nil store error = nil, want error")
	}
	if _, err := CreateIssue(nil, "New issue", TypeTask, PriorityMedium); err == nil {
		t.Fatal("CreateIssue nil store error = nil, want error")
	}
}

func mustShowBead(t *testing.T, store beadstore.Store, id string) protocol.Bead {
	t.Helper()
	bead, err := store.Show(context.Background(), id)
	if err != nil {
		t.Fatalf("Show(%s): %v", id, err)
	}
	if bead == nil {
		t.Fatalf("Show(%s): got nil bead", id)
	}
	return *bead
}

func TestBranchName(t *testing.T) {
	tests := []struct {
		name     string
		issue    Issue
		expected string
	}{
		{
			name:     "Bug issue",
			issue:    Issue{ID: "bd-a1b2", Title: "Fix login token expiry", IssueType: TypeBug},
			expected: "fix/bd-a1b2-fix-login-token-expiry",
		},
		{
			name:     "Feature issue",
			issue:    Issue{ID: "bd-c3d4", Title: "Add search feature", IssueType: TypeFeature},
			expected: "feat/bd-c3d4-add-search-feature",
		},
		{
			name:     "Task issue",
			issue:    Issue{ID: "bd-e5f6", Title: "Update documentation", IssueType: TypeTask},
			expected: "task/bd-e5f6-update-documentation",
		},
		{
			name:     "Chore issue",
			issue:    Issue{ID: "bd-g7h8", Title: "Clean up CI config", IssueType: TypeChore},
			expected: "chore/bd-g7h8-clean-up-ci-config",
		},
		{
			name:     "Special characters stripped",
			issue:    Issue{ID: "bd-i9j0", Title: "Handle @mentions & #tags (v2)", IssueType: TypeFeature},
			expected: "feat/bd-i9j0-handle-mentions-tags-v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BranchName(tt.issue)
			if got != tt.expected {
				t.Errorf("BranchName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello World", "hello-world"},
		{"Fix login/auth bug", "fix-login-auth-bug"},
		{"UPPER CASE", "upper-case"},
		{"   spaces   ", "spaces"},
		{"no-change", "no-change"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.expected {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
