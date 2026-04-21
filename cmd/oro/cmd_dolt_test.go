package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// helpers for injecting a no-op runLaunchctl in tests that exercise code paths
// that call uninstallLaunchAgent or installLaunchAgent indirectly.
func withNoopLaunchctl(t *testing.T) {
	t.Helper()
	orig := runLaunchctl
	runLaunchctl = func(_ ...string) error { return nil }
	t.Cleanup(func() { runLaunchctl = orig })
}

// ---------- discoverBreadsDirs ----------

func TestDiscoverBreadsDirsFromProjectRoot(t *testing.T) {
	t.Run("happy path: reads project.root and derives beads dir", func(t *testing.T) {
		// Create a mock project root with .beads directory.
		projectRoot := t.TempDir()
		beadsDir := filepath.Join(projectRoot, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir beads dir: %v", err)
		}

		// Create oroHome with projects directory and a registered project.
		oroHome := t.TempDir()
		projectsDir := filepath.Join(oroHome, "projects")
		projectDir := filepath.Join(projectsDir, "my-project")
		if err := os.MkdirAll(projectDir, 0o750); err != nil {
			t.Fatalf("mkdir project dir: %v", err)
		}

		// Write project.root file pointing to the project root.
		projectRootFile := filepath.Join(projectDir, "project.root")
		if err := os.WriteFile(projectRootFile, []byte(projectRoot), 0o644); err != nil {
			t.Fatalf("write project.root: %v", err)
		}

		// Call discoverBreadsDirs and expect it to return the beads dir.
		dirs := discoverBreadsDirs(oroHome)
		if len(dirs) != 1 {
			t.Fatalf("expected 1 beads dir, got %d", len(dirs))
		}
		if dirs[0] != beadsDir {
			t.Errorf("beads dir = %s, want %s", dirs[0], beadsDir)
		}
	})

	t.Run("edge: project.root missing → skip project", func(t *testing.T) {
		oroHome := t.TempDir()
		projectsDir := filepath.Join(oroHome, "projects")
		projectDir := filepath.Join(projectsDir, "my-project")
		if err := os.MkdirAll(projectDir, 0o750); err != nil {
			t.Fatalf("mkdir project dir: %v", err)
		}

		// No project.root file.
		dirs := discoverBreadsDirs(oroHome)
		if len(dirs) != 0 {
			t.Errorf("expected no beads dirs (project.root missing), got %d", len(dirs))
		}
	})

	t.Run("edge: project.root points to non-existent dir → skip", func(t *testing.T) {
		oroHome := t.TempDir()
		projectsDir := filepath.Join(oroHome, "projects")
		projectDir := filepath.Join(projectsDir, "my-project")
		if err := os.MkdirAll(projectDir, 0o750); err != nil {
			t.Fatalf("mkdir project dir: %v", err)
		}

		// Write project.root pointing to a non-existent directory.
		projectRootFile := filepath.Join(projectDir, "project.root")
		nonExistent := filepath.Join(t.TempDir(), "no-such-dir", "project-root")
		if err := os.WriteFile(projectRootFile, []byte(nonExistent), 0o644); err != nil {
			t.Fatalf("write project.root: %v", err)
		}

		dirs := discoverBreadsDirs(oroHome)
		if len(dirs) != 0 {
			t.Errorf("expected no beads dirs (project root does not exist), got %d", len(dirs))
		}
	})

	t.Run("edge: empty projects dir → return nil", func(t *testing.T) {
		oroHome := t.TempDir()
		// Don't create any projects.
		dirs := discoverBreadsDirs(oroHome)
		if dirs != nil {
			t.Errorf("expected nil (no projects), got %v", dirs)
		}
	})

	t.Run("multiple projects with valid beads dirs", func(t *testing.T) {
		oroHome := t.TempDir()
		projectsDir := filepath.Join(oroHome, "projects")

		var expectedDirs []string
		for _, projName := range []string{"proj-a", "proj-b"} {
			projectRoot := t.TempDir()
			beadsDir := filepath.Join(projectRoot, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatalf("mkdir beads dir: %v", err)
			}
			expectedDirs = append(expectedDirs, beadsDir)

			projectDir := filepath.Join(projectsDir, projName)
			if err := os.MkdirAll(projectDir, 0o750); err != nil {
				t.Fatalf("mkdir project dir: %v", err)
			}

			projectRootFile := filepath.Join(projectDir, "project.root")
			if err := os.WriteFile(projectRootFile, []byte(projectRoot), 0o644); err != nil {
				t.Fatalf("write project.root: %v", err)
			}
		}

		dirs := discoverBreadsDirs(oroHome)
		if len(dirs) != len(expectedDirs) {
			t.Fatalf("expected %d beads dirs, got %d", len(expectedDirs), len(dirs))
		}
		for i, dir := range dirs {
			if dir != expectedDirs[i] {
				t.Errorf("beads dir %d = %s, want %s", i, dir, expectedDirs[i])
			}
		}
	})
}

func TestDiscoverBreadsDirsDeduplicates(t *testing.T) {
	t.Run("multiple project entries pointing to same root yield one beads dir", func(t *testing.T) {
		projectRoot := t.TempDir()
		beadsDir := filepath.Join(projectRoot, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir beads dir: %v", err)
		}

		oroHome := t.TempDir()
		projectsDir := filepath.Join(oroHome, "projects")

		// Register the same project root under two different project names.
		for _, projName := range []string{"oro", "s-0cfd1c96cbc0c0e0"} {
			projectDir := filepath.Join(projectsDir, projName)
			if err := os.MkdirAll(projectDir, 0o750); err != nil {
				t.Fatalf("mkdir project dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(projectDir, "project.root"), []byte(projectRoot), 0o644); err != nil {
				t.Fatalf("write project.root: %v", err)
			}
		}

		dirs := discoverBreadsDirs(oroHome)
		if len(dirs) != 1 {
			t.Errorf("expected 1 unique beads dir, got %d: %v", len(dirs), dirs)
		}
	})
}

func TestCheckSharedPortConflict(t *testing.T) {
	t.Run("errors when PID file is stale and no process on port", func(t *testing.T) {
		oroHome := t.TempDir()
		pidPath := filepath.Join(oroHome, "dolt-server.pid")

		// Write a stale PID.
		if err := os.WriteFile(pidPath, []byte("99999999"), 0o600); err != nil {
			t.Fatalf("write stale PID: %v", err)
		}

		// SharedDoltPort (13307) should not have anything listening in test env.
		// runIdentityProbe → resolvePID → findPIDFn (lsof) → fails → "cannot identify dolt owner".
		err := checkSharedPortConflict(oroHome)
		if err == nil {
			// If 13307 happens to be in use, skip.
			t.Skip("port 13307 is in use in test environment")
		}
		if !strings.Contains(err.Error(), "cannot identify dolt owner") &&
			!strings.Contains(err.Error(), "process_data_dir_mismatch") {
			t.Errorf("expected identity probe error, got: %v", err)
		}
	})
}

// ---------- oro dolt setup ----------

func TestDoltSetup(t *testing.T) {
	t.Run("happy path: creates shared dolt dir, migrates DB, updates metadata, installs plist", func(t *testing.T) {
		oroHome := t.TempDir()
		homeDir := t.TempDir()

		projectDir := t.TempDir()
		beadsDir := filepath.Join(projectDir, ".beads")
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads"), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "dolt", "beads", "test.db"), []byte("fake-dolt-data"), 0o600); err != nil {
			t.Fatalf("write test.db: %v", err)
		}
		writeMetadata(t, beadsDir, map[string]any{
			"backend":          "dolt",
			"dolt_server_port": 13350,
			"dolt_database":    "beads",
		})

		var plistInstalled bool
		cfg := &doltSetupConfig{
			oroHome:         oroHome,
			homeDir:         homeDir,
			beadsDirs:       []string{beadsDir},
			aliveFn:         func(int) bool { return false },
			dispatcherPIDFn: func() int { return 0 },
			startFn: func(home string) (int, error) {
				pidPath := filepath.Join(home, "dolt-server.pid")
				portPath := filepath.Join(home, "dolt-server.port")
				if err := os.WriteFile(pidPath, []byte("42"), 0o600); err != nil {
					return 0, err
				}
				if err := os.WriteFile(portPath, []byte("13307"), 0o600); err != nil {
					return 0, err
				}
				return 42, nil
			},
			generatePlistFn: func(_, _ string, _ int) ([]byte, error) {
				return []byte("<?xml version=\"1.0\"?><plist>fake</plist>"), nil
			},
			installPlistFn: func(data []byte, hd string) error {
				plistInstalled = true
				dir := filepath.Join(hd, "Library", "LaunchAgents")
				_ = os.MkdirAll(dir, 0o750)
				return os.WriteFile(filepath.Join(dir, "dev.getoro.dolt.plist"), data, 0o600)
			},
		}

		var buf bytes.Buffer
		if err := runDoltSetup(cfg, &buf); err != nil {
			t.Fatalf("runDoltSetup error: %v", err)
		}

		doltDir := filepath.Join(oroHome, "dolt")
		if _, err := os.Stat(doltDir); err != nil {
			t.Errorf("shared dolt dir not created: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(doltDir, "beads", "test.db"))
		if err != nil {
			t.Fatalf("database not copied to shared dir: %v", err)
		}
		if string(data) != "fake-dolt-data" {
			t.Errorf("copied data = %q, want %q", string(data), "fake-dolt-data")
		}

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta error: %v", err)
		}
		if meta == nil || meta.DoltServerPort != SharedDoltPort {
			port := 0
			if meta != nil {
				port = meta.DoltServerPort
			}
			t.Errorf("metadata port = %d, want %d (shared)", port, SharedDoltPort)
		}

		if _, err := os.Stat(filepath.Join(oroHome, "dolt-server.pid")); err != nil {
			t.Errorf("PID file not written to oroHome: %v", err)
		}
		if _, err := os.Stat(filepath.Join(oroHome, "dolt-server.port")); err != nil {
			t.Errorf("port file not written to oroHome: %v", err)
		}

		if !plistInstalled {
			t.Error("plist should have been installed")
		}
		if _, err := os.Stat(launchAgentPlistPath(homeDir)); err != nil {
			t.Errorf("plist file not found: %v", err)
		}
	})

	t.Run("aborts when dispatcher is running", func(t *testing.T) {
		cfg := &doltSetupConfig{
			oroHome:         t.TempDir(),
			homeDir:         t.TempDir(),
			beadsDirs:       []string{},
			aliveFn:         func(int) bool { return true },
			dispatcherPIDFn: func() int { return 5678 },
		}

		var buf bytes.Buffer
		err := runDoltSetup(cfg, &buf)
		if err == nil {
			t.Fatal("runDoltSetup should return error when dispatcher is running")
		}
		if !strings.Contains(err.Error(), "dispatcher") {
			t.Errorf("error should mention dispatcher, got: %v", err)
		}
	})

	t.Run("no-op when no dolt projects found", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeMetadata(t, beadsDir, map[string]any{
			"backend":  "sqlite",
			"database": "issues.db",
		})

		cfg := &doltSetupConfig{
			oroHome:         t.TempDir(),
			homeDir:         t.TempDir(),
			beadsDirs:       []string{beadsDir},
			dispatcherPIDFn: func() int { return 0 },
		}

		var buf bytes.Buffer
		if err := runDoltSetup(cfg, &buf); err != nil {
			t.Fatalf("runDoltSetup error: %v", err)
		}
		if !strings.Contains(buf.String(), "no dolt projects") {
			t.Errorf("should report 'no dolt projects', got: %s", buf.String())
		}
	})

	t.Run("error on database name collision", func(t *testing.T) {
		beads1 := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beads1, 0o750); err != nil {
			t.Fatalf("mkdir beads1: %v", err)
		}
		writeMetadata(t, beads1, map[string]any{
			"backend": "dolt", "dolt_server_port": 13350, "dolt_database": "beads",
		})

		beads2 := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beads2, 0o750); err != nil {
			t.Fatalf("mkdir beads2: %v", err)
		}
		writeMetadata(t, beads2, map[string]any{
			"backend": "dolt", "dolt_server_port": 13351, "dolt_database": "beads",
		})

		cfg := &doltSetupConfig{
			oroHome:         t.TempDir(),
			homeDir:         t.TempDir(),
			beadsDirs:       []string{beads1, beads2},
			dispatcherPIDFn: func() int { return 0 },
		}

		var buf bytes.Buffer
		err := runDoltSetup(cfg, &buf)
		if err == nil {
			t.Fatal("runDoltSetup should return error on DB name collision")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "collision") && !strings.Contains(err.Error(), "already used") {
			t.Errorf("error should mention collision, got: %v", err)
		}
	})

	t.Run("cleans up stale temp dir on retry after partial migration", func(t *testing.T) {
		oroHome := t.TempDir()
		homeDir := t.TempDir()

		doltDir := filepath.Join(oroHome, "dolt")
		if err := os.MkdirAll(doltDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		staleTmpDir := filepath.Join(doltDir, "beads.doltsetup-tmp")
		if err := os.MkdirAll(staleTmpDir, 0o750); err != nil {
			t.Fatalf("mkdir stale tmp: %v", err)
		}
		if err := os.WriteFile(filepath.Join(staleTmpDir, "stale.db"), []byte("stale"), 0o600); err != nil {
			t.Fatalf("write stale.db: %v", err)
		}

		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads"), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "dolt", "beads", "data.db"), []byte("fresh"), 0o600); err != nil {
			t.Fatalf("write data.db: %v", err)
		}
		writeMetadata(t, beadsDir, map[string]any{
			"backend": "dolt", "dolt_server_port": 13350, "dolt_database": "beads",
		})

		cfg := &doltSetupConfig{
			oroHome:         oroHome,
			homeDir:         homeDir,
			beadsDirs:       []string{beadsDir},
			aliveFn:         func(int) bool { return false },
			dispatcherPIDFn: func() int { return 0 },
			startFn:         func(string) (int, error) { return 42, nil },
			generatePlistFn: func(_, _ string, _ int) ([]byte, error) {
				return []byte("<?xml version=\"1.0\"?><plist/>"), nil
			},
			installPlistFn: func(_ []byte, _ string) error {
				return nil
			},
		}

		var buf bytes.Buffer
		if err := runDoltSetup(cfg, &buf); err != nil {
			t.Fatalf("runDoltSetup error: %v", err)
		}

		if _, err := os.Stat(staleTmpDir); !errors.Is(err, os.ErrNotExist) {
			t.Error("stale temp dir should have been cleaned up")
		}

		copiedData, err := os.ReadFile(filepath.Join(doltDir, "beads", "data.db"))
		if err != nil {
			t.Fatalf("copied data.db not found: %v", err)
		}
		if string(copiedData) != "fresh" {
			t.Errorf("copied data.db = %q, want %q", string(copiedData), "fresh")
		}
	})
}

// ---------- oro dolt status ----------

func TestDoltStatus_SharedServerRunning(t *testing.T) {
	// Simulate a running shared server with PID file.
	oroHome := t.TempDir()
	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	portPath := filepath.Join(oroHome, "dolt-server.port")

	// Start a real listener on a free port (we'll override SharedDoltPort check).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portPath, []byte(strconv.Itoa(port)), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &doltCmdConfig{
		oroHome:  oroHome,
		aliveFn:  func(int) bool { return true },
		isPortUp: func(int) bool { return true },
	}

	var buf bytes.Buffer
	err = runDoltStatus(cfg, &buf)
	if err != nil {
		t.Fatalf("runDoltStatus error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "running") {
		t.Errorf("status should show running, got: %s", out)
	}
	if !strings.Contains(out, strconv.Itoa(os.Getpid())) {
		t.Errorf("status should show PID, got: %s", out)
	}
}

func TestDoltStatus_SharedServerStopped(t *testing.T) {
	oroHome := t.TempDir()
	cfg := &doltCmdConfig{
		oroHome:  oroHome,
		aliveFn:  func(int) bool { return false },
		isPortUp: func(int) bool { return false },
	}

	var buf bytes.Buffer
	err := runDoltStatus(cfg, &buf)
	if err != nil {
		t.Fatalf("runDoltStatus error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "stopped") {
		t.Errorf("status should show stopped when not running, got: %s", out)
	}
}

func TestDoltStatus_ShowsDatabaseList(t *testing.T) {
	oroHome := t.TempDir()
	doltDir := filepath.Join(oroHome, "dolt")

	// Create fake database directories inside dolt data dir.
	for _, db := range []string{"beads", "project-alpha"} {
		if err := os.MkdirAll(filepath.Join(doltDir, db), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	portPath := filepath.Join(oroHome, "dolt-server.port")
	if err := os.WriteFile(portPath, []byte("13307"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &doltCmdConfig{
		oroHome:  oroHome,
		aliveFn:  func(int) bool { return true },
		isPortUp: func(int) bool { return true },
	}

	var buf bytes.Buffer
	err := runDoltStatus(cfg, &buf)
	if err != nil {
		t.Fatalf("runDoltStatus error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "beads") {
		t.Errorf("status should list 'beads' database, got: %s", out)
	}
	if !strings.Contains(out, "project-alpha") {
		t.Errorf("status should list 'project-alpha' database, got: %s", out)
	}
}

// ---------- oro dolt start ----------

func TestDoltStart_Idempotent(t *testing.T) {
	// If server already running, start should be a no-op success.
	oroHome := t.TempDir()

	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	portPath := filepath.Join(oroHome, "dolt-server.port")
	if err := os.WriteFile(portPath, []byte("13307"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &doltCmdConfig{
		oroHome:  oroHome,
		aliveFn:  func(int) bool { return true },
		isPortUp: func(int) bool { return true },
		startFn:  func(string) (int, error) { return 0, nil },
	}

	var buf bytes.Buffer
	err := runDoltStart(cfg, &buf)
	if err != nil {
		t.Fatalf("runDoltStart error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "already running") {
		t.Errorf("start should report 'already running', got: %s", out)
	}
}

func TestDoltStart_StartsWhenStopped(t *testing.T) {
	oroHome := t.TempDir()
	started := false

	cfg := &doltCmdConfig{
		oroHome:  oroHome,
		aliveFn:  func(int) bool { return false },
		isPortUp: func(int) bool { return false },
		startFn: func(home string) (int, error) {
			started = true
			return 12345, nil
		},
	}

	var buf bytes.Buffer
	err := runDoltStart(cfg, &buf)
	if err != nil {
		t.Fatalf("runDoltStart error: %v", err)
	}

	if !started {
		t.Error("startFn should have been called")
	}
	out := buf.String()
	if !strings.Contains(out, "12345") {
		t.Errorf("start should show PID, got: %s", out)
	}
}

func TestDoltStart_DoltNotInPath(t *testing.T) {
	oroHome := t.TempDir()

	cfg := &doltCmdConfig{
		oroHome:  oroHome,
		aliveFn:  func(int) bool { return false },
		isPortUp: func(int) bool { return false },
		startFn: func(home string) (int, error) {
			return 0, exec.ErrNotFound
		},
	}

	var buf bytes.Buffer
	err := runDoltStart(cfg, &buf)
	if err == nil {
		t.Fatal("runDoltStart should return error when dolt not found")
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("error should be ErrNotFound, got: %v", err)
	}
}

// ---------- oro dolt stop ----------

func TestDoltStop_RefusesWithoutForce_WhenDispatcherRunning(t *testing.T) {
	oroHome := t.TempDir()

	cfg := &doltCmdConfig{
		oroHome:         oroHome,
		aliveFn:         func(int) bool { return true },
		isPortUp:        func(int) bool { return true },
		force:           false,
		dispatcherPIDFn: func() int { return 999 }, // dispatcher is running
		stopFn:          func(string) error { return nil },
	}

	var buf bytes.Buffer
	err := runDoltStop(cfg, &buf)
	if err == nil {
		t.Fatal("runDoltStop should refuse without --force when dispatcher is running")
	}
	if !strings.Contains(err.Error(), "force") {
		t.Errorf("error should mention --force, got: %v", err)
	}
}

func TestDoltStop_SucceedsWithForce_WhenDispatcherRunning(t *testing.T) {
	oroHome := t.TempDir()
	stopped := false

	cfg := &doltCmdConfig{
		oroHome:         oroHome,
		aliveFn:         func(int) bool { return true },
		isPortUp:        func(int) bool { return true },
		force:           true,
		dispatcherPIDFn: func() int { return 999 },
		stopFn: func(home string) error {
			stopped = true
			return nil
		},
	}

	var buf bytes.Buffer
	err := runDoltStop(cfg, &buf)
	if err != nil {
		t.Fatalf("runDoltStop error: %v", err)
	}
	if !stopped {
		t.Error("stopFn should have been called with --force")
	}
}

func TestDoltStop_SucceedsWithoutForce_WhenNoDispatcher(t *testing.T) {
	oroHome := t.TempDir()
	stopped := false

	// Write PID/port so there's something to stop.
	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	portPath := filepath.Join(oroHome, "dolt-server.port")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portPath, []byte("13307"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &doltCmdConfig{
		oroHome:         oroHome,
		aliveFn:         func(int) bool { return true },
		isPortUp:        func(int) bool { return true },
		force:           false,
		dispatcherPIDFn: func() int { return 0 }, // no dispatcher
		stopFn: func(home string) error {
			stopped = true
			return nil
		},
	}

	var buf bytes.Buffer
	err := runDoltStop(cfg, &buf)
	if err != nil {
		t.Fatalf("runDoltStop error: %v", err)
	}
	if !stopped {
		t.Error("stopFn should have been called when no dispatcher")
	}
}

func TestDoltStop_AlreadyStopped(t *testing.T) {
	oroHome := t.TempDir()

	cfg := &doltCmdConfig{
		oroHome:         oroHome,
		aliveFn:         func(int) bool { return false },
		isPortUp:        func(int) bool { return false },
		force:           false,
		dispatcherPIDFn: func() int { return 0 },
		stopFn:          func(string) error { return nil },
	}

	var buf bytes.Buffer
	err := runDoltStop(cfg, &buf)
	if err != nil {
		t.Fatalf("runDoltStop error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "not running") {
		t.Errorf("should say not running, got: %s", out)
	}
}

// ---------- killOrphanDoltServers ----------

func TestDoltSetup_KillsOrphanPerProjectServers(t *testing.T) {
	t.Run("orphan per-project server killed before shared server starts", func(t *testing.T) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads"), 0o750); err != nil {
			t.Fatal(err)
		}
		writeMetadata(t, beadsDir, map[string]any{
			"backend": "dolt", "dolt_server_port": 13350, "dolt_database": "beads",
		})

		var seq int
		var killOrder, startOrder int

		cfg := &doltSetupConfig{
			oroHome:         t.TempDir(),
			homeDir:         t.TempDir(),
			beadsDirs:       []string{beadsDir},
			aliveFn:         func(int) bool { return false },
			dispatcherPIDFn: func() int { return 0 },
			killOrphansFn: func(_ []doltProject, _ io.Writer) {
				seq++
				killOrder = seq
			},
			startFn: func(string) (int, error) {
				seq++
				startOrder = seq
				return 42, nil
			},
			generatePlistFn: func(_, _ string, _ int) ([]byte, error) {
				return []byte("<plist/>"), nil
			},
			installPlistFn: func([]byte, string) error { return nil },
		}

		var buf bytes.Buffer
		if err := runDoltSetup(cfg, &buf); err != nil {
			t.Fatalf("runDoltSetup error: %v", err)
		}
		if killOrder == 0 {
			t.Error("killOrphansFn was not called")
		}
		if startOrder == 0 {
			t.Error("startFn was not called")
		}
		if killOrder >= startOrder {
			t.Errorf("killOrphansFn (order=%d) must be called before startFn (order=%d)", killOrder, startOrder)
		}
	})

	t.Run("process on SharedDoltPort is not killed", func(t *testing.T) {
		killed := false
		projects := []doltProject{{beadsDir: "/fake/dir", port: SharedDoltPort, dbName: "beads"}}
		killOrphanServersImpl(projects, &bytes.Buffer{},
			func(int) bool { return true },
			func(int) bool { return true },
			func(int) (int, error) { return 99, nil },
			func(int, string) error { killed = true; return nil },
		)
		if killed {
			t.Error("process on SharedDoltPort should not be killed")
		}
	})

	t.Run("no-op when no server running on per-project port", func(t *testing.T) {
		killed := false
		beadsDir := t.TempDir()
		projects := []doltProject{{beadsDir: beadsDir, port: 13350, dbName: "beads"}}
		killOrphanServersImpl(projects, &bytes.Buffer{},
			func(int) bool { return false },
			func(int) bool { return false },
			func(int) (int, error) { return 0, nil },
			func(int, string) error { killed = true; return nil },
		)
		if killed {
			t.Error("should be no-op when no server is running")
		}
	})

	t.Run("warning when lsof not available", func(t *testing.T) {
		beadsDir := t.TempDir()
		projects := []doltProject{{beadsDir: beadsDir, port: 13350, dbName: "beads"}}
		var buf bytes.Buffer
		killOrphanServersImpl(projects, &buf,
			func(int) bool { return false },
			func(int) bool { return true }, // port IS up
			func(int) (int, error) { return 0, exec.ErrNotFound },
			func(int, string) error { return nil },
		)
		if !strings.Contains(buf.String(), "warning") || !strings.Contains(buf.String(), "lsof") {
			t.Errorf("should warn about lsof unavailability, got: %s", buf.String())
		}
	})

	t.Run("kills via PID file when process alive", func(t *testing.T) {
		beadsDir := t.TempDir()
		const fakePID = 12345
		if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.pid"), []byte(strconv.Itoa(fakePID)), 0o600); err != nil {
			t.Fatal(err)
		}
		killed := false
		projects := []doltProject{{beadsDir: beadsDir, port: 13350, dbName: "beads"}}
		killOrphanServersImpl(projects, &bytes.Buffer{},
			func(pid int) bool { return pid == fakePID },
			func(int) bool { return false },
			func(int) (int, error) { return 0, nil },
			func(int, string) error { killed = true; return nil },
		)
		if !killed {
			t.Error("should have killed process from PID file")
		}
	})
}

// ---------- oro dolt teardown ----------

func TestDoltTeardown(t *testing.T) {
	t.Run("happy path: stops server, uninstalls plist, copies DBs back to per-project dirs", func(t *testing.T) {
		oroHome := t.TempDir()
		homeDir := t.TempDir()

		// Write PID/port files so server appears running.
		if err := os.WriteFile(filepath.Join(oroHome, "dolt-server.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroHome, "dolt-server.port"), []byte("13307"), 0o600); err != nil {
			t.Fatal(err)
		}

		// Set up shared dolt dir with database.
		const dbName = "beads"
		sharedDBDir := filepath.Join(oroHome, "dolt", dbName)
		if err := os.MkdirAll(sharedDBDir, 0o750); err != nil {
			t.Fatalf("mkdir shared dolt: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sharedDBDir, "data.db"), []byte("shared-db-data"), 0o600); err != nil {
			t.Fatalf("write data.db: %v", err)
		}

		// Install a plist so we can verify it's removed.
		if err := os.MkdirAll(launchAgentsDir(homeDir), 0o750); err != nil {
			t.Fatalf("mkdir LaunchAgents: %v", err)
		}
		if err := os.WriteFile(launchAgentPlistPath(homeDir), []byte("fake-plist"), 0o600); err != nil {
			t.Fatalf("write plist: %v", err)
		}

		// Per-project beads dir pointing at shared server.
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir beadsDir: %v", err)
		}
		writeMetadata(t, beadsDir, map[string]any{
			"backend": "dolt", "dolt_server_port": SharedDoltPort, "dolt_database": dbName,
		})

		stopped := false
		cfg := &doltCmdConfig{
			oroHome:         oroHome,
			aliveFn:         func(int) bool { return true },
			isPortUp:        func(int) bool { return true },
			force:           false,
			dispatcherPIDFn: func() int { return 0 },
			stopFn:          func(string) error { stopped = true; return nil },
			beadsDirs:       []string{beadsDir},
		}

		var buf bytes.Buffer
		if err := runDoltTeardown(cfg, homeDir, &buf); err != nil {
			t.Fatalf("runDoltTeardown error: %v", err)
		}

		if !stopped {
			t.Error("stopFn should have been called")
		}
		if _, err := os.Stat(launchAgentPlistPath(homeDir)); !errors.Is(err, os.ErrNotExist) {
			t.Error("plist should have been removed")
		}

		copiedData, err := os.ReadFile(filepath.Join(beadsDir, "dolt", dbName, "data.db"))
		if err != nil {
			t.Fatalf("DB not copied back to per-project dir: %v", err)
		}
		if string(copiedData) != "shared-db-data" {
			t.Errorf("copied data = %q, want %q", string(copiedData), "shared-db-data")
		}

		meta, err := readDoltMeta(beadsDir)
		if err != nil {
			t.Fatalf("readDoltMeta: %v", err)
		}
		if meta == nil { //nolint:staticcheck // checked above
			t.Fatal("metadata should exist after teardown")
		}
		if meta.DoltServerPort == SharedDoltPort {
			t.Errorf("metadata should have per-project port, not shared port %d", SharedDoltPort)
		}

		if !strings.Contains(buf.String(), "teardown complete") {
			t.Errorf("should report teardown complete, got: %s", buf.String())
		}
	})

	t.Run("edge: .beads/dolt already exists → skip copy, warn", func(t *testing.T) {
		oroHome := t.TempDir()
		homeDir := t.TempDir()

		const dbName = "beads"
		sharedDBDir := filepath.Join(oroHome, "dolt", dbName)
		if err := os.MkdirAll(sharedDBDir, 0o750); err != nil {
			t.Fatalf("mkdir shared dolt: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sharedDBDir, "shared.db"), []byte("shared-data"), 0o600); err != nil {
			t.Fatalf("write shared.db: %v", err)
		}

		// Per-project beads dir that already has a local dolt/<dbName> dir.
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		existingLocalDolt := filepath.Join(beadsDir, "dolt", dbName)
		if err := os.MkdirAll(existingLocalDolt, 0o750); err != nil {
			t.Fatalf("mkdir existing local dolt: %v", err)
		}
		if err := os.WriteFile(filepath.Join(existingLocalDolt, "existing.db"), []byte("existing-data"), 0o600); err != nil {
			t.Fatalf("write existing.db: %v", err)
		}
		writeMetadata(t, beadsDir, map[string]any{
			"backend": "dolt", "dolt_server_port": SharedDoltPort, "dolt_database": dbName,
		})

		cfg := &doltCmdConfig{
			oroHome:         oroHome,
			aliveFn:         func(int) bool { return false },
			isPortUp:        func(int) bool { return false },
			force:           false,
			dispatcherPIDFn: func() int { return 0 },
			stopFn:          func(string) error { return nil },
			beadsDirs:       []string{beadsDir},
		}

		var buf bytes.Buffer
		if err := runDoltTeardown(cfg, homeDir, &buf); err != nil {
			t.Fatalf("runDoltTeardown error: %v", err)
		}

		// Existing data must NOT be overwritten.
		existingData, err := os.ReadFile(filepath.Join(existingLocalDolt, "existing.db"))
		if err != nil {
			t.Fatalf("existing.db should still be there: %v", err)
		}
		if string(existingData) != "existing-data" {
			t.Errorf("existing data was overwritten: got %q", string(existingData))
		}

		out := buf.String()
		if !strings.Contains(strings.ToLower(out), "warning") &&
			!strings.Contains(strings.ToLower(out), "skip") &&
			!strings.Contains(strings.ToLower(out), "already") {
			t.Errorf("should warn about existing dir, got: %s", out)
		}
	})

	t.Run("help text mentions database copy-back", func(t *testing.T) {
		cmd := newDoltTeardownCmd()
		if !strings.Contains(cmd.Long, "copy databases back") {
			t.Errorf("Long help should mention copying databases back, got: %s", cmd.Long)
		}
		if !strings.Contains(cmd.Short, "restore per-project") {
			t.Errorf("Short help should mention restoring per-project databases, got: %s", cmd.Short)
		}

		parent := newDoltCmd()
		if !strings.Contains(parent.Long, "Copy databases back") {
			t.Errorf("parent Long help should mention database copy-back for teardown, got: %s", parent.Long)
		}
	})

	t.Run("edge: shared server not running → skip stop, still copies DBs back", func(t *testing.T) {
		oroHome := t.TempDir()
		homeDir := t.TempDir()

		const dbName = "beads"
		sharedDBDir := filepath.Join(oroHome, "dolt", dbName)
		if err := os.MkdirAll(sharedDBDir, 0o750); err != nil {
			t.Fatalf("mkdir shared dolt: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sharedDBDir, "data.db"), []byte("db-content"), 0o600); err != nil {
			t.Fatalf("write data.db: %v", err)
		}

		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("mkdir beadsDir: %v", err)
		}
		writeMetadata(t, beadsDir, map[string]any{
			"backend": "dolt", "dolt_server_port": SharedDoltPort, "dolt_database": dbName,
		})

		stopCalled := false
		cfg := &doltCmdConfig{
			oroHome:         oroHome,
			aliveFn:         func(int) bool { return false },
			isPortUp:        func(int) bool { return false },
			force:           false,
			dispatcherPIDFn: func() int { return 0 },
			stopFn:          func(string) error { stopCalled = true; return nil },
			beadsDirs:       []string{beadsDir},
		}

		var buf bytes.Buffer
		if err := runDoltTeardown(cfg, homeDir, &buf); err != nil {
			t.Fatalf("runDoltTeardown error: %v", err)
		}

		if stopCalled {
			t.Error("stopFn should not be called when server is not running")
		}

		// DB should still be copied back even though server wasn't running.
		if _, err := os.ReadFile(filepath.Join(beadsDir, "dolt", dbName, "data.db")); err != nil {
			t.Errorf("DB should be copied back even when server was not running: %v", err)
		}

		if !strings.Contains(buf.String(), "not running") {
			t.Errorf("should say 'not running', got: %s", buf.String())
		}
	})
}

// ---------- atomicCopyDir ----------

func TestAtomicCopyDir_SrcNotExists(t *testing.T) {
	destParent := t.TempDir()
	srcDir := filepath.Join(t.TempDir(), "nonexistent")

	if err := atomicCopyDir(srcDir, destParent, "mydb"); err != nil {
		t.Fatalf("expected nil when src doesn't exist, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destParent, "mydb")); !errors.Is(err, os.ErrNotExist) {
		t.Error("destination should not be created when source doesn't exist")
	}
}

// ---------- copyDirRecursive ----------

func TestCopyDirRecursive_UnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	secretFile := filepath.Join(src, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(secretFile, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(secretFile, 0o600) })

	if err := copyDirRecursive(src, dst); err == nil {
		t.Error("expected error when copying unreadable file")
	}
}

func TestCopyDirRecursive_UnwritableDest(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dstParent := t.TempDir()
	if err := os.Chmod(dstParent, 0o555); err != nil {
		t.Fatalf("chmod dstParent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dstParent, 0o755) })

	dst := filepath.Join(dstParent, "dst")
	if err := copyDirRecursive(src, dst); err == nil {
		t.Error("expected error when destination parent is read-only")
	}
}

// ---------- installDoltPlist error paths ----------

func TestInstallDoltPlist_GenerateFails(t *testing.T) {
	cfg := &doltSetupConfig{
		homeDir: t.TempDir(),
		generatePlistFn: func(_, _ string, _ int) ([]byte, error) {
			return nil, errors.New("generate failed")
		},
	}
	var buf bytes.Buffer
	if err := installDoltPlist(cfg, &buf); err != nil {
		t.Fatalf("expected nil when generate fails (warn+skip), got: %v", err)
	}
	if !strings.Contains(buf.String(), "warning") {
		t.Errorf("should print warning, got: %s", buf.String())
	}
}

func TestInstallDoltPlist_InstallFails(t *testing.T) {
	cfg := &doltSetupConfig{
		homeDir: t.TempDir(),
		generatePlistFn: func(_, _ string, _ int) ([]byte, error) {
			return []byte("<plist/>"), nil
		},
		installPlistFn: func([]byte, string) error {
			return errors.New("install failed")
		},
	}
	var buf bytes.Buffer
	if err := installDoltPlist(cfg, &buf); err == nil {
		t.Fatal("expected error when installPlistFn fails")
	}
}

// ---------- runDoltSetup error paths ----------

func TestRunDoltSetup_FindProjectsError(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Malformed JSON triggers readDoltMeta error → findDoltProjects error.
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{invalid"), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	cfg := &doltSetupConfig{
		oroHome:         t.TempDir(),
		homeDir:         t.TempDir(),
		beadsDirs:       []string{beadsDir},
		dispatcherPIDFn: func() int { return 0 },
	}
	var buf bytes.Buffer
	if err := runDoltSetup(cfg, &buf); err == nil {
		t.Fatal("expected error when metadata is malformed")
	}
}

func TestRunDoltSetup_MkdirAllError(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeMetadata(t, beadsDir, map[string]any{
		"backend": "dolt", "dolt_server_port": 13350, "dolt_database": "beads",
	})

	oroHome := t.TempDir()
	// Place a regular file where oroHome/dolt/ should be created.
	if err := os.WriteFile(filepath.Join(oroHome, "dolt"), []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cfg := &doltSetupConfig{
		oroHome:         oroHome,
		homeDir:         t.TempDir(),
		beadsDirs:       []string{beadsDir},
		dispatcherPIDFn: func() int { return 0 },
	}
	var buf bytes.Buffer
	if err := runDoltSetup(cfg, &buf); err == nil {
		t.Fatal("expected error when dolt dir cannot be created")
	}
}

func TestRunDoltSetup_MigrateError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	srcDir := filepath.Join(beadsDir, "dolt", "beads")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeMetadata(t, beadsDir, map[string]any{
		"backend": "dolt", "dolt_server_port": 13350, "dolt_database": "beads",
	})
	// Make the source dolt dir unreadable so copyDirRecursive fails.
	if err := os.Chmod(srcDir, 0o000); err != nil {
		t.Fatalf("chmod srcDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(srcDir, 0o750) })

	cfg := &doltSetupConfig{
		oroHome:         t.TempDir(),
		homeDir:         t.TempDir(),
		beadsDirs:       []string{beadsDir},
		aliveFn:         func(int) bool { return false },
		dispatcherPIDFn: func() int { return 0 },
		killOrphansFn:   func([]doltProject, io.Writer) {},
	}
	var buf bytes.Buffer
	if err := runDoltSetup(cfg, &buf); err == nil {
		t.Fatal("expected error when source directory is unreadable")
	}
}

func TestRunDoltSetup_InstallPlistError(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeMetadata(t, beadsDir, map[string]any{
		"backend": "dolt", "dolt_server_port": 13350, "dolt_database": "beads",
	})

	cfg := &doltSetupConfig{
		oroHome:         t.TempDir(),
		homeDir:         t.TempDir(),
		beadsDirs:       []string{beadsDir},
		aliveFn:         func(int) bool { return false },
		dispatcherPIDFn: func() int { return 0 },
		killOrphansFn:   func([]doltProject, io.Writer) {},
		generatePlistFn: func(_, _ string, _ int) ([]byte, error) { return []byte("<plist/>"), nil },
		installPlistFn:  func([]byte, string) error { return errors.New("install failed") },
	}
	var buf bytes.Buffer
	if err := runDoltSetup(cfg, &buf); err == nil {
		t.Fatal("expected error when plist install fails")
	}
}

func TestRunDoltSetup_StartFnError(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", "beads"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeMetadata(t, beadsDir, map[string]any{
		"backend": "dolt", "dolt_server_port": 13350, "dolt_database": "beads",
	})

	cfg := &doltSetupConfig{
		oroHome:         t.TempDir(),
		homeDir:         t.TempDir(),
		beadsDirs:       []string{beadsDir},
		aliveFn:         func(int) bool { return false },
		dispatcherPIDFn: func() int { return 0 },
		killOrphansFn:   func([]doltProject, io.Writer) {},
		generatePlistFn: func(_, _ string, _ int) ([]byte, error) { return []byte("<plist/>"), nil },
		installPlistFn:  func([]byte, string) error { return nil },
		startFn:         func(string) (int, error) { return 0, errors.New("start failed") },
	}
	var buf bytes.Buffer
	err := runDoltSetup(cfg, &buf)
	if err == nil {
		t.Fatal("expected error when startFn fails")
	}
	if !strings.Contains(err.Error(), "start") {
		t.Errorf("error should mention start, got: %v", err)
	}
}

// ---------- findDoltProjects ----------

func TestFindDoltProjects_MetaError(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Malformed JSON so readDoltMeta returns an error.
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{invalid"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := findDoltProjects([]string{beadsDir})
	if err == nil {
		t.Fatal("expected error from malformed metadata in findDoltProjects")
	}
}

// ---------- restorePerProjectDBs error paths ----------

func TestRestorePerProjectDBs_MetaError(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{bad json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &doltCmdConfig{
		oroHome:   t.TempDir(),
		beadsDirs: []string{beadsDir},
	}
	var buf bytes.Buffer
	if err := restorePerProjectDBs(cfg, &buf); err == nil {
		t.Fatal("expected error from malformed metadata in restorePerProjectDBs")
	}
}

func TestRestorePerProjectDBs_AtomicCopyError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	oroHome := t.TempDir()
	const dbName = "beads"

	// Create shared DB dir that will be made unreadable.
	sharedDBDir := filepath.Join(oroHome, "dolt", dbName)
	if err := os.MkdirAll(sharedDBDir, 0o750); err != nil {
		t.Fatalf("mkdir shared dolt: %v", err)
	}
	if err := os.Chmod(sharedDBDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sharedDBDir, 0o750) })

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeMetadata(t, beadsDir, map[string]any{
		"backend": "dolt", "dolt_server_port": SharedDoltPort, "dolt_database": dbName,
	})

	cfg := &doltCmdConfig{
		oroHome:   oroHome,
		beadsDirs: []string{beadsDir},
	}
	var buf bytes.Buffer
	if err := restorePerProjectDBs(cfg, &buf); err == nil {
		t.Fatal("expected error when shared DB directory is unreadable")
	}
}

// ---------- readSharedServerState edge cases ----------

func TestReadSharedServerState_InvalidPID(t *testing.T) {
	oroHome := t.TempDir()
	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	portPath := filepath.Join(oroHome, "dolt-server.port")

	if err := os.WriteFile(pidPath, []byte("not-a-number"), 0o600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	cfg := &doltCmdConfig{
		aliveFn:  func(int) bool { return true },
		isPortUp: func(int) bool { return true },
	}
	pid, port, running := readSharedServerState(cfg, pidPath, portPath)
	if pid != 0 || port != 0 || running {
		t.Errorf("invalid PID should return (0,0,false), got (%d,%d,%v)", pid, port, running)
	}
}

func TestReadSharedServerState_NoPortFile(t *testing.T) {
	oroHome := t.TempDir()
	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	portPath := filepath.Join(oroHome, "dolt-server.port") // intentionally absent

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	cfg := &doltCmdConfig{
		aliveFn:  func(int) bool { return true },
		isPortUp: func(int) bool { return true },
	}
	pid, port, running := readSharedServerState(cfg, pidPath, portPath)
	if pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", pid, os.Getpid())
	}
	if port != SharedDoltPort {
		t.Errorf("port = %d, want SharedDoltPort (%d) when port file missing", port, SharedDoltPort)
	}
	if !running {
		t.Error("running should be true when process is alive")
	}
}

func TestReadSharedServerState_InvalidPort(t *testing.T) {
	oroHome := t.TempDir()
	pidPath := filepath.Join(oroHome, "dolt-server.pid")
	portPath := filepath.Join(oroHome, "dolt-server.port")

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	if err := os.WriteFile(portPath, []byte("not-a-port"), 0o600); err != nil {
		t.Fatalf("write port: %v", err)
	}

	cfg := &doltCmdConfig{
		aliveFn:  func(int) bool { return true },
		isPortUp: func(int) bool { return true },
	}
	_, port, _ := readSharedServerState(cfg, pidPath, portPath)
	if port != SharedDoltPort {
		t.Errorf("invalid port file should fall back to SharedDoltPort (%d), got %d", SharedDoltPort, port)
	}
}

// ---------- runDoltStart edge cases ----------

func TestRunDoltStart_PidZeroAdopted(t *testing.T) {
	cfg := &doltCmdConfig{
		oroHome:  t.TempDir(),
		aliveFn:  func(int) bool { return false },
		isPortUp: func(int) bool { return false },
		startFn:  func(string) (int, error) { return 0, nil }, // pid=0 → adopted existing server
	}
	var buf bytes.Buffer
	if err := runDoltStart(cfg, &buf); err != nil {
		t.Fatalf("expected no error when adopted: %v", err)
	}
	if !strings.Contains(buf.String(), "already running") {
		t.Errorf("should report 'already running' for adopted server (pid=0), got: %s", buf.String())
	}
}

func TestRunDoltStart_GenericStartError(t *testing.T) {
	cfg := &doltCmdConfig{
		oroHome:  t.TempDir(),
		aliveFn:  func(int) bool { return false },
		isPortUp: func(int) bool { return false },
		startFn:  func(string) (int, error) { return 0, errors.New("generic start error") },
	}
	var buf bytes.Buffer
	err := runDoltStart(cfg, &buf)
	if err == nil {
		t.Fatal("expected error from generic start failure")
	}
	if errors.Is(err, exec.ErrNotFound) {
		t.Error("should not return ErrNotFound for generic error")
	}
}

// ---------- runDoltStop error path ----------

func TestRunDoltStop_StopFnError(t *testing.T) {
	oroHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(oroHome, "dolt-server.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oroHome, "dolt-server.port"), []byte("13307"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &doltCmdConfig{
		oroHome:         oroHome,
		aliveFn:         func(int) bool { return true },
		isPortUp:        func(int) bool { return true },
		force:           false,
		dispatcherPIDFn: func() int { return 0 },
		stopFn:          func(string) error { return errors.New("stop failed") },
	}
	var buf bytes.Buffer
	if err := runDoltStop(cfg, &buf); err == nil {
		t.Fatal("expected error when stopFn fails")
	}
}

// ---------- killOrphanServersImpl additional coverage ----------

func TestKillOrphanServersImpl_PortDerived(t *testing.T) {
	// port==0 → DerivePort is called; port not listening → no kill.
	beadsDir := t.TempDir()
	killed := false
	projects := []doltProject{{beadsDir: beadsDir, port: 0, dbName: "beads"}}
	killOrphanServersImpl(projects, &bytes.Buffer{},
		func(int) bool { return false }, // alive
		func(int) bool { return false }, // port not up
		func(int) (int, error) { return 0, nil },
		func(int, string) error { killed = true; return nil },
	)
	if killed {
		t.Error("should not kill anything when derived port is not listening")
	}
}

func TestKillOrphanServersImpl_InvalidPIDFile(t *testing.T) {
	// PID file has non-numeric content → parse fails → falls through to lsof.
	beadsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.pid"), []byte("not-a-pid"), 0o600); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	killed := false
	projects := []doltProject{{beadsDir: beadsDir, port: 13350, dbName: "beads"}}
	killOrphanServersImpl(projects, &bytes.Buffer{},
		func(int) bool { return true },  // alive
		func(int) bool { return false }, // port not up → lsof not triggered
		func(int) (int, error) { return 0, nil },
		func(int, string) error { killed = true; return nil },
	)
	if killed {
		t.Error("should not kill when port not up after PID parse failure")
	}
}

func TestKillOrphanServersImpl_KillsViaLsof(t *testing.T) {
	// No PID file, port up, lsof finds PID → kill it.
	beadsDir := t.TempDir()
	killed := false
	var killedPID int
	projects := []doltProject{{beadsDir: beadsDir, port: 13350, dbName: "beads"}}
	killOrphanServersImpl(projects, &bytes.Buffer{},
		func(int) bool { return false },
		func(int) bool { return true },            // port IS up
		func(int) (int, error) { return 42, nil }, // lsof finds PID 42
		func(pid int, _ string) error { killed = true; killedPID = pid; return nil },
	)
	if !killed {
		t.Error("should have killed process discovered via lsof")
	}
	if killedPID != 42 {
		t.Errorf("killed PID = %d, want 42", killedPID)
	}
}

func TestKillOrphanServersImpl_DiscoverNonErrNotFound(t *testing.T) {
	// discover returns a non-ErrNotFound error → skip (no kill).
	beadsDir := t.TempDir()
	killed := false
	projects := []doltProject{{beadsDir: beadsDir, port: 13350, dbName: "beads"}}
	killOrphanServersImpl(projects, &bytes.Buffer{},
		func(int) bool { return false },
		func(int) bool { return true },                                             // port IS up
		func(int) (int, error) { return 0, errors.New("some other discover err") }, // not ErrNotFound
		func(int, string) error { killed = true; return nil },
	)
	if killed {
		t.Error("should not kill when discover returns non-ErrNotFound error")
	}
}

// ---------- runDoltTeardown error paths ----------

func TestRunDoltTeardown_StopError(t *testing.T) {
	oroHome := t.TempDir()
	homeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(oroHome, "dolt-server.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oroHome, "dolt-server.port"), []byte("13307"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &doltCmdConfig{
		oroHome:         oroHome,
		aliveFn:         func(int) bool { return true },
		isPortUp:        func(int) bool { return true },
		force:           false,
		dispatcherPIDFn: func() int { return 0 },
		stopFn:          func(string) error { return errors.New("stop failed") },
		beadsDirs:       []string{},
	}
	var buf bytes.Buffer
	if err := runDoltTeardown(cfg, homeDir, &buf); err == nil {
		t.Fatal("expected error when stop fails")
	}
}

func TestRunDoltTeardown_RestoreError(t *testing.T) {
	oroHome := t.TempDir()
	homeDir := t.TempDir()

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Malformed JSON to trigger restorePerProjectDBs error.
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &doltCmdConfig{
		oroHome:         oroHome,
		aliveFn:         func(int) bool { return false },
		isPortUp:        func(int) bool { return false },
		force:           false,
		dispatcherPIDFn: func() int { return 0 },
		stopFn:          func(string) error { return nil },
		beadsDirs:       []string{beadsDir},
	}
	withNoopLaunchctl(t)
	var buf bytes.Buffer
	if err := runDoltTeardown(cfg, homeDir, &buf); err == nil {
		t.Fatal("expected error when restore fails")
	}
}

// ---------- newDoltStatusCmd RunE ----------

func TestNewDoltStatusCmd_RunEExecutes(t *testing.T) {
	// Execute the RunE of newDoltStatusCmd with real system calls.
	// runDoltStatus never returns an error — it always reports a status.
	withNoopLaunchctl(t)
	cmd := newDoltStatusCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("newDoltStatusCmd RunE returned unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "running") && !strings.Contains(out, "stopped") {
		t.Errorf("unexpected status output: %s", out)
	}
}

// ---------- copyDirRecursive write error ----------

func TestCopyDirRecursive_WriteFileFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	dst := t.TempDir()
	if err := os.Chmod(dst, 0o555); err != nil {
		t.Fatalf("chmod dst: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dst, 0o755) })
	if err := copyDirRecursive(src, dst); err == nil {
		t.Error("expected error writing to read-only dst")
	}
}

// ---------- findDoltProjects empty dbName ----------

func TestFindDoltProjects_EmptyDbName(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeMetadata(t, beadsDir, map[string]any{
		"backend": "dolt", "dolt_server_port": 13350, "dolt_database": "",
	})
	projects, err := findDoltProjects([]string{beadsDir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 1 || projects[0].dbName != "beads" {
		t.Errorf("expected dbName='beads' for empty dolt_database, got %v", projects)
	}
}

// ---------- newDoltStopCmd RunE ----------

func TestNewDoltStopCmd_RunEExecutes(t *testing.T) {
	// Execute the RunE of newDoltStopCmd when dolt is not running.
	// runDoltStop reads state files and probes the port — all safe when dolt is absent.
	cmd := newDoltStopCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("newDoltStopCmd RunE returned unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "not running") {
		t.Logf("stop output: %s", out)
	}
}

// ---------- newDoltSetupCmd RunE ----------

func TestNewDoltSetupCmd_RunEExecutes(t *testing.T) {
	// Execute the RunE with an empty ORO_HOME so no dolt projects are discovered.
	// runDoltSetup returns early with "no dolt projects found; nothing to do".
	t.Setenv("ORO_HOME", t.TempDir())
	withNoopLaunchctl(t)
	cmd := newDoltSetupCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("newDoltSetupCmd RunE returned unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "no dolt projects") {
		t.Logf("setup output: %s", buf.String())
	}
}

// ---------- newDoltTeardownCmd RunE ----------

func TestNewDoltTeardownCmd_RunEExecutes(t *testing.T) {
	// Execute the RunE with empty ORO_HOME and a fake HOME so no real state is touched.
	// With no beads dirs, teardown is a no-op: stop (not running) + uninstall (no plist) + restore (no projects).
	t.Setenv("ORO_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // redirect home so uninstallLaunchAgent targets a temp dir
	withNoopLaunchctl(t)
	cmd := newDoltTeardownCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("newDoltTeardownCmd RunE returned unexpected error: %v", err)
	}
}

// ---------- newDoltStartCmd RunE ----------

func TestNewDoltStartCmdRoutesThroughEnsure(t *testing.T) {
	// Verify that newDoltStartCmd routes through ensureSharedDoltRunning,
	// using the probe-then-kickstart pathway (same as oro start).
	var ensureWasCalled bool
	var ensureGotOroHome string

	orig := ensureSharedDoltRunningFn
	ensureSharedDoltRunningFn = func(oroHome string) (int, error) {
		ensureWasCalled = true
		ensureGotOroHome = oroHome
		return 0, nil // already running
	}
	t.Cleanup(func() { ensureSharedDoltRunningFn = orig })

	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	cmd := newDoltStartCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := cmd.RunE(cmd, nil)
	if err != nil {
		t.Fatalf("newDoltStartCmd RunE failed: %v", err)
	}

	if !ensureWasCalled {
		t.Error("ensureSharedDoltRunning was not called")
	}

	if ensureGotOroHome == "" {
		t.Error("ensureSharedDoltRunning was called with empty oroHome")
	}
}
