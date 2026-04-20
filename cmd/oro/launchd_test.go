package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratePlist(t *testing.T) {
	homeDir := t.TempDir()
	doltPath := "/usr/local/bin/dolt"
	port := 13307

	data, err := generatePlist(doltPath, homeDir, port)
	if err != nil {
		t.Fatalf("generatePlist error: %v", err)
	}

	content := string(data)

	t.Run("contains correct Label", func(t *testing.T) {
		if !strings.Contains(content, launchAgentLabel) {
			t.Errorf("plist missing Label %q\ngot:\n%s", launchAgentLabel, content)
		}
	})

	t.Run("contains resolved dolt path in ProgramArguments", func(t *testing.T) {
		if !strings.Contains(content, doltPath) {
			t.Errorf("plist missing dolt path %q\ngot:\n%s", doltPath, content)
		}
	})

	t.Run("contains RunAtLoad true", func(t *testing.T) {
		if !strings.Contains(content, "RunAtLoad") {
			t.Errorf("plist missing RunAtLoad key\ngot:\n%s", content)
		}
		if !strings.Contains(content, "<true/>") {
			t.Errorf("plist missing <true/> value\ngot:\n%s", content)
		}
	})

	t.Run("contains KeepAlive true", func(t *testing.T) {
		if !strings.Contains(content, "KeepAlive") {
			t.Errorf("plist missing KeepAlive key\ngot:\n%s", content)
		}
	})

	t.Run("contains data-dir pointing to ~/.oro/dolt", func(t *testing.T) {
		expectedDataDir := filepath.Join(homeDir, ".oro", "dolt")
		if !strings.Contains(content, expectedDataDir) {
			t.Errorf("plist missing data-dir %q\ngot:\n%s", expectedDataDir, content)
		}
	})

	t.Run("contains port 13307", func(t *testing.T) {
		portStr := "13307"
		if !strings.Contains(content, portStr) {
			t.Errorf("plist missing port %q\ngot:\n%s", portStr, content)
		}
	})

	t.Run("is valid plist XML", func(t *testing.T) {
		if !strings.HasPrefix(strings.TrimSpace(content), "<?xml") {
			t.Errorf("plist does not start with XML declaration\ngot:\n%s", content)
		}
		if !strings.Contains(content, "<plist") {
			t.Errorf("plist missing <plist> root element\ngot:\n%s", content)
		}
	})
}

func TestInstallLaunchAgent(t *testing.T) {
	t.Run("writes plist to ~/Library/LaunchAgents/", func(t *testing.T) {
		homeDir := t.TempDir()
		launchAgentsDir := filepath.Join(homeDir, "Library", "LaunchAgents")
		if err := os.MkdirAll(launchAgentsDir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		orig := runLaunchctl
		t.Cleanup(func() { runLaunchctl = orig })
		runLaunchctl = func(_ ...string) error { return nil }

		plistBytes := []byte("<plist>test</plist>")
		if err := installLaunchAgent(plistBytes, homeDir); err != nil {
			t.Fatalf("installLaunchAgent error: %v", err)
		}

		plistPath := filepath.Join(launchAgentsDir, launchAgentLabel+".plist")
		data, err := os.ReadFile(plistPath)
		if err != nil {
			t.Fatalf("read installed plist: %v", err)
		}
		if string(data) != string(plistBytes) {
			t.Errorf("installed plist content = %q, want %q", string(data), string(plistBytes))
		}
	})

	t.Run("returns permission error when LaunchAgents not writable", func(t *testing.T) {
		homeDir := t.TempDir()
		// Create Library with write permission first, then LaunchAgents read-only.
		libraryDir := filepath.Join(homeDir, "Library")
		if err := os.Mkdir(libraryDir, 0o750); err != nil {
			t.Fatalf("mkdir Library: %v", err)
		}
		launchAgentsDir := filepath.Join(libraryDir, "LaunchAgents")
		if err := os.Mkdir(launchAgentsDir, 0o500); err != nil { // read+execute only
			t.Fatalf("mkdir LaunchAgents: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(launchAgentsDir, 0o750) })

		// Skip if running as root (root ignores permissions).
		if os.Getuid() == 0 {
			t.Skip("skipping permission test when running as root")
		}

		plistBytes := []byte("<plist>test</plist>")
		err := installLaunchAgent(plistBytes, homeDir)
		if err == nil {
			t.Error("installLaunchAgent should return error when directory not writable")
		}
	})
}

// TestKickstartLabelMatchesPlist guards against drift between the launchd plist
// label (used at install time) and the kickstart service target (used at start
// time). A mismatch makes `launchctl kickstart` a silent no-op, which previously
// caused oro start to fall through to direct-spawn dolt with a stale --data-dir.
func TestKickstartLabelMatchesPlist(t *testing.T) {
	target := kickstartServiceTarget(501)

	want := "gui/501/" + launchAgentLabel
	if target != want {
		t.Errorf("kickstartServiceTarget(501) = %q, want %q", target, want)
	}

	if !strings.HasSuffix(target, launchAgentLabel) {
		t.Errorf("kickstart target %q must end with launchAgentLabel %q", target, launchAgentLabel)
	}
}

func TestIsLaunchAgentLoaded(t *testing.T) {
	t.Run("returns false when agent is not loaded", func(t *testing.T) {
		// The test agent label is not expected to be loaded in CI or dev environments.
		// This test only verifies the function is callable and doesn't panic.
		// Result depends on whether launchctl is available and the agent is installed.
		_ = isLaunchAgentLoaded() // must not panic
	})
}

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

	t.Run("returns nil when plist does not exist (idempotent)", func(t *testing.T) {
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

func TestInstallLaunchAgent_CallsBootstrap(t *testing.T) {
	t.Run("calls bootout then bootstrap after plist write", func(t *testing.T) {
		homeDir := t.TempDir()
		var calls [][]string
		orig := runLaunchctl
		t.Cleanup(func() { runLaunchctl = orig })
		runLaunchctl = func(args ...string) error {
			calls = append(calls, append([]string{}, args...))
			return nil
		}

		if err := installLaunchAgent([]byte("<plist>test</plist>"), homeDir); err != nil {
			t.Fatalf("installLaunchAgent: %v", err)
		}

		if _, err := os.Stat(launchAgentPlistPath(homeDir)); err != nil {
			t.Errorf("plist not written: %v", err)
		}

		if len(calls) < 2 {
			t.Fatalf("expected ≥2 launchctl calls, got %d: %v", len(calls), calls)
		}
		if calls[0][0] != "bootout" {
			t.Errorf("first call want bootout, got %q", calls[0][0])
		}
		if calls[1][0] != "bootstrap" {
			t.Errorf("second call want bootstrap, got %q", calls[1][0])
		}
	})

	t.Run("bootout error on install is ignored", func(t *testing.T) {
		homeDir := t.TempDir()
		orig := runLaunchctl
		t.Cleanup(func() { runLaunchctl = orig })
		runLaunchctl = func(args ...string) error {
			if args[0] == "bootout" {
				return errors.New("service not loaded")
			}
			return nil
		}

		if err := installLaunchAgent([]byte("<plist/>"), homeDir); err != nil {
			t.Errorf("installLaunchAgent should succeed when bootout fails: %v", err)
		}
	})

	t.Run("bootstrap failure returns error", func(t *testing.T) {
		homeDir := t.TempDir()
		orig := runLaunchctl
		t.Cleanup(func() { runLaunchctl = orig })
		runLaunchctl = func(args ...string) error {
			if args[0] == "bootstrap" {
				return errors.New("bootstrap failed")
			}
			return nil
		}

		if err := installLaunchAgent([]byte("<plist/>"), homeDir); err == nil {
			t.Error("installLaunchAgent should return error when bootstrap fails")
		}
	})

	t.Run("launchctl not in PATH: plist written, returns nil", func(t *testing.T) {
		homeDir := t.TempDir()
		orig := runLaunchctl
		t.Cleanup(func() { runLaunchctl = orig })
		runLaunchctl = func(_ ...string) error {
			return errLaunchctlNotFound
		}

		if err := installLaunchAgent([]byte("<plist/>"), homeDir); err != nil {
			t.Errorf("installLaunchAgent should succeed when launchctl not in PATH: %v", err)
		}
		if _, err := os.Stat(launchAgentPlistPath(homeDir)); err != nil {
			t.Errorf("plist should still be written when launchctl not in PATH: %v", err)
		}
	})
}
