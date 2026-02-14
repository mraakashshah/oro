package main

import (
	"bytes"
	"context"
	"os"
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
		tmuxName: "oro",
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
		tmuxName: "oro",
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
		tmuxName: "oro",
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
			tmuxName: "oro",
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
