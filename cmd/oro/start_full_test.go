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
		err := runFullStart(&stdout, 3, "sonnet", spawner, fakeTmux, 100*time.Millisecond)
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

		// 3. Verify manager pane gets the real ManagerPrompt.
		var managerSendKeys []string
		for _, call := range fakeTmux.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				fullCmd := strings.Join(call, " ")
				if strings.Contains(fullCmd, "oro:0.1") {
					managerSendKeys = call
				}
			}
		}
		if managerSendKeys == nil {
			t.Fatal("expected send-keys to oro:0.1 for manager pane")
		}
		// The manager send-keys should reference interactive claude (not -p).
		managerCmd := strings.Join(managerSendKeys, " ")
		if !strings.Contains(managerCmd, "claude '") {
			t.Errorf("expected manager pane to run interactive 'claude ...', got: %s", managerCmd)
		}
		// Verify it includes content from the real ManagerPrompt (check for a distinctive phrase).
		if !strings.Contains(managerCmd, "Oro Manager") {
			t.Errorf("expected manager pane command to include 'Oro Manager' from ManagerPrompt(), got: %s", managerCmd)
		}

		// 4. Verify architect pane gets plain "claude".
		var architectSendKeys []string
		for _, call := range fakeTmux.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				fullCmd := strings.Join(call, " ")
				if strings.Contains(fullCmd, "oro:0.0") {
					architectSendKeys = call
				}
			}
		}
		if architectSendKeys == nil {
			t.Fatal("expected send-keys to oro:0.0 for architect pane")
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
		err := runFullStart(&stdout, 2, "sonnet", spawner, newFakeCmd(), 100*time.Millisecond)
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
		err := runFullStart(&stdout, 2, "sonnet", spawner, newFakeCmd(), 100*time.Millisecond)
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
		err := runFullStart(&stdout, 2, "sonnet", spawner, fakeTmux, 100*time.Millisecond)
		if err == nil {
			t.Fatal("expected error when tmux create fails")
		}
		if !strings.Contains(err.Error(), "tmux") {
			t.Errorf("expected error to mention tmux, got: %v", err)
		}
	})
}

func TestCreateWithManagerPrompt(t *testing.T) {
	t.Run("passes manager prompt to pane 1", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")

		sess := &TmuxSession{Name: "oro", Runner: fake}
		prompt := "You are a test manager."
		err := sess.Create("You are a test architect.", prompt)
		if err != nil {
			t.Fatalf("Create returned error: %v", err)
		}

		// Find the send-keys call for pane 1 (manager).
		var managerCall []string
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "tmux" && call[1] == "send-keys" {
				joined := strings.Join(call, " ")
				if strings.Contains(joined, "oro:0.1") {
					managerCall = call
				}
			}
		}
		if managerCall == nil {
			t.Fatal("expected send-keys for manager pane oro:0.1")
		}
		joined := strings.Join(managerCall, " ")
		if !strings.Contains(joined, "You are a test manager.") {
			t.Errorf("expected manager command to contain prompt, got: %s", joined)
		}
	})
}
