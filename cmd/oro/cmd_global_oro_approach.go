package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
}

// globalOroApproachConfig holds injectable paths for testability.
type globalOroApproachConfig struct {
	oroSkillsDir    string // source: ~/.oro/.claude/skills/
	oroHooksDir     string // source: ~/.oro/hooks/
	claudeSkillsDir string // dest: ~/.claude/skills/
	claudeHooksDir  string // dest: ~/.claude/hooks/
	settingsPath    string // ~/.claude/settings.json
}

func newGlobalOroApproachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "global-oro-approach",
		Short: "Copy oro skills and hooks to ~/.claude/ for use in all Claude sessions",
		Long: `Copies the oro disciplined-workflow skills and portable hooks from
~/.oro/ into ~/.claude/ so every Claude session (not just oro projects)
benefits from the same TDD, debugging, and verification workflows.

What gets copied:
  Skills: all skills except restart-oro and watching-oro → ~/.claude/skills/
  Hooks:  auto-format, prompt-injection-guard, pre-compact, context-pruner,
          stop-checklist, enforce-skills → ~/.claude/hooks/

The hooks section of ~/.claude/settings.json is replaced with wiring for
the copied hooks. All other settings are preserved.

Re-run after upgrading oro to pick up updated skills and hooks.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("get home dir: %w", err)
			}
			cfg := globalOroApproachConfig{
				oroSkillsDir:    filepath.Join(homeDir, ".oro", ".claude", "skills"),
				oroHooksDir:     filepath.Join(homeDir, ".oro", "hooks"),
				claudeSkillsDir: filepath.Join(homeDir, ".claude", "skills"),
				claudeHooksDir:  filepath.Join(homeDir, ".claude", "hooks"),
				settingsPath:    filepath.Join(homeDir, ".claude", "settings.json"),
			}
			return runGlobalOroApproach(cfg, cmd.OutOrStdout())
		},
	}
}

// runGlobalOroApproach is the testable core of the global-oro-approach command.
func runGlobalOroApproach(cfg globalOroApproachConfig, w io.Writer) error {
	if err := copySkills(cfg, w); err != nil {
		return err
	}
	if err := copyHooks(cfg, w); err != nil {
		return err
	}
	return updateGlobalSettings(cfg, w)
}

// copySkills copies all skill directories except the blocked ones.
func copySkills(cfg globalOroApproachConfig, w io.Writer) error {
	entries, err := os.ReadDir(cfg.oroSkillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(w, "skills source not found, skipping: %s\n", cfg.oroSkillsDir)
			return nil
		}
		return fmt.Errorf("read skills dir: %w", err)
	}

	if err := os.MkdirAll(cfg.claudeSkillsDir, 0o750); err != nil {
		return fmt.Errorf("create skills dest: %w", err)
	}

	copied := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if skillsToSkip[e.Name()] {
			continue
		}
		src := filepath.Join(cfg.oroSkillsDir, e.Name())
		dst := filepath.Join(cfg.claudeSkillsDir, e.Name())
		if err := copyDirRecursive(src, dst); err != nil {
			return fmt.Errorf("copy skill %s: %w", e.Name(), err)
		}
		copied++
	}
	fmt.Fprintf(w, "skills: copied %d to %s\n", copied, cfg.claudeSkillsDir)
	return nil
}

// copyHooks copies the portable hooks and fixes hardcoded ~/.oro paths.
func copyHooks(cfg globalOroApproachConfig, w io.Writer) error {
	if err := os.MkdirAll(cfg.claudeHooksDir, 0o750); err != nil {
		return fmt.Errorf("create hooks dest: %w", err)
	}

	copied := 0
	for _, name := range portableHooks {
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

		dst := filepath.Join(cfg.claudeHooksDir, name)
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("stat hook %s: %w", name, err)
		}
		if err := os.WriteFile(dst, []byte(content), info.Mode()); err != nil { //nolint:gosec // dst is trusted path
			return fmt.Errorf("write hook %s: %w", name, err)
		}
		copied++
	}
	fmt.Fprintf(w, "hooks: copied %d to %s\n", copied, cfg.claudeHooksDir)
	return nil
}

// globalHooks returns the portable hooks wiring for ~/.claude/settings.json.
func globalHooks(hooksDir string) map[string][]hookGroup {
	py := func(s string) string { return "python3 " + hooksDir + "/" + s }
	sh := func(s string) string { return hooksDir + "/" + s }

	return map[string][]hookGroup{
		"PreCompact": {{
			Matcher: "",
			Hooks:   []hookEntry{{Type: "command", Command: py("pre_compact.py")}},
		}},
		"PreToolUse": {{
			Matcher: "",
			Hooks:   []hookEntry{{Type: "command", Command: py("enforce_skills.py")}},
		}},
		"PostToolUse": {
			{Matcher: "Read|WebFetch|Bash", Hooks: []hookEntry{
				{Type: "command", Command: py("prompt_injection_guard.py")},
			}},
			{Matcher: "Edit|Write", Hooks: []hookEntry{
				{Type: "command", Command: sh("auto-format.sh")},
			}},
			{Matcher: "", Hooks: []hookEntry{
				{Type: "command", Command: py("context_pruner.py")},
			}},
		},
		"Stop": {{
			Matcher: "",
			Hooks:   []hookEntry{{Type: "command", Command: sh("stop-checklist.sh")}},
		}},
	}
}

// updateGlobalSettings merges the portable hooks into ~/.claude/settings.json,
// preserving all other keys.
func updateGlobalSettings(cfg globalOroApproachConfig, w io.Writer) error {
	data, err := os.ReadFile(cfg.settingsPath) //nolint:gosec // trusted path
	if err != nil {
		return fmt.Errorf("read %s: %w", cfg.settingsPath, err)
	}

	// Parse as generic map to preserve unknown keys.
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parse %s: %w", cfg.settingsPath, err)
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
