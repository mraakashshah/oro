package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
