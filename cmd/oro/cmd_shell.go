package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// shellRunner abstracts launching claude for testability.
type shellRunner func(claudePath string, args, env []string) error

// shellDeps holds injectable dependencies for runShell.
type shellDeps struct {
	runner   shellRunner
	lookPath func(string) (string, error)
}

// execShellRunner replaces the current process with claude (interactive use).
func execShellRunner(claudePath string, args, env []string) error {
	if err := syscall.Exec(claudePath, append([]string{claudePath}, args...), env); err != nil { //nolint:gosec // intentionally replacing process with user-selected binary
		return fmt.Errorf("exec claude: %w", err)
	}
	return nil
}

// newShellCmd creates the "oro shell" subcommand.
func newShellCmd() *cobra.Command {
	return newShellCmdWithDeps(shellDeps{
		runner:   execShellRunner,
		lookPath: exec.LookPath,
	})
}

func newShellCmdWithDeps(deps shellDeps) *cobra.Command {
	var resume bool

	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Launch interactive claude with oro settings wired in",
		Long: `Launches an interactive claude session with oro settings pre-configured.

Sets ORO_HOME and ORO_PROJECT environment variables and passes
--add-dir and --settings to claude. Reads project identity from
.oro/config.yaml in the current directory.

Pass extra flags to claude after --:
  oro shell -- --dangerously-skip-permissions`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShell(".", args, resume, deps)
		},
	}

	cmd.Flags().BoolVar(&resume, "resume", false, "resume last claude session (passes --resume to claude)")

	return cmd
}

// runShell is the testable core of the shell command.
// dir is the project root to read .oro/config.yaml from (typically ".").
// extraArgs are appended verbatim after the constructed claude args.
func runShell(dir string, extraArgs []string, resume bool, deps shellDeps) error {
	// 1. Read project name from .oro/config.yaml — required for shell.
	name, err := readRequiredProjectConfig(dir)
	if err != nil {
		return err
	}

	// 2. Resolve ORO_HOME.
	oroHome, err := resolveOroHome()
	if err != nil {
		return fmt.Errorf("resolve oro home: %w", err)
	}

	// 3. Verify settings.json exists — it's the claude settings file for this project.
	settingsPath := filepath.Join(oroHome, "projects", name, "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		return fmt.Errorf("settings not found at %s — run oro setup first", settingsPath)
	}

	// 4. Locate claude binary.
	claudePath, err := deps.lookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found in PATH: %w", err)
	}

	// 5. Build claude args: --add-dir, --settings, optional --resume, extra passthrough.
	claudeArgs := []string{"--add-dir", oroHome, "--settings", settingsPath}
	if resume {
		claudeArgs = append(claudeArgs, "--resume")
	}
	claudeArgs = append(claudeArgs, extraArgs...)

	// 6. Build env with ORO_HOME and ORO_PROJECT set.
	env := upsertEnvVar(os.Environ(), "ORO_HOME", oroHome)
	env = upsertEnvVar(env, "ORO_PROJECT", name)

	return deps.runner(claudePath, claudeArgs, env)
}

// readRequiredProjectConfig reads the project name from .oro/config.yaml in dir.
// Unlike readProjectConfig, it errors when the file is missing or the project field is absent/empty.
func readRequiredProjectConfig(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".oro", "config.yaml")) //nolint:gosec // path from trusted dir
	if os.IsNotExist(err) {
		return "", fmt.Errorf("no .oro/config.yaml found — run oro init first")
	}
	if err != nil {
		return "", fmt.Errorf("read .oro/config.yaml: %w", err)
	}
	// Simple line-based parsing — avoids YAML dependency for one field.
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "project:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "project:"))
			if name == "" {
				return "", fmt.Errorf(".oro/config.yaml: empty project field — run oro init first")
			}
			return name, nil
		}
	}
	return "", fmt.Errorf(".oro/config.yaml: missing project field — run oro init first")
}

// upsertEnvVar replaces the first occurrence of key in env or appends key=value.
func upsertEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			result := make([]string, len(env))
			copy(result, env)
			result[i] = prefix + value
			return result
		}
	}
	return append(env, prefix+value)
}
