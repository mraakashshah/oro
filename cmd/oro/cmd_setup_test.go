package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupPrereqs verifies that setup fails fast when a prerequisite is missing.
func TestSetupPrereqs(t *testing.T) {
	t.Run("fails when prereq is missing", func(t *testing.T) {
		var buf bytes.Buffer
		prereqs := []prereqDef{
			{Name: "nonexistent-tool-xyz", CheckCmd: "nonexistent-tool-xyz", InstallHint: "Install nonexistent-tool-xyz"},
		}

		err := checkPrereqs(&buf, prereqs)
		if err == nil {
			t.Fatal("expected error for missing prerequisite, got nil")
		}

		errMsg := err.Error()
		if !strings.Contains(errMsg, "nonexistent-tool-xyz") {
			t.Errorf("error should mention the missing tool, got: %s", errMsg)
		}
		if !strings.Contains(errMsg, "Install nonexistent-tool-xyz") {
			t.Errorf("error should include install hint, got: %s", errMsg)
		}
	})

	t.Run("succeeds when all prereqs present", func(t *testing.T) {
		var buf bytes.Buffer
		// "git" should always be available in dev/CI environments.
		prereqs := []prereqDef{
			{Name: "git", CheckCmd: "git", InstallHint: "Install git"},
		}

		err := checkPrereqs(&buf, prereqs)
		if err != nil {
			t.Fatalf("expected no error for present prereq, got: %v", err)
		}

		output := buf.String()
		if !strings.Contains(output, "git: found") {
			t.Errorf("output should confirm git found, got: %s", output)
		}
	})

	t.Run("fails on first missing prereq", func(t *testing.T) {
		var buf bytes.Buffer
		prereqs := []prereqDef{
			{Name: "git", CheckCmd: "git", InstallHint: "Install git"},
			{Name: "nonexistent-abc", CheckCmd: "nonexistent-abc", InstallHint: "Get abc from somewhere"},
			{Name: "go", CheckCmd: "go", InstallHint: "Install go"},
		}

		err := checkPrereqs(&buf, prereqs)
		if err == nil {
			t.Fatal("expected error for missing prerequisite")
		}

		// Should fail on the second one, not reach the third.
		errMsg := err.Error()
		if !strings.Contains(errMsg, "nonexistent-abc") {
			t.Errorf("error should reference nonexistent-abc, got: %s", errMsg)
		}

		// git should have been checked (it comes first).
		output := buf.String()
		if !strings.Contains(output, "git: found") {
			t.Errorf("output should confirm git was checked, got: %s", output)
		}
	})
}

// TestSetupDryRun verifies that dry-run mode prints actions without executing.
func TestSetupDryRun(t *testing.T) {
	var buf bytes.Buffer

	opts := setupOptions{
		projectRoot: t.TempDir(),
		projectName: "test-project",
		dryRun:      true,
	}

	err := runSetup(&buf, opts)
	if err != nil {
		t.Fatalf("dry-run should not fail, got: %v", err)
	}

	output := buf.String()

	// Phase 1: Should mention dry-run prereq check.
	if !strings.Contains(output, "[dry-run] Would check for: claude, git, brew") {
		t.Error("dry-run should mention prereq check")
	}

	// Phase 2: Should mention scanning for languages.
	if !strings.Contains(output, "[dry-run] Would scan") {
		t.Error("dry-run should mention language scanning")
	}

	// Phase 3: Should mention tool installation.
	if !strings.Contains(output, "[dry-run] Would check and install missing tools") {
		t.Error("dry-run should mention tool installation")
	}

	// Phase 4: Should mention bootstrapping.
	if !strings.Contains(output, "[dry-run] Would bootstrap project") {
		t.Error("dry-run should mention bootstrapping")
	}

	// Phase 5: Should mention doctor check.
	if !strings.Contains(output, "[dry-run] Would verify") {
		t.Error("dry-run should mention health verification")
	}

	// Should complete successfully.
	if !strings.Contains(output, "Setup complete.") {
		t.Error("dry-run should print completion message")
	}
}

func TestSetupPhase3DryRunDoesNotExecuteToolChecks(t *testing.T) {
	tmpDir := t.TempDir()
	markerPath := filepath.Join(tmpDir, "marker")
	toolPath := filepath.Join(tmpDir, "fake-tool")
	toolScript := "#!/bin/sh\nprintf executed > " + markerPath + "\n"
	if err := os.WriteFile(toolPath, []byte(toolScript), 0o755); err != nil { //nolint:gosec // test helper script
		t.Fatalf("failed to write fake tool: %v", err)
	}

	origDefs := defaultToolDefs
	defaultToolDefs = []toolDef{
		{Name: "fake-tool", Category: "test", CheckCmd: toolPath, CheckArgs: []string{"--version"}},
	}
	t.Cleanup(func() { defaultToolDefs = origDefs })

	var buf bytes.Buffer
	if err := setupPhase3Tools(&buf, setupOptions{dryRun: true}); err != nil {
		t.Fatalf("dry-run phase 3 should not fail: %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run phase 3 executed tool check, marker stat err=%v", err)
	}
}

// TestSetupSkipTools verifies that --skip-tools skips Phase 3.
func TestSetupSkipTools(t *testing.T) {
	var buf bytes.Buffer

	opts := setupOptions{
		projectRoot: t.TempDir(),
		projectName: "test-project",
		dryRun:      true,
		skipTools:   true,
	}

	err := runSetup(&buf, opts)
	if err != nil {
		t.Fatalf("setup with --skip-tools should not fail, got: %v", err)
	}

	output := buf.String()

	// Phase 3 should be explicitly skipped.
	if !strings.Contains(output, "Phase 3: Tool installation skipped (--skip-tools).") {
		t.Error("output should indicate tool installation was skipped")
	}

	// Should NOT contain any tool installation messages.
	if strings.Contains(output, "Would check and install missing tools") {
		t.Error("output should not contain tool installation dry-run messages when skip-tools is set")
	}

	// Other phases should still be present.
	if !strings.Contains(output, "Phase 1:") {
		t.Error("Phase 1 should still run")
	}
	if !strings.Contains(output, "Phase 2:") {
		t.Error("Phase 2 should still run")
	}
	if !strings.Contains(output, "Phase 4:") {
		t.Error("Phase 4 should still run")
	}
	if !strings.Contains(output, "Phase 5:") {
		t.Error("Phase 5 should still run")
	}
}

// TestSetupDryRunForce verifies that --force is reported in dry-run mode.
func TestSetupDryRunForce(t *testing.T) {
	var buf bytes.Buffer

	opts := setupOptions{
		projectRoot: t.TempDir(),
		projectName: "test-project",
		dryRun:      true,
		force:       true,
	}

	err := runSetup(&buf, opts)
	if err != nil {
		t.Fatalf("dry-run with --force should not fail, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "--force: would overwrite existing files") {
		t.Error("dry-run with --force should mention overwrite")
	}
}

// TestSetupDryRunDev verifies that --dev is reported in dry-run mode.
func TestSetupDryRunDev(t *testing.T) {
	var buf bytes.Buffer

	opts := setupOptions{
		projectRoot: t.TempDir(),
		projectName: "test-project",
		dryRun:      true,
		dev:         true,
	}

	err := runSetup(&buf, opts)
	if err != nil {
		t.Fatalf("dry-run with --dev should not fail, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "--dev: Would install dev-only tools") {
		t.Error("dry-run with --dev should mention dev tools")
	}
}

// TestSetupDoctorCheck verifies the doctor function reports status correctly.
func TestSetupDoctorCheck(t *testing.T) {
	var buf bytes.Buffer

	projectRoot := t.TempDir()
	oroHome := t.TempDir()

	// Run doctor on empty dirs — everything should be MISSING.
	runDoctor(&buf, projectRoot, "test-project", oroHome)

	output := buf.String()

	// Config anchor should be missing.
	if !strings.Contains(output, "MISSING") {
		t.Error("doctor should report MISSING items in empty project")
	}

	if !strings.Contains(output, "Some items missing") {
		t.Error("doctor should report that some items are missing")
	}
}

// TestSetupNewSetupCmd verifies the cobra command is properly constructed.
func TestSetupNewSetupCmd(t *testing.T) {
	cmd := newSetupCmd()

	if cmd.Use != "setup [project-name]" {
		t.Errorf("unexpected Use: %s", cmd.Use)
	}

	// Verify flags exist.
	flags := []string{"project-root", "dev", "dry-run", "skip-tools", "force"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected flag %q to be registered", name)
		}
	}
}

// TestSetupPhase2ReturnsConfig verifies that setupPhase2Detect returns the detected
// language config so it can be threaded through to bootstrapProject without re-detection.
func TestSetupPhase2ReturnsConfig(t *testing.T) {
	t.Run("returns config for Go project", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create go.mod to trigger Go language detection.
		goModPath := filepath.Join(tmpDir, "go.mod")
		if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.21\n"), 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("failed to create go.mod: %v", err)
		}

		var buf bytes.Buffer
		opts := setupOptions{projectRoot: tmpDir, dryRun: false}

		cfg := setupPhase2Detect(&buf, opts)

		if cfg == nil {
			t.Fatal("setupPhase2Detect should return non-nil config when Go project detected")
		}
		if _, ok := cfg.Languages["go"]; !ok {
			t.Errorf("config should contain 'go' language entry, got languages: %v", cfg.Languages)
		}
	})

	t.Run("returns nil in dry-run mode", func(t *testing.T) {
		var buf bytes.Buffer
		opts := setupOptions{projectRoot: t.TempDir(), dryRun: true}

		cfg := setupPhase2Detect(&buf, opts)

		if cfg != nil {
			t.Errorf("setupPhase2Detect should return nil in dry-run mode, got: %+v", cfg)
		}
	})

	t.Run("returns non-nil but empty config when no languages detected", func(t *testing.T) {
		var buf bytes.Buffer
		opts := setupOptions{projectRoot: t.TempDir(), dryRun: false}

		cfg := setupPhase2Detect(&buf, opts)

		// Config is returned (non-nil) even when empty — caller can distinguish from dry-run.
		if cfg == nil {
			t.Fatal("setupPhase2Detect should return non-nil config even when no languages detected")
		}
	})
}

func TestSetupInstallsManagedGitHubCLI(t *testing.T) {
	t.Run("dry-run reports managed CLI setup without executing it", func(t *testing.T) {
		var buf bytes.Buffer
		lookPathCalls := 0
		runCalls := 0
		err := setupPhase1Prereqs(&buf, setupOptions{
			dryRun: true,
			installDeps: InstallDeps{
				GOOS: "darwin",
				LookPath: func(string) (string, error) {
					lookPathCalls++
					return "", errors.New("must not execute during dry-run")
				},
				Run: func(context.Context, string, ...string) ([]byte, error) {
					runCalls++
					return nil, errors.New("must not execute during dry-run")
				},
			},
		})
		if err != nil {
			t.Fatalf("setupPhase1Prereqs() dry-run error = %v", err)
		}
		if lookPathCalls != 0 || runCalls != 0 {
			t.Fatalf("dry-run executed installer dependencies: LookPath=%d Run=%d", lookPathCalls, runCalls)
		}
		if !strings.Contains(buf.String(), "[dry-run] Would ensure GitHub CLI") {
			t.Fatalf("dry-run output = %q, want managed CLI notice", buf.String())
		}
	})

	t.Run("attests an existing supported CLI without mutating Homebrew", func(t *testing.T) {
		var commands []string
		deps := InstallDeps{
			GOOS: "darwin",
			LookPath: func(name string) (string, error) {
				if name != "gh" {
					t.Fatalf("LookPath(%q), want gh", name)
				}
				return "/opt/homebrew/bin/gh", nil
			},
			Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				commands = append(commands, name+" "+strings.Join(args, " "))
				return []byte("gh version 2.63.0 (2025-01-01)\n"), nil
			},
		}

		evidence, err := EnsureManagedGitHubCLI(context.Background(), deps)
		if err != nil {
			t.Fatalf("EnsureManagedGitHubCLI() error = %v", err)
		}
		if evidence.Path != "/opt/homebrew/bin/gh" {
			t.Errorf("evidence path = %q, want managed gh path", evidence.Path)
		}
		if strings.Join(commands, "; ") != "/opt/homebrew/bin/gh --version" {
			t.Fatalf("commands = %v, want gh attestation only", commands)
		}
	})

	t.Run("installs an absent CLI with Homebrew then attests it", func(t *testing.T) {
		installed := false
		var commands []string
		deps := InstallDeps{
			GOOS: "darwin",
			LookPath: func(name string) (string, error) {
				switch name {
				case "gh":
					if !installed {
						return "", errors.New("not found")
					}
					return "/opt/homebrew/bin/gh", nil
				case "brew":
					return "/opt/homebrew/bin/brew", nil
				default:
					return "", errors.New("not found")
				}
			},
			Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				commands = append(commands, name+" "+strings.Join(args, " "))
				if name == "/opt/homebrew/bin/brew" && strings.Join(args, " ") == "install gh" {
					installed = true
					return nil, nil
				}
				return []byte("gh version 2.63.0 (2025-01-01)\n"), nil
			},
		}

		if _, err := EnsureManagedGitHubCLI(context.Background(), deps); err != nil {
			t.Fatalf("EnsureManagedGitHubCLI() error = %v", err)
		}
		want := "/opt/homebrew/bin/brew install gh; /opt/homebrew/bin/gh --version"
		if got := strings.Join(commands, "; "); got != want {
			t.Fatalf("commands = %q, want %q", got, want)
		}
	})

	for _, tc := range []struct {
		name     string
		deps     InstallDeps
		contains string
	}{
		{
			name: "missing Homebrew is actionable",
			deps: InstallDeps{
				GOOS:     "darwin",
				LookPath: func(string) (string, error) { return "", errors.New("not found") },
			},
			contains: "install homebrew",
		},
		{
			name: "Homebrew installation failure is actionable",
			deps: InstallDeps{
				GOOS: "darwin",
				LookPath: func(name string) (string, error) {
					if name == "brew" {
						return "/opt/homebrew/bin/brew", nil
					}
					return "", errors.New("not found")
				},
				Run: func(context.Context, string, ...string) ([]byte, error) {
					return nil, errors.New("formula unavailable")
				},
			},
			contains: "brew install gh",
		},
		{
			name: "unsupported existing CLI fails readiness",
			deps: InstallDeps{
				GOOS:     "darwin",
				LookPath: func(string) (string, error) { return "/opt/homebrew/bin/gh", nil },
				Run: func(context.Context, string, ...string) ([]byte, error) {
					return []byte("gh version 1.14.0\n"), nil
				},
			},
			contains: "unsupported",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EnsureManagedGitHubCLI(context.Background(), tc.deps)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.contains) {
				t.Fatalf("EnsureManagedGitHubCLI() error = %v, want message containing %q", err, tc.contains)
			}
		})
	}

	t.Run("wraps CLI process failures with command context", func(t *testing.T) {
		_, err := runGitHubCLICommand(context.Background(), "/usr/bin/false")
		if err == nil {
			t.Fatal("runGitHubCLICommand() error = nil, want process failure")
		}
		if !strings.Contains(err.Error(), "run GitHub CLI command") {
			t.Fatalf("runGitHubCLICommand() error = %q, want command context", err)
		}
	})

	t.Run("setup attests GitHub CLI on macOS", func(t *testing.T) {
		originalPrereqs := defaultPrereqs
		defaultPrereqs = []prereqDef{{Name: "git", CheckCmd: "git", InstallHint: "Install git"}}
		t.Cleanup(func() { defaultPrereqs = originalPrereqs })

		var commands []string
		err := setupPhase1Prereqs(&bytes.Buffer{}, setupOptions{installDeps: InstallDeps{
			GOOS:     "darwin",
			LookPath: func(string) (string, error) { return "/opt/homebrew/bin/gh", nil },
			Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				commands = append(commands, name+" "+strings.Join(args, " "))
				return []byte("gh version 2.63.0\n"), nil
			},
		}})
		if err != nil {
			t.Fatalf("setupPhase1Prereqs() error = %v", err)
		}
		if got := strings.Join(commands, "; "); got != "/opt/homebrew/bin/gh --version" {
			t.Fatalf("commands = %q, want GitHub CLI attestation", got)
		}
	})

	t.Run("Makefile installs missing gh but uninstall preserves it", func(t *testing.T) {
		tmpDir := t.TempDir()
		binDir := filepath.Join(tmpDir, "bin")
		if err := os.Mkdir(binDir, 0o750); err != nil {
			t.Fatalf("mkdir fake bin: %v", err)
		}
		logPath := filepath.Join(tmpDir, "brew.log")
		brewPath := filepath.Join(binDir, "brew")
		brewScript := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\n"
		if err := os.WriteFile(brewPath, []byte(brewScript), 0o755); err != nil { //nolint:gosec // test helper script
			t.Fatalf("write fake brew: %v", err)
		}
		rmPath := filepath.Join(binDir, "rm")
		if err := os.WriteFile(rmPath, []byte("#!/bin/sh\nexec /bin/rm \"$@\"\n"), 0o755); err != nil { //nolint:gosec // test helper script
			t.Fatalf("write fake rm: %v", err)
		}

		runMake := func(target string) {
			cmd := exec.Command("make", "-f", "Makefile", target, "UNAME_S=Darwin", "ORO_BIN="+filepath.Join(tmpDir, "oro"))
			cmd.Env = append(os.Environ(), "PATH="+binDir)
			cmd.Dir = filepath.Clean("../..")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("make %s: %v\n%s", target, err, out)
			}
		}

		runMake("ensure-github-cli")
		log, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read brew log: %v", err)
		}
		if got := strings.TrimSpace(string(log)); got != "install gh" {
			t.Fatalf("brew invocation = %q, want %q", got, "install gh")
		}

		ghPath := filepath.Join(binDir, "gh")
		if err := os.WriteFile(ghPath, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test helper script
			t.Fatalf("write fake gh: %v", err)
		}
		runMake("ensure-github-cli")
		log, err = os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read brew log after existing gh: %v", err)
		}
		if got := strings.TrimSpace(string(log)); got != "install gh" {
			t.Fatalf("existing gh unexpectedly mutated Homebrew: %q", got)
		}

		runMake("uninstall")
		if _, err := os.Stat(ghPath); err != nil {
			t.Fatalf("make uninstall removed shared gh: %v", err)
		}
	})
}
