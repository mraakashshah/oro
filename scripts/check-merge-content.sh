#!/usr/bin/env bash
set -euo pipefail

repo_root="${1:-$(git rev-parse --show-toplevel)}"
commit="${2:-HEAD}"

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	return 1
}

first_parent_tree() {
	git -C "$repo_root" rev-parse "$commit^1^{tree}"
}

merge_tree() {
	git -C "$repo_root" rev-parse "$commit^{tree}"
}

missing_added_files_from_parent() {
	local parent="$1" base path
	base=$(git -C "$repo_root" merge-base "$commit^1" "$parent") ||
		fail "cannot find merge base for $commit^1 and $parent"
	while IFS= read -r -d '' path; do
		if ! git -C "$repo_root" cat-file -e "$commit:$path" 2>/dev/null; then
			printf '%s\n' "$path"
		fi
	done < <(git -C "$repo_root" diff --name-only -z --diff-filter=A "$base" "$parent")
}

main() {
	local parents
	git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
		fail "not a Git worktree: $repo_root"
	parents=$(git -C "$repo_root" rev-list --parents -n 1 "$commit") ||
		fail "cannot resolve commit $commit"
	set -- $parents
	if [ "$#" -lt 3 ]; then
		return 0
	fi
	if [ "$(merge_tree)" != "$(first_parent_tree)" ]; then
		return 0
	fi

	local parent missing=0
	shift 2
	for parent in "$@"; do
		while IFS= read -r path; do
			[ -n "$path" ] || continue
			printf 'FAIL: merge %s has first-parent tree but drops %s from parent %s\n' \
				"$commit" "$path" "$parent" >&2
			missing=1
		done < <(missing_added_files_from_parent "$parent")
	done
	return "$missing"
}

main
