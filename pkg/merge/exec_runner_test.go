package merge_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/merge"
)

func TestExecGitRunnerRun_NormalizesGitEnvToRequestedDir(t *testing.T) {
	mainRepo := initExecGitRunnerRepo(t, "main")
	assignedRepo := initExecGitRunnerRepo(t, "assigned")

	if err := os.WriteFile(filepath.Join(mainRepo, "main-only.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatalf("write main-only: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assignedRepo, "assigned-only.txt"), []byte("assigned\n"), 0o644); err != nil {
		t.Fatalf("write assigned-only: %v", err)
	}

	t.Setenv("PWD", mainRepo)
	t.Setenv("GIT_DIR", filepath.Join(mainRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", mainRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(mainRepo, ".git", "index"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(mainRepo, ".git"))

	stdout, stderr, err := (&merge.ExecGitRunner{}).Run(context.Background(), assignedRepo, "status", "--short")
	if err != nil {
		t.Fatalf("git status: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "assigned-only.txt") {
		t.Fatalf("expected assigned repo status, got stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if strings.Contains(stdout, "main-only.txt") {
		t.Fatalf("git runner used poisoned main repo env, got stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func initExecGitRunnerRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runExecGit(t, dir, "init", "-b", "main")
	runExecGit(t, dir, "config", "user.email", "test@example.com")
	runExecGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runExecGit(t, dir, "add", ".")
	runExecGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runExecGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	stdout, stderr, err := (&merge.ExecGitRunner{}).Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
	}
}
