package main

import (
	"encoding/json"
	"os"
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
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		p1 := DerivePort(beadsDir)
		if p1 < 13307 || p1 > 14306 {
			t.Errorf("DerivePort with real path = %d, want in [13307, 14306]", p1)
		}

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

		writeMetadata(t, beadsDir, map[string]any{
			"backend":  "sqlite",
			"database": "issues.db",
		})

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

		writeMetadata(t, beadsDir, map[string]any{
			"database": "issues.db",
		})

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

		writeMetadata(t, beadsDir, map[string]any{
			"backend":          "dolt",
			"dolt_server_port": 13350,
			"dolt_database":    "beads",
		})

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

func writeMetadata(t *testing.T, beadsDir string, data map[string]any) {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), b, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}
