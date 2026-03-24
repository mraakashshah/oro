package main

import (
	"fmt"
	"os"
	"path/filepath"
)

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
