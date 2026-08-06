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

TestIncrementalMutationCapacityDeadline() {
	local expected_timeout_minutes file_timeout_seconds incident_shard_count
	local mutation_job observed_frontier observed_window_minutes runner parallel_runner
	local timeout_margin_minutes timeout_minutes workers
	mutation_job=$(workflow_job_block incremental-mutation)
	[[ -n "$mutation_job" ]] || fail 'workflow must define an incremental-mutation job'

	# Three consecutive hosted runs reached only shards 58-59 of this
	# 117-shard incident campaign before the 60-minute job deadline cancelled
	# them. Size the outer boundary from the conservative observed frontier and
	# retain 28 minutes for shard-cost variance and final evidence aggregation.
	incident_shard_count=117
	observed_frontier=58
	observed_window_minutes=60
	timeout_margin_minutes=28
	expected_timeout_minutes=$(((
		incident_shard_count * observed_window_minutes + observed_frontier - 1
	) / observed_frontier + timeout_margin_minutes))

	timeout_minutes=$(printf '%s\n' "$mutation_job" |
		awk '/^    timeout-minutes:/ { print $2; exit }')
	workers=$(printf '%s\n' "$mutation_job" |
		awk '/MUTATION_MAX_WORKERS:/ { print $2; exit }')
	file_timeout_seconds=$(printf '%s\n' "$mutation_job" |
		awk '/MUTATION_FILE_TIMEOUT_SECONDS:/ { print $2; exit }')
	[[ "$timeout_minutes" == "$expected_timeout_minutes" ]] ||
		fail "incremental-mutation timeout = ${timeout_minutes:-missing}, want $expected_timeout_minutes for the observed 117-shard campaign"
	[[ "$workers" == 2 ]] || fail 'incremental-mutation must retain two hosted-runner workers'
	[[ "$file_timeout_seconds" == 240 ]] || fail 'incremental-mutation must retain its 240-second shard boundary'

	# These are literal workflow shell and GitHub expressions.
	# shellcheck disable=SC2016
	for required in \
		'bash scripts/quality_gate/mutation.sh' \
		'--base "$base_sha"' \
		'--head "${{ github.sha }}"' \
		'            --evidence mutation-evidence.json' \
		'      - name: Initialize isolated Go cache roots' \
		'          cache_root="$RUNNER_TEMP/oro-go-cache"' \
		'            printf '\''GOMODCACHE=%s/go-mod\n'\'' "$cache_root"' \
		'            printf '\''GOCACHE=%s/go-build\n'\'' "$cache_root"' \
		'            printf '\''GOTMPDIR=%s/go-tmp\n'\'' "$cache_root"' \
		'      - name: Upload incremental mutation evidence' \
		'        if: ${{ always() }}' \
		'          name: incremental-mutation-evidence' \
		'            mutation-evidence.json' \
		'            mutation-failures/' \
		'          if-no-files-found: error'; do
		printf '%s\n' "$mutation_job" | grep -Fq -- "$required" ||
			fail "incremental-mutation capacity change must preserve: $required"
	done

	runner="$repo_root/scripts/quality_gate/mutation.sh"
	parallel_runner="$repo_root/scripts/quality_gate/mutation_parallel.sh"
	# These are literal runner defaults, not expansions in this contract test.
	# shellcheck disable=SC2016
	for required in \
		'local exec_timeout=${MUTATION_EXEC_TIMEOUT_SECONDS:-60}' \
		'local max_shard_timeout=${MUTATION_MAX_SHARD_TIMEOUT_SECONDS:-900}'; do
		grep -Fq -- "$required" "$runner" ||
			fail "strict mutation runner must retain its fail-closed limit: $required"
	done
	# These are literal runner defaults, not expansions in this contract test.
	# shellcheck disable=SC2016
	for required in \
		': "${MUTATION_TEST_TIMEOUT_MARGIN_SECONDS:=5}"' \
		': "${MUTATION_BASE_SHARD_TIMEOUT_SECONDS:=240}"' \
		': "${MUTATION_MAX_SHARD_TIMEOUT_SECONDS:=900}"' \
		'124) exit_class=timeout ;;' \
		'*) exit_class=infrastructure ;;'; do
		grep -Fq -- "$required" "$parallel_runner" ||
			fail "parallel mutation runner must retain its fail-closed limit: $required"
	done

	TestGoCacheRestoresBeforeSetup
	TestExplicitQGStressLane
	TestPortableQGAggregate
}

TestGoCacheRestoresBeforeSetup() {
	local job job_block job_env initialize_line cache_line setup_line
	for job in incremental-mutation go cgo-free qg-stress; do
		job_block=$(workflow_job_block "$job")
		[[ -n "$job_block" ]] || fail "workflow must define the $job Go job"
		job_env=$(printf '%s\n' "$job_block" | awk '
			/^    env:$/ { in_env = 1; next }
			in_env && /^    [^ ]/ { exit }
			in_env { print }
		')
		# The GitHub expression is intentionally matched literally.
		# shellcheck disable=SC2016
		if printf '%s\n' "$job_env" | grep -Fq '${{ runner.'; then
			fail "$job job-level env must not use the runner context before a runner exists"
		fi

		# Resolve RUNNER_TEMP on the runner and export the roots through GITHUB_ENV.
		# Job-level env cannot use the runner context because GitHub validates that
		# mapping before assigning a runner. The cache must still restore before
		# setup-go invokes `go env` and populates GOMODCACHE.
		# shellcheck disable=SC2016
		for required in \
			'      - name: Initialize isolated Go cache roots' \
			'          cache_root="$RUNNER_TEMP/oro-go-cache"' \
			'            printf '\''GOMODCACHE=%s/go-mod\n'\'' "$cache_root"' \
			'            printf '\''GOCACHE=%s/go-build\n'\'' "$cache_root"' \
			'            printf '\''GOTMPDIR=%s/go-tmp\n'\'' "$cache_root"' \
			'          } >>"$GITHUB_ENV"' \
			'          mkdir -p "$cache_root/go-mod" "$cache_root/go-build" "$cache_root/go-tmp"' \
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
		initialize_line=$(printf '%s\n' "$job_block" | grep -nF 'cache_root="$RUNNER_TEMP/oro-go-cache"' | cut -d: -f1)
		cache_line=$(printf '%s\n' "$job_block" | grep -nF '      - uses: actions/cache@v4' | cut -d: -f1)
		setup_line=$(printf '%s\n' "$job_block" | grep -nF '      - uses: actions/setup-go@v5' | cut -d: -f1)
		[[ -n "$initialize_line" && -n "$cache_line" && -n "$setup_line" ]] ||
			fail "$job must initialize, restore, and configure its Go cache"
		((initialize_line < cache_line && cache_line < setup_line)) ||
			fail "$job must restore into empty cache roots before setup-go resolves the toolchain"
	done
}

TestGoDispatcherIsolation() {
	local dispatcher_commands go_job
	go_job=$(workflow_job_block go)
	[[ -n "$go_job" ]] || fail 'workflow must define the Go job'

	# These workflow shell expressions are intentionally matched literally.
	# shellcheck disable=SC2016
	for required in \
		'mapfile -t TEST_PKGS < <(go list ./internal/... ./pkg/... ./cmd/... | grep -vx '\''oro/pkg/dispatcher'\'')' \
		'go test -race -shuffle=on "${TEST_PKGS[@]}"' \
		'go test -race -shuffle=on -p 1 -timeout=20m ./pkg/dispatcher' \
		'mapfile -t COVERAGE_PKGS < <(go list ./internal/... ./pkg/... | grep -v '\''pkg/dashboard/'\'' | grep -vx '\''oro/pkg/dispatcher'\'')' \
		'go test -race -shuffle=on -coverprofile=coverage-other.out "${COVERAGE_PKGS[@]}"' \
		'go test -race -shuffle=on -p 1 -timeout=20m -coverprofile=coverage-dispatcher.out ./pkg/dispatcher' \
		'test "$(head -n 1 coverage-other.out)" = "mode: atomic"' \
		'test "$(head -n 1 coverage-dispatcher.out)" = "mode: atomic"' \
		'tail -n +2 coverage-dispatcher.out' \
		'} >coverage.out' \
		'COVERAGE=$(go tool cover -func=coverage.out | grep total | awk '\''{print $3}'\'' | sed '\''s/%//'\'')' \
		'awk "BEGIN {exit ($COVERAGE < 78)}"'; do
		printf '%s\n' "$go_job" | grep -Fq -- "$required" ||
			fail "Go Test + Coverage must isolate dispatcher without weakening race, shuffle, or coverage: $required"
	done

	dispatcher_commands=$(printf '%s\n' "$go_job" | grep -F 'go test ' | grep -F './pkg/dispatcher')
	[[ $(printf '%s\n' "$dispatcher_commands" | wc -l) -eq 2 ]] ||
		fail 'Go Test + Coverage must execute exactly two full dispatcher race commands'
	[[ $(printf '%s\n' "$dispatcher_commands" | grep -Fc -- '-timeout=20m') -eq 2 ]] ||
		fail 'each full dispatcher race command must use the explicit 20-minute package deadline'
	if printf '%s\n' "$dispatcher_commands" | grep -Eq -- '(^|[[:space:]])-(run|skip)(=|[[:space:]])'; then
		fail 'dispatcher race commands must not omit tests with -run or -skip'
	fi

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
		'      - name: Initialize isolated Go cache roots' \
		'cache_root="$RUNNER_TEMP/oro-go-cache"' \
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
	TestIncrementalMutationCapacityDeadline)
		TestIncrementalMutationCapacityDeadline
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
