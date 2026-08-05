#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
readonly repo_root
readonly runner="$repo_root/scripts/quality_gate/mutation.sh"
tmp=""

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	return 1
}

new_fixture() {
	local fixture="$1"
	mkdir -p "$fixture/bin" "$fixture/pkg/example"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	git -C "$fixture" add pkg/example/value.go
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	printf 'package example\n\nfunc Value() int { return 2 }\n' >"$fixture/pkg/example/value.go"
	git -C "$fixture" add pkg/example/value.go
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

new_multi_fixture() {
	local fixture="$1"
	mkdir -p "$fixture/bin" "$fixture/pkg/example" "$fixture/pkg/other"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	printf 'package example\n\nfunc Value() int { return 1 }\n' >"$fixture/pkg/example/value.go"
	printf 'package other\n\nfunc Other() int { return 1 }\n' >"$fixture/pkg/other/other.go"
	git -C "$fixture" add pkg/example/value.go pkg/other/other.go
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	printf 'package example\n\nfunc Value() int { return 2 }\n' >"$fixture/pkg/example/value.go"
	printf 'package other\n\nfunc Other() int { return 2 }\n' >"$fixture/pkg/other/other.go"
	git -C "$fixture" add pkg/example/value.go pkg/other/other.go
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

new_targeted_fixture() {
	local fixture="$1"
	local expanded="${2:-false}"
	local target="${3:-hooks}"
	mkdir -p "$fixture/bin" "$fixture/cmd/oro" "$fixture/pkg/dispatcher"
	git -C "$fixture" init -q
	git -C "$fixture" config user.email mutation@example.test
	git -C "$fixture" config user.name mutation-test
	local base_parameters head_parameters package_name source_file source_prefix test_file function_name test_name
	local -a test_names
	base_parameters=""
	head_parameters=""
	source_prefix=""
	case "$target" in
	hooks)
		package_name=main
		source_file=cmd/oro/hooks.go
		test_file=cmd/oro/hooks_test.go
		function_name=isOroDistributedHook
		test_names=(TestIsOroDistributedHookRecognizesFastPrePush)
		;;
	init)
		package_name=main
		source_file=cmd/oro/cmd_init.go
		test_file=cmd/oro/cmd_init_test.go
		function_name=installAgentBranchGuard
		test_names=(TestInstallAgentBranchGuard)
		;;
	start)
		package_name=main
		source_file=cmd/oro/cmd_start.go
		test_file=cmd/oro/cmd_start_test.go
		function_name=hookPathsWouldLeak
		test_names=(
			TestHookPathsWouldLeak
			TestHookPathsWouldLeak_NonTmpdirSandboxRoot
			TestHookPathsWouldLeak_NonstandardGoTempRoot
			TestInstallCodexHookConfigRefusesLeakyHooks
		)
		;;
	scheduling)
		package_name=dispatcher
		source_file=pkg/dispatcher/scheduling.go
		test_file=pkg/dispatcher/scheduling_cursor_test.go
		function_name=advanceAssignedGeneralIdle
		base_parameters='idle []int'
		head_parameters='_ []int'
		source_prefix='func nextGeneralIdleIndex() int { return 0 }\n\n'
		test_names=(TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease)
		;;
	*) fail "unknown targeted fixture: $target" ;;
	esac
	printf 'package %s\n\n%bfunc %s(%s) bool { return false }\n' \
		"$package_name" "$source_prefix" "$function_name" "$base_parameters" >"$fixture/$source_file"
	if [[ "$expanded" = true ]]; then
		printf '\nfunc anotherHookDecision() bool { return false }\n' >>"$fixture/$source_file"
	fi
	printf 'package %s\n' "$package_name" >"$fixture/$test_file"
	for test_name in "${test_names[@]}"; do
		printf '\nfunc %s() {}\n' "$test_name" >>"$fixture/$test_file"
	done
	git -C "$fixture" add "$source_file" "$test_file"
	git -C "$fixture" commit -qm base
	base=$(git -C "$fixture" rev-parse HEAD)
	printf 'package %s\n\n%bfunc %s(%s) bool { return true }\n' \
		"$package_name" "$source_prefix" "$function_name" "$head_parameters" >"$fixture/$source_file"
	if [[ "$expanded" = true ]]; then
		printf '\nfunc anotherHookDecision() bool { return true }\n' >>"$fixture/$source_file"
	fi
	git -C "$fixture" add "$source_file"
	git -C "$fixture" commit -qm head
	head=$(git -C "$fixture" rev-parse HEAD)
	printf '%s\n%s\n' "$base" "$head"
}

write_fake_go() {
	local path="$1"
	cat >"$path" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "$1" = test ]]; then
	printf '%s\n' "$*" >>"${MUTATION_LIST_TRACE:?}"
	if [[ "$MUTATION_FIXTURE" != targeted-list-miss ]]; then
		case "$*" in
		*TestInstallAgentBranchGuard*) printf 'TestInstallAgentBranchGuard\n' ;;
		*TestHookPathsWouldLeak*) printf 'TestHookPathsWouldLeak\nTestHookPathsWouldLeak_NonTmpdirSandboxRoot\nTestHookPathsWouldLeak_NonstandardGoTempRoot\nTestInstallCodexHookConfigRefusesLeakyHooks\n' ;;
		*TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease*) printf 'TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease\n' ;;
		*) printf 'TestIsOroDistributedHookRecognizesFastPrePush\n' ;;
		esac
	fi
	exit 0
fi

if [[ "$1" != tool || "$2" != go-mutesting ]]; then
	echo "unexpected go invocation: $*" >&2
	exit 64
fi

case "${MUTATION_FIXTURE:?}" in
pass)
	printf 'The mutation score is 0.90\ntotal is 10\n'
	;;
reversed)
	printf 'total is 10\nThe mutation score is 0.90\n'
	;;
intervening)
	printf 'The mutation score is 0.90\nmutation detail\ntotal is 10\n'
	;;
annotated)
	printf 'The mutation score is 1.000000 (38 passed, 0 failed, 2 duplicated, 0 skipped, total is 38)\n'
	;;
below)
	printf 'The mutation score is 0.40\ntotal is 10\n'
	;;
crash)
	printf 'go-mutesting crashed\n' >&2
	exit 23
	;;
zero)
	printf 'The mutation score is 0.00\ntotal is 0\n'
	exit 23
	;;
zero-clean)
	printf 'The mutation score is 0.00\ntotal is 0\n'
	;;
timeout)
	printf 'mutation timed out\n' >&2
	exit 124
	;;
malformed)
	printf 'not a mutation report\n'
	;;
malformed-annotated)
	printf 'The mutation score is 1.000000 (38 passed, total is nope)\n'
	;;
aggregate | aggregate-below | aggregate-zero | shard-timeout)
	target=${*: -1}
	match=""
	for arg in "$@"; do
		case "$arg" in
		--match=*) match=${arg#--match=} ;;
		esac
	done
	printf '%s\t%s\t%s\t%s\n' "$target" "$PWD" "${GOCACHE:-}" "$match" >>"${MUTATION_TRACE:?}"
	case "$target" in
	*pkg/example/value.go)
		sleep 0.2
		if [[ "$MUTATION_FIXTURE" = aggregate-zero ]]; then
			printf 'The mutation score is 0.000000 (0 passed, 0 failed, 0 duplicated, 0 skipped, total is 0)\n'
		elif [[ "$MUTATION_FIXTURE" = aggregate-below ]]; then
			printf 'The mutation score is 0.500000 (5 passed, 5 failed, 1 duplicated, 0 skipped, total is 10)\n'
		else
			printf 'The mutation score is 0.900000 (9 passed, 1 failed, 1 duplicated, 0 skipped, total is 10)\n'
		fi
		;;
	*pkg/other/other.go)
		if [[ "$MUTATION_FIXTURE" = shard-timeout ]]; then
			printf 'mutation timed out\n' >&2
			exit 124
		elif [[ "$MUTATION_FIXTURE" = aggregate-zero ]]; then
			printf 'The mutation score is 0.900000 (9 passed, 1 failed, 2 duplicated, 0 skipped, total is 10)\n'
		elif [[ "$MUTATION_FIXTURE" = aggregate-below ]]; then
			printf 'The mutation score is 0.900000 (9 passed, 1 failed, 2 duplicated, 0 skipped, total is 10)\n'
		else
			printf 'The mutation score is 0.600000 (6 passed, 4 failed, 2 duplicated, 0 skipped, total is 10)\n'
		fi
		;;
	*)
		echo "unexpected mutation target: $target" >&2
		exit 65
		;;
	esac
	;;
targeted | targeted-fallback | targeted-list-miss | targeted-timeout)
	printf '%s\n' "$*" >"${MUTATION_ARGS_TRACE:?}"
	if [[ "$MUTATION_FIXTURE" = targeted-timeout ]]; then
		printf 'ORO_MUTATION_EXEC_TIMEOUT\n'
		printf 'UNKOWN exit code for targeted mutation test timeout\n'
	fi
	printf 'The mutation score is 1.000000 (1 passed, 0 failed, 0 duplicated, 0 skipped, total is 1)\n'
	;;
*)
	echo "unknown mutation fixture: $MUTATION_FIXTURE" >&2
	exit 64
	;;
esac
EOF
	chmod +x "$path"
}

run_targeted_fixture() {
	local fixture="$1"
	local outcome="$2"
	local expected_status="$3"
	local expected_exit="$4"
	local expanded="${5:-false}"
	local target="${6:-hooks}"
	local base head evidence status args_trace list_trace
	mapfile -t refs < <(new_targeted_fixture "$fixture" "$expanded" "$target")
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	args_trace="$fixture/mutation-args.txt"
	list_trace="$fixture/mutation-list.txt"
	write_fake_go "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE="$outcome" \
			MUTATION_ARGS_TRACE="$args_trace" MUTATION_LIST_TRACE="$list_trace" \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence" \
			>"$fixture/runner.log" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = "$expected_exit" ]] || fail "$outcome exit = $status, want $expected_exit"
	jq -e --arg status "$expected_status" '.conclusion == $status' "$evidence" >/dev/null ||
		fail "$outcome did not preserve its expected conclusion"
	printf '%s\n' "$evidence"
}

run_multi_fixture() {
	local fixture="$1"
	local outcome="$2"
	local expected_status="$3"
	local expected_exit="$4"
	local base head evidence status trace
	mapfile -t refs < <(new_multi_fixture "$fixture")
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	trace="$fixture/mutation-trace.tsv"
	write_fake_go "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE="$outcome" MUTATION_TRACE="$trace" \
			MUTATION_MAX_WORKERS=2 MUTATION_FILE_TIMEOUT_SECONDS=5 \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence" \
			>"$fixture/runner.log" 2>&1
	)
	status=$?
	set -e
	[[ "$status" = "$expected_exit" ]] || fail "$outcome exit = $status, want $expected_exit"
	[[ -s "$evidence" ]] || fail "$outcome did not write evidence"
	jq -e \
		--arg base "$base" \
		--arg head "$head" \
		--arg status "$expected_status" \
		'.base == $base and .head == $head and .conclusion == $status and
		 .changed_files == ["pkg/example/value.go", "pkg/other/other.go"] and
		 [.shards[].file] == .changed_files' \
		"$evidence" >/dev/null || fail "$outcome evidence is missing deterministic shard identity"
	printf '%s\n' "$evidence"
}

TestStrictIncrementalMutationShards() {
	local evidence trace fixture

	fixture="$tmp/aggregate"
	evidence=$(run_multi_fixture "$fixture" aggregate pass 0)
	jq -e \
		'.mutation_exit_code == 0 and .score == 0.75 and .total == 20 and
		 [.shards[] | {conclusion, exit_code, passed, failed, duplicated, skipped, total}] ==
		 [{conclusion:"completed", exit_code:0, passed:9, failed:1, duplicated:1, skipped:0, total:10},
		  {conclusion:"completed", exit_code:0, passed:6, failed:4, duplicated:2, skipped:0, total:10}]' \
		"$evidence" >/dev/null || fail 'weighted shard aggregation did not preserve strict mutation counts'
	trace="$fixture/mutation-trace.tsv"
	[[ "$(wc -l <"$trace" | tr -d ' ')" = 2 ]] || fail 'each changed file must run exactly once'
	[[ "$(cut -f2 "$trace" | sort -u | wc -l | tr -d ' ')" = 2 ]] || fail 'mutation shards must use isolated worktrees'
	[[ "$(cut -f3 "$trace" | sort -u | wc -l | tr -d ' ')" = 2 ]] || fail 'mutation shards must use isolated Go build caches'
	! grep -q $'\t\t' "$trace" || fail 'mutation shard GOCACHE must be non-empty'
	[[ "$(cut -f4 "$trace" | sort | tr '\n' ' ')" = "^(Other)$ ^(Value)$ " ]] ||
		fail 'mutation shards must target only functions touched in each changed file'
	jq -e '[.shards[].match] == ["^(Value)$", "^(Other)$"]' "$evidence" >/dev/null ||
		fail 'mutation evidence must preserve each deterministic touched-function match'

	evidence=$(run_multi_fixture "$tmp/below" aggregate-below deterministic_failure 1)
	jq -e '.mutation_exit_code == 0 and .score == 0.7 and .total == 20' "$evidence" >/dev/null ||
		fail 'below-threshold aggregate was not kept distinct from infrastructure failure'

	evidence=$(run_multi_fixture "$tmp/timeout" shard-timeout infrastructure_failure 2)
	jq -e \
		'.mutation_exit_code == 124 and .score == null and .total == 0 and
		 [.shards[].conclusion] == ["completed", "infrastructure_failure"] and
		 .shards[1].exit_code == 124' \
		"$evidence" >/dev/null || fail 'per-file timeout did not preserve completed and infrastructure shard evidence'

	evidence=$(run_multi_fixture "$tmp/zero-shard" aggregate-zero pass 0)
	jq -e \
		'.mutation_exit_code == 0 and .score == 0.9 and .total == 10 and
		 .shards[0].conclusion == "completed" and .shards[0].reason == "no mutants generated" and
		 .shards[0].score == null and .shards[0].total == 0 and
		 .shards[1].conclusion == "completed" and .shards[1].total == 10' \
		"$evidence" >/dev/null || fail 'one legitimate zero-mutant shard must not erase completed strict score evidence'
}

TestTargetedMutationScope() {
	local evidence fixture args_trace list_trace scheduling_pattern start_pattern
	fixture="$tmp/targeted"
	evidence=$(run_targeted_fixture "$fixture" targeted pass 0)
	args_trace="$fixture/mutation-args.txt"
	list_trace="$fixture/mutation-list.txt"
	grep -q -- '--exec=bash scripts/quality_gate/mutation_exec.sh' "$args_trace" ||
		fail 'bounded hook mutations must use the checked-in targeted exec boundary'
	grep -q -- '-list \^TestIsOroDistributedHook ./cmd/oro' "$list_trace" ||
		fail 'targeted mutation scope must preflight the exact package test pattern'
	jq -e '.shards[0].match == "^(isOroDistributedHook)$" and .shards[0].test_pattern == "^TestIsOroDistributedHook"' \
		"$evidence" >/dev/null || fail 'targeted mutation evidence must preserve function and test scope'

	evidence=$(run_targeted_fixture "$tmp/targeted-init" targeted pass 0 false init)
	grep -q -- '-list \^TestInstallAgentBranchGuard ./cmd/oro' "$tmp/targeted-init/mutation-list.txt" ||
		fail 'init guard mutations must preflight their exact direct test pattern'
	jq -e '.shards[0].match == "^(installAgentBranchGuard)$" and .shards[0].test_pattern == "^TestInstallAgentBranchGuard"' \
		"$evidence" >/dev/null || fail 'init guard mutation evidence must preserve function and test scope'

	start_pattern='^(TestHookPathsWouldLeak|TestHookPathsWouldLeak_NonTmpdirSandboxRoot|TestHookPathsWouldLeak_NonstandardGoTempRoot|TestInstallCodexHookConfigRefusesLeakyHooks)$'
	evidence=$(run_targeted_fixture "$tmp/targeted-start" targeted pass 0 false start)
	grep -Fq -- "-list $start_pattern ./cmd/oro" "$tmp/targeted-start/mutation-list.txt" ||
		fail 'start hook leak mutations must preflight the exact focused safety tests'
	jq -e --arg pattern "$start_pattern" \
		'.shards[0].match == "^(hookPathsWouldLeak)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'start hook leak mutation evidence must preserve function and focused test scope'

	scheduling_pattern='^TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease$'
	evidence=$(run_targeted_fixture "$tmp/targeted-scheduling" targeted pass 0 false scheduling)
	grep -Fq -- "-list $scheduling_pattern ./pkg/dispatcher" "$tmp/targeted-scheduling/mutation-list.txt" ||
		fail 'dispatcher scheduling mutations must preflight the exact idle-cursor regression test'
	jq -e --arg pattern "$scheduling_pattern" \
		'.shards[0].match == "^(advanceAssignedGeneralIdle)$" and .shards[0].test_pattern == $pattern' \
		"$evidence" >/dev/null || fail 'dispatcher scheduling mutation evidence must preserve function and exact regression test scope'

	evidence=$(run_targeted_fixture "$tmp/targeted-expanded" targeted-fallback pass 0 true)
	! grep -q -- '--exec=' "$tmp/targeted-expanded/mutation-args.txt" ||
		fail 'an expanded mutation surface must fall back to the full package instead of silently narrowing tests'
	jq -e '.shards[0].match == "^(anotherHookDecision|isOroDistributedHook)$" and .shards[0].test_pattern == ""' \
		"$evidence" >/dev/null || fail 'full-package fallback evidence must preserve the expanded function surface'

	evidence=$(run_targeted_fixture "$tmp/targeted-list-miss" targeted-list-miss infrastructure_failure 2)
	jq -e '.shards[0].reason == "targeted mutation test pattern matched no tests"' "$evidence" >/dev/null ||
		fail 'an empty targeted test scope must be an infrastructure failure'

	evidence=$(run_targeted_fixture "$tmp/targeted-timeout" targeted-timeout infrastructure_failure 2)
	jq -e '.mutation_exit_code == 124 and .shards[0].exit_code == 124' "$evidence" >/dev/null ||
		fail 'a targeted mutation test timeout must remain an infrastructure failure'
}

run_fixture() {
	local fixture="$1"
	local outcome="$2"
	local expected_status="$3"
	local expected_exit="$4"
	local base head evidence status
	mapfile -t refs < <(new_fixture "$fixture")
	base=${refs[0]}
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"
	write_fake_go "$fixture/bin/go"

	set +e
	(
		cd "$fixture"
		PATH="$fixture/bin:$PATH" MUTATION_FIXTURE="$outcome" \
			bash "$runner" --base "$base" --head "$head" --evidence "$evidence"
	)
	status=$?
	set -e
	[[ "$status" = "$expected_exit" ]] || fail "$outcome exit = $status, want $expected_exit"
	[[ -s "$evidence" ]] || fail "$outcome did not write evidence"
	jq -e \
		--arg base "$base" \
		--arg head "$head" \
		--arg status "$expected_status" \
		'.base == $base and .head == $head and .conclusion == $status and .changed_files == ["pkg/example/value.go"]' \
		"$evidence" >/dev/null || fail "$outcome evidence is missing exact refs, conclusion, or changed scope"
}

run_missing_base_fixture() {
	local fixture="$1"
	local head evidence status
	mapfile -t refs < <(new_fixture "$fixture")
	head=${refs[1]}
	evidence="$fixture/mutation-evidence.json"

	set +e
	(
		cd "$fixture"
		bash "$runner" --base missing-base --head "$head" --evidence "$evidence"
	)
	status=$?
	set -e
	[[ "$status" = 2 ]] || fail "missing base exit = $status, want 2"
	jq -e --arg head "$head" \
		'.base == "missing-base" and .head == $head and .conclusion == "infrastructure_failure"' \
		"$evidence" >/dev/null || fail 'missing base did not emit infrastructure evidence'
}

TestStrictIncrementalMutation() {
	local file_timeout_seconds incident_file_count job_timeout_minutes minimum_timeout_minutes mutation_batches workers
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' RETURN

	run_fixture "$tmp/pass" pass pass 0
	run_fixture "$tmp/intervening" intervening infrastructure_failure 2
	run_fixture "$tmp/reversed" reversed infrastructure_failure 2
	run_fixture "$tmp/below" below deterministic_failure 1
	run_fixture "$tmp/crash" crash infrastructure_failure 2
	run_fixture "$tmp/zero" zero infrastructure_failure 2
	run_fixture "$tmp/zero-clean" zero-clean infrastructure_failure 2
	run_fixture "$tmp/timeout" timeout infrastructure_failure 2
	run_fixture "$tmp/malformed" malformed infrastructure_failure 2
	run_fixture "$tmp/malformed-annotated" malformed-annotated infrastructure_failure 2
	jq -e '.score == null and .total == 0' "$tmp/malformed-annotated/mutation-evidence.json" >/dev/null ||
		fail 'malformed annotated output was accepted as mutation evidence'
	run_fixture "$tmp/annotated" annotated pass 0
	jq -e '.score == 1 and .total == 38' "$tmp/annotated/mutation-evidence.json" >/dev/null ||
		fail 'annotated output did not preserve its score and total'
	run_missing_base_fixture "$tmp/missing-base"
	TestStrictIncrementalMutationShards
	TestTargetedMutationScope

	awk '
		/^  incremental-mutation:$/ { in_job = 1; next }
		in_job && /^  [a-z0-9][a-z0-9-]*:$/ { exit }
		in_job { print }
	' "$repo_root/.github/workflows/ci.yml" >"$tmp/incremental-mutation.yml"
	grep -q 'scripts/quality_gate/mutation.sh' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must run the strict mutation runner'
	grep -q 'actions/upload-artifact' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must upload its JSON evidence artifact'
	grep -q 'if-no-files-found: error' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation artifact loss must fail the job'
	grep -q 'MUTATION_MAX_WORKERS: 2' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation shard concurrency must match hosted runner capacity'
	grep -q 'MUTATION_FILE_TIMEOUT_SECONDS: 240' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must declare a per-file deadline'
	job_timeout_minutes=$(awk '/^[[:space:]]+timeout-minutes:/ { print $2; exit }' "$tmp/incremental-mutation.yml")
	workers=$(awk '/MUTATION_MAX_WORKERS:/ { print $2; exit }' "$tmp/incremental-mutation.yml")
	file_timeout_seconds=$(awk '/MUTATION_FILE_TIMEOUT_SECONDS:/ { print $2; exit }' "$tmp/incremental-mutation.yml")
	incident_file_count=24
	mutation_batches=$(((incident_file_count + workers - 1) / workers))
	minimum_timeout_minutes=$(((\
		mutation_batches * file_timeout_seconds + 10 * 60 + 59) / \
		60))
	[[ "$job_timeout_minutes" =~ ^[1-9][0-9]*$ ]] ||
		fail 'incremental-mutation job must have a numeric bounded outer deadline'
	((job_timeout_minutes >= minimum_timeout_minutes)) ||
		fail "incremental-mutation outer deadline must cover 24 shards at declared capacity plus 10 minutes overhead"
}

main() {
	case "${1:-}" in
	'' | TestStrictIncrementalMutation)
		TestStrictIncrementalMutation
		;;
	*)
		fail "unknown test $1"
		;;
	esac
}

main "$@"
