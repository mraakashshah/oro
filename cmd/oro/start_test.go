package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestIsDetached(t *testing.T) {
	t.Run("flag override always returns true", func(t *testing.T) {
		// Even if we were in a TTY, --detach flag should force detached mode.
		if !isDetached(true) {
			t.Error("isDetached(true) should always return true")
		}
	})

	t.Run("no flag with non-TTY returns true", func(t *testing.T) {
		// In test environment, stdin is not a terminal — should auto-detect.
		// (go test redirects stdin from /dev/null, which is not a TTY)
		if !isDetached(false) {
			t.Error("isDetached(false) should return true when stdin is not a TTY")
		}
	})
}

func TestRunFullStart_Detached(t *testing.T) {
	t.Run("skips attach and returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-detach1-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)

		// Start a fake UDS listener that ACKs the start directive.
		// Accept multiple connections because pollForSocket probes first.
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() { _ = ln.Close() }()

		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					scanner := bufio.NewScanner(c)
					if scanner.Scan() {
						ack := protocol.Message{
							Type: protocol.MsgACK,
							ACK:  &protocol.ACKPayload{OK: true, Detail: "started"},
						}
						data, _ := json.Marshal(ack)
						data = append(data, '\n')
						_, _ = c.Write(data)
					}
				}(conn)
			}
		}()

		spawner := &mockDaemonSpawner{pid: 42463}
		tmuxRunner := &mockCmdRunner{}

		var buf bytes.Buffer
		err = runFullStart(&buf, 2, 2, "sonnet", "", spawner, tmuxRunner, func(int) error { return nil }, 2*time.Second, func(time.Duration) {}, 50*time.Millisecond, true)
		// With detach=true, should return nil (no attach attempt).
		if err != nil {
			t.Fatalf("runFullStart with detach=true should succeed, got: %v", err)
		}
	})

	t.Run("prints attach instructions", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-detach2-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)

		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() { _ = ln.Close() }()

		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					scanner := bufio.NewScanner(c)
					if scanner.Scan() {
						ack := protocol.Message{
							Type: protocol.MsgACK,
							ACK:  &protocol.ACKPayload{OK: true, Detail: "started"},
						}
						data, _ := json.Marshal(ack)
						data = append(data, '\n')
						_, _ = c.Write(data)
					}
				}(conn)
			}
		}()

		spawner := &mockDaemonSpawner{pid: 42463}
		tmuxRunner := &mockCmdRunner{}

		var buf bytes.Buffer
		err = runFullStart(&buf, 2, 2, "sonnet", "", spawner, tmuxRunner, func(int) error { return nil }, 2*time.Second, func(time.Duration) {}, 50*time.Millisecond, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !contains(out, "oro attach") {
			t.Errorf("expected attach instructions in output, got: %s", out)
		}
		if !contains(out, "detached") {
			t.Errorf("expected 'detached' in output, got: %s", out)
		}
	})
}

func TestCleanEnvForDaemon(t *testing.T) {
	t.Run("removes CLAUDECODE from env", func(t *testing.T) {
		env := []string{"HOME=/Users/test", "CLAUDECODE=1", "PATH=/usr/bin"}
		cleaned := cleanEnvForDaemon(env)
		for _, e := range cleaned {
			if e == "CLAUDECODE=1" {
				t.Error("CLAUDECODE should be removed from daemon env")
			}
		}
		if len(cleaned) != 2 {
			t.Errorf("expected 2 env vars, got %d: %v", len(cleaned), cleaned)
		}
	})

	t.Run("preserves env when CLAUDECODE absent", func(t *testing.T) {
		env := []string{"HOME=/Users/test", "PATH=/usr/bin"}
		cleaned := cleanEnvForDaemon(env)
		if len(cleaned) != 2 {
			t.Errorf("expected 2 env vars, got %d", len(cleaned))
		}
	})
}

func TestStartPreflightAndCheckRunning_DaemonOnlyBypass(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	toolsDir := filepath.Join(tmpDir, "tools")
	if err := os.Mkdir(toolsDir, 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	for _, tool := range []string{"claude", "git"} {
		path := filepath.Join(toolsDir, tool)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write tool %s: %v", tool, err)
		}
	}

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))
	t.Setenv("PATH", toolsDir)
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	t.Setenv(daemonSkipPreflightEnv, "1")

	got, err := startPreflightAndCheckRunning(io.Discard, true)
	if err != nil {
		t.Fatalf("startPreflightAndCheckRunning: %v", err)
	}
	if got != pidFile {
		t.Fatalf("pid path = %q, want %q", got, pidFile)
	}
}

func TestStartPreflightAndCheckRunning_NonDaemonDoesNotBypass(t *testing.T) {
	tmpDir := t.TempDir()

	t.Setenv("PATH", tmpDir)
	t.Setenv(daemonSkipPreflightEnv, "1")

	_, err := startPreflightAndCheckRunning(io.Discard, false)
	if err == nil {
		t.Fatal("expected non-daemon start to run preflight")
	}
	if !strings.Contains(err.Error(), "required tool") {
		t.Fatalf("expected required tool error, got %v", err)
	}
}

func TestStartPreflightAndCheckRunning_DaemonBypassRequiresSQLiteMode(t *testing.T) {
	tmpDir := t.TempDir()

	t.Setenv("PATH", tmpDir)
	t.Setenv("ORO_BEADSOURCE_MODE", "cli")
	t.Setenv(daemonSkipPreflightEnv, "1")

	_, err := startPreflightAndCheckRunning(io.Discard, true)
	if err == nil {
		t.Fatal("expected daemon bypass to require sqlite mode")
	}
	if !strings.Contains(err.Error(), "required tool") {
		t.Fatalf("expected required tool error, got %v", err)
	}
}

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

	// Verify permissions are 0700 (no group or other access).
	perm := info.Mode().Perm()
	if perm != 0o700 {
		t.Errorf("expected perms 0700, got %04o", perm)
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
	skipIfToolsMissing(t)

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
	waitForSocket(t, sockPath, 5*time.Second)

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
	skipIfToolsMissing(t)

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
	waitForSocket(t, sockPath, 5*time.Second)

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
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				if scanner.Scan() {
					var msg protocol.Message
					if json.Unmarshal(scanner.Bytes(), &msg) == nil && msg.Directive != nil {
						directiveCh <- msg.Directive.Op
					}
					// Send ACK
					ack := protocol.Message{
						Type: protocol.MsgACK,
						ACK:  &protocol.ACKPayload{OK: true, Detail: "started"},
					}
					data, _ := json.Marshal(ack)
					data = append(data, '\n')
					_, _ = c.Write(data)
				}
			}(conn)
		}
	}()

	// Mock spawner that does nothing (socket already exists).
	spawner := &mockDaemonSpawner{pid: 12345}
	tmuxRunner := &mockCmdRunner{}

	var buf bytes.Buffer
	err = runFullStart(&buf, 2, 2, "sonnet", "", spawner, tmuxRunner, func(int) error { return nil }, 2*time.Second, func(time.Duration) {}, 50*time.Millisecond, false)
	// We expect an error because AttachInteractive tries to attach to a real tmux session.
	if err == nil {
		t.Fatal("expected error when AttachInteractive tries to attach to nonexistent session")
	}
	if !contains(err.Error(), "attach to tmux session") {
		t.Fatalf("expected attach error, got: %v", err)
	}

	// Verify start directive was sent (before the attach attempt).
	select {
	case op := <-directiveCh:
		if op != "start" {
			t.Fatalf("expected 'start' directive, got %q", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for start directive")
	}
}

func TestReconnectTmuxRecreatesUnhealthySession(t *testing.T) {
	t.Run("unhealthy session is killed and recreated", func(t *testing.T) {
		fake := newFakeCmd()

		// Simulate existing but unhealthy session:
		// has-session → success (session exists)
		// display-message for architect → "zsh" (shell = unhealthy)
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		fake.errs[key("tmux", "has-session", "-t", "oro")] = nil

		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_current_command}")] = "zsh"

		// Kill succeeds
		fake.output[key("tmux", "kill-session", "-t", "oro")] = ""

		// After kill, has-session fails (session gone) — so Create() creates fresh session
		// But fakeCmd returns same result for same key. We need sequential output for has-session.
		// First call: exists (from Create's Exists() check)
		// After kill, Create() tries new-session which should succeed.
		fake.seqOut[key("tmux", "has-session", "-t", "oro")] = []string{
			"", // first check in Create(): Exists() → true
			"", // not called again after kill, but safe fallback
		}

		// Stub new-session and new-window for recreation
		fake.output[key("tmux", "new-session", "-d", "-s", "oro", "-n", "architect")] = "" // partial match won't work

		// Stub pane readiness for the recreated session
		stubPaneReady(fake, "oro", ArchitectNudge(), ManagerNudge())

		var buf bytes.Buffer
		err := reconnectTmux(&buf, fake, "", true, noopSleep, 50*time.Millisecond)
		if err != nil {
			t.Fatalf("reconnectTmux should succeed with detach=true, got: %v", err)
		}

		// Verify kill-session was called (zombie was killed)
		calls := fake.getCalls()
		found := false
		for _, c := range calls {
			if len(c) >= 3 && c[0] == "tmux" && c[1] == "kill-session" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected tmux kill-session to be called for unhealthy session")
		}

		// Verify output mentions reconnection
		out := buf.String()
		if !strings.Contains(out, "session unhealthy") {
			t.Errorf("expected 'session unhealthy' in output, got: %s", out)
		}
	})

	t.Run("healthy session skips recreation", func(t *testing.T) {
		fake := newFakeCmd()

		// Simulate existing healthy session:
		// has-session → success
		// display-message → "claude" for both panes (healthy)
		fake.output[key("tmux", "has-session", "-t", "oro")] = ""
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:architect", "#{pane_current_command}")] = "claude"
		fake.output[key("tmux", "display-message", "-p", "-t", "oro:manager", "#{pane_current_command}")] = "claude"

		var buf bytes.Buffer
		err := reconnectTmux(&buf, fake, "", true, noopSleep, 50*time.Millisecond)
		if err != nil {
			t.Fatalf("reconnectTmux should succeed, got: %v", err)
		}

		// Verify kill-session was NOT called
		calls := fake.getCalls()
		for _, c := range calls {
			if len(c) >= 3 && c[0] == "tmux" && c[1] == "kill-session" {
				t.Error("kill-session should not be called for healthy session")
			}
		}
	})
}

// mockDaemonSpawner implements DaemonSpawner for testing.
type mockDaemonSpawner struct {
	pid int
}

func (m *mockDaemonSpawner) SpawnDaemon(_ string, _, _ int) (int, error) {
	return m.pid, nil
}

// mockCmdRunner implements CmdRunner for testing — records commands but does nothing.
type mockCmdRunner struct{}

func (m *mockCmdRunner) Run(_ string, _ ...string) (string, error) {
	return "", nil
}

// TestRunFullStartAttachesSession verifies that runFullStart attempts to attach
// to the tmux session after creation. Since AttachInteractive uses exec.Command
// directly and would block, we can't fully test it here. But we verify the flow
// by checking that runFullStart would call it (implementation check).
func TestRunFullStartAttachesSession(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := filepath.Join(tmpDir, "oro.sock")

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)

	// Start a fake UDS listener.
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				if scanner.Scan() {
					ack := protocol.Message{
						Type: protocol.MsgACK,
						ACK:  &protocol.ACKPayload{OK: true, Detail: "started"},
					}
					data, _ := json.Marshal(ack)
					data = append(data, '\n')
					_, _ = c.Write(data)
				}
			}(conn)
		}
	}()

	spawner := &mockDaemonSpawner{pid: 12345}
	tmuxRunner := &mockCmdRunner{}

	var buf bytes.Buffer
	// Note: This will attempt to call AttachInteractive which tries to connect
	// to a real tmux session "oro". Since there's no real tmux in the test env,
	// this will fail. We're verifying the code path leads to attach being called.
	err = runFullStart(&buf, 2, 2, "sonnet", "", spawner, tmuxRunner, func(int) error { return nil }, 2*time.Second, func(time.Duration) {}, 50*time.Millisecond, false)

	// We expect an error because AttachInteractive will fail (no tmux session).
	if err == nil {
		t.Fatal("expected error when AttachInteractive fails in test env")
	}
	if !contains(err.Error(), "attach to tmux session") {
		t.Errorf("expected 'attach to tmux session' error, got: %v", err)
	}

	// Verify status message was printed before attach attempt.
	out := buf.String()
	if !contains(out, "oro swarm started") {
		t.Errorf("expected status message before attach, got: %s", out)
	}
}
