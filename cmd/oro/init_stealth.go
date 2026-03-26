package main

// oroPreCommitCheck is the shell snippet injected into the pre-commit wrapper.
// It rejects any staged files under oro-docs/ to prevent accidental leakage in
// stealth mode.
const oroPreCommitCheck = `# oro check: reject staged oro-docs/ files
if git diff --cached --name-only | grep -q '^oro-docs/'; then
    echo "oro: staged files under oro-docs/ are not allowed in stealth mode" >&2
    exit 1
fi`

// oroPrePushCheck is the shell snippet injected into the pre-push wrapper.
// It blocks pushes of agent/* and epic/* branches to prevent oro work-branches
// from appearing in the shared remote. Installed for ALL oro projects (not just stealth).
const oroPrePushCheck = `# oro check: block agent/* and epic/* branches
while IFS= read -r line; do
    local_ref=$(echo "$line" | awk '{print $1}')
    case "$local_ref" in
        refs/heads/agent/*|refs/heads/epic/*)
            echo "oro: pushing agent/* and epic/* branches is not allowed" >&2
            exit 1
            ;;
    esac
done`
