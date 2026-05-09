#!/usr/bin/env bash
# Enforcement gate: rejects agent-specific phrasing in docs/prompts.
# Skips lines inside compat sections (between begin-compat / end-compat markers).
set -euo pipefail

# Split across variables so this file does not trigger itself.
_pfx="Claude"
_sfx=" workers"
_pattern="${_pfx}${_sfx}"

bad=0

check_file() {
	local file="$1"
	# Strip lines inside compat sections, then grep remaining content.
	local filtered
	filtered=$(awk '
		/begin-compat/ { skip=1; next }
		/end-compat/   { skip=0; next }
		!skip          { print }
	' "$file")
	if echo "$filtered" | grep -qi "$_pattern"; then
		printf 'phrasing: %s: use agent-agnostic phrasing instead of "%s"\n' \
			"$file" "$_pattern" >&2
		return 1
	fi
	return 0
}

# self-check: run check against this script's own source.
if [ $# -eq 1 ] && [ "$1" = "self-check" ]; then
	check_file "${BASH_SOURCE[0]}" || bad=1
	exit "$bad"
fi

if [ $# -eq 0 ]; then
	printf 'usage: %s <file>...\n' "$0" >&2
	exit 1
fi

for file in "$@"; do
	[ -e "$file" ] || continue
	check_file "$file" || bad=1
done

exit "$bad"
