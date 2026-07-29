#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
readonly repo_root
readonly workflow_path="$repo_root/.github/workflows/ci.yml"

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	return 1
}

workflow_on_block() {
	awk '
		/^on:$/ { in_on = 1; next }
		in_on && /^jobs:$/ { exit }
		in_on { print }
	' "$workflow_path"
}

TestPullRequestTargets() {
	local on_block push_block pr_block
	on_block=$(workflow_on_block)

	printf '%s\n' "$on_block" | grep -Eq '^  pull_request:[[:space:]]*$' ||
		fail 'pull_request must be unfiltered so it covers main, custom targets, and epic/**'
	# A bare "  pull_request:" header is not proof of an unfiltered trigger: a
	# block-style filter on the following lines narrows it while leaving the
	# header bare. Extract the sub-block and reject any filter inside it, the
	# same way the push assertion below validates its own sub-block.
	pr_block=$(printf '%s\n' "$on_block" | awk '
		/^  pull_request:$/ { in_pr = 1; next }
		in_pr && /^  [[:alnum:]_]+:$/ { exit }
		in_pr { print }
	')
	if printf '%s\n' "$pr_block" | grep -Eq '^    (branches|branches-ignore|tags|tags-ignore|paths|paths-ignore):'; then
		fail 'pull_request must stay unfiltered (no branches/tags/paths restriction)'
	fi
	if printf '%s\n' "$on_block" | grep -Eq '^  pull_request_target:'; then
		fail 'pull_request_target must not be configured'
	fi

	push_block=$(printf '%s\n' "$on_block" | awk '
		/^  push:$/ { in_push = 1; next }
		in_push && /^  [[:alnum:]_]+:$/ { exit }
		in_push { print }
	')
	printf '%s\n' "$push_block" | grep -Eq '^    branches:[[:space:]]*\[[^]]+\][[:space:]]*$' ||
		fail 'push must retain an explicit target branch filter'
}

main() {
	local requested_test=${1:-}
	case "$requested_test" in
	'' | TestPullRequestTargets)
		TestPullRequestTargets
		;;
	*)
		fail "unknown test $requested_test"
		;;
	esac
}

main "$@"
