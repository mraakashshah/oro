package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"text/template"
)

const (
	// launchAgentLabel is the reverse-DNS label used for the launchd plist.
	launchAgentLabel = "dev.getoro.dolt"
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

//nolint:gochecknoglobals // package-level template init: parsed once at startup, read-only after
var plistTemplate = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.DoltPath}}</string>
		<string>sql-server</string>
		<string>--host</string>
		<string>127.0.0.1</string>
		<string>--port</string>
		<string>{{.Port}}</string>
		<string>--data-dir</string>
		<string>{{.DataDir}}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`))

// plistData holds template values for the launchd plist.
type plistData struct {
	Label    string
	DoltPath string
	Port     string
	DataDir  string
}

// generatePlist generates a macOS launchd plist for the shared Dolt server.
// doltPath is the absolute path to the dolt binary.
// homeDir is the user's home directory (~).
// port is the TCP port the server listens on.
//
// The data directory is set to <homeDir>/.oro/dolt.
// Returns exec.ErrNotFound if doltPath is empty.
func generatePlist(doltPath, homeDir string, port int) ([]byte, error) {
	if doltPath == "" {
		return nil, exec.ErrNotFound
	}

	data := plistData{
		Label:    launchAgentLabel,
		DoltPath: doltPath,
		Port:     strconv.Itoa(port),
		DataDir:  filepath.Join(homeDir, ".oro", "dolt"),
	}

	var buf bytes.Buffer
	if err := plistTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render plist template: %w", err)
	}
	return buf.Bytes(), nil
}

// launchAgentsDir returns the path to ~/Library/LaunchAgents.
func launchAgentsDir(homeDir string) string {
	return filepath.Join(homeDir, "Library", "LaunchAgents")
}

// launchAgentPlistPath returns the full path to the installed plist file.
func launchAgentPlistPath(homeDir string) string {
	return filepath.Join(launchAgentsDir(homeDir), launchAgentLabel+".plist")
}

// installLaunchAgent writes plistBytes to ~/Library/LaunchAgents/<label>.plist
// and bootstraps the launchd agent via launchctl.
// If launchctl is not in PATH, the plist is still written and the function
// returns nil (with a warning to stderr).
func installLaunchAgent(plistBytes []byte, homeDir string) error {
	dir := launchAgentsDir(homeDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	plistPath := launchAgentPlistPath(homeDir)
	if err := os.WriteFile(plistPath, plistBytes, 0o600); err != nil { //nolint:gosec // path constructed from trusted homeDir
		return fmt.Errorf("write launch agent plist: %w", err)
	}

	uid := os.Getuid()
	serviceTarget := fmt.Sprintf("gui/%d/%s", uid, launchAgentLabel)
	domain := fmt.Sprintf("gui/%d", uid)

	// Bootout first — ignore error (agent may not be loaded yet).
	if err := runLaunchctl("bootout", serviceTarget); errors.Is(err, errLaunchctlNotFound) {
		fmt.Fprintf(os.Stderr, "warn: launchctl not in PATH; plist written but agent not bootstrapped\n")
		return nil
	}

	if err := runLaunchctl("bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return nil
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

// isLaunchAgentLoaded reports whether the launchd agent is currently loaded.
// Uses `launchctl list <label>` — returns false if launchctl is not in PATH
// or if the agent is not loaded.
func isLaunchAgentLoaded() bool {
	launchctlPath, err := exec.LookPath("launchctl")
	if err != nil {
		return false
	}
	//nolint:gosec // args constructed from trusted constant
	cmd := exec.CommandContext(context.Background(), launchctlPath, "list", launchAgentLabel) //nolint:noctx // one-shot query; no caller context available
	return cmd.Run() == nil
}
