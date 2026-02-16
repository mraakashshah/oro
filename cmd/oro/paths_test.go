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
	t.Setenv("ORO_MEMORY_DB", "")

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
	if paths.MemoryDBPath != filepath.Join(expectedBase, "memories.db") {
		t.Errorf("MemoryDBPath = %q, want %q", paths.MemoryDBPath, filepath.Join(expectedBase, "memories.db"))
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
	t.Setenv("ORO_MEMORY_DB", filepath.Join(tmpDir, "custom-memories.db"))

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
	if paths.MemoryDBPath != filepath.Join(tmpDir, "custom-memories.db") {
		t.Errorf("MemoryDBPath = %q, want %q", paths.MemoryDBPath, filepath.Join(tmpDir, "custom-memories.db"))
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
	t.Setenv("ORO_MEMORY_DB", "")

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

func TestResolvePaths_OroHomeOverride(t *testing.T) {
	tmpDir := t.TempDir()

	// ORO_HOME should affect the default base for other paths if they're not overridden.
	t.Setenv("ORO_HOME", tmpDir)
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")
	t.Setenv("ORO_DB_PATH", "")
	t.Setenv("ORO_MEMORY_DB", "")

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
	if paths.MemoryDBPath != filepath.Join(tmpDir, "memories.db") {
		t.Errorf("MemoryDBPath = %q, want %q", paths.MemoryDBPath, filepath.Join(tmpDir, "memories.db"))
	}
	if paths.CodeIndexDBPath != filepath.Join(tmpDir, "code_index.db") {
		t.Errorf("CodeIndexDBPath = %q, want %q", paths.CodeIndexDBPath, filepath.Join(tmpDir, "code_index.db"))
	}
}
