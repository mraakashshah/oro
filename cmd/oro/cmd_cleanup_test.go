package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCleanup_NothingToClean(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails (no session)
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty (no agent branches)
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	// no epic branches
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty JSON array
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),  // no file
		sockPath: filepath.Join(tmpDir, "oro.sock"), // no file
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "nothing to clean") {
		t.Errorf("expected 'nothing to clean' in output, got: %s", out)
	}
}

func TestCleanup_KillsRunningDispatcher(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails (no session)
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()

	// Write a PID file with a fake PID
	pidPath := filepath.Join(tmpDir, "oro.pid")
	if err := os.WriteFile(pidPath, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	signaled := false
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(pid int) error {
			if pid == 12345 {
				signaled = true
			}
			return nil
		},
		aliveFn: func(pid int) bool {
			return pid == 12345
		},
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !signaled {
		t.Error("expected dispatcher PID 12345 to be signaled")
	}

	// PID file should be removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}

	out := buf.String()
	if !strings.Contains(out, "dispatcher") {
		t.Errorf("expected output to mention dispatcher, got: %s", out)
	}
}

func TestCleanup_KillsTmux(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session succeeds (session exists)
	fake.output[key("tmux", "has-session", "-t", "oro")] = ""
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify kill-session was called
	if killCall := findCall(fake.calls, "kill-session"); killCall == nil {
		t.Error("expected tmux kill-session")
	}

	out := buf.String()
	if !strings.Contains(out, "tmux") {
		t.Errorf("expected output to mention tmux, got: %s", out)
	}
}

func TestCleanup_RemovesStaleFiles(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()

	// Write stale PID file (process not alive)
	pidPath := filepath.Join(tmpDir, "oro.pid")
	if err := os.WriteFile(pidPath, []byte("99999"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Write stale socket file
	sockPath := filepath.Join(tmpDir, "oro.sock")
	if err := os.WriteFile(sockPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: sockPath,
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false }, // process is dead
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// PID file should be removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected stale PID file to be removed")
	}

	// Socket file should be removed
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("expected stale socket file to be removed")
	}

	out := buf.String()
	if !strings.Contains(out, "pid") || !strings.Contains(out, "socket") {
		t.Errorf("expected output to mention pid and socket removal, got: %s", out)
	}
}

func TestCleanup_PrunesWorktrees(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git worktree prune was called
	var pruned bool
	for _, call := range fake.calls {
		if len(call) >= 3 && call[0] == "git" && call[1] == "worktree" && call[2] == "prune" {
			pruned = true
		}
	}
	if !pruned {
		t.Error("expected git worktree prune to be called")
	}
}

func TestCleanup_DeletesAgentBranches(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns agent branches
	fake.output[key("git", "branch", "--list", "agent/*")] = "  agent/cleanup-cli\n  agent/fix-bug\n"
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git branch -D was called for each agent branch
	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-D" {
			deletedBranches = append(deletedBranches, call[3])
		}
	}

	if len(deletedBranches) != 2 {
		t.Fatalf("expected 2 branch deletions, got %d: %v", len(deletedBranches), deletedBranches)
	}

	found := map[string]bool{}
	for _, b := range deletedBranches {
		found[b] = true
	}
	if !found["agent/cleanup-cli"] {
		t.Error("expected agent/cleanup-cli to be deleted")
	}
	if !found["agent/fix-bug"] {
		t.Error("expected agent/fix-bug to be deleted")
	}

	out := buf.String()
	if !strings.Contains(out, "agent/cleanup-cli") || !strings.Contains(out, "agent/fix-bug") {
		t.Errorf("expected output to mention deleted branches, got: %s", out)
	}
}

func TestCleanup_ResetsInProgressBeads(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns beads in progress
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = `[{"id":"oro-abc1"},{"id":"oro-xyz2"}]`

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify bd update was called for each bead
	var resetBeads []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "bd" && call[1] == "update" && call[3] == "--status=open" {
			resetBeads = append(resetBeads, call[2])
		}
	}

	if len(resetBeads) != 2 {
		t.Fatalf("expected 2 bead resets, got %d: %v", len(resetBeads), resetBeads)
	}

	found := map[string]bool{}
	for _, b := range resetBeads {
		found[b] = true
	}
	if !found["oro-abc1"] {
		t.Error("expected oro-abc1 to be reset to open")
	}
	if !found["oro-xyz2"] {
		t.Error("expected oro-xyz2 to be reset to open")
	}

	out := buf.String()
	if !strings.Contains(out, "oro-abc1") || !strings.Contains(out, "oro-xyz2") {
		t.Errorf("expected output to mention reset beads, got: %s", out)
	}
}

func TestCleanup_ContinuesOnErrors(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session succeeds but kill fails
	fake.output[key("tmux", "has-session", "-t", "oro")] = ""
	fake.errs[key("tmux", "kill-session", "-t", "oro")] = fmt.Errorf("kill failed")
	// pgrep fails
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("pgrep error")
	// git worktree prune fails
	fake.errs[key("git", "worktree", "prune")] = fmt.Errorf("prune failed")
	// git branch --list fails
	fake.errs[key("git", "branch", "--list", "agent/*")] = fmt.Errorf("branch list failed")
	fake.errs[key("git", "branch", "--list", "epic/*")] = fmt.Errorf("branch list failed")
	// bd list fails
	fake.errs[key("bd", "list", "--status=in_progress", "--json")] = fmt.Errorf("bd failed")

	tmpDir := t.TempDir()

	// Write a PID file with a process that's alive but signal fails
	pidPath := filepath.Join(tmpDir, "oro.pid")
	if err := os.WriteFile(pidPath, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return fmt.Errorf("signal failed") },
		aliveFn:  func(int) bool { return true },
	}

	// Should NOT return an error despite individual step failures
	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("expected nil error (best-effort), got: %v", err)
	}

	out := buf.String()
	// Should report warnings for failures
	if !strings.Contains(out, "warning") {
		t.Errorf("expected warnings in output, got: %s", out)
	}

	// Verify all steps were attempted (calls include tmux, git, bd)
	var hasGitCall, hasBdCall bool
	for _, call := range fake.calls {
		if call[0] == "git" {
			hasGitCall = true
		}
		if call[0] == "bd" {
			hasBdCall = true
		}
	}
	if !hasGitCall {
		t.Error("expected git commands to be attempted despite earlier failures")
	}
	if !hasBdCall {
		t.Error("expected bd commands to be attempted despite earlier failures")
	}
}

func TestCleanup_KillsWorkerProcesses(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds worker PIDs
	fake.output[key("pgrep", "-f", "ORO_ROLE")] = "11111\n22222"
	// git branch --list returns empty
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var signaledPIDs []int
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(pid int) error {
			signaledPIDs = append(signaledPIDs, pid)
			return nil
		},
		aliveFn: func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that worker PIDs were signaled
	found := map[int]bool{}
	for _, pid := range signaledPIDs {
		found[pid] = true
	}
	if !found[11111] {
		t.Error("expected worker PID 11111 to be signaled")
	}
	if !found[22222] {
		t.Error("expected worker PID 22222 to be signaled")
	}

	out := buf.String()
	if !strings.Contains(out, "worker") {
		t.Errorf("expected output to mention worker processes, got: %s", out)
	}
}

func TestCleanup_RefusedWhenNotTTY(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "oro.pid")

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   newFakeCmd(),
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		isTTY:    func() bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when stdin is not a TTY, got nil")
	}
	if !strings.Contains(err.Error(), "TTY") {
		t.Errorf("expected TTY error, got: %v", err)
	}
}

func TestCleanup_SendsSIGINTToDispatcher(t *testing.T) {
	fake := newFakeCmd()
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "oro.pid")
	if err := os.WriteFile(pidPath, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	signaled := false
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  pidPath,
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(pid int) error { signaled = true; return nil },
		aliveFn:  func(pid int) bool { return pid == 12345 },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !signaled {
		t.Error("expected SIGINT to be sent to dispatcher")
	}
}

func TestCleanupWorktreeDir(t *testing.T) {
	t.Run("removes .worktrees directory when it exists", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
		fake.output[key("git", "branch", "--list", "agent/*")] = ""
		fake.output[key("git", "branch", "--list", "epic/*")] = ""
		fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

		tmpDir := t.TempDir()
		worktreeDir := filepath.Join(tmpDir, ".worktrees")

		// Create .worktrees directory with a file
		if err := os.MkdirAll(worktreeDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktreeDir, "test.txt"), []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}

		// Change to tmpDir so cleanup looks for .worktrees in the right place
		origDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(origDir); err != nil {
				t.Error(err)
			}
		}()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			tmuxName: TmuxSessionName(""),
			pidPath:  filepath.Join(tmpDir, "oro.pid"),
			sockPath: filepath.Join(tmpDir, "oro.sock"),
			signalFn: func(int) error { return nil },
			aliveFn:  func(int) bool { return false },
		}

		err = runCleanup(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify .worktrees directory was removed
		if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
			t.Error("expected .worktrees directory to be removed")
		}

		// Check output
		out := buf.String()
		if !strings.Contains(out, "removing .worktrees/ directory") {
			t.Errorf("expected output to contain %q, got: %s", "removing .worktrees/ directory", out)
		}
	})

	t.Run("no error when directory doesn't exist", func(t *testing.T) {
		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
		fake.output[key("git", "branch", "--list", "agent/*")] = ""
		fake.output[key("git", "branch", "--list", "epic/*")] = ""
		fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

		tmpDir := t.TempDir()

		// Change to tmpDir (no .worktrees directory exists)
		origDir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(origDir); err != nil {
				t.Error(err)
			}
		}()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			tmuxName: TmuxSessionName(""),
			pidPath:  filepath.Join(tmpDir, "oro.pid"),
			sockPath: filepath.Join(tmpDir, "oro.sock"),
			signalFn: func(int) error { return nil },
			aliveFn:  func(int) bool { return false },
		}

		err = runCleanup(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should complete without error even when directory doesn't exist
		// Output should be "nothing to clean" since nothing needed cleanup
		out := buf.String()
		if !strings.Contains(out, "nothing to clean") {
			t.Errorf("expected 'nothing to clean' in output, got: %s", out)
		}
	})
}

func TestCleanupDoltPIDOnly(t *testing.T) {
	t.Run("stale PID (dead process) removes files and returns true", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}

		pidPath := filepath.Join(beadsDir, "dolt-server.pid")
		portPath := filepath.Join(beadsDir, "dolt-server.port")
		// PID 99999 is almost certainly dead.
		if err := os.WriteFile(pidPath, []byte("99999"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(portPath, []byte("13307"), 0o600); err != nil {
			t.Fatal(err)
		}

		fake := newFakeCmd()
		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			beadsDir: beadsDir,
			signalFn: func(int) error { return nil },
		}

		result := cleanupDolt(cfg)

		if !result {
			t.Error("expected cleanupDolt to return true for stale PID")
		}
		if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
			t.Error("expected PID file to be removed")
		}
		if _, err := os.Stat(portPath); !os.IsNotExist(err) {
			t.Error("expected port file to be removed")
		}

		// Must NOT call pgrep — no orphan scan.
		for _, call := range fake.calls {
			if len(call) > 0 && call[0] == "pgrep" {
				t.Error("cleanupDolt should not call pgrep (no orphan scan)")
			}
		}
	})

	t.Run("no PID file scans for orphans but returns false when pgrep fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}

		fake := newFakeCmd()
		// pgrep fails (returns error) — simulates pgrep not in PATH or no matches
		fake.errs[key("pgrep", "-f", "dolt")] = fmt.Errorf("no matching processes")

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			beadsDir: beadsDir,
			signalFn: func(int) error { return nil },
		}

		if cleanupDolt(cfg) {
			t.Error("expected cleanupDolt to return false when pgrep finds nothing")
		}

		// Verify pgrep WAS called (new behavior: scan for orphans)
		pgrepCalled := false
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "pgrep" && call[1] == "-f" {
				pgrepCalled = true
				break
			}
		}
		if !pgrepCalled {
			t.Error("cleanupDolt should call pgrep to scan for orphans when no PID file")
		}
	})

	t.Run("healthy dolt (PID alive) not killed, no pgrep scan", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}

		pidPath := filepath.Join(beadsDir, "dolt-server.pid")
		// Use current process PID — guaranteed alive.
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}

		fake := newFakeCmd()
		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			beadsDir: beadsDir,
			signalFn: func(int) error { return nil },
		}

		if cleanupDolt(cfg) {
			t.Error("expected cleanupDolt to return false for healthy dolt")
		}

		// PID file should still exist.
		if _, err := os.Stat(pidPath); err != nil {
			t.Error("PID file should NOT be removed for healthy dolt")
		}

		for _, call := range fake.calls {
			if len(call) > 0 && call[0] == "pgrep" {
				t.Error("cleanupDolt should not call pgrep")
			}
		}
	})

	t.Run("returns false when beadsDir is empty", func(t *testing.T) {
		fake := newFakeCmd()
		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			beadsDir: "",
			signalFn: func(int) error { return nil },
		}

		if cleanupDolt(cfg) {
			t.Error("expected cleanupDolt to return false when beadsDir is empty")
		}
	})

	t.Run("wired into runCleanup sets cleaned when stale dolt PID found", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}

		pidPath := filepath.Join(beadsDir, "dolt-server.pid")
		if err := os.WriteFile(pidPath, []byte("99999"), 0o600); err != nil {
			t.Fatal(err)
		}

		fake := newFakeCmd()
		fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
		fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
		fake.output[key("git", "branch", "--list", "agent/*")] = ""
		fake.output[key("git", "branch", "--list", "epic/*")] = ""
		fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			tmuxName: TmuxSessionName(""),
			pidPath:  filepath.Join(tmpDir, "oro.pid"),
			sockPath: filepath.Join(tmpDir, "oro.sock"),
			beadsDir: beadsDir,
			signalFn: func(int) error { return nil },
			aliveFn:  func(int) bool { return false },
		}

		err := runCleanup(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if strings.Contains(out, "nothing to clean") {
			t.Error("expected 'nothing to clean' NOT to appear when dolt cleanup was performed")
		}
	})

	t.Run("scans for orphan dolt process when PID file missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}

		fake := newFakeCmd()
		// pgrep finds an orphan dolt process
		fake.output[key("pgrep", "-f", "dolt")] = "12345\n"

		signaled := false
		var signalPID int
		signalFn := func(pid int) error {
			signaled = true
			signalPID = pid
			return nil
		}

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:   fake,
			w:        &buf,
			beadsDir: beadsDir,
			signalFn: signalFn,
		}

		result := cleanupDolt(cfg)

		if !result {
			t.Error("expected cleanupDolt to return true when orphan dolt found")
		}

		// Check pgrep was called
		pgrepCalled := false
		for _, call := range fake.calls {
			if len(call) >= 2 && call[0] == "pgrep" && call[1] == "-f" && len(call) >= 3 && call[2] == "dolt" {
				pgrepCalled = true
				break
			}
		}
		if !pgrepCalled {
			t.Error("expected cleanupDolt to call pgrep -f dolt")
		}

		// Check orphan was signaled
		if !signaled {
			t.Error("expected cleanupDolt to signal the orphan process")
		}
		if signalPID != 12345 {
			t.Errorf("expected signal PID 12345, got %d", signalPID)
		}

		// Check dolt server files are removed
		portPath := filepath.Join(beadsDir, "dolt-server.port")
		if _, err := os.Stat(portPath); !os.IsNotExist(err) {
			t.Error("expected dolt-server.port to be removed after orphan cleanup")
		}
	})
}

// TestCleanupDolt_ScansOrphansWithoutPIDFile verifies that cleanupDolt scans for orphan
// dolt processes when the PID file is missing.
func TestCleanupDolt_ScansOrphansWithoutPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	fake := newFakeCmd()
	// pgrep finds an orphan dolt process
	fake.output[key("pgrep", "-f", "dolt")] = "12345\n"

	signaled := false
	var signalPID int
	signalFn := func(pid int) error {
		signaled = true
		signalPID = pid
		return nil
	}

	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		beadsDir: beadsDir,
		signalFn: signalFn,
	}

	result := cleanupDolt(cfg)

	if !result {
		t.Error("expected cleanupDolt to return true when orphan dolt found")
	}

	// Check pgrep was called
	pgrepCalled := false
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "pgrep" && call[1] == "-f" && len(call) >= 3 && call[2] == "dolt" {
			pgrepCalled = true
			break
		}
	}
	if !pgrepCalled {
		t.Error("expected cleanupDolt to call pgrep -f dolt")
	}

	// Check orphan was signaled
	if !signaled {
		t.Error("expected cleanupDolt to signal the orphan process")
	}
	if signalPID != 12345 {
		t.Errorf("expected signal PID 12345, got %d", signalPID)
	}

	// Check dolt server files are removed
	portPath := filepath.Join(beadsDir, "dolt-server.port")
	if _, err := os.Stat(portPath); !os.IsNotExist(err) {
		t.Error("expected dolt-server.port to be removed after orphan cleanup")
	}
}

func TestCleanup_DeletesEpicBranches(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns epic branches
	fake.output[key("git", "branch", "--list", "epic/*")] = "  epic/oro-5bsn\n  epic/oro-xyz9\n"
	// no agent branches (testing epic separately from agent)
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git branch -D was called for each epic branch
	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-D" {
			deletedBranches = append(deletedBranches, call[3])
		}
	}

	if len(deletedBranches) != 2 {
		t.Fatalf("expected 2 branch deletions, got %d: %v", len(deletedBranches), deletedBranches)
	}

	found := map[string]bool{}
	for _, b := range deletedBranches {
		found[b] = true
	}
	if !found["epic/oro-5bsn"] {
		t.Error("expected epic/oro-5bsn to be deleted")
	}
	if !found["epic/oro-xyz9"] {
		t.Error("expected epic/oro-xyz9 to be deleted")
	}

	out := buf.String()
	if !strings.Contains(out, "epic/oro-5bsn") || !strings.Contains(out, "epic/oro-xyz9") {
		t.Errorf("expected output to mention deleted epic branches, got: %s", out)
	}
}

func TestCleanup_DeletesAgentAndEpicBranches(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns both agent and epic branches
	fake.output[key("git", "branch", "--list", "agent/*")] = "  agent/cleanup-cli\n  agent/fix-bug\n"
	fake.output[key("git", "branch", "--list", "epic/*")] = "  epic/oro-5bsn\n  epic/oro-xyz9\n"
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git branch -D was called for each agent and epic branch
	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-D" {
			deletedBranches = append(deletedBranches, call[3])
		}
	}

	if len(deletedBranches) != 4 {
		t.Fatalf("expected 4 branch deletions, got %d: %v", len(deletedBranches), deletedBranches)
	}

	found := map[string]bool{}
	for _, b := range deletedBranches {
		found[b] = true
	}
	if !found["agent/cleanup-cli"] {
		t.Error("expected agent/cleanup-cli to be deleted")
	}
	if !found["agent/fix-bug"] {
		t.Error("expected agent/fix-bug to be deleted")
	}
	if !found["epic/oro-5bsn"] {
		t.Error("expected epic/oro-5bsn to be deleted")
	}
	if !found["epic/oro-xyz9"] {
		t.Error("expected epic/oro-xyz9 to be deleted")
	}

	out := buf.String()
	if !strings.Contains(out, "agent/cleanup-cli") || !strings.Contains(out, "agent/fix-bug") ||
		!strings.Contains(out, "epic/oro-5bsn") || !strings.Contains(out, "epic/oro-xyz9") {
		t.Errorf("expected output to mention all deleted branches, got: %s", out)
	}
}

func TestCleanup_DoesNotCallBdDaemon(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails (no session)
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// no agent branches
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	// no epic branches
	fake.output[key("git", "branch", "--list", "epic/*")] = ""
	// no in_progress beads
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify bd daemon stop was NOT called.
	for _, call := range fake.calls {
		if len(call) >= 3 && call[0] == "bd" && call[1] == "daemon" && call[2] == "stop" {
			t.Errorf("unexpected 'bd daemon stop' call; calls = %v", fake.calls)
		}
	}
}

// TestCleanupSharedDoltPID verifies that cleanup handles stale ~/.oro/dolt-server.pid
// correctly: removes it when the process is dead, skips it when the server is active.
func TestCleanupSharedDoltPID(t *testing.T) {
	t.Run("removes stale shared PID file when process is dead", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := filepath.Join(tmpDir, "oro")
		if err := os.MkdirAll(oroHome, 0o750); err != nil {
			t.Fatal(err)
		}

		pidPath := filepath.Join(oroHome, "dolt-server.pid")
		portPath := filepath.Join(oroHome, "dolt-server.port")
		// PID 99999 is almost certainly dead.
		if err := os.WriteFile(pidPath, []byte("99999"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(portPath, []byte("13307"), 0o600); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:  newFakeCmd(),
			w:       &buf,
			oroHome: oroHome,
		}

		result := cleanupSharedDoltPID(cfg)

		if !result {
			t.Error("expected cleanupSharedDoltPID to return true for stale PID")
		}
		if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
			t.Error("expected PID file to be removed")
		}
		if _, err := os.Stat(portPath); !os.IsNotExist(err) {
			t.Error("expected port file to be removed")
		}
		if !strings.Contains(buf.String(), "stale") {
			t.Errorf("expected output to mention stale, got: %s", buf.String())
		}
	})

	t.Run("skips when shared server is active", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := filepath.Join(tmpDir, "oro")
		if err := os.MkdirAll(oroHome, 0o750); err != nil {
			t.Fatal(err)
		}

		pidPath := filepath.Join(oroHome, "dolt-server.pid")
		// Use current process PID — guaranteed alive.
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:  newFakeCmd(),
			w:       &buf,
			oroHome: oroHome,
		}

		if cleanupSharedDoltPID(cfg) {
			t.Error("expected cleanupSharedDoltPID to return false when server is active")
		}
		if _, err := os.Stat(pidPath); err != nil {
			t.Error("PID file should NOT be removed when server is active")
		}
	})

	t.Run("returns false when oroHome is empty", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:  newFakeCmd(),
			w:       &buf,
			oroHome: "",
		}

		if cleanupSharedDoltPID(cfg) {
			t.Error("expected false when oroHome is empty")
		}
	})

	t.Run("returns false when no PID file exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		oroHome := filepath.Join(tmpDir, "oro")
		if err := os.MkdirAll(oroHome, 0o750); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		cfg := &cleanupConfig{
			runner:  newFakeCmd(),
			w:       &buf,
			oroHome: oroHome,
		}

		if cleanupSharedDoltPID(cfg) {
			t.Error("expected false when no PID file")
		}
	})
}

// TestCleanupEpicBranches verifies that cleanupAgentBranches deletes epic/* branches.
func TestCleanupEpicBranches(t *testing.T) {
	fake := newFakeCmd()
	// tmux has-session fails
	fake.errs[key("tmux", "has-session", "-t", "oro")] = fmt.Errorf("no session")
	// pgrep finds no workers
	fake.errs[key("pgrep", "-f", "ORO_ROLE")] = fmt.Errorf("no match")
	// git branch --list returns epic branches
	fake.output[key("git", "branch", "--list", "epic/*")] = "  epic/branch-1\n  epic/branch-2\n"
	// no agent branches
	fake.output[key("git", "branch", "--list", "agent/*")] = ""
	// bd list returns empty
	fake.output[key("bd", "list", "--status=in_progress", "--json")] = "[]"

	tmpDir := t.TempDir()
	var buf bytes.Buffer
	cfg := &cleanupConfig{
		runner:   fake,
		w:        &buf,
		tmuxName: TmuxSessionName(""),
		pidPath:  filepath.Join(tmpDir, "oro.pid"),
		sockPath: filepath.Join(tmpDir, "oro.sock"),
		signalFn: func(int) error { return nil },
		aliveFn:  func(int) bool { return false },
	}

	err := runCleanup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git branch -D was called for each epic branch
	var deletedBranches []string
	for _, call := range fake.calls {
		if len(call) >= 4 && call[0] == "git" && call[1] == "branch" && call[2] == "-D" {
			deletedBranches = append(deletedBranches, call[3])
		}
	}

	if len(deletedBranches) != 2 {
		t.Fatalf("expected 2 branch deletions, got %d: %v", len(deletedBranches), deletedBranches)
	}

	found := map[string]bool{}
	for _, b := range deletedBranches {
		found[b] = true
	}
	if !found["epic/branch-1"] {
		t.Error("expected epic/branch-1 to be deleted")
	}
	if !found["epic/branch-2"] {
		t.Error("expected epic/branch-2 to be deleted")
	}

	out := buf.String()
	if !strings.Contains(out, "epic/branch-1") || !strings.Contains(out, "epic/branch-2") {
		t.Errorf("expected output to mention deleted epic branches, got: %s", out)
	}
}
