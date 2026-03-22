package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveHooksDir returns the effective git hooks directory for gitDir.
// If core.hooksPath is set in <gitDir>/config, that value is returned
// (resolved relative to the repo root when not absolute).
// Otherwise defaults to <gitDir>/hooks.
func resolveHooksDir(gitDir string) string {
	configPath := filepath.Join(gitDir, "config")
	data, err := os.ReadFile(configPath) //nolint:gosec // path constructed from trusted gitDir
	if err != nil {
		return filepath.Join(gitDir, "hooks")
	}

	inCore := false
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inCore = strings.EqualFold(line, "[core]")
			continue
		}
		if !inCore {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "hooksPath" {
			hp := strings.TrimSpace(parts[1])
			if !filepath.IsAbs(hp) {
				// Relative to repo working tree (parent of .git/).
				hp = filepath.Join(filepath.Dir(gitDir), hp)
			}
			return hp
		}
	}
	return filepath.Join(gitDir, "hooks")
}

// buildWrapperScript returns the content of the git hook wrapper script.
// The wrapper runs the <hookName>.user backup (if executable) first, then
// applies the oro check.
func buildWrapperScript(hookName, oroCheck string) string {
	return fmt.Sprintf(`#!/bin/sh
# managed by oro — do not edit manually

set -e

# Run user hook if present.
HOOK_DIR="$(dirname "$0")"
if [ -x "${HOOK_DIR}/%s.user" ]; then
    "${HOOK_DIR}/%s.user" "$@"
fi

%s
`, hookName, hookName, oroCheck)
}

// installHookWrapper installs an oro git hook wrapper for the given hookName
// inside gitDir's effective hooks directory (respecting core.hooksPath).
//
// If an existing hook is already an oro wrapper, it is overwritten in-place
// (idempotent). If an existing hook is a genuine user hook (executable and
// not managed by oro), it is renamed to <hookName>.user before installing the
// wrapper. Non-executable files are overwritten without backup.
//
// The hooks directory is created if it does not exist.
func installHookWrapper(gitDir, hookName, oroCheck string) error {
	hooksDir := resolveHooksDir(gitDir)
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	hookPath := filepath.Join(hooksDir, hookName)

	// Check whether an existing hook is already an oro wrapper (idempotent reinstall)
	// or a genuine user hook that needs backup.
	content, readErr := os.ReadFile(hookPath) //nolint:gosec // hookPath constructed from trusted inputs
	isOroWrapper := readErr == nil && strings.Contains(string(content), "managed by oro")
	if !isOroWrapper {
		if info, statErr := os.Stat(hookPath); statErr == nil && info.Mode()&0o111 != 0 {
			// Genuine executable user hook — back it up.
			if err := os.Rename(hookPath, hookPath+".user"); err != nil {
				return fmt.Errorf("backup existing %s hook: %w", hookName, err)
			}
		}
	}

	wrapper := buildWrapperScript(hookName, oroCheck)
	if err := os.WriteFile(hookPath, []byte(wrapper), 0o755); err != nil { //nolint:gosec // 0755 intentional for hook scripts
		return fmt.Errorf("write %s hook wrapper: %w", hookName, err)
	}
	// Explicitly chmod: os.WriteFile preserves existing permissions on existing files.
	if err := os.Chmod(hookPath, 0o755); err != nil { //nolint:gosec // 0755 intentional for hook scripts
		return fmt.Errorf("chmod %s hook wrapper: %w", hookName, err)
	}
	return nil
}

// uninstallHookWrapper removes the oro wrapper for hookName and restores any
// .user backup that was created during install.
func uninstallHookWrapper(gitDir, hookName string) error {
	hooksDir := resolveHooksDir(gitDir)
	hookPath := filepath.Join(hooksDir, hookName)
	userPath := hookPath + ".user"

	if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s hook wrapper: %w", hookName, err)
	}

	// Restore .user backup if it exists.
	if _, err := os.Stat(userPath); err == nil {
		if err := os.Rename(userPath, hookPath); err != nil {
			return fmt.Errorf("restore %s hook backup: %w", hookName, err)
		}
	}
	return nil
}
