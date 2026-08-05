#!/usr/bin/env bash
set -euo pipefail

: "${MUTATION_SOURCE_FILE:?}"
: "${MUTATION_FUNCTION_MATCH:?}"
: "${MUTATION_TEST_PATTERN?}"
: "${MUTATION_TEST_FILE:=}"
: "${MUTATION_EXEC_TIMEOUT:?}"
: "${MUTATION_PARALLEL_WORKERS:?}"
: "${MUTATION_EXEC_SCRIPT:=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/mutation_exec.sh}"
: "${GOCACHE:?}"
: "${GOTMPDIR:?}"

if [[ ! "$MUTATION_PARALLEL_WORKERS" =~ ^[1-9][0-9]*$ ]]; then
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

executor_root=$(mktemp -d "$GOTMPDIR/parallel-mutants.XXXXXX")
generation_log="$executor_root/generation.log"
generation_root=""
worker_pids=()
module_path=$(awk '$1 == "module" { print $2; exit }' go.mod)
if [[ -z "$module_path" ]]; then
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

cleanup_parallel_mutation() {
	local pid
	for pid in "${worker_pids[@]:-}"; do
		kill "$pid" 2>/dev/null || true
	done
	for pid in "${worker_pids[@]:-}"; do
		wait "$pid" 2>/dev/null || true
	done
	if [[ -n "$generation_root" && -d "$generation_root" ]]; then
		rm -rf -- "$generation_root"
	fi
	rm -rf -- "$executor_root"
}
trap cleanup_parallel_mutation EXIT
trap 'exit 124' HUP INT TERM

if ! go tool go-mutesting --debug --no-exec --do-not-remove-tmp-folder \
	"--match=$MUTATION_FUNCTION_MATCH" "$MUTATION_SOURCE_FILE" >"$generation_log" 2>&1; then
	cat "$generation_log"
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

generation_line=$(grep '^Save mutations into ' "$generation_log" | tail -1 || true)
generation_root=${generation_line#Save mutations into }
generation_root=${generation_root#\"}
generation_root=${generation_root%\"}
if [[ -z "$generation_root" || ! -d "$generation_root" ]]; then
	cat "$generation_log"
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

mutation_prefix="$generation_root/$MUTATION_SOURCE_FILE"
original_source="$mutation_prefix.original"
if [[ ! -f "$original_source" ]]; then
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	exit 2
fi

mapfile -t mutants < <(find "$(dirname "$mutation_prefix")" -maxdepth 1 -type f \
	-name "$(basename "$mutation_prefix").*" ! -name "$(basename "$mutation_prefix").original" | sort -V)
duplicate_count=$(grep -c ' is a duplicate, we ignore it$' "$generation_log" || true)
if ((${#mutants[@]} == 0)); then
	printf 'The mutation score is 0.000000 (0 passed, 0 failed, %d duplicated, 0 skipped, total is 0)\n' \
		"$duplicate_count"
	exit 0
fi

mkdir -p "$executor_root/results" "$executor_root/logs" "$executor_root/workers"
for ((worker = 0; worker < MUTATION_PARALLEL_WORKERS; worker++)); do
	worker_parent="$executor_root/workers/$worker"
	mkdir -p "$worker_parent" "$GOCACHE/parallel-$worker" "$GOTMPDIR/parallel-worker-$worker"
	cp -R . "$worker_parent/repo"
done

run_mutant_worker() {
	local worker="$1"
	local worker_repo="$executor_root/workers/$worker/repo"
	local worker_source="$worker_repo/$MUTATION_SOURCE_FILE"
	local worker_test_file=""
	if [[ -n "$MUTATION_TEST_FILE" ]]; then
		worker_test_file="$worker_repo/$MUTATION_TEST_FILE"
	fi
	local position mutant output result status
	for ((position = worker; position < ${#mutants[@]}; position += MUTATION_PARALLEL_WORKERS)); do
		mutant=${mutants[$position]}
		output="$executor_root/logs/$position.log"
		result="$executor_root/results/$position.tsv"
		status=0
		(
			cd "$worker_repo"
			GOCACHE="$GOCACHE/parallel-$worker" \
				GOTMPDIR="$GOTMPDIR/parallel-worker-$worker" \
				MUTATE_CHANGED="$mutant" \
				MUTATE_ORIGINAL="$worker_source" \
				MUTATE_PACKAGE="$module_path/$(dirname "$MUTATION_SOURCE_FILE")" \
				MUTATE_TIMEOUT="$MUTATION_EXEC_TIMEOUT" \
				MUTATION_TEST_PATTERN="$MUTATION_TEST_PATTERN" \
				MUTATION_TEST_FILE="$worker_test_file" \
				timeout "$MUTATION_EXEC_TIMEOUT" bash "$MUTATION_EXEC_SCRIPT"
		) >"$output" 2>&1 || status=$?
		printf '%d\t%d\t%s\n' "$position" "$status" "$mutant" >"$result"
	done
}

for ((worker = 0; worker < MUTATION_PARALLEL_WORKERS; worker++)); do
	run_mutant_worker "$worker" &
	worker_pids+=("$!")
done
worker_failure=0
for pid in "${worker_pids[@]}"; do
	wait "$pid" || worker_failure=1
done
worker_pids=()

passed=0
failed=0
skipped=0
infrastructure_failure=0
if ((worker_failure != 0)); then
	printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
	infrastructure_failure=1
fi
for ((position = 0; position < ${#mutants[@]}; position++)); do
	result="$executor_root/results/$position.tsv"
	output="$executor_root/logs/$position.log"
	if [[ ! -s "$result" || ! -f "$output" ]]; then
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		infrastructure_failure=1
		skipped=$((skipped + 1))
		continue
	fi
	IFS=$'\t' read -r recorded_position status mutant <"$result"
	if [[ "$recorded_position" != "$position" || "$mutant" != "${mutants[$position]}" ]]; then
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		infrastructure_failure=1
		skipped=$((skipped + 1))
		continue
	fi
	cat "$output"
	case "$status" in
	0)
		printf 'PASS %q\n' "$mutant"
		passed=$((passed + 1))
		;;
	1)
		printf 'FAIL %q\n' "$mutant"
		failed=$((failed + 1))
		;;
	*)
		if ! grep -Eq '^(ORO_MUTATION_EXEC_TIMEOUT|ORO_MUTATION_EXEC_FAILURE:)' "$output"; then
			printf 'ORO_MUTATION_EXEC_FAILURE:%d\n' "$status"
		fi
		printf 'SKIP %q\n' "$mutant"
		skipped=$((skipped + 1))
		infrastructure_failure=1
		;;
	esac
done

for ((worker = 0; worker < MUTATION_PARALLEL_WORKERS; worker++)); do
	if ! cmp -s "$original_source" "$executor_root/workers/$worker/repo/$MUTATION_SOURCE_FILE"; then
		printf 'ORO_MUTATION_EXEC_FAILURE:2\n'
		infrastructure_failure=1
	fi
done

total=$((passed + failed + skipped))
score=$(awk "BEGIN { if ($total == 0) print \"0.000000\"; else printf \"%.6f\", $passed / $total }")
printf 'The mutation score is %s (%d passed, %d failed, %d duplicated, %d skipped, total is %d)\n' \
	"$score" "$passed" "$failed" "$duplicate_count" "$skipped" "$total"
if ((infrastructure_failure != 0)); then
	exit 2
fi
