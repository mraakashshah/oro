package ops //nolint:testpackage // buildRepoManifest is an internal review helper

import (
	"context"
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

		got := buildRepoManifest(context.Background(), repo)
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

		got := buildRepoManifest(context.Background(), repo)
		if len(got.Shown) != 0 {
			t.Fatalf("manifest shown = %#v, want empty", got.Shown)
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
