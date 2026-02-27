package main

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/protocol"
)

func TestResolvePaths_Defaults(t *testing.T) {
	// Clear all env overrides.
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}

	paths, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
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
	if paths.CodeIndexDBPath != filepath.Join(expectedBase, "code_index.db") {
		t.Errorf("CodeIndexDBPath = %q, want %q", paths.CodeIndexDBPath, filepath.Join(expectedBase, "code_index.db"))
	}
}

func TestResolvePaths_EnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()

	// Set all env overrides to temp dir paths.
	t.Setenv("ORO_HOME", filepath.Join(tmpDir, "custom-oro"))
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "custom.pid"))
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "custom.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "custom-state.db"))

	paths, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
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
	t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "custom.pid"))
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")

	paths, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
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

// --- ResolveProjectDBPaths tests ---

func TestResolveProjectDBPaths_WithEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PROJECT", "foo")

	paths, err := ResolveProjectDBPaths()
	if err != nil {
		t.Fatalf("ResolveProjectDBPaths() error: %v", err)
	}

	wantState := filepath.Join(tmpDir, "projects", "foo", "state.db")
	wantCode := filepath.Join(tmpDir, "projects", "foo", "code_index.db")

	if paths.StateDBPath != wantState {
		t.Errorf("StateDBPath = %q, want %q", paths.StateDBPath, wantState)
	}
	if paths.CodeIndexDBPath != wantCode {
		t.Errorf("CodeIndexDBPath = %q, want %q", paths.CodeIndexDBPath, wantCode)
	}
	// PID/Socket remain global
	if paths.PIDPath != filepath.Join(tmpDir, "oro.pid") {
		t.Errorf("PIDPath = %q, want global %q", paths.PIDPath, filepath.Join(tmpDir, "oro.pid"))
	}
	if paths.SocketPath != filepath.Join(tmpDir, "oro.sock") {
		t.Errorf("SocketPath = %q, want global %q", paths.SocketPath, filepath.Join(tmpDir, "oro.sock"))
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
	if got := readProjectName(); got != "myproject" {
		t.Errorf("readProjectName() = %q, want %q", got, "myproject")
	}
}

func TestReadProjectName_EmptyFallback(t *testing.T) {
	t.Setenv("ORO_PROJECT", "")
	noConfigDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(noConfigDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	if got := readProjectName(); got != "" {
		t.Errorf("readProjectName() = %q, want empty string", got)
	}
}

func TestResolvePaths_OroHomeOverride(t *testing.T) {
	tmpDir := t.TempDir()

	// ORO_HOME should affect the default base for other paths if they're not overridden.
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")

	paths, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths() error: %v", err)
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
