package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"oro/pkg/langprofile"

	"github.com/spf13/cobra"
)

// Tool status constants.
const (
	statusOK      = "OK"
	statusMissing = "MISSING"
)

// toolDef describes a tool that oro needs, how to check for it, and how to install it.
type toolDef struct {
	Name        string   // human-readable name (e.g. "gofumpt")
	Category    string   // grouping: prerequisites, go-tools, python-tools, system
	CheckCmd    string   // binary to run for version check
	CheckArgs   []string // args for version check (e.g. ["--version"])
	InstallCmd  string   // binary to run for install (linux default)
	InstallArgs []string // args for install
	BrewName    string   // if set, use "brew install <BrewName>" on macOS
}

// toolResult holds the outcome of checking a single tool.
type toolResult struct {
	Name     string
	Category string
	Status   string // statusOK or statusMissing
	Version  string // version string if found
	Err      error  // underlying error if missing
}

// defaultToolDefs is the canonical list of tools oro needs.
// Tests may override this variable to control what gets checked.
var defaultToolDefs = []toolDef{ //nolint:gochecknoglobals // mutable for test injection
	// Phase 1: Prerequisites
	{Name: "go", Category: "prerequisites", CheckCmd: "go", CheckArgs: []string{"version"}},
	{Name: "python3", Category: "prerequisites", CheckCmd: "python3", CheckArgs: []string{"--version"}},
	{Name: "node", Category: "prerequisites", CheckCmd: "node", CheckArgs: []string{"--version"}},
	{Name: "npm", Category: "prerequisites", CheckCmd: "npm", CheckArgs: []string{"--version"}},
	{Name: "brew", Category: "prerequisites", CheckCmd: "brew", CheckArgs: []string{"--version"}},

	// Phase 2: Go tools
	{Name: "gofumpt", Category: "go-tools", CheckCmd: "gofumpt", CheckArgs: []string{"--version"}, InstallCmd: "go", InstallArgs: []string{"install", "mvdan.cc/gofumpt@latest"}},
	{Name: "goimports", Category: "go-tools", CheckCmd: "goimports", CheckArgs: []string{"--version"}, InstallCmd: "go", InstallArgs: []string{"install", "golang.org/x/tools/cmd/goimports@latest"}},
	{Name: "golangci-lint", Category: "go-tools", CheckCmd: "golangci-lint", CheckArgs: []string{"--version"}, BrewName: "golangci-lint", InstallCmd: "go", InstallArgs: []string{"install", "github.com/golangci/golangci-lint/cmd/golangci-lint@latest"}},
	{Name: "go-arch-lint", Category: "go-tools", CheckCmd: "go-arch-lint", CheckArgs: []string{"version"}, InstallCmd: "go", InstallArgs: []string{"install", "github.com/fe3dback/go-arch-lint/v4@latest"}},
	{Name: "govulncheck", Category: "go-tools", CheckCmd: "govulncheck", CheckArgs: []string{"--version"}, InstallCmd: "go", InstallArgs: []string{"install", "golang.org/x/vuln/cmd/govulncheck@latest"}},

	// Phase 3: Python tools (check only — these run via uvx so just verify uv)
	{Name: "uv", Category: "python-tools", CheckCmd: "uv", CheckArgs: []string{"--version"}, BrewName: "uv", InstallCmd: "curl", InstallArgs: []string{"-LsSf", "https://astral.sh/uv/install.sh"}},
	{Name: "ruff", Category: "python-tools", CheckCmd: "ruff", CheckArgs: []string{"--version"}, InstallCmd: "uv", InstallArgs: []string{"tool", "install", "ruff"}},
	{Name: "pyright", Category: "python-tools", CheckCmd: "pyright", CheckArgs: []string{"--version"}, InstallCmd: "npm", InstallArgs: []string{"install", "-g", "pyright"}},

	// Phase 4: System tools
	{Name: "tmux", Category: "system", CheckCmd: "tmux", CheckArgs: []string{"-V"}, BrewName: "tmux", InstallCmd: "apt-get", InstallArgs: []string{"install", "-y", "tmux"}},
	{Name: "shellcheck", Category: "system", CheckCmd: "shellcheck", CheckArgs: []string{"--version"}, BrewName: "shellcheck", InstallCmd: "apt-get", InstallArgs: []string{"install", "-y", "shellcheck"}},
	{Name: "biome", Category: "system", CheckCmd: "biome", CheckArgs: []string{"--version"}, InstallCmd: "npm", InstallArgs: []string{"install", "-g", "@biomejs/biome"}},
	{Name: "jq", Category: "system", CheckCmd: "jq", CheckArgs: []string{"--version"}, BrewName: "jq", InstallCmd: "apt-get", InstallArgs: []string{"install", "-y", "jq"}},
	{Name: "ast-grep", Category: "system", CheckCmd: "ast-grep", CheckArgs: []string{"--version"}, InstallCmd: "npm", InstallArgs: []string{"install", "-g", "@ast-grep/cli"}},
	{Name: "bd", Category: "system", CheckCmd: "bd", CheckArgs: []string{"--version"}, InstallCmd: "go", InstallArgs: []string{"install", "github.com/aarontravass/bd@latest"}},
}

// checkTool runs the version check for a single tool definition and returns the result.
func checkTool(def toolDef) toolResult {
	r := toolResult{
		Name:     def.Name,
		Category: def.Category,
	}

	path, err := exec.LookPath(def.CheckCmd)
	if err != nil {
		r.Status = statusMissing
		r.Err = err
		return r
	}

	// Run the version command to capture version string.
	cmd := exec.CommandContext(context.Background(), path, def.CheckArgs...) //nolint:gosec // args from trusted toolDef table
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Tool exists but version check failed — still mark OK with note.
		r.Status = statusOK
		r.Version = "(version unknown)"
		return r
	}

	r.Status = statusOK
	r.Version = parseVersion(string(out))
	return r
}

// parseVersion extracts a compact version string from command output.
// It takes the first line and trims whitespace.
func parseVersion(raw string) string {
	lines := strings.SplitN(strings.TrimSpace(raw), "\n", 2)
	if len(lines) == 0 {
		return "(unknown)"
	}
	v := strings.TrimSpace(lines[0])
	// Truncate overly long version strings.
	if len(v) > 60 {
		v = v[:60] + "..."
	}
	return v
}

// checkAllTools checks every tool in the given slice and returns results.
func checkAllTools(defs []toolDef) []toolResult {
	results := make([]toolResult, len(defs))
	for i, def := range defs {
		results[i] = checkTool(def)
	}
	return results
}

// allToolsPresent returns true if every result has statusOK.
func allToolsPresent(results []toolResult) bool {
	for _, r := range results {
		if r.Status != statusOK {
			return false
		}
	}
	return true
}

// countMissing returns the number of results with statusMissing.
func countMissing(results []toolResult) int {
	n := 0
	for _, r := range results {
		if r.Status != statusOK {
			n++
		}
	}
	return n
}

// installCommandForTool returns the command and args to install a tool,
// respecting platform (macOS uses brew when BrewName is set).
func installCommandForTool(def toolDef) (bin string, args []string) {
	if runtime.GOOS == "darwin" && def.BrewName != "" {
		return "brew", []string{"install", def.BrewName}
	}
	return def.InstallCmd, def.InstallArgs
}

// formatInitTable writes a human-readable table of tool check results to w.
func formatInitTable(w io.Writer, results []toolResult) {
	// Header.
	fmt.Fprintf(w, "%-20s %-15s %-10s %s\n", "Tool", "Category", "Status", "Version")
	fmt.Fprintf(w, "%-20s %-15s %-10s %s\n", "----", "--------", "------", "-------")

	for _, r := range results {
		ver := r.Version
		if r.Status == statusMissing {
			ver = "-"
		}
		fmt.Fprintf(w, "%-20s %-15s %-10s %s\n", r.Name, r.Category, r.Status, ver)
	}

	fmt.Fprintln(w)

	missing := countMissing(results)
	total := len(results)
	if missing == 0 {
		fmt.Fprintf(w, "All %d tools available.\n", total)
	} else {
		fmt.Fprintf(w, "%d/%d tools available, %d missing.\n", total-missing, total, missing)
		fmt.Fprintln(w, "Run 'oro init' to install missing tools.")
	}
}

// runInstall attempts to install a single tool and returns an error if it fails.
func runInstall(w io.Writer, def toolDef) error {
	cmd, args := installCommandForTool(def)
	if cmd == "" {
		return fmt.Errorf("no install command defined for %s", def.Name)
	}

	fmt.Fprintf(w, "Installing %s ... ", def.Name)

	c := exec.CommandContext(context.Background(), cmd, args...) //nolint:gosec // args from trusted toolDef table
	out, err := c.CombinedOutput()
	if err != nil {
		fmt.Fprintf(w, "FAILED\n")
		return fmt.Errorf("install %s: %w\n%s", def.Name, err, string(out))
	}

	fmt.Fprintf(w, "done\n")
	return nil
}

// newInitCmd creates the "oro init" subcommand.
func newInitCmd() *cobra.Command {
	var (
		checkOnly   bool
		quiet       bool
		projectRoot string
		force       bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap all oro dependencies and generate config",
		Long: `Checks for and installs all tools required by the oro agent swarm,
then generates .oro/config.yaml based on detected languages.

Phase 1: System prerequisites (Go, Python, Node, npm, Homebrew)
Phase 2: Go tools (gofumpt, goimports, golangci-lint, etc.)
Phase 3: Python tools (uv, ruff, pyright)
Phase 4: System tools (tmux, shellcheck, biome, jq, ast-grep, bd)
Phase 5: Generate .oro/config.yaml with language-specific tool profiles

Use --check to verify without installing (exits non-zero if any tool is missing).
Use --quiet to suppress all output (useful for CI scripts).
Use --project-root to specify a different project directory (default: current directory).
Use --force to overwrite existing .oro/config.yaml.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			return runInit(w, checkOnly, quiet, projectRoot, force)
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "verify tools without installing (exit 1 if any missing)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress output, just exit code")
	cmd.Flags().StringVar(&projectRoot, "project-root", ".", "project root directory for config generation")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing .oro/config.yaml")

	return cmd
}

// runInit is the core logic for the init command, separated for testability.
func runInit(w io.Writer, checkOnly, quiet bool, projectRoot string, force bool) error {
	results := checkAllTools(defaultToolDefs)

	if quiet {
		if allToolsPresent(results) {
			return nil
		}
		return fmt.Errorf("%d tools missing", countMissing(results))
	}

	if checkOnly {
		formatInitTable(w, results)
		if !allToolsPresent(results) {
			return fmt.Errorf("%d tools missing", countMissing(results))
		}
		return nil
	}

	// Full install mode: install missing tools, then re-verify.
	missing := []toolDef{}
	for i, r := range results {
		if r.Status != statusOK {
			missing = append(missing, defaultToolDefs[i])
		}
	}

	// Early return if no missing tools
	if len(missing) == 0 {
		formatInitTable(w, results)
		return generateProjectConfig(w, projectRoot, force)
	}

	// Install missing tools
	fmt.Fprintf(w, "Found %d missing tools. Installing...\n\n", len(missing))

	for _, def := range missing {
		if def.InstallCmd == "" {
			fmt.Fprintf(w, "%-20s no auto-install available (install manually)\n", def.Name)
			continue
		}
		if err := runInstall(w, def); err != nil {
			fmt.Fprintf(w, "  error: %v\n", err)
		}
	}

	// Re-verify after install.
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Verifying...")
	results = checkAllTools(defaultToolDefs)
	formatInitTable(w, results)

	if !allToolsPresent(results) {
		return fmt.Errorf("%d tools still missing after install", countMissing(results))
	}

	// Generate .oro/config.yaml
	return generateProjectConfig(w, projectRoot, force)
}

// generateProjectConfig scans the project root for languages and generates .oro/config.yaml.
func generateProjectConfig(w io.Writer, projectRoot string, force bool) error {
	configPath := filepath.Join(projectRoot, ".oro", "config.yaml")

	// Check if config already exists
	if _, err := os.Stat(configPath); err == nil && !force {
		errMsg := fmt.Sprintf("config already exists at %s (use --force to overwrite)", configPath)
		fmt.Fprintln(w, errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Generating config at %s...\n", configPath)

	// Generate config from detected languages
	profiles := langprofile.AllProfiles()
	cfg, err := langprofile.GenerateConfig(projectRoot, profiles)
	if err != nil {
		return fmt.Errorf("generate config: %w", err)
	}

	// Write config to file
	if err := langprofile.WriteConfig(configPath, cfg); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	if len(cfg.Languages) == 0 {
		fmt.Fprintln(w, "No languages detected. Empty config created.")
	} else {
		fmt.Fprintf(w, "Detected %d language(s): ", len(cfg.Languages))
		langs := []string{}
		for lang := range cfg.Languages {
			langs = append(langs, lang)
		}
		fmt.Fprintln(w, strings.Join(langs, ", "))
	}

	return nil
}
