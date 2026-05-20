package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"oro/pkg/agentassets"

	"github.com/spf13/cobra"
)

// skillsToSkip are oro-specific skills that don't make sense outside of an oro session.
var skillsToSkip = map[string]bool{ //nolint:gochecknoglobals // static config
	"restart-oro":  true,
	"watching-oro": true,
}

// portableHooks are the hook files safe to run in any Claude session.
var portableHooks = []string{ //nolint:gochecknoglobals // static config
	"auto-format.sh",
	"prompt_injection_guard.py",
	"pre_compact.py",
	"context_pruner.py",
	"stop-checklist.sh",
	"enforce_skills.py",
	"session_start_global.py",
}

const (
	agentRuntimeClaude = "claude"
	agentRuntimeCodex  = "codex"
	agentRuntimeAll    = "all"
)

// agentAssetsConfig holds injectable paths for testability.
type agentAssetsConfig struct {
	runtime         string
	oroAssetsDir    string // source bundle root under ~/.oro/
	oroSkillsDir    string // source bundle under ~/.oro/
	oroHooksDir     string // source hooks under ~/.oro/
	destSkillsDir   string // runtime-specific destination for skills
	destHooksDir    string // runtime-specific destination for hooks; empty when unsupported
	claudeRulesRoot string // home/root where .claude/rules should be installed
	codexSkillsDir  string // optional Codex destination when runtime=all
	codexPluginRoot string // local marketplace root for Codex plugin package
	settingsPath    string // runtime-specific settings.json; empty when unsupported
	deprecationOut  io.Writer
	// Legacy Claude-specific aliases kept for test and caller compatibility.
	claudeSkillsDir string
	claudeHooksDir  string
	portableHooks   []string // nil means use the package-level portableHooks default
}

// globalOroApproachConfig remains as a compatibility alias for existing tests
// and callers while agent-assets becomes the canonical command surface.
type globalOroApproachConfig = agentAssetsConfig

func newGlobalOroApproachCmd() *cobra.Command {
	return newAgentAssetsCmd("agent-assets", false)
}

func newGlobalOroApproachAliasCmd(use string) *cobra.Command {
	return newAgentAssetsCmd(use, true)
}

func newAgentAssetsCmd(use string, legacyAlias bool) *cobra.Command {
	var runtimeID string
	cmd := &cobra.Command{
		Use:   use,
		Short: "Sync oro skills and runtime bootstrap assets to agent homes",
		Long: `Syncs shared oro skills and portable runtime bootstrap assets from ~/.oro/
into a runtime-specific agent home.

What gets synced:
  Claude runtime:
    Skills: all skills except restart-oro and watching-oro → ~/.claude/skills/ (symlinks)
    Hooks:  portable hooks → ~/.claude/hooks/ (copies)
    Settings: ~/.claude/settings.json hooks section is updated

  Codex runtime:
    Skills: all skills except restart-oro and watching-oro → $CODEX_HOME/skills/ (symlinks)
    Hooks: skipped
    Settings: skipped

The legacy 'global-skills' alias remains as a Claude-targeted compatibility path
during migration.

Re-run after adding or removing skills in ~/.oro/.claude/skills/.
Editing existing skills doesn't require re-running (symlinks are live).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("get home dir: %w", err)
			}
			if legacyAlias {
				cfg := defaultAgentAssetsConfig(homeDir, agentRuntimeClaude)
				cfg.runtime = agentRuntimeClaude
				cfg.deprecationOut = cmd.ErrOrStderr()
				return runGlobalOroApproach(cfg, cmd.OutOrStdout())
			}
			cfg := defaultAgentAssetsConfig(homeDir, runtimeID)
			return runAgentAssetsSync(cfg, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVar(&runtimeID, "runtime", agentRuntimeClaude, "target runtime for synced agent assets (claude, codex, or all)")
	return cmd
}

func defaultAgentAssetsConfig(homeDir, runtimeID string) agentAssetsConfig {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(homeDir, ".codex")
	}

	cfg := agentAssetsConfig{
		runtime:      runtimeID,
		oroAssetsDir: filepath.Join(homeDir, ".oro"),
		oroSkillsDir: filepath.Join(homeDir, ".oro", ".claude", "skills"),
		oroHooksDir:  filepath.Join(homeDir, ".oro", "hooks"),
	}
	switch runtimeID {
	case agentRuntimeCodex:
		cfg.destSkillsDir = filepath.Join(codexHome, "skills")
		cfg.codexPluginRoot = filepath.Join(codexHome, "oro-marketplace")
	case agentRuntimeAll:
		cfg.destSkillsDir = filepath.Join(homeDir, ".claude", "skills")
		cfg.destHooksDir = filepath.Join(homeDir, ".claude", "hooks")
		cfg.claudeRulesRoot = homeDir
		cfg.codexSkillsDir = filepath.Join(codexHome, "skills")
		cfg.codexPluginRoot = filepath.Join(codexHome, "oro-marketplace")
		cfg.settingsPath = filepath.Join(homeDir, ".claude", "settings.json")
	default:
		cfg.runtime = agentRuntimeClaude
		cfg.destSkillsDir = filepath.Join(homeDir, ".claude", "skills")
		cfg.destHooksDir = filepath.Join(homeDir, ".claude", "hooks")
		cfg.claudeRulesRoot = homeDir
		cfg.settingsPath = filepath.Join(homeDir, ".claude", "settings.json")
	}
	return cfg
}

// runGlobalOroApproach is the testable core of the global-oro-approach command.
func runGlobalOroApproach(cfg globalOroApproachConfig, w io.Writer) error {
	if cfg.runtime == "" {
		cfg.runtime = agentRuntimeClaude
	}
	if cfg.runtime == agentRuntimeClaude {
		deprecationOut := cfg.deprecationOut
		if deprecationOut == nil {
			deprecationOut = w
		}
		fmt.Fprintln(deprecationOut, "warning: `oro global-skills` is deprecated; use `oro agent-assets --runtime claude`.")
	}
	return runAgentAssetsSync(cfg, w)
}

// runAgentAssetsSync syncs portable Oro assets into the chosen runtime home.
func runAgentAssetsSync(cfg agentAssetsConfig, w io.Writer) error {
	if cfg.runtime == agentRuntimeAll {
		claudeCfg := cfg
		claudeCfg.runtime = agentRuntimeClaude
		if err := syncAgentRuntimeAssets(claudeCfg, w); err != nil {
			return err
		}

		codexCfg := cfg
		codexCfg.runtime = agentRuntimeCodex
		if cfg.codexSkillsDir != "" {
			codexCfg.destSkillsDir = cfg.codexSkillsDir
		}
		codexCfg.destHooksDir = ""
		codexCfg.claudeHooksDir = ""
		codexCfg.settingsPath = ""
		codexCfg.claudeRulesRoot = ""
		return syncAgentRuntimeAssets(codexCfg, w)
	}

	return syncAgentRuntimeAssets(cfg, w)
}

func syncAgentRuntimeAssets(cfg agentAssetsConfig, w io.Writer) error {
	if err := copySkills(cfg, w); err != nil {
		return err
	}
	if cfg.runtime == agentRuntimeCodex {
		if err := installCodexPluginPackage(cfg, w); err != nil {
			return err
		}
	}
	if !runtimeSupportsHooks(cfg) {
		fmt.Fprintf(w, "hooks: skipped for runtime %s\n", cfg.runtime)
		return nil
	}
	if err := copyHooks(cfg, w); err != nil {
		return err
	}
	if err := updateGlobalSettings(cfg, w); err != nil {
		return err
	}
	return installClaudeRuleAssets(cfg, w)
}

func installCodexPluginPackage(cfg agentAssetsConfig, w io.Writer) error {
	if cfg.codexPluginRoot == "" {
		return nil
	}

	assets, err := (agentassets.CodexGenerator{}).PluginPackage(cfg.oroHooksDir)
	if err != nil {
		return fmt.Errorf("generate Codex plugin package: %w", err)
	}
	if err := agentassets.InstallCodexPluginPackage(context.Background(), cfg.codexPluginRoot, assets); err != nil {
		return fmt.Errorf("install Codex plugin package: %w", err)
	}
	fmt.Fprintf(w, "plugin: installed Codex local marketplace package to %s\n", cfg.codexPluginRoot)
	return nil
}

func installClaudeRuleAssets(cfg agentAssetsConfig, w io.Writer) error {
	if cfg.oroAssetsDir == "" || cfg.claudeRulesRoot == "" {
		return nil
	}

	assets, err := (agentassets.ClaudeGenerator{Source: os.DirFS(cfg.oroAssetsDir)}).RuleAssets()
	if err != nil {
		return fmt.Errorf("generate Claude rule assets: %w", err)
	}
	if err := agentassets.InstallClaudeRules(context.Background(), cfg.claudeRulesRoot, assets); err != nil {
		return fmt.Errorf("install Claude rule assets: %w", err)
	}
	fmt.Fprintf(w, "rules: installed %d Claude rules to %s\n", len(assets), filepath.Join(cfg.claudeRulesRoot, ".claude", "rules"))
	return nil
}

func runtimeSupportsHooks(cfg agentAssetsConfig) bool {
	return cfg.hooksDir() != "" && cfg.settingsPath != ""
}

func (cfg agentAssetsConfig) skillsDir() string {
	if cfg.destSkillsDir != "" {
		return cfg.destSkillsDir
	}
	return cfg.claudeSkillsDir
}

func (cfg agentAssetsConfig) hooksDir() string {
	if cfg.destHooksDir != "" {
		return cfg.destHooksDir
	}
	return cfg.claudeHooksDir
}

// copySkills symlinks all skill directories (except blocked ones) from oroSkillsDir into destSkillsDir.
// Any existing entry at the destination (symlink or directory) is removed first so re-runs are idempotent.
func copySkills(cfg agentAssetsConfig, w io.Writer) error {
	entries, err := os.ReadDir(cfg.oroSkillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(w, "skills source not found, skipping: %s\n", cfg.oroSkillsDir)
			return nil
		}
		return fmt.Errorf("read skills dir: %w", err)
	}

	destSkillsDir := cfg.skillsDir()
	if err := os.MkdirAll(destSkillsDir, 0o750); err != nil {
		return fmt.Errorf("create skills dest: %w", err)
	}

	linked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if skillsToSkip[e.Name()] {
			continue
		}
		src := filepath.Join(cfg.oroSkillsDir, e.Name())
		dst := filepath.Join(destSkillsDir, e.Name())

		// Remove any existing entry (old copy or stale symlink) before creating a fresh symlink.
		if _, lstatErr := os.Lstat(dst); lstatErr == nil {
			if err := os.RemoveAll(dst); err != nil {
				return fmt.Errorf("remove existing skill %s: %w", e.Name(), err)
			}
		}

		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlink skill %s: %w", e.Name(), err)
		}
		linked++
	}
	fmt.Fprintf(w, "skills: linked %d to %s\n", linked, destSkillsDir)
	return nil
}

// copyHooks copies the portable hooks, fixes hardcoded ~/.oro paths, and
// removes any stale files from claudeHooksDir that are no longer in the list.
func copyHooks(cfg agentAssetsConfig, w io.Writer) error {
	destHooksDir := cfg.hooksDir()
	if err := os.MkdirAll(destHooksDir, 0o750); err != nil {
		return fmt.Errorf("create hooks dest: %w", err)
	}

	hooks := cfg.portableHooks
	if hooks == nil {
		hooks = portableHooks
	}

	// Build allow-set for stale-file removal.
	allow := make(map[string]bool, len(hooks))
	for _, name := range hooks {
		allow[name] = true
	}

	copied := 0
	for _, name := range hooks {
		src := filepath.Join(cfg.oroHooksDir, name)
		data, err := os.ReadFile(src) //nolint:gosec // path is from trusted config
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(w, "hook not found, skipping: %s\n", name)
				continue
			}
			return fmt.Errorf("read hook %s: %w", name, err)
		}

		// Fix hardcoded ~/.oro paths to ~/.claude
		content := strings.ReplaceAll(string(data), `".oro"`, `".claude"`)

		dst := filepath.Join(destHooksDir, name)
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("stat hook %s: %w", name, err)
		}
		if err := os.WriteFile(dst, []byte(content), info.Mode()); err != nil { //nolint:gosec // dst is trusted path
			return fmt.Errorf("write hook %s: %w", name, err)
		}
		copied++
	}

	// Remove stale files in the destination that are no longer portable.
	if existing, err := os.ReadDir(destHooksDir); err == nil {
		for _, entry := range existing {
			if entry.IsDir() || allow[entry.Name()] {
				continue
			}
			_ = os.Remove(filepath.Join(destHooksDir, entry.Name()))
		}
	}

	fmt.Fprintf(w, "hooks: copied %d to %s\n", copied, destHooksDir)
	return nil
}

// globalHooks returns the portable hooks wiring for ~/.claude/settings.json.
func globalHooks(hooksDir string) map[string][]agentassets.HookGroup {
	return (agentassets.ClaudeGenerator{}).Hooks(hooksDir)
}

// updateGlobalSettings merges the portable hooks into ~/.claude/settings.json,
// preserving all other keys.
func updateGlobalSettings(cfg agentAssetsConfig, w io.Writer) error {
	data, err := os.ReadFile(cfg.settingsPath) //nolint:gosec // trusted path
	if err != nil {
		return fmt.Errorf("read %s: %w", cfg.settingsPath, err)
	}

	// Parse as generic map to preserve unknown keys.
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", cfg.settingsPath, err)
	}
	if settings == nil {
		settings = make(map[string]json.RawMessage)
	}

	// Build and encode the hooks section.
	// Always use tilde notation so the commands work on any machine.
	hooks := globalHooks("~/.claude/hooks")
	hooksRaw, err := json.Marshal(hooks)
	if err != nil {
		return fmt.Errorf("marshal hooks: %w", err)
	}
	settings["hooks"] = hooksRaw

	out, err := json.MarshalIndent(settings, "", "\t")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	if err := os.WriteFile(cfg.settingsPath, append(out, '\n'), 0o640); err != nil { //nolint:gosec // trusted path
		return fmt.Errorf("write %s: %w", cfg.settingsPath, err)
	}

	fmt.Fprintf(w, "settings: updated hooks in %s\n", cfg.settingsPath)
	return nil
}
