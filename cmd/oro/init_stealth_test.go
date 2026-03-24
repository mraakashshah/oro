package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestBootstrapStealthProject verifies the bootstrapStealthProject core function.
// Acceptance criteria (oro-e2tg.1):
//   - ResolvePaths(projectRoot) returns Mode "stealth" after bootstrapStealthProject runs.
//   - No files created inside project repo root.
//   - Stealth dir contains: config.yaml, beads/, settings.json, quality_gate.sh.
func TestBootstrapStealthProject(t *testing.T) {
	t.Run("ResolvePaths returns stealth after bootstrap", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)

		if err := bootstrapStealthProject(projectDir, oroHome); err != nil {
			t.Fatalf("bootstrapStealthProject: %v", err)
		}

		paths, err := ResolvePaths(projectDir)
		if err != nil {
			t.Fatalf("ResolvePaths: %v", err)
		}
		if paths.Mode != "stealth" {
			t.Errorf("Mode = %q, want %q", paths.Mode, "stealth")
		}
	})

	t.Run("no files created in project root", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)

		if err := bootstrapStealthProject(projectDir, oroHome); err != nil {
			t.Fatalf("bootstrapStealthProject: %v", err)
		}

		entries, err := os.ReadDir(projectDir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 0 {
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			t.Errorf("project root has unexpected files: %v", names)
		}
	})

	t.Run("stealth dir contains required files and dirs", func(t *testing.T) {
		projectDir := t.TempDir()
		oroHome := t.TempDir()
		t.Setenv("ORO_HOME", oroHome)

		if err := bootstrapStealthProject(projectDir, oroHome); err != nil {
			t.Fatalf("bootstrapStealthProject: %v", err)
		}

		// Compute expected stealth dir path.
		resolved, err := filepath.EvalSymlinks(projectDir)
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		sum := sha256.Sum256([]byte(resolved))
		hash := fmt.Sprintf("%x", sum[:8])
		stealthDir := filepath.Join(oroHome, "projects", "s-"+hash)

		// config.yaml must exist.
		if _, err := os.Stat(filepath.Join(stealthDir, "config.yaml")); err != nil {
			t.Errorf("config.yaml missing in stealth dir: %v", err)
		}

		// beads/ must be a directory.
		fi, err := os.Stat(filepath.Join(stealthDir, "beads"))
		if err != nil {
			t.Errorf("beads/ missing in stealth dir: %v", err)
		} else if !fi.IsDir() {
			t.Errorf("beads is not a directory")
		}

		// settings.json must exist.
		if _, err := os.Stat(filepath.Join(stealthDir, "settings.json")); err != nil {
			t.Errorf("settings.json missing in stealth dir: %v", err)
		}

		// quality_gate.sh must exist.
		if _, err := os.Stat(filepath.Join(stealthDir, "quality_gate.sh")); err != nil {
			t.Errorf("quality_gate.sh missing in stealth dir: %v", err)
		}
	})
}
