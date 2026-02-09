package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func (f *fakeSpawner) SpawnDaemon(pidPath string, workers int) (pid int, err error) {
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
		if err := os.WriteFile(f.socketPath, []byte("fake"), 0o600); err != nil {
			return 0, err
		}
	}
	return f.returnPID, nil
}

func TestFullStart(t *testing.T) {
	t.Run("spawns daemon, waits for socket, creates tmux session, prints status", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := filepath.Join(tmpDir, "oro.sock")
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		fakeTmux := newFakeCmd()
		// has-session returns error (session does not exist)
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		spawner := &fakeSpawner{
			returnPID:  12345,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 3, "sonnet", spawner, fakeTmux, 100*time.Millisecond, noopSleep)
		if err != nil {
			t.Fatalf("runFullStart returned error: %v", err)
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
		newSessionCall := findCall(fakeTmux.calls, "new-session")
		if newSessionCall == nil {
			t.Fatal("expected tmux new-session to be called")
		}

		// 3. Verify both panes launch interactive claude with ORO_ROLE env var.
		var pane0Calls, pane1Calls [][]string
		for _, call := range fakeTmux.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:0.0") {
					pane0Calls = append(pane0Calls, call)
				}
				if strings.Contains(joined, "oro:0.1") {
					pane1Calls = append(pane1Calls, call)
				}
			}
		}

		// Architect pane (0): should have launch + beacon injection.
		if len(pane0Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 0, got %d", len(pane0Calls))
		}
		p0Launch := strings.Join(pane0Calls[0], " ")
		if !strings.Contains(p0Launch, "ORO_ROLE=architect") {
			t.Errorf("pane 0 should set ORO_ROLE=architect, got: %s", p0Launch)
		}
		if !strings.Contains(p0Launch, "claude") {
			t.Errorf("pane 0 should launch claude, got: %s", p0Launch)
		}
		if strings.Contains(p0Launch, "claude -p") {
			t.Errorf("pane 0 should use interactive claude, not 'claude -p', got: %s", p0Launch)
		}
		// Verify architect beacon is injected.
		p0Beacon := strings.Join(pane0Calls[1], " ")
		if !strings.Contains(p0Beacon, "oro architect") {
			t.Errorf("pane 0 beacon should contain architect beacon content, got: %s", p0Beacon)
		}

		// Manager pane (1): should have launch + beacon injection.
		if len(pane1Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 1, got %d", len(pane1Calls))
		}
		p1Launch := strings.Join(pane1Calls[0], " ")
		if !strings.Contains(p1Launch, "ORO_ROLE=manager") {
			t.Errorf("pane 1 should set ORO_ROLE=manager, got: %s", p1Launch)
		}
		if !strings.Contains(p1Launch, "claude") {
			t.Errorf("pane 1 should launch claude, got: %s", p1Launch)
		}
		if strings.Contains(p1Launch, "claude -p") {
			t.Errorf("pane 1 should use interactive claude, not 'claude -p', got: %s", p1Launch)
		}
		// Verify manager beacon is injected with real ManagerBeacon content.
		p1Beacon := strings.Join(pane1Calls[1], " ")
		if !strings.Contains(p1Beacon, "Oro Manager") {
			t.Errorf("pane 1 beacon should contain 'Oro Manager' from ManagerBeacon(), got: %s", p1Beacon)
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
		err := runFullStart(&stdout, 2, "sonnet", spawner, newFakeCmd(), 100*time.Millisecond, noopSleep)
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
		err := runFullStart(&stdout, 2, "sonnet", spawner, newFakeCmd(), 100*time.Millisecond, noopSleep)
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
		sockPath := filepath.Join(tmpDir, "oro.sock")
		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))

		fakeTmux := newFakeCmd()
		// has-session returns error (no session)
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		// new-session fails
		fakeTmux.errs[key("tmux", "new-session", "-d", "-s", "oro")] = fmt.Errorf("tmux not installed")

		spawner := &fakeSpawner{
			returnPID:  12345,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 2, "sonnet", spawner, fakeTmux, 100*time.Millisecond, noopSleep)
		if err == nil {
			t.Fatal("expected error when tmux create fails")
		}
		if !strings.Contains(err.Error(), "tmux") {
			t.Errorf("expected error to mention tmux, got: %v", err)
		}
	})
}

func TestCreateWithManagerBeacon(t *testing.T) {
	t.Run("injects both beacons via send-keys to respective panes", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.Create("You are a test architect.", "You are a test manager.")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Collect send-keys calls per pane.
		var pane0Calls, pane1Calls [][]string
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:0.0") {
					pane0Calls = append(pane0Calls, call)
				}
				if strings.Contains(joined, "oro:0.1") {
					pane1Calls = append(pane1Calls, call)
				}
			}
		}

		// Pane 0: beacon injection (second send-keys) should contain architect beacon.
		if len(pane0Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 0, got %d", len(pane0Calls))
		}
		p0Beacon := strings.Join(pane0Calls[1], " ")
		if !strings.Contains(p0Beacon, "You are a test architect.") {
			t.Errorf("pane 0 beacon should contain architect text, got: %s", p0Beacon)
		}

		// Pane 1: beacon injection (second send-keys) should contain manager beacon.
		if len(pane1Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 1, got %d", len(pane1Calls))
		}
		p1Beacon := strings.Join(pane1Calls[1], " ")
		if !strings.Contains(p1Beacon, "You are a test manager.") {
			t.Errorf("pane 1 beacon should contain manager text, got: %s", p1Beacon)
		}
	})

	t.Run("neither pane uses claude -p", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep}
		err := sess.Create("architect prompt", "manager prompt")
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "claude -p") {
					t.Errorf("no pane should use 'claude -p', got: %s", joined)
				}
			}
		}
	})
}
