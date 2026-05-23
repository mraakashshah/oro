package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	// launchAgentLabel is the reverse-DNS label used for the launchd plist.
	launchAgentLabel = "dev.getoro.oro"
)

// errLaunchctlNotFound is returned by runLaunchctl when launchctl is not in PATH.
var errLaunchctlNotFound = errors.New("launchctl not in PATH")

//nolint:gochecknoglobals // replaceable in tests for hermetic launchctl verification
var runLaunchctl = defaultRunLaunchctl

func defaultRunLaunchctl(args ...string) error {
	path, err := exec.LookPath("launchctl")
	if err != nil {
		return errLaunchctlNotFound
	}
	//nolint:gosec // args are trusted internal constants and computed values
	if err := exec.CommandContext(context.Background(), path, args...).Run(); err != nil { //nolint:noctx // one-shot; no caller context available
		return fmt.Errorf("launchctl: %w", err)
	}
	return nil
}

// launchAgentsDir returns the path to ~/Library/LaunchAgents.
func launchAgentsDir(homeDir string) string {
	return filepath.Join(homeDir, "Library", "LaunchAgents")
}

// launchAgentPlistPath returns the full path to the installed plist file.
func launchAgentPlistPath(homeDir string) string {
	return filepath.Join(launchAgentsDir(homeDir), launchAgentLabel+".plist")
}

// uninstallLaunchAgent boots out the launchd agent and removes
// ~/Library/LaunchAgents/<label>.plist.
// Returns nil if the file does not exist (idempotent).
func uninstallLaunchAgent(homeDir string) error {
	uid := os.Getuid()
	serviceTarget := fmt.Sprintf("gui/%d/%s", uid, launchAgentLabel)
	// Ignore bootout error — agent may not be loaded.
	_ = runLaunchctl("bootout", serviceTarget)

	plistPath := launchAgentPlistPath(homeDir)
	err := os.Remove(plistPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove launch agent plist: %w", err)
	}
	return nil
}
