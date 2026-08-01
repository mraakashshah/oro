#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

bad=0

require_readme_glossary() {
	local readme="README.md"
	local missing=0
	for pattern in \
		'^### Task Terminology$' \
		'\*\*Task:\*\* an Oro work item' \
		"\\*\\*Task type:\\*\\* the \`type\` field"; do
		if ! rg -q "$pattern" "$readme"; then
			printf 'terminology: README glossary missing pattern: %s\n' "$pattern" >&2
			missing=1
		fi
	done
	return "$missing"
}

scan_files() {
	local files=("$@")
	local normal_commands='create|show|update|close|reopen|defer|undefer|list|status|ready|blocked|closed|dep|deps|tag|meta|note|comment|search|export|import|doctor|work'
	local public_bead_cli_regex='\boro bead\b'
	local command_regex='\boro bead[[:space:]]+('"$normal_commands"')\b'
	local shell_command_regex='(?s)(^|[[:space:]])["'\'']?\$[A-Za-z_][A-Za-z0-9_]*["'\'']?[[:space:]]+bead[[:space:]]+('"$normal_commands"')\b'
	local argv_command_regex='(?s)["'\'']bead["'\''][[:space:]]*,[[:space:]]*["'\'']('"$normal_commands"')["'\'']'
	local docs_regex="native \`oro bead\`|primary .*oro bead|work items through \`oro bead\`|tracked by the native \`oro bead\` CLI"
	local primary_term_regex='one bead|execute beads|assigns beads|assign new beads|prioritize beads|requeue its bead|work is tracked as beads|all beads are visible|Manage beads|Bead in progress|continuation bead|bead queue|bead progress|bead completion|per-bead|bead dependency graph|Bead Anatomy|No beads ready|Stale Beads|Same bead|P0 bead|create a bead|blocker bead|test bead assigned|worker proof beads|controlled test bead|smoke bead|restart beads|ready bead|worker bead|bead has no AC|worker and bead are stuck|fix beads|child beads|smaller child beads|bead metadata export|export bead metadata|Diagnose why bead|Search beads|Import bead snapshot|Beads In Progress|BEAD CRAFT|SPEC/BEAD|Beads CLI'
	local false_rename_regex='task/(abc|def|ghi|jkl)\b|\.worktrees/task|task/<id>|task branch'
	local junk_smoke_regex='oro task create[^`'\''"]*--title[=[:space:]]t\b|oro task create[^`'\''"]*--description[=[:space:]]d\b|oro task create[^`'\''"]*--acceptance-criteria[=[:space:]]ac\b'

	if ((${#files[@]} == 0)); then
		return 0
	fi

	if rg -n -U --pcre2 "$command_regex" "${files[@]}"; then
		printf "terminology: public docs/prompts must use \`oro task\` for work-item operations.\n" >&2
		bad=1
	fi
	if rg -n -U --pcre2 "$shell_command_regex" "${files[@]}"; then
		printf "terminology: active scripts must not invoke normal work-item operations through shell-split \`oro bead\`; use \`oro task\`.\n" >&2
		bad=1
	fi
	if rg -n -U --pcre2 "$public_bead_cli_regex" "${files[@]}"; then
		printf "terminology: public CLI copy must not expose \`oro bead\` as a normal workflow; bead wording needs an explicit internal, archive, storage, compatibility, or migration reason.\n" >&2
		bad=1
	fi
	if rg -n -U --pcre2 "$argv_command_regex" "${files[@]}" | rg -v '(^|/)(architect_router|notify_manager_on_bead_create|bd_create_notifier|pre_compact)\.py:'; then
		printf "terminology: active hooks/prompts must not invoke work-item operations through argv-form \`oro bead\`; use \`oro task\`.\n" >&2
		bad=1
	fi
	if rg -n --pcre2 "$docs_regex" "${files[@]}"; then
		printf "terminology: public docs/prompts must describe \`oro task\` as the normal work-item CLI.\n" >&2
		bad=1
	fi
	if rg -n --pcre2 "$primary_term_regex" "${files[@]}"; then
		printf 'terminology: active docs/prompts must use task as the primary work-item noun; bead is reserved for legacy/internal/storage/migration contexts.\n' >&2
		bad=1
	fi
	if rg -n --pcre2 "$false_rename_regex" "${files[@]}"; then
		printf 'terminology: do not invent task-prefixed git/worktree names; preserve real branch/worktree conventions such as agent/<id> or legacy/internal bead/<id> examples.\n' >&2
		bad=1
	fi
	if rg -n -U --pcre2 "$junk_smoke_regex" "${files[@]}"; then
		printf 'terminology: public smoke-test guidance must use isolated temp DBs and descriptive task fields, not copy-pastable --title=t/--description=d/--acceptance-criteria=ac placeholders.\n' >&2
		bad=1
	fi
}

find_text_files() {
	local dir
	for dir in "$@"; do
		[ -d "$dir" ] || continue
		find "$dir" -type f \( -name '*.md' -o -name '*.go' -o -name '*.sh' -o -name '*.py' \)
	done
}

find_hook_files() {
	local dir
	for dir in "$@"; do
		[ -d "$dir" ] || continue
		find "$dir" -type f \( -name '*.py' -o -name '*.sh' \) ! -name 'test_*'
	done
}

find_skill_files() {
	local dir
	for dir in "$@"; do
		[ -d "$dir" ] || continue
		find "$dir" -type f \( -name '*.md' -o -name '*.go' -o -name '*.sh' -o -name '*.py' -o ! -name '*.*' \)
	done
}

find_embedded_files() {
	local dir
	for dir in "$@"; do
		[ -d "$dir" ] || continue
		find "$dir" -type f \( -name '*.py' -o -name '*.sh' -o -name '*.md' -o ! -name '*.*' \) ! -name 'test_*'
	done
}

scan_runtime_command_comments() {
	local candidates=("$@")
	local files=()
	local normal_commands='create|show|update|close|reopen|defer|undefer|list|status|ready|blocked|closed|dep|deps|tag|meta|note|comment|search|export|import|doctor|work'
	local command_regex='\boro bead[[:space:]]+('"$normal_commands"')\b'
	local stale_runtime_comment_regex='SessionStart hooks \(bd list, bd ready|oro bead commands?'

	local file
	for file in "${candidates[@]}"; do
		[ -e "$file" ] || continue
		files+=("$file")
	done

	if ((${#files[@]} == 0)); then
		return 0
	fi
	if rg -n -U --pcre2 "$command_regex" "${files[@]}"; then
		printf "terminology: current runtime comments must use \`oro task\` for work-item command paths.\n" >&2
		bad=1
	fi
	if rg -n --pcre2 "$stale_runtime_comment_regex" "${files[@]}"; then
		printf 'terminology: current runtime comments must describe SessionStart task commands, not legacy bd commands.\n' >&2
		bad=1
	fi
}

default_files=()
while IFS= read -r file; do
	default_files+=("$file")
done < <(
	{
		printf '%s\n' README.md docs/INSTALL.md assets/review-patterns.md assets/CLAUDE.md assets/ORO_AGENT.md
		printf '%s\n' scripts/check-phase10-no-bd-install.sh
		find_text_files docs/runbooks assets/beacons assets/commands .claude/commands .claude/hooks/beacons
		find_skill_files assets/skills .claude/skills
		find_hook_files assets/hooks .claude/hooks
		find_embedded_files cmd/oro/_assets/beacons cmd/oro/_assets/commands cmd/oro/_assets/hooks cmd/oro/_assets/skills
		printf '%s\n' cmd/oro/_assets/CLAUDE.md cmd/oro/_assets/ORO_AGENT.md
		printf '%s\n' pkg/worker/prompt.go cmd/oro/architect.go cmd/oro/manager.go cmd/oro/root.go cmd/oro/cmd_help.go cmd/oro/cmd_directive.go cmd/oro/cmd_task.go pkg/ops/ops.go
		find pkg/ops -maxdepth 1 -type f -name '*prompt.go' 2>/dev/null
	} | sort -u
)

files=("${default_files[@]}")
if (($# > 0)); then
	files+=("$@")
fi
existing_files=()
for file in "${files[@]}"; do
	[ -e "$file" ] || continue
	existing_files+=("$file")
done
files=("${existing_files[@]}")
runtime_comment_files=(cmd/oro/tmux.go)
while IFS= read -r file; do
	runtime_comment_files+=("$file")
done < <(find pkg/dispatcher -type f -name '*.go' ! -name '*_test.go')
generic_files=()
for file in "${files[@]}"; do
	normalized_file="${file#./}"
	case "$normalized_file" in
	cmd/oro/tmux.go)
		continue
		;;
	pkg/dispatcher/*.go)
		case "$normalized_file" in
		*_test.go) ;;
		*) continue ;;
		esac
		;;
	esac
	generic_files+=("$file")
done

if ! require_readme_glossary; then
	bad=1
fi
scan_files "${generic_files[@]}"
scan_runtime_command_comments "${runtime_comment_files[@]}"

exit "$bad"
