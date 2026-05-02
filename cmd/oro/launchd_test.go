package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallLaunchAgent(t *testing.T) {
	t.Run("removes plist file", func(t *testing.T) {
		homeDir := t.TempDir()
		launchAgentsDir := filepath.Join(homeDir, "Library", "LaunchAgents")
		if err := os.MkdirAll(launchAgentsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		plistPath := filepath.Join(launchAgentsDir, launchAgentLabel+".plist")
		if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o600); err != nil {
			t.Fatalf("write plist: %v", err)
		}

		orig := runLaunchctl
		t.Cleanup(func() { runLaunchctl = orig })
		runLaunchctl = func(_ ...string) error { return nil }

		if err := uninstallLaunchAgent(homeDir); err != nil {
			t.Fatalf("uninstallLaunchAgent error: %v", err)
		}

		if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
			t.Error("plist file should be removed after uninstall")
		}
	})

	t.Run("returns nil when plist does not exist", func(t *testing.T) {
		homeDir := t.TempDir()

		orig := runLaunchctl
		t.Cleanup(func() { runLaunchctl = orig })
		runLaunchctl = func(_ ...string) error { return nil }

		if err := uninstallLaunchAgent(homeDir); err != nil {
			t.Errorf("uninstallLaunchAgent on missing plist = %v, want nil", err)
		}
	})

	t.Run("calls bootout before removing plist", func(t *testing.T) {
		homeDir := t.TempDir()
		launchAgentsPath := filepath.Join(homeDir, "Library", "LaunchAgents")
		if err := os.MkdirAll(launchAgentsPath, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		plistPath := launchAgentPlistPath(homeDir)
		if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o600); err != nil {
			t.Fatalf("write plist: %v", err)
		}

		var calls [][]string
		orig := runLaunchctl
		t.Cleanup(func() { runLaunchctl = orig })
		runLaunchctl = func(args ...string) error {
			calls = append(calls, append([]string{}, args...))
			return nil
		}

		if err := uninstallLaunchAgent(homeDir); err != nil {
			t.Fatalf("uninstallLaunchAgent: %v", err)
		}

		if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
			t.Error("plist should be removed after uninstall")
		}

		bootoutFound := false
		for _, c := range calls {
			if c[0] == "bootout" {
				bootoutFound = true
			}
		}
		if !bootoutFound {
			t.Errorf("bootout was not called during uninstall, got: %v", calls)
		}
	})
}
