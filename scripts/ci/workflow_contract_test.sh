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

workflow_job_block() {
	local job="$1"
	awk -v job="$job" '
		$0 == "  " job ":" { in_job = 1 }
		in_job && /^  [a-z0-9][a-z0-9-]*:$/ && $1 != job ":" { exit }
		in_job { print }
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

	successful_needs='{"go":{"result":"success"},"cgo-free":{"result":"success"},"shell":{"result":"success"},"docs":{"result":"success"},"python":{"result":"success"},"incremental-mutation":{"result":"success"},"qg-stress":{"result":"success"}}'
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

TestIncrementalMutationCheckoutMatchesHead() {
	local mutation_job
	mutation_job=$(awk '
		/^  incremental-mutation:$/ { in_job = 1 }
		in_job && /^  [a-z0-9][a-z0-9-]*:$/ && $1 != "incremental-mutation:" { exit }
		in_job { print }
	' "$workflow_path")
	[[ -n "$mutation_job" ]] || fail 'workflow must define an incremental-mutation job'

	[[ $(printf '%s\n' "$mutation_job" | grep -Fxc '      - uses: actions/checkout@v4') -eq 1 ]] ||
		fail 'incremental-mutation must use checkout exactly once'
	[[ $(printf '%s\n' "$mutation_job" | grep -Fxc '          fetch-depth: 0') -eq 1 ]] ||
		fail 'incremental-mutation checkout must preserve full history'
	# These are literal GitHub expressions, not shell expansions.
	# shellcheck disable=SC2016
	[[ $(printf '%s\n' "$mutation_job" | grep -Fxc '          ref: ${{ github.sha }}') -eq 1 ]] ||
		fail 'incremental-mutation checkout must use the exact mutation head'
	# shellcheck disable=SC2016
	printf '%s\n' "$mutation_job" | grep -Fq '          base_sha="${{ github.event.pull_request.base.sha || github.event.before }}"' ||
		fail 'incremental-mutation must preserve pull-request and push base selection'
	# shellcheck disable=SC2016
	printf '%s\n' "$mutation_job" | grep -Fq -- '            --base "$base_sha"' ||
		fail 'incremental-mutation must pass the selected base'
	# shellcheck disable=SC2016
	[[ $(printf '%s\n' "$mutation_job" | grep -Fc '            --head "${{ github.sha }}"') -eq 1 ]] ||
		fail 'incremental-mutation must pass the exact checkout head'
	# shellcheck disable=SC2016
	if printf '%s\n' "$mutation_job" | grep -Fq 'github.event.pull_request.merge_commit_sha'; then
		fail 'incremental-mutation must not use a stale pull-request payload merge SHA'
	fi
}

TestGoCacheRestoresBeforeSetup() {
	local job job_block initialize_line cache_line setup_line
	for job in incremental-mutation go cgo-free qg-stress; do
		job_block=$(workflow_job_block "$job")
		[[ -n "$job_block" ]] || fail "workflow must define the $job Go job"

		# The cache must restore before setup-go invokes `go env`. With a toolchain
		# directive, setup-go otherwise populates GOMODCACHE before trying to
		# extract a cache archive into it.
		# shellcheck disable=SC2016
		for required in \
			'      GOMODCACHE: ${{ runner.temp }}/oro-go-cache/go-mod' \
			'      GOCACHE: ${{ runner.temp }}/oro-go-cache/go-build' \
			'      GOTMPDIR: ${{ runner.temp }}/oro-go-cache/go-tmp' \
			'      - uses: actions/cache@v4' \
			'            ${{ env.GOMODCACHE }}' \
			'            ${{ env.GOCACHE }}' \
			'      - uses: actions/setup-go@v5' \
			'          cache: false'; do
			printf '%s\n' "$job_block" | grep -Fqx -- "$required" ||
				fail "$job must restore an explicit job-isolated Go cache before setup-go: $required"
		done

		# The quoted workflow command is intentionally matched literally.
		# shellcheck disable=SC2016
		initialize_line=$(printf '%s\n' "$job_block" | grep -nF 'mkdir -p "$GOMODCACHE" "$GOCACHE" "$GOTMPDIR"' | cut -d: -f1)
		cache_line=$(printf '%s\n' "$job_block" | grep -nF '      - uses: actions/cache@v4' | cut -d: -f1)
		setup_line=$(printf '%s\n' "$job_block" | grep -nF '      - uses: actions/setup-go@v5' | cut -d: -f1)
		[[ -n "$initialize_line" && -n "$cache_line" && -n "$setup_line" ]] ||
			fail "$job must initialize, restore, and configure its Go cache"
		((initialize_line < cache_line && cache_line < setup_line)) ||
			fail "$job must restore into empty cache roots before setup-go resolves the toolchain"
	done
}

TestGoDispatcherIsolation() {
	local go_job
	go_job=$(workflow_job_block go)
	[[ -n "$go_job" ]] || fail 'workflow must define the Go job'

	# These workflow shell expressions are intentionally matched literally.
	# shellcheck disable=SC2016
	for required in \
		'mapfile -t TEST_PKGS < <(go list ./internal/... ./pkg/... ./cmd/... | grep -vx '\''oro/pkg/dispatcher'\'')' \
		'go test -race -shuffle=on "${TEST_PKGS[@]}"' \
		'go test -race -shuffle=on -p 1 ./pkg/dispatcher' \
		'mapfile -t COVERAGE_PKGS < <(go list ./internal/... ./pkg/... | grep -v '\''pkg/dashboard/'\'' | grep -vx '\''oro/pkg/dispatcher'\'')' \
		'go test -race -shuffle=on -coverprofile=coverage-other.out "${COVERAGE_PKGS[@]}"' \
		'go test -race -shuffle=on -p 1 -coverprofile=coverage-dispatcher.out ./pkg/dispatcher' \
		'test "$(head -n 1 coverage-other.out)" = "mode: atomic"' \
		'test "$(head -n 1 coverage-dispatcher.out)" = "mode: atomic"' \
		'tail -n +2 coverage-dispatcher.out' \
		'} >coverage.out'; do
		printf '%s\n' "$go_job" | grep -Fq -- "$required" ||
			fail "Go Test + Coverage must isolate dispatcher without weakening race, shuffle, or coverage: $required"
	done

	if printf '%s\n' "$go_job" | grep -Fq 'go test -race -shuffle=on ./internal/... ./pkg/... ./cmd/...'; then
		fail 'Go correctness must not couple dispatcher to the broad concurrent package invocation'
	fi
}

TestExplicitQGStressLane() {
	local stress_job ordinary_jobs
	stress_job=$(awk '
		/^  qg-stress:$/ { in_job = 1 }
		in_job && /^  [a-z0-9][a-z0-9-]*:$/ && $1 != "qg-stress:" { exit }
		in_job { print }
	' "$workflow_path")
	[[ -n "$stress_job" ]] || fail 'workflow must define an explicit qg-stress job'
	printf '%s\n' "$stress_job" | grep -q '^    needs: \[go, cgo-free, shell, docs, python, incremental-mutation\]$' ||
		fail 'qg-stress must wait for every ordinary portable job before consuming stress resources'
	# These are literal GitHub-expression contracts, not shell expansions.
	# shellcheck disable=SC2016
	for required in \
		'ORO_QG_STRESS_LANE: "1"' \
		'GOCACHE: ${{ runner.temp }}/oro-go-cache/go-build' \
		'GOTMPDIR: ${{ runner.temp }}/oro-go-cache/go-tmp' \
		"-run '^TestConcurrentGatesNoTimingFlakeSerialLaneCatchesRegression\$'" \
		'-count=1' \
		'-p 1' \
		'-timeout=10m' \
		'if: always()' \
		'if-no-files-found: error' \
		'qg-stress-evidence'; do
		printf '%s\n' "$stress_job" | grep -Fq -- "$required" ||
			fail "qg-stress job is missing: $required"
	done

	ordinary_jobs=$(awk '
		/^  go:$/ || /^  cgo-free:$/ { in_job = 1 }
		in_job && /^  [a-z0-9][a-z0-9-]*:$/ && $1 != "go:" && $1 != "cgo-free:" { in_job = 0 }
		in_job { print }
	' "$workflow_path")
	[[ $(printf '%s\n' "$ordinary_jobs" | grep -c 'ORO_QG_STRESS_LANE: "0"') -eq 2 ]] ||
		fail 'ordinary Go and CGO-free jobs must explicitly disable nested QG stress tests'
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
	TestIncrementalMutationCheckoutMatchesHead)
		TestIncrementalMutationCheckoutMatchesHead
		;;
	TestGoCacheRestoresBeforeSetup)
		TestGoCacheRestoresBeforeSetup
		;;
	TestGoDispatcherIsolation)
		TestGoDispatcherIsolation
		;;
	TestExplicitQGStressLane)
		TestExplicitQGStressLane
		;;
	*)
		fail "unknown test $requested_test"
		;;
	esac
}

main "$@"
