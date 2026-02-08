package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
