package ops //nolint:testpackage // selectPersonas is an internal review orchestration helper

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"oro/pkg/agentmodel"
)

func TestSelectPersonas(t *testing.T) {
	t.Run("normal diff selects persona team", func(t *testing.T) {
		worktree := testReviewRepo(t)
		writeFile(t, filepath.Join(worktree, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"changed\" }\n")

		personas := selectPersonas(ReviewOpts{Worktree: worktree, BaseBranch: "main"})
		wantIDs := []string{"correctness", "security", "adversarial", "design", "test", "architecture"}
		if len(personas) != len(wantIDs) {
			t.Fatalf("selectPersonas normal diff returned %d personas, want %d: %#v", len(personas), len(wantIDs), personas)
		}
		for i, wantID := range wantIDs {
			p := personas[i]
			if p.ID != wantID {
				t.Fatalf("persona[%d].ID = %q, want %q", i, p.ID, wantID)
			}
			if p.Role != "ops_review_"+wantID {
				t.Fatalf("persona[%d].Role = %q, want agentmodel role %q", i, p.Role, "ops_review_"+wantID)
			}
			if p.Fragment == "" {
				t.Fatalf("persona[%d].Fragment is empty", i)
			}
			runtime, model, _ := agentmodel.ResolveForRole(p.Role)
			if runtime == "" || model == "" {
				t.Fatalf("persona[%d].Role %q did not resolve through agentmodel: runtime=%q model=%q", i, p.Role, runtime, model)
			}
		}
	})

	t.Run("docs only diff selects no personas", func(t *testing.T) {
		worktree := testReviewRepo(t)
		writeFile(t, filepath.Join(worktree, "docs", "plan.md"), "# changed\n")

		if personas := selectPersonas(ReviewOpts{Worktree: worktree, BaseBranch: "main"}); len(personas) != 0 {
			t.Fatalf("selectPersonas docs-only diff returned %#v, want empty slice", personas)
		}
	})

	t.Run("trivial empty diff selects no personas", func(t *testing.T) {
		worktree := testReviewRepo(t)

		if personas := selectPersonas(ReviewOpts{Worktree: worktree, BaseBranch: "main"}); len(personas) != 0 {
			t.Fatalf("selectPersonas empty diff returned %#v, want empty slice", personas)
		}
	})
}

func testReviewRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pkg", "worker.go"), "package pkg\n\nfunc Worker() string { return \"ok\" }\n")
	writeFile(t, filepath.Join(dir, "docs", "plan.md"), "# plan\n")
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test User")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...) //nolint:gosec // fixed test helper command
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
