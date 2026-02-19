package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestStartReadsProjectConfig(t *testing.T) {
	t.Run("reads project name from .oro/config.yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroDir := filepath.Join(tmpDir, ".oro")
		if err := os.MkdirAll(oroDir, 0o755); err != nil { //nolint:gosec // test dir
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(oroDir, "config.yaml"), []byte("project: myproject\nlanguages:\n  go:\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		name, err := readProjectConfig(tmpDir)
		if err != nil {
			t.Fatalf("readProjectConfig failed: %v", err)
		}
		if name != "myproject" {
			t.Errorf("expected 'myproject', got %q", name)
		}
	})

	t.Run("returns empty string when .oro/config.yaml missing", func(t *testing.T) {
		tmpDir := t.TempDir()

		name, err := readProjectConfig(tmpDir)
		if err != nil {
			t.Fatalf("readProjectConfig should not error on missing config: %v", err)
		}
		if name != "" {
			t.Errorf("expected empty string, got %q", name)
		}
	})

	t.Run("ORO_HOME is set for child processes", func(t *testing.T) {
		// resolveOroHome should return ORO_HOME when set
		t.Setenv("ORO_HOME", "/custom/oro")
		home, err := resolveOroHome()
		if err != nil {
			t.Fatalf("resolveOroHome failed: %v", err)
		}
		if home != "/custom/oro" {
			t.Errorf("expected /custom/oro, got %q", home)
		}
	})
}

func TestDaemonStartupCleansWorkerLogs(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)

	// Create workers dir with some files to simulate stale logs.
	workersDir := tmpDir + "/workers"
	if err := os.MkdirAll(workersDir, 0o700); err != nil {
		t.Fatalf("setup workers dir: %v", err)
	}
	staleLog := workersDir + "/worker-123.log"
	if err := os.WriteFile(staleLog, []byte("stale log content"), 0o600); err != nil {
		t.Fatalf("create stale log: %v", err)
	}

	// cleanWorkerLogs should wipe the directory and recreate it empty.
	cleanWorkerLogs(tmpDir)

	// Assert: workers dir exists but is empty.
	entries, err := os.ReadDir(workersDir)
	if err != nil {
		t.Fatalf("ReadDir workers: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected workers dir to be empty, got %d entries", len(entries))
	}
}

func TestDaemonStartupCleansWorkerLogs_MissingDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("ORO_HOME", tmpDir)

	// cleanWorkerLogs should not fail when workers dir doesn't exist yet.
	cleanWorkerLogs(tmpDir)

	// workers dir should be created.
	workersDir := tmpDir + "/workers"
	if _, err := os.Stat(workersDir); err != nil {
		t.Errorf("expected workers dir to be created, got: %v", err)
	}
}

func TestStartPrintsQuitHint(t *testing.T) {
	t.Run("prints navigation hint when attaching (not detached)", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-hint-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		fakeTmux := newFakeCmd()
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fakeTmux, "oro", ArchitectNudge(), ManagerNudge())

		spawner := &fakeSpawner{
			returnPID:  99999,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		// detach=false means attach, so hint should be printed
		err := runFullStart(&stdout, 2, "sonnet", "", spawner, fakeTmux, func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, false)
		// Expect error because AttachInteractive tries to attach to real tmux
		if err == nil {
			t.Fatal("expected error from AttachInteractive in test environment")
		}

		// Verify hint was printed before attach attempt
		out := stdout.String()
		if !strings.Contains(out, "ctrl-b 0/1") {
			t.Errorf("expected hint to contain 'ctrl-b 0/1', got: %s", out)
		}
		if !strings.Contains(out, "ctrl-b d") {
			t.Errorf("expected hint to contain 'ctrl-b d', got: %s", out)
		}
		if !strings.Contains(out, "oro stop") {
			t.Errorf("expected hint to contain 'oro stop', got: %s", out)
		}
	})

	t.Run("does not print hint when detached", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-detach-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		fakeTmux := newFakeCmd()
		fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubPaneReady(fakeTmux, "oro", ArchitectNudge(), ManagerNudge())

		spawner := &fakeSpawner{
			returnPID:  88888,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		// detach=true means no attach, so hint should NOT be printed
		err := runFullStart(&stdout, 2, "sonnet", "", spawner, fakeTmux, func(int) error { return nil }, 100*time.Millisecond, noopSleep, 50*time.Millisecond, true)
		if err != nil {
			t.Fatalf("runFullStart with detach should succeed, got: %v", err)
		}

		// Verify hint was NOT printed (only detach instructions)
		out := stdout.String()
		if strings.Contains(out, "ctrl-b 0/1") || strings.Contains(out, "switch panes") {
			t.Errorf("hint should not be printed in detached mode, got: %s", out)
		}
		if !strings.Contains(out, "detached") {
			t.Errorf("expected detached message, got: %s", out)
		}
	})
}

// TestRunFullStartKillsDaemonOnSessionCreateError verifies that when
// sess.Create() fails, runFullStart calls killFn(pid) to clean up the
// orphaned daemon process before returning the original error.
func TestRunFullStartKillsDaemonOnSessionCreateError(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := fmt.Sprintf("/tmp/oro-kill-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })

	// Use a deterministic CLAUDE_CONFIG_DIR so we can compute the exact
	// fakeCmd key for the tmux new-session call.
	claudeConfigDir := filepath.Join(tmpDir, "claude-config")
	if err := os.MkdirAll(claudeConfigDir, 0o750); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", filepath.Join(tmpDir, "state.db"))
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfigDir)

	// Compute the exact key that fakeCmd will see for new-session.
	// execEnvCmd("architect", "") uses CLAUDE_CONFIG_DIR to build the command.
	archConfigDir := filepath.Join(claudeConfigDir, "roles", "architect")
	newSessionCmd := fmt.Sprintf(
		"exec env ORO_ROLE=architect BD_ACTOR=architect GIT_AUTHOR_NAME=architect CLAUDE_CONFIG_DIR=%s claude",
		archConfigDir,
	)
	newSessionKey := key("tmux", "new-session", "-d", "-s", "oro", "-n", "architect", newSessionCmd)

	// Spawner starts a real sleep 1000 child and returns its PID.
	var spawnedPID int
	spawnerFn := &killTestSpawner{
		socketPath: sockPath,
		onSpawn: func(pidPath string) (int, error) {
			cmd := exec.Command("sleep", "1000")
			if err := cmd.Start(); err != nil {
				return 0, fmt.Errorf("start sleep 1000: %w", err)
			}
			spawnedPID = cmd.Process.Pid
			// Write PID file so the daemon looks like a real process.
			if err := WritePIDFile(pidPath, spawnedPID); err != nil {
				return 0, err
			}
			return spawnedPID, nil
		},
	}

	fakeTmux := newFakeCmd()
	// has-session returns error (no existing session).
	fakeTmux.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// new-session fails: simulates tmux not available or misconfigured.
	fakeTmux.errs[newSessionKey] = fmt.Errorf("tmux new-session: simulated failure")

	killCalled := false
	killFn := func(pid int) error {
		killCalled = true
		return syscall.Kill(pid, syscall.SIGKILL)
	}

	var stdout bytes.Buffer
	err := runFullStart(&stdout, 2, "sonnet", "", spawnerFn, fakeTmux, killFn, 200*time.Millisecond, noopSleep, 50*time.Millisecond, false)
	if err == nil {
		t.Fatal("expected runFullStart to return error when tmux session create fails")
	}
	// The error should wrap the tmux session creation failure.
	if !strings.Contains(err.Error(), "create tmux session") {
		t.Errorf("expected error to mention 'create tmux session', got: %v", err)
	}

	// killFn must have been called to clean up the orphaned daemon.
	if !killCalled {
		t.Error("expected killFn to be called after tmux session creation failed")
	}

	// The sleep 1000 process must be dead.
	if spawnedPID == 0 {
		t.Fatal("spawner was never called or PID not captured")
	}
	// Reap the zombie: Find+Wait collects the exit status so the PID is freed.
	proc, findErr := os.FindProcess(spawnedPID)
	if findErr != nil {
		t.Fatalf("os.FindProcess(%d): %v", spawnedPID, findErr)
	}
	// Wait with a deadline — SIGKILL is instantaneous, so we should not need long.
	done := make(chan error, 1)
	go func() { _, err := proc.Wait(); done <- err }()
	select {
	case <-done:
		// Process exited — good.
	case <-time.After(2 * time.Second):
		t.Errorf("sleep 1000 (PID %d) did not exit within 2s after SIGKILL cleanup", spawnedPID)
	}
}

// killTestSpawner is a DaemonSpawner that delegates to onSpawn and also
// creates a UDS listener so sendStartDirective can connect.
type killTestSpawner struct {
	socketPath string
	onSpawn    func(pidPath string) (int, error)
}

func (s *killTestSpawner) SpawnDaemon(pidPath string, workers int) (int, error) {
	pid, err := s.onSpawn(pidPath)
	if err != nil {
		return 0, err
	}
	if s.socketPath != "" {
		ln, listenErr := net.Listen("unix", s.socketPath)
		if listenErr != nil {
			return 0, listenErr
		}
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close(); _ = ln.Close() }()
			scanner := bufio.NewScanner(conn)
			if scanner.Scan() {
				ack := protocol.Message{
					Type: protocol.MsgACK,
					ACK:  &protocol.ACKPayload{OK: true, Detail: "started"},
				}
				data, _ := json.Marshal(ack)
				data = append(data, '\n')
				_, _ = conn.Write(data)
			}
		}()
	}
	return pid, nil
}
