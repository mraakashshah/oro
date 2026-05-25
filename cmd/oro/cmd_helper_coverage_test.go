package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/modelartifacts"
)

func TestModelPathHelpers(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))

	if got, want := resolveModelDir("/custom/models"), "/custom/models"; got != want {
		t.Fatalf("resolveModelDir(custom) = %q, want %q", got, want)
	}
	defaultDir := resolveModelDir("")
	if !strings.HasSuffix(defaultDir, filepath.Join(".oro", "models")) {
		t.Fatalf("resolveModelDir(default) = %q, want .oro/models suffix", defaultDir)
	}

	spec := modelartifacts.ModelSpec{Name: "bge-small", Filename: "model.onnx"}
	if got, want := modelLocalPath("/models", spec), filepath.Join("/models", "bge-small", "model.onnx"); got != want {
		t.Fatalf("modelLocalPath() = %q, want %q", got, want)
	}
}

func TestOutlineAndDriftFormattingHelpers(t *testing.T) {
	if _, err := outlineExtract("notes.txt"); err == nil || !strings.Contains(err.Error(), "unsupported file extension: .txt") {
		t.Fatalf("outlineExtract unsupported extension error = %v", err)
	}

	if got := truncateLine("short", 10); got != "short" {
		t.Fatalf("truncateLine short = %q", got)
	}
	if got, want := truncateLine("abcdef", 3), "abc\u2026"; got != want {
		t.Fatalf("truncateLine long = %q, want %q", got, want)
	}
}

func TestLogAndWorkFormattingHelpers(t *testing.T) {
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	if got, err := parseEventSince("", now); err != nil || got != "" {
		t.Fatalf("parseEventSince empty = %q, %v", got, err)
	}
	if got, err := parseEventSince("30m", now); err != nil || got != "2026-05-21 11:30:00" {
		t.Fatalf("parseEventSince duration = %q, %v", got, err)
	}
	if got, err := parseEventSince("2026-05-21T10:15:00Z", now); err != nil || got != "2026-05-21 10:15:00" {
		t.Fatalf("parseEventSince RFC3339 = %q, %v", got, err)
	}
	if _, err := parseEventSince("not-a-time", now); err == nil {
		t.Fatal("parseEventSince invalid input succeeded")
	}

	if got := truncate("abcdef", 3); got != "abc..." {
		t.Fatalf("truncate() = %q", got)
	}
	if got := buildSearchQuery("title", []string{"bug", "urgent"}); got != "title bug urgent" {
		t.Fatalf("buildSearchQuery() = %q", got)
	}
}

func TestRecommendedRecoveryActionVariants(t *testing.T) {
	cases := []struct {
		name       string
		inspect    recoveryInspection
		wantPrefix string
	}{
		{
			name:       "resolved",
			inspect:    recoveryInspection{Quarantine: recoveryQuarantineCLIRecord{Status: "resolved"}},
			wantPrefix: "quarantine is already resolved",
		},
		{
			name:       "dirty",
			inspect:    recoveryInspection{Dirty: recoveryDirtyInspection{Total: 1}},
			wantPrefix: "inspect and preserve dirty worktree changes",
		},
		{
			name: "stale active present",
			inspect: recoveryInspection{
				Quarantine: recoveryQuarantineCLIRecord{Reason: "stale_active_assignment"},
				Worktree:   recoveryWorktreeInspection{Exists: true},
				Branch:     recoveryBranchInspection{Exists: true},
			},
			wantPrefix: "worktree and branch are present",
		},
		{
			name:       "missing worktree",
			inspect:    recoveryInspection{Branch: recoveryBranchInspection{Exists: true}},
			wantPrefix: "worktree is missing but branch exists",
		},
		{
			name:       "missing branch",
			inspect:    recoveryInspection{Worktree: recoveryWorktreeInspection{Exists: true}},
			wantPrefix: "worktree exists but branch is missing",
		},
		{
			name:       "empty",
			inspect:    recoveryInspection{},
			wantPrefix: "branch and worktree are absent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recommendedRecoveryAction(tc.inspect); !strings.HasPrefix(got, tc.wantPrefix) {
				t.Fatalf("recommendedRecoveryAction() = %q, want prefix %q", got, tc.wantPrefix)
			}
		})
	}
}
