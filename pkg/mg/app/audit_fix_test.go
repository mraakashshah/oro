package app

import (
	"context"
	"testing"
	"time"

	"oro/pkg/mg/data"

	tea "charm.land/bubbletea/v2"
)

func newReadyModel(t *testing.T) Model {
	t.Helper()

	issues := []data.Issue{
		testIssue("open-1", data.StatusOpen),
		testIssue("open-2", data.StatusOpen),
	}
	m := New(issues, data.Source{}, data.DefaultBlockingTypes)
	m.startedAt = time.Now().Add(-time.Second)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	return model.(Model)
}

func TestSyncSelectionClearsDetailWhenSelectionMissing(t *testing.T) {
	m := newReadyModel(t)
	if m.detail.Issue == nil {
		t.Fatal("expected detail issue to be initialized")
	}

	m.parade.SelectedIssue = nil
	m.syncSelection()

	if m.detail.Issue != nil {
		t.Fatalf("expected detail issue to be cleared, got %+v", m.detail.Issue)
	}
}

func TestCreateBranchCmdUsesProjectDir(t *testing.T) {
	cmd := createBranchCmd(context.Background(), "/tmp/project", "feat/mg-452-test")
	if cmd.Dir != "/tmp/project" {
		t.Fatalf("expected command dir %q, got %q", "/tmp/project", cmd.Dir)
	}
}
