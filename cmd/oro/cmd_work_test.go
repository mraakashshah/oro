package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/dispatcher"
	"oro/pkg/merge"
	"oro/pkg/protocol"
)

type failingStandaloneWorktreeManager struct {
	dispatcher.WorktreeManager
	prepareCalls int
}

type preFFCheckMerger struct {
	opts merge.Opts
}

func (m *preFFCheckMerger) Merge(ctx context.Context, opts merge.Opts) (*merge.Result, error) {
	m.opts = opts
	if opts.PreFFCheck == nil {
		return nil, errors.New("PreFFCheck was not configured")
	}
	return nil, opts.PreFFCheck(ctx, opts.Worktree)
}

func TestWorkNoSeparatePreRebaseQG(t *testing.T) {
	merger := &preFFCheckMerger{}
	var recorded dispatcher.QGFailureRecord
	qgCalls := 0
	deps := &workDeps{
		merger: merger,
		runQG: func(_ context.Context, worktree string, skipMutation bool) (bool, string, error) {
			qgCalls++
			if worktree != "/tmp/worktree" {
				t.Fatalf("runQG worktree = %q, want /tmp/worktree", worktree)
			}
			if !skipMutation {
				t.Fatal("post-rebase QG must skip mutation testing by default")
			}
			return false, "post-rebase qg failed", nil
		},
		recordQGFailure: func(_ context.Context, rec dispatcher.QGFailureRecord, _ dispatcher.QGFailureClassification) error {
			recorded = rec
			return nil
		},
	}
	cfg := &workConfig{beadID: "oro-work-qg"}

	_, err := mergeToMain(context.Background(), cfg, deps, "/tmp/worktree", "agent/oro-work-qg", "main")
	if err == nil {
		t.Fatal("mergeToMain error = nil, want exitError")
	}
	var exitErr *exitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("mergeToMain error = %T %v, want *exitError", err, err)
	}
	if exitErr.code != exitCodeRetries {
		t.Fatalf("exit code = %d, want %d", exitErr.code, exitCodeRetries)
	}
	if qgCalls != 1 {
		t.Fatalf("runQG calls = %d, want one post-rebase callback", qgCalls)
	}
	if recorded.Component != "oro-work-pre-merge" {
		t.Fatalf("recorded component = %q, want oro-work-pre-merge", recorded.Component)
	}
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

	t.Run("ordinary child remains runnable when epic is only ahead", func(t *testing.T) {
		wtMgr := newAheadStandaloneWorktreeManager(t, branch)
		err := prepareStandaloneWorkTargetBranch(context.Background(), &workDeps{wtMgr: wtMgr}, branch, "main", epicID, &protocol.BeadDetail{Title: "Implement epic work"})
		if err != nil {
			t.Fatalf("prepareStandaloneWorkTargetBranch: %v", err)
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

func newAheadStandaloneWorktreeManager(t *testing.T, branch string) dispatcher.WorktreeManager {
	t.Helper()
	repo := t.TempDir()
	initRecoveryTestRepo(t, repo, branch)
	if err := os.WriteFile(filepath.Join(repo, "epic.txt"), []byte("epic\n"), 0o644); err != nil {
		t.Fatalf("write epic commit: %v", err)
	}
	runRecoveryGit(t, repo, "add", "epic.txt")
	runRecoveryGit(t, repo, "commit", "-m", "epic commit")
	runRecoveryGit(t, repo, "checkout", "main")
	return dispatcher.NewGitWorktreeManager(repo, "", "", &dispatcher.ExecCommandRunner{})
}
