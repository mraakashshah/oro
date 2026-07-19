package storage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/storage"
)

func TestResolveCacheEnvironment(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktree, 0o750); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	external := filepath.Join(t.TempDir(), "external-cache")
	sharedRoot := filepath.Join(t.TempDir(), "shared")
	linked := filepath.Join(t.TempDir(), "linked-worktree")
	if err := os.Symlink(worktree, linked); err != nil {
		t.Fatalf("symlink worktree: %v", err)
	}

	policy := storage.StoragePolicy{
		Providers: []storage.CacheProvider{
			{
				ID:          "user-cache",
				Variables:   []string{"USER_CACHE"},
				DefaultPath: func() string { return filepath.Join(sharedRoot, "user") },
				Scope:       storage.UserScope,
				Concurrency: storage.Concurrent,
				Ownership:   storage.ToolNative,
			},
			{
				ID:          "project-cache",
				Variables:   []string{"PROJECT_CACHE"},
				DefaultPath: func() string { return filepath.Join(sharedRoot, "project") },
				Scope:       storage.ProjectScope,
				Concurrency: storage.Concurrent,
				Ownership:   storage.ToolNative,
			},
			{
				ID:          "repository-cache",
				Variables:   []string{"REPOSITORY_CACHE"},
				DefaultPath: func() string { return filepath.Join(sharedRoot, "repository") },
				Scope:       storage.RepositoryScope,
				Concurrency: storage.Concurrent,
				Ownership:   storage.ToolNative,
			},
		},
		ProjectID:      "project-a",
		RepositoryRoot: filepath.Join(t.TempDir(), "repository"),
	}

	resolved, err := storage.ResolveCacheEnv([]string{
		"USER_CACHE=" + external,
		"PROJECT_CACHE=" + filepath.Join(worktree, ".cache", "project"),
		"REPOSITORY_CACHE=" + filepath.Join(linked, ".cache", "repository"),
		"UNKNOWN_CACHE=" + filepath.Join(worktree, ".cache", "unknown"),
		"UNKNOWN_EXTERNAL=" + external,
		"KEEP=value",
	}, worktree, policy)
	if err != nil {
		t.Fatalf("ResolveCacheEnv() error = %v", err)
	}

	env := cacheEnvMap(resolved.Env)
	if got := env["USER_CACHE"]; got != external {
		t.Errorf("USER_CACHE = %q, want preserved external path %q", got, external)
	}
	if got := env["PROJECT_CACHE"]; !strings.HasPrefix(got, filepath.Join(sharedRoot, "project", "project")+string(os.PathSeparator)) {
		t.Errorf("PROJECT_CACHE = %q, want project-scoped path below %q", got, filepath.Join(sharedRoot, "project", "project"))
	}
	if got := env["REPOSITORY_CACHE"]; !strings.HasPrefix(got, filepath.Join(sharedRoot, "repository", "repository")+string(os.PathSeparator)) {
		t.Errorf("REPOSITORY_CACHE = %q, want repository-scoped path below %q", got, filepath.Join(sharedRoot, "repository", "repository"))
	}
	if got, want := env["UNKNOWN_CACHE"], filepath.Join(worktree, ".cache", "unknown"); got != want {
		t.Errorf("UNKNOWN_CACHE = %q, want unchanged unknown path %q", got, want)
	}
	if got := env["UNKNOWN_EXTERNAL"]; got != external {
		t.Errorf("UNKNOWN_EXTERNAL = %q, want preserved external path %q", got, external)
	}
	if got := env["KEEP"]; got != "value" {
		t.Errorf("KEEP = %q, want value", got)
	}
	if len(resolved.Findings) != 1 {
		t.Fatalf("Findings = %#v, want one unknown internal finding", resolved.Findings)
	}
	if finding := resolved.Findings[0]; finding.Variable != "UNKNOWN_CACHE" || finding.Path != filepath.Join(worktree, ".cache", "unknown") {
		t.Errorf("Finding = %#v, want UNKNOWN_CACHE at worktree path", finding)
	}

	missing, err := storage.ResolveCacheEnv(nil, worktree, policy)
	if err != nil {
		t.Fatalf("ResolveCacheEnv() missing vars error = %v", err)
	}
	missingEnv := cacheEnvMap(missing.Env)
	if got, want := missingEnv["USER_CACHE"], filepath.Join(sharedRoot, "user"); got != want {
		t.Errorf("default USER_CACHE = %q, want %q", got, want)
	}
	if got := missingEnv["PROJECT_CACHE"]; !strings.HasPrefix(got, filepath.Join(sharedRoot, "project", "project")+string(os.PathSeparator)) {
		t.Errorf("default PROJECT_CACHE = %q, want project-scoped path below %q", got, filepath.Join(sharedRoot, "project", "project"))
	}
	if got := missingEnv["REPOSITORY_CACHE"]; !strings.HasPrefix(got, filepath.Join(sharedRoot, "repository", "repository")+string(os.PathSeparator)) {
		t.Errorf("default REPOSITORY_CACHE = %q, want repository-scoped path below %q", got, filepath.Join(sharedRoot, "repository", "repository"))
	}
}

func cacheEnvMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}
