package main

import "strings"

// oroPreCommitCheck is the shell snippet injected into the pre-commit wrapper.
// It rejects any staged files under oro-docs/ to prevent accidental leakage in
// stealth mode.
const oroPreCommitCheck = `# oro check: reject staged oro-docs/ files
if git diff --cached --name-only | grep -q '^oro-docs/'; then
    echo "oro: staged files under oro-docs/ are not allowed in stealth mode" >&2
    exit 1
fi`

const oroPrePushCheckPrefix = `# oro check: block agent/* and epic/* branches
while IFS= read -r line; do
    local_ref=$(echo "$line" | awk '{print $1}')
    case "$local_ref" in
        refs/heads/agent/*|refs/heads/epic/*)
            echo "oro: pushing agent/* and epic/* branches is not allowed" >&2
            exit 1
            ;;
    esac
done

# Run Oro's full quality gate before push. Mutation testing remains disabled
# unless the quality gate is run separately with --mutation-testing.
if [ "${ORO_PRE_PUSH_QG:-1}" != "0" ]; then
    oro_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
    oro_qg=`

const oroPrePushCheckSuffix = `
    if [ -x "$oro_root/scripts/quality_gate.sh" ]; then
        echo "oro: running quality gate (mutation testing disabled by default)" >&2
        ORO_QG_CONTEXT=push oro storage exec --workdir "$oro_root" -- "$oro_root/scripts/quality_gate.sh" || exit $?
    elif [ -x "$oro_qg" ]; then
        echo "oro: running quality gate (mutation testing disabled by default)" >&2
        ORO_QG_CONTEXT=push oro storage exec --workdir "$oro_root" -- "$oro_qg" || exit $?
    fi
fi`

// oroPrePushCheck is the default shell snippet injected into the pre-push
// wrapper by tests and fallback hook installs.
const oroPrePushCheck = oroPrePushCheckPrefix + `"$oro_root/scripts/quality_gate.sh"` + oroPrePushCheckSuffix

// buildOroPrePushCheck returns the shell snippet injected into the pre-push wrapper.
// It blocks pushes of agent/* and epic/* branches to prevent oro work-branches
// from appearing in the shared remote, then runs QG in push context without
// enabling mutation testing by default.
// Installed for ALL oro projects (not just stealth).
func buildOroPrePushCheck(qualityGatePath string) string {
	if qualityGatePath == "" {
		return oroPrePushCheck
	}
	return oroPrePushCheckPrefix + shellSingleQuote(qualityGatePath) + oroPrePushCheckSuffix
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
