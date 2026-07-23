#!/usr/bin/env bash
set -euo pipefail

repo_root="${1:-$(git rev-parse --show-toplevel)}"
main_branch="${ORO_MAIN_BRANCH:-main}"
oro_bin="${ORO_BIN:-oro}"
pgrep_bin="${PGREP_BIN:-pgrep}"
snapshot_dir="${ORO_EPIC_SNAPSHOT_DIR:-$repo_root/.oro/epic-tip-snapshots}"

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 2
}

require_stopped_dispatcher() {
	local status
	if ! status=$("$oro_bin" status 2>&1); then
		fail "cannot determine dispatcher state: $status"
	fi
	if ! grep -Eq '^dispatcher:[[:space:]]+stopped\b' <<<"$status"; then
		fail "dispatcher is running or unavailable; stop it before merging epic refs"
	fi
	if "$pgrep_bin" -f '[q]uality_gate\.sh' >/dev/null 2>&1 ||
		"$pgrep_bin" -f '[o]ro.*worker' >/dev/null 2>&1; then
		fail "quality gate or worker process is still running"
	fi
}

epic_is_dead() {
	local epic="$1"
	local task_id="${epic#epic/}"
	local task child_tasks
	if ! task=$("$oro_bin" task show "$task_id" --json 2>/dev/null); then
		return 1
	fi
	if ! grep -Eq '"status"[[:space:]]*:[[:space:]]*"closed"' <<<"$task"; then
		return 1
	fi
	if ! child_tasks=$("$oro_bin" task list --parent "$task_id" --status open --json 2>/dev/null); then
		return 1
	fi
	! grep -Eq '"id"[[:space:]]*:' <<<"$child_tasks"
}

snapshot_epic_tips() {
	mkdir -p "$snapshot_dir"
	local snapshot
	snapshot=$(mktemp "$snapshot_dir/epic-tips.XXXXXX.txt")
	git -C "$repo_root" for-each-ref --format='%(refname:short) %(objectname)' \
		refs/heads/epic/ >"$snapshot"
	printf 'SNAPSHOT: %s\n' "$snapshot"
}

merge_main_into_epic() {
	local epic="$1"
	local main_sha old_sha ahead temp_root worktree merge_rc=0 merged_sha
	main_sha=$(git -C "$repo_root" rev-parse "$main_branch")
	old_sha=$(git -C "$repo_root" rev-parse "$epic")
	ahead=$(git -C "$repo_root" rev-list --count "$main_branch..$epic")

	if [ "$ahead" -eq 0 ]; then
		git -C "$repo_root" update-ref "refs/heads/$epic" "$main_sha" "$old_sha"
		printf 'FAST-FORWARD: %s -> %s\n' "$epic" "$main_branch"
		return 0
	fi

	temp_root=$(mktemp -d "${TMPDIR:-/tmp}/oro-merge-epic.XXXXXX")
	worktree="$temp_root/worktree"
	if ! git -C "$repo_root" worktree add --quiet --detach "$worktree" "$old_sha"; then
		rmdir "$temp_root" || true
		printf 'ERROR: unable to prepare merge worktree for %s\n' "$epic" >&2
		return 1
	fi

	git -C "$worktree" merge --no-edit "$main_branch" >/dev/null 2>&1 || merge_rc=$?
	if [ "$merge_rc" -ne 0 ]; then
		git -C "$worktree" merge --abort >/dev/null 2>&1 || true
		git -C "$repo_root" worktree remove --force "$worktree" >/dev/null 2>&1 || true
		rmdir "$temp_root" || true
		printf 'CONFLICT: %s; merge aborted and ref left unchanged\n' "$epic" >&2
		return 1
	fi

	merged_sha=$(git -C "$worktree" rev-parse HEAD)
	git -C "$repo_root" update-ref "refs/heads/$epic" "$merged_sha" "$old_sha"
	git -C "$repo_root" worktree remove --force "$worktree" >/dev/null 2>&1 || true
	rmdir "$temp_root" || true
	printf 'MERGED: %s <- %s\n' "$epic" "$main_branch"
}

main() {
	git -C "$repo_root" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
		fail "not a Git worktree: $repo_root"
	git -C "$repo_root" rev-parse --verify "$main_branch^{commit}" >/dev/null 2>&1 ||
		fail "main branch does not exist: $main_branch"
	require_stopped_dispatcher
	snapshot_epic_tips

	local epic failures=0
	while IFS= read -r epic; do
		[ -n "$epic" ] || continue
		if epic_is_dead "$epic"; then
			printf 'SKIP: %s (dead)\n' "$epic"
			continue
		fi
		merge_main_into_epic "$epic" || failures=$((failures + 1))
	done < <(git -C "$repo_root" for-each-ref --format='%(refname:short)' refs/heads/epic/)

	if [ "$failures" -ne 0 ]; then
		printf 'FAIL: %d epic merge(s) conflicted or failed\n' "$failures" >&2
		return 1
	fi
}

main "$@"
