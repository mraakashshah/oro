package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// canonicalPreCommitHook is the verbatim content installed by 'oro init'.
// Design markers: Author identity guard, gofumpt.
const canonicalPreCommitHook = `#!/usr/bin/env sh
# managed by oro — do not edit manually (run 'oro init --force' to regenerate)
#
# Canonical pre-commit hook installed by 'oro init'.
# Design markers: Author identity guard, gofumpt

# --- Beads-injection guard ---
# Detect if beads has rewritten this hook (look for BEADS INTEGRATION markers
# in the hook file itself). If found, fail with a clear error.
if grep -q 'BEADS INTEGRATION' "$0" 2>/dev/null; then
    printf 'oro: pre-commit hook was rewritten by beads\n       run: oro init --force\n' >&2
    exit 1
fi

# --- Author identity guard ---
# Require git user.email to be configured before committing.
if [ -z "$(git config user.email 2>/dev/null)" ]; then
    printf 'error: git user.email is not configured\n       fix:  git config user.email "you@example.com"\n' >&2
    exit 1
fi

# --- gofumpt ---
# Format check for Go projects.
if command -v gofumpt >/dev/null 2>&1 && [ -f go.mod ]; then
    UNFORMATTED="$(gofumpt -l . 2>/dev/null)"
    if [ -n "$UNFORMATTED" ]; then
        printf 'error: gofumpt found unformatted files — run: gofumpt -w .\n' >&2
        printf '%s\n' "$UNFORMATTED" >&2
        exit 1
    fi
fi

# --- Reject staged oro-docs/ files ---
# Prevents accidental commits of oro documentation in stealth mode.
if git diff --cached --name-only 2>/dev/null | grep -q '^oro-docs/'; then
    printf 'oro: staged files under oro-docs/ are not allowed\n' >&2
    exit 1
fi
`

// canonicalPrePushHook is the verbatim content installed by 'oro init'.
// Design markers: golangci-lint, quality_gate.sh.
const canonicalPrePushHook = `#!/usr/bin/env sh
# managed by oro — do not edit manually (run 'oro init --force' to regenerate)
#
# Canonical pre-push hook installed by 'oro init'.
# Design markers: golangci-lint, quality_gate.sh

# --- Beads-injection guard ---
# Detect if beads has rewritten this hook (look for BEADS INTEGRATION markers
# in the hook file itself). If found, fail with a clear error.
if grep -q 'BEADS INTEGRATION' "$0" 2>/dev/null; then
    printf 'oro: pre-push hook was rewritten by beads\n       run: oro init --force\n' >&2
    exit 1
fi

# --- Block agent/* and epic/* branches ---
# Oro worker branches must not be pushed to the shared remote.
while IFS= read -r line; do
    local_ref="$(echo "$line" | awk '{print $1}')"
    case "$local_ref" in
        refs/heads/agent/*|refs/heads/epic/*)
            printf 'oro: pushing %s is not allowed (agent/epic branches stay local)\n' "$local_ref" >&2
            exit 1
            ;;
    esac
done

# --- golangci-lint ---
# Static analysis for Go projects (skip when not available or not a Go project).
if command -v golangci-lint >/dev/null 2>&1 && [ -f go.mod ]; then
    GOFLAGS=-buildvcs=false golangci-lint run --timeout 5m ./... || exit 1
fi

# --- quality_gate.sh (all checks) ---
# Project-level quality gate. Runs all configured checks.
if [ -x ./quality_gate.sh ]; then
    printf 'oro: running quality_gate.sh (all checks)...\n'
    ./quality_gate.sh || exit 1
fi
`

// canonicalHookContent returns the verbatim canonical content for the named
// git hook. Returns (content, true) for known hooks, ("", false) otherwise.
func canonicalHookContent(hookName string) (string, bool) {
	switch hookName {
	case "pre-commit":
		return canonicalPreCommitHook, true
	case "pre-push":
		return canonicalPrePushHook, true
	default:
		return "", false
	}
}

// HookDriftError is returned by installCanonicalHook when an existing hook
// does not match the canonical content and --force was not set.
type HookDriftError struct {
	HookName string
	Path     string
}

func (e *HookDriftError) Error() string {
	return fmt.Sprintf("hook %q has drifted from canonical content at %s — run 'oro init --force' to restore", e.HookName, e.Path)
}

// backupNonOroHook renames the existing hook to <hookPath>.pre-oro so the
// user does not lose custom content when oro overwrites a drifted hook.
func backupNonOroHook(hookPath string, existing []byte) error {
	backupPath := hookPath + ".pre-oro"
	if err := os.WriteFile(backupPath, existing, 0o600); err != nil { //nolint:gosec // backup file
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

// installCanonicalHook writes the canonical content for hookName into the
// effective hooks directory for gitDir (respecting core.hooksPath).
//
//   - If the hook already matches canonical content: returns (true, nil) — no-op.
//   - If the hook differs and force is false: returns (false, *HookDriftError).
//   - If the hook differs and force is true: backs up existing content to
//     <hookPath>.pre-oro, then writes canonical content.
//   - If no hook exists: writes canonical content.
//
// The hooks directory is created if absent. Returns ok=false, err=nil on first
// install; ok=false, err=*HookDriftError on drift-without-force.
func installCanonicalHook(gitDir, hookName string, force bool) (bool, error) {
	canonical, ok := canonicalHookContent(hookName)
	if !ok {
		return false, fmt.Errorf("no canonical hook template for %q", hookName)
	}

	hooksDir := resolveHooksDir(gitDir)
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		return false, fmt.Errorf("create hooks dir: %w", err)
	}

	hookPath := filepath.Join(hooksDir, hookName)

	existing, readErr := os.ReadFile(hookPath) //nolint:gosec // path from trusted inputs
	if readErr == nil {
		if string(existing) == canonical {
			return true, nil // already canonical — no-op
		}
		if !force {
			return false, &HookDriftError{HookName: hookName, Path: hookPath}
		}
		if err := backupNonOroHook(hookPath, existing); err != nil {
			return false, fmt.Errorf("backup existing %s hook: %w", hookName, err)
		}
	}

	if err := os.WriteFile(hookPath, []byte(canonical), 0o755); err != nil { //nolint:gosec // 0755 intentional for hook scripts
		return false, fmt.Errorf("write %s hook: %w", hookName, err)
	}
	if err := os.Chmod(hookPath, 0o755); err != nil { //nolint:gosec // 0755 intentional for hook scripts
		return false, fmt.Errorf("chmod %s hook: %w", hookName, err)
	}
	return false, nil
}

// installCanonicalHooks installs canonical pre-commit and pre-push hooks in
// the project's git hooks directory. Drift is warned but not fatal (fail-open).
func installCanonicalHooks(projectRoot string, force bool, w io.Writer) {
	gitDir := filepath.Join(projectRoot, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return
	}
	for _, hookName := range []string{"pre-commit", "pre-push"} {
		if _, err := installCanonicalHook(gitDir, hookName, force); err != nil {
			fmt.Fprintf(w, "warning: install %s hook: %v\n", hookName, err)
		}
	}
}

// uninstallCanonicalHook removes the named git hook from the effective hooks
// directory for gitDir. It is a no-op if the hook does not exist.
func uninstallCanonicalHook(gitDir, hookName string) error {
	hooksDir := resolveHooksDir(gitDir)
	hookPath := filepath.Join(hooksDir, hookName)
	if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s hook: %w", hookName, err)
	}
	return nil
}

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
