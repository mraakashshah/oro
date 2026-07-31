#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/check-merge-content.sh"

fixture=""

cleanup() {
	if [ -n "$fixture" ] && [ -d "$fixture" ]; then
		rm -rf "$fixture"
	fi
}

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	return 1
}

make_fixture() {
	fixture=$(mktemp -d)
	git -C "$fixture" init --quiet --initial-branch=main
	git -C "$fixture" config user.name fixture
	git -C "$fixture" config user.email fixture@example.invalid
	printf 'base\n' >"$fixture/base.txt"
	git -C "$fixture" add base.txt
	git -C "$fixture" commit --quiet -m base

	git -C "$fixture" switch --quiet -c epic
	printf 'epic\n' >"$fixture/epic-only.txt"
	git -C "$fixture" add epic-only.txt
	git -C "$fixture" commit --quiet -m epic

	git -C "$fixture" switch --quiet main
	printf 'main\n' >"$fixture/main-only.txt"
	git -C "$fixture" add main-only.txt
	git -C "$fixture" commit --quiet -m main
}

test_rejects_tree_neutral_merge_that_drops_second_parent_file() {
	make_fixture
	git -C "$fixture" merge --quiet --no-ff -s ours epic -m 'bad preserve merge'
	local output
	if output=$("$CHECK" "$fixture" HEAD 2>&1); then
		fail "checker accepted a tree-neutral merge that dropped epic-only.txt"
		return
	fi
	printf '%s\n' "$output" | grep -Fq 'epic-only.txt' ||
		fail "checker did not report the dropped second-parent file: $output"
}

test_accepts_normal_content_merge() {
	make_fixture
	git -C "$fixture" merge --quiet --no-ff epic -m 'normal content merge'
	"$CHECK" "$fixture" HEAD || fail 'checker rejected a normal content merge'
}

trap cleanup EXIT

test_rejects_tree_neutral_merge_that_drops_second_parent_file
test_accepts_normal_content_merge
printf 'PASS: merge content checks\n'
