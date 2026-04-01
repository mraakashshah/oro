package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

func TestStartDoltServerAdoptsRunning(t *testing.T) {
	t.Run("adopts when PID file present and process alive", func(t *testing.T) {
		// Start a TCP listener on a free port to simulate a running dolt server.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()

		port := ln.Addr().(*net.TCPAddr).Port
		tmpDir := t.TempDir()

		// Write a PID file pointing at our own process (alive).
		pidPath := filepath.Join(tmpDir, "dolt-server.pid")
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write PID file: %v", err)
		}

		pid, err := startDoltServer(tmpDir, port)
		if err != nil {
			t.Fatalf("startDoltServer should adopt own server, got error: %v", err)
		}
		if pid != 0 {
			t.Errorf("adopted server should return pid=0, got %d", pid)
		}
	})

	t.Run("returns error when port occupied by foreign process", func(t *testing.T) {
		// Start a TCP listener on a free port to simulate a foreign process.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()

		port := ln.Addr().(*net.TCPAddr).Port
		tmpDir := t.TempDir()
		// No PID file written — port occupied by foreign process.

		_, err = startDoltServer(tmpDir, port)
		if err == nil {
			t.Fatal("startDoltServer should return error when port occupied by foreign process (no PID file)")
		}
	})

	t.Run("returns error when port occupied and PID file stale", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()

		port := ln.Addr().(*net.TCPAddr).Port
		tmpDir := t.TempDir()

		// Write a PID file with a dead PID (PID 1 is init, but use a very
		// large PID that almost certainly doesn't exist).
		pidPath := filepath.Join(tmpDir, "dolt-server.pid")
		if err := os.WriteFile(pidPath, []byte("9999999"), 0o600); err != nil {
			t.Fatalf("write PID file: %v", err)
		}

		_, err = startDoltServer(tmpDir, port)
		if err == nil {
			t.Fatal("startDoltServer should return error when PID file is stale and port occupied by foreign process")
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

func TestIsSharedServer(t *testing.T) {
	t.Run("returns true for port 13307", func(t *testing.T) {
		if !isSharedServer(SharedDoltPort) {
			t.Errorf("isSharedServer(%d) = false, want true", SharedDoltPort)
		}
	})

	t.Run("returns false for port below 13307", func(t *testing.T) {
		if isSharedServer(13306) {
			t.Error("isSharedServer(13306) = true, want false")
		}
	})

	t.Run("returns false for port above 13307", func(t *testing.T) {
		if isSharedServer(13308) {
			t.Error("isSharedServer(13308) = true, want false")
		}
	})

	t.Run("returns false for port 0", func(t *testing.T) {
		if isSharedServer(0) {
			t.Error("isSharedServer(0) = true, want false")
		}
	})
}

func TestStartSharedDoltServer(t *testing.T) {
	t.Run("returns ErrNotFound when dolt not in PATH", func(t *testing.T) {
		if isDoltServerRunning(SharedDoltPort) {
			t.Skip("shared dolt server already running on port 13307 — cannot test LookPath fallback")
		}
		t.Setenv("PATH", t.TempDir())
		tmpDir := t.TempDir()
		_, err := startSharedDoltServer(tmpDir)
		if err == nil {
			t.Fatal("startSharedDoltServer should return error when dolt not in PATH")
		}
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("startSharedDoltServer error = %v, want exec.ErrNotFound", err)
		}
	})

	t.Run("returns error with blocker PID when port 13307 occupied by foreign process", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:13307")
		if err != nil {
			t.Skipf("cannot bind to port 13307 (already in use): %v", err)
		}
		defer ln.Close()

		// Wait until port is confirmed listening.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && !isDoltServerRunning(SharedDoltPort) {
			time.Sleep(20 * time.Millisecond)
		}

		tmpDir := t.TempDir()
		// No PID file written — port is occupied by a foreign (non-dolt) process.
		_, err = startSharedDoltServer(tmpDir)
		if err == nil {
			t.Fatal("startSharedDoltServer should return error when port 13307 occupied by foreign process")
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

// startListeningProcess starts a subprocess that persistently listens on the
// given port and returns the process. Uses nc -k (keep-open) so a single
// probe connection from isDoltServerRunning does not cause nc to exit.
// The caller is responsible for cleanup.
func startListeningProcess(t *testing.T, port int) *exec.Cmd {
	t.Helper()
	// -k: keep listening after each connection (macOS/BSD nc).
	cmd := exec.Command("nc", "-k", "-l", strconv.Itoa(port)) //nolint:gosec // test helper with controlled port argument
	if err := cmd.Start(); err != nil {
		t.Skipf("nc not available or failed to start: %v", err)
	}
	// Wait until port is accepting connections (up to 2s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if isDoltServerRunning(port) {
			return cmd
		}
		time.Sleep(30 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	t.Skip("nc listener did not become ready in time")
	return nil
}

func TestDiscoverPIDByPort(t *testing.T) {
	t.Run("returns ErrNotFound when lsof not in PATH", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := discoverPIDByPort(19997)
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("discoverPIDByPort error = %v, want exec.ErrNotFound", err)
		}
	})

	t.Run("returns error when no process on port", func(t *testing.T) {
		_, err := discoverPIDByPort(19997)
		if err == nil {
			t.Error("discoverPIDByPort should return error when no process listening on port")
		}
		if errors.Is(err, exec.ErrNotFound) {
			t.Skip("lsof not available")
		}
	})

	t.Run("returns our PID when we are listening", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen: %v", err)
		}
		defer ln.Close()                      //nolint:errcheck // test cleanup
		port := ln.Addr().(*net.TCPAddr).Port //nolint:errcheck // type assertion safe after net.Listen("tcp",...)

		pid, err := discoverPIDByPort(port)
		if errors.Is(err, exec.ErrNotFound) {
			t.Skip("lsof not available")
		}
		if err != nil {
			t.Fatalf("discoverPIDByPort(%d) = error %v", port, err)
		}
		if pid != os.Getpid() {
			t.Errorf("discoverPIDByPort = PID %d, want own PID %d", pid, os.Getpid())
		}
	})
}

func TestKillAndWait(t *testing.T) {
	t.Run("sends SIGTERM and waits for process to die", func(t *testing.T) {
		tmpDir := t.TempDir()
		cmd := exec.Command("sleep", "100")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })

		pid := cmd.Process.Pid
		if err := killAndWait(pid, tmpDir); err != nil {
			t.Errorf("killAndWait error: %v", err)
		}
		if IsProcessAlive(pid) {
			t.Error("process should be dead after killAndWait")
		}
	})

	t.Run("removes PID and port files on completion", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidPath := filepath.Join(tmpDir, "dolt-server.pid")
		portPath := filepath.Join(tmpDir, "dolt-server.port")
		if err := os.WriteFile(pidPath, []byte("0"), 0o600); err != nil {
			t.Fatalf("write pid: %v", err)
		}
		if err := os.WriteFile(portPath, []byte("13400"), 0o600); err != nil {
			t.Fatalf("write port: %v", err)
		}

		cmd := exec.Command("sleep", "100")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })

		_ = killAndWait(cmd.Process.Pid, tmpDir)

		if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
			t.Error("dolt-server.pid should be removed after killAndWait")
		}
		if _, err := os.Stat(portPath); !errors.Is(err, os.ErrNotExist) {
			t.Error("dolt-server.port should be removed after killAndWait")
		}
	})
}

func TestStopDoltServerPortFallback(t *testing.T) {
	t.Run("(1) PID file present + process alive → killed via SIGTERM", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		cmd := exec.Command("sleep", "100")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })

		pid := cmd.Process.Pid
		pidPath := filepath.Join(beadsDir, "dolt-server.pid")
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o600); err != nil {
			t.Fatalf("write PID file: %v", err)
		}

		if err := stopDoltServer(beadsDir); err != nil {
			t.Errorf("stopDoltServer error: %v", err)
		}

		if IsProcessAlive(pid) {
			t.Error("process should be dead after stopDoltServer")
		}
		if _, err := os.Stat(pidPath); !errors.Is(err, os.ErrNotExist) {
			t.Error("PID file should be removed")
		}
	})

	t.Run("(2) PID file missing + port listening → killed via lsof fallback", func(t *testing.T) {
		// Find a free port, then start a listener subprocess on it.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port //nolint:errcheck // type assertion safe after net.Listen("tcp",...)
		_ = ln.Close()

		cmd := startListeningProcess(t, port)
		t.Cleanup(func() { _ = cmd.Process.Kill() })

		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeMetadata(t, beadsDir, map[string]interface{}{
			"backend":          "dolt",
			"dolt_server_port": port,
			"dolt_database":    "beads",
		})
		// No PID file written.

		if err := stopDoltServer(beadsDir); err != nil {
			t.Errorf("stopDoltServer error: %v", err)
		}

		// Allow brief settling time.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && IsProcessAlive(cmd.Process.Pid) {
			time.Sleep(50 * time.Millisecond)
		}
		if IsProcessAlive(cmd.Process.Pid) {
			t.Errorf("nc process (PID %d) should be dead after lsof fallback", cmd.Process.Pid)
		}
	})

	t.Run("(3) PID file missing + port not listening → returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeMetadata(t, beadsDir, map[string]interface{}{
			"backend":          "dolt",
			"dolt_server_port": 19996,
			"dolt_database":    "beads",
		})

		if err := stopDoltServer(beadsDir); err != nil {
			t.Errorf("stopDoltServer = %v, want nil when port not listening", err)
		}
	})

	t.Run("(4) PID file missing + metadata.json missing → returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Neither PID file nor metadata.json.

		if err := stopDoltServer(beadsDir); err != nil {
			t.Errorf("stopDoltServer = %v, want nil when metadata missing", err)
		}
	})

	t.Run("(5) poll wait uses IsProcessAlive loop not proc.Wait", func(t *testing.T) {
		// Verify killAndWait is used (poll loop) by ensuring a process started
		// with os.StartProcess (which would cause proc.Wait deadlock if misused)
		// is handled correctly. We use sleep as a proxy.
		tmpDir := t.TempDir()
		cmd := exec.Command("sleep", "100")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })

		pid := cmd.Process.Pid
		done := make(chan error, 1)
		go func() {
			done <- killAndWait(pid, tmpDir)
		}()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("killAndWait error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("killAndWait timed out — suggests blocking wait instead of poll")
		}
	})
}
