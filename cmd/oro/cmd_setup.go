package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"oro/pkg/langprofile"

	"github.com/spf13/cobra"
)

// setupOptions holds flags and resolved values for the setup command.
type setupOptions struct {
	projectRoot string
	projectName string
	dev         bool
	dryRun      bool
	skipTools   bool
	force       bool
}

// prereqDef describes a hard prerequisite that must exist before setup proceeds.
type prereqDef struct {
	Name        string
	CheckCmd    string
	InstallHint string
}

// defaultPrereqs are the hard requirements for oro setup.
// These must be present before any other phase runs.
var defaultPrereqs = []prereqDef{ //nolint:gochecknoglobals // static config
	{Name: "claude", CheckCmd: "claude", InstallHint: "Install the Claude CLI: https://docs.anthropic.com/en/docs/claude-cli"},
	{Name: "git", CheckCmd: "git", InstallHint: "Install git: https://git-scm.com/downloads"},
	{Name: "brew", CheckCmd: "brew", InstallHint: "Install Homebrew: /bin/bash -c \"$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""},
}

// newSetupCmd creates the "oro setup" subcommand.
func newSetupCmd() *cobra.Command {
	var opts setupOptions

	cmd := &cobra.Command{
		Use:   "setup [project-name]",
		Short: "Set up a project for oro with prereq checks, language detection, and health verification",
		Long: `The user-friendly project setup command. Unlike 'oro init', setup checks
hard prerequisites first, detects project languages, installs tools,
bootstraps the project, and runs a health check at the end.

Phase 1: Check hard prerequisites (claude, git, brew) — fail fast
Phase 2: Detect project languages via langprofile
Phase 3: Install missing tools (reuses oro init logic)
Phase 4: Bootstrap project (config, assets, hooks)
Phase 5: Doctor check (verify everything is in place)

Use --dry-run to see what would happen without executing.
Use --skip-tools to skip tool installation (Phase 3).
Use --force to overwrite existing config files.
Use --dev to also install dev-only tools (placeholder).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			if len(args) > 0 {
				opts.projectName = args[0]
			}
			return runSetup(w, opts)
		},
	}

	cmd.Flags().StringVar(&opts.projectRoot, "project-root", ".", "project root directory")
	cmd.Flags().BoolVar(&opts.dev, "dev", false, "also install dev-only tools (mutation testing, etc.)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print what would happen without executing")
	cmd.Flags().BoolVar(&opts.skipTools, "skip-tools", false, "skip tool installation (Phase 3)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "overwrite existing config files")

	return cmd
}

// runSetup is the core logic for the setup command, separated for testability.
func runSetup(w io.Writer, opts setupOptions) error {
	if err := setupPhase1Prereqs(w, opts); err != nil {
		return err
	}

	_ = setupPhase2Detect(w, opts)

	if err := setupPhase3Tools(w, opts); err != nil {
		return err
	}

	name, err := setupPhase4Bootstrap(w, opts)
	if err != nil {
		return err
	}

	if opts.dev {
		setupDevTools(w, opts)
	}

	if err := setupPhase5Doctor(w, opts, name); err != nil {
		return err
	}

	fmt.Fprintln(w, "Setup complete.")
	return nil
}

// setupPhase1Prereqs checks hard prerequisites (claude, git, brew).
func setupPhase1Prereqs(w io.Writer, opts setupOptions) error {
	fmt.Fprintln(w, "Phase 1: Checking prerequisites...")
	if opts.dryRun {
		fmt.Fprintln(w, "  [dry-run] Would check for: claude, git, brew")
	} else if err := checkPrereqs(w, defaultPrereqs); err != nil {
		return err
	}
	fmt.Fprintln(w, "  All prerequisites found.")
	fmt.Fprintln(w)
	return nil
}

// setupPhase2Detect detects project languages via langprofile and returns the
// detected config so it can be threaded through to bootstrapProject.
// Returns nil in dry-run mode (no detection performed).
func setupPhase2Detect(w io.Writer, opts setupOptions) *langprofile.Config {
	fmt.Fprintln(w, "Phase 2: Detecting project languages...")
	if opts.dryRun {
		fmt.Fprintf(w, "  [dry-run] Would scan %s for language markers\n", opts.projectRoot)
		fmt.Fprintln(w)
		return nil
	}
	cfg := detectAndPrintLanguages(w, opts.projectRoot)
	fmt.Fprintln(w)
	return cfg
}

// detectAndPrintLanguages runs language detection, prints results, and returns
// the detected config for downstream use.
func detectAndPrintLanguages(w io.Writer, projectRoot string) *langprofile.Config {
	profiles := langprofile.AllProfiles()
	cfg, err := langprofile.GenerateConfig(projectRoot, profiles)
	switch {
	case err != nil:
		fmt.Fprintf(w, "  Warning: language detection failed: %v\n", err)
		return &langprofile.Config{Languages: map[string]langprofile.LanguageConfig{}}
	case len(cfg.Languages) == 0:
		fmt.Fprintln(w, "  No languages detected.")
	default:
		for lang := range cfg.Languages {
			fmt.Fprintf(w, "  Detected: %s\n", lang)
		}
	}
	return cfg
}

// setupPhase3Tools installs missing tools (reuses oro init logic).
func setupPhase3Tools(w io.Writer, opts setupOptions) error {
	switch {
	case opts.skipTools:
		fmt.Fprintln(w, "Phase 3: Tool installation skipped (--skip-tools).")
	case opts.dryRun:
		fmt.Fprintln(w, "Phase 3: Tool installation...")
		fmt.Fprintln(w, "  [dry-run] Would check and install missing tools from defaultToolDefs")
		results := checkAllTools(defaultToolDefs)
		missing := countMissing(results)
		if missing > 0 {
			fmt.Fprintf(w, "  [dry-run] %d tools currently missing — would attempt install\n", missing)
		} else {
			fmt.Fprintf(w, "  [dry-run] All %d tools already present\n", len(results))
		}
	default:
		fmt.Fprintln(w, "Phase 3: Installing tools...")
		results := checkAllTools(defaultToolDefs)
		if err := installMissingTools(w, results); err != nil {
			return fmt.Errorf("tool installation: %w", err)
		}
	}
	fmt.Fprintln(w)
	return nil
}

// setupPhase4Bootstrap creates config, settings, and extracts assets.
func setupPhase4Bootstrap(w io.Writer, opts setupOptions) (string, error) {
	fmt.Fprintln(w, "Phase 4: Bootstrapping project...")
	name, err := resolveProjectName(opts.projectRoot, opts.projectName)
	if err != nil {
		return "", fmt.Errorf("resolve project name: %w", err)
	}

	if opts.dryRun {
		printBootstrapDryRun(w, name, opts)
	} else if err := executeBootstrap(w, name, opts); err != nil {
		return "", err
	}

	fmt.Fprintln(w)
	return name, nil
}

// printBootstrapDryRun prints what bootstrap would do.
func printBootstrapDryRun(w io.Writer, name string, opts setupOptions) {
	fmt.Fprintf(w, "  [dry-run] Would bootstrap project %q at %s\n", name, opts.projectRoot)
	fmt.Fprintf(w, "  [dry-run] Would create .oro/config.yaml, settings.json, extract assets\n")
	if opts.force {
		fmt.Fprintln(w, "  [dry-run] --force: would overwrite existing files")
	}
}

// executeBootstrap runs the actual bootstrap logic.
func executeBootstrap(w io.Writer, name string, opts setupOptions) error {
	oroHome, err := resolveOroHome()
	if err != nil {
		return fmt.Errorf("resolve oro home: %w", err)
	}

	subAssets, err := fs.Sub(EmbeddedAssets, "_assets")
	if err != nil {
		return fmt.Errorf("access embedded assets: %w", err)
	}

	cfg, err := bootstrapProject(opts.projectRoot, name, oroHome, subAssets, opts.force)
	if err != nil {
		return fmt.Errorf("bootstrap project: %w", err)
	}

	if opts.force {
		if err := extractAssets(oroHome, subAssets, true); err != nil {
			return fmt.Errorf("force extract assets: %w", err)
		}
	}

	fmt.Fprintf(w, "  Project %q bootstrapped.\n", name)

	// Use the config threaded back from bootstrapProject (avoids redundant disk read).
	if cfg != nil && len(cfg.Languages) > 0 {
		fmt.Fprintf(w, "  Config: %d language(s) detected.\n", len(cfg.Languages))
	}

	return nil
}

// setupDevTools prints a placeholder message for --dev mode.
func setupDevTools(w io.Writer, opts setupOptions) {
	if opts.dryRun {
		fmt.Fprintln(w, "[dry-run] --dev: Would install dev-only tools (mutation testing, etc.)")
	} else {
		fmt.Fprintln(w, "Note: --dev flag acknowledged. Dev-only tools (mutation testing, etc.) not yet implemented.")
	}
	fmt.Fprintln(w)
}

// setupPhase5Doctor runs a health check to verify setup.
func setupPhase5Doctor(w io.Writer, opts setupOptions, projectName string) error {
	fmt.Fprintln(w, "Phase 5: Running health check...")
	if opts.dryRun {
		fmt.Fprintln(w, "  [dry-run] Would verify: .oro/config.yaml, settings.json, hooks, companion binaries")
	} else {
		oroHome, err := resolveOroHome()
		if err != nil {
			return fmt.Errorf("resolve oro home for doctor: %w", err)
		}
		runDoctor(w, opts.projectRoot, projectName, oroHome)
	}
	fmt.Fprintln(w)
	return nil
}

// checkPrereqs verifies hard prerequisites are present.
// Returns an error with actionable install instructions on first failure.
func checkPrereqs(w io.Writer, prereqs []prereqDef) error {
	for _, p := range prereqs {
		if _, err := exec.LookPath(p.CheckCmd); err != nil {
			return fmt.Errorf("prerequisite %q not found.\n  %s", p.Name, p.InstallHint)
		}
		fmt.Fprintf(w, "  %s: found\n", p.Name)
	}
	return nil
}

// doctorCheck represents a single health check item.
type doctorCheck struct {
	Name   string
	Path   string
	Status string // "OK" or "MISSING"
}

// runDoctor performs post-setup health verification.
// It checks that key files and directories exist after setup.
func runDoctor(w io.Writer, projectRoot, projectName, oroHome string) {
	projectDir := filepath.Join(oroHome, "projects", projectName)

	checks := []doctorCheck{
		{Name: "config anchor", Path: filepath.Join(projectRoot, ".oro", "config.yaml")},
		{Name: "settings.json", Path: filepath.Join(projectDir, "settings.json")},
		{Name: "hooks dir", Path: filepath.Join(oroHome, "hooks")},
		{Name: "skills dir", Path: filepath.Join(oroHome, ".claude", "skills")},
		{Name: "handoffs dir", Path: filepath.Join(projectDir, "handoffs")},
	}

	allOK := true
	for i := range checks {
		if fileOrDirExists(checks[i].Path) {
			checks[i].Status = "OK"
		} else {
			checks[i].Status = "MISSING"
			allOK = false
		}
		fmt.Fprintf(w, "  %-20s %s\n", checks[i].Name+":", checks[i].Status)
	}

	// Check companion binaries (non-fatal).
	if _, err := discoverCompanion("oro-dash"); err == nil {
		fmt.Fprintf(w, "  %-20s %s\n", "oro-dash:", "OK")
	} else {
		fmt.Fprintf(w, "  %-20s %s\n", "oro-dash:", "MISSING (optional)")
	}

	fmt.Fprintln(w)
	if allOK {
		fmt.Fprintln(w, "  Health check passed.")
	} else {
		fmt.Fprintln(w, "  Some items missing. Run 'oro setup' again or check paths.")
	}
}

// fileOrDirExists returns true if a path exists (file or directory).
func fileOrDirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
