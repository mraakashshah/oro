package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultMemoryStoreUsesProjectPaths verifies that defaultMemoryStore()
// uses ResolveProjectDBPaths to respect the current project context,
// so that different projects have separate databases.
func TestDefaultMemoryStoreUsesProjectPaths(t *testing.T) {
	// Setup: create a temporary .oro/config.yaml with a project name
	tmpDir := t.TempDir()
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir to tmpDir: %v", err)
	}
	defer func() { _ = os.Chdir(originalWd) }()

	// Create .oro/config.yaml with project name
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil {
		t.Fatalf("failed to create .oro dir: %v", err)
	}

	configFile := filepath.Join(oroDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte("project: test-project\n"), 0o600); err != nil {
		t.Fatalf("failed to write config.yaml: %v", err)
	}

	// Also create a temporary ORO_HOME to isolate the test
	oroHome := t.TempDir()
	origOroHome := os.Getenv("ORO_HOME")
	origOroProject := os.Getenv("ORO_PROJECT")
	defer func() {
		if origOroHome != "" {
			_ = os.Setenv("ORO_HOME", origOroHome) //nolint:errcheck // best-effort cleanup
		} else {
			os.Unsetenv("ORO_HOME")
		}
		if origOroProject != "" {
			_ = os.Setenv("ORO_PROJECT", origOroProject) //nolint:errcheck // best-effort cleanup
		} else {
			os.Unsetenv("ORO_PROJECT")
		}
	}()
	if err := os.Setenv("ORO_HOME", oroHome); err != nil {
		t.Fatalf("failed to set ORO_HOME: %v", err)
	}
	os.Unsetenv("ORO_PROJECT") // Clear any existing ORO_PROJECT so config.yaml is used

	// Verify that the StateDBPath would be project-scoped
	// The store's DB path should be in ~/.oro/projects/test-project/ not ~/.oro/
	expectedProjPath := filepath.Join(oroHome, "projects", "test-project")
	expectedDBPath := filepath.Join(expectedProjPath, "state.db")

	// Create the project directory structure
	if err := os.MkdirAll(expectedProjPath, 0o750); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	// Call defaultMemoryStore() which should now use ResolveProjectDBPaths
	_, err = defaultMemoryStore()
	if err != nil {
		t.Fatalf("defaultMemoryStore failed: %v", err)
	}

	// Verify ResolveProjectDBPaths would return the project-scoped path
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		t.Fatalf("ResolveProjectDBPaths failed: %v", err)
	}

	if paths.StateDBPath != expectedDBPath {
		t.Errorf("Expected StateDBPath %s, got %s", expectedDBPath, paths.StateDBPath)
	}

	// Verify the database was created at the project-scoped location
	if _, err := os.Stat(expectedDBPath); err != nil {
		t.Fatalf("Expected database to exist at %s: %v", expectedDBPath, err)
	}

	// Also verify that the global path was NOT used
	globalDBPath := filepath.Join(oroHome, "state.db")
	if _, err := os.Stat(globalDBPath); err == nil {
		// The global database shouldn't be created when a project is specified
		t.Errorf("Global database should not exist at %s when project is set", globalDBPath)
	}
}
