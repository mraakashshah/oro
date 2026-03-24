package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// bootstrapStealthProject initializes stealth (zero-footprint) mode for a project.
// All state is stored under oroHome/projects/s-<hash>; nothing is written to projectRoot.
//
// Created files:
//   - <stealthDir>/config.yaml   — mode anchor containing "mode: stealth"
//   - <stealthDir>/beads/        — beads data directory
//   - <stealthDir>/settings.json — Claude settings with hook commands
//   - <stealthDir>/quality_gate.sh — quality gate shell script
func bootstrapStealthProject(projectRoot, oroHome string) error {
	hash, err := projectHash(projectRoot)
	if err != nil {
		return fmt.Errorf("compute project hash: %w", err)
	}
	stealthDir := filepath.Join(oroHome, "projects", "s-"+hash)

	// 1. Create stealth directory.
	if err := os.MkdirAll(stealthDir, 0o750); err != nil {
		return fmt.Errorf("create stealth dir: %w", err)
	}

	// 2. Write config.yaml with mode: stealth.
	configPath := filepath.Join(stealthDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("mode: stealth\n"), 0o600); err != nil { //nolint:gosec // stealth config: restrictive perms
		return fmt.Errorf("write config.yaml: %w", err)
	}

	// 3. Create beads/ directory.
	beadsDir := filepath.Join(stealthDir, "beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		return fmt.Errorf("create beads dir: %w", err)
	}

	// 4. Write settings.json.
	settingsData, err := generateSettings("$HOME/.oro")
	if err != nil {
		return fmt.Errorf("generate settings: %w", err)
	}
	settingsPath := filepath.Join(stealthDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsData, 0o644); err != nil { //nolint:gosec // settings file needs to be readable
		return fmt.Errorf("write settings.json: %w", err)
	}

	// 5. Write quality_gate.sh.
	// writeQualityGateScriptFile skips when no languages are detected, so we use
	// writeStealthQualityGate which writes the file unconditionally.
	stealthPaths := stealthProjectPaths(projectRoot, stealthDir)
	if err := writeStealthQualityGate(stealthPaths); err != nil {
		// Fail-open: quality gate is useful but not critical for bootstrap.
		fmt.Fprintf(os.Stderr, "warning: stealth quality gate generation failed: %v\n", err)
	}

	// 6. Install git hooks (pre-commit + pre-push) when .git dir is present.
	if err := installStealthGitHooks(projectRoot); err != nil {
		// Fail-open: hooks are useful but not critical for bootstrap.
		fmt.Fprintf(os.Stderr, "warning: stealth git hook installation failed: %v\n", err)
	}

	return nil
}

// installStealthGitHooks installs pre-commit and pre-push oro wrappers into
// projectRoot/.git/hooks (respecting core.hooksPath). It is a no-op when
// .git does not exist.
func installStealthGitHooks(projectRoot string) error {
	gitDir := filepath.Join(projectRoot, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil // no git repo — skip silently
	}

	if err := installHookWrapper(gitDir, "pre-commit", oroPreCommitCheck); err != nil {
		return fmt.Errorf("install pre-commit hook: %w", err)
	}
	if err := installHookWrapper(gitDir, "pre-push", oroPrePushCheck); err != nil {
		return fmt.Errorf("install pre-push hook: %w", err)
	}
	return nil
}

// writeStealthQualityGate writes quality_gate.sh to paths.QualityGate using an
// atomic tmp-rename. Unlike writeQualityGateScriptFile it does not skip when no
// language config is present — stealth dirs need the file regardless.
func writeStealthQualityGate(paths ProjectPaths) error {
	if err := os.MkdirAll(filepath.Dir(paths.QualityGate), 0o750); err != nil {
		return fmt.Errorf("create quality gate dir: %w", err)
	}
	tmp := paths.QualityGate + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755) //nolint:gosec // quality gate script must be executable
	if err != nil {
		return fmt.Errorf("open temp quality gate: %w", err)
	}
	if writeErr := writeQualityGateScript(f, paths); writeErr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("render quality gate: %w", writeErr)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp quality gate: %w", err)
	}
	if err := os.Rename(tmp, paths.QualityGate); err != nil {
		return fmt.Errorf("rename quality gate: %w", err)
	}
	return nil
}

// oroPreCommitCheck is the shell snippet injected into the pre-commit wrapper.
// It rejects any staged files under oro-docs/ to prevent accidental leakage in
// stealth mode.
const oroPreCommitCheck = `# oro check: reject staged oro-docs/ files
if git diff --cached --name-only | grep -q '^oro-docs/'; then
    echo "oro: staged files under oro-docs/ are not allowed in stealth mode" >&2
    exit 1
fi`

// oroPrePushCheck is the shell snippet injected into the pre-push wrapper.
// It blocks pushes of agent/* branches to prevent stealth-mode work-branches
// from appearing in the shared remote.
const oroPrePushCheck = `# oro check: block agent/* branches
while IFS= read -r line; do
    local_ref=$(echo "$line" | awk '{print $1}')
    case "$local_ref" in
        refs/heads/agent/*)
            echo "oro: pushing agent/* branches is not allowed in stealth mode" >&2
            exit 1
            ;;
    esac
done`
