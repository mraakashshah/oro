package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/dispatcher"
	"oro/pkg/protocol"
)

type failingStandaloneWorktreeManager struct {
	dispatcher.WorktreeManager
	prepareCalls int
}

func (m *failingStandaloneWorktreeManager) PrepareBaseBranchForAssignment(_ context.Context, _, _ string) (bool, error) {
	m.prepareCalls++
	return false, fmt.Errorf("rev-parse target branch: %w", errors.ErrUnsupported)
}

func TestWorkAllowsRebaseChildAgainstDivergedEpicBranch(t *testing.T) {
	const (
		epicID = "oro-26yy"
		branch = protocol.EpicBranchPrefix + epicID
	)
	t.Run("rebase child bypasses divergence guards", func(t *testing.T) {
		wtMgr := newDivergedStandaloneWorktreeManager(t, branch)
		err := prepareStandaloneWorkTargetBranch(context.Background(), &workDeps{wtMgr: wtMgr}, branch, "main", epicID, &protocol.BeadDetail{Title: "Rebase " + branch + " onto main"})
		if err != nil {
			t.Fatalf("prepareStandaloneWorkTargetBranch: %v", err)
		}
	})

	t.Run("ordinary child remains blocked", func(t *testing.T) {
		wtMgr := newDivergedStandaloneWorktreeManager(t, branch)
		err := prepareStandaloneWorkTargetBranch(context.Background(), &workDeps{wtMgr: wtMgr}, branch, "main", epicID, &protocol.BeadDetail{Title: "Implement epic work"})
		if err == nil {
			t.Fatal("prepareStandaloneWorkTargetBranch error = nil, want diverged branch rejection")
		}
	})

	t.Run("rebase child still fails on operational preparation error", func(t *testing.T) {
		wtMgr := &failingStandaloneWorktreeManager{}
		err := prepareStandaloneWorkTargetBranch(context.Background(), &workDeps{wtMgr: wtMgr}, branch, "main", epicID, &protocol.BeadDetail{Title: "Rebase " + branch + " onto main"})
		if err == nil {
			t.Fatal("prepareStandaloneWorkTargetBranch error = nil, want operational error")
		}
		if wtMgr.prepareCalls != 1 {
			t.Fatalf("prepare calls = %d, want 1", wtMgr.prepareCalls)
		}
	})
}

func newDivergedStandaloneWorktreeManager(t *testing.T, branch string) dispatcher.WorktreeManager {
	t.Helper()
	repo := t.TempDir()
	initRecoveryTestRepo(t, repo, branch)
	if err := os.WriteFile(filepath.Join(repo, "epic.txt"), []byte("epic\n"), 0o644); err != nil {
		t.Fatalf("write epic commit: %v", err)
	}
	runRecoveryGit(t, repo, "add", "epic.txt")
	runRecoveryGit(t, repo, "commit", "-m", "epic commit")
	runRecoveryGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main commit: %v", err)
	}
	runRecoveryGit(t, repo, "add", "main.txt")
	runRecoveryGit(t, repo, "commit", "-m", "main commit")
	return dispatcher.NewGitWorktreeManager(repo, "", "", &dispatcher.ExecCommandRunner{})
}
