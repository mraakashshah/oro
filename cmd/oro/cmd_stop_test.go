package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStopGraceful(t *testing.T) {
	// Setup: PID file with our own PID (so DaemonStatus returns "running").
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup PID: %v", err)
	}

	// Setup: fakeCmd to record tmux and bd calls.
	fake := newFakeCmd()

	// Track whether signal was sent.
	signaled := false

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath:  pidFile,
		tmuxName: "oro",
		runner:   fake,
		w:        &buf,
		signalFn: func(pid int) error { signaled = true; return nil },
		aliveFn:  func(pid int) bool { return false }, // process "exits" immediately
	}

	if err := runStopSequence(context.Background(), cfg); err != nil {
		t.Fatalf("runStopSequence: %v", err)
	}

	// Assert: SIGTERM was sent (this is the only shutdown mechanism now).
	if !signaled {
		t.Error("expected signalFn to be called")
	}

	// Assert: tmux kill-session was called.
	if killCall := findCall(fake.calls, "kill-session"); killCall == nil {
		t.Errorf("tmux kill-session not called; calls = %v", fake.calls)
	}

	// Assert: bd sync was called.
	bdSynced := false
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "bd" && call[1] == "sync" {
			bdSynced = true
		}
	}
	if !bdSynced {
		t.Errorf("bd sync not called; calls = %v", fake.calls)
	}
}

func TestStop_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")

	var buf bytes.Buffer
	cfg := &stopConfig{
		pidPath: pidFile,
		w:       &buf,
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
		pidPath: pidFile,
		w:       &buf,
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

// TestStop_RefusedWhenAgentRole verifies that oro stop refuses to run
// when ORO_ROLE is set (i.e., called from an agent, not a human).
func TestStop_RefusedWhenAgentRole(t *testing.T) {
	t.Setenv("ORO_ROLE", "manager")

	root := newRootCmd()
	root.SetArgs([]string{"stop"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error when ORO_ROLE is set, got nil")
	}
	if !strings.Contains(err.Error(), "agent") && !strings.Contains(err.Error(), "human") {
		t.Errorf("expected error about agent/human restriction, got: %v", err)
	}
}
