package main

import (
	"context"
	"fmt"
	"testing"

	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
)

type divergedStandaloneWorktreeManager struct {
	dispatcher.WorktreeManager
	prepareCalls int
	uniqueCalls  int
}

func (m *divergedStandaloneWorktreeManager) PrepareBaseBranchForAssignment(_ context.Context, _, _ string) (bool, error) {
	m.prepareCalls++
	return false, fmt.Errorf("epic branch diverged from base")
}

func (m *divergedStandaloneWorktreeManager) BaseBranchHasUniqueCommits(_ context.Context, _, _ string) (bool, error) {
	m.uniqueCalls++
	return true, nil
}

func TestWorkAllowsRebaseChildAgainstDivergedEpicBranch(t *testing.T) {
	const (
		epicID = "oro-26yy"
		branch = protocol.EpicBranchPrefix + epicID
	)
	t.Run("rebase child bypasses divergence guards", func(t *testing.T) {
		wtMgr := &divergedStandaloneWorktreeManager{}
		err := prepareStandaloneWorkTargetBranch(context.Background(), &workDeps{wtMgr: wtMgr}, branch, "main", epicID, &protocol.BeadDetail{Title: "Rebase " + branch + " onto main"})
		if err != nil {
			t.Fatalf("prepareStandaloneWorkTargetBranch: %v", err)
		}
		if wtMgr.prepareCalls != 0 || wtMgr.uniqueCalls != 0 {
			t.Fatalf("diverged rebase child checks = prepare:%d unique:%d, want neither guard", wtMgr.prepareCalls, wtMgr.uniqueCalls)
		}
	})

	t.Run("ordinary child remains blocked", func(t *testing.T) {
		wtMgr := &divergedStandaloneWorktreeManager{}
		err := prepareStandaloneWorkTargetBranch(context.Background(), &workDeps{wtMgr: wtMgr}, branch, "main", epicID, &protocol.BeadDetail{Title: "Implement epic work"})
		if err == nil {
			t.Fatal("prepareStandaloneWorkTargetBranch error = nil, want diverged branch rejection")
		}
	})
}
