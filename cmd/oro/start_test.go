package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestBootstrapOroDir_CreatesWithCorrectPerms(t *testing.T) {
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")

	// Directory should not exist yet.
	if _, err := os.Stat(oroDir); err == nil {
		t.Fatal("expected .oro dir to not exist before bootstrap")
	}

	if err := bootstrapOroDir(oroDir); err != nil {
		t.Fatalf("bootstrapOroDir: %v", err)
	}

	// Verify directory was created.
	info, err := os.Stat(oroDir)
	if err != nil {
		t.Fatalf("stat .oro dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected .oro to be a directory")
	}

	// Verify permissions are 0750.
	perm := info.Mode().Perm()
	if perm != 0o750 {
		t.Errorf("expected perms 0750, got %04o", perm)
	}
}

func TestBootstrapOroDir_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")

	// Create twice — should not error.
	if err := bootstrapOroDir(oroDir); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}

	// Write a file inside to prove idempotent call doesn't wipe contents.
	marker := filepath.Join(oroDir, "marker.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if err := bootstrapOroDir(oroDir); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	// Verify marker still exists.
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("marker file was deleted by idempotent bootstrap")
	}
}

func TestStartCommandPreflightChecks(t *testing.T) {
	// Test that the start command runs preflight checks before attempting to start.
	// Since we can't easily mock exec.LookPath, this test verifies that with all
	// tools present, preflight doesn't block the start command.
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := filepath.Join(tmpDir, "oro.sock")
	dbPath := filepath.Join(tmpDir, "state.db")

	cmd := newStartCmd()

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)

	// Try to run with daemon-only mode (simpler than full start).
	cmd.SetArgs([]string{"--daemon-only", "--workers", "1"})

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	// Run in background since it blocks.
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Execute()
	}()

	// Wait for socket to appear (confirms preflight passed and dispatcher started).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// If socket exists, preflight checks passed.
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("preflight may have failed - socket not created: %v", err)
	}

	// Clean up - send interrupt to stop daemon.
	pid, err := ReadPIDFile(pidFile)
	if err != nil {
		t.Fatalf("read PID file: %v", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find process: %v", err)
	}
	_ = proc.Signal(os.Interrupt)

	// Wait for shutdown.
	select {
	case <-errCh:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit")
	}
}

func TestDaemonOnlyStartsDispatcher(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := filepath.Join(tmpDir, "oro.sock")
	dbPath := filepath.Join(tmpDir, "state.db")

	cmd := newStartCmd()

	// Override paths via environment for testability.
	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)

	cmd.SetArgs([]string{"--daemon-only", "--workers", "2"})

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	// Run command in background — it blocks until context cancels.
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Execute()
	}()

	// Wait for socket file to appear (dispatcher is listening).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify socket file exists.
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket file %s not created: %v\nstdout: %s", sockPath, err, stdout.String())
	}

	// Verify we can connect to the socket.
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("cannot connect to dispatcher socket: %v", err)
	}
	_ = conn.Close()

	// Verify PID file was written.
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("PID file not created: %v", err)
	}

	// Send SIGTERM to stop the daemon.
	pid, err := ReadPIDFile(pidFile)
	if err != nil {
		t.Fatalf("read PID file: %v", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find process: %v", err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		t.Fatalf("send interrupt: %v", err)
	}

	// Wait for command to finish.
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("command failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not exit after signal")
	}

	// Verify output mentions dispatcher.
	out := stdout.String()
	if !contains(out, "starting dispatcher") {
		t.Errorf("expected 'starting dispatcher' in output, got: %s", out)
	}
	if !contains(out, "dispatcher stopped") {
		t.Errorf("expected 'dispatcher stopped' in output, got: %s", out)
	}

	// Verify socket is cleaned up (listener closed).
	_, err = net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err == nil {
		t.Error("socket should be closed after shutdown")
	}
}

// TestStartSendsDirective verifies that runFullStart sends a "start" directive
// to the dispatcher after the socket is ready, transitioning it from StateInert
// to StateRunning.
func TestStartSendsDirective(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := filepath.Join(tmpDir, "oro.sock")

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)

	// Start a fake UDS listener that records the directive it receives.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	directiveCh := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			var msg protocol.Message
			if json.Unmarshal(scanner.Bytes(), &msg) == nil && msg.Directive != nil {
				directiveCh <- msg.Directive.Op
			}
		}

		// Send ACK
		ack := protocol.Message{
			Type: protocol.MsgACK,
			ACK:  &protocol.ACKPayload{OK: true, Detail: "started"},
		}
		data, _ := json.Marshal(ack)
		data = append(data, '\n')
		_, _ = conn.Write(data)
	}()

	// Mock spawner that does nothing (socket already exists).
	spawner := &mockDaemonSpawner{pid: 12345}
	tmuxRunner := &mockCmdRunner{}

	var buf bytes.Buffer
	err = runFullStart(&buf, 2, "sonnet", spawner, tmuxRunner, 2*time.Second, func(time.Duration) {}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("runFullStart: %v", err)
	}

	// Verify start directive was sent.
	select {
	case op := <-directiveCh:
		if op != "start" {
			t.Fatalf("expected 'start' directive, got %q", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for start directive")
	}
}

// mockDaemonSpawner implements DaemonSpawner for testing.
type mockDaemonSpawner struct {
	pid int
}

func (m *mockDaemonSpawner) SpawnDaemon(_ string, _ int) (int, error) {
	return m.pid, nil
}

// mockCmdRunner implements CmdRunner for testing — records commands but does nothing.
type mockCmdRunner struct{}

func (m *mockCmdRunner) Run(_ string, _ ...string) (string, error) {
	return "", nil
}
