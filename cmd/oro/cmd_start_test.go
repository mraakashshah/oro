package main

import (
	"bufio"
	"bytes"
	"context"
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

	"oro/pkg/dispatcher"
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

func TestCleanStaleWorkerLogs(t *testing.T) {
	t.Run("old dirs deleted, new dirs survive", func(t *testing.T) {
		tmpDir := t.TempDir()
		workersDir := filepath.Join(tmpDir, "workers")
		if err := os.MkdirAll(workersDir, 0o700); err != nil {
			t.Fatal(err)
		}

		// Create an "old" directory and backdate its modtime to 8 days ago.
		oldDir := filepath.Join(workersDir, "worker-old")
		if err := os.MkdirAll(oldDir, 0o700); err != nil {
			t.Fatal(err)
		}
		eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
		if err := os.Chtimes(oldDir, eightDaysAgo, eightDaysAgo); err != nil {
			t.Fatal(err)
		}

		// Create a "new" directory (default modtime = now).
		newDir := filepath.Join(workersDir, "worker-new")
		if err := os.MkdirAll(newDir, 0o700); err != nil {
			t.Fatal(err)
		}

		cleanStaleWorkerLogs(tmpDir, 7*24*time.Hour)

		// Old dir should be gone.
		if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
			t.Errorf("expected old dir to be removed, got err: %v", err)
		}
		// New dir should survive.
		if _, err := os.Stat(newDir); err != nil {
			t.Errorf("expected new dir to survive, got: %v", err)
		}
	})

	t.Run("missing workers dir no error", func(t *testing.T) {
		tmpDir := t.TempDir()
		// workers dir does not exist — should not panic or error.
		cleanStaleWorkerLogs(tmpDir, 7*24*time.Hour)
	})

	t.Run("non-directory entries ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		workersDir := filepath.Join(tmpDir, "workers")
		if err := os.MkdirAll(workersDir, 0o700); err != nil {
			t.Fatal(err)
		}

		// Create a regular file (not a directory) with an old modtime.
		oldFile := filepath.Join(workersDir, "stale.log")
		if err := os.WriteFile(oldFile, []byte("log"), 0o600); err != nil {
			t.Fatal(err)
		}
		eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
		if err := os.Chtimes(oldFile, eightDaysAgo, eightDaysAgo); err != nil {
			t.Fatal(err)
		}

		cleanStaleWorkerLogs(tmpDir, 7*24*time.Hour)

		// File should still exist — only directories are cleaned.
		if _, err := os.Stat(oldFile); err != nil {
			t.Errorf("expected non-dir file to survive, got: %v", err)
		}
	})
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

// TestWireDependencies_SetsPaneRestarter verifies that wireDependencies
// wires up a PaneRestarter using execEnvCmd to build the manager cmdStr.
func TestWireDependencies_SetsPaneRestarter(t *testing.T) {
	t.Run("sets PaneRestarter with manager cmdStr", func(t *testing.T) {
		// Create a mock dispatcher to capture the SetPaneRestarter call.
		mockDispatcher := &dispatcher.Dispatcher{}
		sockPath := "/tmp/test.sock"
		oroHome := "/tmp/oro"

		// Set ORO_PROJECT env var for execEnvCmd.
		t.Setenv("ORO_PROJECT", "test-project")

		// Create a mock command runner.
		runner := &fakeCommandRunner{}

		// Call wireDependencies.
		wireDependencies(mockDispatcher, sockPath, oroHome, runner, false /* daemonOnly */)

		// Assert: paneRestarter must be set (non-nil).
		if mockDispatcher.GetPaneRestarter() == nil {
			t.Fatal("expected paneRestarter to be set, but got nil")
		}

		// Assert: the cmdStr should use execEnvCmd to include manager role
		pr := mockDispatcher.GetPaneRestarter()
		tmuxRestarter, ok := pr.(*dispatcher.TmuxPaneRestarter)
		if !ok {
			t.Fatalf("expected TmuxPaneRestarter, got %T", pr)
		}

		// The cmdStr should contain "claude" from the execEnvCmd output
		cmdStr := tmuxRestarter.CmdStr()
		if cmdStr == "" {
			t.Fatal("expected non-empty cmdStr")
		}
		// Should contain elements from execEnvCmd result
		if !strings.Contains(cmdStr, "claude") {
			t.Errorf("expected cmdStr to contain 'claude', got: %s", cmdStr)
		}
		if !strings.Contains(cmdStr, "ORO_ROLE=manager") {
			t.Errorf("expected cmdStr to contain 'ORO_ROLE=manager', got: %s", cmdStr)
		}
	})
}

// TestStartProgressTimeoutFlag verifies that --progress-timeout and --review-timeout
// flags wire through to buildDispatcher's Config.
func TestStartProgressTimeoutFlag(t *testing.T) {
	t.Run("explicit flags set Config timeouts", func(t *testing.T) {
		cmd := newStartCmd()
		cmd.SetArgs([]string{"--progress-timeout=20m", "--review-timeout=30m"})
		if err := cmd.ParseFlags([]string{"--progress-timeout=20m", "--review-timeout=30m"}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}

		pt, err := cmd.Flags().GetDuration("progress-timeout")
		if err != nil {
			t.Fatalf("GetDuration progress-timeout: %v", err)
		}
		if pt != 20*time.Minute {
			t.Errorf("progress-timeout: got %v, want 20m", pt)
		}

		rt, err := cmd.Flags().GetDuration("review-timeout")
		if err != nil {
			t.Fatalf("GetDuration review-timeout: %v", err)
		}
		if rt != 30*time.Minute {
			t.Errorf("review-timeout: got %v, want 30m", rt)
		}
	})

	t.Run("omitted flags default to zero (dispatcher applies 10m/15m)", func(t *testing.T) {
		cmd := newStartCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("ParseFlags: %v", err)
		}

		pt, _ := cmd.Flags().GetDuration("progress-timeout")
		if pt != 0 {
			t.Errorf("progress-timeout default: got %v, want 0 (dispatcher default)", pt)
		}

		rt, _ := cmd.Flags().GetDuration("review-timeout")
		if rt != 0 {
			t.Errorf("review-timeout default: got %v, want 0 (dispatcher default)", rt)
		}
	})

	t.Run("ExecDaemonSpawner forwards timeout flags to child", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{
			ProgressTimeout: 20 * time.Minute,
			ReviewTimeout:   30 * time.Minute,
		}
		args := spawner.buildArgs(3)
		argStr := strings.Join(args, " ")
		if !strings.Contains(argStr, "--progress-timeout=20m0s") {
			t.Errorf("expected --progress-timeout=20m0s in args, got: %s", argStr)
		}
		if !strings.Contains(argStr, "--review-timeout=30m0s") {
			t.Errorf("expected --review-timeout=30m0s in args, got: %s", argStr)
		}
	})

	t.Run("ExecDaemonSpawner omits zero-value timeouts", func(t *testing.T) {
		spawner := &ExecDaemonSpawner{}
		args := spawner.buildArgs(2)
		argStr := strings.Join(args, " ")
		if strings.Contains(argStr, "progress-timeout") {
			t.Errorf("zero progress-timeout should not appear in args, got: %s", argStr)
		}
		if strings.Contains(argStr, "review-timeout") {
			t.Errorf("zero review-timeout should not appear in args, got: %s", argStr)
		}
	})
}

func TestRegenerateProjectSettings_WritesFile(t *testing.T) {
	t.Run("WritesFile", func(t *testing.T) {
		tmpHome := t.TempDir()
		var w bytes.Buffer

		regenerateProjectSettings(&w, tmpHome, "myproject")

		settingsPath := filepath.Join(tmpHome, "projects", "myproject", "settings.json")
		data, err := os.ReadFile(settingsPath) //nolint:gosec // test reads from TempDir path
		if err != nil {
			t.Fatalf("expected settings.json to be written: %v", err)
		}
		if !strings.Contains(string(data), "compact_trigger.py") {
			t.Errorf("expected settings.json to contain 'compact_trigger.py', got: %s", string(data))
		}
	})

	t.Run("EmptyProjectName_Noop", func(t *testing.T) {
		tmpHome := t.TempDir()
		var w bytes.Buffer

		regenerateProjectSettings(&w, tmpHome, "")

		entries, err := os.ReadDir(tmpHome)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected no files written for empty project name, got %d entries", len(entries))
		}
	})

	t.Run("CreatesProjectDir", func(t *testing.T) {
		tmpHome := t.TempDir()
		var w bytes.Buffer

		regenerateProjectSettings(&w, tmpHome, "myproject")

		projDir := filepath.Join(tmpHome, "projects", "myproject")
		if _, err := os.Stat(projDir); err != nil {
			t.Errorf("expected project dir to be created: %v", err)
		}
		settingsPath := filepath.Join(projDir, "settings.json")
		if _, err := os.Stat(settingsPath); err != nil {
			t.Errorf("expected settings.json to be created: %v", err)
		}
	})
}

// fakeCommandRunner is a mock CommandRunner for testing.
type fakeCommandRunner struct{}

func (f *fakeCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return []byte{}, nil
}

// TestWireDependencies_DaemonOnly_SkipsPaneRestarter verifies that when
// daemonOnly=true, wireDependencies does NOT set a PaneRestarter on the
// dispatcher, preventing pane_restart_failed spam in daemon mode.
func TestWireDependencies_DaemonOnly_SkipsPaneRestarter(t *testing.T) {
	t.Run("daemon mode: paneRestarter is nil", func(t *testing.T) {
		d := &dispatcher.Dispatcher{}
		sockPath := "/tmp/test-daemon.sock"
		oroHome := "/tmp/oro-daemon"
		runner := &fakeCommandRunner{}

		wireDependencies(d, sockPath, oroHome, runner, true /* daemonOnly */)

		if d.GetPaneRestarter() != nil {
			t.Fatal("expected paneRestarter to be nil in daemon mode, but it was set")
		}
	})

	t.Run("non-daemon mode: paneRestarter is set", func(t *testing.T) {
		d := &dispatcher.Dispatcher{}
		sockPath := "/tmp/test-nodaemon.sock"
		oroHome := "/tmp/oro-nodaemon"
		runner := &fakeCommandRunner{}

		wireDependencies(d, sockPath, oroHome, runner, false /* daemonOnly */)

		if d.GetPaneRestarter() == nil {
			t.Fatal("expected paneRestarter to be set in non-daemon mode, but got nil")
		}
	})
}
