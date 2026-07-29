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

workflow_job_names() {
	awk '
		/^jobs:$/ { in_jobs = 1; next }
		in_jobs && /^[^[:space:]]/ { exit }
		in_jobs && /^  [a-z0-9][a-z0-9-]*:$/ {
			job = $1
			sub(/:$/, "", job)
			print job
		}
	' "$workflow_path"
}

portable_job_names() {
	workflow_job_names | grep -vx 'oro-portable-qg'
}

portable_qg_needs() {
	awk '
		/^  oro-portable-qg:$/ { in_aggregate = 1; next }
		in_aggregate && /^  [a-z0-9][a-z0-9-]*:$/ { exit }
		in_aggregate && /^    needs: / {
			needs = $0
			sub(/^    needs: \[/, "", needs)
			sub(/\]$/, "", needs)
			gsub(/, /, "\n", needs)
			print needs
		}
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

TestPortableQGAggregate() {
	local expected_jobs actual_needs actual_required needs_json successful_needs status
	expected_jobs=$(portable_job_names | sort)
	actual_needs=$(portable_qg_needs | sort)
	actual_required=$(sed -n 's/^readonly required_jobs=(\(.*\))$/\1/p' \
		"$repo_root/scripts/ci/require-needs-success.sh" | tr ' ' '\n' | sort)

	[[ $(grep -Ec '^  oro-portable-qg:$' "$workflow_path") -eq 1 ]] ||
		fail 'oro-portable-qg must be defined exactly once'
	awk '
		/^  oro-portable-qg:$/ { in_aggregate = 1; next }
		in_aggregate && /^  [a-z0-9][a-z0-9-]*:$/ { exit }
		in_aggregate && /^    if: \$\{\{ always\(\) \}\}$/ { found = 1 }
		END { exit !found }
	' "$workflow_path" || fail 'oro-portable-qg must use always()'
	[[ "$actual_needs" == "$expected_jobs" ]] ||
		fail 'oro-portable-qg needs must include every portable job exactly once'
	[[ "$actual_required" == "$expected_jobs" ]] ||
		fail 'require-needs-success must require every portable job exactly once'

	successful_needs='{"go":{"result":"success"},"cgo-free":{"result":"success"},"shell":{"result":"success"},"docs":{"result":"success"},"python":{"result":"success"}}'
	for status in skipped cancelled timed_out action_required; do
		needs_json=$(printf '%s\n' "$successful_needs" | jq --arg status "$status" '.go.result = $status')
		if "$repo_root/scripts/ci/require-needs-success.sh" "$needs_json" >/dev/null 2>&1; then
			fail "require-needs-success accepted $status dependency"
		fi
	done
	if "$repo_root/scripts/ci/require-needs-success.sh" '{}' >/dev/null 2>&1; then
		fail 'require-needs-success accepted missing dependencies'
	fi
}

main() {
	local requested_test=${1:-}
	case "$requested_test" in
	'' | TestPullRequestTargets)
		TestPullRequestTargets
		;;
	TestPortableQGAggregate)
		TestPortableQGAggregate
		;;
	*)
		fail "unknown test $requested_test"
		;;
	esac
}

main "$@"
