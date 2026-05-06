package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// fakeSpawner records calls to SpawnDaemon for testing.
type fakeSpawner struct {
	called     bool
	pidPath    string
	workers    int
	returnPID  int
	returnErr  error
	socketPath string // create socket file after "spawn" to simulate daemon coming up
}

func (f *fakeSpawner) SpawnDaemon(pidPath string, workers, _ int) (pid int, err error) {
	f.called = true
	f.pidPath = pidPath
	f.workers = workers
	if f.returnErr != nil {
		return 0, f.returnErr
	}
	// Simulate: daemon writes PID file and creates socket.
	if err := WritePIDFile(pidPath, f.returnPID); err != nil {
		return 0, err
	}
	if f.socketPath != "" {
		// Start a real UDS listener so pollForSocket and sendStartDirective
		// can connect. Accept multiple connections: pollForSocket does a
		// connect-check first, then sendStartDirective sends the directive.
		ln, listenErr := net.Listen("unix", f.socketPath)
		if listenErr != nil {
			return 0, listenErr
		}
		go func() {
			defer ln.Close()
			for {
				conn, err := ln.Accept()
				if err != nil {
					return // listener closed
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
					// If no data read (connect-check), just close.
				}(conn)
			}
		}()
	}
	return f.returnPID, nil
}

func TestFullStart(t *testing.T) {
	t.Run("spawns daemon, waits for socket, creates tmux session, prints status", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		// Use short socket path — macOS limits UDS paths to 108 chars.
		sockPath := fmt.Sprintf("/tmp/oro-ft-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		fakeTmux := newFakeCmd()
		// has-session returns error (session does not exist)
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fakeTmux, "oro", ManagerNudge())

		spawner := &fakeSpawner{
			returnPID:  12345,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 3, 3, "sonnet", "", spawner, fakeTmux, func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, false)
		// We expect an error because AttachInteractive tries to attach to a real tmux session.
		// In the test environment, there's no real "oro" session, so attach will fail.
		if err == nil {
			t.Fatal("expected error when AttachInteractive tries to attach to nonexistent session")
		}
		if !strings.Contains(err.Error(), "attach to tmux session") {
			t.Fatalf("expected attach error, got: %v", err)
		}

		// 1. Verify daemon was spawned with correct args.
		if !spawner.called {
			t.Fatal("expected SpawnDaemon to be called")
		}
		if spawner.pidPath != pidFile {
			t.Errorf("expected pidPath=%s, got %s", pidFile, spawner.pidPath)
		}
		if spawner.workers != 3 {
			t.Errorf("expected workers=3, got %d", spawner.workers)
		}

		// 2. Verify tmux session was created.
		// Use getCalls() for thread-safe access — beacon goroutine may still be
		// writing to calls via Runner.Run concurrently.
		tmuxCalls := fakeTmux.getCalls()
		newSessionCall := findCall(tmuxCalls, "new-session")
		if newSessionCall == nil {
			t.Fatal("expected tmux new-session to be called")
		}

		// 3. Verify manager window launches interactive claude with role env vars via new-session.
		mgrCmd := newSessionCall[len(newSessionCall)-1]
		for _, envVar := range []string{"ORO_ROLE=manager", "BD_ACTOR=manager", "GIT_AUTHOR_NAME=manager"} {
			if !strings.Contains(mgrCmd, envVar) {
				t.Errorf("new-session command should set %s, got: %s", envVar, mgrCmd)
			}
		}
		if !strings.Contains(mgrCmd, "claude") {
			t.Errorf("new-session command should launch claude, got: %s", mgrCmd)
		}
		if strings.Contains(mgrCmd, "claude -p") {
			t.Errorf("should use interactive claude, not 'claude -p', got: %s", mgrCmd)
		}

		// 4. Verify no new-window call (single-window architecture).
		for _, call := range tmuxCalls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "new-window" {
				t.Errorf("Create must not call new-window (single manager window), got: %v", call)
			}
		}

		// 5. Verify manager nudge is injected via send-keys.
		var managerCalls [][]string
		for _, call := range tmuxCalls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				if strings.Contains(strings.Join(call, " "), "oro:manager") {
					managerCalls = append(managerCalls, call)
				}
			}
		}
		if len(managerCalls) < 1 {
			t.Fatalf("expected at least 1 send-keys to manager window, got %d", len(managerCalls))
		}
		mgrNudge := strings.Join(managerCalls[0], " ")
		if !strings.Contains(mgrNudge, "oro manager") {
			t.Errorf("manager window nudge should contain 'oro manager', got: %s", mgrNudge)
		}

		// 5. Verify status output.
		out := stdout.String()
		if !strings.Contains(out, "oro swarm started") {
			t.Errorf("expected output to contain 'oro swarm started', got: %s", out)
		}
		if !strings.Contains(out, "12345") {
			t.Errorf("expected output to contain PID 12345, got: %s", out)
		}
		if !strings.Contains(out, "workers=3") {
			t.Errorf("expected output to contain 'workers=3', got: %s", out)
		}
	})

	t.Run("returns error when daemon spawn fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("ORO_PID_PATH", filepath.Join(tmpDir, "oro.pid"))
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
		t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

		spawner := &fakeSpawner{
			returnErr: fmt.Errorf("spawn failed"),
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 2, 2, "sonnet", "", spawner, newFakeCmd(), func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, false)
		if err == nil {
			t.Fatal("expected error when spawn fails")
		}
		if !strings.Contains(err.Error(), "spawn") {
			t.Errorf("expected error to mention spawn, got: %v", err)
		}
	})

	t.Run("returns error when socket does not appear", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(tmpDir, "oro.sock"))
		t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

		// Spawner succeeds but does NOT create the socket file.
		spawner := &fakeSpawner{
			returnPID:  99999,
			socketPath: "", // don't create socket
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 2, 2, "sonnet", "", spawner, newFakeCmd(), func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, false)
		if err == nil {
			t.Fatal("expected error when socket never appears")
		}
		if !strings.Contains(err.Error(), "socket") {
			t.Errorf("expected error to mention socket, got: %v", err)
		}
	})

	t.Run("returns error when tmux create fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-ft-tmux-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

		fakeTmux := newFakeCmd()
		// has-session returns error (no session)
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		// new-session fails — key must include the execEnvCmd arg that production code passes.
		fakeTmux.errs[key("tmux", "new-session", "-d", "-s", "oro", "-n", "manager", execEnvCmd("manager", ""))] = fmt.Errorf("tmux not installed")

		spawner := &fakeSpawner{
			returnPID:  12345,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 2, 2, "sonnet", "", spawner, fakeTmux, func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, false)
		if err == nil {
			t.Fatal("expected error when tmux create fails")
		}
		if !strings.Contains(err.Error(), "tmux") {
			t.Errorf("expected error to mention tmux, got: %v", err)
		}
	})
}

func TestCreateWithNudges(t *testing.T) {
	t.Run("injects manager nudge via send-keys", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "You are a test manager.")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("You are a test manager.")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		var managerCalls [][]string
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:manager") {
					managerCalls = append(managerCalls, call)
				}
			}
		}

		if len(managerCalls) < 1 {
			t.Fatalf("expected at least 1 send-keys to manager window, got %d", len(managerCalls))
		}
		mgrNudge := strings.Join(managerCalls[0], " ")
		if !strings.Contains(mgrNudge, "You are a test manager.") {
			t.Errorf("manager window nudge should contain manager text, got: %s", mgrNudge)
		}
	})

	t.Run("manager window does not use claude -p", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fake, "oro", "manager nudge")

		sess := &TmuxSession{Name: TmuxSessionName(""), Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("manager nudge")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}
		sess.WaitBeacon()

		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "claude -p") {
					t.Errorf("manager window should not use 'claude -p', got: %s", joined)
				}
			}
		}
	})

	t.Run("nudge is shorter than full beacon", func(t *testing.T) {
		mgrNudge := ManagerNudge()
		mgrBeacon := ManagerBeacon()
		if len(mgrNudge) >= len(mgrBeacon) {
			t.Errorf("ManagerNudge (%d chars) should be much shorter than ManagerBeacon (%d chars)",
				len(mgrNudge), len(mgrBeacon))
		}
		if len(mgrNudge) > 500 {
			t.Errorf("ManagerNudge should be a short nudge (<500 chars), got %d chars", len(mgrNudge))
		}
	})

	t.Run("prints startup progress with checkmarks", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := "/tmp/oro-test.sock" // Use short path to avoid socket path length limits
		dbPath := filepath.Join(tmpDir, "state.db")
		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		// Clean up socket after test
		t.Cleanup(func() {
			_ = os.Remove(sockPath)
		})

		fakeTmux := newFakeCmd()
		// has-session returns error (session does not exist)
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fakeTmux, "oro", ManagerNudge())

		spawner := &fakeSpawner{
			returnPID:  12345,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 3, 3, "sonnet", "", spawner, fakeTmux, func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		output := stdout.String()

		// Verify startup progress steps are logged
		expectedSteps := []string{
			"✓ Preflight checks passed",
			"✓ Daemon started (PID 12345)",
			"✓ Dispatcher socket ready",
			"✓ Tmux session created",
			"✓ Beacon verified",
			"oro swarm started",
		}

		for _, step := range expectedSteps {
			if !strings.Contains(output, step) {
				t.Errorf("expected output to contain %q, got:\n%s", step, output)
			}
		}
	})
}
