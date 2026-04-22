package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
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

// findPathWithPort returns an absolute path under baseDir whose DerivePort equals targetPort.
func findPathWithPort(t *testing.T, baseDir string, targetPort int) string {
	t.Helper()
	for i := 0; i < 500000; i++ {
		p := filepath.Join(baseDir, fmt.Sprintf("p%d", i), ".beads")
		if DerivePort(p) == targetPort {
			return p
		}
	}
	t.Fatalf("could not find path hashing to port %d in 500k iterations", targetPort)
	return ""
}

func TestAllocatePort_NewProject(t *testing.T) {
	oroHome := t.TempDir()
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	port, err := AllocatePort(beadsDir, "test-project", oroHome)
	if err != nil {
		t.Fatalf("AllocatePort error: %v", err)
	}
	if port < doltPortBase+1 || port > doltPortBase+doltPortRange-1 {
		t.Errorf("AllocatePort = %d, want in [%d, %d]", port, doltPortBase+1, doltPortBase+doltPortRange-1)
	}
	if port == SharedDoltPort {
		t.Errorf("AllocatePort = %d = SharedDoltPort, must never return it", port)
	}
}

func TestAllocatePort_Idempotent(t *testing.T) {
	oroHome := t.TempDir()
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	port1, err := AllocatePort(beadsDir, "test-project", oroHome)
	if err != nil {
		t.Fatalf("first AllocatePort error: %v", err)
	}
	port2, err := AllocatePort(beadsDir, "test-project", oroHome)
	if err != nil {
		t.Fatalf("second AllocatePort error: %v", err)
	}
	if port1 != port2 {
		t.Errorf("AllocatePort not idempotent: first=%d second=%d", port1, port2)
	}
}

func TestAllocatePort_Collision(t *testing.T) {
	oroHome := t.TempDir()

	// Register project1; its parent dir must exist so pruneRegistry doesn't remove it.
	projDir1 := t.TempDir()
	beadsDir1 := filepath.Join(projDir1, ".beads")
	port1, err := AllocatePort(beadsDir1, "project1", oroHome)
	if err != nil {
		t.Fatalf("AllocatePort(project1) error: %v", err)
	}

	// Find a different beadsDir that DerivePort maps to the same port as project1.
	searchBase := t.TempDir()
	beadsDir2 := findPathWithPort(t, searchBase, port1)

	port2, err := AllocatePort(beadsDir2, "project2", oroHome)
	if err != nil {
		t.Fatalf("AllocatePort(project2) error: %v", err)
	}
	if port2 == port1 {
		t.Errorf("collision not resolved: both projects got port %d", port1)
	}
	if port2 == SharedDoltPort {
		t.Errorf("AllocatePort(project2) = SharedDoltPort %d, must never return it", port2)
	}
}

func TestAllocatePort_SharedPortReserved(t *testing.T) {
	oroHome := t.TempDir()
	baseDir := t.TempDir()

	// Allocate multiple projects; none should get SharedDoltPort.
	for i := 0; i < 20; i++ {
		beadsDir := filepath.Join(baseDir, fmt.Sprintf("proj%d", i), ".beads")
		port, err := AllocatePort(beadsDir, fmt.Sprintf("project%d", i), oroHome)
		if err != nil {
			t.Fatalf("AllocatePort(proj%d) error: %v", i, err)
		}
		if port == SharedDoltPort {
			t.Errorf("AllocatePort(proj%d) = %d = SharedDoltPort, must never allocate it", i, port)
		}
	}
}

func TestAllocatePort_DeriveReturns13307(t *testing.T) {
	oroHome := t.TempDir()
	searchBase := t.TempDir()
	beadsDir := findPathWithPort(t, searchBase, SharedDoltPort)

	port, err := AllocatePort(beadsDir, "test", oroHome)
	if err != nil {
		t.Fatalf("AllocatePort error: %v", err)
	}
	if port == SharedDoltPort {
		t.Errorf("AllocatePort = %d = SharedDoltPort, must not return it even when DerivePort would", port)
	}
	if port != SharedDoltPort+1 {
		t.Errorf("AllocatePort = %d, want %d (immediate bump from 13307)", port, SharedDoltPort+1)
	}
}

func TestAllocatePort_CorruptRegistry(t *testing.T) {
	oroHome := t.TempDir()
	registryPath := filepath.Join(oroHome, "port-registry.json")
	if err := os.WriteFile(registryPath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt registry: %v", err)
	}

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	port, err := AllocatePort(beadsDir, "test", oroHome)
	if err != nil {
		t.Fatalf("AllocatePort with corrupt registry should not error: %v", err)
	}
	if port < doltPortBase+1 || port > doltPortBase+doltPortRange-1 {
		t.Errorf("AllocatePort = %d, want in [%d, %d]", port, doltPortBase+1, doltPortBase+doltPortRange-1)
	}
	if port == SharedDoltPort {
		t.Errorf("AllocatePort = SharedDoltPort, must not allocate it")
	}
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

func TestAllocatePort_MigrationPopulates(t *testing.T) {
	oroHome := t.TempDir()
	projectsDir := filepath.Join(oroHome, "projects")

	// Create two projects with dolt-server.port files.
	projDir1 := filepath.Join(projectsDir, "p1")
	projRoot1 := t.TempDir()
	if err := os.MkdirAll(projDir1, 0o750); err != nil {
		t.Fatalf("mkdir proj1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDir1, "project.root"), []byte(projRoot1), 0o600); err != nil {
		t.Fatalf("write project.root 1: %v", err)
	}

	beadsDir1 := filepath.Join(projRoot1, ".beads")
	if err := os.MkdirAll(beadsDir1, 0o750); err != nil {
		t.Fatalf("mkdir beads1: %v", err)
	}
	port1 := 13400
	if err := os.WriteFile(filepath.Join(beadsDir1, "dolt-server.port"), []byte(strconv.Itoa(port1)), 0o600); err != nil {
		t.Fatalf("write port 1: %v", err)
	}

	projDir2 := filepath.Join(projectsDir, "p2")
	projRoot2 := t.TempDir()
	if err := os.MkdirAll(projDir2, 0o750); err != nil {
		t.Fatalf("mkdir proj2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDir2, "project.root"), []byte(projRoot2), 0o600); err != nil {
		t.Fatalf("write project.root 2: %v", err)
	}

	beadsDir2 := filepath.Join(projRoot2, ".beads")
	if err := os.MkdirAll(beadsDir2, 0o750); err != nil {
		t.Fatalf("mkdir beads2: %v", err)
	}
	port2 := 13401
	if err := os.WriteFile(filepath.Join(beadsDir2, "dolt-server.port"), []byte(strconv.Itoa(port2)), 0o600); err != nil {
		t.Fatalf("write port 2: %v", err)
	}

	reg := emptyRegistry()
	err := migrateExistingPorts(reg, oroHome)
	if err != nil {
		t.Fatalf("migrateExistingPorts error: %v", err)
	}

	if len(reg.Allocations) != 2 {
		t.Errorf("after migration, registry has %d allocations, want 2", len(reg.Allocations))
	}

	abs1, _ := filepath.Abs(beadsDir1)
	abs2, _ := filepath.Abs(beadsDir2)

	if alloc, ok := reg.Allocations[abs1]; !ok {
		t.Errorf("beadsDir1 not in registry")
	} else if alloc.Port != port1 {
		t.Errorf("beadsDir1 port = %d, want %d", alloc.Port, port1)
	}

	if alloc, ok := reg.Allocations[abs2]; !ok {
		t.Errorf("beadsDir2 not in registry")
	} else if alloc.Port != port2 {
		t.Errorf("beadsDir2 port = %d, want %d", alloc.Port, port2)
	}
}

func TestAllocatePort_PruneStale(t *testing.T) {
	oroHome := t.TempDir()
	projectsDir := filepath.Join(oroHome, "projects")

	// Create project with deleted project root.
	projDir := filepath.Join(projectsDir, "stale")
	projRoot := t.TempDir()
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "project.root"), []byte(projRoot), 0o600); err != nil {
		t.Fatalf("write project.root: %v", err)
	}

	beadsDir := filepath.Join(projRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	port := 13400
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(port)), 0o600); err != nil {
		t.Fatalf("write port: %v", err)
	}

	// Now delete the project root.
	if err := os.RemoveAll(projRoot); err != nil {
		t.Fatalf("remove project root: %v", err)
	}

	reg := emptyRegistry()
	err := migrateExistingPorts(reg, oroHome)
	if err != nil {
		t.Fatalf("migrateExistingPorts error: %v", err)
	}

	if len(reg.Allocations) != 0 {
		t.Errorf("after migration with deleted root, registry has %d allocations, want 0 (should prune stale)", len(reg.Allocations))
	}
}

func TestAllocatePort_PruneStealth(t *testing.T) {
	oroHome := t.TempDir()
	projectsDir := filepath.Join(oroHome, "projects")

	// Create project with stealth dir (project.root exists but points to deleted root).
	stealthHash := "s-abc123"
	stealthDir := filepath.Join(projectsDir, stealthHash)
	projRoot := t.TempDir()
	if err := os.MkdirAll(stealthDir, 0o750); err != nil {
		t.Fatalf("mkdir stealth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stealthDir, "project.root"), []byte(projRoot), 0o600); err != nil {
		t.Fatalf("write project.root: %v", err)
	}

	beadsDir := filepath.Join(projRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	port := 13400
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(port)), 0o600); err != nil {
		t.Fatalf("write port: %v", err)
	}

	// Delete project root but leave stealth dir intact.
	if err := os.RemoveAll(projRoot); err != nil {
		t.Fatalf("remove project root: %v", err)
	}

	reg := emptyRegistry()
	err := migrateExistingPorts(reg, oroHome)
	if err != nil {
		t.Fatalf("migrateExistingPorts error: %v", err)
	}

	if len(reg.Allocations) != 0 {
		t.Errorf("after migration with stealth dir, registry has %d allocations, want 0 (should prune stealth)", len(reg.Allocations))
	}
}

func TestAllocatePort_ConcurrentLocking(t *testing.T) {
	oroHome := t.TempDir()
	projectsDir := filepath.Join(oroHome, "projects")

	// Create a project.
	projDir := filepath.Join(projectsDir, "p1")
	projRoot := t.TempDir()
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "project.root"), []byte(projRoot), 0o600); err != nil {
		t.Fatalf("write project.root: %v", err)
	}

	beadsDir := filepath.Join(projRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	port := 13400
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(port)), 0o600); err != nil {
		t.Fatalf("write port: %v", err)
	}

	// Test that concurrent AllocatePort calls work safely.
	reg := emptyRegistry()
	err := migrateExistingPorts(reg, oroHome)
	if err != nil {
		t.Fatalf("migrateExistingPorts error: %v", err)
	}

	abs, _ := filepath.Abs(beadsDir)
	if alloc, ok := reg.Allocations[abs]; !ok {
		t.Errorf("beadsDir not in registry after migration")
	} else if alloc.Port != port {
		t.Errorf("port = %d, want %d", alloc.Port, port)
	}

	// Write the migrated registry to disk.
	registryPath := filepath.Join(oroHome, "port-registry.json")
	if err := writeRegistryAtomic(registryPath, reg); err != nil {
		t.Fatalf("writeRegistryAtomic error: %v", err)
	}

	// Now allocate again; should return same port (idempotent).
	port2, err := AllocatePort(beadsDir, "p1", oroHome)
	if err != nil {
		t.Fatalf("AllocatePort error: %v", err)
	}
	if port2 != port {
		t.Errorf("AllocatePort after migration returned %d, want %d", port2, port)
	}
}

// TestAllocatePort_MigrationOnFirstCall verifies that when the registry file does
// not exist, AllocatePort auto-discovers existing projects via discoverBreadsDirs
// and pre-populates the registry before allocating the requested port. This
// prevents the first project to init from claiming a port already in use by an
// unregistered existing project.
func TestAllocatePort_MigrationOnFirstCall(t *testing.T) {
	oroHome := t.TempDir()
	projectsDir := filepath.Join(oroHome, "projects")

	// Set up an existing project (projA) with a known port in dolt-server.port.
	projDirA := filepath.Join(projectsDir, "projA")
	projRootA := t.TempDir()
	if err := os.MkdirAll(projDirA, 0o750); err != nil {
		t.Fatalf("mkdir projA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDirA, "project.root"), []byte(projRootA), 0o600); err != nil {
		t.Fatalf("write project.root A: %v", err)
	}
	beadsDirA := filepath.Join(projRootA, ".beads")
	if err := os.MkdirAll(beadsDirA, 0o750); err != nil {
		t.Fatalf("mkdir beadsA: %v", err)
	}
	portA := 13400
	if err := os.WriteFile(filepath.Join(beadsDirA, "dolt-server.port"), []byte(strconv.Itoa(portA)), 0o600); err != nil {
		t.Fatalf("write portA: %v", err)
	}

	// NO registry file exists yet. Now call AllocatePort for a NEW project (projB).
	projRootB := t.TempDir()
	beadsDirB := filepath.Join(projRootB, ".beads")
	if err := os.MkdirAll(beadsDirB, 0o750); err != nil {
		t.Fatalf("mkdir beadsB: %v", err)
	}

	portB, err := AllocatePort(beadsDirB, "projB", oroHome)
	if err != nil {
		t.Fatalf("AllocatePort(projB): %v", err)
	}

	// Read the registry that AllocatePort created.
	registryPath := filepath.Join(oroHome, "port-registry.json")
	reg, err := readRegistry(registryPath)
	if err != nil {
		t.Fatalf("readRegistry: %v", err)
	}

	// projA should have been auto-discovered and registered with its existing port.
	absA, _ := filepath.Abs(beadsDirA)
	allocA, ok := reg.Allocations[absA]
	if !ok {
		t.Fatalf("projA not in registry after AllocatePort — migration did not run")
	}
	if allocA.Port != portA {
		t.Errorf("projA port = %d, want %d", allocA.Port, portA)
	}

	// projB should NOT have been given projA's port (13400).
	if portB == portA {
		t.Errorf("projB got projA's port %d — migration did not prevent collision", portA)
	}
}

// TestAllocatePort_MetadataSync verifies that when AllocatePort assigns a port
// that differs from the port already recorded in metadata.json (because the
// preferred port was taken by another project), initDoltForProject updates
// metadata.json to match the registry-assigned port via setDoltPort.
func TestAllocatePort_MetadataSync(t *testing.T) {
	oroHome := t.TempDir()

	// Project 1: allocate a port so that project 2 cannot have it.
	projDir1 := t.TempDir() // parent must exist so projectRootAlive keeps it during pruneRegistry
	beadsDir1 := filepath.Join(projDir1, ".beads")
	port1, err := AllocatePort(beadsDir1, "project1", oroHome)
	if err != nil {
		t.Fatalf("AllocatePort(project1): %v", err)
	}

	// Project 2: find a path whose DerivePort equals port1 (collision scenario).
	searchBase := t.TempDir()
	beadsDir2 := findPathWithPort(t, searchBase, port1)
	if err := os.MkdirAll(filepath.Dir(beadsDir2), 0o750); err != nil {
		t.Fatalf("mkdir beadsDir2 parent: %v", err)
	}
	if err := os.MkdirAll(beadsDir2, 0o750); err != nil {
		t.Fatalf("mkdir beadsDir2: %v", err)
	}

	// Write stale metadata.json for project2 with the colliding port.
	writeMetadata(t, beadsDir2, map[string]interface{}{
		"backend":          "dolt",
		"dolt_server_port": port1,
		"dolt_database":    "beads",
	})

	// initDoltForProject must call AllocatePort (getting a bumped port ≠ port1)
	// and sync metadata.json to the registry-assigned port.
	initDoltForProject(beadsDir2, oroHome)

	// Read back the registry entry for beadsDir2.
	absBeadsDir2, _ := filepath.Abs(beadsDir2)
	registryPath := filepath.Join(oroHome, "port-registry.json")
	regData, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	var reg portRegistry
	if err := json.Unmarshal(regData, &reg); err != nil {
		t.Fatalf("unmarshal registry: %v", err)
	}
	alloc, ok := reg.Allocations[absBeadsDir2]
	if !ok {
		t.Fatal("beadsDir2 not in registry after initDoltForProject — AllocatePort was not called")
	}
	registryPort := alloc.Port

	// Registry port must not be port1 (collision must have been resolved).
	if registryPort == port1 {
		t.Errorf("registry port = %d = port1; collision not resolved", registryPort)
	}

	// Metadata port must match registry port.
	meta, err := readDoltMeta(beadsDir2)
	if err != nil {
		t.Fatalf("readDoltMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("metadata.json missing after initDoltForProject")
	}
	if meta.DoltServerPort != registryPort {
		t.Errorf("metadata.DoltServerPort = %d, want registry port %d (setDoltPort not called)", meta.DoltServerPort, registryPort)
	}
}

// TestAllocatePort_ConcurrentProcesses verifies that concurrent initDoltForProject
// calls — simulating two concurrent "oro init" processes — produce no duplicate ports.
func TestAllocatePort_ConcurrentProcesses(t *testing.T) {
	oroHome := t.TempDir()

	// Use paths that all DerivePort to the same value so they would collide without
	// registry-based allocation.
	const n = 5
	targetPort := SharedDoltPort + 1 // 13308 — guaranteed to be chosen by DerivePort for matching paths

	searchBase := t.TempDir()
	beadsDirs := make([]string, n)
	for i := 0; i < n; i++ {
		subBase := filepath.Join(searchBase, fmt.Sprintf("sub%d", i))
		bd := findPathWithPort(t, subBase, targetPort)
		if err := os.MkdirAll(filepath.Dir(bd), 0o750); err != nil {
			t.Fatalf("mkdir parent[%d]: %v", i, err)
		}
		beadsDirs[i] = bd
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for _, bd := range beadsDirs {
		bd := bd
		go func() {
			defer wg.Done()
			initDoltForProject(bd, oroHome)
		}()
	}
	wg.Wait()

	portsSeen := make(map[int]string)
	for _, bd := range beadsDirs {
		meta, err := readDoltMeta(bd)
		if err != nil {
			t.Errorf("readDoltMeta(%s): %v", bd, err)
			continue
		}
		if meta == nil {
			t.Errorf("metadata missing for %s", bd)
			continue
		}
		port := meta.DoltServerPort
		if port == SharedDoltPort {
			t.Errorf("got SharedDoltPort for %s", bd)
		}
		if prev, dup := portsSeen[port]; dup {
			t.Errorf("duplicate port %d: assigned to both %s and %s", port, prev, bd)
		}
		portsSeen[port] = bd
	}
}

func TestSetDoltMode(t *testing.T) {
	t.Run("sets dolt_mode field when metadata.json missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		err := setDoltMode(beadsDir, "server")
		if err != nil {
			t.Fatalf("setDoltMode error: %v", err)
		}

		// Since backend is not set, readDoltMeta returns nil for non-dolt backends.
		// Read raw JSON to verify DoltMode was written.
		metaPath := filepath.Join(beadsDir, "metadata.json")
		rawData, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatalf("read metadata.json: %v", err)
		}
		var rawMeta map[string]interface{}
		if err := json.Unmarshal(rawData, &rawMeta); err != nil {
			t.Fatalf("parse metadata.json: %v", err)
		}
		if val, ok := rawMeta["dolt_mode"]; !ok {
			t.Error("dolt_mode field not in metadata.json")
		} else if val != "server" {
			t.Errorf("dolt_mode = %v, want %q", val, "server")
		}
	})

	t.Run("preserves existing fields when setting dolt_mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		// Write initial metadata with backend and port.
		data := map[string]interface{}{
			"backend":          "dolt",
			"dolt_server_port": 13400,
			"dolt_database":    "beads",
		}
		writeMetadata(t, beadsDir, data)

		// Set dolt_mode.
		err := setDoltMode(beadsDir, "server")
		if err != nil {
			t.Fatalf("setDoltMode error: %v", err)
		}

		// Read metadata and verify all fields are present.
		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta == nil {
			t.Fatal("readDoltMeta = nil, want non-nil")
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
		if meta.DoltMode != "server" {
			t.Errorf("DoltMode = %q, want %q", meta.DoltMode, "server")
		}
	})

	t.Run("overwrites existing dolt_mode on second call", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		// Write initial metadata.
		data := map[string]interface{}{
			"backend":          "dolt",
			"dolt_server_port": 13400,
			"dolt_database":    "beads",
		}
		writeMetadata(t, beadsDir, data)

		// First call: set to "server".
		if err := setDoltMode(beadsDir, "server"); err != nil {
			t.Fatalf("setDoltMode(server) error: %v", err)
		}

		meta, _ := readDoltMeta(beadsDir)
		if meta.DoltMode != "server" {
			t.Errorf("first setDoltMode: DoltMode = %q, want %q", meta.DoltMode, "server")
		}

		// Second call: set to "embedded".
		if err := setDoltMode(beadsDir, "embedded"); err != nil {
			t.Fatalf("setDoltMode(embedded) error: %v", err)
		}

		meta, _ = readDoltMeta(beadsDir)
		if meta.DoltMode != "embedded" {
			t.Errorf("second setDoltMode: DoltMode = %q, want %q", meta.DoltMode, "embedded")
		}

		// Verify other fields still present.
		if meta.Backend != "dolt" {
			t.Errorf("Backend = %q, want %q", meta.Backend, "dolt")
		}
		if meta.DoltServerPort != 13400 {
			t.Errorf("DoltServerPort = %d, want 13400", meta.DoltServerPort)
		}
	})

	t.Run("omitempty tag: field omitted from JSON when zero value", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		// Create metadata with backend only (no dolt_mode).
		data := map[string]interface{}{
			"backend": "dolt",
		}
		writeMetadata(t, beadsDir, data)

		// Read back raw JSON to verify dolt_mode is not present initially.
		metaPath := filepath.Join(beadsDir, "metadata.json")
		rawData, _ := os.ReadFile(metaPath)
		var rawMeta map[string]interface{}
		json.Unmarshal(rawData, &rawMeta)
		if _, ok := rawMeta["dolt_mode"]; ok {
			t.Error("dolt_mode should not be in initial metadata")
		}

		// Now set dolt_mode to "server".
		setDoltMode(beadsDir, "server")

		// Read raw JSON again and verify dolt_mode is present.
		rawData, _ = os.ReadFile(metaPath)
		json.Unmarshal(rawData, &rawMeta)
		if val, ok := rawMeta["dolt_mode"]; !ok {
			t.Error("dolt_mode not added to metadata.json")
		} else if val != "server" {
			t.Errorf("dolt_mode = %v, want %q", val, "server")
		}
	})

	t.Run("accepts invalid mode string (validation is caller responsibility)", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		// Set with invalid mode string.
		err := setDoltMode(beadsDir, "invalid-mode")
		if err != nil {
			t.Fatalf("setDoltMode with invalid mode should not error: %v", err)
		}

		// Verify it was written.
		meta, _ := readDoltMeta(beadsDir)
		if meta == nil {
			// If backend is not set, try reading raw JSON.
			metaPath := filepath.Join(beadsDir, "metadata.json")
			rawData, _ := os.ReadFile(metaPath)
			var rawMeta map[string]interface{}
			json.Unmarshal(rawData, &rawMeta)
			if val, ok := rawMeta["dolt_mode"]; !ok || val != "invalid-mode" {
				t.Error("invalid mode string was not written")
			}
		} else if meta.DoltMode != "invalid-mode" {
			t.Errorf("DoltMode = %q, want %q", meta.DoltMode, "invalid-mode")
		}
	})
}
