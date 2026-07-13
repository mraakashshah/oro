package dispatcher //nolint:testpackage // white-box test verifies scan worktree lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJanitorScanWorktree(t *testing.T) {
	t.Parallel()

	d, _, worktrees, _, _, _ := newTestDispatcher(t)
	repoRoot := t.TempDir()
	sourceScript := filepath.Join(repoRoot, "scripts", "janitor_detect.sh")
	if err := os.MkdirAll(filepath.Dir(sourceScript), 0o755); err != nil {
		t.Fatalf("create script directory: %v", err)
	}
	const sourceContents = "#!/usr/bin/env bash\necho detector\n"
	if err := os.WriteFile(sourceScript, []byte(sourceContents), 0o755); err != nil {
		t.Fatalf("write detector script: %v", err)
	}
	d.repoRoot = repoRoot
	d.cfg.DefaultBranch = "trunk"

	var createdIDs []string
	worktrees.createFn = func(_ context.Context, id, baseBranch string) (string, string, error) {
		if baseBranch != "trunk" {
			t.Fatalf("base branch = %q, want trunk", baseBranch)
		}
		createdIDs = append(createdIDs, id)
		path := filepath.Join(t.TempDir(), id)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create worktree: %v", err)
		}
		return path, "agent/" + id, nil
	}

	detectorErr := errors.New("detector failed")
	err := d.withScanWorktree(context.Background(), func(path string) error {
		snapshot := filepath.Join(path, "scripts", "janitor_detect.sh")
		scriptDirInfo, statErr := os.Stat(filepath.Dir(snapshot))
		if statErr != nil {
			t.Fatalf("stat detector snapshot directory: %v", statErr)
		}
		if mode := scriptDirInfo.Mode().Perm(); mode&0o027 != 0 {
			t.Fatalf("detector snapshot directory mode = %o, want no group-write or other permissions", mode)
		}
		contents, readErr := os.ReadFile(snapshot)
		if readErr != nil {
			t.Fatalf("read detector snapshot: %v", readErr)
		}
		if string(contents) != sourceContents {
			t.Fatalf("detector snapshot = %q, want %q", contents, sourceContents)
		}
		if err := os.WriteFile(snapshot, []byte("tampered\n"), 0o755); err != nil {
			t.Fatalf("tamper detector snapshot: %v", err)
		}
		return detectorErr
	})
	if !errors.Is(err, detectorErr) {
		t.Fatalf("withScanWorktree() error = %v, want detector error", err)
	}
	rootContents, err := os.ReadFile(sourceScript)
	if err != nil {
		t.Fatalf("read root detector script: %v", err)
	}
	if string(rootContents) != sourceContents {
		t.Fatalf("root detector script was mutated: got %q, want %q", rootContents, sourceContents)
	}

	if err := d.withScanWorktree(context.Background(), func(string) error { return nil }); err != nil {
		t.Fatalf("second withScanWorktree() error: %v", err)
	}
	if len(createdIDs) != 2 {
		t.Fatalf("created worktrees = %d, want 2", len(createdIDs))
	}
	if createdIDs[0] == createdIDs[1] {
		t.Fatalf("scan worktree IDs collided: %q", createdIDs[0])
	}
	for _, id := range createdIDs {
		if !strings.HasPrefix(id, "janitor-scan-qg-") {
			t.Errorf("scan worktree ID = %q, want epicQGWorktreeID-style prefix", id)
		}
	}

	worktrees.mu.Lock()
	removed := append([]string(nil), worktrees.removed...)
	worktrees.mu.Unlock()
	if len(removed) != 2 {
		t.Fatalf("removed worktrees = %d, want 2 (including detector error)", len(removed))
	}

	t.Run("create failure is logged and the next scan retries", func(t *testing.T) {
		d, _, failedWorktrees, _, _, _ := newTestDispatcher(t)
		attempts := 0
		failedWorktrees.createFn = func(_ context.Context, _, _ string) (string, string, error) {
			attempts++
			if attempts == 1 {
				return "", "", errors.New("git unavailable")
			}
			return t.TempDir(), "agent/janitor", nil
		}

		if err := d.withScanWorktree(context.Background(), func(string) error { return nil }); err == nil {
			t.Fatal("first withScanWorktree() error = nil, want create error")
		}
		if err := d.withScanWorktree(context.Background(), func(string) error { return nil }); err != nil {
			t.Fatalf("second withScanWorktree() error = %v, want retry to succeed", err)
		}
		if attempts != 2 {
			t.Fatalf("create attempts = %d, want 2", attempts)
		}
		if n := eventCount(t, d.db, "janitor_scan_worktree_failed"); n != 1 {
			t.Fatalf("janitor_scan_worktree_failed events = %d, want 1", n)
		}
	})
}
