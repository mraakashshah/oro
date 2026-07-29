package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"oro/pkg/agentassets"
	"oro/pkg/config"
	"oro/pkg/langprofile"
	"oro/pkg/worker"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// Tool status constants.
const (
	statusOK      = "OK"
	statusMissing = "MISSING"
	statusSkipped = "SKIPPED"
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

// filterToolsByLanguage filters tool definitions based on detected languages.
// Prerequisites and system tools are always included.
// Language-specific tools (go-tools, python-tools) are included only if
// their corresponding language is detected in the config.
// If no languages are detected, only prerequisites and system tools are returned.
func filterToolsByLanguage(defs []toolDef, cfg *langprofile.Config) []toolDef {
	var filtered []toolDef

	for _, def := range defs {
		switch def.Category {
		case "prerequisites", "system":
			// Always include prerequisites and system tools
			filtered = append(filtered, def)
		case "go-tools":
			// Include only if Go is detected
			if _, hasGo := cfg.Languages["go"]; hasGo {
				filtered = append(filtered, def)
			}
		case "python-tools":
			// Include only if Python is detected
			if _, hasPython := cfg.Languages["python"]; hasPython {
				filtered = append(filtered, def)
			}
		default:
			// Unknown category: include by default (fail-open)
			filtered = append(filtered, def)
		}
	}

	return filtered
}

// checkToolsWithLanguageFilter checks tools after filtering by language config.
// Tools not in the filtered set are marked as SKIPPED.
func checkToolsWithLanguageFilter(defs []toolDef, cfg *langprofile.Config) []toolResult {
	filtered := filterToolsByLanguage(defs, cfg)
	filteredMap := make(map[string]bool)
	for _, def := range filtered {
		filteredMap[def.Name] = true
	}

	results := make([]toolResult, 0, len(defs))

	// Add results for filtered tools (actual checks)
	for _, def := range filtered {
		results = append(results, checkTool(def))
	}

	// Add skipped results for tools not in filtered set
	for _, def := range defs {
		if !filteredMap[def.Name] {
			results = append(results, toolResult{
				Name:     def.Name,
				Category: def.Category,
				Status:   statusSkipped,
			})
		}
	}

	return results
}

// countMissingExcludingSkipped returns the count of results with statusMissing,
// excluding tools marked as statusSkipped.
func countMissingExcludingSkipped(results []toolResult) int {
	n := 0
	for _, r := range results {
		if r.Status == statusMissing {
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
		if r.Status == statusMissing || r.Status == statusSkipped {
			ver = "-"
		}
		fmt.Fprintf(w, "%-20s %-15s %-10s %s\n", r.Name, r.Category, r.Status, ver)
	}

	fmt.Fprintln(w)

	missing := countMissingExcludingSkipped(results)
	skipped := 0
	for _, r := range results {
		if r.Status == statusSkipped {
			skipped++
		}
	}
	total := len(results) - skipped
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

// initDeps holds injectable dependencies for the init wizard (TTY detection and prompting).
type initDeps struct {
	isTTY  func() bool
	prompt func(w io.Writer, question, def string) (string, error)
}

// defaultInitDeps returns production initDeps using real stdin/isatty.
func defaultInitDeps() *initDeps {
	return &initDeps{
		isTTY:  func() bool { return isatty.IsTerminal(os.Stdin.Fd()) },
		prompt: interactivePrompt,
	}
}

// interactivePrompt prints question+default to w, reads a line from stdin,
// and returns the trimmed input (or def when the user presses Enter).
func interactivePrompt(w io.Writer, question, def string) (string, error) {
	fmt.Fprintf(w, "%s [%s]: ", question, def)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			return line, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return def, fmt.Errorf("reading input: %w", err)
	}
	return def, nil
}

func defaultProviderMode() config.ProviderMode {
	return config.ProviderModeCodexCodingClaudeReview
}

// resolveInitConfigPath returns the path to config.yaml for the just-bootstrapped project.
func resolveInitConfigPath(stealth bool, projectRoot, oroHome string) (string, error) {
	if stealth {
		hash, err := projectHash(projectRoot)
		if err != nil {
			return "", fmt.Errorf("compute project hash: %w", err)
		}
		return filepath.Join(oroHome, "projects", "s-"+hash, "config.yaml"), nil
	}
	return filepath.Join(projectRoot, ".oro", "config.yaml"), nil
}

// runProviderModeWizard prompts for one of Oro's built-in provider routing presets.
func runProviderModeWizard(w io.Writer, prompt func(io.Writer, string, string) (string, error)) (config.ProviderMode, error) {
	mode, err := prompt(w, "Provider mode (codex-only, claude-only, codex-coding-claude-review, claude-coding-codex-review)", string(defaultProviderMode()))
	if err != nil {
		return "", err
	}
	return config.ProviderMode(mode), nil
}

// writeInitAgentProviderMode writes agent.provider_mode to configPath.
// When TTY and not skipping: runs the interactive wizard.
// When non-TTY and not skipping: writes the default mode and prints a stderr notice.
// When skipping: writes the default mode silently.
func writeInitAgentProviderMode(w, errW io.Writer, configPath string, skipWizard bool, deps *initDeps) error {
	mode := defaultProviderMode()
	isTTY := deps.isTTY()

	if !skipWizard && isTTY {
		var err error
		mode, err = runProviderModeWizard(w, deps.prompt)
		if err != nil {
			return err
		}
	} else if !skipWizard && !isTTY {
		fmt.Fprintf(errW, "oro: non-interactive session — writing default agent provider mode. Run 'oro init' interactively to customize.\n")
	}

	cfg := &config.AgentConfig{ProviderMode: mode}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("validate agent provider mode: %w", err)
	}
	if err := config.MergeKey(configPath, "agent", map[string]any{"provider_mode": mode}); err != nil {
		return fmt.Errorf("write agent provider mode: %w", err)
	}
	return nil
}

// newInitCmd creates the "oro init" subcommand using production dependencies.
func newInitCmd() *cobra.Command {
	return newInitCmdWithDeps(defaultInitDeps())
}

// newInitCmdWithDeps creates the "oro init" subcommand with injectable deps.
func newInitCmdWithDeps(deps *initDeps) *cobra.Command {
	var (
		checkOnly   bool
		quiet       bool
		local       bool
		force       bool
		skipWizard  bool
		projectRoot string
	)

	cmd := &cobra.Command{
		Use:   "init [project-name]",
		Short: "Detect project, generate config, and extract assets",
		Long: `Bootstraps a project for oro. By default uses stealth mode: zero footprint
in the project directory, all config stored under ~/.oro/projects/s-<hash>/.

Use --local to create .oro/config.yaml in the project root (visible to
collaborators, committable).

Use 'oro setup' to install missing tools (interactive, installs via brew/go/npm).

The project name is taken from the first argument, or defaults to the
directory name of --project-root.

Flags:
  --check         Verify tools without bootstrapping (exits non-zero if any missing).
  --quiet         Suppress all output (useful for CI scripts).
  --local         In-repo mode: create .oro/ directory in the project root.
                  By default oro uses stealth mode (zero footprint).
  --force         Overwrite regenerated Oro assets and quality gate files.
  --project-root  Specify a different project directory (default: current directory).
  --skip-wizard   Skip interactive setup wizard and write the default agent provider mode silently.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()
			projectName := ""
			if len(args) > 0 {
				projectName = args[0]
			}
			stealth := !local
			return runInitWithDeps(w, errW, checkOnly, quiet, stealth, skipWizard, force, projectRoot, projectName, deps)
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "verify tools without installing (exit 1 if any missing)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress output, just exit code")
	cmd.Flags().BoolVar(&local, "local", false, "in-repo mode: create .oro/ in project root (default: stealth)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite regenerated Oro assets and quality gate files")
	cmd.Flags().StringVar(&projectRoot, "project-root", ".", "project root directory for config generation")
	cmd.Flags().BoolVar(&skipWizard, "skip-wizard", false, "skip interactive setup wizard (write default agent provider mode silently)")

	return cmd
}

// runInit is the backward-compatible entry point used by cmd_start auto-init.
// It always skips the wizard (non-interactive context).
func runInit(w io.Writer, checkOnly, quiet, stealth bool, projectRoot, projectName string) error {
	return runInitWithDeps(w, os.Stderr, checkOnly, quiet, stealth, true, false, projectRoot, projectName, defaultInitDeps())
}

// runInitWithDeps is the core init logic with injectable dependencies.
func runInitWithDeps(w, errW io.Writer, checkOnly, quiet, stealth, skipWizard, force bool, projectRoot, projectName string, deps *initDeps) error {
	// Resolve real repo root for worktree support (e.g. when running from inside .worktrees/).
	if resolved, err := langprofile.ResolveProjectRoot(projectRoot); err == nil {
		projectRoot = resolved
	}

	// Report tool status but never install. Use oro setup for that.
	results := checkAllTools(defaultToolDefs)
	if quiet {
		if !allToolsPresent(results) {
			return fmt.Errorf("%d tools missing — run 'oro setup' to install", countMissing(results))
		}
		return nil
	}
	if checkOnly {
		formatInitTable(w, results)
		if !allToolsPresent(results) {
			return fmt.Errorf("%d tools missing — run 'oro setup' to install", countMissing(results))
		}
		return nil
	}
	// Report missing tools but continue with bootstrap.
	missing := countMissing(results)
	if missing > 0 {
		formatInitTable(w, results)
		fmt.Fprintf(w, "\n%d tools missing. Run 'oro setup' to install them.\nContinuing with project bootstrap...\n\n", missing)
	}

	oroHome, err := resolveOroHome()
	if err != nil {
		return err
	}

	subAssets, err := fs.Sub(EmbeddedAssets, "_assets")
	if err != nil {
		return fmt.Errorf("access embedded assets: %w", err)
	}

	if err := runBootstrapAndPrint(w, stealth, force, projectRoot, projectName, oroHome, subAssets); err != nil {
		return err
	}

	// Write agent provider mode to config (wizard, defaults, or skip).
	cfgPath, err := resolveInitConfigPath(stealth, projectRoot, oroHome)
	if err != nil {
		fmt.Fprintf(errW, "warning: could not resolve config path for agent provider mode: %v\n", err)
		return nil
	}
	if err := writeInitAgentProviderMode(w, errW, cfgPath, skipWizard, deps); err != nil {
		fmt.Fprintf(errW, "warning: agent provider mode config: %v\n", err)
	}
	return nil
}

// runBootstrapAndPrint runs the appropriate bootstrap path (stealth or local)
// and prints the success message to w.
func runBootstrapAndPrint(w io.Writer, stealth, force bool, projectRoot, projectName, oroHome string, assets fs.FS) error {
	if stealth {
		return runInitStealth(w, force, projectRoot, oroHome, assets)
	}
	name, err := resolveProjectName(projectRoot, projectName)
	if err != nil {
		return err
	}
	if _, err := bootstrapProject(projectRoot, name, oroHome, assets, force); err != nil {
		return fmt.Errorf("bootstrap project: %w", err)
	}
	fmt.Fprintf(w, "\n✓ Initialized project %q\n", name)
	fmt.Fprintf(w, "  Local anchor: %s/.oro/config.yaml\n", projectRoot)
	fmt.Fprintf(w, "  Project dir:  %s/projects/%s/\n", oroHome, name)
	fmt.Fprintf(w, "  Settings:     %s/projects/%s/settings.json\n", oroHome, name)
	fmt.Fprintf(w, "\nRun 'oro start' to launch agents.\n")
	return nil
}

// runInitStealth handles the stealth branch of runInit: bootstraps a stealth
// project and prints the success message.
func runInitStealth(w io.Writer, force bool, projectRoot, oroHome string, assets fs.FS) error {
	if err := bootstrapStealthProject(projectRoot, oroHome, assets, force); err != nil {
		return fmt.Errorf("bootstrap stealth project: %w", err)
	}
	hash, err := projectHash(projectRoot)
	if err != nil {
		return err
	}
	stealthDirName := "s-" + hash
	fmt.Fprintf(w, "\n✓ Initialized stealth project %q\n", stealthDirName)
	fmt.Fprintf(w, "  Stealth dir: %s/projects/%s/\n", oroHome, stealthDirName)
	fmt.Fprintf(w, "  Settings:    %s/projects/%s/settings.json\n", oroHome, stealthDirName)
	fmt.Fprintf(w, "\nRun 'oro start' to launch agents.\n")
	return nil
}

// installAgentBranchGuard installs a pre-push hook that blocks agent/* and epic/*
// branch pushes. Used by all oro projects (not just stealth). Fail-open.
func installAgentBranchGuard(absProjectRoot string) {
	gitDir := filepath.Join(absProjectRoot, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return
	}
	qgPath := filepath.Join(absProjectRoot, "scripts", "quality_gate.sh")
	if err := installHookWrapper(gitDir, "pre-push", buildOroPrePushCheck(qgPath)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: install pre-push hook: %v\n", err)
	}
}

// installStealthGitHooks installs oro pre-commit and pre-push wrappers in the
// project's .git/hooks directory. Errors are logged as warnings; the function
// is fail-open because missing hooks are recoverable.
func installStealthGitHooks(absProjectRoot, qualityGatePath string) {
	gitDir := filepath.Join(absProjectRoot, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return
	}
	if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
		fmt.Fprintf(os.Stderr, "warning: install pre-commit hook: %v\n", err)
	}
	if err := installHookWrapper(gitDir, "pre-push", buildOroPrePushCheck(qualityGatePath)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: install pre-push hook: %v\n", err)
	}
}

// bootstrapStealthProject creates a zero-footprint oro project.
// Rather than writing .oro/config.yaml into the project root, it creates
// <oroHome>/projects/s-<hash>/config.yaml with mode: stealth.
// Git pre-commit and pre-push hooks are installed to prevent accidental leakage.
func bootstrapStealthProject(projectRoot, oroHome string, assets fs.FS, force bool) error { //nolint:funlen // sequential bootstrap steps, mirrors bootstrapProject
	hash, err := projectHash(projectRoot)
	if err != nil {
		return fmt.Errorf("compute project hash: %w", err)
	}
	stealthDirName := "s-" + hash
	stealthDir := filepath.Join(oroHome, "projects", stealthDirName)

	// 1. Create stealth directory structure.
	handoffsDir := filepath.Join(stealthDir, "handoffs")
	if err := os.MkdirAll(handoffsDir, 0o755); err != nil { //nolint:gosec // needs to be readable
		return fmt.Errorf("create stealth dir: %w", err)
	}

	// 2. Detect languages.
	profiles := langprofile.AllProfiles()
	cfg, detectErr := langprofile.GenerateConfig(projectRoot, profiles)
	if detectErr != nil {
		cfg = &langprofile.Config{Languages: map[string]langprofile.LanguageConfig{}}
	}

	// 3. Write config.yaml with mode: stealth.
	var cfgBuf strings.Builder
	fmt.Fprintf(&cfgBuf, "mode: stealth\n")
	fmt.Fprintf(&cfgBuf, "project: %s\n", stealthDirName)
	if langYAML := langprofile.BuildYAML(cfg); langYAML != "" {
		cfgBuf.WriteString(langYAML)
	}
	if err := os.WriteFile(filepath.Join(stealthDir, "config.yaml"), []byte(cfgBuf.String()), 0o600); err != nil { //nolint:gosec // config file
		return fmt.Errorf("write stealth config.yaml: %w", err)
	}

	// 4. Write project.root.
	absProjectRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve absolute project root: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stealthDir, "project.root"), []byte(absProjectRoot), 0o644); err != nil { //nolint:gosec // readable file
		return fmt.Errorf("write project.root: %w", err)
	}

	// 5. Ensure git repo (fail-open).
	ensureGitRepo(projectRoot)

	// 6. Create task data directory directly (no symlink — zero footprint in project).
	tasksDir := filepath.Join(stealthDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil { //nolint:gosec // needs to be readable
		return fmt.Errorf("create stealth task data dir: %w", err)
	}

	// 7. Install git hooks to prevent accidental leakage in stealth mode.
	stealthPaths := stealthProjectPaths(projectRoot, stealthDir)
	installStealthGitHooks(absProjectRoot, stealthPaths.QualityGate)

	// 8. Generate settings.json.
	settingsData, err := generateSettings("$HOME/.oro")
	if err != nil {
		return fmt.Errorf("generate settings: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stealthDir, "settings.json"), settingsData, 0o644); err != nil { //nolint:gosec // readable file
		return fmt.Errorf("write stealth settings: %w", err)
	}

	// 9. Extract embedded assets to oroHome (additive unless --force is set).
	if err := extractAssets(oroHome, assets, force); err != nil {
		return fmt.Errorf("extract assets: %w", err)
	}

	// 10. Generate quality_gate.sh to the stealth path (not in project root).
	if err := writeQualityGateScriptFile(stealthPaths, force); err != nil {
		fmt.Fprintf(os.Stderr, "warning: quality gate generation failed: %v\n", err)
	}

	// 11. Build oro-search-hook binary (fail-open).
	_ = ensureSearchHook(
		os.Stderr,
		filepath.Join(oroHome, "hooks", "oro-search-hook"),
		filepath.Join(absProjectRoot, "cmd", "oro-search-hook"),
	)
	publishOracleSettings(stealthDir, oroHome)

	return nil
}

// installMissingTools installs any missing tools and re-verifies.
func installMissingTools(w io.Writer, results []toolResult) error {
	missing := []toolDef{}
	for i, r := range results {
		if r.Status != statusOK {
			missing = append(missing, defaultToolDefs[i])
		}
	}

	if len(missing) == 0 {
		formatInitTable(w, results)
		return nil
	}

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

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Verifying...")
	results = checkAllTools(defaultToolDefs)
	formatInitTable(w, results)

	if !allToolsPresent(results) {
		return fmt.Errorf("%d tools still missing after install", countMissing(results))
	}
	return nil
}

// ensureGitRepo runs "git init" in projectRoot if it's not already a git repo.
// Fail-open: logs a warning on error but never blocks init.
func ensureGitRepo(projectRoot string) {
	if _, err := os.Stat(filepath.Join(projectRoot, ".git")); err == nil {
		return // already a git repo
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: git init failed: %v\n%s\n", err, out)
	}
}

// resolveProjectName returns the project name from the argument or derives it from the directory.
func resolveProjectName(projectRoot, projectName string) (string, error) {
	if projectName != "" {
		return projectName, nil
	}
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	return filepath.Base(absRoot), nil
}

// bootstrapProject orchestrates project initialization for externalized config.
// It creates the local anchor (.oro/config.yaml), sets up global gitignore, generates
// per-project settings.json, creates handoffs dir, and extracts embedded assets.
// Returns the detected language config (threaded from createProjectAnchor) so
// callers avoid a redundant disk read.
func bootstrapProject(projectRoot, projectName, oroHome string, assets fs.FS, force bool) (*langprofile.Config, error) { //nolint:funlen // sequential bootstrap steps
	// 1. Create local anchor: .oro/config.yaml with project name.
	// Thread the detected config back to the caller.
	cfg, err := createProjectAnchor(projectRoot, projectName)
	if err != nil {
		return nil, fmt.Errorf("create project anchor: %w", err)
	}

	// 2. Add .oro/ to global gitignore (not per-repo).
	if err := ensureGlobalGitignore(); err != nil {
		// Fail-open: warn but don't block init.
		fmt.Fprintf(os.Stderr, "warning: could not update global gitignore: %v\n", err)
	}

	// 3. Create per-project directory structure under oroHome.
	projectDir := filepath.Join(oroHome, "projects", projectName)
	handoffsDir := filepath.Join(projectDir, "handoffs")
	if err := os.MkdirAll(handoffsDir, 0o755); err != nil { //nolint:gosec // project dir needs to be readable
		return nil, fmt.Errorf("create project dir: %w", err)
	}

	// 3a. Write project.root with the absolute path of the project root.
	absProjectRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute project root: %w", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "project.root"), []byte(absProjectRoot), 0o644); err != nil { //nolint:gosec // readable file
		return nil, fmt.Errorf("write project.root: %w", err)
	}

	// 3b. Initialize git repo if not already present.
	ensureGitRepo(projectRoot)

	// 5. Generate settings.json (always overwrite — idempotent).
	settingsData, err := generateSettings("$HOME/.oro")
	if err != nil {
		return nil, fmt.Errorf("generate settings: %w", err)
	}
	settingsPath := filepath.Join(projectDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsData, 0o644); err != nil { //nolint:gosec // settings file needs to be readable
		return nil, fmt.Errorf("write settings: %w", err)
	}

	// 6. Extract embedded assets to oroHome (additive unless --force is set).
	if err := extractAssets(oroHome, assets, force); err != nil {
		return nil, fmt.Errorf("extract assets: %w", err)
	}

	// 7. Generate quality_gate.sh in project root (skip if exists, unless force).
	if err := writeQualityGateScriptFile(standardProjectPaths(projectRoot), force); err != nil {
		// Fail-open: warn but continue. Quality gate is helpful but not critical.
		fmt.Fprintf(os.Stderr, "warning: quality gate generation failed: %v\n", err)
	}

	// 7b. Install pre-push hook to block agent/* and epic/* branch pushes (fail-open).
	installAgentBranchGuard(absProjectRoot)

	// 8. Build oro-search-hook binary. Fail-open: ensureSearchHook logs a
	// warning and returns nil when srcDir is missing (go-install users lack
	// the source tree). oro init always runs from the repo root so the
	// source is normally available.
	_ = ensureSearchHook(
		os.Stderr,
		filepath.Join(oroHome, "hooks", "oro-search-hook"),
		filepath.Join(absProjectRoot, "cmd", "oro-search-hook"),
	)
	publishOracleSettings(projectDir, oroHome)

	return cfg, nil
}

// createProjectAnchor creates the .oro/config.yaml anchor in the project root.
// It preserves existing project configuration and merges the project name;
// newly created anchors include detected language profiles.
// Returns the detected language config so callers can use it without re-reading from disk.
func createProjectAnchor(projectRoot, projectName string) (*langprofile.Config, error) {
	oroDir := filepath.Join(projectRoot, ".oro")
	if err := os.MkdirAll(oroDir, 0o755); err != nil { //nolint:gosec // config dir needs to be readable
		return nil, fmt.Errorf("create .oro dir: %w", err)
	}
	configPath := filepath.Join(oroDir, "config.yaml")
	if cfg, preserved, err := preserveProjectAnchor(projectRoot, projectName, configPath); err != nil {
		return nil, err
	} else if preserved {
		return cfg, nil
	}

	// Detect languages and build initial config content.
	var buf strings.Builder
	profiles := langprofile.AllProfiles()
	cfg, err := langprofile.GenerateConfig(projectRoot, profiles)
	if err == nil {
		langYAML := langprofile.BuildYAML(cfg)
		if langYAML != "" {
			buf.WriteString(langYAML)
		}
	} else {
		cfg = &langprofile.Config{Languages: map[string]langprofile.LanguageConfig{}}
	}

	if err := os.WriteFile(configPath, []byte(buf.String()), 0o644); err != nil { //nolint:gosec // config file needs to be readable
		return nil, fmt.Errorf("write config.yaml: %w", err)
	}
	if err := config.MergeKey(configPath, "project", projectName); err != nil {
		return nil, fmt.Errorf("write project to config.yaml: %w", err)
	}
	return cfg, nil
}

func preserveProjectAnchor(projectRoot, projectName, configPath string) (*langprofile.Config, bool, error) {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("inspect config.yaml: %w", err)
	}
	if err := config.MergeKey(configPath, "project", projectName); err != nil {
		return nil, false, fmt.Errorf("write project to config.yaml: %w", err)
	}
	cfg, err := langprofile.GenerateConfig(projectRoot, langprofile.AllProfiles())
	if err != nil {
		cfg = &langprofile.Config{Languages: map[string]langprofile.LanguageConfig{}}
	}
	return cfg, true, nil
}

// oroGitignoreEntries returns the patterns oro needs ignored globally
// so that oro artifacts never pollute target repos.
func oroGitignoreEntries() []string {
	return []string{".oro/"}
}

func oroLegacyGitignoreEntries() []string {
	return []string{".beads/", ".beads", ".dolt/"}
}

// ensureGlobalGitignore adds oro-related entries to the user's global
// gitignore file so target repos are never polluted. It resolves the
// path from git config core.excludesFile, falling back to ~/.gitignore_global.
func ensureGlobalGitignore() error {
	path, err := resolveGlobalGitignorePath()
	if err != nil {
		return err
	}
	return ensureGlobalGitignoreAt(path)
}

// resolveGlobalGitignorePath returns the path to the user's global gitignore,
// creating one and configuring git if none is set.
func resolveGlobalGitignorePath() (string, error) {
	if p, err := readGitExcludesFile(); err == nil && p != "" {
		return p, nil
	}

	// No global excludes file configured — set one up.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	p := filepath.Join(home, ".gitignore_global")
	//nolint:gosec // p is from os.UserHomeDir, not user input
	if err := exec.CommandContext(context.Background(), "git", "config", "--global", "core.excludesFile", p).Run(); err != nil {
		return "", fmt.Errorf("set core.excludesFile: %w", err)
	}
	return p, nil
}

// readGitExcludesFile reads core.excludesFile from git config and expands ~.
func readGitExcludesFile() (string, error) {
	out, err := exec.CommandContext(context.Background(), "git", "config", "--global", "core.excludesFile").Output()
	if err != nil {
		return "", fmt.Errorf("read core.excludesFile: %w", err)
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand home dir: %w", err)
		}
		p = filepath.Join(home, p[2:])
	}
	return p, nil
}

// ensureGlobalGitignoreAt adds oro entries to the given gitignore path.
// Testable core — ensureGlobalGitignore wraps this with path resolution.
func ensureGlobalGitignoreAt(path string) error {
	existing, err := os.ReadFile(path) //nolint:gosec // user-controlled path
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read global gitignore: %w", err)
	}

	// Build set of existing entries for quick lookup.
	existingLines := make(map[string]bool)
	for _, line := range strings.Split(string(existing), "\n") {
		existingLines[strings.TrimSpace(line)] = true
	}

	// Collect entries that need to be added.
	var missing []string
	for _, entry := range oroGitignoreEntries() {
		if !existingLines[entry] {
			missing = append(missing, entry)
		}
	}
	if len(missing) == 0 {
		return nil // all entries present
	}

	// Append missing entries with a section header.
	var buf strings.Builder
	if len(existing) > 0 {
		buf.Write(existing)
		if !strings.HasSuffix(string(existing), "\n") {
			buf.WriteByte('\n')
		}
	}
	buf.WriteString("\n# Oro (managed by oro init)\n")
	for _, entry := range missing {
		buf.WriteString(entry)
		buf.WriteByte('\n')
	}

	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil { //nolint:gosec // gitignore needs to be readable
		return fmt.Errorf("write global gitignore: %w", err)
	}
	return nil
}

// hookEntry is a single hook command in settings.json.
type hookEntry struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`
}

// hookGroup is a matcher + list of hooks for one lifecycle phase.
type hookGroup struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// buildHookConfig returns the full hooks map for settings.json.
// hooksDir is the absolute path prefix for hook scripts (e.g. "$HOME/.oro/hooks").
func buildHookConfig(hooksDir string) map[string][]hookGroup {
	return buildHookConfigWithCapture(hooksDir, false)
}

func buildHookConfigWithCapture(hooksDir string, continuousCapture bool) map[string][]hookGroup {
	py := func(s string) string { return "python3 " + hooksDir + "/" + s }
	sh := func(s string) string { return hooksDir + "/" + s }

	cfg := map[string][]hookGroup{
		"SessionStart": {
			{Matcher: "", Hooks: []hookEntry{
				{Type: "command", Command: py("session_start_extras.py"), StatusMessage: "Loading project context..."},
			}},
			{Matcher: "compact", Hooks: []hookEntry{
				{Type: "command", Command: py("session_start_compact.py")},
			}},
		},
		"PreCompact": {{
			Matcher: "",
			Hooks: []hookEntry{
				{Type: "command", Command: py("pre_compact.py")},
			},
		}},
		"PreToolUse": {
			{Matcher: "", Hooks: []hookEntry{
				{Type: "command", Command: py("pane_handoff_reminder.py")},
			}},
			{Matcher: "Bash", Hooks: []hookEntry{
				{Type: "command", Command: py("worktree_guard.py")},
				{Type: "command", Command: py("no_cd_guard.py")},
				{Type: "command", Command: py("rebase_worktree_guard.py")},
				{Type: "command", Command: py("destructive_command_guard.py")},
			}},
			{Matcher: "Read", Hooks: []hookEntry{
				{Type: "command", Command: sh("oro-search-hook"), Timeout: 5000, StatusMessage: "Searching codebase..."},
			}},
			{Matcher: "Task", Hooks: []hookEntry{
				{Type: "command", Command: py("enforce_worktree.py")},
			}},
			{Matcher: "Write|Edit|NotebookEdit", Hooks: []hookEntry{
				{Type: "command", Command: py("enforce_worktree_writes.py")},
			}},
		},
		"PostToolUse": postToolUseHookGroups(py, sh),
		"Stop": {{Matcher: "", Hooks: []hookEntry{
			{Type: "command", Command: py("context_block_stop.py")},
			{Type: "command", Command: sh("stop-checklist.sh")},
		}}},
	}
	if continuousCapture {
		cfg["PostToolUse"] = append([]hookGroup{{
			Matcher: "",
			Hooks: []hookEntry{
				{Type: "command", Command: sh("oro-capture-hook")},
			},
		}}, cfg["PostToolUse"]...)
	}
	return cfg
}

func postToolUseHookGroups(py, sh func(string) string) []hookGroup {
	return []hookGroup{
		{Matcher: "", Hooks: []hookEntry{
			{Type: "command", Command: py("context_pct_writer.py")},
			{Type: "command", Command: py("compact_trigger.py")},
			{Type: "command", Command: py("context_pruner.py")},
		}},
		{Matcher: "Read|WebFetch|Bash", Hooks: []hookEntry{
			{Type: "command", Command: py("prompt_injection_guard.py")},
		}},
		{Matcher: "Edit|Write", Hooks: []hookEntry{
			{Type: "command", Command: sh("auto-format.sh")},
		}},
		{Matcher: "Task", Hooks: []hookEntry{
			{Type: "command", Command: py("validate_agent_completion.py")},
		}},
	}
}

// generateSettings produces a settings.json with hook commands using absolute
// paths under oroHome. Shell variable $HOME is used for portability.
// Permissions include Context7 MCP tools so workers can look up library/API
// documentation during implementation (same capability as interactive sessions).
func generateSettings(oroHome string) ([]byte, error) { //nolint:unparam // callers always pass "$HOME/.oro" but the parameter is intentional for future flexibility
	settings := struct {
		Permissions map[string][]string    `json:"permissions"`
		Hooks       map[string][]hookGroup `json:"hooks"`
	}{
		Permissions: map[string][]string{
			"allow": {
				"mcp__context7__resolve-library-id",
				"mcp__context7__query-docs",
			},
		},
		Hooks: buildHookConfig(oroHome + "/hooks"),
	}

	data, err := json.MarshalIndent(settings, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	return data, nil
}

// generateOracleSettings produces the isolated settings profile used by
// read-only Oracle launches. It intentionally contains no general permissions
// or project hooks: only the managed hook's activation probe and Read guard.
func generateOracleSettings(hookPath string) ([]byte, error) {
	settings := struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}{
		Hooks: map[string][]hookGroup{
			"SessionStart": {{Hooks: []hookEntry{{Type: "command", Command: hookPath}}}},
			"PreToolUse":   {{Matcher: "Read", Hooks: []hookEntry{{Type: "command", Command: hookPath}}}},
		},
	}

	data, err := json.MarshalIndent(settings, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("marshal Oracle settings: %w", err)
	}
	return data, nil
}

// writeOracleSettings validates the managed executable and atomically replaces
// the isolated Oracle profile without touching the regular project settings.
func writeOracleSettings(projectDir, hookPath string) error {
	canonicalHookPath, err := worker.ValidateManagedOracleHook(hookPath)
	if err != nil {
		return fmt.Errorf("validate managed Oracle hook: %w", err)
	}
	data, err := generateOracleSettings(canonicalHookPath)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(projectDir, ".oracle-settings-*")
	if err != nil {
		return fmt.Errorf("create Oracle settings temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck // best-effort cleanup after an interrupted write
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write Oracle settings temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set Oracle settings permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close Oracle settings temp file: %w", err)
	}
	// #nosec G703 -- projectDir is the resolved Oro home projects directory
	// (filepath.Join(oroHome, "projects", projectName)) and the basename is a
	// constant, so neither component is caller-tainted.
	if err := os.Rename(tmpPath, filepath.Join(projectDir, "oracle-settings.json")); err != nil {
		return fmt.Errorf("install Oracle settings: %w", err)
	}
	return nil
}

func publishOracleSettings(projectDir, oroHome string) {
	hookPath := filepath.Join(oroHome, "hooks", "oro-search-hook")
	if err := writeOracleSettings(projectDir, hookPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Oracle settings not published: %v\n", err)
	}
}

// assetMapping maps source directory names in the embedded FS to their
// destination paths relative to oroHome.
var assetMapping = map[string]string{ //nolint:gochecknoglobals // static config
	"skills":   filepath.Join(".claude", "skills"),
	"hooks":    "hooks",
	"beacons":  "beacons",
	"commands": filepath.Join(".claude", "commands"),
}

const sharedAgentInstructionsFile = "ORO_AGENT.md"

// fileExists returns true if a file exists at path (not a directory).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// filePermForAsset returns the appropriate file permission mode for an asset file.
// Shell (.sh) and Python (.py) scripts get executable permissions (0o755),
// all other files get standard read/write permissions (0o644).
func filePermForAsset(path string) os.FileMode {
	if strings.HasSuffix(path, ".sh") || strings.HasSuffix(path, ".py") {
		return 0o755
	}
	return 0o644
}

// writeFileWithPolicy implements the content-aware regeneration policy for
// agent instruction views derived from ORO_AGENT.md.
//   - New file: always written.
//   - Exists + matches: silently skipped.
//   - Exists + diverged + force=false: warning emitted to w, file NOT overwritten.
//   - Exists + diverged + force=true: file overwritten silently.
func writeFileWithPolicy(w io.Writer, destPath string, data []byte, force bool) error {
	if fileExists(destPath) {
		existing, err := os.ReadFile(destPath) //nolint:gosec // path from trusted caller
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(destPath), err)
		}
		if bytes.Equal(existing, data) {
			return nil // content matches — silent skip
		}
		if !force {
			fmt.Fprintf(w, "[oro] warning: %s has local edits and differs from ORO_AGENT.md — skipping (use --force to overwrite)\n", filepath.Base(destPath))
			return nil
		}
	}
	if err := os.WriteFile(destPath, data, 0o644); err != nil { //nolint:gosec // needs to be readable
		return fmt.Errorf("write %s: %w", filepath.Base(destPath), err)
	}
	return nil
}

// extractSharedAgentInstructionsW extracts the shared instruction source to
// dest/ORO_AGENT.md using the content-aware regeneration policy.
func extractSharedAgentInstructionsW(dest string, assets fs.FS, force bool, w io.Writer) error {
	data, err := fs.ReadFile(assets, sharedAgentInstructionsFile)
	if err != nil {
		return nil //nolint:nilerr // shared instructions are optional during migration
	}
	destPath := filepath.Join(dest, sharedAgentInstructionsFile)
	return writeFileWithPolicy(w, destPath, data, force)
}

// extractClaudeMDW extracts the Claude compatibility view to dest/.claude/CLAUDE.md.
// ORO_AGENT.md is the canonical source; falls back to a direct CLAUDE.md asset for
// older bundles. Uses the content-aware regeneration policy.
func extractClaudeMDW(dest string, assets fs.FS, force bool, w io.Writer) error {
	sharedData, err := fs.ReadFile(assets, sharedAgentInstructionsFile)
	if err == nil {
		data := generateClaudeWrapper(sharedData)
		claudeDir := filepath.Join(dest, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil { //nolint:gosec // needs to be readable
			return fmt.Errorf("create .claude dir: %w", err)
		}
		claudePath := filepath.Join(claudeDir, "CLAUDE.md")
		return writeFileWithPolicy(w, claudePath, data, force)
	}

	data, err := fs.ReadFile(assets, "CLAUDE.md")
	if err != nil {
		return nil //nolint:nilerr // CLAUDE.md is optional in assets
	}
	claudeDir := filepath.Join(dest, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil { //nolint:gosec // needs to be readable
		return fmt.Errorf("create .claude dir: %w", err)
	}
	claudePath := filepath.Join(claudeDir, "CLAUDE.md")
	return writeFileWithPolicy(w, claudePath, data, force)
}

func generateClaudeWrapper(sharedData []byte) []byte {
	title := firstMarkdownHeading(sharedData)
	if title == "" {
		title = "# Oro Project Instructions"
	}

	return fmt.Appendf(nil, "%s\n\nThis Claude compatibility file is generated from `../%s`.\nRead `../%s` for the canonical Oro agent instructions.\n",
		title,
		sharedAgentInstructionsFile,
		sharedAgentInstructionsFile,
	)
}

func firstMarkdownHeading(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

// extractAgentsMDW extracts the AGENTS.md compatibility view to dest/AGENTS.md.
// ORO_AGENT.md is the single source. Uses the content-aware regeneration policy.
func extractAgentsMDW(dest string, assets fs.FS, force bool, w io.Writer) error {
	data, err := fs.ReadFile(assets, sharedAgentInstructionsFile)
	if err != nil {
		return nil //nolint:nilerr // ORO_AGENT.md is optional during migration
	}
	agentsPath := filepath.Join(dest, "AGENTS.md")
	return writeFileWithPolicy(w, agentsPath, data, force)
}

// extractAssetDir walks a single mapped directory from srcFS and copies files
// to destBase/destDir. Skips existing files when force is false.
func extractAssetDir(srcFS fs.FS, srcDir, destBase, destDir string, force bool) error {
	return fs.WalkDir(srcFS, ".", func(path string, d fs.DirEntry, err error) error { //nolint:wrapcheck // caller wraps with srcDir context
		if err != nil {
			return err
		}

		destPath := filepath.Join(destBase, destDir, path)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0o755) //nolint:gosec // needs to be readable
		}

		if !force && fileExists(destPath) {
			return nil
		}

		data, err := fs.ReadFile(srcFS, path)
		if err != nil {
			return fmt.Errorf("read %s/%s: %w", srcDir, path, err)
		}

		return os.WriteFile(destPath, data, filePermForAsset(path)) //nolint:gosec // needs to be readable
	})
}

// extractThresholdsJSON extracts thresholds.json from assets to dest/thresholds.json.
// Skips writing if force is false and the file already exists.
// Returns nil if thresholds.json is absent from the embedded FS (optional asset).
func extractThresholdsJSON(dest string, assets fs.FS, force bool) error {
	data, err := fs.ReadFile(assets, "thresholds.json")
	if err != nil {
		return nil //nolint:nilerr // thresholds.json is optional in assets
	}
	destPath := filepath.Join(dest, "thresholds.json")
	if !force && fileExists(destPath) {
		return nil
	}
	if err := os.WriteFile(destPath, data, 0o644); err != nil { //nolint:gosec // needs to be readable
		return fmt.Errorf("write thresholds.json: %w", err)
	}
	return nil
}

func extractClaudeRuleAssets(dest string, assets fs.FS) error {
	rules, err := agentassets.ClaudeRuleAssets(assets)
	if err != nil {
		return fmt.Errorf("generate claude rule assets: %w", err)
	}
	if err := agentassets.InstallClaudeRules(context.Background(), dest, rules); err != nil {
		return fmt.Errorf("install claude rule assets: %w", err)
	}
	return nil
}

// extractAssets walks the embedded FS and copies files to oroHome.
// Directory mapping: skills → .claude/skills/, hooks → hooks/, beacons → beacons/,
// commands → .claude/commands/, ORO_AGENT.md → ORO_AGENT.md,
// CLAUDE.md → .claude/CLAUDE.md, AGENTS.md → AGENTS.md.
// Divergence warnings for agent instruction views are written to os.Stderr.
func extractAssets(dest string, assets fs.FS, force bool) error {
	return extractAssetsW(dest, assets, force, os.Stderr)
}

// extractAssetsW is extractAssets with an explicit writer for divergence warnings
// on agent instruction views (ORO_AGENT.md, CLAUDE.md, AGENTS.md).
func extractAssetsW(dest string, assets fs.FS, force bool, w io.Writer) error {
	if err := extractSharedAgentInstructionsW(dest, assets, force, w); err != nil {
		return err
	}
	if err := extractClaudeMDW(dest, assets, force, w); err != nil {
		return err
	}
	if err := extractAgentsMDW(dest, assets, force, w); err != nil {
		return err
	}
	if err := extractThresholdsJSON(dest, assets, force); err != nil {
		return err
	}
	if err := extractClaudeRuleAssets(dest, assets); err != nil {
		return err
	}

	for srcDir, destDir := range assetMapping {
		srcFS, err := fs.Sub(assets, srcDir)
		if err != nil {
			continue // directory not present in assets, skip
		}
		if err := extractAssetDir(srcFS, srcDir, dest, destDir, force); err != nil {
			return fmt.Errorf("extract %s: %w", srcDir, err)
		}
	}

	// Write version stamp so oro start can detect when assets need re-extraction.
	if versionData, err := fs.ReadFile(assets, ".version"); err == nil {
		stampPath := filepath.Join(dest, ".asset-version")
		_ = os.WriteFile(stampPath, versionData, 0o644) //nolint:gosec // stamp file needs to be readable
	}

	return nil
}
