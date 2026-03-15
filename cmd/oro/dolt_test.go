package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDerivePort(t *testing.T) {
	t.Run("returns port in range 13307-14306", func(t *testing.T) {
		port := DerivePort("/some/project/.beads")
		if port < 13307 || port > 14306 {
			t.Errorf("DerivePort = %d, want in [13307, 14306]", port)
		}
	})

	t.Run("returns stable port for same path", func(t *testing.T) {
		path := "/home/user/projects/myapp/.beads"
		p1 := DerivePort(path)
		p2 := DerivePort(path)
		if p1 != p2 {
			t.Errorf("DerivePort not stable: first=%d second=%d", p1, p2)
		}
	})

	t.Run("returns different ports for different paths", func(t *testing.T) {
		p1 := DerivePort("/projects/alpha/.beads")
		p2 := DerivePort("/projects/beta/.beads")
		if p1 == p2 {
			t.Errorf("DerivePort returned same port %d for different paths", p1)
		}
	})

	t.Run("resolves relative path to absolute before hashing", func(t *testing.T) {
		// Two calls with the same resolved absolute path should return the same port.
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		p1 := DerivePort(beadsDir)
		// Verify port is in range.
		if p1 < 13307 || p1 > 14306 {
			t.Errorf("DerivePort with real path = %d, want in [13307, 14306]", p1)
		}

		// Same call returns same result.
		p2 := DerivePort(beadsDir)
		if p1 != p2 {
			t.Errorf("DerivePort not stable: %d != %d", p1, p2)
		}
	})
}

func TestReadDoltMeta(t *testing.T) {
	t.Run("returns nil for missing .beads directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		nonexistent := filepath.Join(tmpDir, "no-such-beads")

		meta, err := readDoltMeta(nonexistent)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta != nil {
			t.Errorf("readDoltMeta = %+v, want nil for missing dir", meta)
		}
	})

	t.Run("returns nil for missing metadata.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta != nil {
			t.Errorf("readDoltMeta = %+v, want nil when metadata.json missing", meta)
		}
	})

	t.Run("returns nil for sqlite backend", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		data := map[string]interface{}{
			"backend":  "sqlite",
			"database": "issues.db",
		}
		writeMetadata(t, beadsDir, data)

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta != nil {
			t.Errorf("readDoltMeta = %+v, want nil for sqlite backend", meta)
		}
	})

	t.Run("returns nil for missing backend field", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		data := map[string]interface{}{
			"database": "issues.db",
		}
		writeMetadata(t, beadsDir, data)

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta != nil {
			t.Errorf("readDoltMeta = %+v, want nil when no backend field", meta)
		}
	})

	t.Run("returns metadata for dolt backend", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		data := map[string]interface{}{
			"backend":          "dolt",
			"dolt_server_port": 13350,
			"dolt_database":    "beads",
		}
		writeMetadata(t, beadsDir, data)

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta == nil {
			t.Fatal("readDoltMeta = nil, want non-nil for dolt backend")
		}
		if meta.Backend != "dolt" {
			t.Errorf("meta.Backend = %q, want %q", meta.Backend, "dolt")
		}
		if meta.DoltServerPort != 13350 {
			t.Errorf("meta.DoltServerPort = %d, want 13350", meta.DoltServerPort)
		}
	})

	t.Run("returns error for malformed metadata.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("not-json"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		_, err := readDoltMeta(beadsDir)
		if err == nil {
			t.Fatal("readDoltMeta should return error for malformed JSON")
		}
	})
}

func TestIsDoltServerRunning(t *testing.T) {
	t.Run("returns false when no server is listening", func(t *testing.T) {
		// Port 19999 is almost certainly not in use during tests.
		running := isDoltServerRunning(19999)
		if running {
			t.Error("isDoltServerRunning = true, want false when no server on port 19999")
		}
	})
}

func TestStartDoltServer(t *testing.T) {
	t.Run("returns ErrNotFound when dolt not in PATH", func(t *testing.T) {
		// Override PATH to ensure dolt is not found.
		t.Setenv("PATH", t.TempDir())

		tmpDir := t.TempDir()
		_, err := startDoltServer(tmpDir, 19998)
		if err == nil {
			t.Fatal("startDoltServer should return error when dolt not in PATH")
		}
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("startDoltServer error = %v, want exec.ErrNotFound", err)
		}
	})
}

func TestStopDoltServer(t *testing.T) {
	t.Run("returns nil when no PID file exists (idempotent)", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := stopDoltServer(tmpDir)
		if err != nil {
			t.Errorf("stopDoltServer = %v, want nil when no PID file", err)
		}
	})
}

func TestEnsureDoltMetadata(t *testing.T) {
	t.Run("creates metadata.json when missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")

		err := ensureDoltMetadata(beadsDir, 13400)
		if err != nil {
			t.Fatalf("ensureDoltMetadata error: %v", err)
		}

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta == nil {
			t.Fatal("readDoltMeta = nil after ensureDoltMetadata")
		}
		if meta.Backend != "dolt" {
			t.Errorf("Backend = %q, want %q", meta.Backend, "dolt")
		}
		if meta.DoltServerPort != 13400 {
			t.Errorf("DoltServerPort = %d, want 13400", meta.DoltServerPort)
		}
		if meta.DoltDatabase != "beads" {
			t.Errorf("DoltDatabase = %q, want %q", meta.DoltDatabase, "beads")
		}
	})

	t.Run("preserves existing non-default port", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		// Write metadata with a custom port.
		data := map[string]interface{}{
			"backend":          "dolt",
			"dolt_server_port": 13500,
			"dolt_database":    "beads",
		}
		writeMetadata(t, beadsDir, data)

		// ensureDoltMetadata should not overwrite the existing port.
		err := ensureDoltMetadata(beadsDir, 13400)
		if err != nil {
			t.Fatalf("ensureDoltMetadata error: %v", err)
		}

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta.DoltServerPort != 13500 {
			t.Errorf("DoltServerPort = %d, want 13500 (should preserve existing)", meta.DoltServerPort)
		}
	})

	t.Run("overwrites default port 3307", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		data := map[string]interface{}{
			"backend":          "dolt",
			"dolt_server_port": 3307,
			"dolt_database":    "beads",
		}
		writeMetadata(t, beadsDir, data)

		err := ensureDoltMetadata(beadsDir, 13400)
		if err != nil {
			t.Fatalf("ensureDoltMetadata error: %v", err)
		}

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta.DoltServerPort != 13400 {
			t.Errorf("DoltServerPort = %d, want 13400 (should replace default 3307)", meta.DoltServerPort)
		}
	})
}

// writeMetadata writes a JSON object to <beadsDir>/metadata.json.
func writeMetadata(t *testing.T, beadsDir string, data map[string]interface{}) {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), b, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}
