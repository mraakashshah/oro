package main

import (
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
	)

	cmd := &cobra.Command{
		Use:   "init [project-name]",
		Short: "Bootstrap oro dependencies, generate config, and extract assets",
		Long: `Checks for and installs all tools required by the oro agent swarm,
creates .oro/config.yaml with project identity, generates per-project
settings.json with hook paths, and extracts embedded assets to ~/.oro/.

Phase 1: System prerequisites (Go, Python, Node, npm, Homebrew)
Phase 2: Go tools (gofumpt, goimports, golangci-lint, etc.)
Phase 3: Python tools (uv, ruff, pyright)
Phase 4: System tools (tmux, shellcheck, biome, jq, ast-grep, bd)
Phase 5: Bootstrap project (config anchor, settings, embedded assets)

The project name is taken from the first argument, or defaults to the
directory name of --project-root.

Use --check to verify without installing (exits non-zero if any tool is missing).
Use --quiet to suppress all output (useful for CI scripts).
Use --project-root to specify a different project directory (default: current directory).
Use --force to overwrite existing .oro/config.yaml.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			projectName := ""
			if len(args) > 0 {
				projectName = args[0]
			}
			return runInit(w, checkOnly, quiet, projectRoot, projectName)
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "verify tools without installing (exit 1 if any missing)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress output, just exit code")
	cmd.Flags().StringVar(&projectRoot, "project-root", ".", "project root directory for config generation")

	return cmd
}

// runInit is the core logic for the init command, separated for testability.
func runInit(w io.Writer, checkOnly, quiet bool, projectRoot, projectName string) error {
	if err := ensureTools(w, checkOnly, quiet); err != nil {
		return err
	}
	if checkOnly || quiet {
		return nil
	}

	name, err := resolveProjectName(projectRoot, projectName)
	if err != nil {
		return err
	}

	oroHome, err := resolveOroHome()
	if err != nil {
		return err
	}

	subAssets, err := fs.Sub(EmbeddedAssets, "_assets")
	if err != nil {
		return fmt.Errorf("access embedded assets: %w", err)
	}

	if err := bootstrapProject(projectRoot, name, oroHome, subAssets); err != nil {
		return fmt.Errorf("bootstrap project: %w", err)
	}

	fmt.Fprintf(w, "\n✓ Initialized project %q\n", name)
	fmt.Fprintf(w, "  Local anchor: %s/.oro/config.yaml\n", projectRoot)
	fmt.Fprintf(w, "  Project dir:  %s/projects/%s/\n", oroHome, name)
	fmt.Fprintf(w, "  Settings:     %s/projects/%s/settings.json\n", oroHome, name)
	fmt.Fprintf(w, "\nRun 'oro start' to launch agents.\n")

	return nil
}

// ensureTools checks for required tools, optionally installing missing ones.
// Returns nil on success. In check-only or quiet mode, returns an error if tools are missing.
func ensureTools(w io.Writer, checkOnly, quiet bool) error {
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

	return installMissingTools(w, results)
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

// resolveOroHome returns the oro home directory from ORO_HOME env var or ~/.oro.
func resolveOroHome() (string, error) {
	if v := os.Getenv("ORO_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".oro"), nil
}

// bootstrapProject orchestrates project initialization for externalized config.
// It creates the local anchor (.oro/config.yaml), manages .gitignore, generates
// per-project settings.json, creates handoffs dir, and extracts embedded assets.
func bootstrapProject(projectRoot, projectName, oroHome string, assets fs.FS) error {
	// 1. Create local anchor: .oro/config.yaml with project name.
	if err := createProjectAnchor(projectRoot, projectName); err != nil {
		return fmt.Errorf("create project anchor: %w", err)
	}

	// 2. Add .oro/ and .beads to .gitignore if not present.
	if err := ensureGitignore(projectRoot, ".oro/"); err != nil {
		return fmt.Errorf("update gitignore: %w", err)
	}
	if err := ensureGitignore(projectRoot, ".beads"); err != nil {
		return fmt.Errorf("update gitignore for .beads: %w", err)
	}

	// 3. Create per-project directory structure under oroHome.
	projectDir := filepath.Join(oroHome, "projects", projectName)
	handoffsDir := filepath.Join(projectDir, "handoffs")
	if err := os.MkdirAll(handoffsDir, 0o755); err != nil { //nolint:gosec // project dir needs to be readable
		return fmt.Errorf("create project dir: %w", err)
	}

	// 4. Set up beads symlink: .beads → oroHome/projects/<name>/beads/
	beadsTarget := filepath.Join(projectDir, "beads")
	if err := setupBeadsSymlink(projectRoot, beadsTarget); err != nil {
		return fmt.Errorf("setup beads symlink: %w", err)
	}

	// 5. Generate settings.json (always overwrite — idempotent).
	settingsData, err := generateSettings("$HOME/.oro")
	if err != nil {
		return fmt.Errorf("generate settings: %w", err)
	}
	settingsPath := filepath.Join(projectDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsData, 0o644); err != nil { //nolint:gosec // settings file needs to be readable
		return fmt.Errorf("write settings: %w", err)
	}

	// 6. Extract embedded assets to oroHome.
	if err := extractAssets(oroHome, assets); err != nil {
		return fmt.Errorf("extract assets: %w", err)
	}

	return nil
}

// createProjectAnchor writes the .oro/config.yaml anchor file in the project root.
// It includes the project name and detected language profiles.
func createProjectAnchor(projectRoot, projectName string) error {
	oroDir := filepath.Join(projectRoot, ".oro")
	if err := os.MkdirAll(oroDir, 0o755); err != nil { //nolint:gosec // config dir needs to be readable
		return fmt.Errorf("create .oro dir: %w", err)
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("project: %s\n", projectName))

	// Detect languages and append profile config.
	profiles := langprofile.AllProfiles()
	cfg, err := langprofile.GenerateConfig(projectRoot, profiles)
	if err == nil {
		langYAML := langprofile.BuildYAML(cfg)
		if langYAML != "" {
			buf.WriteString(langYAML)
		}
	}

	configPath := filepath.Join(oroDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(buf.String()), 0o644); err != nil { //nolint:gosec // config file needs to be readable
		return fmt.Errorf("write config.yaml: %w", err)
	}
	return nil
}

// ensureGitignore adds entry to .gitignore if not already present.
// Creates .gitignore if it doesn't exist.
func ensureGitignore(projectRoot, entry string) error {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")

	existing, err := os.ReadFile(gitignorePath) //nolint:gosec // path constructed from trusted projectRoot
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	// Check if entry already present (line-by-line match).
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil // already present
		}
	}

	// Append entry. Ensure existing content ends with newline.
	var buf strings.Builder
	if len(existing) > 0 {
		buf.Write(existing)
		if !strings.HasSuffix(string(existing), "\n") {
			buf.WriteByte('\n')
		}
	}
	buf.WriteString(entry)
	buf.WriteByte('\n')

	if err := os.WriteFile(gitignorePath, []byte(buf.String()), 0o644); err != nil { //nolint:gosec // gitignore needs to be readable
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return nil
}

// setupBeadsSymlink creates a beads data directory under oroHome and symlinks
// <projectRoot>/.beads to it. This keeps beads data centralized in ~/.oro/
// rather than polluting the target project.
//
// If .beads already exists as a real directory (not a symlink), it is left
// alone to avoid data loss during migration.
func setupBeadsSymlink(projectRoot, beadsTarget string) error {
	// Ensure the target directory exists.
	if err := os.MkdirAll(beadsTarget, 0o755); err != nil { //nolint:gosec // needs to be readable
		return fmt.Errorf("create beads dir: %w", err)
	}

	linkPath := filepath.Join(projectRoot, ".beads")

	// Nothing exists yet — create the symlink.
	fi, err := os.Lstat(linkPath)
	if err != nil {
		if err := os.Symlink(beadsTarget, linkPath); err != nil {
			return fmt.Errorf("create .beads symlink: %w", err)
		}
		return nil
	}

	// It's a real directory — don't clobber it.
	if fi.IsDir() {
		return nil
	}

	// It's already the correct symlink — no-op.
	if fi.Mode()&os.ModeSymlink != 0 {
		if target, readErr := os.Readlink(linkPath); readErr == nil && target == beadsTarget {
			return nil
		}
		// Wrong target — remove stale symlink and recreate.
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove stale symlink: %w", err)
		}
	}

	if err := os.Symlink(beadsTarget, linkPath); err != nil {
		return fmt.Errorf("create .beads symlink: %w", err)
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
	py := func(s string) string { return "python3 " + hooksDir + "/" + s }
	sh := func(s string) string { return hooksDir + "/" + s }

	return map[string][]hookGroup{
		"SessionStart": {{
			Matcher: "",
			Hooks: []hookEntry{
				{Type: "command", Command: sh("enforce-skills.sh")},
				{Type: "command", Command: py("session_start_extras.py"), StatusMessage: "Loading project context..."},
			},
		}},
		"PreToolUse": {
			{Matcher: "", Hooks: []hookEntry{
				{Type: "command", Command: py("inject_context_usage.py")},
			}},
			{Matcher: "Bash", Hooks: []hookEntry{
				{Type: "command", Command: py("architect_router.py")},
				{Type: "command", Command: py("worktree_guard.py")},
				{Type: "command", Command: py("no_cd_guard.py")},
				{Type: "command", Command: py("rebase_worktree_guard.py")},
			}},
			{Matcher: "Read", Hooks: []hookEntry{
				{Type: "command", Command: sh("oro-search-hook"), Timeout: 5000, StatusMessage: "Searching codebase..."},
			}},
			{Matcher: "Task", Hooks: []hookEntry{
				{Type: "command", Command: py("enforce_worktree.py")},
			}},
		},
		"PostToolUse": {
			{Matcher: "Read|WebFetch|Bash", Hooks: []hookEntry{
				{Type: "command", Command: py("prompt_injection_guard.py")},
			}},
			{Matcher: "Edit|Write", Hooks: []hookEntry{
				{Type: "command", Command: sh("auto-format.sh")},
			}},
			{Matcher: "Bash", Hooks: []hookEntry{
				{Type: "command", Command: py("memory_capture.py")},
				{Type: "command", Command: py("learning_reminder.py")},
				{Type: "command", Command: py("bd_create_notifier.py")},
			}},
			{Matcher: "Task", Hooks: []hookEntry{
				{Type: "command", Command: py("validate_agent_completion.py")},
			}},
		},
		"Stop": {{
			Matcher: "",
			Hooks: []hookEntry{
				{Type: "command", Command: sh("stop-checklist.sh")},
			},
		}},
	}
}

// generateSettings produces a settings.json with hook commands using absolute
// paths under oroHome. Shell variable $HOME is used for portability.
func generateSettings(oroHome string) ([]byte, error) {
	settings := struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}{
		Hooks: buildHookConfig(oroHome + "/hooks"),
	}

	data, err := json.MarshalIndent(settings, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	return data, nil
}

// assetMapping maps source directory names in the embedded FS to their
// destination paths relative to oroHome.
var assetMapping = map[string]string{ //nolint:gochecknoglobals // static config
	"skills":   filepath.Join(".claude", "skills"),
	"hooks":    "hooks",
	"beacons":  "beacons",
	"commands": filepath.Join(".claude", "commands"),
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

// extractAssets walks the embedded FS and copies files to oroHome.
// Directory mapping: skills → .claude/skills/, hooks → hooks/, beacons → beacons/,
// commands → .claude/commands/, CLAUDE.md → .claude/CLAUDE.md.
func extractAssets(dest string, assets fs.FS) error {
	// Handle CLAUDE.md specially (single file, not a directory).
	if data, err := fs.ReadFile(assets, "CLAUDE.md"); err == nil {
		claudeDir := filepath.Join(dest, ".claude")
		if err := os.MkdirAll(claudeDir, 0o755); err != nil { //nolint:gosec // needs to be readable
			return fmt.Errorf("create .claude dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), data, 0o644); err != nil { //nolint:gosec // needs to be readable
			return fmt.Errorf("write CLAUDE.md: %w", err)
		}
	}

	// Walk each mapped directory.
	for srcDir, destDir := range assetMapping {
		srcFS, err := fs.Sub(assets, srcDir)
		if err != nil {
			continue // directory not present in assets, skip
		}

		err = fs.WalkDir(srcFS, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			destPath := filepath.Join(dest, destDir, path)

			if d.IsDir() {
				return os.MkdirAll(destPath, 0o755) //nolint:gosec // needs to be readable
			}

			data, err := fs.ReadFile(srcFS, path)
			if err != nil {
				return fmt.Errorf("read %s/%s: %w", srcDir, path, err)
			}

			return os.WriteFile(destPath, data, filePermForAsset(path)) //nolint:gosec // needs to be readable
		})
		if err != nil {
			return fmt.Errorf("extract %s: %w", srcDir, err)
		}
	}

	return nil
}
