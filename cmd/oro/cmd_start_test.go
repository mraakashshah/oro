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

func TestDaemonStartupCleansWorkerLogs(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)

	// Create workers dir with some files to simulate stale logs.
	workersDir := tmpDir + "/workers"
	if err := os.MkdirAll(workersDir, 0o700); err != nil {
		t.Fatalf("setup workers dir: %v", err)
	}
	staleLog := workersDir + "/worker-123.log"
	if err := os.WriteFile(staleLog, []byte("stale log content"), 0o600); err != nil {
		t.Fatalf("create stale log: %v", err)
	}

	// cleanWorkerLogs should wipe the directory and recreate it empty.
	cleanWorkerLogs(tmpDir)

	// Assert: workers dir exists but is empty.
	entries, err := os.ReadDir(workersDir)
	if err != nil {
		t.Fatalf("ReadDir workers: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected workers dir to be empty, got %d entries", len(entries))
	}
}

func TestDaemonStartupCleansWorkerLogs_MissingDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)

	// cleanWorkerLogs should not fail when workers dir doesn't exist yet.
	cleanWorkerLogs(tmpDir)

	// workers dir should be created.
	workersDir := tmpDir + "/workers"
	if _, err := os.Stat(workersDir); err != nil {
		t.Errorf("expected workers dir to be created, got: %v", err)
	}
}
