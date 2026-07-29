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
	local on_block push_block
	on_block=$(workflow_on_block)

	printf '%s\n' "$on_block" | grep -Eq '^  pull_request:[[:space:]]*$' ||
		fail 'pull_request must be unfiltered so it covers main, custom targets, and epic/**'
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
