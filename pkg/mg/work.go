// Package mg provides the mardi-gras TUI for oro, including
// worker dispatch via tmux pane splits.
package mg

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// InTmux returns true if running inside a tmux session.
//
//oro:testonly
func InTmux() bool {
	return os.Getenv("TMUX") != ""
}

// TmuxAvailable returns true if tmux binary is on PATH.
//
//oro:testonly
func TmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// WorkAvailable returns true if the oro binary is on PATH.
//
//oro:testonly
func WorkAvailable() bool {
	_, err := exec.LookPath("oro")
	return err == nil
}

// LaunchWorkInTmux splits a tmux pane running `oro work <beadID>`.
// Tags the pane with @oro_mg_work=<beadID> for tracking.
// Returns paneID on success.
//
//oro:testonly
func LaunchWorkInTmux(beadID, projectDir string) (string, error) {
	tmuxArgs := []string{
		"split-window",
		"-h",        // vertical split (pane to the right)
		"-l", "60%", // worker gets 60% of width
		"-d",             // don't switch focus
		"-c", projectDir, // working directory
		"-P", "-F", "#{pane_id}", // print the new pane ID
		"--",
		"oro", "work", beadID,
	}

	cmd := exec.Command("tmux", tmuxArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux split-window: %w", err)
	}
	paneID := strings.TrimSpace(string(out))

	// Tag the pane so PollWorkerPanes can find it later.
	_ = exec.Command("tmux", "set-option", "-p", "-t", paneID,
		"@oro_mg_work", beadID).Run()

	return paneID, nil
}

// PollWorkerPanes queries tmux for panes tagged with @oro_mg_work.
// Returns map of beadID -> paneID.
func PollWorkerPanes() (map[string]string, error) {
	out, err := exec.Command("tmux", "list-panes", "-a",
		"-F", "#{@oro_mg_work}\t#{pane_id}").Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	return parseWorkerPanes(string(out)), nil
}

// parseWorkerPanes extracts worker panes from tmux list-panes output.
// Each line is "<beadID>\t%<paneNum>" for tagged panes, or "\t%<paneNum>" for untagged.
func parseWorkerPanes(output string) map[string]string {
	panes := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		tag := strings.TrimSpace(parts[0])
		paneID := strings.TrimSpace(parts[1])
		if tag != "" && paneID != "" {
			panes[tag] = paneID
		}
	}
	return panes
}

// KillWorkerPane closes the tmux pane for the given bead.
//
//oro:testonly
func KillWorkerPane(beadID string) error {
	panes, err := PollWorkerPanes()
	if err != nil {
		return err
	}
	paneID, ok := panes[beadID]
	if !ok {
		return fmt.Errorf("no worker pane for %s", beadID)
	}
	return exec.Command("tmux", "kill-pane", "-t", paneID).Run()
}

// SelectWorkerPane switches focus to the tmux pane for the given bead.
//
//oro:testonly
func SelectWorkerPane(beadID string) error {
	panes, err := PollWorkerPanes()
	if err != nil {
		return err
	}
	paneID, ok := panes[beadID]
	if !ok {
		return fmt.Errorf("no worker pane for %s", beadID)
	}
	return exec.Command("tmux", "select-pane", "-t", paneID).Run()
}

// WorkCommand returns an *exec.Cmd for `oro work <beadID>` (non-tmux fallback).
//
//oro:testonly
func WorkCommand(beadID, projectDir string) *exec.Cmd {
	cmd := exec.Command("oro", "work", beadID)
	cmd.Dir = projectDir
	return cmd
}
