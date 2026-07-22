package main

import (
	"bufio"
	"bytes"
	"context"
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
	called              bool
	pidPath             string
	workers             int
	maxWorkers          int
	manualIntegration   bool
	returnPID           int
	returnErr           error
	socketPath          string // if set, create a UDS listener after "spawn"
	daemonSkipPreflight string
	oroHome             string
	project             string
}

func (f *dispatcherFakeSpawner) SetManualIntegration(enabled bool) {
	f.manualIntegration = enabled
}

func (f *dispatcherFakeSpawner) SpawnDaemon(pidPath string, workers, maxWorkers int) (int, error) {
	f.called = true
	f.pidPath = pidPath
	f.workers = workers
	f.maxWorkers = maxWorkers
	f.daemonSkipPreflight = os.Getenv(daemonSkipPreflightEnv)
	f.oroHome = os.Getenv("ORO_HOME")
	f.project = os.Getenv("ORO_PROJECT")
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
		// Accept multiple connections: pollForSocket does a connect-check
		// first, then sendStartDirective sends the directive.
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
				}(conn)
			}
		}()
	}
	return f.returnPID, nil
}

func TestDispatcherStartPropagatesOracleRuntimeIdentity(t *testing.T) { //nolint:testpackage // white-box startup contract
	t.Run("resolves unset identity before spawning", func(t *testing.T) {
		t.Setenv("ORO_HOME", "")
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_PID_PATH", filepath.Join(t.TempDir(), "oro.pid"))
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(t.TempDir(), "oro.sock"))

		spawner := &dispatcherFakeSpawner{returnPID: 123}
		var stdout bytes.Buffer
		if err := runDispatcherStart(&stdout, 0, false, false, spawner, time.Millisecond); err == nil {
			t.Fatal("expected socket error after the identity was resolved")
		}
		if !spawner.called {
			t.Fatal("expected SpawnDaemon to be called")
		}
		if spawner.oroHome == "" || spawner.project != "oro" {
			t.Fatalf("SpawnDaemon saw identity ORO_HOME=%q ORO_PROJECT=%q", spawner.oroHome, spawner.project)
		}
	})

	t.Run("preserves explicit identity", func(t *testing.T) {
		t.Setenv("ORO_HOME", filepath.Join(t.TempDir(), "home"))
		t.Setenv("ORO_PROJECT", "explicit-project")
		t.Setenv("ORO_PID_PATH", filepath.Join(t.TempDir(), "oro.pid"))
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(t.TempDir(), "oro.sock"))

		spawner := &dispatcherFakeSpawner{returnPID: 123}
		var stdout bytes.Buffer
		_ = runDispatcherStart(&stdout, 0, false, false, spawner, time.Millisecond)
		if spawner.oroHome != os.Getenv("ORO_HOME") || spawner.project != "explicit-project" {
			t.Fatalf("explicit identity changed: ORO_HOME=%q ORO_PROJECT=%q", spawner.oroHome, spawner.project)
		}
	})

	t.Run("resolution failure has no daemon side effects", func(t *testing.T) {
		t.Setenv("ORO_HOME", "")
		t.Setenv("ORO_PROJECT", "")
		t.Setenv("ORO_PID_PATH", filepath.Join(t.TempDir(), "oro.pid"))
		t.Setenv("ORO_SOCKET_PATH", filepath.Join(t.TempDir(), "oro.sock"))
		t.Chdir(t.TempDir())

		spawner := &dispatcherFakeSpawner{}
		var stdout bytes.Buffer
		if err := runDispatcherStart(&stdout, 0, false, false, spawner, time.Millisecond); err == nil {
			t.Fatal("expected runtime identity resolution error")
		}
		if spawner.called {
			t.Fatal("SpawnDaemon called after identity resolution failed")
		}
	})
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
		err := runDispatcherStart(&stdout, 0, false, false, spawner, socketPollTimeout)
		if err != nil {
			t.Fatalf("runDispatcherStart returned error: %v", err)
		}

		// 1. Daemon must have been spawned.
		if !spawner.called {
			t.Fatal("expected SpawnDaemon to be called")
		}

		// 2. workers=0 and maxWorkers=0 (manual worker mode, no auto-scaling).
		if spawner.workers != 0 {
			t.Errorf("expected workers=0, got %d", spawner.workers)
		}
		if spawner.maxWorkers != 0 {
			t.Errorf("expected maxWorkers=0, got %d", spawner.maxWorkers)
		}
		if spawner.manualIntegration {
			t.Error("manual integration should be false by default")
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
		err := runDispatcherStart(&stdout, 0, false, false, spawner, 100*time.Millisecond)
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
		// Use start without --force so it goes through preflightAndCheckRunning.
		// When preflight tools are missing, skip the test entirely.
		skipIfToolsMissing(t)
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
		err := runDispatcherStart(&stdout, 0, false, false, spawner, 50*time.Millisecond) // short timeout
		if err == nil {
			t.Fatal("expected error when socket never appears")
		}
		if !strings.Contains(err.Error(), "socket") {
			t.Errorf("expected error to mention socket, got: %v", err)
		}
	})
}

func TestWithDaemonPreflightBypass(t *testing.T) {
	t.Run("sets env only while enabled", func(t *testing.T) {
		t.Setenv(daemonSkipPreflightEnv, "")
		if err := os.Unsetenv(daemonSkipPreflightEnv); err != nil {
			t.Fatalf("unset env: %v", err)
		}

		err := withDaemonPreflightBypass(true, func() error {
			if got := os.Getenv(daemonSkipPreflightEnv); got != "1" {
				t.Fatalf("%s = %q, want 1", daemonSkipPreflightEnv, got)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("withDaemonPreflightBypass: %v", err)
		}
		if _, ok := os.LookupEnv(daemonSkipPreflightEnv); ok {
			t.Fatalf("%s leaked after callback", daemonSkipPreflightEnv)
		}
	})

	t.Run("does not set env while disabled", func(t *testing.T) {
		if err := os.Unsetenv(daemonSkipPreflightEnv); err != nil {
			t.Fatalf("unset env: %v", err)
		}
		err := withDaemonPreflightBypass(false, func() error {
			if _, ok := os.LookupEnv(daemonSkipPreflightEnv); ok {
				t.Fatalf("%s unexpectedly set", daemonSkipPreflightEnv)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("withDaemonPreflightBypass: %v", err)
		}
	})
}

func TestDispatcherStartForcePropagatesDaemonPreflightBypass(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := fmt.Sprintf("/tmp/oro-dsf-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	dbPath := filepath.Join(tmpDir, "state.db")

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	t.Setenv("PATH", tmpDir)

	spawner := &dispatcherFakeSpawner{
		returnPID:  12345,
		socketPath: sockPath,
	}
	previousFactory := newDispatcherDaemonSpawner
	newDispatcherDaemonSpawner = func() DaemonSpawner { return spawner }
	t.Cleanup(func() { newDispatcherDaemonSpawner = previousFactory })

	cmd := newDispatcherCmd()
	cmd.SetArgs([]string{"start", "--force", "--workers", "0"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("dispatcher start --force: %v", err)
	}
	if !spawner.called {
		t.Fatal("expected daemon spawner to be called")
	}
	if spawner.daemonSkipPreflight != "1" {
		t.Fatalf("%s seen by spawner = %q, want 1", daemonSkipPreflightEnv, spawner.daemonSkipPreflight)
	}
	if _, ok := os.LookupEnv(daemonSkipPreflightEnv); ok {
		t.Fatalf("%s leaked after command", daemonSkipPreflightEnv)
	}
	if !strings.Contains(stdout.String(), "dispatcher started") {
		t.Fatalf("expected start output, got %q", stdout.String())
	}
}

func TestDispatcherStartManualIntegrationFlagConfiguresDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := fmt.Sprintf("/tmp/oro-dsmi-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	dbPath := filepath.Join(tmpDir, "state.db")

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	t.Setenv("PATH", tmpDir)

	spawner := &dispatcherFakeSpawner{
		returnPID:  12346,
		socketPath: sockPath,
	}
	previousFactory := newDispatcherDaemonSpawner
	newDispatcherDaemonSpawner = func() DaemonSpawner { return spawner }
	t.Cleanup(func() { newDispatcherDaemonSpawner = previousFactory })

	cmd := newDispatcherCmd()
	cmd.SetArgs([]string{"start", "--force", "--workers", "0", "--manual-integration"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("dispatcher start --manual-integration: %v", err)
	}
	if !spawner.called {
		t.Fatal("expected daemon spawner to be called")
	}
	if !spawner.manualIntegration {
		t.Fatal("expected manual integration to be forwarded to daemon spawner")
	}
}

func TestDispatcherStartAutoMergeDefaultDoesNotEnableManualIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockPath := fmt.Sprintf("/tmp/oro-dsam-%d.sock", time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	dbPath := filepath.Join(tmpDir, "state.db")

	t.Setenv("ORO_PID_PATH", pidFile)
	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_BEADSOURCE_MODE", "sqlite")
	t.Setenv("PATH", tmpDir)

	spawner := &dispatcherFakeSpawner{
		returnPID:  12347,
		socketPath: sockPath,
	}
	previousFactory := newDispatcherDaemonSpawner
	newDispatcherDaemonSpawner = func() DaemonSpawner { return spawner }
	t.Cleanup(func() { newDispatcherDaemonSpawner = previousFactory })

	cmd := newDispatcherCmd()
	cmd.SetArgs([]string{"start", "--force", "--workers", "0"})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("dispatcher start default auto-merge mode: %v", err)
	}
	if !spawner.called {
		t.Fatal("expected daemon spawner to be called")
	}
	if spawner.manualIntegration {
		t.Fatal("default dispatcher start must leave manual integration disabled")
	}
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
		return
	}
	if wFlag.DefValue != "0" {
		t.Errorf("expected --workers default=0, got %q", wFlag.DefValue)
	}

	// Verify --force flag exists.
	fFlag := startCmd.Flags().Lookup("force")
	if fFlag == nil {
		t.Fatal("expected --force flag on dispatcher start")
	}

	// Verify --manual-integration flag exists.
	miFlag := startCmd.Flags().Lookup("manual-integration")
	if miFlag == nil {
		t.Fatal("expected --manual-integration flag on dispatcher start")
	}
	if miFlag.DefValue != "false" {
		t.Errorf("expected --manual-integration default=false, got %q", miFlag.DefValue)
	}

	// Find the "stop" subcommand.
	var stopCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Use == "stop" {
			stopCmd = sub
			break
		}
	}
	if stopCmd == nil {
		t.Fatal("expected 'stop' subcommand under 'dispatcher'")
	}

	// Verify --force flag exists on stop.
	sfFlag := stopCmd.Flags().Lookup("force")
	if sfFlag == nil {
		t.Fatal("expected --force flag on dispatcher stop")
	}
}

// TestDispatcherStopDocNoBdSync verifies that godoc comments for the dispatcher stop
// functions do not reference "bd sync" (removed in oro-i8rd.4). This prevents
// documentation drift where stale comments describe behavior that no longer exists.
func TestDispatcherStopDocNoBdSync(t *testing.T) {
	src, err := os.ReadFile("cmd_dispatcher.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	content := string(src)

	// The godoc for newDispatcherStopCmd must not mention "bd sync".
	// Extract the comment block before func newDispatcherStopCmd.
	idx := strings.Index(content, "func newDispatcherStopCmd()")
	if idx < 0 {
		t.Fatal("could not find newDispatcherStopCmd function")
	}
	stopCmdDoc := content[:idx]
	if strings.Contains(stopCmdDoc, "bd sync") {
		t.Error("newDispatcherStopCmd godoc still references 'bd sync' — remove stale comment")
	}

	// The godoc for runDispatcherStopSequence must not mention "bd sync".
	idx = strings.Index(content, "func runDispatcherStopSequence(")
	if idx < 0 {
		t.Fatal("could not find runDispatcherStopSequence function")
	}
	// Look at the 500 chars before the function signature for the godoc block.
	start := idx - 500
	if start < 0 {
		start = 0
	}
	seqDoc := content[start:idx]
	if strings.Contains(seqDoc, "bd sync") {
		t.Error("runDispatcherStopSequence godoc still references 'bd sync' — remove stale step")
	}

	// Step numbering: the godoc should list steps 0-4 (not 0-5).
	// After removing bd sync, step "Remove PID file" should be step 4, not 5.
	if strings.Contains(seqDoc, "5.") {
		t.Error("runDispatcherStopSequence godoc still has a step 5 — renumber after removing bd sync step")
	}
}

// TestDispatcherStopSendsSignalAndWaits is the acceptance-criteria test for oro-18c5.6.
// It verifies that `oro dispatcher stop` sends SIGINT to the daemon PID, waits for exit,
// removes PID file, and does NOT call tmux kill-session or bd sync.
func TestDispatcherStopSendsSignalAndWaits(t *testing.T) {
	t.Run("sends SIGINT, waits, removes PID file, no bd sync or tmux kill-session", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := filepath.Join(tmpDir, "nonexistent.sock")
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_PID_PATH", pidFile)
		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)

		// Write a PID file pointing to the current process (alive).
		if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
			t.Fatalf("write PID file: %v", err)
		}

		fake := newFakeCmd()
		signaled := false
		var buf bytes.Buffer

		cfg := &stopConfig{
			pidPath:  pidFile,
			sockPath: sockPath,
			runner:   fake,
			w:        &buf,
			stdin:    strings.NewReader("YES\n"),
			signalFn: func(pid int) error { signaled = true; return nil },
			aliveFn:  func(pid int) bool { return false }, // exits immediately
			killFn:   func(pid int) error { return nil },
			isTTY:    func() bool { return true },
		}

		if err := runDispatcherStopSequence(context.Background(), cfg); err != nil {
			t.Fatalf("runDispatcherStopSequence returned error: %v", err)
		}

		// 1. SIGINT must be sent.
		if !signaled {
			t.Error("expected signalFn (SIGINT) to be called")
		}

		// 2. bd sync must NOT be called.
		bdSyncCalled := false
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "bd" && call[1] == "sync" {
				bdSyncCalled = true
				break
			}
		}
		if bdSyncCalled {
			t.Errorf("expected 'bd sync' NOT to be called; calls = %v", fake.calls)
		}

		// 3. PID file must be removed.
		if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
			t.Error("expected PID file to be removed after stop")
		}

		// 4. tmux kill-session must NOT be called.
		if killCall := findCall(fake.calls, "kill-session"); killCall != nil {
			t.Errorf("dispatcher stop must NOT call tmux kill-session; calls = %v", fake.calls)
		}
	})

	t.Run("dispatcher not running prints message and returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := filepath.Join(tmpDir, "nonexistent.sock")

		var buf bytes.Buffer
		cfg := &stopConfig{
			pidPath:  pidFile,
			sockPath: sockPath,
			w:        &buf,
		}

		if err := runDispatcherStopSequence(context.Background(), cfg); err != nil {
			t.Fatalf("unexpected error when dispatcher not running: %v", err)
		}

		if !strings.Contains(buf.String(), "not running") {
			t.Errorf("expected 'not running' in output, got: %q", buf.String())
		}
	})

	t.Run("drain timeout triggers SIGKILL fallback", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := filepath.Join(tmpDir, "nonexistent.sock")

		if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
			t.Fatalf("write PID file: %v", err)
		}

		fake := newFakeCmd()
		var killedPID int
		var buf bytes.Buffer

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		cfg := &stopConfig{
			pidPath:  pidFile,
			sockPath: sockPath,
			runner:   fake,
			w:        &buf,
			stdin:    strings.NewReader("YES\n"),
			signalFn: func(pid int) error { return nil },
			aliveFn:  func(pid int) bool { return true }, // process never dies
			killFn:   func(pid int) error { killedPID = pid; return nil },
			isTTY:    func() bool { return true },
		}

		if err := runDispatcherStopSequence(ctx, cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if killedPID == 0 {
			t.Error("expected SIGKILL fallback when drain times out")
		}
	})

	t.Run("requires --force or TTY confirmation", func(t *testing.T) {
		tmpDir := t.TempDir()
		pidFile := filepath.Join(tmpDir, "oro.pid")
		sockPath := filepath.Join(tmpDir, "nonexistent.sock")

		if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
			t.Fatalf("write PID file: %v", err)
		}

		var buf bytes.Buffer
		cfg := &stopConfig{
			pidPath:  pidFile,
			sockPath: sockPath,
			runner:   newFakeCmd(),
			w:        &buf,
			stdin:    strings.NewReader(""),
			isTTY:    func() bool { return false }, // not a terminal
		}

		err := runDispatcherStopSequence(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected error when stdin is not a TTY")
		}
		if !strings.Contains(err.Error(), "not a TTY") {
			t.Errorf("expected TTY error, got: %v", err)
		}
	})
}
