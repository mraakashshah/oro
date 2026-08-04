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

write_fake_go() {
	local path="$1"
	cat >"$path" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

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
aggregate | aggregate-below | shard-timeout)
	target=${*: -1}
	printf '%s\t%s\t%s\n' "$target" "$PWD" "${GOCACHE:-}" >>"${MUTATION_TRACE:?}"
	case "$target" in
	*pkg/example/value.go)
		sleep 0.2
		if [[ "$MUTATION_FIXTURE" = aggregate-below ]]; then
			printf 'The mutation score is 0.500000 (5 passed, 5 failed, 1 duplicated, 0 skipped, total is 10)\n'
		else
			printf 'The mutation score is 0.900000 (9 passed, 1 failed, 1 duplicated, 0 skipped, total is 10)\n'
		fi
		;;
	*pkg/other/other.go)
		if [[ "$MUTATION_FIXTURE" = shard-timeout ]]; then
			printf 'mutation timed out\n' >&2
			exit 124
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
*)
	echo "unknown mutation fixture: $MUTATION_FIXTURE" >&2
	exit 64
	;;
esac
EOF
	chmod +x "$path"
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

	evidence=$(run_multi_fixture "$tmp/below" aggregate-below deterministic_failure 1)
	jq -e '.mutation_exit_code == 0 and .score == 0.7 and .total == 20' "$evidence" >/dev/null ||
		fail 'below-threshold aggregate was not kept distinct from infrastructure failure'

	evidence=$(run_multi_fixture "$tmp/timeout" shard-timeout infrastructure_failure 2)
	jq -e \
		'.mutation_exit_code == 124 and .score == null and .total == 0 and
		 [.shards[].conclusion] == ["completed", "infrastructure_failure"] and
		 .shards[1].exit_code == 124' \
		"$evidence" >/dev/null || fail 'per-file timeout did not preserve completed and infrastructure shard evidence'
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
	grep -q 'timeout-minutes: 35' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must have a bounded outer deadline'
	grep -q 'MUTATION_MAX_WORKERS: 4' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must declare bounded shard concurrency'
	grep -q 'MUTATION_FILE_TIMEOUT_SECONDS: 240' "$tmp/incremental-mutation.yml" ||
		fail 'incremental-mutation job must declare a per-file deadline'
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
