package ops //nolint:testpackage // buildRepoManifest is an internal review helper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuildRepoManifestAudit(t *testing.T) {
	t.Run("includes every tracked file and excludes untracked files", func(t *testing.T) {
		repo := t.TempDir()
		writeRepoManifestFile(t, repo, "README.md", "one\ntwo\n")
		writeRepoManifestFile(t, repo, "pkg/example.go", "package pkg\n\nfunc Example() {}\n")
		runGit(t, repo, "init", "-b", "main")
		runGit(t, repo, "add", ".")
		writeRepoManifestFile(t, repo, "scratch.txt", "untracked\n")

		got, err := buildRepoManifest(context.Background(), repo)
		if err != nil {
			t.Fatalf("buildRepoManifest() error = %v", err)
		}
		want := map[string][][2]int{
			"README.md":      {{1, 3}},
			"pkg/example.go": {{1, 4}},
		}
		if !reflect.DeepEqual(got.Shown, want) {
			t.Fatalf("manifest shown = %#v, want %#v", got.Shown, want)
		}
	})

	t.Run("empty repository yields an empty manifest", func(t *testing.T) {
		repo := t.TempDir()
		runGit(t, repo, "init", "-b", "main")

		got, err := buildRepoManifest(context.Background(), repo)
		if err != nil {
			t.Fatalf("buildRepoManifest() error = %v", err)
		}
		if len(got.Shown) != 0 {
			t.Fatalf("manifest shown = %#v, want empty", got.Shown)
		}
	})

	t.Run("invalid worktree fails before spawning auditors", func(t *testing.T) {
		nonRepo := t.TempDir()
		if _, err := buildRepoManifest(context.Background(), nonRepo); err == nil {
			t.Fatal("buildRepoManifest() error = nil, want non-repository error")
		}

		spawner := &recordingReviewSpawner{
			stdout: structuredReviewOutput(t, ReviewReport{Reviewer: "auditor", Verdict: VerdictApproved}),
		}
		s := NewSpawner(spawner)

		result := waitResult(t, s.Audit(context.Background(), AuditOpts{
			Worktree: nonRepo,
		}))

		if result.Verdict != VerdictFailed || result.Err == nil {
			t.Fatalf("Audit result = %+v, want failed verdict with manifest error", result)
		}
		if calls := spawner.getCalls(); len(calls) != 0 {
			t.Fatalf("audit spawn calls = %d, want 0 after manifest failure", len(calls))
		}
	})

	t.Run("context cancellation remains distinguishable", func(t *testing.T) {
		repo := t.TempDir()
		runGit(t, repo, "init", "-b", "main")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		spawner := &recordingReviewSpawner{
			stdout: structuredReviewOutput(t, ReviewReport{Reviewer: "auditor", Verdict: VerdictApproved}),
		}
		result := waitResult(t, NewSpawner(spawner).Audit(ctx, AuditOpts{Worktree: repo}))

		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("Audit error = %v, want context.Canceled", result.Err)
		}
		if calls := spawner.getCalls(); len(calls) != 0 {
			t.Fatalf("audit spawn calls = %d, want 0 after manifest cancellation", len(calls))
		}
	})
}

func writeRepoManifestFile(t *testing.T, repo, name, body string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
