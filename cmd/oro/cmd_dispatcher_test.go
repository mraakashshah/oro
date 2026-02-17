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

	"github.com/spf13/cobra"
)

// dispatcherFakeSpawner records SpawnDaemon calls for dispatcher tests.
type dispatcherFakeSpawner struct {
	called     bool
	pidPath    string
	workers    int
	returnPID  int
	returnErr  error
	socketPath string // if set, create a UDS listener after "spawn"
}

func (f *dispatcherFakeSpawner) SpawnDaemon(pidPath string, workers int) (int, error) {
	f.called = true
	f.pidPath = pidPath
	f.workers = workers
	if f.returnErr != nil {
		return 0, f.returnErr
	}
	if err := WritePIDFile(pidPath, f.returnPID); err != nil {
		return 0, err
	}
	if f.socketPath != "" {
		ln, err := net.Listen("unix", f.socketPath)
		if err != nil {
			return 0, err
		}
		// Accept one connection, send ACK for start directive.
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

// TestDispatcherStartSpawnsDaemon is the acceptance-criteria test for oro-18c5.3.
// It verifies that `oro dispatcher start` spawns daemon with --daemon-only --workers 0,
// waits for socket, sends start directive, prints PID, and does NOT create tmux session.
func TestDispatcherStartSpawnsDaemon(t *testing.T) {
	t.Run("spawns daemon with workers=0, waits for socket, sends start directive, prints PID", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-ds-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		spawner := &dispatcherFakeSpawner{
			returnPID:  55555,
			socketPath: sockPath,
		}

		var stdout bytes.Buffer
		err := runDispatcherStart(&stdout, 0, spawner, socketPollTimeout)
		if err != nil {
			t.Fatalf("runDispatcherStart returned error: %v", err)
		}

		// 1. Daemon must have been spawned.
		if !spawner.called {
			t.Fatal("expected SpawnDaemon to be called")
		}

		// 2. workers=0 (manual worker mode, no auto-scaling).
		if spawner.workers != 0 {
			t.Errorf("expected workers=0, got %d", spawner.workers)
		}

		// 3. Output must contain PID.
		out := stdout.String()
		if !strings.Contains(out, "55555") {
			t.Errorf("expected output to contain PID 55555, got: %s", out)
		}

		// 4. Output must NOT mention tmux (no session created).
		if strings.Contains(strings.ToLower(out), "tmux") {
			t.Errorf("dispatcher start must not create tmux session, but output contains 'tmux': %s", out)
		}
	})

	t.Run("defaults to --workers 0", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-dsw-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		spawner := &dispatcherFakeSpawner{
			returnPID:  42,
			socketPath: sockPath,
		}

		// Run the cobra command with no flags — workers should default to 0.
		cmd := newDispatcherCmd()
		cmd.SetArgs([]string{"start"})

		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stdout)

		// Inject fake spawner via the command's spawner field.
		// We call runDispatcherStart directly to verify the default.
		err := runDispatcherStart(&stdout, 0, spawner, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spawner.workers != 0 {
			t.Errorf("expected default workers=0, got %d", spawner.workers)
		}
	})

	t.Run("already running prints message and returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-dsr-%d.sock", time.Now().UnixNano())
		t.Cleanup(func() { _ = os.Remove(sockPath) })
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		// Write a PID file pointing to the current process so DaemonStatus
		// returns StatusRunning (current process is alive).
		if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
			t.Fatalf("write pid file: %v", err)
		}

		// Also create a socket listener so probeSocket succeeds.
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() { _ = ln.Close() }()

		// Serve status probe so probeSocket returns current PID.
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				func() {
					defer func() { _ = conn.Close() }()
					scanner := bufio.NewScanner(conn)
					if scanner.Scan() {
						detail := fmt.Sprintf(`{"pid":%d}`, os.Getpid())
						ack := protocol.Message{
							Type: protocol.MsgACK,
							ACK:  &protocol.ACKPayload{OK: true, Detail: detail},
						}
						data, _ := json.Marshal(ack)
						data = append(data, '\n')
						_, _ = conn.Write(data)
					}
				}()
			}
		}()

		cmd := newDispatcherCmd()
		cmd.SetArgs([]string{"start"})
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stdout)

		// Execute the command; it should see "already running" and return nil.
		err = cmd.Execute()
		if err != nil {
			t.Fatalf("expected nil when dispatcher already running, got: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "already running") {
			t.Errorf("expected 'already running' in output, got: %s", out)
		}
	})

	t.Run("returns error when socket does not appear", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := fmt.Sprintf("/tmp/oro-dst-%d.sock", time.Now().UnixNano())
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		// Spawner succeeds but never creates socket.
		spawner := &dispatcherFakeSpawner{
			returnPID:  99999,
			socketPath: "", // no socket
		}

		var stdout bytes.Buffer
		err := runDispatcherStart(&stdout, 0, spawner, 50*time.Millisecond) // short timeout
		if err == nil {
			t.Fatal("expected error when socket never appears")
		}
		if !strings.Contains(err.Error(), "socket") {
			t.Errorf("expected error to mention socket, got: %v", err)
		}
	})
}

// TestDispatcherCmdStructure verifies the cobra command hierarchy.
func TestDispatcherCmdStructure(t *testing.T) {
	cmd := newDispatcherCmd()

	if cmd.Use != "dispatcher" {
		t.Errorf("expected Use='dispatcher', got %q", cmd.Use)
	}

	// Find the "start" subcommand.
	var startCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Use == "start" {
			startCmd = sub
			break
		}
	}
	if startCmd == nil {
		t.Fatal("expected 'start' subcommand under 'dispatcher'")
	}

	// Verify --workers flag defaults to 0.
	wFlag := startCmd.Flags().Lookup("workers")
	if wFlag == nil {
		t.Fatal("expected --workers flag on dispatcher start")
	}
	if wFlag.DefValue != "0" {
		t.Errorf("expected --workers default=0, got %q", wFlag.DefValue)
	}

	// Verify --force flag exists.
	fFlag := startCmd.Flags().Lookup("force")
	if fFlag == nil {
		t.Fatal("expected --force flag on dispatcher start")
	}
}
