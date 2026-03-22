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
		if meta == nil {
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
