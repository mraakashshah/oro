package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestDaemonLifecycle(t *testing.T) {
	// Use a temp directory instead of ~/.oro for isolation.
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")

	t.Run("WritePIDFile writes current PID", func(t *testing.T) {
		pid := os.Getpid()
		err := WritePIDFile(pidFile, pid)
		if err != nil {
			t.Fatalf("WritePIDFile failed: %v", err)
		}

		data, err := os.ReadFile(pidFile) //nolint:gosec // test file, path is from t.TempDir
		if err != nil {
			t.Fatalf("reading PID file: %v", err)
		}

		got, err := strconv.Atoi(string(data))
		if err != nil {
			t.Fatalf("parsing PID from file: %v", err)
		}

		if got != pid {
			t.Errorf("PID file contains %d, want %d", got, pid)
		}

		// Cleanup for next subtest.
		_ = os.Remove(pidFile)
	})

	t.Run("ReadPIDFile returns pid from file", func(t *testing.T) {
		wantPID := 12345
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(wantPID)), 0o600); err != nil {
			t.Fatalf("setup: write PID file: %v", err)
		}
		defer os.Remove(pidFile)

		got, err := ReadPIDFile(pidFile)
		if err != nil {
			t.Fatalf("ReadPIDFile failed: %v", err)
		}
		if got != wantPID {
			t.Errorf("ReadPIDFile = %d, want %d", got, wantPID)
		}
	})

	t.Run("ReadPIDFile returns error for missing file", func(t *testing.T) {
		_, err := ReadPIDFile(filepath.Join(tmpDir, "nonexistent.pid"))
		if err == nil {
			t.Fatal("expected error for missing PID file")
		}
	})

	t.Run("ReadPIDFile returns error for non-numeric content", func(t *testing.T) {
		badFile := filepath.Join(tmpDir, "bad.pid")
		if err := os.WriteFile(badFile, []byte("notanumber"), 0o600); err != nil {
			t.Fatalf("setup: write bad PID file: %v", err)
		}
		defer os.Remove(badFile)

		_, err := ReadPIDFile(badFile)
		if err == nil {
			t.Fatal("expected error for non-numeric PID file content")
		}
	})

	t.Run("RemovePIDFile removes the file", func(t *testing.T) {
		if err := os.WriteFile(pidFile, []byte("999"), 0o600); err != nil {
			t.Fatalf("setup: write PID file: %v", err)
		}

		err := RemovePIDFile(pidFile)
		if err != nil {
			t.Fatalf("RemovePIDFile failed: %v", err)
		}

		if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
			t.Error("PID file still exists after RemovePIDFile")
		}
	})

	t.Run("RemovePIDFile is idempotent for missing file", func(t *testing.T) {
		err := RemovePIDFile(filepath.Join(tmpDir, "already-gone.pid"))
		if err != nil {
			t.Fatalf("RemovePIDFile should not error for missing file: %v", err)
		}
	})

	t.Run("IsProcessAlive returns true for own process", func(t *testing.T) {
		alive := IsProcessAlive(os.Getpid())
		if !alive {
			t.Error("expected own process to be alive")
		}
	})

	t.Run("IsProcessAlive returns false for bogus PID", func(t *testing.T) {
		// PID 4000000 is almost certainly not running.
		alive := IsProcessAlive(4000000)
		if alive {
			t.Error("expected bogus PID to not be alive")
		}
	})

	t.Run("DaemonStatus reports running for live process", func(t *testing.T) {
		pid := os.Getpid()
		if err := WritePIDFile(pidFile, pid); err != nil {
			t.Fatalf("setup: %v", err)
		}
		defer os.Remove(pidFile)

		noSock := filepath.Join(tmpDir, "nonexistent.sock")
		status, gotPID, err := DaemonStatus(pidFile, noSock)
		if err != nil {
			t.Fatalf("DaemonStatus failed: %v", err)
		}
		if status != StatusRunning {
			t.Errorf("DaemonStatus = %q, want %q", status, StatusRunning)
		}
		if gotPID != pid {
			t.Errorf("DaemonStatus PID = %d, want %d", gotPID, pid)
		}
	})

	t.Run("DaemonStatus reports stopped when no PID file", func(t *testing.T) {
		noSock := filepath.Join(tmpDir, "nonexistent.sock")
		status, pid, err := DaemonStatus(filepath.Join(tmpDir, "nope.pid"), noSock)
		if err != nil {
			t.Fatalf("DaemonStatus failed: %v", err)
		}
		if status != StatusStopped {
			t.Errorf("DaemonStatus = %q, want %q", status, StatusStopped)
		}
		if pid != 0 {
			t.Errorf("DaemonStatus PID = %d, want 0", pid)
		}
	})

	t.Run("DaemonStatus reports stale when process is dead", func(t *testing.T) {
		// Write a PID that doesn't correspond to a running process.
		if err := WritePIDFile(pidFile, 4000000); err != nil {
			t.Fatalf("setup: %v", err)
		}
		defer os.Remove(pidFile)

		noSock := filepath.Join(tmpDir, "nonexistent.sock")
		status, _, err := DaemonStatus(pidFile, noSock)
		if err != nil {
			t.Fatalf("DaemonStatus failed: %v", err)
		}
		if status != StatusStale {
			t.Errorf("DaemonStatus = %q, want %q", status, StatusStale)
		}
	})

	t.Run("SIGTERM with nil authorized honored (backward compat)", func(t *testing.T) {
		if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
			t.Fatalf("setup: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		shutdownCtx, cleanupFn := SetupSignalHandler(ctx, pidFile, nil)

		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatalf("sending SIGTERM: %v", err)
		}

		select {
		case <-shutdownCtx.Done():
			// OK — context was cancelled by signal handler (nil = always honor).
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for signal handler to cancel context")
		}

		cleanupFn()
		if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
			t.Error("PID file should be cleaned up after SIGTERM")
		}
	})

	t.Run("SIGTERM ignored when not authorized", func(t *testing.T) {
		if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
			t.Fatalf("setup: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var authorized atomic.Bool // defaults to false
		shutdownCtx, cleanupFn := SetupSignalHandler(ctx, pidFile, &authorized)
		defer cleanupFn()

		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatalf("sending SIGTERM: %v", err)
		}

		// Context should NOT be cancelled — SIGTERM is unauthorized.
		select {
		case <-shutdownCtx.Done():
			t.Fatal("context should not be cancelled when SIGTERM is unauthorized")
		case <-time.After(200 * time.Millisecond):
			// OK — SIGTERM was ignored.
		}
	})

	t.Run("SIGTERM honored when authorized", func(t *testing.T) {
		if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
			t.Fatalf("setup: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var authorized atomic.Bool
		authorized.Store(true)
		shutdownCtx, cleanupFn := SetupSignalHandler(ctx, pidFile, &authorized)
		defer cleanupFn()

		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatalf("sending SIGTERM: %v", err)
		}

		select {
		case <-shutdownCtx.Done():
			// OK — authorized SIGTERM was honored.
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for authorized SIGTERM to cancel context")
		}
	})

	t.Run("SIGINT always honored regardless of authorization", func(t *testing.T) {
		if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
			t.Fatalf("setup: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var authorized atomic.Bool // false — but SIGINT should still work
		shutdownCtx, cleanupFn := SetupSignalHandler(ctx, pidFile, &authorized)
		defer cleanupFn()

		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatalf("sending SIGINT: %v", err)
		}

		select {
		case <-shutdownCtx.Done():
			// OK — SIGINT always honored.
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for SIGINT to cancel context")
		}
	})

	t.Run("DefaultPIDPath returns path under home directory", func(t *testing.T) {
		p, err := DefaultPIDPath()
		if err != nil {
			t.Fatalf("DefaultPIDPath failed: %v", err)
		}
		home, _ := os.UserHomeDir()
		expected := filepath.Join(home, ".oro", "oro.pid")
		if p != expected {
			t.Errorf("DefaultPIDPath = %q, want %q", p, expected)
		}
	})

	t.Run("StopDaemon sends SIGTERM to a running process", func(t *testing.T) {
		// We cannot easily test killing another process in a unit test,
		// but we can test the error path for a non-existent process.
		if err := WritePIDFile(pidFile, 4000000); err != nil {
			t.Fatalf("setup: %v", err)
		}
		defer os.Remove(pidFile)

		err := StopDaemon(pidFile)
		if err == nil {
			t.Fatal("expected error when stopping non-existent process")
		}
	})

	t.Run("StopDaemon returns error for missing PID file", func(t *testing.T) {
		err := StopDaemon(filepath.Join(tmpDir, "gone.pid"))
		if err == nil {
			t.Fatal("expected error when PID file does not exist")
		}
	})
}

func TestDaemonStatus_SocketFirst(t *testing.T) {
	// Socket responds with PID, no PID file → StatusRunning via socket.
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "nonexistent.pid") // no PID file

	// Use /tmp for socket to stay under macOS 104-char UDS path limit.
	sockPath := fmt.Sprintf("/tmp/oro-ds-%d.sock", time.Now().UnixNano())
	defer os.Remove(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Mock UDS server returns our own PID so IsProcessAlive succeeds.
	myPID := os.Getpid()
	statusJSON := fmt.Sprintf(`{"state":"running","pid":%d,"worker_count":2,"queue_depth":0,"assignments":{}}`, myPID)

	ready := make(chan struct{})
	go runMockPIDDispatcher(ctx, t, sockPath, statusJSON, ready)

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timeout waiting for mock dispatcher")
	}

	status, gotPID, err := DaemonStatus(pidFile, sockPath)
	if err != nil {
		t.Fatalf("DaemonStatus failed: %v", err)
	}
	if status != StatusRunning {
		t.Errorf("DaemonStatus = %q, want %q", status, StatusRunning)
	}
	if gotPID != myPID {
		t.Errorf("DaemonStatus PID = %d, want %d", gotPID, myPID)
	}
}

func TestDaemonStatus_FallbackToPIDFile(t *testing.T) {
	// No socket, PID file exists with live process → StatusRunning from fallback.
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	noSock := filepath.Join(tmpDir, "nonexistent.sock")

	pid := os.Getpid()
	if err := WritePIDFile(pidFile, pid); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer os.Remove(pidFile)

	status, gotPID, err := DaemonStatus(pidFile, noSock)
	if err != nil {
		t.Fatalf("DaemonStatus failed: %v", err)
	}
	if status != StatusRunning {
		t.Errorf("DaemonStatus = %q, want %q", status, StatusRunning)
	}
	if gotPID != pid {
		t.Errorf("DaemonStatus PID = %d, want %d", gotPID, pid)
	}
}

func TestDaemonStatus_BothMissing(t *testing.T) {
	// No socket, no PID file → StatusStopped.
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "nonexistent.pid")
	noSock := filepath.Join(tmpDir, "nonexistent.sock")

	status, pid, err := DaemonStatus(pidFile, noSock)
	if err != nil {
		t.Fatalf("DaemonStatus failed: %v", err)
	}
	if status != StatusStopped {
		t.Errorf("DaemonStatus = %q, want %q", status, StatusStopped)
	}
	if pid != 0 {
		t.Errorf("DaemonStatus PID = %d, want 0", pid)
	}
}

func TestProbeSocket_NonExistent(t *testing.T) {
	// Non-existent socket path → returns 0.
	tmpDir := t.TempDir()
	noSock := filepath.Join(tmpDir, "nonexistent.sock")

	pid := probeSocket(noSock)
	if pid != 0 {
		t.Errorf("probeSocket = %d, want 0 for non-existent socket", pid)
	}
}

// runMockPIDDispatcher starts a UDS listener that accepts one connection,
// reads a DIRECTIVE message with op=status, and sends an ACK with the given
// status JSON (which includes a PID field) as the detail.
func runMockPIDDispatcher(ctx context.Context, t *testing.T, sockPath, statusJSON string, ready chan<- struct{}) {
	t.Helper()

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Errorf("mock dispatcher listen: %v", err)
		return
	}
	defer ln.Close()
	defer os.Remove(sockPath)

	close(ready)

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		connCh <- conn
	}()

	select {
	case conn := <-connCh:
		defer conn.Close()

		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			t.Error("failed to read line from connection")
			return
		}

		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Errorf("unmarshal message: %v", err)
			return
		}

		if msg.Type != protocol.MsgDirective || msg.Directive == nil || msg.Directive.Op != "status" {
			t.Errorf("expected status directive, got: %+v", msg)
			return
		}

		ack := protocol.Message{
			Type: protocol.MsgACK,
			ACK: &protocol.ACKPayload{
				OK:     true,
				Detail: statusJSON,
			},
		}
		data, _ := json.Marshal(ack)
		data = append(data, '\n')
		_, _ = conn.Write(data)

	case <-ctx.Done():
		return
	}
}
