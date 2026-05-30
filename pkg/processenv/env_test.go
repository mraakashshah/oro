package processenv_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"oro/pkg/processenv"
)

func TestForWorkdirNormalizesPWDAndStripsGitEnv(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"PWD=/wrong/root",
		"GIT_DIR=/wrong/root/.git",
		"GIT_WORK_TREE=/wrong/root",
		"GIT_COMMON_DIR=/wrong/root/.git",
		"GIT_INDEX_FILE=/wrong/index",
		"GIT_PREFIX=cmd/oro/",
		"CUSTOM=1",
	}

	got := processenv.ForWorkdir(env, "/assigned/worktree")
	wantPresent := map[string]bool{
		"PATH=/bin":              false,
		"PWD=/assigned/worktree": false,
		"CUSTOM=1":               false,
	}
	for _, entry := range got {
		if _, ok := wantPresent[entry]; ok {
			wantPresent[entry] = true
		}
		if entry == "GIT_DIR=/wrong/root/.git" ||
			entry == "GIT_WORK_TREE=/wrong/root" ||
			entry == "GIT_COMMON_DIR=/wrong/root/.git" ||
			entry == "GIT_INDEX_FILE=/wrong/index" ||
			entry == "GIT_PREFIX=cmd/oro/" {
			t.Fatalf("git override env leaked: %q in %v", entry, got)
		}
	}
	for entry, found := range wantPresent {
		if !found {
			t.Fatalf("missing %q in %v", entry, got)
		}
	}
}

func TestForWorkdirAddsPWDWhenMissing(t *testing.T) {
	got := processenv.ForWorkdir([]string{"PATH=/bin"}, "/assigned/worktree")
	if envMap(got)["PWD"] != "/assigned/worktree" {
		t.Fatalf("ForWorkdir() = %v, want PWD appended", got)
	}
}

func TestForWorkdirDisablesInteractiveGitEditors(t *testing.T) {
	got := processenv.ForWorkdir([]string{
		"PATH=/bin",
		"GIT_EDITOR=subl -w",
		"GIT_SEQUENCE_EDITOR=code --wait",
		"VISUAL=vim",
		"EDITOR=nano",
		"GIT_MERGE_AUTOEDIT=yes",
	}, "/assigned/worktree")
	env := envMap(got)

	for _, key := range []string{"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "VISUAL", "EDITOR"} {
		if env[key] != "true" {
			t.Fatalf("%s = %q, want true in %v", key, env[key], got)
		}
	}
	if env["GIT_MERGE_AUTOEDIT"] != "no" {
		t.Fatalf("GIT_MERGE_AUTOEDIT = %q, want no in %v", env["GIT_MERGE_AUTOEDIT"], got)
	}
}

func TestForWorkdirIsolatesCacheAndTempOutsideWorktree(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	got := processenv.ForWorkdir([]string{
		"PATH=/bin",
		"PWD=/wrong/root",
		"GOCACHE=" + filepath.Join(worktree, ".gocache"),
		"GOLANGCI_LINT_CACHE=" + filepath.Join(worktree, ".cache", "golangci-lint"),
		"UV_CACHE_DIR=" + filepath.Join(worktree, ".cache", "uv"),
		"TMPDIR=" + filepath.Join(worktree, ".tmp"),
		"TMP=" + filepath.Join(worktree, ".tmp"),
		"TEMP=" + filepath.Join(worktree, ".tmp"),
		"GOMODCACHE=" + filepath.Join(worktree, ".gomodcache"),
	}, worktree)
	env := envMap(got)

	for _, key := range []string{"GOCACHE", "GOLANGCI_LINT_CACHE", "UV_CACHE_DIR", "TMPDIR", "TMP", "TEMP", "GOMODCACHE"} {
		value := env[key]
		if value == "" {
			t.Fatalf("%s not set in %v", key, got)
		}
		if pathInside(value, worktree) {
			t.Fatalf("%s=%q still points inside worktree %q", key, value, worktree)
		}
	}
	if _, err := os.Stat(env["TMPDIR"]); err != nil {
		t.Fatalf("TMPDIR %q was not created: %v", env["TMPDIR"], err)
	}
}

func TestForWorkdirUsesShortTmpRootOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin has a short Unix socket path limit")
	}

	longTmpRoot := filepath.Join(t.TempDir(), strings.Repeat("very-long-temp-root-", 5))
	t.Setenv("TMPDIR", longTmpRoot)

	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	got := processenv.ForWorkdir([]string{"PATH=/bin"}, worktree)
	tmpDir := envMap(got)["TMPDIR"]
	wantPrefix := filepath.Join("/tmp", "oro-subprocess") + string(os.PathSeparator)
	if !strings.HasPrefix(tmpDir, wantPrefix) {
		t.Fatalf("TMPDIR = %q, want short /tmp oro subprocess root prefix %q", tmpDir, wantPrefix)
	}

	sampleSocket := filepath.Join(tmpDir, "TestOversizeMessage", "001", "oro-test.sock")
	if len(sampleSocket) >= 104 {
		t.Fatalf("sample Unix socket path is too long for darwin: len(%q) = %d", sampleSocket, len(sampleSocket))
	}
}

func TestForWorkdirPreservesExternalGOMODCACHE(t *testing.T) {
	worktree := filepath.Join(t.TempDir(), "worktree")
	externalModCache := filepath.Join(t.TempDir(), "gomodcache")

	got := processenv.ForWorkdir([]string{
		"PATH=/bin",
		"GOMODCACHE=" + externalModCache,
	}, worktree)
	env := envMap(got)
	if env["GOMODCACHE"] != externalModCache {
		t.Fatalf("GOMODCACHE = %q, want preserved external value %q", env["GOMODCACHE"], externalModCache)
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func pathInside(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}
