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
		// Start a real UDS listener so sendStartDirective can connect.
		ln, listenErr := net.Listen("unix", f.socketPath)
		if listenErr != nil {
			return 0, listenErr
		}
		// Accept one connection and ACK the directive.
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
		stubCapturePaneReady(fakeTmux, "oro")

		spawner := &fakeSpawner{
			returnPID:  12345,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 3, "sonnet", spawner, fakeTmux, 100*time.Millisecond, noopSleep, 50*time.Millisecond)
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
		newSessionCall := findCall(fakeTmux.calls, "new-session")
		if newSessionCall == nil {
			t.Fatal("expected tmux new-session to be called")
		}

		// 3. Verify both panes launch interactive claude with role env vars.
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

		// Architect pane (0): should have launch + nudge injection.
		if len(pane0Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 0, got %d", len(pane0Calls))
		}
		p0Launch := strings.Join(pane0Calls[0], " ")
		for _, envVar := range []string{"ORO_ROLE=architect", "BD_ACTOR=architect", "GIT_AUTHOR_NAME=architect"} {
			if !strings.Contains(p0Launch, envVar) {
				t.Errorf("pane 0 should set %s, got: %s", envVar, p0Launch)
			}
		}
		if !strings.Contains(p0Launch, "claude") {
			t.Errorf("pane 0 should launch claude, got: %s", p0Launch)
		}
		if strings.Contains(p0Launch, "claude -p") {
			t.Errorf("pane 0 should use interactive claude, not 'claude -p', got: %s", p0Launch)
		}
		// Verify architect nudge is injected (short, not the full beacon).
		p0Nudge := strings.Join(pane0Calls[1], " ")
		if !strings.Contains(p0Nudge, "oro architect") {
			t.Errorf("pane 0 nudge should contain 'oro architect', got: %s", p0Nudge)
		}

		// Manager pane (1): should have launch + nudge injection.
		if len(pane1Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 1, got %d", len(pane1Calls))
		}
		p1Launch := strings.Join(pane1Calls[0], " ")
		for _, envVar := range []string{"ORO_ROLE=manager", "BD_ACTOR=manager", "GIT_AUTHOR_NAME=manager"} {
			if !strings.Contains(p1Launch, envVar) {
				t.Errorf("pane 1 should set %s, got: %s", envVar, p1Launch)
			}
		}
		if !strings.Contains(p1Launch, "claude") {
			t.Errorf("pane 1 should launch claude, got: %s", p1Launch)
		}
		if strings.Contains(p1Launch, "claude -p") {
			t.Errorf("pane 1 should use interactive claude, not 'claude -p', got: %s", p1Launch)
		}
		// Verify manager nudge is injected (short, not the full beacon).
		p1Nudge := strings.Join(pane1Calls[1], " ")
		if !strings.Contains(p1Nudge, "oro manager") {
			t.Errorf("pane 1 nudge should contain 'oro manager', got: %s", p1Nudge)
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
		err := runFullStart(&stdout, 2, "sonnet", spawner, newFakeCmd(), 100*time.Millisecond, noopSleep, 50*time.Millisecond)
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
		err := runFullStart(&stdout, 2, "sonnet", spawner, newFakeCmd(), 100*time.Millisecond, noopSleep, 50*time.Millisecond)
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
		// new-session fails
		fakeTmux.errs[key("tmux", "new-session", "-d", "-s", "oro")] = fmt.Errorf("tmux not installed")

		spawner := &fakeSpawner{
			returnPID:  12345,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		err := runFullStart(&stdout, 2, "sonnet", spawner, fakeTmux, 100*time.Millisecond, noopSleep, 50*time.Millisecond)
		if err == nil {
			t.Fatal("expected error when tmux create fails")
		}
		if !strings.Contains(err.Error(), "tmux") {
			t.Errorf("expected error to mention tmux, got: %v", err)
		}
	})
}

func TestCreateWithNudges(t *testing.T) {
	t.Run("injects both nudges via send-keys to respective panes", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubCapturePaneReady(fake, "oro")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
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

		// Pane 0: nudge injection (second send-keys) should contain architect nudge.
		if len(pane0Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 0, got %d", len(pane0Calls))
		}
		p0Nudge := strings.Join(pane0Calls[1], " ")
		if !strings.Contains(p0Nudge, "You are a test architect.") {
			t.Errorf("pane 0 nudge should contain architect text, got: %s", p0Nudge)
		}

		// Pane 1: nudge injection (second send-keys) should contain manager nudge.
		if len(pane1Calls) < 2 {
			t.Fatalf("expected at least 2 send-keys to pane 1, got %d", len(pane1Calls))
		}
		p1Nudge := strings.Join(pane1Calls[1], " ")
		if !strings.Contains(p1Nudge, "You are a test manager.") {
			t.Errorf("pane 1 nudge should contain manager text, got: %s", p1Nudge)
		}
	})

	t.Run("neither pane uses claude -p", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		stubCapturePaneReady(fake, "oro")

		sess := &TmuxSession{Name: "oro", Runner: fake, Sleeper: noopSleep, ReadyTimeout: time.Second, BeaconTimeout: 50 * time.Millisecond}
		err := sess.Create("architect nudge", "manager nudge")
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

	t.Run("nudges are short not full beacons", func(t *testing.T) {
		// Verify that ArchitectNudge and ManagerNudge are significantly shorter
		// than the full beacon content, confirming the Gastown pattern.
		archNudge := ArchitectNudge()
		archBeacon := ArchitectBeacon()
		if len(archNudge) >= len(archBeacon) {
			t.Errorf("ArchitectNudge (%d chars) should be much shorter than ArchitectBeacon (%d chars)",
				len(archNudge), len(archBeacon))
		}
		if len(archNudge) > 500 {
			t.Errorf("ArchitectNudge should be a short nudge (<500 chars), got %d chars", len(archNudge))
		}

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
}
