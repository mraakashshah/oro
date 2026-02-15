package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartReadsProjectConfig(t *testing.T) {
	t.Run("reads project name from .oro/config.yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o755); err != nil { //nolint:gosec // test dir
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: myproject\nlanguages:\n  go:\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		name, err := readProjectConfig(tmpDir)
		if err != nil {
			t.Fatalf("readProjectConfig failed: %v", err)
		}
		if name != "myproject" {
			t.Errorf("expected 'myproject', got %q", name)
		}
	})

	t.Run("returns empty string when .oro/config.yaml missing", func(t *testing.T) {
		tmpDir := t.TempDir()

		name, err := readProjectConfig(tmpDir)
		if err != nil {
			t.Fatalf("readProjectConfig should not error on missing config: %v", err)
		}
		if name != "" {
			t.Errorf("expected empty string, got %q", name)
		}
	})

	t.Run("ORO_HOME is set for child processes", func(t *testing.T) {
		// resolveOroHome should return ORO_HOME when set
		t.Setenv("ORO_HOME", "/custom/oro")
		home, err := resolveOroHome()
		if err != nil {
			t.Fatalf("resolveOroHome failed: %v", err)
		}
		if home != "/custom/oro" {
			t.Errorf("expected /custom/oro, got %q", home)
		}
	})
}
