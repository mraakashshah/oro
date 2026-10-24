package factory //nolint:testpackage // Exercises the private Git boundary with real repositories.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateIsolation(t *testing.T) {
	t.Run("detached candidate and independent checkouts", gitTestCandidateCheckouts)
	t.Run("worker commits rejected", gitTestWorkerCommitRejected)
	t.Run("checker mutations rejected", gitTestCheckerMutations)
	t.Run("target validation", gitTestTargetValidation)
	t.Run("environment and hooks isolated", gitTestEnvironmentIsolation)
}

func gitTestCandidateCheckouts(t *testing.T) {
	t.Helper()
	repo, base := gitTestRepository(t)
	ctx := context.Background()
	git := gitRepository{path: repo}
	if got, err := git.target(ctx, "delivery"); err != nil || got != base {
		t.Fatalf("target = %q, %v; want %s", got, err, base)
	}
	worker := filepath.Join(t.TempDir(), "worker")
	if err := git.checkout(ctx, worker, base); err != nil {
		t.Fatal(err)
	}
	if got := gitTestRun(t, worker, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Fatalf("candidate must be detached, got %q", got)
	}
	if err := git.checkout(ctx, worker, base); err == nil {
		t.Fatal("reused an existing worktree")
	}
	if got, err := git.commit(ctx, worker, base); err != nil || got != base {
		t.Fatalf("no-change commit = %q, %v; want base", got, err)
	}
	gitTestWrite(t, filepath.Join(worker, "source.txt"), "repaired source\n", 0o600)
	gitTestWrite(t, filepath.Join(worker, "ignored-output"), "misleading success\n", 0o600)
	candidate, err := git.commit(ctx, worker, base)
	if err != nil || candidate == base {
		t.Fatalf("candidate = %q, %v; want new core commit", candidate, err)
	}
	if parent := gitTestRun(t, repo, "rev-parse", candidate+"^"); parent != base {
		t.Fatalf("candidate parent = %s, want %s", parent, base)
	}
	for _, stage := range []string{"acceptance", "review"} {
		checkout := filepath.Join(t.TempDir(), stage)
		if err := git.checkout(ctx, checkout, candidate); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(checkout, "ignored-output")); !os.IsNotExist(err) {
			t.Fatalf("worker output reached %s checkout: %v", stage, err)
		}
		gitTestWrite(t, filepath.Join(checkout, "checker-output"), "checker diagnostic\n", 0o600)
		if err := git.pristine(ctx, checkout, candidate); err != nil {
			t.Fatalf("untracked checker output changed candidate identity: %v", err)
		}
	}
	if got := gitTestRun(t, repo, "rev-parse", "delivery"); got != base {
		t.Fatalf("candidate creation advanced target to %s", got)
	}
	if got := gitTestRun(t, repo, "status", "--porcelain"); got != "" {
		t.Fatalf("candidate creation changed primary checkout: %s", got)
	}
}

func gitTestWorkerCommitRejected(t *testing.T) {
	t.Helper()
	repo, base := gitTestRepository(t)
	git := gitRepository{path: repo}
	worker := filepath.Join(t.TempDir(), "worker")
	if err := git.checkout(context.Background(), worker, base); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, worker, "commit", "--allow-empty", "-m", "worker-created commit")
	if _, err := git.commit(context.Background(), worker, base); err == nil {
		t.Fatal("accepted a worker-created commit")
	}
}

func gitTestCheckerMutations(t *testing.T) {
	t.Helper()
	for _, mutation := range []string{"tracked", "index", "head"} {
		t.Run(mutation, func(t *testing.T) {
			repo, base := gitTestRepository(t)
			git := gitRepository{path: repo}
			checker := filepath.Join(t.TempDir(), "checker")
			if err := git.checkout(context.Background(), checker, base); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "tracked":
				gitTestWrite(t, filepath.Join(checker, "source.txt"), "changed\n", 0o600)
			case "index":
				gitTestWrite(t, filepath.Join(checker, "source.txt"), "changed\n", 0o600)
				gitTestRun(t, checker, "add", "source.txt")
				gitTestWrite(t, filepath.Join(checker, "source.txt"), "original\n", 0o600)
			case "head":
				gitTestRun(t, checker, "commit", "--allow-empty", "-m", "checker commit")
			}
			if err := git.pristine(context.Background(), checker, base); err == nil {
				t.Fatalf("accepted checker %s mutation", mutation)
			}
		})
	}
}

func gitTestTargetValidation(t *testing.T) {
	t.Helper()
	repo, _ := gitTestRepository(t)
	git := gitRepository{path: repo}
	for _, branch := range []string{"main", "missing", "../delivery", "--help", "delivery^{commit}", ""} {
		if _, err := git.target(context.Background(), branch); err == nil {
			t.Errorf("accepted unavailable or unsafe target %q", branch)
		}
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitTestRun(t, repo, "worktree", "add", linked, "delivery")
	if _, err := git.target(context.Background(), "delivery"); err == nil {
		t.Fatal("accepted target checked out in another linked worktree")
	}
	nonGit := gitRepository{path: t.TempDir()}
	if _, err := nonGit.target(context.Background(), "delivery"); err == nil {
		t.Fatal("accepted a non-repository")
	}
}

func gitTestEnvironmentIsolation(t *testing.T) {
	t.Helper()
	repo, base := gitTestRepository(t)
	git := gitRepository{path: repo}
	hooks := t.TempDir()
	gitTestWrite(t, filepath.Join(hooks, "pre-commit"), "#!/bin/sh\nexit 71\n", 0o700)
	gitTestRun(t, repo, "config", "core.hooksPath", hooks)
	gitTestRun(t, repo, "config", "commit.gpgSign", "true")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "wrong-repository"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "wrong-index"))
	t.Setenv("GIT_AUTHOR_NAME", "Worker Impersonator")
	t.Setenv("GIT_COMMITTER_EMAIL", "worker@example.invalid")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", hooks)
	t.Setenv("ORO_PROJECT", "unrelated-project")
	worker := filepath.Join(t.TempDir(), "worker")
	if err := git.checkout(context.Background(), worker, base); err != nil {
		t.Fatal(err)
	}
	gitTestWrite(t, filepath.Join(worker, "source.txt"), "changed\n", 0o600)
	candidate, err := git.commit(context.Background(), worker, base)
	if err != nil {
		t.Fatal(err)
	}
	if identity := gitTestRun(t, repo, "show", "-s", "--format=%an|%ae|%cn|%ce", candidate); identity != "Oro Factory|factory@oro.invalid|Oro Factory|factory@oro.invalid" {
		t.Fatalf("commit identity = %q", identity)
	}
}

func gitTestRepository(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitTestRun(t, repo, "init", "-b", "main")
	gitTestWrite(t, filepath.Join(repo, ".gitignore"), "ignored-output\n", 0o600)
	gitTestWrite(t, filepath.Join(repo, "source.txt"), "original\n", 0o600)
	gitTestRun(t, repo, "add", ".")
	gitTestRun(t, repo, "commit", "-m", "initial source")
	gitTestRun(t, repo, "branch", "delivery")
	return repo, gitTestRun(t, repo, "rev-parse", "HEAD")
}

func gitTestRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgSign=false"}, args...)
	cmd := exec.Command("git", argv...)
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "ORO_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitTestWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
