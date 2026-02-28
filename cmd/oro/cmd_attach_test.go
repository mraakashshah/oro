package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachNoSession(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &attachConfig{
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		isTTY:    func() bool { return true },
	}
	err := runAttach(cfg)
	if err == nil {
		t.Fatal("expected error for no running session")
	}
	if !strings.Contains(err.Error(), "oro start") {
		t.Errorf("expected 'oro start' in error, got: %v", err)
	}
}

func TestAttachStale(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, 4000000); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg := &attachConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		isTTY:    func() bool { return true },
	}
	err := runAttach(cfg)
	if err == nil {
		t.Fatal("expected error for stale PID")
	}
	if !strings.Contains(err.Error(), "oro cleanup") {
		t.Errorf("expected 'oro cleanup' in error, got: %v", err)
	}
}

func TestAttachDaemonOnly(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fake := newFakeCmd()
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no such session")
	cfg := &attachConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		tmuxName: "oro",
		runner:   fake,
		isTTY:    func() bool { return true },
	}
	err := runAttach(cfg)
	if err == nil {
		t.Fatal("expected error for daemon-only mode")
	}
	if !strings.Contains(err.Error(), "daemon-only mode") {
		t.Errorf("expected 'daemon-only mode' in error, got: %v", err)
	}
}

func TestAttachUnhealthy(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fake := newFakeCmd()
	// has-session succeeds (Exists = true, no entry in errs map)
	// isHealthy: architect returns "zsh" → unhealthy
	archPane := "oro:architect"
	fake.output[key("tmux", "display-message", "-p", "-t", archPane, "#{pane_current_command}")] = "zsh"
	cfg := &attachConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		tmuxName: "oro",
		runner:   fake,
		isTTY:    func() bool { return true },
	}
	err := runAttach(cfg)
	if err == nil {
		t.Fatal("expected error for unhealthy session")
	}
	if !strings.Contains(err.Error(), "oro stop") {
		t.Errorf("expected 'oro stop' in error, got: %v", err)
	}
}

func TestAttachNoTTY(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fake := newFakeCmd()
	archPane := "oro:architect"
	mgrPane := "oro:manager"
	fake.output[key("tmux", "display-message", "-p", "-t", archPane, "#{pane_current_command}")] = "claude"
	fake.output[key("tmux", "display-message", "-p", "-t", mgrPane, "#{pane_current_command}")] = "claude"
	cfg := &attachConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		tmuxName: "oro",
		runner:   fake,
		isTTY:    func() bool { return false },
	}
	err := runAttach(cfg)
	if err == nil {
		t.Fatal("expected error when no TTY")
	}
	if !strings.Contains(err.Error(), "terminal") {
		t.Errorf("expected 'terminal' in error, got: %v", err)
	}
}

func TestAttachSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "oro.pid")
	if err := WritePIDFile(pidFile, os.Getpid()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fake := newFakeCmd()
	archPane := "oro:architect"
	mgrPane := "oro:manager"
	fake.output[key("tmux", "display-message", "-p", "-t", archPane, "#{pane_current_command}")] = "claude"
	fake.output[key("tmux", "display-message", "-p", "-t", mgrPane, "#{pane_current_command}")] = "claude"

	attached := false
	cfg := &attachConfig{
		pidPath:  pidFile,
		sockPath: filepath.Join(tmpDir, "nonexistent.sock"),
		tmuxName: "oro",
		runner:   fake,
		isTTY:    func() bool { return true },
		attachFn: func() error { attached = true; return nil },
	}
	if err := runAttach(cfg); err != nil {
		t.Fatalf("runAttach: %v", err)
	}
	if !attached {
		t.Error("expected attachFn to be called")
	}
}

func TestHelpShowsAttach(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help command: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "attach") {
		t.Errorf("expected 'attach' in help output, got:\n%s", output)
	}
	// Verify attach is in Lifecycle section, between start and stop.
	lifecycleIdx := strings.Index(output, "Lifecycle:")
	attachIdx := strings.Index(output, "attach")
	stopIdx := strings.Index(output, "\n  stop")
	if lifecycleIdx < 0 || attachIdx < 0 || stopIdx < 0 {
		t.Fatalf("expected Lifecycle, attach, stop in output; got:\n%s", output)
	}
	if lifecycleIdx >= attachIdx || attachIdx >= stopIdx {
		t.Errorf("expected 'attach' between 'Lifecycle:' and 'stop'; indices: lifecycle=%d, attach=%d, stop=%d",
			lifecycleIdx, attachIdx, stopIdx)
	}
}
