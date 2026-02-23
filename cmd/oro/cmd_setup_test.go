package main

import (
	"bytes"
	"os"
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
