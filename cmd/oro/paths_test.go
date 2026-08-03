package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

func TestResolveStoragePaths(t *testing.T) {
	userRoot := t.TempDir()
	oroHome := filepath.Join(userRoot, ".oro")
	if err := os.MkdirAll(oroHome, 0o750); err != nil {
		t.Fatalf("mkdir oro home: %v", err)
	}

	paths, err := ResolveStoragePaths(oroHome)
	if err != nil {
		t.Fatalf("ResolveStoragePaths() error: %v", err)
	}
	canonicalOroHome := paths.OroHome

	for name, path := range map[string]string{
		"catalog":  paths.CatalogPath,
		"lock":     paths.LockPath,
		"evidence": paths.EvidenceRoot,
		"cache":    paths.CacheRoot,
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%s path = %q, want absolute", name, path)
		}
		if rel, err := filepath.Rel(canonicalOroHome, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("%s path = %q, want beneath %q", name, path, canonicalOroHome)
		}
	}

	if paths.CatalogPath != filepath.Join(canonicalOroHome, "storage", "catalog.db") {
		t.Errorf("CatalogPath = %q", paths.CatalogPath)
	}
	if paths.LockPath != filepath.Join(canonicalOroHome, "storage", "maintenance.lock") {
		t.Errorf("LockPath = %q", paths.LockPath)
	}
	if paths.EvidenceRoot != filepath.Join(canonicalOroHome, "storage", "evidence") {
		t.Errorf("EvidenceRoot = %q", paths.EvidenceRoot)
	}
	if paths.CacheRoot != filepath.Join(canonicalOroHome, "cache") {
		t.Errorf("CacheRoot = %q", paths.CacheRoot)
	}

	for _, test := range []struct {
		name string
		root string
		want error
	}{
		{name: "relative", root: ".oro", want: ErrInvalidPath},
		{name: "worktree", root: filepath.Join(userRoot, "repo", ".oro"), want: ErrUnsafeStorageRoot},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "worktree" {
				if err := os.MkdirAll(filepath.Join(userRoot, "repo", ".git"), 0o750); err != nil {
					t.Fatalf("mkdir worktree marker: %v", err)
				}
				if err := os.MkdirAll(test.root, 0o750); err != nil {
					t.Fatalf("mkdir worktree storage root: %v", err)
				}
			}
			_, err := ResolveStoragePaths(test.root)
			if !errors.Is(err, test.want) {
				t.Fatalf("ResolveStoragePaths(%q) error = %v, want %v", test.root, err, test.want)
			}
		})
	}

	escape := filepath.Join(userRoot, "repo", "escaped-oro")
	if err := os.MkdirAll(filepath.Join(userRoot, "repo", ".git"), 0o750); err != nil {
		t.Fatalf("mkdir symlink worktree marker: %v", err)
	}
	if err := os.MkdirAll(escape, 0o750); err != nil {
		t.Fatalf("mkdir symlink escape target: %v", err)
	}
	if err := os.Symlink(escape, filepath.Join(userRoot, "linked-oro")); err != nil {
		t.Fatalf("symlink storage root: %v", err)
	}
	_, err = ResolveStoragePaths(filepath.Join(userRoot, "linked-oro"))
	if !errors.Is(err, ErrUnsafeStorageRoot) {
		t.Fatalf("ResolveStoragePaths(symlink escape) error = %v, want %v", err, ErrUnsafeStorageRoot)
	}
}

func TestResolvePaths_Standard(t *testing.T) {
	repoRoot := t.TempDir()

	// No .oro/config.yaml → defaults to standard mode
	paths, err := ResolvePaths(repoRoot)
	if err != nil {
		t.Fatalf("ResolveProjectPaths() error: %v", err)
	}

	if paths.Mode != "standard" {
		t.Errorf("Mode = %q, want %q", paths.Mode, "standard")
	}
	if paths.BeadsDir != filepath.Join(repoRoot, protocol.OroDir, "tasks") {
		t.Errorf("BeadsDir = %q, want %q", paths.BeadsDir, filepath.Join(repoRoot, protocol.OroDir, "tasks"))
	}
	if paths.WorktreesDir != filepath.Join(repoRoot, ".worktrees") {
		t.Errorf("WorktreesDir = %q, want %q", paths.WorktreesDir, filepath.Join(repoRoot, ".worktrees"))
	}
	if paths.OroDocsDir != filepath.Join(repoRoot, "docs") {
		t.Errorf("OroDocsDir = %q, want %q", paths.OroDocsDir, filepath.Join(repoRoot, "docs"))
	}
}

func TestResolvePaths_Stealth(t *testing.T) {
	repoRoot := t.TempDir()
	tmpOroHome := t.TempDir()
	t.Setenv("ORO_HOME", tmpOroHome)

	// Compute expected hash: SHA-256 of resolved repoRoot, truncated to 16 hex chars.
	resolved, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	sum := sha256.Sum256([]byte(resolved))
	hash := fmt.Sprintf("%x", sum[:8])

	// Create stealth config at ~/.oro/projects/s-<hash>/config.yaml
	stealthDir := filepath.Join(tmpOroHome, "projects", "s-"+hash)
	if err := os.MkdirAll(stealthDir, 0o750); err != nil {
		t.Fatalf("mkdir stealth dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stealthDir, "config.yaml"), []byte("mode: stealth\n"), 0o600); err != nil {
		t.Fatalf("write stealth config: %v", err)
	}

	paths, err := ResolvePaths(repoRoot)
	if err != nil {
		t.Fatalf("ResolveDaemonPaths() error: %v", err)
	}

	if paths.Mode != "stealth" {
		t.Errorf("Mode = %q, want %q", paths.Mode, "stealth")
	}
	// All data dirs must live under stealthDir.
	if paths.BeadsDir != filepath.Join(stealthDir, "tasks") {
		t.Errorf("BeadsDir = %q, want %q", paths.BeadsDir, filepath.Join(stealthDir, "tasks"))
	}
	if paths.WorktreesDir != filepath.Join(stealthDir, "worktrees") {
		t.Errorf("WorktreesDir = %q, want %q", paths.WorktreesDir, filepath.Join(stealthDir, "worktrees"))
	}
	if paths.OroDocsDir != filepath.Join(stealthDir, "docs") {
		t.Errorf("OroDocsDir = %q, want %q", paths.OroDocsDir, filepath.Join(stealthDir, "docs"))
	}
	if paths.OroProjectDir != stealthDir {
		t.Errorf("OroProjectDir = %q, want %q", paths.OroProjectDir, stealthDir)
	}
}

func TestResolvePaths_Defaults(t *testing.T) {
	// Clear all env overrides.
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}

	paths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("ResolveDaemonPaths() error: %v", err)
	}

	// All default paths should be under ~/.oro.
	expectedBase := filepath.Join(home, protocol.OroDir)

	if paths.OroHome != expectedBase {
		t.Errorf("OroHome = %q, want %q", paths.OroHome, expectedBase)
	}
	if paths.PIDPath != filepath.Join(expectedBase, "oro.pid") {
		t.Errorf("PIDPath = %q, want %q", paths.PIDPath, filepath.Join(expectedBase, "oro.pid"))
	}
	if paths.SocketPath != filepath.Join(expectedBase, "oro.sock") {
		t.Errorf("SocketPath = %q, want %q", paths.SocketPath, filepath.Join(expectedBase, "oro.sock"))
	}
	if paths.StateDBPath != filepath.Join(expectedBase, "state.db") {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, filepath.Join(expectedBase, "state.db"))
	}
	if paths.ReviewEvidenceDir != filepath.Join(expectedBase, "review-evidence") {
		t.Errorf("ReviewEvidenceDir = %q, want %q", paths.ReviewEvidenceDir, filepath.Join(expectedBase, "review-evidence"))
	}
	if paths.CodeIndexDBPath != filepath.Join(expectedBase, "code_index.db") {
		t.Errorf("CodeIndexDBPath = %q, want %q", paths.CodeIndexDBPath, filepath.Join(expectedBase, "code_index.db"))
	}
}

func TestResolvePaths_EnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()

	// Set all env overrides to temp dir paths.
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_HOME", filepath.Join(tmpDir, "custom-oro"))
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "custom.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "custom.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "custom-state.db"))

	paths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("ResolveDaemonPaths() error: %v", err)
	}

	// Verify all env overrides are honored.
	if paths.OroHome != filepath.Join(tmpDir, "custom-oro") {
		t.Errorf("OroHome = %q, want %q", paths.OroHome, filepath.Join(tmpDir, "custom-oro"))
	}
	if paths.PIDPath != filepath.Join(tmpDir, "custom.pid") {
		t.Errorf("PIDPath = %q, want %q", paths.PIDPath, filepath.Join(tmpDir, "custom.pid"))
	}
	if paths.SocketPath != filepath.Join(tmpDir, "custom.sock") {
		t.Errorf("SocketPath = %q, want %q", paths.SocketPath, filepath.Join(tmpDir, "custom.sock"))
	}
	if paths.StateDBPath != filepath.Join(tmpDir, "custom-state.db") {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, filepath.Join(tmpDir, "custom-state.db"))
	}

	// CodeIndexDBPath respects ORO_HOME when set.
	if paths.CodeIndexDBPath != filepath.Join(tmpDir, "custom-oro", "code_index.db") {
		t.Errorf("CodeIndexDBPath = %q, want %q", paths.CodeIndexDBPath, filepath.Join(tmpDir, "custom-oro", "code_index.db"))
	}
}

func TestResolvePaths_PartialEnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}

	// Override only some paths.
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "custom.pid"))
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")

	paths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("ResolveDaemonPaths() error: %v", err)
	}

	expectedBase := filepath.Join(home, protocol.OroDir)

	// PIDPath is overridden.
	if paths.PIDPath != filepath.Join(tmpDir, "custom.pid") {
		t.Errorf("PIDPath = %q, want %q", paths.PIDPath, filepath.Join(tmpDir, "custom.pid"))
	}

	// Others use defaults.
	if paths.OroHome != expectedBase {
		t.Errorf("OroHome = %q, want %q", paths.OroHome, expectedBase)
	}
	if paths.SocketPath != filepath.Join(expectedBase, "oro.sock") {
		t.Errorf("SocketPath = %q, want %q", paths.SocketPath, filepath.Join(expectedBase, "oro.sock"))
	}
	if paths.StateDBPath != filepath.Join(expectedBase, "state.db") {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, filepath.Join(expectedBase, "state.db"))
	}
}

// --- ResolvePaths project-scoping tests ---

func TestResolvePaths_ProjectScopesAllPaths(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "foo")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")

	paths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("ResolveDaemonPaths() error: %v", err)
	}

	projDir := filepath.Join(tmpDir, "projects", "foo")

	// OroHome stays global (used for worker logs, hooks, etc.)
	if paths.OroHome != tmpDir {
		t.Errorf("OroHome = %q, want global %q", paths.OroHome, tmpDir)
	}

	// All state paths scoped to project dir
	if paths.PIDPath != filepath.Join(projDir, "oro.pid") {
		t.Errorf("PIDPath = %q, want %q", paths.PIDPath, filepath.Join(projDir, "oro.pid"))
	}
	if paths.SocketPath != filepath.Join(projDir, "oro.sock") {
		t.Errorf("SocketPath = %q, want %q", paths.SocketPath, filepath.Join(projDir, "oro.sock"))
	}
	if paths.StateDBPath != filepath.Join(projDir, "state.db") {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, filepath.Join(projDir, "state.db"))
	}
	if paths.CodeIndexDBPath != filepath.Join(projDir, "code_index.db") {
		t.Errorf("CodeIndexDBPath = %q, want %q", paths.CodeIndexDBPath, filepath.Join(projDir, "code_index.db"))
	}
}

func TestResolvePaths_EnvOverridesProjectScope(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "foo")
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "override.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "override.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "override.db"))

	paths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("ResolveDaemonPaths() error: %v", err)
	}

	// Explicit env vars override project scoping
	if paths.PIDPath != filepath.Join(tmpDir, "override.pid") {
		t.Errorf("PIDPath = %q, want env override %q", paths.PIDPath, filepath.Join(tmpDir, "override.pid"))
	}
	if paths.SocketPath != filepath.Join(tmpDir, "override.sock") {
		t.Errorf("SocketPath = %q, want env override %q", paths.SocketPath, filepath.Join(tmpDir, "override.sock"))
	}
	if paths.StateDBPath != filepath.Join(tmpDir, "override.db") {
		t.Errorf("StateDBPath = %q, want env override %q", paths.StateDBPath, filepath.Join(tmpDir, "override.db"))
	}
}

// ResolveProjectDBPaths is now an alias for ResolvePaths.
func TestResolveProjectDBPaths_WithEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "foo")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")

	paths, err := ResolveProjectDBPaths()
	if err != nil {
		t.Fatalf("ResolveProjectDBPaths() error: %v", err)
	}

	projDir := filepath.Join(tmpDir, "projects", "foo")

	if paths.StateDBPath != filepath.Join(projDir, "state.db") {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, filepath.Join(projDir, "state.db"))
	}
	if paths.CodeIndexDBPath != filepath.Join(projDir, "code_index.db") {
		t.Errorf("CodeIndexDBPath = %q, want %q", paths.CodeIndexDBPath, filepath.Join(projDir, "code_index.db"))
	}
	// PID/Socket are now also project-scoped
	if paths.PIDPath != filepath.Join(projDir, "oro.pid") {
		t.Errorf("PIDPath = %q, want project-scoped %q", paths.PIDPath, filepath.Join(projDir, "oro.pid"))
	}
	if paths.SocketPath != filepath.Join(projDir, "oro.sock") {
		t.Errorf("SocketPath = %q, want project-scoped %q", paths.SocketPath, filepath.Join(projDir, "oro.sock"))
	}
}

func TestResolveProjectDBPaths_WithConfigYAML(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "")

	// Create .oro/config.yaml in a temporary "project root"
	projDir := t.TempDir()
	oroDir := filepath.Join(projDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: bar\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Change to project root so readProjectName finds the config
	origDir, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	paths, err := ResolveProjectDBPaths()
	if err != nil {
		t.Fatalf("ResolveProjectDBPaths() error: %v", err)
	}

	wantState := filepath.Join(tmpDir, "projects", "bar", "state.db")
	if paths.StateDBPath != wantState {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, wantState)
	}
}

func TestResolveProjectDBPaths_EnvPriorityOverConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "env-wins")

	// Create .oro/config.yaml with different project name
	projDir := t.TempDir()
	oroDir := filepath.Join(projDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: config-loses\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	paths, err := ResolveProjectDBPaths()
	if err != nil {
		t.Fatalf("ResolveProjectDBPaths() error: %v", err)
	}

	// ORO_PROJECT should win over config.yaml
	wantState := filepath.Join(tmpDir, "projects", "env-wins", "state.db")
	if paths.StateDBPath != wantState {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, wantState)
	}
}

func TestResolveProjectDBPaths_FallbackToGlobal(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_DB_PATH", "")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")

	// No config.yaml in CWD — should fall back to global
	noConfigDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(noConfigDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	paths, err := ResolveProjectDBPaths()
	if err != nil {
		t.Fatalf("ResolveProjectDBPaths() error: %v", err)
	}

	// Should fall back to global ~/.oro/state.db
	wantState := filepath.Join(tmpDir, "state.db")
	if paths.StateDBPath != wantState {
		t.Errorf("StateDBPath = %q, want global %q", paths.StateDBPath, wantState)
	}
}

func TestReadProjectName_EnvFirst(t *testing.T) {
	t.Setenv("ORO_PROJECT", "myproject")
	if got := readProjectNameCWD(); got != "myproject" {
		t.Errorf("readProjectNameCWD() = %q, want %q", got, "myproject")
	}
}

func TestEnsureRuntimeProjectEnvDoesNotExportResolvedProject(t *testing.T) {
	repoRoot := t.TempDir()
	oroHome := t.TempDir()
	project := "isolated-project"
	if err := os.MkdirAll(filepath.Join(repoRoot, ".oro"), 0o750); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".oro", "config.yaml"), []byte("project: "+project+"\n"), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "")

	runtimeEnv, err := ensureRuntimeProjectEnv(repoRoot)
	if err != nil {
		t.Fatalf("ensureRuntimeProjectEnv: %v", err)
	}
	if runtimeEnv.Project != project {
		t.Fatalf("resolved project = %q, want %q", runtimeEnv.Project, project)
	}
	if got := os.Getenv("ORO_PROJECT"); got != "" {
		t.Fatalf("ensureRuntimeProjectEnv leaked ORO_PROJECT=%q", got)
	}
}

func TestWithRuntimeProjectEnvRestoresResolvedProject(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".oro"), 0o750); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".oro", "config.yaml"), []byte("project: scoped-project\n"), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Setenv("ORO_HOME", t.TempDir())
	t.Setenv("ORO_PROJECT", "")

	if err := withRuntimeProjectEnv(repoRoot, func(runtimeEnv runtimeProjectEnv) error {
		if runtimeEnv.Project != "scoped-project" {
			t.Errorf("resolved project = %q, want scoped-project", runtimeEnv.Project)
		}
		if got := os.Getenv("ORO_PROJECT"); got != "scoped-project" {
			t.Errorf("scoped ORO_PROJECT = %q, want scoped-project", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("withRuntimeProjectEnv: %v", err)
	}
	if got := os.Getenv("ORO_PROJECT"); got != "" {
		t.Fatalf("withRuntimeProjectEnv leaked ORO_PROJECT=%q", got)
	}
}

func TestReadProjectName_EmptyFallback(t *testing.T) {
	t.Setenv("ORO_PROJECT", "")
	noConfigDir := t.TempDir()
	t.Setenv("ORO_HOME", t.TempDir())

	name, mode, err := readProjectName(noConfigDir)
	if err != nil {
		t.Fatalf("readProjectName() error: %v", err)
	}
	if name != "" {
		t.Errorf("readProjectName() name = %q, want empty string", name)
	}
	if mode != "standard" {
		t.Errorf("readProjectName() mode = %q, want %q", mode, "standard")
	}
}

func TestReadProjectName_StealthFallback(t *testing.T) {
	repoRoot := t.TempDir()
	tmpOroHome := t.TempDir()
	t.Setenv("ORO_HOME", tmpOroHome)
	t.Setenv("ORO_PROJECT", "")

	// Compute hash of repoRoot (resolving symlinks)
	resolved, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	hash := computePathHash(resolved)

	// Create stealth config at ~/.oro/projects/s-<hash>/config.yaml
	stealthDir := filepath.Join(tmpOroHome, "projects", "s-"+hash)
	if err := os.MkdirAll(stealthDir, 0o750); err != nil {
		t.Fatalf("mkdir stealth dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stealthDir, "config.yaml"), []byte("mode: stealth\n"), 0o600); err != nil {
		t.Fatalf("write stealth config: %v", err)
	}

	name, mode, err := readProjectName(repoRoot)
	if err != nil {
		t.Fatalf("readProjectName() error: %v", err)
	}
	if name != "s-"+hash {
		t.Errorf("name = %q, want %q", name, "s-"+hash)
	}
	if mode != "stealth" {
		t.Errorf("mode = %q, want %q", mode, "stealth")
	}
}

func TestReadProjectName_StealthFallback_StandardWins(t *testing.T) {
	repoRoot := t.TempDir()
	tmpOroHome := t.TempDir()
	t.Setenv("ORO_HOME", tmpOroHome)
	t.Setenv("ORO_PROJECT", "")

	// Create standard config at <repoRoot>/.oro/config.yaml
	oroDir := filepath.Join(repoRoot, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil {
		t.Fatalf("mkdir .oro: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: myproject\n"), 0o600); err != nil {
		t.Fatalf("write standard config: %v", err)
	}

	name, mode, err := readProjectName(repoRoot)
	if err != nil {
		t.Fatalf("readProjectName() error: %v", err)
	}
	if name != "myproject" {
		t.Errorf("name = %q, want %q", name, "myproject")
	}
	if mode != "standard" {
		t.Errorf("mode = %q, want %q", mode, "standard")
	}
}

func TestReadProjectName_StealthFallback_NeitherExists(t *testing.T) {
	repoRoot := t.TempDir()
	tmpOroHome := t.TempDir()
	t.Setenv("ORO_HOME", tmpOroHome)
	t.Setenv("ORO_PROJECT", "")

	name, mode, err := readProjectName(repoRoot)
	if err != nil {
		t.Fatalf("readProjectName() error: %v", err)
	}
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
	if mode != "standard" {
		t.Errorf("mode = %q, want %q", mode, "standard")
	}
}

func TestReadProjectName_StealthFallback_Symlink(t *testing.T) {
	// Create a real repo root directory.
	realRoot := t.TempDir()
	tmpOroHome := t.TempDir()
	t.Setenv("ORO_HOME", tmpOroHome)
	t.Setenv("ORO_PROJECT", "")

	// Create a symlink pointing to realRoot.
	symlinkRoot := filepath.Join(t.TempDir(), "linked-repo")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// Hash should be based on resolved (real) path, not the symlink.
	resolved, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	hash := computePathHash(resolved)

	// Create stealth config using the resolved hash.
	stealthDir := filepath.Join(tmpOroHome, "projects", "s-"+hash)
	if err := os.MkdirAll(stealthDir, 0o750); err != nil {
		t.Fatalf("mkdir stealth dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stealthDir, "config.yaml"), []byte("mode: stealth\n"), 0o600); err != nil {
		t.Fatalf("write stealth config: %v", err)
	}

	// Pass the symlink path — readProjectName should resolve it and find the stealth config.
	name, mode, err := readProjectName(symlinkRoot)
	if err != nil {
		t.Fatalf("readProjectName(symlink) error: %v", err)
	}
	if name != "s-"+hash {
		t.Errorf("name = %q, want %q", name, "s-"+hash)
	}
	if mode != "stealth" {
		t.Errorf("mode = %q, want %q", mode, "stealth")
	}
}

func TestResolvePaths_OroHomeOverride(t *testing.T) {
	tmpDir := t.TempDir()

	// ORO_HOME should affect the default base for other paths if they're not overridden.
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")

	paths, err := ResolveDaemonPaths()
	if err != nil {
		t.Fatalf("ResolveDaemonPaths() error: %v", err)
	}

	// All paths should use ORO_HOME as base.
	if paths.OroHome != tmpDir {
		t.Errorf("OroHome = %q, want %q", paths.OroHome, tmpDir)
	}
	if paths.PIDPath != filepath.Join(tmpDir, "oro.pid") {
		t.Errorf("PIDPath = %q, want %q", paths.PIDPath, filepath.Join(tmpDir, "oro.pid"))
	}
	if paths.SocketPath != filepath.Join(tmpDir, "oro.sock") {
		t.Errorf("SocketPath = %q, want %q", paths.SocketPath, filepath.Join(tmpDir, "oro.sock"))
	}
	if paths.StateDBPath != filepath.Join(tmpDir, "state.db") {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, filepath.Join(tmpDir, "state.db"))
	}
	if paths.CodeIndexDBPath != filepath.Join(tmpDir, "code_index.db") {
		t.Errorf("CodeIndexDBPath = %q, want %q", paths.CodeIndexDBPath, filepath.Join(tmpDir, "code_index.db"))
	}
}

// --- migrateGlobalDBs tests ---

func TestMigrateGlobalDBsToProject(t *testing.T) {
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)

	// Create global DBs
	globalStateDB := filepath.Join(oroHome, "state.db")
	globalCodeIndexDB := filepath.Join(oroHome, "code_index.db")
	if err := os.WriteFile(globalStateDB, []byte("global-state"), 0o600); err != nil {
		t.Fatalf("create global state.db: %v", err)
	}
	if err := os.WriteFile(globalCodeIndexDB, []byte("global-code"), 0o600); err != nil {
		t.Fatalf("create global code_index.db: %v", err)
	}

	projectName := "foo"
	projectDir := filepath.Join(oroHome, "projects", projectName)
	projStateDB := filepath.Join(projectDir, "state.db")
	projCodeIndexDB := filepath.Join(projectDir, "code_index.db")

	t.Run("MigrateStateDB", func(t *testing.T) {
		// Clean project dir between tests
		_ = os.RemoveAll(projectDir) //nolint:errcheck

		err := migrateGlobalDBs(projectName)
		if err != nil {
			t.Fatalf("migrateGlobalDBs(%q) error: %v", projectName, err)
		}

		// Check that state.db was copied
		projData, err := os.ReadFile(projStateDB) //nolint:gosec
		if err != nil {
			t.Fatalf("read project state.db: %v", err)
		}
		if string(projData) != "global-state" {
			t.Errorf("state.db content = %q, want %q", string(projData), "global-state")
		}

		// Check that code_index.db was copied
		codeData, err := os.ReadFile(projCodeIndexDB) //nolint:gosec
		if err != nil {
			t.Fatalf("read project code_index.db: %v", err)
		}
		if string(codeData) != "global-code" {
			t.Errorf("code_index.db content = %q, want %q", string(codeData), "global-code")
		}
	})

	t.Run("NoOpIfProjectDBExists", func(t *testing.T) {
		// Clean and create project dir with existing DBs
		_ = os.RemoveAll(projectDir) //nolint:errcheck
		if err := os.MkdirAll(projectDir, 0o750); err != nil {
			t.Fatalf("mkdir project dir: %v", err)
		}
		if err := os.WriteFile(projStateDB, []byte("project-state"), 0o600); err != nil {
			t.Fatalf("create project state.db: %v", err)
		}
		if err := os.WriteFile(projCodeIndexDB, []byte("project-code"), 0o600); err != nil {
			t.Fatalf("create project code_index.db: %v", err)
		}

		err := migrateGlobalDBs(projectName)
		if err != nil {
			t.Fatalf("migrateGlobalDBs(%q) error: %v", projectName, err)
		}

		// Check that existing project DBs were not overwritten
		projData, err := os.ReadFile(projStateDB) //nolint:gosec
		if err != nil {
			t.Fatalf("read project state.db: %v", err)
		}
		if string(projData) != "project-state" {
			t.Errorf("state.db was overwritten, content = %q, want %q", string(projData), "project-state")
		}
	})

	t.Run("NoOpIfGlobalDBMissing", func(t *testing.T) {
		// Clean project dir and remove global DBs
		_ = os.RemoveAll(projectDir)     //nolint:errcheck
		_ = os.Remove(globalStateDB)     //nolint:errcheck
		_ = os.Remove(globalCodeIndexDB) //nolint:errcheck

		err := migrateGlobalDBs(projectName)
		if err != nil {
			t.Fatalf("migrateGlobalDBs(%q) error: %v", projectName, err)
		}

		// Check that no project DBs were created
		if _, err := os.Stat(projStateDB); err == nil {
			t.Error("project state.db should not exist when global DB is missing")
		}
		if _, err := os.Stat(projCodeIndexDB); err == nil {
			t.Error("project code_index.db should not exist when global DB is missing")
		}
	})

	t.Run("ErrorOnCopyFailure", func(t *testing.T) {
		// Recreate global DBs
		if err := os.WriteFile(globalStateDB, []byte("global-state"), 0o600); err != nil {
			t.Fatalf("create global state.db: %v", err)
		}
		if err := os.WriteFile(globalCodeIndexDB, []byte("global-code"), 0o600); err != nil {
			t.Fatalf("create global code_index.db: %v", err)
		}

		// Clean project dir
		_ = os.RemoveAll(projectDir) //nolint:errcheck

		// Create a file where the project dir should be to cause copy to fail
		if err := os.WriteFile(projectDir, []byte("not-a-dir"), 0o600); err != nil {
			t.Fatalf("create blocking file: %v", err)
		}

		err := migrateGlobalDBs(projectName)
		if err == nil {
			t.Errorf("migrateGlobalDBs(%q) expected error, got nil", projectName)
		}

		// Verify global DBs are untouched
		globalData, err := os.ReadFile(globalStateDB) //nolint:gosec
		if err != nil {
			t.Fatalf("read global state.db: %v", err)
		}
		if string(globalData) != "global-state" {
			t.Error("global state.db was corrupted")
		}
	})
}

// TestAllCmdPathsUseProjectPaths verifies that no cmd/oro source file (outside
// paths.go and test files) contains hardcoded path string literals that should
// instead come from the ProjectPaths struct.
//
// Acceptance: grep -rn '"\.beads"\|"\.worktrees"\|"\.oro/config' cmd/oro/*.go
// returns 0 hits outside of ResolvePaths itself and tests.
// TestLegacyBeadsAliasDeleted guards the Phase 11 cleanup requirement that the
// legacy beads alias is no longer present in paths.go.
func TestLegacyBeadsAliasDeleted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("paths.go"))
	if err != nil {
		t.Fatalf("read paths.go: %v", err)
	}
	legacyAliasName := "Legacy" + "BeadsDir"
	if strings.Contains(string(data), legacyAliasName) {
		t.Fatalf("paths.go still contains %s", legacyAliasName)
	}
}

func TestPaths_ReviewPatternCandidates_StandardAndStealth(t *testing.T) {
	t.Run("standard", func(t *testing.T) {
		repoRoot := t.TempDir()

		paths, err := ResolvePaths(repoRoot)
		if err != nil {
			t.Fatalf("ResolvePaths() error: %v", err)
		}

		wantReviewPatterns := filepath.Join(repoRoot, "assets", "review-patterns.md")
		if paths.ReviewPatterns != wantReviewPatterns {
			t.Errorf("ReviewPatterns = %q, want %q", paths.ReviewPatterns, wantReviewPatterns)
		}

		wantCandidates := filepath.Join(repoRoot, ".oro", "review-pattern-candidates.md")
		if paths.ReviewPatternCandidates != wantCandidates {
			t.Errorf("ReviewPatternCandidates = %q, want %q", paths.ReviewPatternCandidates, wantCandidates)
		}
	})

	t.Run("stealth", func(t *testing.T) {
		repoRoot := t.TempDir()
		tmpOroHome := t.TempDir()
		t.Setenv("ORO_HOME", tmpOroHome)

		resolved, err := filepath.EvalSymlinks(repoRoot)
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		sum := sha256.Sum256([]byte(resolved))
		hash := fmt.Sprintf("%x", sum[:8])

		stealthDir := filepath.Join(tmpOroHome, "projects", "s-"+hash)
		if err := os.MkdirAll(stealthDir, 0o750); err != nil {
			t.Fatalf("mkdir stealth dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stealthDir, "config.yaml"), []byte("mode: stealth\n"), 0o600); err != nil {
			t.Fatalf("write stealth config: %v", err)
		}

		paths, err := ResolvePaths(repoRoot)
		if err != nil {
			t.Fatalf("ResolvePaths() error: %v", err)
		}

		wantReviewPatterns := filepath.Join(stealthDir, "review-patterns.md")
		if paths.ReviewPatterns != wantReviewPatterns {
			t.Errorf("ReviewPatterns = %q, want %q", paths.ReviewPatterns, wantReviewPatterns)
		}

		wantCandidates := filepath.Join(stealthDir, "review-pattern-candidates.md")
		if paths.ReviewPatternCandidates != wantCandidates {
			t.Errorf("ReviewPatternCandidates = %q, want %q", paths.ReviewPatternCandidates, wantCandidates)
		}
	})
}

func TestAllCmdPathsUseProjectPaths(t *testing.T) {
	re := regexp.MustCompile(`"\.beads"|"\.worktrees"|"\.oro/config`)

	// paths.go is excluded because ResolvePaths defines those literals legitimately.
	// cmd_init.go is excluded because globalGitignoreEntries holds gitignore
	// patterns (not project paths) — they must be literal strings.
	excluded := map[string]bool{
		"paths.go":    true,
		"cmd_init.go": true,
	}

	goFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}

	for _, f := range goFiles {
		base := filepath.Base(f)
		if excluded[base] || strings.HasSuffix(base, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				t.Errorf("%s:%d: hardcoded path literal — use ProjectPaths instead:\n\t%s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
