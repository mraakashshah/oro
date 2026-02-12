package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// cleanupConfig holds injectable dependencies for the cleanup command.
type cleanupConfig struct {
	runner   CmdRunner
	w        io.Writer
	tmuxName string
	pidPath  string
	sockPath string
	signalFn func(int) error // sends SIGTERM; injectable for testing
	aliveFn  func(int) bool  // checks process liveness; injectable for testing
}

// newCleanupCmd creates the "oro cleanup" subcommand.
func newCleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cleanup",
		Short: "Clean all stale state after a crash",
		Long: `Idempotently cleans up all Oro state: kills tmux session, dispatcher,
and worker processes; removes stale PID/socket files; prunes git worktrees;
deletes agent/* branches; and resets orphaned in_progress beads to open.

Safe to run anytime. If nothing is running, reports "nothing to clean".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath, err := oroPath("ORO_PID_PATH", "oro.pid")
			if err != nil {
				return fmt.Errorf("get pid path: %w", err)
			}
			sockPath, err := oroPath("ORO_SOCKET_PATH", "oro.sock")
			if err != nil {
				return fmt.Errorf("get socket path: %w", err)
			}

			cfg := &cleanupConfig{
				runner:   &ExecRunner{},
				w:        cmd.OutOrStdout(),
				tmuxName: "oro",
				pidPath:  pidPath,
				sockPath: sockPath,
				signalFn: defaultSignal,
				aliveFn:  IsProcessAlive,
			}

			return runCleanup(cmd.Context(), cfg)
		},
	}
}

// beadEntry represents a minimal bead from bd list JSON output.
type beadEntry struct {
	ID string `json:"id"`
}

// runCleanup performs best-effort cleanup of all Oro state.
// Each step continues on error, reporting warnings. Returns nil on success
// even if individual steps had warnings.
func runCleanup(_ context.Context, cfg *cleanupConfig) error {
	cleaned := false

	// 1. Kill tmux session if it exists.
	if cleanedTmux := cleanupTmux(cfg); cleanedTmux {
		cleaned = true
	}

	// 2. Kill dispatcher process if running (read PID file).
	if cleanedDispatcher := cleanupDispatcher(cfg); cleanedDispatcher {
		cleaned = true
	}

	// 3. Kill worker claude processes with ORO_ROLE env var.
	if cleanedWorkers := cleanupWorkers(cfg); cleanedWorkers {
		cleaned = true
	}

	// 4. Remove stale PID file.
	if cleanedPID := cleanupPIDFile(cfg); cleanedPID {
		cleaned = true
	}

	// 5. Remove stale socket file.
	if cleanedSock := cleanupSocketFile(cfg); cleanedSock {
		cleaned = true
	}

	// 6. Prune git worktrees.
	cleanupWorktrees(cfg)

	// 7. Delete agent/* branches.
	if cleanedBranches := cleanupAgentBranches(cfg); cleanedBranches {
		cleaned = true
	}

	// 8. Reset in_progress beads back to open.
	if cleanedBeads := cleanupBeads(cfg); cleanedBeads {
		cleaned = true
	}

	if !cleaned {
		fmt.Fprintln(cfg.w, "nothing to clean")
	}

	return nil
}

// cleanupTmux kills the tmux session if it exists. Returns true if something was cleaned.
func cleanupTmux(cfg *cleanupConfig) bool {
	tmux := &TmuxSession{Name: cfg.tmuxName, Runner: cfg.runner}
	if !tmux.Exists() {
		return false
	}

	fmt.Fprintf(cfg.w, "killing tmux session %q\n", cfg.tmuxName)
	if err := tmux.Kill(); err != nil {
		fmt.Fprintf(cfg.w, "warning: tmux kill: %v\n", err)
	}
	return true
}

// cleanupDispatcher signals the dispatcher process if running. Returns true if something was cleaned.
func cleanupDispatcher(cfg *cleanupConfig) bool {
	pid, err := ReadPIDFile(cfg.pidPath)
	if err != nil {
		// No PID file or unreadable — nothing to do.
		return false
	}

	if !cfg.aliveFn(pid) {
		// Process is dead, PID file is stale — will be cleaned in step 4.
		return false
	}

	fmt.Fprintf(cfg.w, "killing dispatcher (PID %d)\n", pid)
	if err := cfg.signalFn(pid); err != nil {
		fmt.Fprintf(cfg.w, "warning: signal dispatcher PID %d: %v\n", pid, err)
	}
	return true
}

// cleanupWorkers finds and kills worker processes with ORO_ROLE env var.
// Returns true if something was cleaned.
func cleanupWorkers(cfg *cleanupConfig) bool {
	out, err := cfg.runner.Run("pgrep", "-f", "ORO_ROLE")
	if err != nil {
		// No matching processes.
		return false
	}

	pids := parseWorkerPIDs(out)
	if len(pids) == 0 {
		return false
	}

	fmt.Fprintf(cfg.w, "killing %d worker process(es)\n", len(pids))
	for _, pid := range pids {
		if err := cfg.signalFn(pid); err != nil {
			fmt.Fprintf(cfg.w, "warning: signal worker PID %d: %v\n", pid, err)
		}
	}
	return true
}

// parseWorkerPIDs parses newline-separated PIDs from pgrep output.
func parseWorkerPIDs(output string) []int {
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// cleanupPIDFile removes a stale PID file. Returns true if the file existed and was removed.
func cleanupPIDFile(cfg *cleanupConfig) bool {
	if _, err := os.Stat(cfg.pidPath); errors.Is(err, os.ErrNotExist) {
		return false
	}

	fmt.Fprintf(cfg.w, "removing stale pid file %s\n", cfg.pidPath)
	if err := RemovePIDFile(cfg.pidPath); err != nil {
		fmt.Fprintf(cfg.w, "warning: remove pid file: %v\n", err)
	}
	return true
}

// cleanupSocketFile removes a stale socket file. Returns true if the file existed and was removed.
func cleanupSocketFile(cfg *cleanupConfig) bool {
	if _, err := os.Stat(cfg.sockPath); errors.Is(err, os.ErrNotExist) {
		return false
	}

	fmt.Fprintf(cfg.w, "removing stale socket file %s\n", cfg.sockPath)
	err := os.Remove(cfg.sockPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(cfg.w, "warning: remove socket file: %v\n", err)
	}
	return true
}

// cleanupWorktrees runs git worktree prune.
func cleanupWorktrees(cfg *cleanupConfig) {
	if _, err := cfg.runner.Run("git", "worktree", "prune"); err != nil {
		fmt.Fprintf(cfg.w, "warning: git worktree prune: %v\n", err)
	}
}

// cleanupAgentBranches deletes local agent/* branches. Returns true if branches were deleted.
func cleanupAgentBranches(cfg *cleanupConfig) bool {
	out, err := cfg.runner.Run("git", "branch", "--list", "agent/*")
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: list agent branches: %v\n", err)
		return false
	}

	branches := parseBranchNames(out)
	if len(branches) == 0 {
		return false
	}

	for _, branch := range branches {
		fmt.Fprintf(cfg.w, "deleting branch %s\n", branch)
		if _, err := cfg.runner.Run("git", "branch", "-D", branch); err != nil {
			fmt.Fprintf(cfg.w, "warning: delete branch %s: %v\n", branch, err)
		}
	}
	return true
}

// parseBranchNames parses branch names from git branch output (strips leading whitespace and *).
func parseBranchNames(output string) []string {
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "* ")
		if line == "" {
			continue
		}
		branches = append(branches, line)
	}
	return branches
}

// cleanupBeads resets in_progress beads back to open. Returns true if beads were reset.
func cleanupBeads(cfg *cleanupConfig) bool {
	out, err := cfg.runner.Run("bd", "list", "--status=in_progress", "--format=json")
	if err != nil {
		fmt.Fprintf(cfg.w, "warning: list in_progress beads: %v\n", err)
		return false
	}

	var beads []beadEntry
	if err := json.Unmarshal([]byte(out), &beads); err != nil {
		fmt.Fprintf(cfg.w, "warning: parse bead list: %v\n", err)
		return false
	}

	if len(beads) == 0 {
		return false
	}

	for _, bead := range beads {
		fmt.Fprintf(cfg.w, "resetting bead %s to open\n", bead.ID)
		if _, err := cfg.runner.Run("bd", "update", bead.ID, "--status=open"); err != nil {
			fmt.Fprintf(cfg.w, "warning: reset bead %s: %v\n", bead.ID, err)
		}
	}
	return true
}
