package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ttyStop returns a stopConfig that passes TTY confirmation (simulates interactive terminal).
func ttyStop(pidFile string, fake *fakeCmd, buf *bytes.Buffer) *stopConfig {
	return &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(filepath.Dir(pidFile), "nonexistent.sock"),
		tmuxName: TmuxSessionName(""),
		runner:   fake,
		w:        buf,
		stdin:    strings.NewReader("YES\n"),
		signalFn: func(pid int) error { return nil },
		aliveFn:  func(pid int) bool { return false },
		killFn:   func(pid int) error { return nil },
		isTTY:    func() bool { return true },
	}
}

func TestStop_SIGINTSucceeds(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()
	signaled := false

	var buf bytes.Buffer
	cfg := ttyStop(pidFile, fake, &buf)
	cfg.signalFn = func(pid int) error { signaled = true; return nil }

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	if !signaled {
		t.Error("expected signalFn (SIGINT) to be called")
	}
	if killCall := findCall(fake.calls, "kill-session"); killCall == nil {
		t.Errorf("tmux kill-session not called; calls = %v", fake.calls)
	}
}

func TestStop_SIGINTFailsFallsBackToSIGKILL(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()

	var killedWith int
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cfg := ttyStop(pidFile, fake, &buf)
	cfg.signalFn = func(pid int) error { return nil }
	cfg.aliveFn = func(pid int) bool { return true } // process won't die
	cfg.killFn = func(pid int) error { killedWith = pid; return nil }

	if err := runStopSequence(ctx, cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	if killedWith == 0 {
		t.Error("expected killFn (SIGKILL) to be called when process won't exit")
	}
}

func TestStop_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		w:        &buf,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(buf.String(), "not running") {
		t.Errorf("expected 'not running' message, got %q", buf.String())
	}
}

func TestStop_Stale(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	// PID 4000000 is almost certainly not running.
	if err := WritePIDFile(pidFile, 4000000); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		w:        &buf,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(buf.String(), "stale") {
		t.Errorf("expected 'stale' message, got %q", buf.String())
	}

	// PID file should be removed.
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}
}

func TestStopStatusStaleRemovesSocket(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	sockFile := filepath.Join(tmpDir, "oro.sock")

	// PID 4000000 is almost certainly not running.
	if err := WritePIDFile(pidFile, 4000000); err != nil {
		t.Fatalf("setup PID: %v", err)
	}
	// Create a stale socket file.
	if err := os.WriteFile(sockFile, []byte("stale"), 0o600); err != nil {
		t.Fatalf("setup socket: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: sockFile,
		w:        &buf,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}
	if _, err := os.Stat(sockFile); !os.IsNotExist(err) {
		t.Error("expected socket file to be removed")
	}
}

func TestStop_RefusedWhenNotTTY(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		tmuxName: TmuxSessionName(""),
		runner:   newFakeCmd(),
		w:        &buf,
		stdin:    strings.NewReader(""),
		isTTY:    func() bool { return false }, // not a terminal
	}

	err := runStopSequence(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when stdin is not a TTY")
	}
	if !strings.Contains(err.Error(), "not a TTY") {
		t.Errorf("expected TTY error, got: %v", err)
	}
}

func TestStop_RefusedWhenConfirmationNotYES(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		tmuxName: TmuxSessionName(""),
		runner:   newFakeCmd(),
		w:        &buf,
		stdin:    strings.NewReader("no\n"),
		isTTY:    func() bool { return true },
	}

	err := runStopSequence(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when confirmation is not YES")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Errorf("expected aborted error, got: %v", err)
	}
}

func TestStop_ForceRequiresEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	t.Run("force without env var fails", func(t *testing.T) {
		t.Setenv("ORO_HUMAN_CONFIRMED", "")

		var buf bytes.Buffer
		cfg := &stopConfig{
			pidPath:  pidFile,
			sockPath: filepath.Join(filepath.Dir(pidFile), "nonexistent.sock"),
			tmuxName: TmuxSessionName(""),
			runner:   newFakeCmd(),
			w:        &buf,
			force:    true,
			isTTY:    func() bool { return false },
		}

		err := runStopSequence(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected error when --force used without ORO_HUMAN_CONFIRMED")
		}
		if !strings.Contains(err.Error(), "ORO_HUMAN_CONFIRMED") {
			t.Errorf("expected ORO_HUMAN_CONFIRMED error, got: %v", err)
		}
	})

	t.Run("force with env var succeeds", func(t *testing.T) {
		t.Setenv("ORO_HUMAN_CONFIRMED", "1")

		fake := newFakeCmd()
		var buf bytes.Buffer
		cfg := ttyStop(pidFile, fake, &buf)
		cfg.force = true
		cfg.isTTY = func() bool { return false } // doesn't matter with --force

		err := runStopSequence(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error with --force and ORO_HUMAN_CONFIRMED=1: %v", err)
		}
	})
}

// TestRunStopSequence verifies the full stop sequence completes successfully and
// does NOT invoke bd daemon stop or bd sync (those are handled by the dispatcher
// itself on shutdown and the pre-commit hook, respectively).
func TestRunStopSequence(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()
	var buf bytes.Buffer
	cfg := ttyStop(pidFile, fake, &buf)

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	if !strings.Contains(buf.String(), "shutdown complete") {
		t.Errorf("expected 'shutdown complete' message, got %q", buf.String())
	}

	// Verify bd daemon stop was NOT called.
	for _, call := range fake.calls {
		if len(call) >= 3 && call[0] == "bd" && call[1] == "daemon" && call[2] == "stop" {
			t.Errorf("unexpected 'bd daemon stop' call; calls = %v", fake.calls)
		}
	}

	// Verify bd sync was NOT called.
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "bd" && call[1] == "sync" {
			t.Errorf("unexpected 'bd sync' call; calls = %v", fake.calls)
		}
	}
}

// --- discoverProjectDaemons tests ---

func TestDiscoverProjectDaemons_FindsRunning(t *testing.T) {
	oroHome := t.TempDir()

	// Create two project dirs with PID files — use our own PID (known alive).
	for _, name := range []string{"alpha", "beta"} {
		projDir := filepath.Join(oroHome, "projects", name)
		if err := os.MkdirAll(projDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), os.Getpid()); err != nil {
			t.Fatal(err)
		}
	}

	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) != 2 {
		t.Fatalf("expected 2 daemons, got %d: %+v", len(daemons), daemons)
	}

	names := map[string]bool{}
	for _, d := range daemons {
		names[d.Project] = true
		if d.PID != os.Getpid() {
			t.Errorf("expected PID %d, got %d for project %s", os.Getpid(), d.PID, d.Project)
		}
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected projects alpha and beta, got %v", names)
	}
}

func TestDiscoverProjectDaemons_SkipsStalePIDs(t *testing.T) {
	oroHome := t.TempDir()

	projDir := filepath.Join(oroHome, "projects", "stale")
	if err := os.MkdirAll(projDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// PID 4000000 is almost certainly not running.
	if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), 4000000); err != nil {
		t.Fatal(err)
	}

	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) != 0 {
		t.Errorf("expected 0 daemons (stale PID), got %d: %+v", len(daemons), daemons)
	}
}

func TestDiscoverProjectDaemons_IncludesLegacyGlobal(t *testing.T) {
	oroHome := t.TempDir()

	// Legacy global PID file at ~/.oro/oro.pid
	if err := WritePIDFile(filepath.Join(oroHome, "oro.pid"), os.Getpid()); err != nil {
		t.Fatal(err)
	}

	daemons := discoverProjectDaemons(oroHome)
	if len(daemons) != 1 {
		t.Fatalf("expected 1 daemon (legacy global), got %d: %+v", len(daemons), daemons)
	}
	if daemons[0].Project != "(global)" {
		t.Errorf("expected project name '(global)', got %q", daemons[0].Project)
	}
}

// TestStopSequenceDoesNotStopDolt verifies that runStopSequence completes
// successfully with a beadsDir set — dolt persists across sessions and is
// never stopped by the stop sequence (structural guarantee: stopConfig has no
// stopDoltFn field). See also TestStopSequenceCleansDolt for a richer assertion.
func TestStopSequenceDoesNotStopDolt(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	fake := newFakeCmd()
	var buf bytes.Buffer
	cfg := ttyStop(pidFile, fake, &buf)
	cfg.beadsDir = filepath.Join(tmpDir, ".beads")

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	if !strings.Contains(buf.String(), "shutdown complete") {
		t.Errorf("expected 'shutdown complete', got %q", buf.String())
	}
}

// TestStopSequenceCleansDolt verifies that runStopSequence completes successfully
// when beadsDir is set, and that no dolt stop occurs — dolt persists across sessions.
func TestStopSequenceCleansDolt(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	// Create a fake beadsDir (mimics a project with dolt).
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	doltPIDPath := filepath.Join(beadsDir, "dolt-server.pid")
	if err := os.WriteFile(doltPIDPath, []byte("9999"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := newFakeCmd()
	var buf bytes.Buffer
	cfg := ttyStop(pidFile, fake, &buf)
	cfg.beadsDir = beadsDir

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	// dolt PID file must still exist — stopDoltFn is not part of runStopSequence.
	if _, err := os.Stat(doltPIDPath); os.IsNotExist(err) {
		t.Error("dolt PID file should still exist — runStopSequence must not stop dolt")
	}

	if !strings.Contains(buf.String(), "shutdown complete") {
		t.Errorf("expected 'shutdown complete', got %q", buf.String())
	}
}

func TestStop_NotRunning_SuggestsAllWhenOtherDaemonsExist(t *testing.T) {
	oroHome := t.TempDir()
	t.Setenv("ORO_HOME", oroHome)
	t.Setenv("ORO_PROJECT", "myproject")
	t.Setenv("ORO_PID_PATH", "")
	t.Setenv("ORO_SOCKET_PATH", "")

	// Create project dir for myproject (no daemon running)
	myProjDir := filepath.Join(oroHome, "projects", "myproject")
	if err := os.MkdirAll(myProjDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Create another project with a running daemon
	otherDir := filepath.Join(oroHome, "projects", "other")
	if err := os.MkdirAll(otherDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := WritePIDFile(filepath.Join(otherDir, "oro.pid"), os.Getpid()); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  filepath.Join(myProjDir, "oro.pid"),
		sockPath: filepath.Join(myProjDir, "oro.sock"),
		w:        &buf,
		oroHome:  oroHome,
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "not running") {
		t.Errorf("expected 'not running' message, got %q", output)
	}
	if !strings.Contains(output, "oro stop --all") {
		t.Errorf("expected suggestion to use --all, got %q", output)
	}
}

// TestStopAllCorrectBeadsDir verifies that runStopAll reads project.root from the
// project dir and that missing project.root logs a warning.
// Note: dolt is intentionally NOT stopped during oro stop (persists across sessions).
func TestStopAllCorrectBeadsDir(t *testing.T) {
	t.Run("stops daemon and does not call stopDoltFn", func(t *testing.T) {
		oroHome := t.TempDir()
		projectRoot := t.TempDir()

		// Start a fake daemon subprocess; reap in background to avoid zombies.
		cmd := exec.CommandContext(context.Background(), "sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep process: %v", err)
		}
		go func() { _ = cmd.Wait() }()
		defer func() { _ = cmd.Process.Signal(os.Kill) }()

		pid := cmd.Process.Pid
		projName := "myproject"
		projDir := filepath.Join(oroHome, "projects", projName)
		if err := os.MkdirAll(projDir, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(projDir, "project.root"), []byte(projectRoot), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), pid); err != nil {
			t.Fatal(err)
		}

		t.Setenv("ORO_HUMAN_CONFIRMED", "1")
		var buf bytes.Buffer
		if err := runStopAll(context.Background(), oroHome, true, &buf); err != nil {
			t.Fatalf("runStopAll: %v", err)
		}

		// Dolt is not stopped — structural guarantee (stopConfig has no stopDoltFn field).
		if !strings.Contains(buf.String(), "stopping") {
			t.Errorf("expected stopping output, got %q", buf.String())
		}
	})

	t.Run("missing project.root logs warning and skips dolt cleanup", func(t *testing.T) {
		oroHome := t.TempDir()

		// Start a fake daemon subprocess; reap in background to avoid zombies.
		cmd := exec.CommandContext(context.Background(), "sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep process: %v", err)
		}
		go func() { _ = cmd.Wait() }()
		defer func() { _ = cmd.Process.Signal(os.Kill) }()

		pid := cmd.Process.Pid
		projName := "myproject"
		projDir := filepath.Join(oroHome, "projects", projName)
		if err := os.MkdirAll(projDir, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), pid); err != nil {
			t.Fatal(err)
		}

		t.Setenv("ORO_HUMAN_CONFIRMED", "1")
		var buf bytes.Buffer
		if err := runStopAll(context.Background(), oroHome, true, &buf); err != nil {
			t.Fatalf("runStopAll: %v", err)
		}

		if !strings.Contains(buf.String(), "warning") {
			t.Errorf("expected warning in output when project.root is missing, got %q", buf.String())
		}
	})
}

// TestStopAll_DoltPersists verifies that runStopAll does NOT clean up dolt PID
// files — dolt server intentionally persists across oro sessions.
func TestStopAll_DoltPersists(t *testing.T) {
	oroHome := t.TempDir()

	// Start two temporary subprocesses as fake daemons; reap to avoid zombies.
	var pids []int
	var projectRoots []string
	var cleanupProcs func()
	{
		cleanupFns := []func(){}
		for range []string{"alpha", "beta"} {
			cmd := exec.CommandContext(context.Background(), "sleep", "60")
			if err := cmd.Start(); err != nil {
				t.Fatalf("start sleep process: %v", err)
			}
			pid := cmd.Process.Pid
			pids = append(pids, pid)
			projectRoots = append(projectRoots, t.TempDir())
			go func(c *exec.Cmd) { _ = c.Wait() }(cmd)
			cleanupFns = append(cleanupFns, func(p *os.Process) func() {
				return func() { _ = p.Signal(os.Kill) }
			}(cmd.Process))
		}
		cleanupProcs = func() {
			for _, fn := range cleanupFns {
				fn()
			}
		}
	}
	defer cleanupProcs()

	// Create two projects with the fake daemon PIDs, project.root files, and beads dirs.
	for i, projName := range []string{"alpha", "beta"} {
		projDir := filepath.Join(oroHome, "projects", projName)
		if err := os.MkdirAll(projDir, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(projDir, "project.root"), []byte(projectRoots[i]), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WritePIDFile(filepath.Join(projDir, "oro.pid"), pids[i]); err != nil {
			t.Fatal(err)
		}

		// Create beads dir and fake dolt PID file at <projectRoot>/.beads/.
		beadsDir := filepath.Join(projectRoots[i], ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		doltPIDPath := filepath.Join(beadsDir, "dolt-server.pid")
		if err := os.WriteFile(doltPIDPath, []byte("9999"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("ORO_HUMAN_CONFIRMED", "1")

	var buf bytes.Buffer
	if err := runStopAll(context.Background(), oroHome, true, &buf); err != nil {
		t.Fatalf("runStopAll: %v", err)
	}

	// Dolt PID files must still exist — dolt is not stopped during oro stop.
	for i, projName := range []string{"alpha", "beta"} {
		doltPIDPath := filepath.Join(projectRoots[i], ".beads", "dolt-server.pid")
		if _, err := os.Stat(doltPIDPath); os.IsNotExist(err) {
			t.Errorf("dolt PID file for project %s should still exist (dolt persists), but was removed", projName)
		}
	}

	// Verify output indicates multiple daemons were processed.
	output := buf.String()
	if !strings.Contains(output, "found 2 running daemon(s)") {
		t.Errorf("expected 'found 2 running daemon(s)' in output, got %q", output)
	}
}
